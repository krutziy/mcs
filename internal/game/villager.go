package game

// 村民交易：右键村民打开 merchant 窗口（menu 19），trade_list 下发固定贸易表，
// 窗口 7 槽位：0/1 输入、2 结果；点击结果验证输入并消耗/产出。

import (
	"mcserver/internal/assets"
	"mcserver/internal/entity"
	"mcserver/internal/packet"
	"mcserver/internal/protocol"
)

// menuMerchant 是 menu 注册表的 merchant 容器类型 ID。
const menuMerchant int32 = 19

// villagerTrade 一条固定贸易（物品均 777 基准名）。
type villagerTrade struct {
	input1, input2 string // input2 空 = 单输入
	count1, count2 int32
	output         string
	outCount       int32
}

// villagerTrades 固定贸易表（原版农民/ butcher 风格的通用简化）。
var villagerTrades = []villagerTrade{
	{"wheat", "", 20, 0, "emerald", 1},
	{"carrot", "", 15, 0, "emerald", 1},
	{"potato", "", 15, 0, "emerald", 1},
	{"emerald", "", 1, 0, "bread", 6},
	{"emerald", "", 4, 0, "iron_ingot", 1},
	{"emerald", "", 2, 0, "arrow", 16},
	{"emerald", "", 1, 0, "cooked_beef", 4},
	{"emerald", "", 8, 0, "saddle", 1},
}

// villagerSession 打开中的交易会话。
type villagerSession struct {
	slots [2]entity.Item // 0/1 输入
}

// tryOpenVillager 右键村民：打开交易窗口。
func (g *Game) tryOpenVillager(p *entity.Player, m *mob) bool {
	if m.kind.name != "villager" {
		return false
	}
	g.villagers[p.EntityID] = &villagerSession{}
	p.Send.SendPlay("minecraft:open_screen", func(w *protocol.Writer) {
		packet.WriteOpenScreen(w, menuMerchant, "Villager")
	})
	g.sendTradeList(p)
	g.syncVillager(p)
	g.mobSound(m, "entity.villager.trade")
	return true
}

// sendTradeList 下发 trade_list（窗口 7；无组件匹配器 = 空）。
func (g *Game) sendTradeList(p *entity.Player) {
	trades := villagerTrades
	proto := g.playerProto(p)
	p.Send.SendPlay("minecraft:trade_list", func(w *protocol.Writer) {
		w.VarInt(7) // 窗口号
		w.VarInt(int32(len(trades)))
		for _, t := range trades {
			in1 := assets.ItemIDs(777)[t.input1]
			w.VarInt(g.itemFor(p, in1))
			w.VarInt(t.count1)
			w.VarInt(0) // 输入1 组件匹配器数 = 0
			packet.WriteSlot(w, g.itemFor(p, assets.ItemIDs(777)[t.output]), t.outCount)
			if t.input2 != "" {
				w.Bool(true)
				w.VarInt(g.itemFor(p, assets.ItemIDs(777)[t.input2]))
				w.VarInt(t.count2)
				w.VarInt(0)
			} else {
				w.Bool(false)
			}
			w.Bool(false)   // disabled
			w.Int32(0)      // uses
			w.Int32(12)     // max uses
			w.Int32(0)      // xp
			w.Int32(0)      // special price
			w.Float(0)      // price multiplier
			w.Int32(0)      // demand
		}
		w.VarInt(1)    // villager level
		w.VarInt(0)    // experience
		w.Bool(true)   // regular villager
		w.Bool(false)  // can restock
	})
	_ = proto
}

// villagerSlotRef 窗口 7 槽位：0/1 输入、2 结果（只读）、3-29 背包、30-38 快捷栏。
func villagerSlotRef(vs *villagerSession, p *entity.Player, slot int32) *entity.Item {
	switch {
	case slot == 0:
		return &vs.slots[0]
	case slot == 1:
		return &vs.slots[1]
	case slot >= 3 && slot <= 29:
		return &p.Slots[9+(slot-3)]
	case slot >= 30 && slot <= 38:
		return &p.Slots[36+(slot-30)]
	}
	return nil
}

