// Package game 实现框架核心：事件总线、玩家管理、20TPS 主循环。
//
// 并发模型：
//   - 每个连接一个读 goroutine，解析包后把事件投递到共享 channel
//   - 单个主循环 goroutine 消费事件并驱动 tick——玩法逻辑全部单线程，无需加锁
//   - 输出通过各会话的发送方法（内部有写锁）广播
package game

import (
	"reflect"
	"sync"

	"mcserver/internal/entity"
)

// Event 是所有事件的标记接口。
type Event interface{}

// PlayerJoin 玩家完成进入世界的全部流程后触发。
type PlayerJoin struct{ Player *entity.Player }

// PlayerQuit 玩家断开后触发。
type PlayerQuit struct {
	Player *entity.Player
	Reason string
}

// PlayerMoved 玩家位置更新（已写入 Player 字段）后触发。
type PlayerMoved struct{ Player *entity.Player }

// PlayerChatted 玩家发送聊天。
type PlayerChatted struct {
	Player  *entity.Player
	Message string
}

// PlayerCommand 玩家执行命令（不含前导斜杠）。
type PlayerCommand struct {
	Player *entity.Player
	Name   string
	Args   []string
}

// Tick 每个游戏刻（50ms）触发。
type Tick struct{ Count int64 }

// EventBus 是玩法模块的扩展点：订阅任意事件类型，回调在主循环 goroutine 中执行。
type EventBus struct {
	mu   sync.Mutex
	subs map[reflect.Type][]func(Event)
}

// NewEventBus 创建事件总线。
func NewEventBus() *EventBus {
	return &EventBus{subs: map[reflect.Type][]func(Event){}}
}

// On 订阅指定类型的事件。玩法模块在初始化时调用。
func (b *EventBus) On(example Event, handler func(Event)) {
	b.mu.Lock()
	defer b.mu.Unlock()
	t := reflect.TypeOf(example)
	b.subs[t] = append(b.subs[t], handler)
}

// emit 在主循环中分发事件。
func (b *EventBus) emit(e Event) {
	b.mu.Lock()
	handlers := b.subs[reflect.TypeOf(e)]
	b.mu.Unlock()
	for _, h := range handlers {
		h(e)
	}
}
