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
	"skeleton": {name: "skeleton", health: 20, speed: 0.06, hostile: true, damage: 0},
	"creeper": {name: "creeper", health: 20, speed: 0.07, hostile: true, damage: 0},
	"blaze":      {name: "blaze", health: 20, speed: 0.05, hostile: true, damage: 4},
	"silverfish": {name: "silverfish", health: 8, speed: 0.09, hostile: true, damage: 1},
	"shulker":    {name: "shulker", health: 30, speed: 0, hostile: true, damage: 0},
	"villager":   {name: "villager", health: 20, speed: 0.05},
}

var passiveKinds = []string{"pig", "sheep", "cow", "chicken", "rabbit"}

// mob 是一只活着的生物。
type mob struct {
	eid  int32
	uuid protocol.UUID
	kind mobKind
	dim  int32
	color       int32 // 羊色（DyeColor 序）
	poison      int32 // 中毒剩余刻
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

// spawnMob 生成一只生物（默认主世界）并广播给区块观察者。
func (g *Game) spawnMob(kindName string, x, y, z float64) *mob {
	return g.spawnMobIn(DimOverworld, kindName, x, y, z)
}

// spawnMobIn 在指定维度生成生物。
func (g *Game) spawnMobIn(dim int32, kindName string, x, y, z float64) *mob {
	kind, ok := mobKinds[kindName]
	if !ok {
		return nil
	}
	eid := g.nextEID.Add(1)
	m := &mob{eid: eid, uuid: entityUUID(eid), kind: kind, dim: dim,
		x: x, y: y, z: z, health: kind.health, wanderCd: 20 + rand.Intn(100)}
	m.lastBX, m.lastBY, m.lastBZ = x, y, z
	g.mobs[eid] = m
	for _, v := range g.chunkViewers(dim, int32(x)>>4, int32(z)>>4) {
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
	delete(g.mobPaths, m.eid)
	eid := m.eid
	for _, p := range g.players {
		if p.Dim != m.dim {
			continue
		}
		p.Send.SendPlay("minecraft:remove_entities", func(w *protocol.Writer) {
			packet.WriteRemoveEntities(w, []int32{eid})
		})
	}
}

// tickMobs 推进全部生物：AI、物理、广播、消散。
func (g *Game) tickMobs() {
	for _, m := range g.mobs {
		m.age++
		// 中毒：每 25 刻 1 伤（不死，止于 1 血）
		if m.poison > 0 {
			m.poison--
			if m.poison%25 == 0 && m.health > 1 {
				m.health--
				if n := mobHurtSoundName(m.kind.name); n != "" {
					g.mobSound(m, n)
				}
			}
			if m.health <= 0 {
				g.killMob(m, nil)
				continue
			}
		}
		// AI
		if m.kind.name == "blaze" || m.kind.name == "shulker" {
			g.tickExtendedAI(m)
			m.vx, m.vz = 0, 0 // 悬浮/固定
		} else if m.kind.hostile {
			if m.kind.name == "skeleton" || m.kind.name == "creeper" {
				g.tickHostileAI(m)
				if m.kind.name == "creeper" && g.creepers[m.eid] == nil {
					// 苦力怕也要追人（引信逻辑在 tickHostileAI）
					if target := g.nearestPlayer(m.x, m.y, m.z, 16); target != nil && !target.Dead {
						dx, dz := target.X-m.x, target.Z-m.z
						if d := math.Sqrt(dx*dx + dz*dz); d > 2.5 {
							m.x += dx / d * m.kind.speed
							m.z += dz / d * m.kind.speed
						}
					}
				}
			} else if target := g.nearestPlayer(m.x, m.y, m.z, 16); target != nil && !target.Dead {
				dx, dz := target.X-m.x, target.Z-m.z
				d := math.Sqrt(dx*dx + dz*dz)
				if d > 1.4 {
					nx, nz := m.x+dx/d*m.kind.speed, m.z+dz/d*m.kind.speed
					// 前方一格被挡：尝试跳上台阶（上方两格需空）
					if g.blockAtDim(m.dim, floorI(nx), floorI(m.y)+0, floorI(nz)) &&
						!g.blockAtDim(m.dim, floorI(nx), floorI(m.y)+1, floorI(nz)) &&
						!g.blockAtDim(m.dim, floorI(nx), floorI(m.y)+2, floorI(nz)) {
						m.vy = 0.42 // 原版跳跃初速
					}
					m.x, m.z = nx, nz
				}
				m.tx, m.tz = target.X, target.Z
				if m.attackCd > 0 {
					m.attackCd--
				} else if d < 1.6 && abs32(int32(target.Y-m.y)) <= 2 {
					g.damage(target, m.kind.damage, damageMobAttack)
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
					if g.blockAtDim(m.dim, floorI(tx), floorI(m.y)-1, floorI(tz)) {
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
					if g.blockAtDim(m.dim, floorI(nx), floorI(m.y)-1, floorI(nz)) {
						m.x, m.z = nx, nz
					} else {
						m.tx, m.tz = 0, 0
					}
				} else {
					m.tx, m.tz = 0, 0
				}
			}
		}
		// 物理：击退衰减 + 重力贴地（vy>0 为跳跃/击退上升段）
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
		if m.vy <= 0 && g.blockAtDim(m.dim, floorI(m.x), floorI(m.y-0.1), floorI(m.z)) {
			m.vy = 0
			m.y = float64(floorI(m.y-0.1)) + 1
		} else {
			m.vy -= 0.08
			m.y += m.vy
			if m.vy > 0 && g.blockAtDim(m.dim, floorI(m.x), floorI(m.y+1.9), floorI(m.z)) {
				m.vy = 0 // 撞头
			}
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
			for _, v := range g.chunkViewers(m.dim, int32(x)>>4, int32(z)>>4) {
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
// 攻击者下落中（离地且有下落距离）触发暴击：×1.5 并广播暴击粒子。
func (g *Game) handleAttack(ev *playerAttackEvent) {
	p := ev.p
	if p == nil || p.Dead {
		return
	}
	dmg := g.attackDamage(p) + g.enchantAttackBonus(p)
	if p.Effects["weakness"] > 0 {
		dmg *= 0.5 // 虚弱：近战减半
	}
	crit := !p.OnGround && p.FallStart != 0 && p.FallStart >= p.Y &&
		!g.playerInWater(p)
	if crit {
		dmg *= 1.5
	}
	// 打末影水晶
	if c, ok := g.crystals[ev.target]; ok {
		if dist2(p.X, p.Y, p.Z, c.x, c.y, c.z) <= 100 {
			g.destroyCrystal(c, p)
			return
		}
	}
	// 打末影龙
	if g.dragon != nil && ev.target == g.dragon.eid {
		if dist2(p.X, p.Y, p.Z, g.dragon.px, g.dragon.y, g.dragon.pz) <= 100 {
			g.damageDragon(dmg, p)
			g.playSoundTo(p, SoundHostile, "entity.ender_dragon.hurt", 1, 1)
			return
		}
	}
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
		if crit { // 暴击粒子（entity_event 7）
			eid := m.eid
			for _, v := range g.players {
				v.Send.SendPlay("minecraft:entity_event", func(w *protocol.Writer) {
					packet.WriteEntityEvent(w, eid, 7)
				})
			}
		}
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
			if crit {
				eid := other.EntityID
				for _, v := range g.players {
					v.Send.SendPlay("minecraft:entity_event", func(w *protocol.Writer) {
						packet.WriteEntityEvent(w, eid, 7)
					})
				}
			}
			g.damage(other, dmg, damageGeneric)
			return
		}
	}
}

// playerInWater 玩家身体处于水中（暴击与摔落豁免判定）。
func (g *Game) playerInWater(p *entity.Player) bool {
	b := g.world.GetBlock(floorI(p.X), floorI(p.Y), floorI(p.Z))
	return b >= world.WaterMin && b <= world.WaterMax
}

// killMob 生物死亡：动画、移除、掉落（数量按官方战利品表范围随机）。
func (g *Game) killMob(m *mob, by *entity.Player) {
	eid := m.eid
	wasSheared := g.sheared[eid]
	for _, v := range g.players {
		v.Send.SendPlay("minecraft:entity_event", func(w *protocol.Writer) {
			packet.WriteEntityEvent(w, eid, 3)
		})
		v.Send.SendPlay("minecraft:remove_entities", func(w *protocol.Writer) {
			packet.WriteRemoveEntities(w, []int32{eid})
		})
	}
	delete(g.mobs, eid)
	delete(g.sheared, eid)
	delete(g.saddled, eid)
	delete(g.mobPaths, eid)
	// 羊未剪毛时额外掉 1 同色羊毛（原版行为）
	if m.kind.name == "sheep" && !wasSheared {
		g.spawnItem(m.dim, m.x, m.y+0.5, m.z, entity.Item{ID: g.itemID777(woolOfColor(m.color)), Count: 1})
	}
	if d, ok := g.mobDrops[m.kind.name]; ok {
		n := d.Min
		if d.Max > d.Min {
			n += rand.Int31n(d.Max - d.Min + 1)
		}
		if n > 0 {
			g.spawnItem(m.dim, m.x, m.y+0.5, m.z, entity.Item{ID: d.Item, Count: n})
		}
	}
	if xp := mobXP[m.kind.name]; xp > 0 {
		g.spawnXPOrb(m.dim, m.x, m.y+0.5, m.z, xp)
	}
	// 扩展掉落：烈焰棒 0-1、潜贝壳 0-1（50%）
	switch m.kind.name {
	case "blaze":
		if rand.Intn(2) == 0 {
			g.spawnItem(m.dim, m.x, m.y+0.5, m.z, entity.Item{ID: g.itemID777("blaze_rod"), Count: 1})
		}
		g.spawnXPOrb(m.dim, m.x, m.y+0.5, m.z, 10)
	case "shulker":
		if rand.Intn(2) == 0 {
			g.spawnItem(m.dim, m.x, m.y+0.5, m.z, entity.Item{ID: g.itemID777("shulker_shell"), Count: 1})
		}
		g.spawnXPOrb(m.dim, m.x, m.y+0.5, m.z, 5)
	}
	g.Bus.emit(MobKilled{Player: by, Kind: m.kind.name})
}

// attackDamage 按手持物品计算攻击力（公开数值 + 附魔加成）。
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
						if kind == "sheep" {
							g.spawnColoredSheep(DimOverworld, x+rand.Float64()*2-1, y, z+rand.Float64()*2-1, -1)
						} else {
						g.spawnMob(kind, x+rand.Float64()*2-1, y, z+rand.Float64()*2-1)
						}
					}
					if rand.Intn(10) == 0 { // 村民偶发与被动群同批
						g.spawnMobIn(DimOverworld, "villager", x, y, z)
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
	return g.nearestPlayerDim(-1, x, y, z, r)
}

// nearestPlayerDim 指定维度（-1 = 任意）内最近的活着的玩家。
func (g *Game) nearestPlayerDim(dim int32, x, y, z float64, r float64) *entity.Player {
	var best *entity.Player
	bestD := r * r
	for _, p := range g.players {
		if p.Dead || (dim >= 0 && p.Dim != dim) {
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
	return g.nearestPlayerDistDim(-1, x, y, z)
}

// nearestPlayerDistDim 指定维度（-1 = 任意）的最近玩家距离。
func (g *Game) nearestPlayerDistDim(dim int32, x, y, z float64) float64 {
	d := math.MaxFloat64
	for _, p := range g.players {
		if dim >= 0 && p.Dim != dim {
			continue
		}
		if dd := dist2(x, y, z, p.X, p.Y, p.Z); dd < d {
			d = dd
		}
	}
	if d == math.MaxFloat64 {
		return d
	}
	return math.Sqrt(d)
}
