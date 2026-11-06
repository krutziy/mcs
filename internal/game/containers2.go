package game

// 漏斗（容器自动化：上方容器/掉落物 → 漏斗 → 下方容器）、
// 潜影盒（27 格容器，破坏保留内容）、发射器（通电射箭/掉落物品）。

import (
	"encoding/json"
	"os"

	"mcserver/internal/assets"
	"mcserver/internal/entity"
	"mcserver/internal/packet"
	"mcserver/internal/protocol"
	"mcserver/internal/world"
)

// 漏斗/潜影盒/发射器方块状态（777 基准默认态）。
const (
	hopperBlock      uint32 = 13080
	shulkerBoxState  uint32 = 16849
	dispenserState   uint32 = 666
	menuShulker      int32  = 20
	menuHopper       int32  = 16
)

// hopperState5 一个漏斗（5 格 + 冷却）。
type hopperState struct {
	slots [5]entity.Item
	cd    int32 // 传输冷却（4 刻 1 次）
}

// shulkerState 一个潜影盒（27 格）。
type shulkerState struct {
	slots [27]entity.Item
}

// tryOpenHopper 右键漏斗：开窗（menu 16：0-4 漏斗 + 5-31 背包 + 32-40 快捷栏）。
func (g *Game) tryOpenHopper(p *entity.Player, u packet.UseItemOn) bool {
	if g.w(p).GetBlock(u.X, u.Y, u.Z) != hopperBlock {
		return false
	}
	pos := [4]int32{p.Dim, u.X, u.Y, u.Z}
	g.openHopper[p.EntityID] = pos
	if g.hoppers[pos] == nil {
		g.hoppers[pos] = &hopperState{}
	}
	p.Send.SendPlay("minecraft:open_screen", func(w *protocol.Writer) {
		packet.WriteOpenScreen(w, menuHopper, "Hopper")
	})
	g.syncHopper(p, pos)
	return true
}

// hopperSlotRef 窗口 8 槽位。
func hopperSlotRef(hs *hopperState, p *entity.Player, slot int32) *entity.Item {
	switch {
	case slot >= 0 && slot <= 4:
		return &hs.slots[slot]
	case slot >= 5 && slot <= 31:
		return &p.Slots[9+(slot-5)]
	case slot >= 32 && slot <= 40:
		return &p.Slots[36+(slot-32)]
	}
	return nil
}

func (g *Game) syncHopper(p *entity.Player, pos [4]int32) {
	hs := g.hoppers[pos]
	if hs == nil {
		return
	}
	slots := make([]packet.ItemSlot, 41)
	for i := 0; i < 5; i++ {
		slots[i] = itemSlotOf(hs.slots[i])
	}
	for i := int32(5); i <= 31; i++ {
		slots[i] = itemSlotOf(p.Slots[9+(i-5)])
	}
	for i := int32(32); i <= 40; i++ {
		slots[i] = itemSlotOf(p.Slots[36+(i-32)])
	}
	p.ContainerState++
	state := p.ContainerState
	p.Send.SendPlay("minecraft:container_set_content", func(w *protocol.Writer) {
		packet.WriteContainerSetContent(w, 8, state, slots, itemSlotOf(p.Cursor))
	})
}

// tryOpenShulker 右键潜影盒：开窗（menu 20：0-26 盒 + 27-53 背包 + 54-62 快捷栏）。
func (g *Game) tryOpenShulker(p *entity.Player, u packet.UseItemOn) bool {
	name := g.blockNames[g.w(p).GetBlock(u.X, u.Y, u.Z)]
	if name != "shulker_box" && !contains(name, "_shulker_box") {
		return false
	}
	pos := [4]int32{p.Dim, u.X, u.Y, u.Z}
	g.openShulker[p.EntityID] = pos
	if g.shulkers[pos] == nil {
		g.shulkers[pos] = &shulkerState{}
	}
	p.Send.SendPlay("minecraft:open_screen", func(w *protocol.Writer) {
		packet.WriteOpenScreen(w, menuShulker, "Shulker Box")
	})
	g.syncShulkerBox(p, pos)
	return true
}

