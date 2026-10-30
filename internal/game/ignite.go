package game

// TNT（打火石点燃 → 引信实体 80 刻 → 爆炸）、火方块（点燃/自然熄灭/点燃 TNT）、
// 床在主世界外爆炸（原版行为）。

import (
	"math/rand"

	"mcserver/internal/entity"
	"mcserver/internal/packet"
	"mcserver/internal/protocol"
	"mcserver/internal/world"
)

// tntState TNT 方块（unstable=false）。
const tntState uint32 = 3833

// primedTnt 一个点燃的 TNT 实体。
type primedTnt struct {
	eid      int32
	uuid     protocol.UUID
	dim      int32
	x, y, z  float64
	vy       float64
	fuse     int32
}

// tryIgniteTnt 打火石右键 TNT：移除方块并生成引信实体。返回 true 拦截。
func (g *Game) tryIgniteTnt(p *entity.Player, u packet.UseItemOn) bool {
	if g.itemNameOf(p, p.Held().ID) != "flint_and_steel" {
		return false
	}
	if g.w(p).GetBlock(u.X, u.Y, u.Z) != tntState {
		return false
	}
	g.applyBlock(p.Dim, u.X, u.Y, u.Z, world.StateAir, u.Sequence, p)
	g.primeTnt(p.Dim, float64(u.X)+0.5, float64(u.Y), float64(u.Z)+0.5)
	g.blockSound(p.Dim, float64(u.X), float64(u.Y), float64(u.Z), "entity.tnt.primed")
	if !g.isCreative(p) {
		g.damageHeld(p)
	}
	return true
}

// primeTnt 点燃一个 TNT 实体（含小幅上抛）。
func (g *Game) primeTnt(dim int32, x, y, z float64) {
	eid := g.nextEID.Add(1)
	t := &primedTnt{eid: eid, uuid: entityUUID(eid), dim: dim,
		x: x, y: y, z: z, vy: 0.2, fuse: 80}
	g.tnts[eid] = t
	for _, v := range g.chunkViewers(dim, int32(x)>>4, int32(z)>>4) {
		typ := g.entityTypeID(v, "tnt")
		eid2, xx, yy, zz := eid, x, y, z
		v.Send.SendPlay("minecraft:add_entity", func(w *protocol.Writer) {
			packet.SpawnEntity{EntityID: eid2, UUID: t.uuid, Type: typ,
				X: xx, Y: yy, Z: zz, VX: int16(rand.Intn(41) - 20), VY: 1600, VZ: int16(rand.Intn(41) - 20)}.Write(w)
		})
	}
}

// tickTnts 引信推进：物理下落 + 到期爆炸（爆炸范围内 TNT 连锁点燃）。
func (g *Game) tickTnts() {
	for eid, t := range g.tnts {
		t.fuse--
		if t.fuse <= 0 {
			delete(g.tnts, eid)
			g.removeTntBroadcast(t)
			g.explode(t.dim, t.x, t.y+0.5, t.z, 4)
			// 连锁：爆炸范围内的 TNT 方块被点燃
			g.chainTnt(t.dim, int32(t.x), int32(t.y), int32(t.z), 6)
			continue
		}
		t.vy -= 0.04
		ny := t.y + t.vy
		if g.blockAtDim(t.dim, floorI(t.x), floorI(ny), floorI(t.z)) {
			ny = float64(floorI(ny) + 1)
			t.vy = 0
		}
		t.y = ny
		// 每 10 刻广播位置（下落表现）
		if t.fuse%10 == 0 {
			eid2, x, y, z := eid, t.x, t.y, t.z
			for _, v := range g.players {
				if v.Dim != t.dim {
					continue
				}
				v.Send.SendPlay("minecraft:teleport_entity", func(w *protocol.Writer) {
					packet.WriteTeleportEntity(w, eid2, x, y, z, 0, 0, 0, 0, 0, true)
				})
			}
		}
	}
}

// chainTnt 半径内 TNT 方块转为点燃（延迟随机错开连锁节奏）。
func (g *Game) chainTnt(dim, cx, cy, cz int32, r int32) {
	w := g.wd(dim)
	for dx := -r; dx <= r; dx++ {
		for dy := -r; dy <= r; dy++ {
			for dz := -r; dz <= r; dz++ {
				if dx*dx+dy*dy+dz*dz > r*r {
					continue
				}
				x, y, z := cx+dx, cy+dy, cz+dz
				if w.GetBlock(x, y, z) != tntState {
					continue
				}
				g.applyBlock(dim, x, y, z, world.StateAir, 0, nil)
				t := &primedTnt{}
				_ = t
				g.primeTnt(dim, float64(x)+0.5, float64(y), float64(z)+0.5)
				// 缩短引信制造连锁节奏
				if last := len(g.tnts); last > 0 {
					for _, tt := range g.tnts {
						if floorI(tt.x) == x && floorI(tt.y) == y && floorI(tt.z) == z {
							tt.fuse = 10 + rand.Int31n(20)
						}
					}
				}
			}
		}
	}
}

