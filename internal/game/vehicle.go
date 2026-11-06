package game

// 载具系统：矿车（铁轨上放置/乘坐/移动/下车/空车物理）与鞍猪
//（player_input 驱动、胡萝卜钓竿加速）。村民交易见 villager.go。

import (
	"math"

	"mcserver/internal/entity"
	"mcserver/internal/packet"
	"mcserver/internal/protocol"
	"mcserver/internal/world"
)

// railStates 铁轨状态区间（直线/弯轨 7315-7334）。
func isRail(s uint32) bool { return s >= 7315 && s <= 7334 }

// minecart 一辆矿车。
type minecart struct {
	eid      int32
	uuid     protocol.UUID
	dim      int32
	x, y, z  float64
	vx, vy   float64
	vz       float64
	rider    int32 // 乘客玩家 EID（0 = 空）
	moving   bool  // 有惯性
}

// railShapeFor 依邻轨自动选 shape（直线 NS/EW；两侧正交邻轨 → 弯）。
// 邻轨集合 → 弯形映射：north+east=north_east 等。
func railShapeFor(g *Game, dim, x, y, z int32) uint32 {
	n := isRail(g.wd(dim).GetBlock(x, y, z-1))
	s := isRail(g.wd(dim).GetBlock(x, y, z+1))
	e := isRail(g.wd(dim).GetBlock(x+1, y, z))
	w := isRail(g.wd(dim).GetBlock(x-1, y, z))
	var shape string
	switch {
	case n && e:
		shape = "north_east"
	case n && w:
		shape = "north_west"
	case s && e:
		shape = "south_east"
	case s && w:
		shape = "south_west"
	case e || w:
		shape = "east_west"
	default:
		shape = "north_south"
	}
	for st, full := range fullStateNames {
		if full == "minecraft:rail[shape="+shape+",waterlogged=false]" {
			return st
		}
	}
	return 0
}

// autoShapeRail 放置铁轨后按邻轨修正形状（含邻轨回调修正）。
func (g *Game) autoShapeRail(dim, x, y, z int32) {
	w := g.wd(dim)
	if !isRail(w.GetBlock(x, y, z)) {
		return
	}
	if ns := railShapeFor(g, dim, x, y, z); ns != 0 && ns != w.GetBlock(x, y, z) {
		g.applyBlock(dim, x, y, z, ns, 0, nil)
	}
	// 邻轨回调
	for _, d := range [][3]int32{{0, 0, 1}, {0, 0, -1}, {1, 0, 0}, {-1, 0, 0}} {
		nx, ny, nz := x+d[0], y+d[1], z+d[2]
		if isRail(w.GetBlock(nx, ny, nz)) {
			if ns := railShapeFor(g, dim, nx, ny, nz); ns != 0 && ns != w.GetBlock(nx, ny, nz) {
				g.applyBlock(dim, nx, ny, nz, ns, 0, nil)
			}
		}
	}
}

// tryPlaceMinecart 手持矿车右键铁轨：生成矿车。
func (g *Game) tryPlaceMinecart(p *entity.Player, u packet.UseItemOn) bool {
	if g.itemNameOf(p, p.Held().ID) != "minecart" {
		return false
	}
	if !isRail(g.w(p).GetBlock(u.X, u.Y, u.Z)) {
		return false
	}
	g.spawnMinecartAt(p.Dim, float64(u.X)+0.5, float64(u.Y)+0.06, float64(u.Z)+0.5, 0)
	if !g.isCreative(p) {
		s := &p.Slots[36+p.HeldSlot]
		s.Count--
		if s.Count <= 0 {
			*s = entity.Item{}
		}
		g.syncInventory(p)
	}
	if u.Sequence > 0 {
		p.Send.SendPlay("minecraft:block_changed_ack", func(w *protocol.Writer) {
			packet.WriteBlockChangedAck(w, u.Sequence)
		})
	}
	return true
}

// spawnMinecartAt 生成矿车实体并广播。
func (g *Game) spawnMinecartAt(dim int32, x, y, z float64, vx float64) {
	eid := g.nextEID.Add(1)
	mc := &minecart{eid: eid, uuid: entityUUID(eid), dim: dim, x: x, y: y, z: z, vx: vx}
	g.carts[eid] = mc
	for _, v := range g.chunkViewers(dim, int32(x)>>4, int32(z)>>4) {
		g.sendCartSpawn(v, mc)
	}
}

