package game

// 敌对生物扩展：骷髅（远程箭矢实体）、苦力怕（引信-爆炸，半径破坏+伤害+音效）、
// 亡灵生物白天燃烧；树叶枯萎（distance 状态传播，>7 消失并掉树苗）。

import (
	"math"
	"math/rand"

	"mcserver/internal/protocol"

	"mcserver/internal/entity"
	"mcserver/internal/packet"
	"mcserver/internal/world"
)

const (
	entityArrow   int32 = 6
	entityCreeper int32 = 32
	entitySkeleton int32 = 118
)

// 苦力怕元数据 index：0=引信状态（VarInt 30=点燃），state=swell。
const creeperFuseMeta byte = 0

// mob 扩展字段（嵌入 mobs.go 的 mob 结构由 build tag 分离不便，直接在此定义扩展 map）。

// creeperState 苦力怕的引信状态（按 eid）。
// fuse > 0 = 点燃倒计时（1.5 秒 = 30 刻）。
type creeperFuse struct {
	fuse int32
}

// arrow 是一支飞行中的箭。
type arrow struct {
	eid        int32
	uuid       protocol.UUID
	owner      int32
	x, y, z    float64
	vx, vy, vz float64
	age        int
}

// tickHostileAI 敌对生物扩展行为：骷髅射箭、苦力怕引信。
func (g *Game) tickHostileAI(m *mob) {
	if m.kind.name == "skeleton" {
		target := g.nearestPlayer(m.x, m.y, m.z, 15)
		if target == nil || target.Dead {
			return
		}
		if m.attackCd > 0 {
			m.attackCd--
			return
		}
		// 与玩家保持 8 格内射箭
		d := math.Sqrt(dist2(m.x, m.y, m.z, target.X, target.Y, target.Z))
		if d < 15 {
			g.shootArrow(m, target)
			m.attackCd = 40 // 2 秒一箭
		}
		return
	}
	if m.kind.name == "creeper" {
		target := g.nearestPlayer(m.x, m.y, m.z, 3)
		_, has := g.creepers[m.eid]
		if target != nil && !target.Dead {
			if !has {
				g.creepers[m.eid] = &creeperFuse{fuse: 30}
				g.setCreeperSwell(m, 1) // 点燃动画
			}
		} else if has {
			// 玩家离开 → 熄火
			delete(g.creepers, m.eid)
			g.setCreeperSwell(m, -1)
		}
	}
}

// shootArrow 骷髅朝目标射一箭（初速 0.6 格/刻，带垂直补偿）。
func (g *Game) shootArrow(m *mob, target *entity.Player) {
	eid := g.nextEID.Add(1)
	dx, dy, dz := target.X-m.x, (target.Y+1)-(m.y+1.5), target.Z-m.z
	d := math.Sqrt(dx*dx + dy*dy + dz*dz)
	if d < 0.5 {
		d = 0.5
	}
	speed := 0.4
	ar := &arrow{eid: eid, owner: m.eid,
		x: m.x, y: m.y + 1.5, z: m.z,
		vx: dx / d * speed, vy: dy/d*speed + d*0.02, vz: dz / d * speed}
	ar.uuid = entityUUID(eid)
	g.arrows[eid] = ar
	for _, v := range g.chunkViewers(int32(m.x)>>4, int32(m.z)>>4) {
		typ := g.entityTypeID(v, "arrow")
		eid2, x, y, z := eid, ar.x, ar.y, ar.z
		vx, vy, vz := int16(ar.vx*8000), int16(ar.vy*8000), int16(ar.vz*8000)
		v.Send.SendPlay("minecraft:add_entity", func(w *protocol.Writer) {
			packet.SpawnEntity{EntityID: eid2, UUID: ar.uuid, Type: typ,
				X: x, Y: y, Z: z, VX: vx, VY: vy, VZ: vz, ObjectData: m.eid}.Write(w)
		})
	}
}

// tickArrows 箭矢飞行：重力、命中玩家/生物/方块。
func (g *Game) tickArrows() {
	for eid, ar := range g.arrows {
		ar.age++
		if ar.age > 1200 {
			g.removeArrow(eid)
			continue
		}
		ar.vy -= 0.05
		ox, oy, oz := ar.x, ar.y, ar.z
		nx, ny, nz := ar.x+ar.vx, ar.y+ar.vy, ar.z+ar.vz
		// 子步进：每 0.25 位移采样一次命中
		steps := 4
		removed := false
		for st := 1; st <= steps && !removed; st++ {
			f := float64(st) / float64(steps)
			sx, sy, sz := ox+(nx-ox)*f, oy+(ny-oy)*f, oz+(nz-oz)*f
			if g.blockAt(floorI(sx), floorI(sy), floorI(sz)) {
				g.removeArrow(eid)
				removed = true
				break
			}
			for _, p := range g.players {
				if p.Dead || p.EntityID == ar.owner {
					continue
				}
				if math.Abs(p.X-sx) < 0.7 && math.Abs(p.Z-sz) < 0.7 && sy > p.Y && sy < p.Y+2.0 {
					g.damage(p, 3, damageGeneric)
					g.removeArrow(eid)
					removed = true
					break
				}
			}
			if removed {
				break
			}
			for mid, m := range g.mobs {
				if mid == ar.owner {
					continue
				}
				if math.Abs(m.x-sx) < 0.8 && math.Abs(m.z-sz) < 0.8 && sy > m.y && sy < m.y+2.0 {
					m.health -= 3
					if m.health <= 0 {
						g.killMob(m, nil)
					}
					g.removeArrow(eid)
					removed = true
					break
				}
			}
		}
		if removed {
			continue
		}
		ar.x, ar.y, ar.z = nx, ny, nz
	}
}

