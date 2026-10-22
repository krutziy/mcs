package game

// 生物系统：被动生物（猪/羊/牛/鸡/兔）游荡生成、敌对僵尸夜间生成与追击、
// 攻击/击退/死亡掉落（官方 jar 战利品表生成的 mobdrops 资产）。
// 实体网络类型按观察者协议查 entitytype 表，移动用 teleport_entity 周期广播。

import (
	"math"
	"math/rand"

	"mcserver/internal/entity"
	"mcserver/internal/packet"
	"mcserver/internal/protocol"
	"mcserver/internal/world"
)

// mobKind 是受支持的生物种类与属性（数值为公开游戏常识）。
type mobKind struct {
	name     string
	health   float32
	speed    float64 // 格/刻（游荡）
	hostile  bool
	damage   float32 // 敌对攻击力
}

var mobKinds = map[string]mobKind{
	"pig": {name: "pig", health: 10, speed: 0.05},
	"sheep": {name: "sheep", health: 8, speed: 0.05},
	"cow": {name: "cow", health: 10, speed: 0.05},
	"chicken": {name: "chicken", health: 4, speed: 0.04},
	"rabbit": {name: "rabbit", health: 3, speed: 0.07},
	"zombie": {name: "zombie", health: 20, speed: 0.07, hostile: true, damage: 3},
}

var passiveKinds = []string{"pig", "sheep", "cow", "chicken", "rabbit"}

// mob 是一只活着的生物。
type mob struct {
	eid  int32
	uuid protocol.UUID
	kind mobKind
	x, y, z     float64
	vx, vy, vz  float64 // 击退/重力
	health      float32
	age         int
	tx, tz      float64 // 游荡目标
	wanderCd    int
	attackCd    int
	lastBX, lastBY, lastBZ float64 // 上次广播位置
}

// 生物数量与生成参数。
const (
	mobPassiveCap = 12
	mobHostileCap = 8
	mobDespawnR   = 72.0
)

// spawnMob 生成一只生物并广播给区块观察者。
func (g *Game) spawnMob(kindName string, x, y, z float64) *mob {
	kind, ok := mobKinds[kindName]
	if !ok {
		return nil
	}
	eid := g.nextEID.Add(1)
	m := &mob{eid: eid, uuid: entityUUID(eid), kind: kind,
		x: x, y: y, z: z, health: kind.health, wanderCd: 20 + rand.Intn(100)}
	m.lastBX, m.lastBY, m.lastBZ = x, y, z
	g.mobs[eid] = m
	for _, v := range g.chunkViewers(int32(x)>>4, int32(z)>>4) {
		g.sendMobSpawn(v, m)
	}
	return m
}

// sendMobSpawn 向玩家发送生物生成。
func (g *Game) sendMobSpawn(p *entity.Player, m *mob) {
	typ := g.entityTypeID(p, m.kind.name)
	p.Send.SendPlay("minecraft:add_entity", func(w *protocol.Writer) {
		packet.SpawnEntity{EntityID: m.eid, UUID: m.uuid, Type: typ,
			X: m.x, Y: m.y, Z: m.z}.Write(w)
	})
}

// removeMob 移除生物（死亡/消散）。
func (g *Game) removeMob(m *mob) {
	delete(g.mobs, m.eid)
	eid := m.eid
	for _, p := range g.players {
		p.Send.SendPlay("minecraft:remove_entities", func(w *protocol.Writer) {
			packet.WriteRemoveEntities(w, []int32{eid})
		})
	}
}

