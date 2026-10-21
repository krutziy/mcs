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

// WriteLoginFinished 写入登录成功包（uuid, name, properties）。
func WriteLoginFinished(w *protocol.Writer, uuid protocol.UUID, name string) {
	WriteLoginFinishedFor(w, uuid, name, 0)
}

// WriteLoginFinishedFor 写入登录成功包。properties 后的尾部字节经官方
// 各版本抓包逐字节确认：774 无尾部；775/776（26.1/26.2）一个 0x0a 字节；
// 777（26.3）一段固定 16 字节（语义未明，缺它真实客户端解码失败）。
func WriteLoginFinishedFor(w *protocol.Writer, uuid protocol.UUID, name string, proto int32) {
	w.UUID(uuid)
	w.String(name)
	w.VarInt(0) // properties 数量：离线模式无皮肤属性
	switch {
	case proto >= 777:
		w.RawBytes([]byte{0x80, 0x19, 0x1b, 0xaa, 0xc8, 0xec, 0x46, 0xea,
			0x85, 0xc2, 0x5d, 0x22, 0x6a, 0xf9, 0x2a, 0x58})
	case proto >= 775:
		w.Uint8(0x0a)
	}
}

// WriteSetCompression 写入压缩阈值（负数表示关闭）。
func WriteSetCompression(w *protocol.Writer, threshold int32) { w.VarInt(threshold) }

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