// shulkerSlotRef 窗口 9 槽位。
func shulkerSlotRef(ss *shulkerState, p *entity.Player, slot int32) *entity.Item {
	switch {
	case slot >= 0 && slot <= 26:
		return &ss.slots[slot]
	case slot >= 27 && slot <= 53:
		return &p.Slots[9+(slot-27)]
	case slot >= 54 && slot <= 62:
		return &p.Slots[36+(slot-54)]
	}
	return nil
}

func (g *Game) syncShulkerBox(p *entity.Player, pos [4]int32) {
	ss := g.shulkers[pos]
	if ss == nil {
		return
	}
	slots := make([]packet.ItemSlot, 63)
	for i := 0; i < 27; i++ {
		slots[i] = itemSlotOf(ss.slots[i])
	}
	for i := int32(27); i <= 53; i++ {
		slots[i] = itemSlotOf(p.Slots[9+(i-27)])
	}
	for i := int32(54); i <= 62; i++ {
		slots[i] = itemSlotOf(p.Slots[36+(i-54)])
	}
	p.ContainerState++
	state := p.ContainerState
	p.Send.SendPlay("minecraft:container_set_content", func(w *protocol.Writer) {
		packet.WriteContainerSetContent(w, 9, state, slots, itemSlotOf(p.Cursor))
	})
}

// dropHopperContents 挖掉漏斗：掉内容。
func (g *Game) dropHopperContents(pos [4]int32) {
	hs := g.hoppers[pos]
	if hs == nil {
		return
	}
	delete(g.hoppers, pos)
	for eid, hpos := range g.openHopper {
		if hpos == pos {
			delete(g.openHopper, eid)
		}
	}
	for _, it := range hs.slots {
		if it.ID != 0 {
			g.spawnItem(pos[0], float64(pos[1])+0.5, float64(pos[2])+0.5, float64(pos[3])+0.5, it)
		}
	}
}

// dropShulkerContents 挖掉潜影盒：掉【带内容的潜影盒物品】+ 清状态。
// 物品带 NBT 太复杂 → 掉盒 + 内容散落（简化：原版整盒保留，此处盒+内容同点掉落）。
func (g *Game) dropShulkerContents(pos [4]int32) {
	ss := g.shulkers[pos]
	if ss == nil {
		return
	}
	delete(g.shulkers, pos)
	for eid, spos := range g.openShulker {
		if spos == pos {
			delete(g.openShulker, eid)
		}
	}
	g.spawnItem(pos[0], float64(pos[1])+0.5, float64(pos[2])+0.5, float64(pos[3])+0.5,
		entity.Item{ID: g.itemID777("shulker_box"), Count: 1})
	for _, it := range ss.slots {
		if it.ID != 0 {
			g.spawnItem(pos[0], float64(pos[1])+0.5, float64(pos[2])+0.5, float64(pos[3])+0.5, it)
		}
	}
}

// tickHoppers 漏斗传输：每 4 刻一档 —— 上方容器取 1 / 下方容器送 1 / 捡上方掉落物。
func (g *Game) tickHoppers() {
	if g.tick%4 != 0 {
		return
	}
	for pos, hs := range g.hoppers {
		dim, x, y, z := pos[0], pos[1], pos[2], pos[3]
		_ = hs.cd
		// 1) 送下方容器（箱/熔炉输入/酿造材料/漏斗/潜影盒）
		below := [4]int32{dim, x, y - 1, z}
		if hs.slots[0].ID != 0 { // 只从首槽送（先进先出）
			if g.hopperPushOut(hs, below) {
				continue // 一刻一档
			}
		}
		// 2) 从上方容器取（箱/漏斗/潜影盒首件）
		above := [4]int32{dim, x, y + 1, z}
		g.hopperPullIn(hs, above)
		// 3) 捡上方掉落物（0.75 格内）
		for _, ie := range g.items {
			if ie.dim != dim || ie.age < 10 {
				continue
			}
			if dist2(ie.x, ie.y, ie.z, float64(x)+0.5, float64(y)+0.75, float64(z)+0.5) < 0.6 {
				if g.hopperAbsorb(hs, ie.item) {
					g.removeItem(ie)
					break
				}
			}
		}
	}
}

// hopperFirstFree 首个空槽（-1 满）。
func hopperFirstFree(hs *hopperState) int {
	for i := range hs.slots {
		if hs.slots[i].ID == 0 {
			return i
		}
	}
	return -1
}

