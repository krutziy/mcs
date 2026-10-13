package entity

import (
	"mcserver/internal/protocol"
)

// Item 是玩家物品槽的最小描述（放置判定只需物品 ID 与数量）。
type Item struct {
	ID    int32 // 物品网络 ID（该玩家协议版本）
	Count int32
}

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

	// 快捷栏（仅创造模式物品包更新；用于放置时判定手持方块）
	Hotbar   [9]Item
	HeldSlot int32

	// 区块视野跟踪（主循环维护：已发送区块集合与当前缓存中心）
	SentChunks      map[[2]int32]bool
	CenterCX, CenterCZ int32

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
