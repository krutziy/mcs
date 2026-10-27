package conn

import "crypto/cipher"

// cfb8Stream 实现 AES-CFB8（8 位密文反馈）流式加解密。
//
// Go 标准库只有整块反馈的 cipher.CFBStream（等价 CFB128），而 Minecraft
// 加密通道对齐 Java 的 AES/CFB8/NoPadding：每处理一个字节，就把一个
// 密文字节移入 128 位移位寄存器并重新做一次 AES 加密取首字节作密钥流。
// 注意反馈的是密文字节：加密时密文是输出、解密时密文是输入，因此
// 两个方向必须用各自的实例（同 Java 的 ENCRYPT/DECRYPT 模式）。
// 正确性由 NIST SP 800-38A F.3.7/F.3.9/F.3.11 官方向量与 JDK 交叉验证，
// 见 cfb8_test.go。
type cfb8Stream struct {
	b       cipher.Block
	sr      [16]byte // 移位寄存器：初值 = IV，此后 = 最近 16 个密文字节
	decrypt bool
}

// newCFB8Stream 创建 CFB8 流；decrypt 指定方向（反馈取密文字节）。
// iv 必须为 16 字节（Minecraft 约定取共享密钥）。
func newCFB8Stream(block cipher.Block, iv []byte, decrypt bool) *cfb8Stream {
	s := &cfb8Stream{b: block, decrypt: decrypt}
	copy(s.sr[:], iv)
	return s
}

// XORKeyStream 实现 cipher.Stream 接口。
func (s *cfb8Stream) XORKeyStream(dst, src []byte) {
	if len(dst) < len(src) {
		panic("conn: CFB8 输出缓冲不足")
	}
	var ks [16]byte
	for i := range src {
		s.b.Encrypt(ks[:], s.sr[:])
		out := src[i] ^ ks[0]
		feedback := out
		if s.decrypt {
			feedback = src[i] // 解密方向：密文在输入侧
		}
		copy(s.sr[:15], s.sr[1:])
		s.sr[15] = feedback
		dst[i] = out
	}
}
