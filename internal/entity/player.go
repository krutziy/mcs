package entity

import (
	"mcserver/internal/protocol"
)

// Player 是一个在线玩家的状态。所有字段只在游戏主循环 goroutine 中访问，
// 会话读 goroutine 通过事件投递修改请求。
type Player struct {
	EntityID int32
	UUID     protocol.UUID
	Name     string

	X, Y, Z    float64
	Yaw, Pitch float32
	OnGround   bool
	Sneaking   bool
	Sprinting  bool

	// 同步状态
	BroadcastX, BroadcastY, BroadcastZ float64 // 上次广播给他人的位置
	TeleportID                         int32   // 待确认的传送

	// KeepAlive
	KeepAlivePending int64 // 未确认的 keepalive 时间戳，0 表示无
	KeepAliveMissed  int

	// 由 server 层填充的发送句柄
	Send Sender
}

// Sender 抽象会话的发送能力，主循环通过它给玩家发包。
type Sender interface {
	// SendPlay 构建并发送一个 play 状态的服务端包。
	SendPlay(packetName string, build func(w *protocol.Writer)) error
	// SendRawPlay 发送预构建的 play 包体。
	SendRawPlay(packetName string, body []byte) error
	// DisconnectReason 断开原因（日志用）。
	Addr() string
}
