package game

// 玩家成长类：经验值（挖矿/杀怪/熔炼 + 经验球实体 + set_experience）、
// 工具耐久（damage 组件 + 破坏）、盔甲减伤（armor 点数 + set_equipment 广播）、
// 死亡掉落（物品栏 + 经验）。数值取原版公开数值。

import (
	"math/rand"

	"mcserver/internal/entity"
	"mcserver/internal/packet"
	"mcserver/internal/protocol"
)

const entityXPOrb int32 = 50

// xpOrb 是一个经验球（复用掉落物拾取模型）。
type xpOrb struct {
	eid    int32
	uuid   protocol.UUID
	amount int32
	x, y, z, vy float64
	age    int
}

// 矿石挖掘经验（公开数值；掉落由战利品表负责）。
var oreXP = map[string]int32{
	"coal_ore": 2, "deepslate_coal_ore": 2,
	"diamond_ore": 5, "deepslate_diamond_ore": 5,
	"emerald_ore": 5, "lapis_ore": 3, "redstone_ore": 2,
}

// 生物击杀经验。
var mobXP = map[string]int32{"pig": 2, "sheep": 2, "cow": 2, "chicken": 1, "rabbit": 1, "zombie": 5}

// 熔炼产物经验（每件，简化整数）。
var smeltXP = map[string]int32{"iron_ingot": 1, "gold_ingot": 1, "copper_ingot": 1,
	"glass": 0, "cooked_beef": 1, "cooked_porkchop": 1, "cooked_chicken": 1, "charcoal": 0}

// 工具/武器最大耐久（公开数值）。
var maxDurability = map[string]int32{
	"wooden": 59, "golden": 32, "stone": 131, "copper": 190,
	"iron": 250, "diamond": 1561, "netherite": 2031,
}

// durabilityOf 物品名 → 最大耐久（0 = 无耐久条）。
func durabilityOf(name string) int32 {
	for mat, d := range maxDurability {
		if len(name) > len(mat)+1 && name[:len(mat)] == mat {
			switch name[len(mat)+1:] {
			case "sword", "pickaxe", "axe", "shovel", "hoe":
				return d
			}
		}
	}
	switch name {
	case "bow", "fishing_rod", "carrot_on_a_stick", "warped_fungus_on_a_stick":
		return 64
	case "shears", "flint_and_steel":
		return 64
	case "shield":
		return 336
	}
	return 0
}

// 盔甲点数：[皮革/锁链/铁/钻/金/合金] × [头/胸/腿/靴]。
type armorSet struct{ helmet, chest, legs, boots int32 }

var armorPoints = map[string]armorSet{
	"leather":    {1, 3, 2, 1},
	"chainmail":  {2, 5, 4, 1},
	"iron":       {2, 6, 5, 2},
	"diamond":    {3, 8, 6, 3},
	"golden":     {2, 5, 3, 1},
	"netherite":  {3, 8, 6, 3},
	"turtle_shell": {2, 0, 0, 0},
}

// armorPointOf 盔甲物品名 → 点数（0 = 非盔甲）。
func armorPointOf(name string) int32 {
	if a, ok := armorPoints[name]; ok {
		return a.helmet
	}
	for mat, a := range armorPoints {
		if len(name) > len(mat)+1 && name[:len(mat)] == mat {
			switch name[len(mat)+1:] {
			case "helmet", "turtle_shell":
				return a.helmet
			case "chestplate":
				return a.chest
			case "leggings":
				return a.legs
			case "boots":
				return a.boots
			}
		}
	}
	return 0
}

// totalArmor 玩家盔甲槽（5-8）总点数（物品为玩家协议空间，经名字判定）。
func (g *Game) totalArmor(p *entity.Player) int32 {
	var sum int32
	for i := int32(5); i <= 8; i++ {
		if p.Slots[i].ID != 0 {
			sum += armorPointOf(g.itemNameOf(p, p.Slots[i].ID))
		}
	}
	if sum > 20 {
		sum = 20
	}
	return sum
}

// damageHeld 使用手持工具一次（耐久 -1，破坏则移除并同步）。
func (g *Game) damageHeld(p *entity.Player) {
	if g.isCreative(p) {
		return
	}
	s := &p.Slots[36+p.HeldSlot]
	if s.ID == 0 {
		return
	}
	max := durabilityOf(g.itemNameOf(p, s.ID))
	if max == 0 {
		return // 无耐久条
	}
	s.Damage++
	if s.Damage >= max {
		*s = entity.Item{} // 工具破坏
	}
	g.syncInventory(p)
}

// Item.Damage 语义：已损失耐久（0=全新），累加到 max 破坏（与 wire 的 damage 组件一致）。
func (g *Game) ensureDurability(p *entity.Player) {
	s := &p.Slots[36+p.HeldSlot]
	if s.ID == 0 || s.Damage != 0 {
		return
	}
	name := g.itemNameOf(p, s.ID)
	if max := durabilityOf(name); max > 0 {
		s.Damage = max
	}
}

// giveXP 给玩家经验并同步（升级公式：每级 7+2*level 点，简化线性）。
func (g *Game) giveXP(p *entity.Player, amount int32) {
	p.TotalXP += amount
	p.XPPending += amount
	for p.XPPending >= xpToNext(p.Level) {
		p.XPPending -= xpToNext(p.Level)
		p.Level++
	}
	g.syncExperience(p)
}

