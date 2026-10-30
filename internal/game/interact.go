package game

// 世界交互类方块：箱子（共享容器）、床（睡觉/重生点）、门（开关）、
// 沙/砾石重力（falling_block 实体）、简化水流扩散、树苗生长。
// 床/门为双格方块，状态组合经官方 blocks.json 生成的资产表查询。

import (
	"math/rand"
	"strings"

	"mcserver/internal/assets"
	"mcserver/internal/entity"
	"mcserver/internal/packet"
	"mcserver/internal/protocol"
	"mcserver/internal/world"
)

// menuGeneric9x3 是 menu 注册表的 generic_9x3（原版箱子容器类型）。
const menuGeneric9x3 int32 = 2

const chestBlockState uint32 = 5544 // minecraft:chest 默认状态（facing=north）

const (
	entityFallingBlock int32 = 52
	saplingState       uint32 = 30
	leavesState        uint32 = 209
	logState           uint32 = 8070
)

// bedInfo / doorInfo 反查表（state → 描述）。
type bedInfo struct {
	name             string
	facing           string
	isHead           bool
	footID, headID   uint32 // 该床该朝向的 foot/head 状态
}

type doorInfo struct {
	facing string
	half   string
	open   bool
}

// loadInteractTables 加载床/门状态资产并构建反查表。
func (g *Game) loadInteractTables() {
	g.bedByState = map[uint32]*bedInfo{}
	if b, err := assets.InteractFile("bedstates"); err == nil {
		for _, line := range strings.Split(string(b), "\n") {
			f := strings.Fields(line)
			if len(f) != 4 {
				continue
			}
			foot, head := parseU32(f[2]), parseU32(f[3])
			info := &bedInfo{name: f[0], facing: f[1], footID: foot, headID: head}
			g.bedByState[foot] = info
			g.bedByState[head] = info
		}
	}
	g.doorByState = map[uint32]*doorInfo{}
	if b, err := assets.InteractFile("doorstates"); err == nil {
		for _, line := range strings.Split(string(b), "\n") {
			f := strings.Fields(line)
			if len(f) != 4 {
				continue
			}
			id := parseU32(f[3])
			g.doorByState[id] = &doorInfo{facing: f[0], half: f[1], open: f[2] == "true"}
			g.doorOpenFlip = map[[2]uint32]uint32{}
		}
		for id, info := range g.doorByState {
			want := "true"
			if info.open {
				want = "false"
			}
			for id2, info2 := range g.doorByState {
				if info2.facing == info.facing && info2.half == info.half && (info2.open == (want == "true")) {
					g.doorOpenFlip[[2]uint32{id, 0}] = id2
				}
			}
		}
	}
}

func parseU32(s string) uint32 {
	var v uint32
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0
		}
		v = v*10 + uint32(c-'0')
	}
	return v
}

// facingFromYaw 玩家朝向 → 门/床的朝向名。
func facingFromYaw(yaw float32) string {
	a := ((int(yaw)%360)+360)%360
	switch {
	case a < 45 || a >= 315:
		return "north"
	case a < 135:
		return "east"
	case a < 225:
		return "south"
	default:
		return "west"
	}
}

// facingDir 朝向名 → 单位向量（门头/床头延伸方向）。
func facingDir(f string) (int32, int32) {
	switch f {
	case "north":
		return 0, -1
	case "south":
		return 0, 1
	case "west":
		return -1, 0
	default:
		return 1, 0
	}
}

// ---- 床 ----

