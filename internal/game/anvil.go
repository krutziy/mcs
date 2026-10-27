package game

// 铁砧：右键打开（menu anvil=最后 generic 9x3? 用 menu 注册表 anvil——
// 查得 id 待定；先复用 generic_9x3 容器布局），双输入槽 + 输出槽。
// 功能：同物品合并修复（消耗经验）、附魔合并（两件附魔道具合并取高/相加）、
// 重命名（第三槽预览由客户端本地完成，取出时应用 Cursor 名字记账）。

import (
	"strings"

	"mcserver/internal/assets"
	"mcserver/internal/entity"
	"mcserver/internal/packet"
	"mcserver/internal/protocol"
)

const (
	anvilState     uint32 = 12962
	chippedAnvil   uint32 = 12966
	damagedAnvil   uint32 = 12970
	menuAnvil      int32  = 12 // menu 注册表 anvil
	itemNameTagID  int32  = 0  // 占位：name tag 物品后查
)

var anvilStates = map[uint32]bool{12962: true, 12966: true, 12970: true}

// anvilSession 打开铁砧的会话状态（双输入）。
type anvilSession struct {
	in0, in1   entity.Item
	ench0      []entity.EnchEntry // in0 的附魔记录
	ench1      []entity.EnchEntry
	resultEnch []entity.EnchEntry
}

// tryOpenAnvil 右键铁砧。
func (g *Game) tryOpenAnvil(p *entity.Player, u packet.UseItemOn) bool {
	if !anvilStates[g.world.GetBlock(u.X, u.Y, u.Z)] {
		return false
	}
	if _, ok := g.anvils[p.EntityID]; !ok {
		g.anvils[p.EntityID] = &anvilSession{}
	}
	p.Send.SendPlay("minecraft:open_screen", func(w *protocol.Writer) {
		packet.WriteOpenScreen(w, menuAnvil, "Repair & Name")
	})
	g.syncAnvil(p)
	return true
}

// closeAnvil 关闭并退料。
func (g *Game) closeAnvil(p *entity.Player) {
	as, ok := g.anvils[p.EntityID]
	if !ok {
		return
	}
	delete(g.anvils, p.EntityID)
	for _, it := range []entity.Item{as.in0, as.in1} {
		if it.ID != 0 {
			if n := g.giveItem(p, it); n < it.Count {
				g.spawnItem(p.X, p.Y+1, p.Z, entity.Item{ID: it.ID, Count: it.Count - n})
			}
		}
	}
	p.Send.SendPlay("minecraft:container_close", func(w *protocol.Writer) {
		packet.WriteContainerClose(w, 6)
	})
	g.syncInventory(p)
}

// anvilSlotRef 窗口 6 槽位（0 输入A 1 输入B 2 输出 3-29 背包 30-38 快捷栏）。
func anvilSlotRef(as *anvilSession, p *entity.Player, slot int32) *entity.Item {
	switch {
	case slot == 0:
		return &as.in0
	case slot == 1:
		return &as.in1
	case slot >= 3 && slot <= 29:
		return &p.Slots[9+(slot-3)]
	case slot >= 30 && slot <= 38:
		return &p.Slots[36+(slot-30)]
	}
	return nil
}

// anvilResult 计算输出（取出时生效）：修复/合并/改名预览。
func (g *Game) anvilResult(as *anvilSession) entity.Item {
	if as.in0.ID == 0 {
		return entity.Item{}
	}
	out := as.in0
	// 合并修复：同 ID 两件 → 耐久相加（Damage 语义=已损，合并后已损减半）
	if as.in1.ID == as.in0.ID && as.in1.ID != 0 {
		merged := as.in0
		max := durabilityOf(g.itemNameOf777(as.in0.ID))
		if max > 0 {
			remain := (max-as.in0.Damage) + (max - as.in1.Damage)
			if remain > max {
				remain = max
			}
			merged.Damage = max - remain
			merged.Count = 1
		}
		// 附魔合并：同名取高，冲突项（简化）忽略
		seen := map[string]int32{}
		for _, e := range append(append([]entity.EnchEntry{}, as.ench0...), as.ench1...) {
			if seen[e.Name] < e.Level {
				seen[e.Name] = e.Level
			}
		}
		for _, e := range as.ench1 {
			if seen[e.Name] == e.Level { // 同级 → +1（原版规则）
				seen[e.Name] = e.Level + 1
				if g.enchants != nil {
					if info, ok := g.enchants[e.Name]; ok && seen[e.Name] > info.MaxLevel {
						seen[e.Name] = info.MaxLevel
					}
				}
			}
		}
		as.resultEnch = nil
		for name, lv := range seen {
			as.resultEnch = append(as.resultEnch, entity.EnchEntry{Name: name, Level: lv})
		}
		return merged
	}
	// 材料修复：同工具 + 材料（钻石修钻甲等简化为任意同品质材料 1 修复 25%）
	if as.in1.ID != 0 {
		matName := g.itemNameOf777(as.in1.ID)
		max := durabilityOf(g.itemNameOf777(as.in0.ID))
		if max > 0 && isRepairMaterial(g.itemNameOf777(as.in0.ID), matName) {
			rep := as.in0
			rep.Damage -= max / 4
			if rep.Damage < 0 {
				rep.Damage = 0
			}
			return rep
		}
	}
	as.resultEnch = as.ench0
	return out
}

