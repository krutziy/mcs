package game

// 物品实体：挖掘掉落生成、简单重力、靠近拾取、超时消失。
// 客户端会自行模拟物品实体的物理（生成包带速度即可），服务端只维护
// 近似轨迹用于拾取判定与消失计时，不做逐帧位置广播。

import (
	"crypto/md5"
	"fmt"
	"math"
	"math/rand"

	"mcserver/internal/entity"
	"mcserver/internal/packet"
	"mcserver/internal/protocol"
	"mcserver/internal/world"
)

// itemEntity 常量：重力/摩擦取原版物品实体的近似值。
const (
	itemGravity    = 0.04
	itemPickupWait = 10   // 出生后 0.5 秒才能被拾取（原版 10 刻）
	itemLifetime   = 6000 // 5 分钟消失
	itemMaxStack   = 64
)

// itemEntity 是一个掉落物。
type itemEntity struct {
	eid     int32
	uuid    protocol.UUID
	item    entity.Item
	dim     int32
	x, y, z float64
	vx, vy  float64 // 水平速度仅用于散开，落地即衰减
	vz      float64
	age     int
}

// spawnItem 在指定维度生成一个掉落物并向区块观察者广播。
func (g *Game) spawnItem(dim int32, x, y, z float64, item entity.Item) {
	if item.ID == 0 || item.Count <= 0 {
		return
	}
	eid := g.nextEID.Add(1)
	ie := &itemEntity{
		eid:  eid,
		uuid: entityUUID(eid),
		item: item,
		dim:  dim,
		x:    x, y: y, z: z,
		vx:   (rand.Float64() - 0.5) * 0.2,
		vy:   0.2,
		vz:   (rand.Float64() - 0.5) * 0.2,
	}
	g.items[eid] = ie

	viewers := g.chunkViewers(dim, int32(x)>>4, int32(z)>>4)
	for _, v := range viewers {
		g.sendItemSpawn(v, ie)
	}
}

// sendItemSpawn 向单个玩家发送物品实体的生成与元数据（物品 ID 按协议翻译；
// 耐久/附魔随组件下发）。
func (g *Game) sendItemSpawn(p *entity.Player, ie *itemEntity) {
	typ := g.entityTypeID(p, "item")
	itemID := g.itemFor(p, ie.item.ID)
	meta := packet.BuildItemEntityMetadata(itemID, ie.item.Count, ie.item.Damage, enchPairsOf(ie.item.Ench))
	p.Send.SendPlay("minecraft:add_entity", func(w *protocol.Writer) {
		packet.SpawnEntity{
			EntityID: ie.eid, UUID: ie.uuid, Type: typ,
			X: ie.x, Y: ie.y, Z: ie.z,
			VX: int16(ie.vx * 8000), VY: int16(ie.vy * 8000), VZ: int16(ie.vz * 8000),
		}.Write(w)
	})
	p.Send.SendPlay("minecraft:set_entity_data", func(w *protocol.Writer) {
		packet.WriteSetEntityData(w, ie.eid, meta)
	})
}

// stackOf 玩家协议物品 ID → 最大堆叠（未知物品 64）。
func (g *Game) stackOf(p *entity.Player, id int32) int32 {
	if name := g.itemNameOf(p, id); name != "" {
		if n, ok := g.stacks[name]; ok {
			return n
		}
	}
	return itemMaxStack
}

// stackOf777 基准(777)物品 ID → 最大堆叠（掉落物合并用）。
func (g *Game) stackOf777(id int32) int32 {
	if name := g.itemNameOf777(id); name != "" {
		if n, ok := g.stacks[name]; ok {
			return n
		}
	}
	return itemMaxStack
}

