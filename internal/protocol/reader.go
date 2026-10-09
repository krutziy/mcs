// Package protocol 实现 Minecraft Java 版协议的线格式（wire format）编解码：
// VarInt、字符串、位置、角度、UUID、NBT 等基础类型。
package protocol

import (
	"encoding/binary"
	"errors"
	"math"
	"unicode/utf8"
)

// ErrOverrun 表示读入了不完整或超长的数据。
var ErrOverrun = errors.New("protocol: 数据不足或超长")

// nMaxLength 是协议字符串的字节长度上限。
const nMaxLength = 32767

// Reader 是带粘滞错误的字节读取器：第一个错误会被记录，
// 之后所有读取直接返回零值，调用方只需在最后检查 Err 字段。
type Reader struct {
	data []byte
	off  int
	Err  error
}

// NewReader 基于已有字节构造读取器。
func NewReader(data []byte) *Reader {
	return &Reader{data: data}
}

// Remaining 返回尚未读取的字节数。
func (r *Reader) Remaining() int { return len(r.data) - r.off }

func (r *Reader) take(n int) []byte {
	if r.Err != nil {
		return nil
	}
	if n < 0 || r.off+n > len(r.data) {
		r.Err = ErrOverrun
		return nil
	}
	b := r.data[r.off : r.off+n]
	r.off += n
	return b
}

// Bool 读取 1 字节布尔值。
func (r *Reader) Bool() bool {
	b := r.take(1)
	return b != nil && b[0] != 0
}

// Uint8 读取单字节。
func (r *Reader) Uint8() uint8 {
	b := r.take(1)
	if b == nil {
		return 0
	}
	return b[0]
}

// Int8 读取有符号单字节。
func (r *Reader) Int8() int8 { return int8(r.Uint8()) }

// Uint16 读取大端无符号短整数（Java 平台字节序为大端）。
func (r *Reader) Uint16() uint16 {
	b := r.take(2)
	if b == nil {
		return 0
	}
	return binary.BigEndian.Uint16(b)
}

// Int16 读取大端有符号短整数。
func (r *Reader) Int16() int16 { return int16(r.Uint16()) }

// Int32 读取大端有符号整数。
func (r *Reader) Int32() int32 {
	b := r.take(4)
	if b == nil {
		return 0
	}
	return int32(binary.BigEndian.Uint32(b))
}

// Int64 读取大端有符号长整数。
func (r *Reader) Int64() int64 {
	b := r.take(8)
	if b == nil {
		return 0
	}
	return int64(binary.BigEndian.Uint64(b))
}

// Float 读取大端单精度浮点。
func (r *Reader) Float() float32 { return math.Float32frombits(uint32(r.Int32())) }

// Double 读取大端双精度浮点。
func (r *Reader) Double() float64 { return math.Float64frombits(uint64(r.Int64())) }

// VarInt 读取协议变长整数（最多 5 字节，每字节低 7 位有效，高位为继续标志）。
func (r *Reader) VarInt() int32 {
	var result uint32
	for i := 0; i < 5; i++ {
		b := r.Uint8()
		result |= uint32(b&0x7F) << (7 * i)
		if b&0x80 == 0 {
			return int32(result)
		}
	}
	r.Err = ErrOverrun
	return 0
}

// VarLong 读取协议变长长整数（最多 10 字节）。
func (r *Reader) VarLong() int64 {
	var result uint64
	for i := 0; i < 10; i++ {
		b := r.Uint8()
		result |= uint64(b&0x7F) << (7 * i)
		if b&0x80 == 0 {
			return int64(result)
		}
	}
	r.Err = ErrOverrun
	return 0
}

// String 读取协议字符串（VarInt 字节长度 + UTF-8 内容，上限 32767 字节）。
func (r *Reader) String() string {
	n := int(r.VarInt())
	if r.Err != nil {
		return ""
	}
	if n < 0 || n > 4*nMaxLength {
		r.Err = ErrOverrun
		return ""
	}
	b := r.take(n)
	if b == nil {
		return ""
	}
	if !utf8.Valid(b) {
		r.Err = ErrOverrun
		return ""
	}
	return string(b)
}

// UUID 读取 128 位 UUID。
func (r *Reader) UUID() (u UUID) {
	b := r.take(16)
	if b == nil {
		return
	}
	copy(u[:], b)
	return u
}

// Position 读取方块坐标（1.14+ 压缩格式：x 占高 26 位、z 占中 26 位、y 占低 12 位）。
func (r *Reader) Position() Position {
	v := uint64(r.Int64())
	return Position{
		X: SignExtend(v>>38, 26),
		Z: SignExtend((v>>12)&0x3FF_FFFF, 26),
		Y: SignExtend(v&0xFFF, 12),
	}
}

// ByteArray 读取 VarInt 长度 + 字节数组。
func (r *Reader) ByteArray() []byte {
	n := int(r.VarInt())
	if r.Err != nil {
		return nil
	}
	return r.take(n)
}

// FixedBytes 读取固定长度字节。
func (r *Reader) FixedBytes(n int) []byte { return r.take(n) }

// Rest 返回剩余全部字节。
func (r *Reader) Rest() []byte {
	if r.Err != nil {
		return nil
	}
	b := r.data[r.off:]
	r.off = len(r.data)
	return b
}

// SignExtend 将 bits 位宽的无符号数按二进制补码符号扩展为有符号整数。
func SignExtend(v uint64, bits uint) int32 {
	v &= (1 << bits) - 1
	if v&(1<<(bits-1)) != 0 {
		v -= 1 << bits
	}
	return int32(v)
}