func (g *Game) sendCartSpawn(v *entity.Player, mc *minecart) {
	typ := g.entityTypeID(v, "minecart")
	eid, x, y, z := mc.eid, mc.x, mc.y, mc.z
	v.Send.SendPlay("minecraft:add_entity", func(w *protocol.Writer) {
		packet.SpawnEntity{EntityID: eid, UUID: mc.uuid, Type: typ,
			X: x, Y: y, Z: z}.Write(w)
	})
}

// tryRideEntity 右键实体（interact）：矿车乘坐 / 鞍猪骑乘。
func (g *Game) tryRideEntity(p *entity.Player, target int32) bool {
	// 矿车
	if mc, ok := g.carts[target]; ok {
		if mc.rider == 0 && mc.dim == p.Dim &&
			dist2(p.X, p.Y, p.Z, mc.x, mc.y, mc.z) < 16 {
			g.mountCart(p, mc)
			return true
		}
		return true
	}
	// 鞍猪
	if m, ok := g.mobs[target]; ok && m.kind.name == "pig" && g.saddled[m.eid] &&
		m.dim == p.Dim && dist2(p.X, p.Y, p.Z, m.x, m.y, m.z) < 16 {
		g.mountPig(p, m)
		return true
	}
	return false
}

// mountCart 上车：set_passengers + 玩家位置吸附。
func (g *Game) mountCart(p *entity.Player, mc *minecart) {
	mc.rider = p.EntityID
	p.Vehicle = mc.eid
	g.sendPassengers(mc)
	p.X, p.Y, p.Z = mc.x, mc.y+0.3, mc.z
	p.TeleportID++
	id := p.TeleportID
	x, y, z := p.X, p.Y, p.Z
	p.Send.SendPlay("minecraft:player_position", func(w *protocol.Writer) {
		packet.WritePlayerPosition(w, packet.PlayerPositionOut{TeleportID: id, X: x, Y: y, Z: z})
	})
}

func (g *Game) sendPassengers(mc *minecart) {
	eid := mc.eid
	rider := mc.rider
	for _, v := range g.players {
		if v.Dim != mc.dim {
			continue
		}
		v.Send.SendPlay("minecraft:set_passengers", func(w *protocol.Writer) {
			w.VarInt(eid)
			w.VarInt(1)
			w.VarInt(rider)
		})
	}
}

// dismount 玩家下车（shift 或断线）。
func (g *Game) dismount(p *entity.Player) {
	if p.Vehicle == 0 {
		return
	}
	if mc, ok := g.carts[p.Vehicle]; ok && mc.rider == p.EntityID {
		mc.rider = 0
		g.sendPassengers(mc) // 空乘客列表
	}
	if m, ok := g.mobs[p.Vehicle]; ok {
		delete(g.riding, p.EntityID)
		_ = m
	}
	p.Vehicle = 0
	p.Y += 1 // 下车抬升防卡
	p.TeleportID++
	id := p.TeleportID
	x, y, z := p.X, p.Y, p.Z
	p.Send.SendPlay("minecraft:player_position", func(w *protocol.Writer) {
		packet.WritePlayerPosition(w, packet.PlayerPositionOut{TeleportID: id, X: x, Y: y, Z: z})
	})
}

// handleVehicleMove 处理 serverbound move_vehicle：乘骑中的矿车跟随。
func (g *Game) handleVehicleMove(p *entity.Player, x, y, z float64, yaw, pitch float32) {
	if p.Vehicle == 0 {
		return
	}
	if mc, ok := g.carts[p.Vehicle]; ok && mc.rider == p.EntityID {
		mc.x, mc.y, mc.z = x, y, z
		mc.moving = true
		// 玩家位置同步到车上（其余玩家经 teleport 广播）
		p.X, p.Y, p.Z = x, y+0.3, z
		g.broadcastCart(mc, yaw)
	}
}

// broadcastCart 每 5 刻广播矿车位置。
func (g *Game) broadcastCart(mc *minecart, yaw float32) {
	eid, x, y, z := mc.eid, mc.x, mc.y, mc.z
	for _, v := range g.players {
		if v.Dim != mc.dim || v.EntityID == mc.rider {
			continue
		}
		v.Send.SendPlay("minecraft:teleport_entity", func(w *protocol.Writer) {
			packet.WriteTeleportEntity(w, eid, x, y, z, 0, 0, 0, yaw, 0, true)
		})
	}
}

