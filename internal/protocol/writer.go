package protocol

import (
	"encoding/binary"
	"math"
)

// Writer 是带粘滞错误的字节写入器，底层为缓冲区。
type Writer struct {
	buf []byte
	Err error
}

// NewWriter 创建空写入器。
func NewWriter() *Writer { return &Writer{} }

// Bytes 返回已写入的全部字节。
func (w *Writer) Bytes() []byte { return w.buf }

// Len 返回已写入的字节数。
func (w *Writer) Len() int { return len(w.buf) }

func (w *Writer) raw(b []byte) {
	if w.Err != nil {
		return
	}
	w.buf = append(w.buf, b...)
}

// Bool 写入 1 字节布尔值。
func (w *Writer) Bool(v bool) {
	if v {
		w.raw([]byte{1})
	} else {
		w.raw([]byte{0})
	}
}

// Uint8 写入单字节。
func (w *Writer) Uint8(v uint8) { w.raw([]byte{v}) }

// Int8 写入有符号单字节。
func (w *Writer) Int8(v int8) { w.Uint8(uint8(v)) }

// Uint16 写入大端无符号短整数。
func (w *Writer) Uint16(v uint16) {
	w.raw([]byte{byte(v >> 8), byte(v)})
}

// Int16 写入大端有符号短整数。
func (w *Writer) Int16(v int16) { w.Uint16(uint16(v)) }

// Int32 写入大端有符号整数。
func (w *Writer) Int32(v int32) {
	var b [4]byte
	binary.BigEndian.PutUint32(b[:], uint32(v))
	w.raw(b[:])
}

// Int64 写入大端有符号长整数。
func (w *Writer) Int64(v int64) {
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], uint64(v))
	w.raw(b[:])
}

// Float 写入大端单精度浮点。
func (w *Writer) Float(v float32) { w.Int32(int32(math.Float32bits(v))) }

// Double 写入大端双精度浮点。
func (w *Writer) Double(v float64) { w.Int64(int64(math.Float64bits(v))) }

// VarInt 写入协议变长整数。
func (w *Writer) VarInt(v int32) {
	u := uint32(v)
	for {
		if u&^0x7F == 0 {
			w.Uint8(uint8(u))
			return
		}
		w.Uint8(uint8(u&0x7F) | 0x80)
		u >>= 7
	}
}

// VarLong 写入协议变长长整数。
func (w *Writer) VarLong(v int64) {
	u := uint64(v)
	for {
		if u&^0x7F == 0 {
			w.Uint8(uint8(u))
			return
		}
		w.Uint8(uint8(u&0x7F) | 0x80)
		u >>= 7
	}
}

// String 写入协议字符串（VarInt 字节长度 + UTF-8 内容）。
func (w *Writer) String(s string) {
	w.VarInt(int32(len(s)))
	w.raw([]byte(s))
}

// UUID 写入 128 位 UUID。
func (w *Writer) UUID(u UUID) { w.raw(u[:]) }

// Position 写入方块坐标（1.14+ 压缩格式，见 Reader.Position）。
func (w *Writer) Position(p Position) {
	v := (uint64(uint32(p.X))&0x3FF_FFFF)<<38 |
		(uint64(uint32(p.Z))&0x3FF_FFFF)<<12 |
		uint64(uint32(p.Y))&0xFFF
	w.Int64(int64(v))
}

// ByteArray 写入 VarInt 长度 + 字节数组。
func (w *Writer) ByteArray(b []byte) {
	w.VarInt(int32(len(b)))
	w.raw(b)
}

// Angle 以协议角度（1/256 圈的字节）写入朝向。
func (w *Writer) Angle(deg float32) {
	w.Uint8(uint8(deg / 360 * 256))
}

// RawBytes 原样写入字节（用于复用录制的包体）。
func (w *Writer) RawBytes(b []byte) { w.raw(b) }

// FixedBytes 原样写入固定字节（区别于带长度前缀的 ByteArray）。
func (w *Writer) FixedBytes(b []byte) { w.raw(b) }
