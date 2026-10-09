// Package conn 实现连接层：TCP 上的包分帧（VarInt 长度前缀）、
// zlib 压缩协商后的解压/压缩，以及协议状态机（握手→状态/登录→配置→游戏）。
package conn

import (
	"bufio"
	"bytes"
	"compress/zlib"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"time"

	"mcserver/internal/protocol"
)

// State 是协议状态机的一个状态。
type State int

const (
	StateHandshaking State = iota
	StateStatus
	StateLogin
	StateConfiguration
	StatePlay
)

// maxPacketSize 是单个包（解压后）的大小上限。
const maxPacketSize = 8 << 20

// ErrPacketTooLarge 表示帧长度超出上限，通常意味着流已错位，必须断开。
var ErrPacketTooLarge = errors.New("conn: 包大小超限")

// ErrClosed 表示连接已关闭。
var ErrClosed = errors.New("conn: 连接已关闭")

// Conn 封装一条客户端 TCP 连接。
// 读端由会话 goroutine 独占；写端内部有锁，可从任意 goroutine 调用。
type Conn struct {
	tcp  net.Conn
	rbuf *bufio.Reader

	writeMu     sync.Mutex
	wbuf        *bufio.Writer
	compression int32 // -1 表示未启用压缩

	state State
}

// New 包装一条 TCP 连接。
func New(tcp net.Conn) *Conn {
	return &Conn{
		tcp:         tcp,
		rbuf:        bufio.NewReaderSize(tcp, 64*1024),
		wbuf:        bufio.NewWriterSize(tcp, 128*1024),
		compression: -1,
		state:       StateHandshaking,
	}
}

// State 返回当前协议状态。
func (c *Conn) State() State { return c.state }

// SetState 切换协议状态。
func (c *Conn) SetState(s State) { c.state = s }

// SetCompression 启用压缩（阈值取 threshold，负数表示关闭）。
// 必须在发送 Set Compression 包的同一时刻调用才能保持流一致。
func (c *Conn) SetCompression(threshold int32) {
	c.writeMu.Lock()
	c.compression = threshold
	c.writeMu.Unlock()
}

// RemoteAddr 返回对端地址。
func (c *Conn) RemoteAddr() net.Addr { return c.tcp.RemoteAddr() }

// SetReadDeadline 设置读超时（用于空闲检测）。
func (c *Conn) SetReadDeadline(t time.Time) error { return c.tcp.SetReadDeadline(t) }

// ReadPacket 阻塞读取一个包，返回包 ID 与载荷读取器。
func (c *Conn) ReadPacket() (id int32, body *protocol.Reader, err error) {
	length, err := readVarInt(c.rbuf)
	if err != nil {
		return 0, nil, err
	}
	if length < 0 || int64(length) > maxPacketSize {
		return 0, nil, ErrPacketTooLarge
	}
	frame := make([]byte, length)
	if _, err := io.ReadFull(c.rbuf, frame); err != nil {
		return 0, nil, err
	}

	c.writeMu.Lock()
	compressed := c.compression >= 0
	c.writeMu.Unlock()

	var payload []byte
	if compressed {
		r := protocol.NewReader(frame)
		dataLen := r.VarInt()
		if r.Err != nil {
			return 0, nil, r.Err
		}
		if dataLen != 0 {
			if dataLen < 0 || int64(dataLen) > maxPacketSize {
				return 0, nil, ErrPacketTooLarge
			}
			zr, err := zlib.NewReader(bytes.NewReader(r.Rest()))
			if err != nil {
				return 0, nil, err
			}
			payload, err = io.ReadAll(io.LimitReader(zr, int64(dataLen)))
			if err != nil {
				return 0, nil, err
			}
			if len(payload) != int(dataLen) {
				return 0, nil, fmt.Errorf("conn: 解压长度不符 %d != %d", len(payload), dataLen)
			}
		} else {
			payload = r.Rest()
		}
	} else {
		payload = frame
	}

	pr := protocol.NewReader(payload)
	id = pr.VarInt()
	if pr.Err != nil {
		return 0, nil, pr.Err
	}
	return id, pr, nil
}

// WritePacket 构建并立即发送一个包。
func (c *Conn) WritePacket(id int32, build func(w *protocol.Writer)) error {
	w := protocol.NewWriter()
	w.VarInt(id)
	build(w)
	if w.Err != nil {
		return w.Err
	}
	return c.writeFrame(w.Bytes())
}

// WritePacketBuffered 构建一个包但不立即刷新（用于批量发送），
// 调用方在一批结束后调用 Flush。
func (c *Conn) WritePacketBuffered(id int32, build func(w *protocol.Writer)) error {
	w := protocol.NewWriter()
	w.VarInt(id)
	build(w)
	if w.Err != nil {
		return w.Err
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return writeFrameLocked(c.wbuf, c.compression, w.Bytes())
}

// Flush 把缓冲中的包全部写出。
func (c *Conn) Flush() error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return c.wbuf.Flush()
}

func (c *Conn) writeFrame(payload []byte) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if err := writeFrameLocked(c.wbuf, c.compression, payload); err != nil {
		return err
	}
	return c.wbuf.Flush()
}

func writeFrameLocked(out *bufio.Writer, compression int32, payload []byte) error {
	if compression < 0 {
		writeVarInt(out, int32(len(payload)))
		_, err := out.Write(payload)
		return err
	}
	// 小于阈值的包不压缩，但解压后长度字段必须写 0
	if int32(len(payload)) < compression {
		writeVarInt(out, int32(len(payload)+1))
		writeVarInt(out, 0)
		_, err := out.Write(payload)
		return err
	}
	var buf bytes.Buffer
	zw := zlib.NewWriter(&buf)
	if _, err := zw.Write(payload); err != nil {
		return err
	}
	if err := zw.Close(); err != nil {
		return err
	}
	writeVarInt(out, int32(buf.Len()+varIntLen(int32(len(payload)))))
	writeVarInt(out, int32(len(payload)))
	_, err := out.Write(buf.Bytes())
	return err
}

func readVarInt(r io.ByteReader) (int32, error) {
	var result uint32
	for i := 0; i < 5; i++ {
		b, err := r.ReadByte()
		if err != nil {
			return 0, err
		}
		result |= uint32(b&0x7F) << (7 * i)
		if b&0x80 == 0 {
			return int32(result), nil
		}
	}
	return 0, ErrPacketTooLarge
}

func writeVarInt(out *bufio.Writer, v int32) {
	u := uint32(v)
	var buf [5]byte
	n := 0
	for {
		if u&^0x7F == 0 {
			buf[n] = byte(u)
			n++
			break
		}
		buf[n] = byte(u&0x7F) | 0x80
		u >>= 7
		n++
	}
	_, _ = out.Write(buf[:n])
}

func varIntLen(v int32) int {
	u := uint32(v)
	n := 1
	for u&^0x7F != 0 {
		u >>= 7
		n++
	}
	return n
}

// Close 关闭底层连接。
func (c *Conn) Close() error { return c.tcp.Close() }