// tickCarts 矿车物理：无人乘坐时沿铁轨滑动（重力 + 摩擦 + 轨向导引）。
func (g *Game) tickCarts() {
	for eid, mc := range g.carts {
		w := g.wd(mc.dim)
		if mc.rider != 0 {
			// 有人：位置由 move_vehicle 驱动；掉下轨道则下车
			if !isRail(w.GetBlock(floorI(mc.x), floorI(mc.y-0.1), floorI(mc.z))) &&
				!g.blockAtDim(mc.dim, floorI(mc.x), floorI(mc.y-0.1), floorI(mc.z)) {
				if p := g.players[mc.rider]; p != nil {
					g.dismount(p)
				}
			}
			continue
		}
		// 空车：重力 + 摩擦；落轨滑行简化为水平摩擦
		if mc.moving || mc.vy != 0 {
			mc.vy -= 0.04
			ny := mc.y + mc.vy
			onRail := isRail(w.GetBlock(floorI(mc.x), floorI(mc.y-0.06), floorI(mc.z)))
			if onRail {
				ny = mc.y
				mc.vy = 0
			} else if g.blockAtDim(mc.dim, floorI(mc.x), floorI(ny-0.06), floorI(mc.z)) {
				ny = float64(floorI(ny-0.06)+1) + 0.06
				mc.vy = 0
				mc.vx *= 0.5
				mc.vz *= 0.5
			}
			nx, nz := mc.x+mc.vx, mc.z+mc.vz
			mc.x, mc.y, mc.z = nx, ny, nz
			mc.vx *= 0.98
			mc.vz *= 0.98
			if absF(mc.vx) < 0.02 && absF(mc.vz) < 0.02 && mc.vy == 0 {
				mc.vx, mc.vz, mc.moving = 0, 0, false
			}
			if g.tick%5 == 0 {
				g.broadcastCart(mc, 0)
			}
		}
		if mc.y < world.MinY-10 {
			delete(g.carts, eid)
			for _, p := range g.players {
				p.Send.SendPlay("minecraft:remove_entities", func(w *protocol.Writer) {
					packet.WriteRemoveEntities(w, []int32{eid})
				})
			}
		}
	}
}

// ---- 鞍猪 ----

// trySaddlePig 手持鞍右键猪：上鞍。
func (g *Game) trySaddlePig(p *entity.Player, m *mob) bool {
	if m.kind.name != "pig" || g.saddled[m.eid] {
		return false
	}
	if g.itemNameOf(p, p.Held().ID) != "saddle" {
		return false
	}
	g.saddled[m.eid] = true
	if !g.isCreative(p) {
		s := &p.Slots[36+p.HeldSlot]
		s.Count--
		if s.Count <= 0 {
			*s = entity.Item{}
		}
		g.syncInventory(p)
	}
	g.mobSound(m, "entity.pig.saddle")
	return true
}

// mountPig 骑猪：玩家吸附猪背，player_input 驱动。
func (g *Game) mountPig(p *entity.Player, m *mob) {
	if g.riding[p.EntityID] != 0 {
		return
	}
	g.riding[p.EntityID] = m.eid
	p.Vehicle = m.eid
	eid := m.eid
	rider := p.EntityID
	for _, v := range g.players {
		if v.Dim != m.dim {
			continue
		}
		v.Send.SendPlay("minecraft:set_passengers", func(w *protocol.Writer) {
			w.VarInt(eid)
			w.VarInt(1)
			w.VarInt(rider)
		})
	}
	p.X, p.Y, p.Z = m.x, m.y+0.9, m.z
}

// handlePlayerInput 骑猪时的输入驱动（前进沿玩家朝向；shift 下车）。
func (g *Game) handlePlayerInput(p *entity.Player, inputs byte) {
	if p.Vehicle == 0 {
		return
	}
	// shift 下车
	if inputs&0x20 != 0 {
		g.dismount(p)
		return
	}
	m := g.mobs[p.Vehicle]
	if m == nil {
		return
	}
	speed := 0.14
	if g.itemNameOf(p, p.Held().ID) == "carrot_on_a_stick" {
		speed = 0.25 // 钓竿加速
	}
	if p.HorseSpeed > 0 {
		speed = p.HorseSpeed // 马速
	}
	if inputs&0x01 != 0 || true { // forward（马常走）
		yaw := float64(p.Yaw) * math.Pi / 180
		dx, dz := -math.Sin(yaw), math.Cos(yaw)
		nx, nz := m.x+dx*speed, m.z+dz*speed
		if g.walkableAt(m.dim, floorI(nx), floorI(m.y), floorI(nz)) {
			m.x, m.z = nx, nz
		} else if g.walkableAt(m.dim, floorI(nx), floorI(m.y)+1, floorI(nz)) && m.vy == 0 {
			m.vy = 0.42
			m.x, m.z = nx, nz
		}
	}
	p.X, p.Y, p.Z = m.x, m.y+0.9, m.z
}
