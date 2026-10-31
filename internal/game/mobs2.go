package game

// 生物扩展：烈焰人（火球）、蠹虫、潜贝贝（漂浮子弹）、村民（见 villager.go）、
// 羊 16 色（元数据 sheep/color 序列化 108 + 双索引兼容）、刷怪笼驱动、
// 下界疣种植/生长/收获、末地城潜贝贝布设与战利品。

import (
	"math"
	"math/rand"

	"mcserver/internal/assets"
	"mcserver/internal/entity"
	"mcserver/internal/packet"
	"mcserver/internal/protocol"
	"mcserver/internal/world"

	"mcserver/plugins/terrain"
)

// 下界疣状态（age 0-3 = 11181-11184）。
var wartAges = [4]uint32{11181, 11182, 11183, 11184}

// sheepColors 染料短名 → 羊色索引（DyeColor 注册表序）与同色羊毛短名。
var sheepColors = []struct{ dye, wool string }{
	{"white_dye", "white_wool"}, {"orange_dye", "orange_wool"}, {"magenta_dye", "magenta_wool"},
	{"light_blue_dye", "light_blue_wool"}, {"yellow_dye", "yellow_wool"}, {"lime_dye", "lime_wool"},
	{"pink_dye", "pink_wool"}, {"gray_dye", "gray_wool"}, {"light_gray_dye", "light_gray_wool"},
	{"cyan_dye", "cyan_wool"}, {"purple_dye", "purple_wool"}, {"blue_dye", "blue_wool"},
	{"brown_dye", "brown_wool"}, {"green_dye", "green_wool"}, {"red_dye", "red_wool"},
	{"black_dye", "black_wool"},
}

// sendMobColor 广播生物颜色元数据（sheep/color 序列化 108；索引 16/17 双发兼容）。
func (g *Game) sendMobColor(m *mob, color int32) {
	eid := m.eid
	for _, v := range g.players {
		if v.Dim != m.dim {
			continue
		}
		v.Send.SendPlay("minecraft:set_entity_data", func(w *protocol.Writer) {
			w.VarInt(eid)
			w.Uint8(16)
			w.VarInt(108)
			w.VarInt(color)
			w.Uint8(17)
			w.VarInt(108)
			w.VarInt(color)
			w.Uint8(0xFF)
		})
	}
}

// spawnColoredSheep 生成指定颜色的羊（-1 随机）。
func (g *Game) spawnColoredSheep(dim int32, x, y, z float64, color int32) *mob {
	if color < 0 {
		color = rand.Int31n(8) // 自然色系前 8 种常见
	}
	m := g.spawnMobIn(dim, "sheep", x, y, z)
	if m != nil {
		m.color = color
		g.sendMobColor(m, color)
	}
	return m
}

// tryDyeSheep 手持染料右键羊：染色。
func (g *Game) tryDyeSheep(p *entity.Player, m *mob) bool {
	held := p.Held()
	if held.ID == 0 {
		return false
	}
	name := g.itemNameOf(p, held.ID)
	for ci, c := range sheepColors {
		if c.dye != name {
			continue
		}
		m.color = int32(ci)
		g.sendMobColor(m, int32(ci))
		if !g.isCreative(p) {
			s := &p.Slots[36+p.HeldSlot]
			s.Count--
			if s.Count <= 0 {
				*s = entity.Item{}
			}
			g.syncInventory(p)
		}
		g.mobSound(m, "entity.sheep.dye")
		return true
	}
	return false
}

// woolOfColor 羊色 → 羊毛短名（默认白）。
func woolOfColor(color int32) string {
	if color >= 0 && int(color) < len(sheepColors) {
		return sheepColors[color].wool
	}
	return "white_wool"
}

// ---- 烈焰人 / 蠹虫 / 潜贝贝 AI ----

// tickExtendedAI 扩展生物行为：烈焰人火球、潜贝贝漂浮子弹。
func (g *Game) tickExtendedAI(m *mob) {
	if m.kind.name == "blaze" {
		target := g.nearestPlayerDim(m.dim, m.x, m.y, m.z, 16)
		if target == nil || target.Dead {
			return
		}
		if m.attackCd > 0 {
			m.attackCd--
			return
		}
		if d := dist2(m.x, m.y, m.z, target.X, target.Y, target.Z); d < 256 {
			g.shootFireball(m, target)
			m.attackCd = 40
		}
		return
	}
	if m.kind.name == "shulker" {
		target := g.nearestPlayerDim(m.dim, m.x, m.y, m.z, 16)
		if target == nil || target.Dead {
			return
		}
		if m.attackCd > 0 {
			m.attackCd--
			return
		}
		g.shootShulkerBullet(m, target)
		m.attackCd = 60
	}
}

