// Package packet 定义各协议状态下具体包的编解码。
// 字段格式依据 26.1 官方服务端抓包逐字节验证（见 .tools/captures）。
package packet

import (
	"mcserver/internal/protocol"
)

// ---- Status ----

// PingPayload 是 status/login/config 各状态的 ping/pong 载荷。
type PingPayload struct{ Time int64 }

// WritePing 写入 ping/pong 的载荷（time i64）。
func WritePing(w *protocol.Writer, p PingPayload) { w.Int64(p.Time) }

// ---- Login ----

// LoginStart 是客户端登录首包（用户名 + 玩家 UUID）。
type LoginStart struct {
	Username string
	UUID     protocol.UUID
}

// ReadLoginStart 解析登录首包。
func ReadLoginStart(r *protocol.Reader) LoginStart {
	return LoginStart{Username: r.String(), UUID: r.UUID()}
}

// WriteSetCompression 写入压缩阈值（负数表示关闭）。
func WriteSetCompression(w *protocol.Writer, threshold int32) { w.VarInt(threshold) }

// WriteLoginFinishedWithProperties 写入登录成功包（uuid, name, properties）。
// 正版模式把 Mojang 返回的皮肤属性原样下发给客户端；离线模式传 nil 属性。

// EncryptionRequest 是 clientbound minecraft:hello 加密请求：
// serverID 恒为空串（历史遗留字段），公钥与验证令牌为字节数组。
type EncryptionRequest struct {
	ServerID    string
	PublicKey   []byte
	VerifyToken []byte
	// ShouldAuthenticate: 26.x(775+) 新增，指示客户端是否走 Mojang 正版验证
	ShouldAuthenticate bool
}

// WriteEncryptionRequest 写入加密请求包。
func WriteEncryptionRequest(w *protocol.Writer, r EncryptionRequest) {
	w.String(r.ServerID)
	w.ByteArray(r.PublicKey)
	w.ByteArray(r.VerifyToken)
	w.Bool(r.ShouldAuthenticate)
}

// ReadEncryptionRequest 解析加密请求包（recorder 侧用）。
func ReadEncryptionRequest(r *protocol.Reader) EncryptionRequest {
	req := EncryptionRequest{ServerID: r.String()}
	req.PublicKey = r.ByteArray()
	req.VerifyToken = r.ByteArray()
	req.ShouldAuthenticate = r.Bool()
	return req
}

// EncryptionResponse 是 serverbound minecraft:key：
// 共享密钥与验证令牌均以服务器 RSA 公钥加密后回传。
type EncryptionResponse struct {
	SharedSecret []byte
	VerifyToken  []byte
}

// WriteEncryptionResponse 写入加密回应包（recorder 侧用）。
func WriteEncryptionResponse(w *protocol.Writer, r EncryptionResponse) {
	w.ByteArray(r.SharedSecret)
	w.ByteArray(r.VerifyToken)
}

// ReadEncryptionResponse 解析加密回应包。
func ReadEncryptionResponse(r *protocol.Reader) EncryptionResponse {
	return EncryptionResponse{SharedSecret: r.ByteArray(), VerifyToken: r.ByteArray()}
}

// Property 是玩家档案属性（正版皮肤的 textures 等）。
type Property struct {
	Name      string
	Value     string
	Signature string
}

// WriteLoginFinishedWithProperties 写入登录成功包（uuid, name, properties）。
// 正版模式把 Mojang 返回的皮肤属性原样下发给客户端。
func WriteLoginFinishedWithProperties(w *protocol.Writer, uuid protocol.UUID, name string, props []Property) {
	w.UUID(uuid)
	w.String(name)
	w.VarInt(int32(len(props)))
	for _, p := range props {
		w.String(p.Name)
		w.String(p.Value)
		w.String(p.Signature)
	}
}

// WriteDisconnectLogin 写入登录阶段踢出消息（JSON 文本组件）。
func WriteDisconnectLogin(w *protocol.Writer, reason string) { w.String(reason) }

// ---- Configuration ----

// WriteRegistryData 写入 registry_data 包（复用官方录制的包体）。
func WriteRegistryData(w *protocol.Writer, body []byte) { w.RawBytes(body) }

// WriteFinishConfiguration 写入配置完成包（空载荷）。
func WriteFinishConfiguration(w *protocol.Writer) {}

// WriteKeepAlive 写入 keep_alive 载荷（任意状态通用）。
func WriteKeepAlive(w *protocol.Writer, id int64) { w.Int64(id) }

// ReadKeepAlive 读取 keep_alive 载荷。
func ReadKeepAlive(r *protocol.Reader) int64 { return r.Int64() }

// ReadSelectKnownPacks 解析客户端 known packs 回应，返回条目。
type KnownPack struct{ Namespace, ID, Version string }

// ReadSelectKnownPacks 解析 select_known_packs 回应。
func ReadSelectKnownPacks(r *protocol.Reader) []KnownPack {
	n := int(r.VarInt())
	out := make([]KnownPack, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, KnownPack{r.String(), r.String(), r.String()})
	}
	return out
}