// hopperAbsorb 物品进漏斗（可堆叠叠加）。
func (g *Game) hopperAbsorb(hs *hopperState, it entity.Item) bool {
	for i := range hs.slots {
		s := &hs.slots[i]
		if s.ID == it.ID && s.Count < 64 && s.Damage == 0 && len(s.Ench) == 0 {
			add := 64 - s.Count
			if add > it.Count {
				add = it.Count
			}
			s.Count += add
			it.Count -= add
			if it.Count <= 0 {
				return true
			}
		}
	}
	if i := hopperFirstFree(hs); i >= 0 {
		hs.slots[i] = it
		return true
	}
	return false
}

// hopperPushOut 送 1 件到目标容器。返回是否成功。
func (g *Game) hopperPushOut(hs *hopperState, target [4]int32) bool {
	it := hs.slots[0]
	if it.ID == 0 {
		return false
	}
	// 目标类型
	w := g.wd(target[0])
	tb := w.GetBlock(target[1], target[2], target[3])
	tname := g.blockNames[tb]
	switch {
	case tname == "chest":
		cs := g.chests[target]
		if cs == nil {
			cs = &chestState{}
			g.chests[target] = cs
		}
		n := 27
		if cs.large {
			n = 54
		}
		for i := 0; i < n; i++ {
			s := &cs.slots[i]
			if s.ID == it.ID && s.Count < 64 && s.Damage == 0 && len(s.Ench) == 0 {
				add := 64 - s.Count
				if add > it.Count {
					add = it.Count
				}
				s.Count += add
				it.Count -= add
				if it.Count <= 0 {
					hs.slots[0] = entity.Item{}
					return true
				}
			}
		}
		for i := 0; i < n; i++ {
			if cs.slots[i].ID == 0 {
				cs.slots[i] = it
				hs.slots[0] = entity.Item{}
				return true
			}
		}
	case tb == furnaceLit || tb == furnaceUnlit:
		fs := g.furnaces[target]
		if fs == nil {
			fs = &furnaceState{}
			g.furnaces[target] = fs
		}
		// 送输入槽
		if fs.input.ID == 0 {
			fs.input = it
			hs.slots[0] = entity.Item{}
			return true
		}
		if fs.input.ID == it.ID && fs.input.Count < 64 {
			add := 64 - fs.input.Count
			if add > it.Count {
				add = it.Count
			}
			fs.input.Count += add
			it.Count -= add
			if it.Count <= 0 {
				hs.slots[0] = entity.Item{}
				return true
			}
		}
		// 燃料槽（燃料类）
		if g.fuelTime(g.itemNameOf777(it.ID)) > 0 {
			if fs.fuel.ID == 0 {
				fs.fuel = it
				hs.slots[0] = entity.Item{}
				return true
			}
			if fs.fuel.ID == it.ID && fs.fuel.Count < 64 {
				add := 64 - fs.fuel.Count
				if add > it.Count {
					add = it.Count
				}
				fs.fuel.Count += add
				hs.slots[0] = entity.Item{}
				return true
			}
		}
	case tb == brewingStandState:
		bs := g.brews[target]
		if bs == nil {
			bs = &brewingState{}
			g.brews[target] = bs
		}
		if bs.slots[3].ID == 0 { // 材料槽
			bs.slots[3] = it
			hs.slots[0] = entity.Item{}
			return true
		}
	case tb == hopperBlock:
		th := g.hoppers[target]
		if th == nil {
			th = &hopperState{}
			g.hoppers[target] = th
		}
		if g.hopperAbsorb(th, it) {
			hs.slots[0] = entity.Item{}
			return true
		}
	case tname == "shulker_box" || contains(tname, "_shulker_box"):
		ss := g.shulkers[target]
		if ss == nil {
			ss = &shulkerState{}
			g.shulkers[target] = ss
		}
		for i := 0; i < 27; i++ {
			s := &ss.slots[i]
			if s.ID == it.ID && s.Count < 64 && s.Damage == 0 && len(s.Ench) == 0 {
				add := 64 - s.Count
				if add > it.Count {
					add = it.Count
				}
				s.Count += add
				it.Count -= add
				if it.Count <= 0 {
					hs.slots[0] = entity.Item{}
					return true
				}
			}
		}
		for i := 0; i < 27; i++ {
			if ss.slots[i].ID == 0 {
				ss.slots[i] = it
				hs.slots[0] = entity.Item{}
				return true
			}
		}
	}
	return false
}