// tickItems 推进所有掉落物：重力/水中浮力/同物合并/拾取/消失。
func (g *Game) tickItems() {
	for _, ie := range g.items {
		ie.age++
		if ie.age >= itemLifetime {
			g.removeItem(ie)
			continue
		}
		// 简单物理：水平摩擦减速，下落直到碰到实体方块，落地后停在其上表面
		if ie.vy != 0 || ie.vx != 0 || ie.vz != 0 {
			st := g.blockStateAt(floorI(ie.x), floorI(ie.y), floorI(ie.z))
			inWater := st >= world.WaterMin && st <= world.WaterMax
			if inWater {
				// 水面漂浮：浮力抵消重力，缓慢上浮到液面
				ie.vy += itemGravity * 1.2
				if ie.vy > 0.06 {
					ie.vy = 0.06
				}
				ie.vx *= 0.9
				ie.vz *= 0.9
			} else {
				ie.vy -= itemGravity
				ie.vx *= 0.98
				ie.vz *= 0.98
			}
			if absF(ie.vx) < 0.01 {
				ie.vx = 0
			}
			if absF(ie.vz) < 0.01 {
				ie.vz = 0
			}
			nx, ny, nz := ie.x+ie.vx, ie.y+ie.vy, ie.z+ie.vz
			if hit := floorI(ny - 0.06); ie.vy < 0 && g.blockAtDim(ie.dim, floorI(nx), hit, floorI(nz)) {
				ny = float64(hit) + 1.125 // 方块上表面 + 物品半高
				ie.vy, ie.vx, ie.vz = 0, 0, 0
			}
			ie.x, ie.y, ie.z = nx, ny, nz
		}
		// 拾取：同维度玩家靠近（水平 1.2 格，垂直覆盖身体 ±缓冲）
		if ie.age < itemPickupWait {
			continue
		}
		for _, p := range g.players {
			if p.Dim != ie.dim {
				continue
			}
			dh := (p.X-ie.x)*(p.X-ie.x) + (p.Z-ie.z)*(p.Z-ie.z)
			if dh < 1.44 && ie.y > p.Y-1.0 && ie.y < p.Y+2.0 {
				if n := g.giveItem(p, entity.Item{ID: g.itemFor(p, ie.item.ID), Count: ie.item.Count,
				Damage: ie.item.Damage, Ench: ie.item.Ench}); n > 0 {
					g.playSoundTo(p, SoundPlayer, "entity.item.pickup", 0.4, 0.9+rand.Float32()*0.3)
					g.broadcastTakeItem(ie, p, n)
					if n >= ie.item.Count {
						g.removeItem(ie)
					} else {
						ie.item.Count -= n
					}
				}
				break
			}
		}
	}
	g.mergeItems()
}

// mergeItems 合并贴近的同 ID 掉落物（距离 < 0.6，合计不超堆叠上限）。
func (g *Game) mergeItems() {
	type key struct{ x, y, z int32 }
	buckets := map[key][]int32{}
	for eid, ie := range g.items {
		if ie.age < itemPickupWait {
			continue // 刚出生的带散开速度，稳定后再合并
		}
		buckets[key{floorI(ie.x), floorI(ie.y), floorI(ie.z)}] =
			append(buckets[key{floorI(ie.x), floorI(ie.y), floorI(ie.z)}], eid)
	}
	for _, eids := range buckets {
		if len(eids) < 2 {
			continue
		}
		for i := 0; i < len(eids); i++ {
			a := g.items[eids[i]]
			if a == nil {
				continue
			}
			for j := i + 1; j < len(eids); j++ {
				b := g.items[eids[j]]
				if b == nil || a.item.ID != b.item.ID {
					continue
				}
				if a.item.Damage != b.item.Damage || len(a.item.Ench) > 0 || len(b.item.Ench) > 0 {
					continue // 耐久/附魔差异不合并
				}
				limit := g.stackOf777(a.item.ID)
				if a.item.Count+b.item.Count > limit {
					continue
				}
				if dist2(a.x, a.y, a.z, b.x, b.y, b.z) > 0.36 {
					continue
				}
				// 合并到 a，移除 b，a 用元数据更新数量（无重生闪烁）
				a.item.Count += b.item.Count
				g.removeItem(b)
				delete(g.items, eids[j])
				eid2 := eids[i]
				for _, v := range g.chunkViewers(a.dim, floorI(a.x)>>4, floorI(a.z)>>4) {
					itemID := g.itemFor(v, a.item.ID)
					meta := packet.BuildItemEntityMetadata(itemID, a.item.Count, a.item.Damage, enchPairsOf(a.item.Ench))
					v.Send.SendPlay("minecraft:set_entity_data", func(w *protocol.Writer) {
						packet.WriteSetEntityData(w, eid2, meta)
					})
				}
			}
		}
	}
}

