package conn

import (
	"bytes"
	"crypto/aes"
	"crypto/rand"
	"encoding/hex"
	"testing"
)

// NIST SP 800-38A 附录 F.3.7/F.3.9/F.3.11 的 CFB8 加密向量。
// 期望密文已与 JDK 26 的 AES/CFB8/NoPadding 输出逐字节核对一致。
func TestCFB8NISTVectors(t *testing.T) {
	const iv = "000102030405060708090a0b0c0d0e0f"
	const pt = "6bc1bee22e409f96e93d7e117393172a"
	cases := []struct{ name, key, ct string }{
		{"AES-128", "2b7e151628aed2a6abf7158809cf4f3c", "3b79424c9c0dd436bace9e0ed4586a4f"},
		{"AES-192", "8e73b0f7da0e6452c810f32b809079e562f8ead2522c6b7b", "cda2521ef0a905ca44cd057cbf0d47a0"},
		{"AES-256", "603deb1015ca71be2b73aef0857d77811f352c073b6108d72d9810a30914dff4", "dc1f1a8520a64db55fcc8ac554844e88"},
	}
	ivB, _ := hex.DecodeString(iv)
	ptB, _ := hex.DecodeString(pt)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			key, _ := hex.DecodeString(tc.key)
			want, _ := hex.DecodeString(tc.ct)
			block, err := aes.NewCipher(key)
			if err != nil {
				t.Fatal(err)
			}
			got := make([]byte, len(ptB))
			newCFB8Stream(block, ivB, false).XORKeyStream(got, ptB)
			if !bytes.Equal(got, want) {
				t.Errorf("加密 = %x, 期望 %x", got, want)
			}
			// 解密用独立实例（同一运算），验证对称性
			back := make([]byte, len(ptB))
			newCFB8Stream(block, ivB, true).XORKeyStream(back, want)
			if !bytes.Equal(back, ptB) {
				t.Errorf("解密 = %x, 期望 %x", back, ptB)
			}
		})
	}
}

// 60 字节长流向量（跨多个 AES 块的逐字节反馈），与 JDK AES/CFB8/NoPadding
// 交叉验证生成：key=0f0e..00, iv=1011..00, 明文为 0x00..0x3b。
func TestCFB8LongStreamCrossCheckedWithJDK(t *testing.T) {
	key, _ := hex.DecodeString("0f0e0d0c0b0a09080706050403020100")
	iv, _ := hex.DecodeString("101112131415161718191a1c1d1e1f00")
	pt := make([]byte, 60)
	for i := range pt {
		pt[i] = byte(i)
	}
	want, _ := hex.DecodeString("e78e5abb519199752f8be5ce1a87f6e4bf52fba39a581f71209364df85284243987ed072eb3279d2a6b79b751bd20dda0e77d0017f5cafd2acc0cab8")
	block, _ := aes.NewCipher(key)
	got := make([]byte, len(pt))
	newCFB8Stream(block, iv, false).XORKeyStream(got, pt)
	if !bytes.Equal(got, want) {
		t.Errorf("长流加密 = %x, 期望 %x", got, want)
	}
}

// 流状态必须跨多次 XORKeyStream 调用保持（分块写 == 一次写）。
func TestCFB8StreamStatePersistsAcrossCalls(t *testing.T) {
	key := make([]byte, 16)
	iv := make([]byte, 16)
	rand.Read(key)
	rand.Read(iv)
	block, _ := aes.NewCipher(key)
	pt := make([]byte, 100)
	rand.Read(pt)

	whole := newCFB8Stream(block, iv, false)
	oneShot := make([]byte, len(pt))
	whole.XORKeyStream(oneShot, pt)

	piecemeal := newCFB8Stream(block, iv, false)
	chunked := make([]byte, 0, len(pt))
	for off := 0; off < len(pt); off += 7 {
		end := off + 7
		if end > len(pt) {
			end = len(pt)
		}
		out := make([]byte, end-off)
		piecemeal.XORKeyStream(out, pt[off:end])
		chunked = append(chunked, out...)
	}
	if !bytes.Equal(oneShot, chunked) {
		t.Errorf("分块加密与一次加密不一致")
	}
}