// tryPlaceBed 手持床物品放置双格（foot 在目标位、head 在朝向反方向）。
func (g *Game) tryPlaceBed(p *entity.Player, u packet.UseItemOn, tx, ty, tz int32, state uint32) bool {
	if _, isBed := g.bedByState[state]; !isBed {
		return false
	}
	bedName := g.bedNameOfState(state)
	facing := facingFromYaw(p.Yaw + 180) // 头在玩家面向一侧
	foot, head, ok := bedStatesFor(bedName, facing)
	if !ok {
		return false
	}
	dx, dz := facingDir(facing)
	hx, hz := tx+dx, tz+dz
	// 两格都得可放置
	for _, pos := range [][3]int32{{tx, ty, tz}, {hx, ty, hz}} {
		s := g.w(p).GetBlock(pos[0], pos[1], pos[2])
		if s != world.StateAir && s != world.StateWater {
			return false
		}
	}
	g.w(p).SetBlock(tx, ty, tz, foot)
	g.recordBlockChange(p.Dim, tx, ty, tz, foot)
	g.w(p).SetBlock(hx, ty, hz, head)
	g.recordBlockChange(p.Dim, hx, ty, hz, head)
	g.flushBlockUpdates()
	if u.Sequence > 0 {
		p.Send.SendPlay("minecraft:block_changed_ack", func(w *protocol.Writer) {
			packet.WriteBlockChangedAck(w, u.Sequence)
		})
	}
	// 生存消耗
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

// bedNameOfState 从反查表取床物品名（如 red_bed）。
func (g *Game) bedNameOfState(state uint32) string {
	if info, ok := g.bedByState[state]; ok {
		return info.name
	}
	return ""
}

// bedStatesFor 指定床与朝向取 foot/head 状态。
func bedStatesFor(name, facing string) (uint32, uint32, bool) {
	b, err := assets.InteractFile("bedstates")
	if err != nil {
		return 0, 0, false
	}
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Fields(line)
		if len(f) == 4 && f[0] == name && f[1] == facing {
			return parseU32(f[2]), parseU32(f[3]), true
		}
	}
	return 0, 0, false
}

// trySleepBed 右键床：设重生点；夜里且（单人或全员睡）则跳到清晨。
func (g *Game) trySleepBed(p *entity.Player, u packet.UseItemOn) bool {
	info, isBed := g.bedByState[g.w(p).GetBlock(u.X, u.Y, u.Z)]
	if !isBed {
		return false
	}
	if p.Dim != DimOverworld {
		g.bedExplode(p, u.X, u.Y, u.Z)
		return true
	}
	if g.isCreative(p) {
		return true // 创造不睡
	}
	// 床头位置（重生点）
	dx, dz := facingDir(info.facing)
	bx, by, bz := u.X, u.Y, u.Z
	if !info.isHead {
		bx, bz = u.X+dx, u.Z+dz
	}
	p.SpawnPos = [3]float64{float64(bx) + 0.5, float64(by) + 1, float64(bz) + 0.5}
	// 夜间判定（12541-23458）
	night := g.dayTime > 12541 && g.dayTime < 23458
	if !night {
		g.notify(p, "§7你只能在夜晚入睡")
		return true
	}
	// 全体生存玩家都要躺下（简化：单人直接睡；多人需全员执行交互）
	p.Sleeping = true
	allAsleep := true
	for _, other := range g.players {
		if other.EntityID != p.EntityID && !g.isCreative(other) && !other.Sleeping {
			allAsleep = false
		}
	}
	if allAsleep {
		g.dayTime = 1000 // 清晨
		g.broadcastTime()
		g.BroadcastSystem("§7天亮了")
		for _, other := range g.players {
			other.Sleeping = false
		}
	} else {
		g.notify(p, "§7等待其他玩家入睡…")
	}
	return true
}

// breakBedIfHit 挖到床的任一半：两半一起清掉并掉落一张床。
func (g *Game) breakBedIfHit(dim, x, y, z int32, old uint32) {
	info, isBed := g.bedByState[old]
	if !isBed {
		return
	}
	dx, dz := facingDir(info.facing)
	other := [3]int32{x, y, z}
	if info.isHead {
		other = [3]int32{x - dx, y, z - dz}
	} else {
		other = [3]int32{x + dx, y, z + dz}
	}
	// 清另一半（若也是床）
	if _, ok := g.bedByState[g.wd(dim).GetBlock(other[0], other[1], other[2])]; ok {
		g.wd(dim).SetBlock(other[0], other[1], other[2], world.StateAir)
		g.recordBlockChange(dim, other[0], other[1], other[2], world.StateAir)
	}
	g.flushBlockUpdates()
}

