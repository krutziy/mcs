package conn

import (
	"bytes"
	"crypto/rand"
	"net"
	"testing"
	"time"

	"mcserver/internal/protocol"
)

// 明文阶段 → 双方启用加密 → 双向收发包（含压缩组合）应完全透明。
func TestEncryptionRoundTrip(t *testing.T) {
	secret := make([]byte, 16)
	rand.Read(secret)
	c1, c2 := net.Pipe()
	defer c1.Close()
	defer c2.Close()

	a := New(c1)
	b := New(c2)

	// 启用加密前先明文互通一次
	go a.WritePacket(0x10, func(w *protocol.Writer) { w.String("plain") })
	_ = b.SetReadDeadline(time.Now().Add(2 * time.Second))
	if id, r, err := b.ReadPacket(); err != nil || id != 0x10 || r.String() != "plain" {
		t.Fatalf("明文阶段失败: id=%d err=%v", id, err)
	}

	if err := a.SetEncryption(secret); err != nil {
		t.Fatal(err)
	}
	if err := b.SetEncryption(secret); err != nil {
		t.Fatal(err)
	}

	// 加密 + 压缩同时启用
	a.SetCompression(256)
	b.SetCompression(256)

	big := bytes.Repeat([]byte{0xCD}, 3000)
	done := make(chan struct{})
	go func() {
		defer close(done)
		a.WritePacket(0x42, func(w *protocol.Writer) { w.ByteArray(big) })
		a.WritePacket(0x43, func(w *protocol.Writer) { w.Uint8(9) })
	}()

	_ = b.SetReadDeadline(time.Now().Add(2 * time.Second))
	id, r, err := b.ReadPacket()
	if err != nil {
		t.Fatalf("加密大包读取失败: %v", err)
	}
	if id != 0x42 || !bytes.Equal(r.ByteArray(), big) {
		t.Errorf("加密大包内容不符")
	}
	if id2, r2, err := b.ReadPacket(); err != nil || id2 != 0x43 || r2.Uint8() != 9 {
		t.Errorf("加密小包不符 id=%d err=%v", id2, err)
	}
	<-done

	// 反向（b→a），证明写加密流独立于读加密流
	go b.WritePacket(0x50, func(w *protocol.Writer) { w.String("回程") })
	_ = a.SetReadDeadline(time.Now().Add(2 * time.Second))
	if id3, r3, err := a.ReadPacket(); err != nil || id3 != 0x50 || r3.String() != "回程" {
		t.Errorf("反向包不符 id=%d err=%v", id3, err)
	}
}
