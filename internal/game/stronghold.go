package game

// 要塞交互：末地门框放末影之眼（eye=false → true 保留朝向）、12 框全满 →
// 中央 3x3 填充 end_portal；投掷末影之眼指向要塞；末地水晶右键出口门
// 基座重生末影龙。

import (
	"math"

	"mcserver/internal/entity"
	"mcserver/internal/packet"
	"mcserver/internal/protocol"
	"mcserver/internal/world"

	"mcserver/plugins/terrain"
)

// 末地门框状态（777：eye=false N/S/W/E = 11207-11210，eye=true = 11203-11206）。
const (
	frameNoEyeNorth uint32 = 11207
	frameEyeNorth   uint32 = 11203
)

// frameEyeTrue 框状态 → 放眼后的状态（同 facing）。
var frameEyeTrue = map[uint32]uint32{
	11207: 11203, // north
	11208: 11204, // south
	11209: 11205, // west
	11210: 11206, // east
}

// isEndPortalFrame 状态是否为末地门框。
func isEndPortalFrame(s uint32) bool { return s >= 11207 && s <= 11210 }

// tryPlaceEyeInFrame 手持末影之眼右键门框：放入眼睛；12 框全满则激活传送门。
func (g *Game) tryPlaceEyeInFrame(p *entity.Player, u packet.UseItemOn) bool {
	if g.itemNameOf(p, p.Held().ID) != "ender_eye" {
		return false
	}
	s := g.w(p).GetBlock(u.X, u.Y, u.Z)
	if !isEndPortalFrame(s) {
		return false
	}
	g.applyBlock(p.Dim, u.X, u.Y, u.Z, frameEyeTrue[s], u.Sequence, p)
	if !g.isCreative(p) {
		held := &p.Slots[36+p.HeldSlot]
		held.Count--
		if held.Count <= 0 {
			*held = entity.Item{}
		}
		g.syncInventory(p)
	}
	g.blockSound(p.Dim, float64(u.X), float64(u.Y), float64(u.Z), "block.end_portal_frame.fill")
	g.maybeActivateEndPortal(p.Dim, u.X, u.Y, u.Z)
	return true
}

// maybeActivateEndPortal 检查以某框为中心 ±3 的 5x5 门环是否 12 框全放眼，
// 是则中央 3x3 填充 end_portal。
func (g *Game) maybeActivateEndPortal(dim, x, y, z int32) {
	w := g.wd(dim)
	for cx := x - 2; cx <= x+2; cx++ {
		for cz := z - 2; cz <= z+2; cz++ {
			if !g.portalRingComplete(w, cx, y, cz) {
				continue
			}
			// 填充内腔（替换岩浆/空气）
			for dx := int32(-1); dx <= 1; dx++ {
				for dz := int32(-1); dz <= 1; dz++ {
					cur := w.GetBlock(cx+dx, y, cz+dz)
					if cur == world.StateAir || cur == world.StateLava {
						g.applyBlock(dim, cx+dx, y, cz+dz, endPortalState, 0, nil)
					}
				}
			}
			g.blockSound(dim, float64(cx), float64(y), float64(cz), "block.end_portal.spawn")
			return
		}
	}
}

// portalRingComplete (cx,cz) 是否为门环中心（边框 12 框全部 eye=true）。
func (g *Game) portalRingComplete(w *world.Manager, cx, y, cz int32) bool {
	frames := 0
	for dx := int32(-2); dx <= 2; dx++ {
		for dz := int32(-2); dz <= 2; dz++ {
			if abs32(dx) != 2 && abs32(dz) != 2 {
				continue
			}
			if abs32(dx) == 2 && abs32(dz) == 2 {
				continue // 角不算
			}
			s := w.GetBlock(cx+dx, y, cz+dz)
			if s < 11203 || s > 11206 { // eye=true 族
				return false
			}
			frames++
		}
	}
	return frames == 12
}

// throwEnderEye use_item（非对方块）手持末影之眼：投出指向要塞。
func (g *Game) throwEnderEye(p *entity.Player) bool {
	if g.itemNameOf(p, p.Held().ID) != "ender_eye" {
		return false
	}
	sx, sz := terrain.StrongholdPos(g.Cfg.WorldSeed)
	dx := float64(sx) - p.X
	dz := float64(sz) - p.Z
	d := math.Sqrt(dx*dx + dz*dz)
	vx, vy, vz := 0.0, 0.4, 0.0
	if d > 24 {
		vx, vz = dx/d*0.8, dz/d*0.8
	} else {
		vy = 0.8 // 到位：垂直向上提示
	}
	if !g.isCreative(p) {
		s := &p.Slots[36+p.HeldSlot]
		s.Count--
		if s.Count <= 0 {
			*s = entity.Item{}
		}
		g.syncInventory(p)
	}
	eid := g.nextEID.Add(1)
	tp := &thrownProjectile{eid: eid, uuid: entityUUID(eid), kind: "ender_eye",
		owner: p.EntityID, dim: p.Dim,
		x: p.X, y: p.Y + 1.5, z: p.Z, vx: vx, vy: vy, vz: vz}
	g.thrown[eid] = tp
	for _, v := range g.chunkViewers(p.Dim, int32(p.X)>>4, int32(p.Z)>>4) {
		typ := g.entityTypeID(v, "eye_of_ender")
		eid2, xx, yy, zz := eid, tp.x, tp.y, tp.z
		ivx, ivy, ivz := int16(vx*8000), int16(vy*8000), int16(vz*8000)
		v.Send.SendPlay("minecraft:add_entity", func(w *protocol.Writer) {
			packet.SpawnEntity{EntityID: eid2, UUID: tp.uuid, Type: typ,
				X: xx, Y: yy, Z: zz, VX: ivx, VY: ivy, VZ: ivz}.Write(w)
		})
	}
	g.playSoundTo(p, SoundPlayer, "entity.ender_eye.launch", 1, 1)
	return true
}

// tryRespawnDragon 末地水晶右键出口门基座：重生末影龙（简化：一次一个水晶）。
func (g *Game) tryRespawnDragon(p *entity.Player, u packet.UseItemOn) bool {
	if g.itemNameOf(p, p.Held().ID) != "end_crystal" {
		return false
	}
	if p.Dim != DimEnd || !g.dragonDefeated || g.dragon != nil {
		return false
	}
	// 出口门基座（中心柱 bedrock）附近
	if abs32(u.X) > 3 || abs32(u.Z) > 3 {
		return false
	}
	if g.w(p).GetBlock(u.X, u.Y, u.Z) != world.StateBedrock {
		return false
	}
	if !g.isCreative(p) {
		held := &p.Slots[36+p.HeldSlot]
		held.Count--
		if held.Count <= 0 {
			*held = entity.Item{}
		}
		g.syncInventory(p)
	}
	g.dragonDefeated = false
	g.saveEndState()
	g.ensureDragonFight()
	g.blockSound(p.Dim, float64(u.X), float64(u.Y), float64(u.Z), "entity.end_crystal.respawn")
	return true
}
