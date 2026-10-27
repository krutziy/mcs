// auth.go 实现正版验证（online mode）的 Mojang session server 交互：
// 认证哈希派生（Java BigInteger 风格的 SHA-1 十六进制）与 hasJoined 查询。
package server

import (
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"net/url"
	"strings"
	"time"

	"mcserver/internal/packet"
	"mcserver/internal/protocol"
)

// sessionServerURL 是 Mojang session server 的 hasJoined 端点。
const sessionServerURL = "https://sessionserver.mojang.com/session/minecraft/hasJoined"

// AuthHash 计算登录认证哈希：SHA-1(serverID + sharedSecret + encodedPublicKey)，
// 结果按 Java BigInteger.toString(16) 规则（最高位置位视为负数取补码、
// 去前导零、小写）表示。导出供 recorder 等工具复算同一哈希。
// serverID 恒为空串（官方约定），因此实际输入是 secret 与公钥的拼接。
func AuthHash(serverID string, sharedSecret, encodedPublicKey []byte) string {
	h := sha1.New()
	h.Write([]byte(serverID))
	h.Write(sharedSecret)
	h.Write(encodedPublicKey)
	sum := h.Sum(nil)
	n := new(big.Int).SetBytes(sum)
	if sum[0]&0x80 != 0 { // 最高位为 1：Java BigInteger 按补码解读为负数
		max := new(big.Int).Lsh(big.NewInt(1), uint(len(sum)*8))
		n.Sub(n, max)
	}
	return n.Text(16)
}

// authClient 是可替换的 hasJoined 客户端（测试中可打桩）。
type authClient struct {
	http *http.Client
	// baseURL 可在测试中覆盖（默认 sessionServerURL）
	baseURL string
}

func newAuthClient() *authClient {
	return &authClient{
		http:    &http.Client{Timeout: 10 * time.Second},
		baseURL: sessionServerURL,
	}
}

// hasJoinedResponse 是 hasJoined 的成功响应（204 之外）。
type hasJoinedResponse struct {
	ID    string `json:"id"`   // 无连字符 UUID
	Name  string `json:"name"` // 规范化用户名
	Props []struct {
		Name      string `json:"name"`
		Value     string `json:"value"`
		Signature string `json:"signature"`
	} `json:"properties"`
}

// ErrAuthRejected 表示 session server 未确认该玩家（未登录/用户名不符）。
var ErrAuthRejected = errors.New("auth: session server 未确认该玩家（未用正版客户端登录？）")

// HasJoined 向 session server 校验玩家身份。返回正版 UUID 与皮肤属性。
func (a *authClient) HasJoined(username, serverHash string) (protocol.UUID, string, []packet.Property, error) {
	u := fmt.Sprintf("%s?username=%s&serverId=%s", a.baseURL,
		url.QueryEscape(username), url.QueryEscape(serverHash))
	resp, err := a.http.Get(u)
	if err != nil {
		return protocol.UUID{}, "", nil, fmt.Errorf("auth: 请求 session server 失败: %w", err)
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusNoContent:
		return protocol.UUID{}, "", nil, ErrAuthRejected
	case resp.StatusCode != http.StatusOK:
		return protocol.UUID{}, "", nil, fmt.Errorf("auth: session server 返回 %d", resp.StatusCode)
	}
	var body hasJoinedResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return protocol.UUID{}, "", nil, fmt.Errorf("auth: 解析 hasJoined 响应失败: %w", err)
	}
	uuid, err := uuidFromUndashed(body.ID)
	if err != nil {
		return protocol.UUID{}, "", nil, fmt.Errorf("auth: 响应 UUID 无效: %w", err)
	}
	props := make([]packet.Property, 0, len(body.Props))
	for _, p := range body.Props {
		props = append(props, packet.Property{Name: p.Name, Value: p.Value, Signature: p.Signature})
	}
	return uuid, body.Name, props, nil
}

// uuidFromUndashed 把 Mojang 的 32 位无连字符十六进制 UUID 解析为字节。
func uuidFromUndashed(s string) (protocol.UUID, error) {
	var u protocol.UUID
	s = strings.ReplaceAll(s, "-", "")
	b, err := hex.DecodeString(s)
	if err != nil || len(b) != 16 {
		return u, fmt.Errorf("非法 UUID %q", s)
	}
	copy(u[:], b)
	return u, nil
}
