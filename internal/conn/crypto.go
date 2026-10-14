package conn

// Minecraft 的 AES-CFB8 加密（8 位密文反馈）。IV = 共享密钥本身。
// 加密作用于整个帧（长度前缀 + 载荷），在压缩层之外。

import (
	"crypto/aes"
	"crypto/cipher"
)

type cfb8 struct {
	block cipher.Block
	buf   [16]byte
}

func newCFB8(key []byte) (*cfb8, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	c := &cfb8{block: block}
	copy(c.buf[:], key[:aes.BlockSize])
	return c, nil
}

// encrypt 原地加密：反馈输出密文字节。
func (c *cfb8) encrypt(p []byte) {
	var enc [16]byte
	for i := range p {
		c.block.Encrypt(enc[:], c.buf[:])
		wire := p[i] ^ enc[0]
		p[i] = wire
		copy(c.buf[:15], c.buf[1:])
		c.buf[15] = wire
	}
}

// decrypt 原地解密：反馈输入（线上）密文字节。
func (c *cfb8) decrypt(p []byte) {
	var enc [16]byte
	for i := range p {
		c.block.Encrypt(enc[:], c.buf[:])
		wire := p[i]
		p[i] = wire ^ enc[0]
		copy(c.buf[:15], c.buf[1:])
		c.buf[15] = wire
	}
}

// SetEncryption 用共享密钥启用双向 AES-CFB8（后续帧整体加解密）。
func (c *Conn) SetEncryption(key []byte) error {
	r, err := newCFB8(key)
	if err != nil {
		return err
	}
	w, err := newCFB8(key)
	if err != nil {
		return err
	}
	c.writeMu.Lock()
	c.dec, c.enc = r, w
	c.writeMu.Unlock()
	return nil
}
