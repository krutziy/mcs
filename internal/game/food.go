package game

// 进食：右键按住使用食物（use_item 开始 → 32 刻后完成 → player_action
// release_use_item 提前松手取消）。客户端本地播放进食动画，服务端
// 只在计时走满时结算营养/饱和并消耗物品（全量同步纠正预测）。
// 食物数值（营养/饱和修饰）来自公开游戏知识，与原版一致；
// 物品名 → 网络 ID 按玩家协议查 itemid_<proto>.txt。

import (
	"mcserver/internal/entity"
	"mcserver/internal/packet"
	"mcserver/internal/protocol"
)

type foodDef struct {
	nutrition  int32
	saturation float32 // 饱和修饰值（实际饱和增量 = nutrition × saturation × 2）
}

// 常见食物表（名称 → 营养, 饱和修饰）。
var foodItems = map[string]foodDef{
	"bread": {5, 0.6}, "apple": {4, 0.6}, "golden_apple": {4, 1.2},
	"cooked_beef": {8, 0.8}, "cooked_porkchop": {8, 0.8}, "cooked_chicken": {6, 0.6},
	"cooked_mutton": {6, 0.8}, "cooked_rabbit": {5, 0.6}, "cooked_cod": {5, 0.6},
	"cooked_salmon": {6, 0.8}, "carrot": {3, 0.6}, "golden_carrot": {6, 1.2},
	"baked_potato": {5, 0.6}, "potato": {1, 0.3}, "beetroot": {1, 0.6},
	"melon_slice": {2, 0.3}, "sweet_berries": {2, 0.1}, "cookie": {2, 0.1},
	"pumpkin_pie": {8, 0.3}, "chorus_fruit": {4, 0.3}, "dried_kelp": {1, 0.3},
	"honey_bottle": {6, 0.1}, "mushroom_stew": {6, 0.6}, "rabbit_stew": {10, 0.6},
	"beetroot_soup": {6, 0.6}, "suspicious_stew": {6, 0.6}, "tropical_fish": {1, 0.1},
	"beef": {3, 0.3}, "porkchop": {3, 0.3}, "chicken": {2, 0.3}, "mutton": {2, 0.3},
	"rabbit": {3, 0.3}, "cod": {2, 0.1}, "salmon": {2, 0.1},
	"rotten_flesh": {4, 0.1}, "spider_eye": {2, 0.8}, "glow_berries": {2, 0.1},
}

// eatDurationTicks 进食所需刻数（原版 1.6 秒）。
const eatDurationTicks int32 = 32

// handleUseItem 处理 use_item（对空使用物品 = 进食开始/挥空）。
// 手持食物且未满饥饿时开始 32 刻计时（tickEating 结算）；
// 药水仍为即时饮用。
func (g *Game) handleUseItem(ev *playerUseItemEvent) {
	p := ev.p
	if p == nil || p.Dead {
		return
	}
	if ev.sequence > 0 { // 与方块动作同一套变更序列号确认
		p.Send.SendPlay("minecraft:block_changed_ack", func(w *protocol.Writer) {
			packet.WriteBlockChangedAck(w, ev.sequence)
		})
	}
	if g.drinkPotion(p) {
		return
	}
	held := p.Held()
	if held.ID == 0 || held.Count <= 0 || p.Food >= 20 {
		return
	}
	name := g.itemNameOf(p, held.ID)
	if _, ok := foodItems[name]; !ok {
		return
	}
	// 开始进食：记录物品快照（换物品/松手会中断）
	p.EatTicks = 0
	p.EatItem = held.ID
}

// handleReleaseUse 处理 player_action release_use_item：进食中断。
func (g *Game) handleReleaseUse(p *entity.Player) {
	p.EatTicks = -1
}

// tickEating 推进所有玩家进食计时：手持物品变化/死亡即中断，
// 32 刻结算营养与饱和并消耗一个物品。
func (g *Game) tickEating() {
	for _, p := range g.players {
		if p.EatTicks < 0 {
			continue
		}
		if p.Dead {
			p.EatTicks = -1
			continue
		}
		held := p.Held()
		if held.ID == 0 || held.ID != p.EatItem || held.Count <= 0 {
			p.EatTicks = -1
			continue
		}
		p.EatTicks++
		if p.EatTicks < eatDurationTicks {
			continue
		}
		// 完成：结算（此时可能已满饥饿——原版开始后仍会吃完）
		p.EatTicks = -1
		def, ok := foodItems[g.itemNameOf(p, held.ID)]
		if !ok {
			continue
		}
		p.Food += def.nutrition
		if p.Food > 20 {
			p.Food = 20
		}
		p.Saturation += float32(def.nutrition) * def.saturation * 2
		if p.Saturation > float32(p.Food) {
			p.Saturation = float32(p.Food)
		}
		s := &p.Slots[36+p.HeldSlot]
		s.Count--
		if s.Count <= 0 {
			*s = entity.Item{}
		}
		g.syncHealth(p)
		g.syncInventory(p)
	}
}

// itemNameOf 按玩家协议把物品网络 ID 翻译为物品短名（无映射时空串）。
func (g *Game) itemNameOf(p *entity.Player, id int32) string {
	if m, ok := g.itemNames[g.playerProto(p)]; ok {
		return m[id]
	}
	return ""
}

var _ = protocol.Position{}
