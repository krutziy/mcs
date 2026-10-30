package game

// 玩家投掷物：弓（拉弓蓄力 → 松手发射，伤害随蓄力）、雪球/鸡蛋/末影珍珠
// （use_item 即时投掷）。箭复用 hostile.go 的箭矢实体（owner=玩家 EID），
// 末影珍珠命中传送投掷者。

import (
	"math"
	"math/rand"

	"mcserver/internal/entity"
	"mcserver/internal/packet"
	"mcserver/internal/protocol"
)

// bowMaxTicks 拉满弓所需刻数（原版 20 刻）。
const bowMaxTicks int32 = 20

// pearlCooldown 末影珍珠冷却（原版 20 刻）。
const pearlCooldown int32 = 20

// handleBowUse use_item 手持弓：开始蓄力（BowTicks 计时，松手发射）。
// 返回 true 表示已处理。
func (g *Game) handleBowUse(ev *playerUseItemEvent) bool {
	p := ev.p
	held := p.Held()
	if g.itemNameOf(p, held.ID) != "bow" {
		return false
	}
	// 需要箭（创造除外）
	if !g.isCreative(p) {
		arrowID := g.itemFor(p, g.itemID777("arrow"))
		found := false
		for i := int32(9); i < 46 && !found; i++ {
			if p.Slots[i].ID == arrowID && p.Slots[i].Count > 0 {
				found = true
			}
		}
		if !found {
			return true // 有弓无箭：不蓄力
		}
	}
	p.BowTicks = 0
	return true
}

// fireBow 松开发射：蓄力比例 → 初速与伤害；消耗箭与耐久。
func (g *Game) fireBow(p *entity.Player) {
	power := float64(p.BowTicks) / float64(bowMaxTicks)
	if power > 1 {
		power = 1
	}
	p.BowTicks = -1
	if power < 0.1 {
		return // 没拉开：不射
	}
	if !g.isCreative(p) {
		arrowID := g.itemFor(p, g.itemID777("arrow"))
		for i := int32(9); i < 46; i++ {
			if p.Slots[i].ID == arrowID && p.Slots[i].Count > 0 {
				p.Slots[i].Count--
				if p.Slots[i].Count <= 0 {
					p.Slots[i] = entity.Item{}
				}
				break
			}
		}
		g.damageHeld(p) // 弓耐久 -1
		g.syncInventory(p)
	}
	// 发射方向（yaw/pitch）
	yaw := float64(p.Yaw) * math.Pi / 180
	pitch := float64(p.Pitch) * math.Pi / 180
	dx := -math.Sin(yaw) * math.Cos(pitch)
	dy := -math.Sin(pitch)
	dz := math.Cos(yaw) * math.Cos(pitch)
	speed := 0.6 + power*2.0
	dmg := float32(1 + power*4) // 满力 5 + 暴击
	crit := power >= 1
	g.spawnPlayerArrow(p, p.X+dx*0.5, p.Y+1.5, p.Z+dz*0.5, dx*speed, dy*speed, dz*speed, dmg, crit)
	g.playSoundTo(p, SoundPlayer, "entity.arrow.shoot", 1, float32(0.4+power*0.6))
}

// spawnPlayerArrow 生成玩家箭矢实体（复用 arrow 结构，记伤害与暴击）。
func (g *Game) spawnPlayerArrow(p *entity.Player, x, y, z, vx, vy, vz float64, dmg float32, crit bool) {
	eid := g.nextEID.Add(1)
	ar := &arrow{eid: eid, owner: p.EntityID, dim: p.Dim,
		x: x, y: y, z: z, vx: vx, vy: vy, vz: vz,
		dmg: dmg, crit: crit}
	ar.uuid = entityUUID(eid)
	g.arrows[eid] = ar
	for _, v := range g.chunkViewers(p.Dim, int32(x)>>4, int32(z)>>4) {
		typ := g.entityTypeID(v, "arrow")
		eid2, xx, yy, zz := eid, x, y, z
		ivx, ivy, ivz := int16(vx*8000), int16(vy*8000), int16(vz*8000)
		v.Send.SendPlay("minecraft:add_entity", func(w *protocol.Writer) {
			packet.SpawnEntity{EntityID: eid2, UUID: ar.uuid, Type: typ,
				X: xx, Y: yy, Z: zz, VX: ivx, VY: ivy, VZ: ivz, ObjectData: p.EntityID}.Write(w)
		})
	}
}