// ---- 门 ----

// tryPlaceDoor 手持门物品放置双格。
func (g *Game) tryPlaceDoor(p *entity.Player, u packet.UseItemOn, tx, ty, tz int32, state uint32) bool {
	if _, isDoor := g.doorByState[state]; !isDoor {
		return false
	}
	facing := facingFromYaw(p.Yaw + 180)
	lower, upper, ok := doorStatesFor(facing, false)
	if !ok {
		return false
	}
	if g.w(p).GetBlock(tx, ty+1, tz) != world.StateAir {
		return false
	}
	g.w(p).SetBlock(tx, ty, tz, lower)
	g.recordBlockChange(p.Dim, tx, ty, tz, lower)
	g.w(p).SetBlock(tx, ty+1, tz, upper)
	g.recordBlockChange(p.Dim, tx, ty+1, tz, upper)
	g.flushBlockUpdates()
	if u.Sequence > 0 {
		p.Send.SendPlay("minecraft:block_changed_ack", func(w *protocol.Writer) {
			packet.WriteBlockChangedAck(w, u.Sequence)
		})
	}
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

// doorStatesFor 指定朝向取 lower/upper 状态。
func doorStatesFor(facing string, open bool) (uint32, uint32, bool) {
	b, err := assets.InteractFile("doorstates")
	if err != nil {
		return 0, 0, false
	}
	var lower, upper uint32
	found := 0
	op := "false"
	if open {
		op = "true"
	}
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Fields(line)
		if len(f) == 4 && f[0] == facing && f[2] == op {
			switch f[1] {
			case "lower":
				lower = parseU32(f[3])
				found++
			case "upper":
				upper = parseU32(f[3])
				found++
			}
		}
	}
	return lower, upper, found == 2
}

// toggleDoor 右键门：两半开关互换。
func (g *Game) toggleDoor(p *entity.Player, u packet.UseItemOn) bool {
	s := g.w(p).GetBlock(u.X, u.Y, u.Z)
	info, isDoor := g.doorByState[s]
	if !isDoor {
		return false
	}
	newS := g.doorOpenFlip[[2]uint32{s, 0}]
	if newS == 0 {
		return true
	}
	if info2, ok := g.doorByState[newS]; ok && info2 != nil {
		if info2.open {
			g.blockSound(p.Dim, float64(u.X), float64(u.Y), float64(u.Z), "block.wooden_door.open")
		} else {
			g.blockSound(p.Dim, float64(u.X), float64(u.Y), float64(u.Z), "block.wooden_door.close")
		}
	}
	other := u.Y
	if info.half == "lower" {
		other = u.Y + 1
	} else {
		other = u.Y - 1
	}
	g.w(p).SetBlock(u.X, u.Y, u.Z, newS)
	g.recordBlockChange(p.Dim, u.X, u.Y, u.Z, newS)
	if o := g.w(p).GetBlock(u.X, other, u.Z); g.doorByState[o] != nil {
		no := g.doorOpenFlip[[2]uint32{o, 0}]
		if no != 0 {
			g.w(p).SetBlock(u.X, other, u.Z, no)
			g.recordBlockChange(p.Dim, u.X, other, u.Z, no)
		}
	}
	g.flushBlockUpdates()
	if u.Sequence > 0 {
		p.Send.SendPlay("minecraft:block_changed_ack", func(w *protocol.Writer) {
			packet.WriteBlockChangedAck(w, u.Sequence)
		})
	}
	return true
}

// breakDoorIfHit 挖到门的任一半：两半一起清掉。
func (g *Game) breakDoorIfHit(dim, x, y, z int32, old uint32) {
	info, isDoor := g.doorByState[old]
	if !isDoor {
		return
	}
	other := y
	if info.half == "lower" {
		other = y + 1
	} else {
		other = y - 1
	}
	if g.doorByState[g.wd(dim).GetBlock(x, other, z)] != nil {
		g.wd(dim).SetBlock(x, other, z, world.StateAir)
		g.recordBlockChange(dim, x, other, z, world.StateAir)
	}
	g.flushBlockUpdates()
}