// tickMobs 推进全部生物：AI、物理、广播、消散。
func (g *Game) tickMobs() {
	for _, m := range g.mobs {
		m.age++
		// AI
		if m.kind.hostile {
			if target := g.nearestPlayer(m.x, m.y, m.z, 16); target != nil && !target.Dead {
				dx, dz := target.X-m.x, target.Z-m.z
				d := math.Sqrt(dx*dx + dz*dz)
				if d > 1.4 {
					m.x += dx / d * m.kind.speed
					m.z += dz / d * m.kind.speed
				}
				m.tx, m.tz = target.X, target.Z
				if m.attackCd > 0 {
					m.attackCd--
				} else if d < 1.6 && abs32(int32(target.Y-m.y)) <= 2 {
					g.damage(target, m.kind.damage, damageGeneric)
					m.attackCd = 20 // 每秒一次
				}
			}
		} else {
			if m.wanderCd > 0 {
				m.wanderCd--
			} else {
				// 随机换向：目标点脚下需要有方块（不往悬崖/虚空走）
				for try := 0; try < 4; try++ {
					tx := m.x + (rand.Float64()-0.5)*16
					tz := m.z + (rand.Float64()-0.5)*16
					if g.blockAt(floorI(tx), floorI(m.y)-1, floorI(tz)) {
						m.tx, m.tz = tx, tz
						break
					}
				}
				m.wanderCd = 100 + rand.Intn(200)
			}
			if m.tx != 0 || m.tz != 0 {
				dx, dz := m.tx-m.x, m.tz-m.z
				if d := math.Sqrt(dx*dx + dz*dz); d > 0.8 {
					nx, nz := m.x+dx/d*m.kind.speed, m.z+dz/d*m.kind.speed
					if g.blockAt(floorI(nx), floorI(m.y)-1, floorI(nz)) {
						m.x, m.z = nx, nz
					} else {
						m.tx, m.tz = 0, 0
					}
				} else {
					m.tx, m.tz = 0, 0
				}
			}
		}
		// 物理：击退衰减 + 重力贴地
		m.x += m.vx
		m.z += m.vz
		m.vx *= 0.9
		m.vz *= 0.9
		if absF(m.vx) < 0.005 {
			m.vx = 0
		}
		if absF(m.vz) < 0.005 {
			m.vz = 0
		}
		if !g.blockAt(floorI(m.x), floorI(m.y-0.1), floorI(m.z)) {
			m.vy -= 0.08
			m.y += m.vy
		} else {
			m.vy = 0
			m.y = float64(floorI(m.y-0.1)) + 1
		}
		if m.y < world.MinY-10 {
			g.removeMob(m)
			continue
		}
		// 每 10 刻广播位移（绝对位置）
		if m.age%10 == 0 && dist2(m.x, m.y, m.z, m.lastBX, m.lastBY, m.lastBZ) > 0.01 {
			m.lastBX, m.lastBY, m.lastBZ = m.x, m.y, m.z
			eid, x, y, z := m.eid, m.x, m.y, m.z
			yaw := float32(0)
			for _, v := range g.chunkViewers(int32(x)>>4, int32(z)>>4) {
				v.Send.SendPlay("minecraft:teleport_entity", func(w *protocol.Writer) {
					packet.WriteTeleportEntity(w, eid, x, y, z, 0, 0, 0, yaw, 0, true)
				})
			}
		}
		// 远离所有玩家则消散
		if m.age%100 == 0 && g.nearestPlayerDist(m.x, m.y, m.z) > mobDespawnR {
			g.removeMob(m)
		}
	}
}

// handleAttack 处理玩家攻击（serverbound attack：VarInt 目标实体）。
func (g *Game) handleAttack(ev *playerAttackEvent) {
	p := ev.p
	if p == nil || p.Dead {
		return
	}
	dmg := g.attackDamage(p)
	// 打生物
	if m, ok := g.mobs[ev.target]; ok {
		if dist2(p.X, p.Y, p.Z, m.x, m.y, m.z) > 36 {
			return // 超出 6 格
		}
		dx, dz := m.x-p.X, m.z-p.Z
		d := math.Sqrt(dx*dx + dz*dz)
		if d < 0.01 {
			dx, dz, d = 1, 0, 1
		}
		m.health -= dmg
		g.damageHeld(p)
		m.vx += dx / d * 0.4
		m.vz += dz / d * 0.4
		m.vy = 0.3
		if m.health <= 0 {
			g.killMob(m, p)
		} else {
			eid := m.eid
			for _, v := range g.players {
				v.Send.SendPlay("minecraft:entity_event", func(w *protocol.Writer) {
					packet.WriteEntityEvent(w, eid, 2)
				})
			}
		}
		return
	}
	// 打玩家（PVP）
	for _, other := range g.players {
		if other.EntityID == ev.target && other.EntityID != p.EntityID &&
			dist2(p.X, p.Y, p.Z, other.X, other.Y, other.Z) <= 36 {
			g.damage(other, dmg, damageGeneric)
			return
		}
	}
}