func (g *Game) removeTntBroadcast(t *primedTnt) {
	eid := t.eid
	for _, p := range g.players {
		if p.Dim != t.dim {
			continue
		}
		p.Send.SendPlay("minecraft:remove_entities", func(w *protocol.Writer) {
			packet.WriteRemoveEntities(w, []int32{eid})
		})
	}
}

// ---- 火方块 ----

// tryIgniteFire 打火石点燃火（非传送门/TNT 的常规用法）：目标面外空气放火。
// 返回 true 拦截。
func (g *Game) tryIgniteFire(p *entity.Player, u packet.UseItemOn) bool {
	if g.itemNameOf(p, p.Held().ID) != "flint_and_steel" {
		return false
	}
	fdx, fdy, fdz := packet.FaceOffset(u.Face)
	tx, ty, tz := u.X+fdx, u.Y+fdy, u.Z+fdz
	w := g.w(p)
	if w.GetBlock(tx, ty, tz) != world.StateAir {
		return false
	}
	// 至少一个相邻实体方块（火要有依托）
	supported := g.blockAtDim(p.Dim, tx, ty-1, tz) || g.blockAtDim(p.Dim, tx-1, ty, tz) ||
		g.blockAtDim(p.Dim, tx+1, ty, tz) || g.blockAtDim(p.Dim, tx, ty, tz-1) || g.blockAtDim(p.Dim, tx, ty, tz+1)
	if !supported {
		return false
	}
	g.applyBlock(p.Dim, tx, ty, tz, world.FireMin, u.Sequence, p)
	g.trackFire(p.Dim, tx, ty, tz)
	g.blockSound(p.Dim, float64(tx), float64(ty), float64(tz), "item.flintandsteel.use")
	if !g.isCreative(p) {
		g.damageHeld(p)
	}
	return true
}

// trackFire 登记火方块（寿命 8-15 秒随机，到期自熄；原版火可蔓延此处简化）。
func (g *Game) trackFire(dim, x, y, z int32) {
	g.fires[[4]int32{dim, x, y, z}] = 160 + rand.Int31n(140)
}

// tickFires 火寿命推进：到期熄灭；TNT 相邻火被点燃；可燃方块（木/叶）概率烧毁。
func (g *Game) tickFires() {
	for pos, life := range g.fires {
		life--
		if life <= 0 {
			delete(g.fires, pos)
			dim, x, y, z := pos[0], pos[1], pos[2], pos[3]
			if g.wd(dim).GetBlock(x, y, z) >= world.FireMin {
				g.applyBlock(dim, x, y, z, world.StateAir, 0, nil)
			}
			continue
		}
		g.fires[pos] = life
		// 概率点燃相邻 TNT
		if life%20 == 0 {
			dim, x, y, z := pos[0], pos[1], pos[2], pos[3]
			w := g.wd(dim)
			for _, d := range [][3]int32{{1, 0, 0}, {-1, 0, 0}, {0, 1, 0}, {0, -1, 0}, {0, 0, 1}, {0, 0, -1}} {
				if w.GetBlock(x+d[0], y+d[1], z+d[2]) == tntState {
					g.applyBlock(dim, x+d[0], y+d[1], z+d[2], world.StateAir, 0, nil)
					g.primeTnt(dim, float64(x+d[0])+0.5, float64(y+d[1]), float64(z+d[2])+0.5)
					return
				}
			}
		}
	}
}

// bedExplode 床在主世界外使用即爆炸（原版行为）。
func (g *Game) bedExplode(p *entity.Player, x, y, z int32) {
	g.notify(p, "§c床在这个维度会爆炸！")
	g.applyBlock(p.Dim, x, y, z, world.StateAir, 0, nil)
	g.explode(p.Dim, float64(x)+0.5, float64(y)+0.5, float64(z)+0.5, 5)
	g.blockSound(p.Dim, float64(x), float64(y), float64(z), "entity.generic.explode")
}
