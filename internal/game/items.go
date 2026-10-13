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
	x, y, z float64
	vx, vy  float64 // 水平速度仅用于散开，落地即衰减
	vz      float64
	age     int
}

// spawnItem 在方块位置生成一个掉落物并向区块观察者广播。
func (g *Game) spawnItem(x, y, z float64, item entity.Item) {
	if item.ID == 0 || item.Count <= 0 {
		return
	}
	eid := g.nextEID.Add(1)
	ie := &itemEntity{
		eid:  eid,
		uuid: entityUUID(eid),
		item: item,
		x:    x, y: y, z: z,
		vx:   (rand.Float64() - 0.5) * 0.2,
		vy:   0.2,
		vz:   (rand.Float64() - 0.5) * 0.2,
	}
	g.items[eid] = ie

	viewers := g.chunkViewers(int32(x)>>4, int32(z)>>4)
	for _, v := range viewers {
		g.sendItemSpawn(v, ie)
	}
}

// sendItemSpawn 向单个玩家发送物品实体的生成与元数据（物品 ID 按协议翻译）。
func (g *Game) sendItemSpawn(p *entity.Player, ie *itemEntity) {
	typ := g.entityTypeID(p, "item")
	itemID := g.itemFor(p, ie.item.ID)
	meta := packet.BuildItemEntityMetadata(itemID, ie.item.Count)
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

// tickItems 推进所有掉落物：重力/碰撞/拾取/消失。
func (g *Game) tickItems() {
	for eid, ie := range g.items {
		ie.age++
		if ie.age >= itemLifetime {
			g.removeItem(eid)
			continue
		}
		// 简单物理：水平摩擦减速，下落直到碰到实体方块，落地后停在其上表面
		if ie.vy != 0 || ie.vx != 0 || ie.vz != 0 {
			ie.vy -= itemGravity
			ie.vx *= 0.98
			ie.vz *= 0.98
			if absF(ie.vx) < 0.01 {
				ie.vx = 0
			}
			if absF(ie.vz) < 0.01 {
				ie.vz = 0
			}
			nx, ny, nz := ie.x+ie.vx, ie.y+ie.vy, ie.z+ie.vz
			if hit := floorI(ny - 0.06); g.blockAt(floorI(nx), hit, floorI(nz)) {
				ny = float64(hit) + 1.125 // 方块上表面 + 物品半高
				ie.vy, ie.vx, ie.vz = 0, 0, 0
			}
			ie.x, ie.y, ie.z = nx, ny, nz
		}
		// 拾取：任意在线玩家靠近（水平 1.2 格，垂直覆盖身体 ±缓冲）
		if ie.age < itemPickupWait {
			continue
		}
		for _, p := range g.players {
			dh := (p.X-ie.x)*(p.X-ie.x) + (p.Z-ie.z)*(p.Z-ie.z)
			if dh < 1.44 && ie.y > p.Y-1.0 && ie.y < p.Y+2.0 {
				if n := g.giveItem(p, ie.item); n > 0 {
					g.broadcastTakeItem(ie, p, n)
					if n >= ie.item.Count {
						g.removeItem(eid)
					} else {
						ie.item.Count -= n
					}
				}
				break
			}
		}
	}
}

// blockAt 判断世界坐标处是否为实体方块（物品站立判定，简化为非空气非水）。
func (g *Game) blockAt(x, y, z int32) bool {
	s := g.world.GetBlock(x, y, z)
	return s != world.StateAir && s != world.StateWater
}

// removeItem 广播移除并删除掉落物。
func (g *Game) removeItem(eid int32) {
	delete(g.items, eid)
	for _, p := range g.players {
		p.Send.SendPlay("minecraft:remove_entities", func(w *protocol.Writer) {
			packet.WriteRemoveEntities(w, []int32{eid})
		})
	}
}

// broadcastTakeItem 通知观察者物品被拾取（飞向玩家的动画）。
func (g *Game) broadcastTakeItem(ie *itemEntity, collector *entity.Player, count int32) {
	for _, p := range g.players {
		p.Send.SendPlay("minecraft:take_item_entity", func(w *protocol.Writer) {
			packet.WriteTakeItemEntity(w, ie.eid, collector.EntityID, count)
		})
	}
}

// giveItem 把物品（777 基准 ID）放入玩家物品栏（先叠加同 ID 槽，再找空位；
// 快捷栏优先），入包时翻译到玩家协议的物品 ID。返回放入数量。
func (g *Game) giveItem(p *entity.Player, item entity.Item) int32 {
	local := entity.Item{ID: g.itemFor(p, item.ID), Count: item.Count}
	remain := local.Count
	for _, span := range [][2]int32{{36, 45}, {9, 36}} {
		for idx := span[0]; idx < span[1] && remain > 0; idx++ {
			s := &p.Slots[idx]
			if s.ID == local.ID && s.Count > 0 && s.Count < itemMaxStack {
				add := min32(itemMaxStack-s.Count, remain)
				s.Count += add
				remain -= add
			}
		}
	}
	for _, span := range [][2]int32{{36, 45}, {9, 36}} {
		for idx := span[0]; idx < span[1] && remain > 0; idx++ {
			if p.Slots[idx].ID == 0 {
				add := min32(itemMaxStack, remain)
				p.Slots[idx] = entity.Item{ID: local.ID, Count: add}
				remain -= add
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

func floorI(v float64) int32 { return int32(math.Floor(v)) }

func absF(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}

