package game

// 附魔台：右键打开（menu enchantment=13，窗口 5），槽 0=物品、1=青金石。
// 从点击按钮选择三档（1/2/3 级花费），按权重随机抽取适用附魔并写入
// 物品的 enchantments 组件（组件 ID 23）。附魔生效：锋利/保护/效率等
// 常用项在攻击/挖掘/减伤处应用。

import (
	"math/rand"

	"mcserver/internal/assets"
	"mcserver/internal/entity"
	"mcserver/internal/packet"
	"mcserver/internal/protocol"
)

const (
	menuEnchant    int32 = 13
	enchTableState uint32 = 11185
	itemLapis      int32 = 1014
	compEnchantments int32 = 23 // minecraft:enchantments 组件 ID
)


// tryOpenEnchantTable 右键附魔台。
func (g *Game) tryOpenEnchantTable(p *entity.Player, u packet.UseItemOn) bool {
	if g.world.GetBlock(u.X, u.Y, u.Z) != enchTableState {
		return false
	}
	g.enchOpen[p.EntityID] = true
	p.Send.SendPlay("minecraft:open_screen", func(w *protocol.Writer) {
		packet.WriteOpenScreen(w, menuEnchant, "Enchant")
	})
	g.syncEnchant(p)
	return true
}

// closeEnchantTable 关闭窗口。
func (g *Game) closeEnchantTable(p *entity.Player) {
	delete(g.enchOpen, p.EntityID)
	p.Send.SendPlay("minecraft:container_close", func(w *protocol.Writer) {
		packet.WriteContainerClose(w, 5)
	})
	// 物品/青金石退回背包
	for _, it := range p.EnchSlots {
		if it.ID != 0 {
			if n := g.giveItem(p, it); n < it.Count {
				g.spawnItem(p.X, p.Y+1, p.Z, entity.Item{ID: it.ID, Count: it.Count - n})
			}
		}
	}
	p.EnchSlots = [2]entity.Item{}
	g.syncInventory(p)
}

// syncEnchant 同步附魔台窗口（2 槽 + 三档选项经 container_set_data/自定义不强求 UI）。
func (g *Game) syncEnchant(p *entity.Player) {
	slots := make([]packet.ItemSlot, 39) // 0 物品 1 青金石 2-38 物品栏
	if p.EnchSlots[0].ID != 0 {
		var el []packet.EnchPair
		for _, e := range p.EnchItem {
			el = append(el, packet.EnchPair{Name: e.Name, Level: e.Level})
		}
		slots[0] = packet.ItemSlot{ID: p.EnchSlots[0].ID, Count: p.EnchSlots[0].Count, EnchList: el}
	}
	slots[1] = packet.ItemSlot{ID: p.EnchSlots[1].ID, Count: p.EnchSlots[1].Count}
	for i := int32(2); i <= 28; i++ {
		s := p.Slots[9+(i-2)]
		slots[i] = packet.ItemSlot{ID: s.ID, Count: s.Count}
	}
	for i := int32(29); i <= 37; i++ {
		s := p.Slots[36+(i-29)]
		slots[i] = packet.ItemSlot{ID: s.ID, Count: s.Count}
	}
	p.ContainerState++
	state := p.ContainerState
	p.Send.SendPlay("minecraft:container_set_content", func(w *protocol.Writer) {
		packet.WriteContainerSetContentEnch(w, 5, state, slots, packet.ItemSlot{})
	})
	// 三档费用提示（属性 0/1/2 = 各档所需等级，seed 属性忽略）
	for i, cost := range []int16{1, 2, 3} {
		idx := int16(i)
		p.Send.SendPlay("minecraft:container_set_data", func(w *protocol.Writer) {
			packet.WriteContainerSetData(w, 5, idx, cost)
		})
	}
}

