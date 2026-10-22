package entity

import (
	"mcserver/internal/protocol"
)

// Item 是玩家物品槽的最小描述（放置判定只需物品 ID 与数量）。
type Item struct {
	ID     int32 // 物品网络 ID（该玩家协议版本）
	Count  int32
	Damage int32 // 已损失耐久（0=全新；wire 上的 damage 组件）
}

// Held 返回当前手持物品（快捷栏选中槽）。
func (p *Player) Held() Item { return p.Slots[36+p.HeldSlot] }

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

	// 物品栏（窗口 0 的 46 槽：0-4 合成 5-8 盔甲 9-35 背包 36-44 快捷栏 45 副手；
	// ID 0 = 空。主循环独占访问）
	Slots          [46]Item
	HeldSlot       int32 // 0..8 → Slots[36+HeldSlot]
	Cursor         Item // 鼠标光标上的物品
	ContainerState int32 // 容器同步状态号（每次全量同步 +1）

	// 区块视野跟踪（主循环维护：已发送区块集合与当前缓存中心）
	SentChunks      map[[2]int32]bool
	CenterCX, CenterCZ int32

	// 经验（主循环独占）
	TotalXP   int32
	Level     int32
	XPPending int32

	// SpawnPos 个人重生点（睡床设置；零值 = 世界出生点）
	SpawnPos [3]float64
	Sleeping bool

	// GameMode 该玩家的游戏模式（1=创造 0=生存；默认继承服务器配置）
	GameMode int32

	// 生命值/饥饿（主循环独占；创造模式免疫伤害）
	Health      float32
	Food        int32
	Saturation  float32
	Exhaustion  float64 // 体力消耗累积（≥4 扣饱和/食物）
	Dead        bool    // 死亡等待重生
	FallStart   float64 // 摔落起点 y（0 = 无追踪）

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