// shootFireball 烈焰人火球（复用箭矢实体，fire 标记点燃）。
func (g *Game) shootFireball(m *mob, target *entity.Player) {
	eid := g.nextEID.Add(1)
	dx, dy, dz := target.X-m.x, (target.Y+1)-(m.y+1.5), target.Z-m.z
	d := math.Sqrt(dx*dx + dy*dy + dz*dz)
	if d < 0.5 {
		d = 0.5
	}
	speed := 0.45
	ar := &arrow{eid: eid, owner: m.eid, dim: m.dim,
		x: m.x, y: m.y + 1.5, z: m.z,
		vx: dx / d * speed, vy: dy/d*speed + d*0.015, vz: dz / d * speed,
		dmg: 5, fire: true}
	ar.uuid = entityUUID(eid)
	g.arrows[eid] = ar
	for _, v := range g.chunkViewers(m.dim, int32(m.x)>>4, int32(m.z)>>4) {
		typ := g.entityTypeID(v, "small_fireball")
		eid2, x, y, z := eid, ar.x, ar.y, ar.z
		vx, vy, vz := int16(ar.vx*8000), int16(ar.vy*8000), int16(ar.vz*8000)
		v.Send.SendPlay("minecraft:add_entity", func(w *protocol.Writer) {
			packet.SpawnEntity{EntityID: eid2, UUID: ar.uuid, Type: typ,
				X: x, Y: y, Z: z, VX: vx, VY: vy, VZ: vz, ObjectData: m.eid}.Write(w)
		})
	}
	g.mobSound(m, "entity.blaze.shoot")
}

// shootShulkerBullet 潜贝贝子弹：追踪目标的漂浮弹（命中失重 +2 伤）。
func (g *Game) shootShulkerBullet(m *mob, target *entity.Player) {
	eid := g.nextEID.Add(1)
	b := &shulkerBullet{eid: eid, uuid: entityUUID(eid), owner: m.eid, dim: m.dim,
		x: m.x, y: m.y + 0.5, z: m.z, target: target.EntityID}
	g.bullets[eid] = b
	for _, v := range g.chunkViewers(m.dim, int32(m.x)>>4, int32(m.z)>>4) {
		typ := g.entityTypeID(v, "shulker_bullet")
		eid2, x, y, z := eid, b.x, b.y, b.z
		v.Send.SendPlay("minecraft:add_entity", func(w *protocol.Writer) {
			packet.SpawnEntity{EntityID: eid2, UUID: b.uuid, Type: typ,
				X: x, Y: y, Z: z, ObjectData: m.eid}.Write(w)
		})
	}
	g.mobSound(m, "entity.shulker.shoot")
}

// shulkerBullet 一个追踪子弹。
type shulkerBullet struct {
	eid    int32
	uuid   protocol.UUID
	owner  int32
	dim    int32
	target int32
	x, y, z float64
	age    int
}

// tickShulkerBullets 子弹追踪：命中玩家（失重 30 刻 + 2 伤）或超时消散。
func (g *Game) tickShulkerBullets() {
	for eid, b := range g.bullets {
		b.age++
		t := g.players[b.target]
		if b.age > 200 || t == nil || t.Dim != b.dim || t.Dead {
			g.removeBullet(eid, b)
			continue
		}
		dx, dy, dz := t.X-b.x, t.Y+1-b.y, t.Z-b.z
		d := math.Sqrt(dx*dx + dy*dy + dz*dz)
		if d < 0.8 {
			// 命中：失重 30 刻 + 4 伤
			t.Effects["levitation"] = 30
			t.EffectAmp["levitation"] = 0
			g.syncEffects(t)
			g.damage(t, 4, damageGeneric)
			g.removeBullet(eid, b)
			continue
		}
		sp := 0.25
		b.x += dx / d * sp
		b.y += dy / d * sp
		b.z += dz / d * sp
		if g.tick%10 == 0 {
			eid2, x, y, z := eid, b.x, b.y, b.z
			for _, v := range g.players {
				if v.Dim != b.dim {
					continue
				}
				v.Send.SendPlay("minecraft:teleport_entity", func(w *protocol.Writer) {
					packet.WriteTeleportEntity(w, eid2, x, y, z, 0, 0, 0, 0, 0, true)
				})
			}
		}
	}
}

func (g *Game) removeBullet(eid int32, b *shulkerBullet) {
	delete(g.bullets, eid)
	for _, p := range g.players {
		if p.Dim != b.dim {
			continue
		}
		p.Send.SendPlay("minecraft:remove_entities", func(w *protocol.Writer) {
			packet.WriteRemoveEntities(w, []int32{eid})
		})
	}
}

// ---- 刷怪笼 ----