// ---- 沙砾重力 ----

// gravityBlockAt 方块更新后检查上方是否重力方块，是则转为下落实体。
func (g *Game) triggerGravity(dim, x, y, z int32) {
	w := g.wd(dim)
	for yy := y; ; yy++ {
		s := w.GetBlock(x, yy, z)
		if s != world.StateSand && s != world.StateGravel {
			break
		}
		w.SetBlock(x, yy, z, world.StateAir)
		g.recordBlockChange(dim, x, yy, z, world.StateAir)
		g.spawnFallingBlock(dim, x, yy, z, s)
	}
	g.flushBlockUpdates()
}

// fallingBlock 是一个下落中的方块实体。
type fallingBlock struct {
	eid       int32
	uuid      protocol.UUID
	state     uint32
	dim       int32
	x, y, z   float64
	vy        float64
}

// spawnFallingBlock 生成下落方块实体并广播。
func (g *Game) spawnFallingBlock(dim, x, y, z int32, state uint32) {
	eid := g.nextEID.Add(1)
	fb := &fallingBlock{eid: eid, uuid: entityUUID(eid), state: state, dim: dim,
		x: float64(x) + 0.5, y: float64(y), z: float64(z) + 0.5, vy: -0.1}
	g.fallings[eid] = fb
	for _, v := range g.chunkViewers(dim, x>>4, z>>4) {
		typ := g.entityTypeID(v, "falling_block")
		st, ex, ez := state, x, z
		v.Send.SendPlay("minecraft:add_entity", func(w *protocol.Writer) {
			packet.SpawnEntity{EntityID: eid, UUID: fb.uuid, Type: typ,
				X: fb.x, Y: fb.y, Z: fb.z, ObjectData: int32(st)}.Write(w)
			_ = ex
			_ = ez
		})
	}
}

// tickFallings 推进下落方块：落地放置。
func (g *Game) tickFallings() {
	for eid, fb := range g.fallings {
		fb.vy -= 0.04
		ny := fb.y + fb.vy
		if g.blockAtDim(fb.dim, floorI(fb.x), floorI(ny-0.02), floorI(fb.z)) {
			// 落地：放置在命中方块上方
			ly := floorI(ny-0.02) + 1
			g.wd(fb.dim).SetBlock(floorI(fb.x), ly, floorI(fb.z), fb.state)
			g.recordBlockChange(fb.dim, floorI(fb.x), ly, floorI(fb.z), fb.state)
			g.flushBlockUpdates()
			g.removeFalling(eid)
			continue
		}
		fb.y = ny
		if fb.y < world.MinY-10 {
			g.removeFalling(eid)
		}
	}
}

func (g *Game) removeFalling(eid int32) {
	fb := g.fallings[eid]
	delete(g.fallings, eid)
	for _, p := range g.players {
		if fb != nil && p.Dim != fb.dim {
			continue
		}
		p.Send.SendPlay("minecraft:remove_entities", func(w *protocol.Writer) {
			packet.WriteRemoveEntities(w, []int32{eid})
		})
	}
}

// ---- 简化水流 ----

// scheduleWaterFlow 挖掉方块后，若相邻有水则安排该位流水。
func (g *Game) scheduleWaterFlow(dim, x, y, z int32) {
	w := g.wd(dim)
	for _, d := range [][3]int32{{1, 0, 0}, {-1, 0, 0}, {0, 0, 1}, {0, 0, -1}, {0, 1, 0}} {
		if s := w.GetBlock(x+d[0], y+d[1], z+d[2]); s >= world.WaterMin && s <= world.WaterMax {
			g.waterQueue = append(g.waterQueue, [4]int32{dim, x, y, z})
			return
		}
	}
}