// applyEnchant 点击附魔档位（1-3）：消耗等级+青金石，随机附魔。
func (g *Game) applyEnchant(p *entity.Player, tier int32) {
	if tier < 1 || tier > 3 {
		return
	}
	item := &p.EnchSlots[0]
	if item.ID == 0 || p.EnchSlots[1].Count < tier || p.Level < tier {
		return
	}
	name := g.itemNameOf(p, item.ID)
	// 适用附魔池
	type cand struct {
		name  string
		weight int32
	}
	var pool []cand
	for en, info := range g.enchants {
		if !enchAppliesTo(info.Items, name, g) {
			continue
		}
		pool = append(pool, cand{en, info.Weight})
	}
	if len(pool) == 0 {
		return
	}
	// 加权抽取 tier 条（去重）
	picked := map[string]bool{}
	for n := int32(0); n < tier; n++ {
		var total int32
		for _, c := range pool {
			if !picked[c.name] {
				total += c.weight
			}
		}
		if total == 0 {
			break
		}
		r := rand.Int31n(total)
		for _, c := range pool {
			if picked[c.name] {
				continue
			}
			r -= c.weight
			if r < 0 {
				picked[c.name] = true
				// 等级：tier 上限 + 随机
				maxLv := g.enchants[c.name].MaxLevel
				lv := maxLv
				if maxLv > tier {
					lv = tier + rand.Int31n(maxLv-tier+1)
				}
				p.EnchItem = append(p.EnchItem, entity.EnchEntry{Name: c.name, Level: lv})
				break
			}
		}
	}
	// 消耗
	p.Level -= tier
	if p.Level < 0 {
		p.Level = 0
	}
	p.XPPending = 0
	p.EnchSlots[1].Count -= tier
	if p.EnchSlots[1].Count <= 0 {
		p.EnchSlots[1] = entity.Item{}
	}
	g.syncExperience(p)
	g.syncEnchant(p)
}

// enchAppliesTo 附魔适用性：标签（itemtags 资产）或物品名匹配。
// 附魔资产的 Items 是"enchantable/xxx"（enchantable 标签在 item tags 内）。
func enchAppliesTo(items, itemName string, g *Game) bool {
	if itemName == "" {
		return false
	}
	for _, spec := range splitComma(items) {
		if spec == itemName {
			return true
		}
		// spec 形如 "enchantable/sword"——查 itemtags（我们生成的 tag 名无斜杠前缀）
		if members, ok := g.itemTags[spec]; ok && members[itemName] {
			return true
		}
	}
	return false
}

func splitComma(s string) []string {
	var out []string
	cur := ""
	for _, c := range s {
		if c == ',' {
			out = append(out, cur)
			cur = ""
		} else {
			cur += string(c)
		}
	}
	if cur != "" {
		out = append(out, cur)
	}
	return out
}

// enchantBonus 攻击附魔加成（锋利：1+0.5*(lv-1)；亡灵杀手/节肢杀手对任意 +2.5*lv 简化）。
func (g *Game) enchantAttackBonus(p *entity.Player) float32 {
	var bonus float32
	for _, e := range p.EnchItem {
		switch e.Name {
		case "sharpness":
			bonus += 1 + 0.5*float32(e.Level-1)
		case "smite", "bane_of_arthropods":
			bonus += 2.5 * float32(e.Level)
		}
	}
	return bonus
}

// enchantArmorBonus 保护类附魔的额外减伤（每点 EPF 4%，保护 I=1/锋利保护类）。
func (g *Game) enchantArmorBonus(p *entity.Player) float32 {
	var epf float32
	for _, e := range p.EnchItem {
		switch e.Name {
		case "protection":
			epf += float32(e.Level)
		case "fire_protection", "blast_protection", "projectile_protection":
			epf += float32(e.Level) * 2
		}
	}
	if epf > 20 {
		epf = 20
	}
	return epf * 0.04
}

// writeEnchSlot 物品带附魔组件的 Slot 输出（enchantments 组件：NBT 列表）。
func writeEnchSlot(w *protocol.Writer, itemID, count int32, list []entity.EnchEntry) {
	w.Bool(true)
	w.VarInt(itemID)
	w.VarInt(count)
	if len(list) > 0 {
		w.VarInt(1) // 增补组件数
		w.VarInt(compEnchantments)
		// enchantments 组件载荷：NBT Compound {id: level}
		c := protocol.Compound{}
		for _, e := range list {
			c["minecraft:"+e.Name] = e.Level
		}
		protocol.WriteNBT(w, c)
	} else {
		w.VarInt(0)
	}
	w.VarInt(0)
}

var _ = assets.Enchants
var _ = protocol.Writer{}