func (g *Game) removeArrow(eid int32) {
	delete(g.arrows, eid)
	for _, p := range g.players {
		p.Send.SendPlay("minecraft:remove_entities", func(w *protocol.Writer) {
			packet.WriteRemoveEntities(w, []int32{eid})
		})
	}
}

// setCreeperSwell 设置苦力怕引信状态元数据（30=膨胀开始，-1 重置）。
func (g *Game) setCreeperSwell(m *mob, state int32) {
	eid := m.eid
	for _, v := range g.players {
		v.Send.SendPlay("minecraft:set_entity_data", func(w *protocol.Writer) {
			w.VarInt(eid)
			w.Uint8(creeperFuseMeta)
			w.VarInt(31) // VarInt 类型
			w.VarInt(state)
			w.Uint8(0xFF)
		})
	}
}

// tickCreepers 引信推进与爆炸。
func (g *Game) tickCreepers() {
	for eid, f := range g.creepers {
		f.fuse--
		if f.fuse > 0 {
			continue
		}
		m := g.mobs[eid]
		delete(g.creepers, eid)
		if m == nil {
			continue
		}
		g.explode(m.x, m.y+0.5, m.z, 3)
		g.killMobSilent(m)
	}
}

// killMobSilent 无掉落移除（爆炸自灭）。
func (g *Game) killMobSilent(m *mob) {
	delete(g.mobs, m.eid)
	eid := m.eid
	for _, p := range g.players {
		p.Send.SendPlay("minecraft:remove_entities", func(w *protocol.Writer) {
			packet.WriteRemoveEntities(w, []int32{eid})
		})
	}
}

// explode 爆炸：半径内破坏非防爆方块 + 伤害随距离衰减 + 音效粒子。
func (g *Game) explode(x, y, z float64, r int32) {
	// 爆炸音效与粒子（level_event 1021=爆炸音）
	for _, v := range g.chunkViewers(int32(x)>>4, int32(z)>>4) {
		v.Send.SendPlay("minecraft:level_event", func(w *protocol.Writer) {
			packet.WriteLevelEvent(w, 1021, int32(x), int32(y), int32(z), 0, false)
		})
	}
	// 方块破坏（外圈随机保留，原版观感）
	cx, cy, cz := floorI(x), floorI(y), floorI(z)
	var changes [][3]int32
	for dx := -r; dx <= r; dx++ {
		for dy := -r; dy <= r; dy++ {
			for dz := -r; dz <= r; dz++ {
				if dx*dx+dy*dy+dz*dz > r*r {
					continue
				}
				bx, by, bz := cx+dx, cy+dy, cz+dz
				s := g.world.GetBlock(bx, by, bz)
				if s == world.StateAir || s == world.StateBedrock {
					continue
				}
				changes = append(changes, [3]int32{bx, by, bz})
			}
		}
	}
	for _, pos := range changes {
		g.dropFurnaceContents(pos)
		g.dropChestContents(pos)
		g.world.SetBlock(pos[0], pos[1], pos[2], world.StateAir)
		g.recordBlockChange(pos[0], pos[1], pos[2], world.StateAir)
	}
	g.flushBlockUpdates()
	// 伤害（中心 30 线性衰减到边缘 0；盔甲参与减免）
	for _, p := range g.players {
		if p.Dead {
			continue
		}
		d := math.Sqrt(dist2(p.X, p.Y, p.Z, x, y, z))
		if d <= float64(r)+1 {
			dmg := float32(24 * (1 - d/(float64(r)+1)))
			if dmg > 0 {
				g.damage(p, dmg, damageGeneric)
			}
		}
	}
	// 生物伤害与部分掉落
	for _, m := range g.mobs {
		d := math.Sqrt(dist2(m.x, m.y, m.z, x, y, z))
		if d <= float64(r)+1 {
			m.health -= float32(24 * (1 - d/(float64(r)+1)))
			if m.health <= 0 {
				g.killMobSilent(m)
			}
		}
	}
	// 触发重力与流水
	for _, pos := range changes {
		g.triggerGravity(pos[0], pos[1]+1, pos[2])
		g.scheduleWaterFlow(pos[0], pos[1], pos[2])
		g.scheduleLeafCheck(pos[0], pos[1], pos[2])
	}
}

