package game

// 进食：右键使用食物（use_item 包）恢复饥饿与饱和。
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

// handleUseItem 处理 use_item（对空使用物品 = 进食/挥空）。
// 简化：手持食物且未满饥饿时立即吃完（原版需按住 1.6 秒，进度动画未实现）。
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
	def, ok := foodItems[name]
	if !ok {
		return
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

// itemNameOf 按玩家协议把物品网络 ID 翻译为物品短名（无映射时空串）。
func (g *Game) itemNameOf(p *entity.Player, id int32) string {
	if m, ok := g.itemNames[g.playerProto(p)]; ok {
		return m[id]
	}
	return ""
}