// tickWater 每 5 刻处理一步水流（水平扩散最多 4 格 + 向下自由流）。
func (g *Game) tickWater() {
	if g.tick%5 != 0 || len(g.waterQueue) == 0 {
		return
	}
	next := g.waterQueue
	g.waterQueue = nil
	for _, pos := range next {
		dim, x, y, z := pos[0], pos[1], pos[2], pos[3]
		w := g.wd(dim)
		if w.GetBlock(x, y, z) != world.StateAir {
			continue
		}
		// 最低水平 level（源=0）
		best := int32(-1)
		for _, d := range [][3]int32{{1, 0, 0}, {-1, 0, 0}, {0, 0, 1}, {0, 0, -1}} {
			s := w.GetBlock(x+d[0], y+d[1], z+d[2])
			if s >= world.WaterMin && s <= world.WaterMax {
				lv := int32(s) - int32(world.WaterMin)
				if best == -1 || lv < best {
					best = lv
				}
			}
		}
		if best == -1 {
			continue
		}
		// 上方有水 → 垂直流（level 同上方）
		if up := w.GetBlock(x, y+1, z); up >= world.WaterMin && up <= world.WaterMax {
			best = int32(up) - int32(world.WaterMin)
		} else if best >= 4 {
			continue // 水平扩散最多 4 格
		} else {
			best++
		}
		ns := uint32(int32(world.WaterMin) + best)
		w.SetBlock(x, y, z, ns)
		g.recordBlockChange(dim, x, y, z, ns)
		// 继续扩散
		g.scheduleWaterFlow(dim, x+1, y, z)
		g.scheduleWaterFlow(dim, x-1, y, z)
		g.scheduleWaterFlow(dim, x, y, z+1)
		g.scheduleWaterFlow(dim, x, y, z-1)
		g.scheduleWaterFlow(dim, x, y-1, z)
	}
	g.flushBlockUpdates()
}

// ---- 树苗 ----

// trackSapling 放置树苗时登记。
func (g *Game) trackSapling(dim, x, y, z int32, state uint32) {
	if state == saplingState {
		g.saplings[[4]int32{dim, x, y, z}] = 0
	}
}

// tickSaplings 树苗随机生长为橡树（复用地形树形逻辑的小型版）。
func (g *Game) tickSaplings() {
	if g.tick%40 != 0 {
		return
	}
	for pos, age := range g.saplings {
		if g.wd(pos[0]).GetBlock(pos[1], pos[2], pos[3]) != saplingState {
			delete(g.saplings, pos)
			continue
		}
		age++
		g.saplings[pos] = age
		if age < 30 || (age < 200 && rand.Intn(4) != 0) { // 30 期后随机，200 期必长
			continue
		}
		delete(g.saplings, pos)
		g.growOak(pos[0], pos[1], pos[2], pos[3])
	}
}

// growOak 在树苗位置长一棵橡树（4-5 格树干 + 树冠）。
func (g *Game) growOak(dim, x, y, z int32) {
	w := g.wd(dim)
	trunk := int32(4 + rand.Intn(2))
	for t := int32(0); t < trunk; t++ {
		w.SetBlock(x, y+t, z, logState)
		g.recordBlockChange(dim, x, y+t, z, logState)
	}
	top := y + trunk - 1
	for dy := int32(-2); dy <= 1; dy++ {
		r := int32(2)
		if dy >= 0 {
			r = 1
		}
		for dx := -r; dx <= r; dx++ {
			for dz := -r; dz <= r; dz++ {
				if dx == 0 && dz == 0 && dy < 1 {
					continue
				}
				if abs32(dx) == 2 && abs32(dz) == 2 {
					continue
				}
				px, py, pz := x+dx, top+dy, z+dz
				if w.GetBlock(px, py, pz) == world.StateAir {
					w.SetBlock(px, py, pz, leavesState)
					g.recordBlockChange(dim, px, py, pz, leavesState)
				}
			}
		}
	}
	g.flushBlockUpdates()
}

// notify 给单个玩家发系统消息。
func (g *Game) notify(p *entity.Player, msg string) {
	p.Send.SendPlay("minecraft:system_chat", func(w *protocol.Writer) {
		packet.WriteSystemChat(w, msg, false)
	})
}
