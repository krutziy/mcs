package protocol

import (
	"bytes"
	"reflect"
	"testing"
)

func TestVarIntRoundTrip(t *testing.T) {
	cases := []int32{0, 1, 127, 128, 255, 256, 25565, 2097151, 2147483647, -1, -2147483648}
	for _, v := range cases {
		w := NewWriter()
		w.VarInt(v)
		r := NewReader(w.Bytes())
		got := r.VarInt()
		if r.Err != nil || got != v {
			t.Errorf("VarInt(%d) 往返失败: got=%d err=%v", v, got, r.Err)
		}
	}
}

func TestVarIntKnownBytes(t *testing.T) {
	// 协议文档中的经典样例
	cases := []struct {
		v  int32
		b  []byte
	}{
		{0, []byte{0x00}},
		{1, []byte{0x01}},
		{2, []byte{0x02}},
		{127, []byte{0x7f}},
		{128, []byte{0x80, 0x01}},
		{255, []byte{0xff, 0x01}},
		{25565, []byte{0xdd, 0xc7, 0x01}},
		{2097151, []byte{0xff, 0xff, 0x7f}},
		{2147483647, []byte{0xff, 0xff, 0xff, 0xff, 0x07}},
		{-1, []byte{0xff, 0xff, 0xff, 0xff, 0x0f}},
		{-2147483648, []byte{0x80, 0x80, 0x80, 0x80, 0x08}},
	}
	for _, c := range cases {
		w := NewWriter()
		w.VarInt(c.v)
		if !bytes.Equal(w.Bytes(), c.b) {
			t.Errorf("VarInt(%d) 编码 = %x, 期望 %x", c.v, w.Bytes(), c.b)
		}
		r := NewReader(c.b)
		if got := r.VarInt(); r.Err != nil || got != c.v {
			t.Errorf("VarInt 解码 %x = %d (期望 %d)", c.b, got, c.v)
		}
	}
}

func TestVarIntRejectsOverlong(t *testing.T) {
	r := NewReader([]byte{0xff, 0xff, 0xff, 0xff, 0xff, 0x01}) // 6 字节继续位
	r.VarInt()
	if r.Err == nil {
		t.Error("超过 5 字节的 VarInt 应报错")
	}
}

func TestStringRoundTrip(t *testing.T) {
	for _, s := range []string{"", "hello", "你好世界", "minecraft:overworld"} {
		w := NewWriter()
		w.String(s)
		r := NewReader(w.Bytes())
		if got := r.String(); r.Err != nil || got != s {
			t.Errorf("String(%q) 往返失败: got=%q err=%v", s, got, r.Err)
		}
	}
}

func TestPositionRoundTrip(t *testing.T) {
	cases := []Position{
		{0, 0, 0}, {8, 63, -2}, {-100, 400, 250000}, {33554431, 2047, -33554432},
	}
	for _, p := range cases {
		w := NewWriter()
		w.Position(p)
		r := NewReader(w.Bytes())
		got := r.Position()
		if r.Err != nil || got != p {
			t.Errorf("Position%+v 往返失败: got=%+v err=%v", p, got, r.Err)
		}
	}
}

func TestPositionKnownBits(t *testing.T) {
	// 手工推导：v = (x&0x3FFFFFF)<<38 | (z&0x3FFFFFF)<<12 | (y&0xFFF)
	cases := []struct {
		p    Position
		want []byte
	}{
		{Position{1, 2, 3}, []byte{0x00, 0x00, 0x00, 0x40, 0x00, 0x00, 0x30, 0x02}},
		{Position{-1, 2047, -33554432}, []byte{0xff, 0xff, 0xff, 0xe0, 0x00, 0x00, 0x07, 0xff}},
	}
	for _, c := range cases {
		w := NewWriter()
		w.Position(c.p)
		if !bytes.Equal(w.Bytes(), c.want) {
			t.Errorf("Position%+v 编码 = %x, 期望 %x", c.p, w.Bytes(), c.want)
		}
	}
}

func TestPrimitivesRoundTrip(t *testing.T) {
	w := NewWriter()
	w.Bool(true)
	w.Int16(-300)
	w.Int32(123456)
	w.Int64(-9876543210)
	w.Float(3.5)
	w.Double(-2.25)
	w.VarLong(1 << 40)
	w.UUID(UUID{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16})
	w.String("tail")

	r := NewReader(w.Bytes())
	if !r.Bool() {
		t.Error("Bool 失败")
	}
	if got := r.Int16(); got != -300 {
		t.Errorf("Int16 = %d", got)
	}
	if got := r.Int32(); got != 123456 {
		t.Errorf("Int32 = %d", got)
	}
	if got := r.Int64(); got != -9876543210 {
		t.Errorf("Int64 = %d", got)
	}
	if got := r.Float(); got != 3.5 {
		t.Errorf("Float = %v", got)
	}
	if got := r.Double(); got != -2.25 {
		t.Errorf("Double = %v", got)
	}
	if got := r.VarLong(); got != 1<<40 {
		t.Errorf("VarLong = %d", got)
	}
	u := r.UUID()
	if u != (UUID{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16}) {
		t.Errorf("UUID = %v", u)
	}
	if got := r.String(); got != "tail" {
		t.Errorf("String = %q", got)
	}
	if r.Err != nil {
		t.Errorf("意外错误: %v", r.Err)
	}
}

func TestReaderOverrun(t *testing.T) {
	r := NewReader([]byte{0x01, 0x02})
	r.Int64()
	if r.Err == nil {
		t.Error("读取超出数据应报错")
	}
}

func TestNBTRoundTrip(t *testing.T) {
	root := Compound{
		"name":   "minecraft:dimension_type",
		"byte":   int8(-5),
		"short":  int16(300),
		"int":    int32(-70000),
		"long":   int64(1 << 40),
		"float":  float32(1.5),
		"double": float64(-0.5),
		"bytes":  []byte{1, 2, 3},
		"str":    "值",
		"list":   List{int32(1), int32(2), int32(3)},
		"strs":   List{"a", "b"},
		"nested": Compound{"deep": List{Compound{"x": int32(9)}}},
		"ints":   []int32{10, 20},
		"longs":  []int64{100, 200},
	}
	w := NewWriter()
	WriteNBT(w, root)
	if w.Err != nil {
		t.Fatalf("编码失败: %v", w.Err)
	}
	r := NewReader(w.Bytes())
	got := ReadNBT(r)
	if r.Err != nil {
		t.Fatalf("解码失败: %v", r.Err)
	}
	if !reflect.DeepEqual(got, root) {
		t.Errorf("NBT 往返不一致:\n got  %#v\n want %#v", got, root)
	}
}

func TestNBTEmptyListAndCompound(t *testing.T) {
	root := Compound{"empty": List{}, "comp": Compound{}}
	w := NewWriter()
	WriteNBT(w, root)
	r := NewReader(w.Bytes())
	got := ReadNBT(r).(Compound)
	if r.Err != nil {
		t.Fatalf("解码失败: %v", r.Err)
	}
	if len(got["empty"].(List)) != 0 || len(got["comp"].(Compound)) != 0 {
		t.Error("空列表/空复合标签往返失败")
	}
}