// isRepairMaterial 工具品质 → 材料（铁工具+铁锭、钻+钻石……简化按首词匹配）。
func isRepairMaterial(tool, mat string) bool {
	m := map[string]string{"wooden": "planks", "stone": "stone", "iron": "iron_ingot",
		"diamond": "diamond", "golden": "gold_ingot", "netherite": "netherite_ingot", "copper": "copper_ingot"}
	for q, mm := range m {
		if strings.HasPrefix(tool, q) {
			return mat == mm
		}
	}
	return false
}

// takeAnvilResult 取输出：扣双输入与经验（合并 3 级/材料 1 级）。
func (g *Game) takeAnvilResult(p *entity.Player) bool {
	as, ok := g.anvils[p.EntityID]
	if !ok || as.in0.ID == 0 {
		return false
	}
	result := g.anvilResult(as)
	if result.ID == 0 {
		return false
	}
	cost := int32(1)
	if as.in1.ID == as.in0.ID {
		cost = 3
	}
	if p.Level < cost && !g.isCreative(p) {
		return false
	}
	cur := p.Cursor
	if cur.ID != 0 && (cur.ID != result.ID || cur.Count+result.Count > itemMaxStack) {
		return false
	}
	if cur.ID == 0 {
		p.Cursor = result
	} else {
		p.Cursor.Count += result.Count
	}
	// 附魔记录迁移到光标
	p.EnchItem = as.resultEnch
	if !g.isCreative(p) {
		p.Level -= cost
		if p.Level < 0 {
			p.Level = 0
		}
	}
	g.syncExperience(p)
	as.in0 = entity.Item{}
	as.in1 = entity.Item{}
	as.resultEnch = nil
	return true
}

// syncAnvil 同步铁砧窗口（输出槽给预览）。
func (g *Game) syncAnvil(p *entity.Player) {
	as, ok := g.anvils[p.EntityID]
	if !ok {
		return
	}
	preview := g.anvilResult(as)
	slots := make([]packet.ItemSlot, 39)
	slots[0] = packet.ItemSlot{ID: as.in0.ID, Count: as.in0.Count, Damage: as.in0.Damage}
	slots[1] = packet.ItemSlot{ID: as.in1.ID, Count: as.in1.Count, Damage: as.in1.Damage}
	if preview.ID != 0 {
		var el []packet.EnchPair
		for _, e := range as.resultEnch {
			el = append(el, packet.EnchPair{Name: e.Name, Level: e.Level})
		}
		slots[2] = packet.ItemSlot{ID: preview.ID, Count: preview.Count, Damage: preview.Damage, EnchList: el}
	}
	for i := int32(3); i <= 29; i++ {
		s := p.Slots[9+(i-3)]
		slots[i] = packet.ItemSlot{ID: s.ID, Count: s.Count, Damage: s.Damage}
	}
	for i := int32(30); i <= 38; i++ {
		s := p.Slots[36+(i-30)]
		slots[i] = packet.ItemSlot{ID: s.ID, Count: s.Count, Damage: s.Damage}
	}
	p.ContainerState++
	state := p.ContainerState
	p.Send.SendPlay("minecraft:container_set_content", func(w *protocol.Writer) {
		packet.WriteContainerSetContentEnch(w, 6, state, slots, packet.ItemSlot{})
	})
	// 费用提示（属性 0 = 等级费用）
	g.anvilResult(as)
	cost := int32(1)
	if as.in1.ID == as.in0.ID && as.in1.ID != 0 {
		cost = 3
	}
	p.Send.SendPlay("minecraft:container_set_data", func(w *protocol.Writer) {
		packet.WriteContainerSetData(w, 6, 0, int16(cost))
	})
	_ = assets.ItemIDs
}