// blockStateAt 取世界方块状态 ID（物品/环境判定用）。
func (g *Game) blockStateAt(x, y, z int32) uint32 {
	return g.world.GetBlock(x, y, z)
}

// blockAtDim 指定维度的实体方块判定（物品/生物站立）。
func (g *Game) blockAtDim(dim, x, y, z int32) bool {
	s := g.wd(dim).GetBlock(x, y, z)
	return s != world.StateAir && s != world.StateWater
}

// blockAt 判断世界坐标处是否为实体方块（物品站立判定，简化为非空气非水）。
func (g *Game) blockAt(x, y, z int32) bool {
	s := g.world.GetBlock(x, y, z)
	return s != world.StateAir && s != world.StateWater
}

// removeItem 广播移除并删除掉落物（同维度观察者）。
func (g *Game) removeItem(ie *itemEntity) {
	delete(g.items, ie.eid)
	eid := ie.eid
	for _, p := range g.players {
		if p.Dim != ie.dim {
			continue
		}
		p.Send.SendPlay("minecraft:remove_entities", func(w *protocol.Writer) {
			packet.WriteRemoveEntities(w, []int32{eid})
		})
	}
}

// broadcastTakeItem 通知观察者物品被拾取（飞向玩家的动画）。
func (g *Game) broadcastTakeItem(ie *itemEntity, collector *entity.Player, count int32) {
	for _, p := range g.players {
		if p.Dim != ie.dim {
			continue
		}
		p.Send.SendPlay("minecraft:take_item_entity", func(w *protocol.Writer) {
			packet.WriteTakeItemEntity(w, ie.eid, collector.EntityID, count)
		})
	}
}

// giveItem 把物品放入玩家物品栏（先叠加同 ID 槽，再找空位；快捷栏优先）。
// item 的 ID 必须已是该玩家协议空间（777 基准调用方先经 itemFor 翻译，
// 掉落物拾取路径见 pickUpItem）。返回放入数量。
// 带附魔/耐久的物品按不可堆叠处理（各占一槽）。
func (g *Game) giveItem(p *entity.Player, item entity.Item) int32 {
	local := item
	limit := g.stackOf(p, local.ID)
	stackable := len(local.Ench) == 0 && local.Damage == 0
	remain := local.Count
	if stackable {
		for _, span := range [][2]int32{{36, 45}, {9, 36}} {
			for idx := span[0]; idx < span[1] && remain > 0; idx++ {
				s := &p.Slots[idx]
				if s.ID == local.ID && s.Count > 0 && s.Count < limit && len(s.Ench) == 0 {
					add := min32(limit-s.Count, remain)
					s.Count += add
					remain -= add
				}
			}
		}
	}
	if remain > 0 {
		// 空位放置（不可堆叠物品每槽 1 件：count 按 1 消耗）
		per := int32(1)
		if stackable {
			per = limit
		}
		for _, span := range [][2]int32{{36, 45}, {9, 36}} {
			for idx := span[0]; idx < span[1] && remain > 0; idx++ {
				if p.Slots[idx].ID == 0 {
					one := entity.Item{ID: local.ID, Damage: local.Damage, Ench: local.Ench}
					if stackable {
						add := min32(per, remain)
						one.Count = add
						remain -= add
					} else {
						one.Count = 1
						remain--
					}
					p.Slots[idx] = one
				}
			}
		}
	}
	given := local.Count - remain
	if given > 0 {
		g.syncInventory(p)
	}
	return given
}

// entityUUID 由实体 ID 派生确定性 UUID。
func entityUUID(eid int32) protocol.UUID {
	sum := md5.Sum([]byte(fmt.Sprintf("mcs-entity:%d", eid)))
	sum[6] = (sum[6] & 0x0f) | 0x30
	sum[8] = (sum[8] & 0x3f) | 0x80
	var u protocol.UUID
	copy(u[:], sum[:])
	return u
}

func min32(a, b int32) int32 {
	if a < b {
		return a
	}
	return b
}

func dist2(x1, y1, z1, x2, y2, z2 float64) float64 {
	dx, dy, dz := x1-x2, y1-y2, z1-z2
	return dx*dx + dy*dy + dz*dz
}

func floorI(v float64) int32 { return int32(math.Floor(v)) }

func absF(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}