// hopperPullIn 从上方容器首件拉入漏斗。
func (g *Game) hopperPullIn(hs *hopperState, above [4]int32) {
	if hopperFirstFree(hs) < 0 {
		return
	}
	w := g.wd(above[0])
	ab := w.GetBlock(above[1], above[2], above[3])
	aname := g.blockNames[ab]
	switch {
	case aname == "chest":
		cs := g.chests[above]
		if cs == nil {
			return
		}
		n := 27
		if cs.large {
			n = 54
		}
		for i := 0; i < n; i++ {
			if cs.slots[i].ID != 0 {
				if g.hopperAbsorb(hs, cs.slots[i]) {
					cs.slots[i] = entity.Item{}
					return
				}
			}
		}
	case ab == hopperBlock:
		ah := g.hoppers[above]
		if ah == nil {
			return
		}
		for i := range ah.slots {
			if ah.slots[i].ID != 0 {
				if g.hopperAbsorb(hs, ah.slots[i]) {
					ah.slots[i] = entity.Item{}
					return
				}
			}
		}
	case aname == "shulker_box" || contains(aname, "_shulker_box"):
		ss := g.shulkers[above]
		if ss == nil {
			return
		}
		for i := 0; i < 27; i++ {
			if ss.slots[i].ID != 0 {
				if g.hopperAbsorb(hs, ss.slots[i]) {
					ss.slots[i] = entity.Item{}
					return
				}
			}
		}
	}
}

// ---- 发射器 ----

// dispenserState8 发射器会话（9 格）。
type dispenserState8 struct {
	slots [9]entity.Item
}

// tryOpenDispenser 右键发射器：开窗（generic_9x3? 原版为 dispenser 专用菜单）。
// 菜单注册表无 dispenser 类型 → 复用 generic 3x3（menu 6 generic_3x3）。
func (g *Game) tryOpenDispenser(p *entity.Player, u packet.UseItemOn) bool {
	if g.blockNames[g.w(p).GetBlock(u.X, u.Y, u.Z)] != "dispenser" {
		return false
	}
	pos := [4]int32{p.Dim, u.X, u.Y, u.Z}
	g.openDispenser[p.EntityID] = pos
	if g.dispensers[pos] == nil {
		g.dispensers[pos] = &dispenserState8{}
	}
	p.Send.SendPlay("minecraft:open_screen", func(w *protocol.Writer) {
		packet.WriteOpenScreen(w, 6, "Dispenser")
	})
	g.syncDispenser(p, pos)
	return true
}

// dispenserSlotRef 窗口 10 槽位（generic_3x3：0-8 + 9-35 背包 + 36-44 快捷栏）。
func dispenserSlotRef(ds *dispenserState8, p *entity.Player, slot int32) *entity.Item {
	switch {
	case slot >= 0 && slot <= 8:
		return &ds.slots[slot]
	case slot >= 9 && slot <= 35:
		return &p.Slots[9+(slot-9)]
	case slot >= 36 && slot <= 44:
		return &p.Slots[slot]
	}
	return nil
}

func (g *Game) syncDispenser(p *entity.Player, pos [4]int32) {
	ds := g.dispensers[pos]
	if ds == nil {
		return
	}
	slots := make([]packet.ItemSlot, 45)
	for i := 0; i < 9; i++ {
		slots[i] = itemSlotOf(ds.slots[i])
	}
	for i := int32(9); i <= 35; i++ {
		slots[i] = itemSlotOf(p.Slots[9+(i-9)])
	}
	for i := int32(36); i <= 44; i++ {
		slots[i] = itemSlotOf(p.Slots[i])
	}
	p.ContainerState++
	state := p.ContainerState
	p.Send.SendPlay("minecraft:container_set_content", func(w *protocol.Writer) {
		packet.WriteContainerSetContent(w, 10, state, slots, itemSlotOf(p.Cursor))
	})
}

// tickDispensers 发射器：通电边沿触发（取首格物品发射：箭射出、其他掉落前方）。
func (g *Game) tickDispensers() {
	if g.tick%2 != 0 {
		return
	}
	for pos, ds := range g.dispensers {
		dim, x, y, z := pos[0], pos[1], pos[2], pos[3]
		powered := g.posPowered(dim, x, y, z)
		key := [4]int32{dim, x, y, z}
		if powered && !g.dispenserFired[key] {
			g.dispenserFired[key] = true
			g.dispenseItem(dim, x, y, z, ds)
		} else if !powered {
			delete(g.dispenserFired, key)
		}
	}
}

