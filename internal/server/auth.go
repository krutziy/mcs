package server

// 在线模式验证：Mojang session server hasJoined 协议 + 加密握手载荷。
// 默认离线模式（online_mode=false）不经过这里。

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha1"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"time"

	"mcserver/internal/conn"
	"mcserver/internal/packet"
	"mcserver/internal/protocol"
)

const sessionURL = "https://sessionserver.mojang.com/session/minecraft/hasJoined"

// AuthResult 是 hasJoined 的验证结果。
type AuthResult struct {
	UUID string `json:"id"`
	Name string `json:"name"`
}

// newKeyPair 生成登录加密用的 RSA 密钥（进程启动一次）。
func newKeyPair() (*rsa.PrivateKey, error) {
	return rsa.GenerateKey(rand.Reader, 2048)
}

// publicKeyDER 导出公钥 DER。
func publicKeyDER(k *rsa.PrivateKey) []byte {
	b, _ := x509.MarshalPKIXPublicKey(&k.PublicKey)
	return b
}

// mojangServerId 计算 Mojang 风格的 serverId：sha1(secret+DER) 按
// Java BigInteger 的 toString(16)（去前导零、负数带负号、小写）。
func mojangServerId(secret, pubDER []byte) string {
	h := sha1.New()
	h.Write(secret)
	h.Write(pubDER)
	n := new(big.Int).SetBytes(h.Sum(nil))
	if n.Sign() == 0 {
		return "0"
	}
	return n.Text(16)
}

// hasJoined 调用 Mojang session server 验证玩家。
func hasJoined(username, serverID string) (*AuthResult, error) {
	cli := &http.Client{Timeout: 5 * time.Second}
	resp, err := cli.Get(sessionURL + "?username=" + username + "&serverId=" + serverID)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("hasJoined 状态 %d", resp.StatusCode)
	}
	var r AuthResult
	body, _ := io.ReadAll(resp.Body)
	if err := json.Unmarshal(body, &r); err != nil || r.UUID == "" {
		return nil, fmt.Errorf("hasJoined 响应异常")
	}
	return &r, nil
}

// encryptHandshake 发送加密请求并完成响应协商。
// 成功时启用连接加密并返回验证通过的玩家 UUID；失败返回错误（连接已被拒）。
func (s *Server) encryptHandshake(c *conn.Conn, suite interface {
	MustID(state, dir, packet string) int32
}, username string) (protocol.UUID, error) {
	verify := make([]byte, 4)
	rand.Read(verify)
	pubDER := publicKeyPair(s.privKey)

	c.WritePacket(suite.MustID("login", "clientbound", "minecraft:hello"), func(w *protocol.Writer) {
		w.String("")
		w.ByteArray(pubDER)
		w.ByteArray(verify)
		w.Bool(true) // 要求客户端认证
	})

	id, r, err := c.ReadPacket()
	if err != nil {
		return protocol.UUID{}, err
	}
	if id != suite.MustID("login", "serverbound", "minecraft:key") {
		return protocol.UUID{}, fmt.Errorf("期待加密响应，收到包 0x%02x", id)
	}
	secretCipher := r.ByteArray()
	tokenCipher := r.ByteArray()
	secret, err := rsa.DecryptPKCS1v15(nil, s.privKey, secretCipher)
	if err != nil || len(secret) != 16 {
		return protocol.UUID{}, fmt.Errorf("共享密钥解密失败")
	}
	token, err := rsa.DecryptPKCS1v15(nil, s.privKey, tokenCipher)
	if err != nil || string(token) != string(verify) {
		return protocol.UUID{}, fmt.Errorf("verifyToken 不符")
	}

	res, err := hasJoined(username, mojangServerId(secret, pubDER))
	if err != nil {
		return protocol.UUID{}, fmt.Errorf("正版验证失败: %v", err)
	}
	var u protocol.UUID
	if _, err := fmt.Sscanf(res.UUID, "%02x%02x%02x%02x%02x%02x%02x%02x%02x%02x%02x%02x%02x%02x%02x%02x",
		&u[0], &u[1], &u[2], &u[3], &u[4], &u[5], &u[6], &u[7],
		&u[8], &u[9], &u[10], &u[11], &u[12], &u[13], &u[14], &u[15]); err != nil {
		return protocol.UUID{}, fmt.Errorf("UUID 解析失败")
	}
	if err := c.SetEncryption(secret); err != nil {
		return protocol.UUID{}, err
	}
	return u, nil
}

func publicKeyPair(k *rsa.PrivateKey) []byte { return publicKeyDER(k) }

var _ = packet.WriteSetCompression