// thrownProjectile 一个投掷物（雪球/鸡蛋/末影珍珠）。
type thrownProjectile struct {
	eid        int32
	uuid       protocol.UUID
	kind       string // snowball | egg | ender_pearl | ender_eye | splash_potion
	owner      int32
	dim        int32
	potion     string // 喷溅药水的药水名
	x, y, z    float64
	vx, vy, vz float64
	age        int
}

// handleThrowUse use_item 手持投掷物：立即投出。返回 true 已处理。
func (g *Game) handleThrowUse(ev *playerUseItemEvent) bool {
	p := ev.p
	name := g.itemNameOf(p, p.Held().ID)
	switch name {
	case "snowball", "egg", "ender_pearl", "splash_potion":
	default:
		return false
	}
	if name == "ender_pearl" && p.PearlCd > 0 {
		return true
	}
	if name == "ender_pearl" {
		p.PearlCd = pearlCooldown
	}
	// 消耗
	if !g.isCreative(p) {
		s := &p.Slots[36+p.HeldSlot]
		s.Count--
		if s.Count <= 0 {
			*s = entity.Item{}
		}
		g.syncInventory(p)
	}
	yaw := float64(p.Yaw) * math.Pi / 180
	pitch := float64(p.Pitch) * math.Pi / 180
	dx := -math.Sin(yaw) * math.Cos(pitch)
	dy := -math.Sin(pitch)
	dz := math.Cos(yaw) * math.Cos(pitch)
	speed := 1.5
	// 喷溅药水携带药水名（拾取记账）
	potionName := ""
	if name == "splash_potion" {
		potionName = p.PotionOf[[2]int32{36 + p.HeldSlot, 0}]
	}
	eid := g.nextEID.Add(1)
	tp := &thrownProjectile{eid: eid, uuid: entityUUID(eid), kind: name,
		owner: p.EntityID, dim: p.Dim, potion: potionName,
		x: p.X + dx*0.5, y: p.Y + 1.5, z: p.Z + dz*0.5,
		vx: dx * speed, vy: dy*speed + 0.1, vz: dz * speed}
	g.thrown[eid] = tp
	for _, v := range g.chunkViewers(p.Dim, int32(tp.x)>>4, int32(tp.z)>>4) {
		typ := g.entityTypeID(v, name)
		eid2, xx, yy, zz := eid, tp.x, tp.y, tp.z
		ivx, ivy, ivz := int16(tp.vx*8000), int16(tp.vy*8000), int16(tp.vz*8000)
		v.Send.SendPlay("minecraft:add_entity", func(w *protocol.Writer) {
			packet.SpawnEntity{EntityID: eid2, UUID: tp.uuid, Type: typ,
				X: xx, Y: yy, Z: zz, VX: ivx, VY: ivy, VZ: ivz, ObjectData: p.EntityID}.Write(w)
		})
	}
	g.playSoundTo(p, SoundPlayer, "entity."+name+".throw", 1, 1)
	return true
}

// tickThrown 投掷物飞行与命中（重力、方块/生物命中、珍珠传送、
// 末影之眼指向飞行、喷溅药水范围生效）。
func (g *Game) tickThrown() {
	for eid, tp := range g.thrown {
		tp.age++
		if tp.age > 600 {
			g.removeThrown(eid, tp)
			continue
		}
		// 末影之眼：无重力直线飞 60 刻，80% 消散 / 20% 掉落
		if tp.kind == "ender_eye" {
			if tp.age >= 60 {
				if rand.Intn(5) == 0 {
					g.spawnItem(tp.dim, tp.x, tp.y, tp.z,
						entity.Item{ID: g.itemID777("ender_eye"), Count: 1})
				}
				g.removeThrown(eid, tp)
				continue
			}
			tp.x += tp.vx
			tp.y += tp.vy
			tp.z += tp.vz
			continue
		}
		tp.vy -= 0.03
		nx, ny, nz := tp.x+tp.vx, tp.y+tp.vy, tp.z+tp.vz
		hit := false
		if g.blockAtDim(tp.dim, floorI(nx), floorI(ny), floorI(nz)) {
			hit = true
		}
		// 命中生物
		for mid, m := range g.mobs {
			if m.dim != tp.dim {
				continue
			}
			if math.Abs(m.x-nx) < 0.8 && math.Abs(m.z-nz) < 0.8 && ny > m.y && ny < m.y+2 {
				if tp.kind == "ender_pearl" {
					m.health -= 0 // 珍珠不伤害生物
				} else {
					m.health -= 0.5 // 雪球/鸡蛋：原版对生物 0 伤（仅击退），这里 0.5 象征
					m.vx += tp.vx * 0.2
					m.vz += tp.vz * 0.2
					if n := mobHurtSoundName(m.kind.name); n != "" {
						g.mobSound(m, n)
					}
					_ = mid
					if m.health <= 0 {
						g.killMob(m, nil)
					}
				}
				hit = true
				break
			}
		}
		// 命中玩家
		for _, p := range g.players {
			if p.EntityID == tp.owner || p.Dim != tp.dim || p.Dead {
				continue
			}
			if math.Abs(p.X-nx) < 0.7 && math.Abs(p.Z-nz) < 0.7 && ny > p.Y && ny < p.Y+2 {
				hit = true
				break
			}
		}
		if hit {
			switch tp.kind {
			case "ender_pearl":
				g.pearlLand(tp)
			case "splash_potion":
				g.splashPotionEffect(tp)
			}
			g.removeThrown(eid, tp)
			continue
		}
		tp.x, tp.y, tp.z = nx, ny, nz
	}
}