// dispenseItem 发射首格：箭 → 射出（向下方向简化朝 facing=north 的 -z）；其余掉落。
func (g *Game) dispenseItem(dim, x, y, z int32, ds *dispenserState8) {
	for i := range ds.slots {
		it := ds.slots[i]
		if it.ID == 0 {
			continue
		}
		name := g.itemNameOf777(it.ID)
		if name == "arrow" {
			// 射箭（-z 方向初速）
			eid := g.nextEID.Add(1)
			ar := &arrow{eid: eid, owner: -1, dim: dim,
				x: float64(x) + 0.5, y: float64(y) + 0.5, z: float64(z) - 0.6,
				vx: 0, vy: 0.02, vz: -1.2, dmg: 3}
			ar.uuid = entityUUID(eid)
			g.arrows[eid] = ar
			for _, v := range g.chunkViewers(dim, x>>4, z>>4) {
				typ := g.entityTypeID(v, "arrow")
				eid2, xx, yy, zz := eid, ar.x, ar.y, ar.z
				v.Send.SendPlay("minecraft:add_entity", func(w *protocol.Writer) {
					packet.SpawnEntity{EntityID: eid2, UUID: ar.uuid, Type: typ,
						X: xx, Y: yy, Z: zz, VZ: -9600}.Write(w)
				})
			}
			g.blockSound(dim, float64(x), float64(y), float64(z), "entity.arrow.shoot")
		} else {
			// 掉落前方
			g.spawnItem(dim, float64(x)+0.5, float64(y)+0.4, float64(z)-0.8, it)
		}
		it.Count--
		if it.Count <= 0 {
			ds.slots[i] = entity.Item{}
		} else {
			ds.slots[i] = it
		}
		return // 一次一件
	}
}

// 持久化（漏斗/潜影盒/发射器随存档）。
type hopperJSON struct {
	Pos   [4]int32     `json:"pos"`
	Slots []entity.Item `json:"slots"`
}

func (g *Game) saveHoppers() {
	if g.Cfg.SaveDir == "" {
		return
	}
	var list []hopperJSON
	for pos, hs := range g.hoppers {
		c := hopperJSON{Pos: pos}
		for _, s := range hs.slots {
			c.Slots = append(c.Slots, s)
		}
		list = append(list, c)
	}
	b, _ := json.Marshal(list)
	_ = os.WriteFile(g.Cfg.SaveDir+"/hoppers.json", b, 0o644)
}

func (g *Game) loadHoppers() {
	b, err := os.ReadFile(g.Cfg.SaveDir + "/hoppers.json")
	if err != nil {
		return
	}
	var list []hopperJSON
	if json.Unmarshal(b, &list) != nil {
		return
	}
	for _, e := range list {
		hs := &hopperState{}
		for i, s := range e.Slots {
			if i < 5 {
				hs.slots[i] = s
			}
		}
		g.hoppers[e.Pos] = hs
	}
}

type shulkerJSON struct {
	Pos   [4]int32     `json:"pos"`
	Slots []entity.Item `json:"slots"`
}

func (g *Game) saveShulkers() {
	if g.Cfg.SaveDir == "" {
		return
	}
	var list []shulkerJSON
	for pos, ss := range g.shulkers {
		c := shulkerJSON{Pos: pos}
		for _, s := range ss.slots {
			c.Slots = append(c.Slots, s)
		}
		list = append(list, c)
	}
	b, _ := json.Marshal(list)
	_ = os.WriteFile(g.Cfg.SaveDir+"/shulkers.json", b, 0o644)
}

func (g *Game) loadShulkers() {
	b, err := os.ReadFile(g.Cfg.SaveDir + "/shulkers.json")
	if err != nil {
		return
	}
	var list []shulkerJSON
	if json.Unmarshal(b, &list) != nil {
		return
	}
	for _, e := range list {
		ss := &shulkerState{}
		for i, s := range e.Slots {
			if i < 27 {
				ss.slots[i] = s
			}
		}
		g.shulkers[e.Pos] = ss
	}
}

var _ = assets.ItemIDs
var _ = world.StateAir