// syncVillager 同步交易窗口内容。
func (g *Game) syncVillager(p *entity.Player) {
	vs, ok := g.villagers[p.EntityID]
	if !ok {
		return
	}
	slots := make([]packet.ItemSlot, 39)
	slots[0] = itemSlotOf(vs.slots[0])
	slots[1] = itemSlotOf(vs.slots[1])
	if t, ok := g.matchVillagerTrade(p, vs); ok {
		slots[2] = itemSlotOf(entity.Item{ID: g.itemFor(p, assets.ItemIDs(777)[t.output]), Count: t.outCount})
	}
	for i := int32(3); i <= 29; i++ {
		slots[i] = itemSlotOf(p.Slots[9+(i-3)])
	}
	for i := int32(30); i <= 38; i++ {
		slots[i] = itemSlotOf(p.Slots[36+(i-30)])
	}
	p.ContainerState++
	state := p.ContainerState
	p.Send.SendPlay("minecraft:container_set_content", func(w *protocol.Writer) {
		packet.WriteContainerSetContent(w, 7, state, slots, itemSlotOf(p.Cursor))
	})
}

// matchVillagerTrade 当前输入是否匹配一条贸易（按玩家协议空间的名字比较）。
func (g *Game) matchVillagerTrade(p *entity.Player, vs *villagerSession) (villagerTrade, bool) {
	if vs.slots[0].ID == 0 {
		return villagerTrade{}, false
	}
	name1 := g.itemNameOf(p, vs.slots[0].ID)
	name2 := ""
	if vs.slots[1].ID != 0 {
		name2 = g.itemNameOf(p, vs.slots[1].ID)
	}
	for _, t := range villagerTrades {
		if name1 != t.input1 || vs.slots[0].Count < t.count1 {
			continue
		}
		if t.input2 == "" && name2 != "" {
			continue
		}
		if t.input2 != "" && (name2 != t.input2 || vs.slots[1].Count < t.count2) {
			continue
		}
		return t, true
	}
	return villagerTrade{}, false
}

// takeVillagerTrade 点击结果槽：验证并完成交易。
func (g *Game) takeVillagerTrade(p *entity.Player) {
	vs, ok := g.villagers[p.EntityID]
	if !ok {
		return
	}
	// 名字匹配
	name1 := g.itemNameOf777(g.canonItem(p, vs.slots[0].ID))
	name2 := ""
	if vs.slots[1].ID != 0 {
		name2 = g.itemNameOf777(g.canonItem(p, vs.slots[1].ID))
	}
	for _, t := range villagerTrades {
		if name1 != t.input1 || vs.slots[0].Count < t.count1 {
			continue
		}
		if t.input2 == "" && name2 != "" {
			continue
		}
		if t.input2 != "" && (name2 != t.input2 || vs.slots[1].Count < t.count2) {
			continue
		}
		// 成交：扣输入、给输出
		vs.slots[0].Count -= t.count1
		if vs.slots[0].Count <= 0 {
			vs.slots[0] = entity.Item{}
		}
		if t.input2 != "" {
			vs.slots[1].Count -= t.count2
			if vs.slots[1].Count <= 0 {
				vs.slots[1] = entity.Item{}
			}
		}
		out := entity.Item{ID: g.itemFor(p, assets.ItemIDs(777)[t.output]), Count: t.outCount}
		if n := g.giveItem(p, out); n < out.Count {
			g.spawnItem(p.Dim, p.X, p.Y+1, p.Z, entity.Item{ID: g.canonItem(p, out.ID), Count: out.Count - n})
		}
		g.playSoundTo(p, SoundPlayer, "entity.villager.yes", 1, 1)
		return
	}
	g.playSoundTo(p, SoundPlayer, "entity.villager.no", 1, 1)
}

// closeVillager 关闭交易窗口：退回输入。
func (g *Game) closeVillager(p *entity.Player) {
	vs, ok := g.villagers[p.EntityID]
	if !ok {
		return
	}
	delete(g.villagers, p.EntityID)
	p.Send.SendPlay("minecraft:container_close", func(w *protocol.Writer) {
		packet.WriteContainerClose(w, 7)
	})
	for _, it := range vs.slots {
		if it.ID != 0 {
			if n := g.giveItem(p, it); n < it.Count {
				g.spawnItem(p.Dim, p.X, p.Y+1, p.Z, entity.Item{ID: g.canonItem(p, it.ID), Count: it.Count - n})
			}
		}
	}
	g.syncInventory(p)
}