// tickSpawners 每 100 刻：玩家附近 8 格内的刷怪笼按维度/深度出怪
//（下界→烈焰人，主世界 y<MinY+40→蠹虫），附近同类 <4 只时生成 1-2。
func (g *Game) tickSpawners() {
	if g.tick%100 != 0 {
		return
	}
	for _, p := range g.players {
		if p.Dead {
			continue
		}
		px, py, pz := floorI(p.X), floorI(p.Y), floorI(p.Z)
		for dx := int32(-8); dx <= 8; dx += 2 {
			for dy := int32(-4); dy <= 4; dy += 2 {
				for dz := int32(-8); dz <= 8; dz += 2 {
					if g.w(p).GetBlock(px+dx, py+dy, pz+dz) != 5444 {
						continue
					}
					kind := ""
					switch p.Dim {
					case DimNether:
						kind = "blaze"
					case DimOverworld:
						if py+dy < world.MinY+45 {
							kind = "silverfish"
						}
					}
					if kind == "" {
						continue
					}
					near := 0
					for _, m := range g.mobs {
						if m.dim == p.Dim && m.kind.name == kind &&
							dist2(m.x, m.y, m.z, p.X, p.Y, p.Z) < 900 {
							near++
						}
					}
					if near >= 4 {
						continue
					}
					n := 1 + rand.Intn(2)
					for i := 0; i < n; i++ {
						g.spawnMobIn(p.Dim, kind,
							float64(px+dx)+0.5+rand.Float64()-0.5,
							float64(py+dy), float64(pz+dz)+0.5+rand.Float64()-0.5)
					}
				}
			}
		}
	}
}

// ---- 下界疣 ----

// tryPlantWart 灵魂沙顶面种下界疣。
func (g *Game) tryPlantWart(p *entity.Player, u packet.UseItemOn) bool {
	if g.itemNameOf(p, p.Held().ID) != "nether_wart" {
		return false
	}
	if u.Face != 1 {
		return false
	}
	if g.w(p).GetBlock(u.X, u.Y, u.Z) != 8668 { // soul_sand
		return false
	}
	if g.w(p).GetBlock(u.X, u.Y+1, u.Z) != world.StateAir {
		return false
	}
	g.applyBlock(p.Dim, u.X, u.Y+1, u.Z, wartAges[0], u.Sequence, p)
	g.crops[[4]int32{p.Dim, u.X, u.Y + 1, u.Z}] = -1 // 负数标记下界疣
	if !g.isCreative(p) {
		s := &p.Slots[36+p.HeldSlot]
		s.Count--
		if s.Count <= 0 {
			*s = entity.Item{}
		}
		g.syncInventory(p)
	}
	return true
}

// isWartState 状态是否下界疣。
func isWartState(s uint32) bool { return s >= 11181 && s <= 11184 }

// harvestWart 挖下界疣：掉 2-4 个（成熟）或 1 个。
func (g *Game) harvestWart(p *entity.Player, dim, x, y, z int32, old uint32) {
	delete(g.crops, [4]int32{dim, x, y, z})
	age := int32(old - 11181)
	n := int32(1)
	if age >= 3 {
		n = 2 + rand.Int31n(3)
	}
	g.spawnItem(dim, float64(x)+0.5, float64(y)+0.3, float64(z)+0.5,
		entity.Item{ID: g.itemID777("nether_wart"), Count: n})
}

// ---- 末地城潜贝贝与战利品 ----

// ensureEndCityMobs 玩家进末地时在外岛城塔布设潜贝贝（一次性标记）。
func (g *Game) ensureEndCityMobs() {
	if g.endCityMobsDone {
		return
	}
	g.endCityMobsDone = true
	for i := 0; i < 4; i++ {
		cx, cz := terrain.EndCityPos(g.Cfg.WorldSeed, i)
		base := float64(world.MinY + 84)
		for _, off := range [][2]int32{{-3, 0}, {3, 0}, {0, -3}, {2, 2}} {
			g.spawnMobIn(DimEnd, "shulker", float64(cx+off[0])+0.5, base, float64(cz+off[1])+0.5)
		}
	}
}

// maybeEndCityLoot 首次打开末地城箱：填战利品（壳/钻石/铁/金/附魔书/鞍）。
func (g *Game) maybeEndCityLoot(p *entity.Player, pos [4]int32) {
	if pos[0] != DimEnd || g.endCityLoot[pos] {
		return
	}
	g.endCityLoot[pos] = true
	ids := g.itemIDs777()
	loot := []entity.Item{
		{ID: ids["shulker_shell"], Count: 2},
		{ID: ids["diamond"], Count: 3},
		{ID: ids["iron_ingot"], Count: 5},
		{ID: ids["gold_ingot"], Count: 4},
		{ID: ids["saddle"], Count: 1},
	}
	cs := g.chests[pos]
	for i, it := range loot {
		if i < 27 {
			cs.slots[3+i*2] = it
		}
	}
	_ = p
}

// itemIDs777 777 基准名→ID（含懒缓存）。
func (g *Game) itemIDs777() map[string]int32 {
	if len(itemNameToID777) == 0 {
		for n, id := range assets.ItemIDs(777) {
			itemNameToID777[n] = id
		}
	}
	return itemNameToID777
}