func xpToNext(level int32) int32 { return 7 + level*2 }

// syncExperience 同步 set_experience（进度 = 本级内比例）。
func (g *Game) syncExperience(p *entity.Player) {
	lv, pend := p.Level, p.XPPending
	progress := float32(pend) / float32(xpToNext(lv))
	total := p.TotalXP
	p.Send.SendPlay("minecraft:set_experience", func(w *protocol.Writer) {
		w.Float(progress)
		w.VarInt(lv)
		w.VarInt(total)
	})
}

// spawnXPOrb 生成经验球。
func (g *Game) spawnXPOrb(x, y, z float64, amount int32) {
	eid := g.nextEID.Add(1)
	o := &xpOrb{eid: eid, uuid: entityUUID(eid), amount: amount,
		x: x, y: y + 0.25, z: z, vy: 0.2}
	g.orbs[eid] = o
	for _, v := range g.chunkViewers(int32(x)>>4, int32(z)>>4) {
		typ := g.entityTypeID(v, "experience_orb")
		eid2, xx, yy, zz := eid, x, o.y, z
		v.Send.SendPlay("minecraft:add_entity", func(w *protocol.Writer) {
			packet.SpawnEntity{EntityID: eid2, UUID: o.uuid, Type: typ,
				X: xx, Y: yy, Z: zz}.Write(w)
		})
	}
}

// tickOrbs 经验球物理 + 拾取。
func (g *Game) tickOrbs() {
	for eid, o := range g.orbs {
		o.age++
		if o.age >= itemLifetime {
			g.removeOrb(eid)
			continue
		}
		if o.vy != 0 {
			o.vy -= itemGravity
			ny := o.y + o.vy
			if g.blockAt(floorI(o.x), floorI(ny-0.06), floorI(o.z)) {
				ny = float64(floorI(ny-0.06)) + 1.25
				o.vy = 0
			}
			o.y = ny
		}
		if o.age < itemPickupWait {
			continue
		}
		for _, p := range g.players {
			if p.Dead {
				continue
			}
			dh := (p.X-o.x)*(p.X-o.x) + (p.Z-o.z)*(p.Z-o.z)
			if dh < 2.25 && o.y > p.Y-1 && o.y < p.Y+2 {
				g.giveXP(p, o.amount)
				eid2, collector := eid, p.EntityID
				for _, v := range g.players {
					v.Send.SendPlay("minecraft:take_item_entity", func(w *protocol.Writer) {
						packet.WriteTakeItemEntity(w, eid2, collector, 1)
					})
				}
				g.removeOrb(eid)
				break
			}
		}
	}
}

func (g *Game) removeOrb(eid int32) {
	delete(g.orbs, eid)
	for _, p := range g.players {
		p.Send.SendPlay("minecraft:remove_entities", func(w *protocol.Writer) {
			packet.WriteRemoveEntities(w, []int32{eid})
		})
	}
}

// armorAbsorb 伤害经盔甲减免（每点护甲 -4%，最多 80%）。
func (g *Game) armorAbsorb(p *entity.Player, amount float32) float32 {
	pts := g.totalArmor(p)
	if pts <= 0 {
		return amount
	}
	return amount * (1 - float32(pts)*0.04)
}

// broadcastEquipment 向其他人广播玩家装备（手持 + 盔甲）。
func (g *Game) broadcastEquipment(p *entity.Player) {
	eid := p.EntityID
	held := p.Held()
	head, chest, legs, boots := p.Slots[5], p.Slots[6], p.Slots[7], p.Slots[8]
	for _, other := range g.players {
		if other.EntityID == eid {
			continue
		}
		other.Send.SendPlay("minecraft:set_equipment", func(w *protocol.Writer) {
			w.VarInt(eid)
			w.Int8(0) // 主手
			if held.ID == 0 {
				packet.WriteEmptySlot(w)
			} else {
				packet.WriteSlot(w, held.ID, held.Count)
			}
			w.Int8(5) // 头
			writeSlotOrEmpty(w, head)
			w.Int8(4) // 胸
			writeSlotOrEmpty(w, chest)
			w.Int8(3) // 腿
			writeSlotOrEmpty(w, legs)
			w.Int8(2) // 靴
			writeSlotOrEmpty(w, boots)
			w.Int8(-1) // 结束
		})
	}
}

func writeSlotOrEmpty(w *protocol.Writer, it entity.Item) {
	if it.ID == 0 {
		packet.WriteEmptySlot(w)
	} else {
		packet.WriteSlot(w, it.ID, it.Count)
	}
}

// dropInventoryOnDeath 死亡掉落：物品栏 + 盔甲全掉，经验折半掉球。
func (g *Game) dropInventoryOnDeath(p *entity.Player) {
	for i := range p.Slots {
		if p.Slots[i].ID != 0 {
			it := p.Slots[i]
			g.spawnItem(p.X, p.Y+0.5, p.Z, it)
			p.Slots[i] = entity.Item{}
		}
	}
	p.Cursor = entity.Item{}
	if xp := p.TotalXP / 2; xp > 0 {
		for xp > 0 {
			n := int32(1 + rand.Intn(3))
			if n > xp {
				n = xp
			}
			g.spawnXPOrb(p.X, p.Y+0.5, p.Z, n)
			xp -= n
		}
	}
	p.TotalXP, p.Level, p.XPPending = 0, 0, 0
	g.syncInventory(p)
}