// splashPotionEffect 喷溅药水破裂：半径 4 内玩家/生物获得药水效果。
func (g *Game) splashPotionEffect(tp *thrownProjectile) {
	g.blockSound(tp.dim, tp.x, tp.y, tp.z, "entity.splash_potion.break")
	for _, p := range g.players {
		if p.Dim != tp.dim || p.Dead {
			continue
		}
		if dist2(p.X, p.Y, p.Z, tp.x, tp.y, tp.z) > 16 {
			continue
		}
		g.applyPotionByName(p, tp.potion)
	}
	for _, m := range g.mobs {
		if m.dim != tp.dim {
			continue
		}
		if dist2(m.x, m.y, m.z, tp.x, tp.y, tp.z) > 16 {
			continue
		}
		g.applyPotionToMob(m, tp.potion)
	}
}

// pearlLand 珍珠落地：传送投掷者并造成 2.5 摔落伤害。
func (g *Game) pearlLand(tp *thrownProjectile) {
	p := g.playerByEID(tp.owner)
	if p == nil || p.Dead {
		return
	}
	x, y, z := tp.x, tp.y, tp.z
	// 找安全落脚（上方一格）
	if g.blockAtDim(tp.dim, floorI(x), floorI(y), floorI(z)) {
		y = float64(floorI(y) + 1)
	}
	p.X, p.Y, p.Z = x, y, z
	p.PortalCooldown = maxI32(p.PortalCooldown, 10)
	p.TeleportID++
	id := p.TeleportID
	px, py, pz := x, y, z
	p.Send.SendPlay("minecraft:player_position", func(w *protocol.Writer) {
		packet.WritePlayerPosition(w, packet.PlayerPositionOut{TeleportID: id, X: px, Y: py, Z: pz})
	})
	g.damage(p, 2.5, damageFall)
	g.syncMovement(p)
	g.playSoundTo(p, SoundPlayer, "entity.ender_pearl.land", 1, 1)
}

func (g *Game) removeThrown(eid int32, tp *thrownProjectile) {
	delete(g.thrown, eid)
	for _, p := range g.players {
		if p.Dim != tp.dim {
			continue
		}
		p.Send.SendPlay("minecraft:remove_entities", func(w *protocol.Writer) {
			packet.WriteRemoveEntities(w, []int32{eid})
		})
	}
}

// playerByEID 按实体 ID 找在线玩家。
func (g *Game) playerByEID(eid int32) *entity.Player {
	return g.players[eid]
}

// tickBowCharging 拉弓蓄力推进与冷却。
func (g *Game) tickBowCharging() {
	for _, p := range g.players {
		if p.PearlCd > 0 {
			p.PearlCd--
		}
		if p.BowTicks < 0 {
			continue
		}
		if p.Dead || p.Held().ID == 0 || g.itemNameOf(p, p.Held().ID) != "bow" {
			p.BowTicks = -1
			continue
		}
		p.BowTicks++
		if p.BowTicks > bowMaxTicks+40 {
			p.BowTicks = bowMaxTicks + 40 // 保持满蓄（不自动发射）
		}
	}
}

func maxI32(a, b int32) int32 {
	if a > b {
		return a
	}
	return b
}