// killMob 生物死亡：动画、移除、掉落。
func (g *Game) killMob(m *mob, by *entity.Player) {
	eid := m.eid
	for _, v := range g.players {
		v.Send.SendPlay("minecraft:entity_event", func(w *protocol.Writer) {
			packet.WriteEntityEvent(w, eid, 3)
		})
		v.Send.SendPlay("minecraft:remove_entities", func(w *protocol.Writer) {
			packet.WriteRemoveEntities(w, []int32{eid})
		})
	}
	delete(g.mobs, eid)
	if itemID, ok := g.mobDrops[m.kind.name]; ok {
		g.spawnItem(m.x, m.y+0.5, m.z, entity.Item{ID: itemID, Count: 1})
	}
	if xp := mobXP[m.kind.name]; xp > 0 {
		g.spawnXPOrb(m.x, m.y+0.5, m.z, xp)
	}
	g.Bus.emit(MobKilled{Player: by, Kind: m.kind.name})
}

// attackDamage 按手持物品计算攻击力（公开数值：剑 4-8，工具 2-5，空手 1）。
func (g *Game) attackDamage(p *entity.Player) float32 {
	name := g.itemNameOf(p, p.Held().ID)
	if name == "" {
		return 1
	}
	// 材质基数
	base := map[string]float32{"wooden": 4, "golden": 4, "stone": 5, "copper": 5,
		"iron": 6, "diamond": 7, "netherite": 8}
	for mat, v := range base {
		if len(name) >= len(mat)+6 && name[:len(mat)] == mat {
			switch name[len(mat)+1:] {
			case "sword":
				return v
			case "axe":
				return v + 1
			case "pickaxe":
				return v - 2
			}
		}
	}
	return 1
}

// tickMobSpawning 生成调度：白天被动群、夜间僵尸。
func (g *Game) tickMobSpawning() {
	if g.tick%100 != 0 || len(g.players) == 0 {
		return
	}
	night := g.dayTime >= 13000 && g.dayTime <= 23000
	if !night {
		nPassive := 0
		for _, m := range g.mobs {
			if !m.kind.hostile {
				nPassive++
			}
		}
		if nPassive < mobPassiveCap {
			if x, y, z, ok := g.findSpawnSpot(24, world.StateGrassBlock); ok {
				kind := passiveKinds[rand.Intn(len(passiveKinds))]
				n := 1 + rand.Intn(3)
				for i := 0; i < n; i++ {
					g.spawnMob(kind, x+rand.Float64()*2-1, y, z+rand.Float64()*2-1)
				}
			}
		}
	} else {
		nHostile := 0
		for _, m := range g.mobs {
			if m.kind.hostile {
				nHostile++
			}
		}
		if nHostile < mobHostileCap {
			if x, y, z, ok := g.findSpawnSpot(32, 0); ok {
				g.spawnMob("zombie", x, y, z)
			}
		}
	}
}

// findSpawnSpot 在随机玩家附近 dist 内找一处可站立的表面（surface 为目标表面方块，0=任意实体）。
func (g *Game) findSpawnSpot(dist int32, surface uint32) (float64, float64, float64, bool) {
	for _, p := range g.players {
		for try := 0; try < 16; try++ {
			x := int32(p.X) + (rand.Int31n(dist*2) - dist)
			z := int32(p.Z) + (rand.Int31n(dist*2) - dist)
			if !p.SentChunks[[2]int32{x >> 4, z >> 4}] {
				continue
			}
			for y := int32(p.Y) + 8; y >= world.MinY; y-- {
				b := g.world.GetBlock(x, y, z)
				if b == world.StateAir {
					continue
				}
				if surface != 0 && b != surface {
					break
				}
				if g.world.GetBlock(x, y+1, z) == world.StateAir && g.world.GetBlock(x, y+2, z) == world.StateAir {
					return float64(x) + 0.5, float64(y) + 1, float64(z) + 0.5, true
				}
				break
			}
		}
	}
	return 0, 0, 0, false
}

// nearestPlayer 返回指定范围内最近的活着的玩家。
func (g *Game) nearestPlayer(x, y, z float64, r float64) *entity.Player {
	var best *entity.Player
	bestD := r * r
	for _, p := range g.players {
		if p.Dead {
			continue
		}
		if d := dist2(x, y, z, p.X, p.Y, p.Z); d < bestD {
			bestD, best = d, p
		}
	}
	return best
}

// nearestPlayerDist 返回最近玩家距离（无人时无穷远）。
func (g *Game) nearestPlayerDist(x, y, z float64) float64 {
	d := math.MaxFloat64
	for _, p := range g.players {
		if dd := dist2(x, y, z, p.X, p.Y, p.Z); dd < d {
			d = dd
		}
	}
	if d == math.MaxFloat64 {
		return d
	}
	return math.Sqrt(d)
}