// ---- 亡灵白天燃烧 ----

// tickUndeadBurn 白天（天光充足时段）亡灵（僵尸/骷髅）每秒 1 点火伤，
// 头顶无遮挡判定简化为 y >= 地表。
func (g *Game) tickUndeadBurn() {
	if g.tick%20 != 0 {
		return
	}
	daylight := g.dayTime < 12541 || g.dayTime > 23458
	if !daylight {
		return
	}
	for _, m := range g.mobs {
		if m.kind.name != "zombie" && m.kind.name != "skeleton" {
			continue
		}
		// 头顶遮挡：向上找 24 格内实体方块
		blocked := false
		for dy := int32(2); dy < 26; dy++ {
			if g.blockAt(floorI(m.x), floorI(m.y)+dy, floorI(m.z)) {
				blocked = true
				break
			}
		}
		if blocked {
			continue
		}
		m.health -= 1
		if m.health <= 0 {
			g.killMobSilent(m)
		}
	}
}

// ---- 树叶枯萎 ----

// leavesDistance 树叶 distance 值（persistent=false, waterlogged=false 状态组）。
var leavesDistance = [8]uint32{0, 270, 274, 278, 282, 286, 290, 294}

// scheduleLeafCheck 登记需重算 distance 的叶子（方块变更周围）。
func (g *Game) scheduleLeafCheck(x, y, z int32) {
	for dx := int32(-1); dx <= 1; dx++ {
		for dy := int32(-1); dy <= 1; dy++ {
			for dz := int32(-1); dz <= 1; dz++ {
				s := g.world.GetBlock(x+dx, y+dy, z+dz)
				if isLeaves(s) {
					g.leafQueue = append(g.leafQueue, [3]int32{x + dx, y + dy, z + dz})
				}
			}
		}
	}
}

func isLeaves(s uint32) bool {
	for i := 1; i < len(leavesDistance); i++ {
		if s == leavesDistance[i] {
			return true
		}
	}
	return false
}

// leavesDist 取叶子当前 distance（非叶子返回 0）。
func leavesDist(s uint32) int32 {
	for i := 1; i < len(leavesDistance); i++ {
		if s == leavesDistance[i] {
			return int32(i)
		}
	}
	return 0
}

// tickLeafDecay 每 10 刻处理叶子队列：重算 distance（邻叶/原木最近 +1），
// >7 消失并概率掉树苗（原版 5%）。
func (g *Game) tickLeafDecay() {
	if g.tick%10 != 0 || len(g.leafQueue) == 0 {
		return
	}
	queue := g.leafQueue
	g.leafQueue = nil
	for _, pos := range queue {
		s := g.world.GetBlock(pos[0], pos[1], pos[2])
		if !isLeaves(s) {
			continue
		}
		// 邻域最小支撑 distance（原木=0 → 叶=1）
		best := int32(15)
		for dx := int32(-1); dx <= 1 && best > 1; dx++ {
			for dy := int32(-1); dy <= 1 && best > 1; dy++ {
				for dz := int32(-1); dz <= 1 && best > 1; dz++ {
					if dx == 0 && dy == 0 && dz == 0 {
						continue
					}
					ns := g.world.GetBlock(pos[0]+dx, pos[1]+dy, pos[2]+dz)
					if ns == logState {
						best = 1
					} else if d := leavesDist(ns); d > 0 && d+1 < best {
						best = d + 1
					}
				}
			}
		}
		if best > 7 {
			// 枯萎消失 + 概率掉树苗
			g.world.SetBlock(pos[0], pos[1], pos[2], world.StateAir)
			g.recordBlockChange(pos[0], pos[1], pos[2], world.StateAir)
			if rand.Intn(20) == 0 {
				g.spawnItem(float64(pos[0])+0.5, float64(pos[1])+0.5, float64(pos[2])+0.5,
					entity.Item{ID: g.itemID777("oak_sapling"), Count: 1})
			}
			g.scheduleLeafCheck(pos[0], pos[1], pos[2])
		} else if best != leavesDist(s) {
			g.world.SetBlock(pos[0], pos[1], pos[2], leavesDistance[best])
			g.recordBlockChange(pos[0], pos[1], pos[2], leavesDistance[best])
		}
	}
	g.flushBlockUpdates()
}

// itemID777 777 基准物品名 → ID（熔炉已有 itemNameOf777 反向缓存）。
func (g *Game) itemID777(name string) int32 {
	return itemNameToID777[name]
}

// itemNameToID777 名→ID 缓存（itemNameOf777 首次调用时填充）。
var itemNameToID777 = map[string]int32{}
