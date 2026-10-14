package conn

import (
	"bytes"
	"net"
	"testing"
	"time"

	"mcserver/internal/protocol"
)

func TestFramingUncompressed(t *testing.T) {
	c1, c2 := net.Pipe()
	defer c1.Close()
	defer c2.Close()

	a := New(c1)
	go func() {
		a.WritePacket(0x05, func(w *protocol.Writer) { w.String("hi") })
	}()

	b := New(c2)
	_ = b.SetReadDeadline(time.Now().Add(2 * time.Second))
	id, r, err := b.ReadPacket()
	if err != nil {
		t.Fatalf("读取失败: %v", err)
	}
	if id != 0x05 || r.String() != "hi" {
		t.Errorf("id=0x%02x body=%v", id, r)
	}
}

func TestFramingCompressedRoundTrip(t *testing.T) {
	c1, c2 := net.Pipe()
	a := New(c1)
	b := New(c2)

	// 双方都启用压缩，阈值 256
	a.SetCompression(256)
	b.SetCompression(256)

	bigPayload := bytes.Repeat([]byte{0xAB}, 2000)

	done := make(chan struct{})
	go func() {
		defer close(done)
		a.WritePacket(0x42, func(w *protocol.Writer) { w.ByteArray(bigPayload) })
		// 再发一个低于阈值的小包
		a.WritePacket(0x43, func(w *protocol.Writer) { w.Uint8(7) })
	}()

	_ = b.SetReadDeadline(time.Now().Add(2 * time.Second))
	id, r, err := b.ReadPacket()
	if err != nil {
		t.Fatalf("大包读取失败: %v", err)
	}
	if id != 0x42 || !bytes.Equal(r.ByteArray(), bigPayload) {
		t.Errorf("大包内容不符")
	}
	id2, r2, err := b.ReadPacket()
	if err != nil {
		t.Fatalf("小包读取失败: %v", err)
	}
	if id2 != 0x43 || r2.Uint8() != 7 {
		t.Errorf("小包不符 id=0x%02x", id2)
	}
	<-done
}

func TestWriteFrameCompressedSmallProducesZeroMarker(t *testing.T) {
	// 复现与 vanilla 交互的问题：低于阈值的包帧必须是 [len+1][0x00][payload]
	c1, c2 := net.Pipe()
	a := New(c1)
	a.SetCompression(256)

	raw := make(chan []byte, 1)
	go func() {
		buf := make([]byte, 16)
		n, _ := c2.Read(buf)
		raw <- buf[:n]
	}()

	a.WritePacket(0x03, func(w *protocol.Writer) {})
	got := <-raw
	want := []byte{0x02, 0x00, 0x03} // 帧长2, 未压缩标记0, 包ID 0x03
	if !bytes.Equal(got, want) {
		t.Errorf("小包帧字节 = %x, 期望 %x", got, want)
	}
	c1.Close()
	c2.Close()
}

// TestCFB8RoundTrip CFB8 双向往返（同密钥两条独立流互通）。
func TestCFB8RoundTrip(t *testing.T) {
	key := []byte("0123456789abcdef")
	a, _ := newCFB8(key)
	b, _ := newCFB8(key)
	msg := []byte("hello minecraft encryption CFB8 1234567890")
	enc := append([]byte(nil), msg...)
	a.encrypt(enc)
	if string(enc) == string(msg) {
		t.Fatal("加密后不应与原文相同")
	}
	b.decrypt(enc)
	if string(enc) != string(msg) {
		t.Fatal("解密应还原原文")
	}
	// 连续分块流式（反馈状态跨调用保持）
	msg2 := []byte("SECOND CHUNK AFTER FIRST")
	enc2 := append([]byte(nil), msg2...)
	a.encrypt(enc2)
	b.decrypt(enc2)
	if string(enc2) != string(msg2) {
		t.Fatal("流式加解密状态应连续")
	}
}
