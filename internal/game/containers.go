package game

// 容器高级点击：shift 快速移动（窗口 0 与全部容器窗）、双击收集、拖拽分发。
// 仍走"服务端权威 + 全量同步"模型：处理完各窗口的 sync* 即纠正客户端预测。

import (
	"mcserver/internal/entity"
	"mcserver/internal/packet"
)

// dragState 一个进行中的拖拽（模式 5：按钮 0/1/2 左键均分、4/5/6 右键每格 1）。
type dragState struct {
	window int32
	button int32 // 0 = 左拖，4 = 右拖
	slots  []int32
}

// hotbarRefs 快捷栏槽引用（36-44）。
func hotbarRefs(p *entity.Player) []*entity.Item {
	out := make([]*entity.Item, 0, 9)
	for i := int32(36); i <= 44; i++ {
		out = append(out, &p.Slots[i])
	}
	return out
}

// mainRefs 背包主区槽引用（9-35）。
func mainRefs(p *entity.Player) []*entity.Item {
	out := make([]*entity.Item, 0, 27)
	for i := int32(9); i <= 35; i++ {
		out = append(out, &p.Slots[i])
	}
	return out
}

// invRefs 玩家物品栏引用（快捷栏优先，容器转入用）。
func invRefs(p *entity.Player) []*entity.Item {
	return append(hotbarRefs(p), mainRefs(p)...)
}

// moveStack 把 src 槽的整组物品移入 dsts（先同类合并，后空位）。
// 带附魔/耐久的物品只进空位（不可堆叠）。返回移动数量。
func (g *Game) moveStack(p *entity.Player, src *entity.Item, dsts []*entity.Item) int32 {
	if src.ID == 0 || src.Count <= 0 {
		return 0
	}
	limit := g.stackOf(p, src.ID)
	stackable := len(src.Ench) == 0 && src.Damage == 0
	moved := int32(0)
	if stackable {
		for _, d := range dsts {
			if src.Count <= 0 {
				break
			}
			if d == src || (d.ID == src.ID && d.Count > 0 && d.Count < limit && len(d.Ench) == 0) {
				add := min32(limit-d.Count, src.Count)
				if add > 0 {
					d.Count += add
					src.Count -= add
					moved += add
				}
			}
		}
	}
	for _, d := range dsts {
		if src.Count <= 0 {
			break
		}
		if d == src || d.ID != 0 {
			continue
		}
		if !stackable {
			*d = entity.Item{ID: src.ID, Count: 1, Damage: src.Damage, Ench: src.Ench}
			src.Count--
			moved++
			continue
		}
		*d = entity.Item{ID: src.ID, Count: src.Count, Damage: 0}
		moved += src.Count
		src.Count = 0
	}
	if src.Count <= 0 {
		*src = entity.Item{}
	}
	return moved
}

// collectToCursor 双击收集：把 refs 中与光标同 ID 的物品收集到光标至上限。
func (g *Game) collectToCursor(p *entity.Player, refs []*entity.Item) {
	if p.Cursor.ID == 0 {
		return
	}
	limit := g.stackOf(p, p.Cursor.ID)
	for _, s := range refs {
		if p.Cursor.Count >= limit {
			return
		}
		if s.ID != p.Cursor.ID || len(s.Ench) > 0 || s.Damage != 0 {
			continue
		}
		add := min32(limit-p.Cursor.Count, s.Count)
		if add <= 0 {
			continue
		}
		p.Cursor.Count += add
		s.Count -= add
		if s.Count <= 0 {
			*s = entity.Item{}
		}
	}
}

// applyDrag 结束拖拽：左键均分（轮转每格 1 个）、右键每格 1 个。
func (g *Game) applyDrag(p *entity.Player, ref func(int32) *entity.Item) {
	d := g.drags[p.EntityID]
	if d == nil || p.Cursor.ID == 0 || len(d.slots) == 0 {
		delete(g.drags, p.EntityID)
		return
	}
	delete(g.drags, p.EntityID)
	limit := g.stackOf(p, p.Cursor.ID)
	// 只投放空位或同类可叠加槽
	var targets []*entity.Item
	seen := map[*entity.Item]bool{}
	for _, slot := range d.slots {
		s := ref(slot)
		if s == nil || seen[s] {
			continue
		}
		seen[s] = true
		if s.ID == 0 || (s.ID == p.Cursor.ID && len(s.Ench) == 0 && s.Damage == 0 && s.Count < limit) {
			targets = append(targets, s)
		}
	}
	if d.button == 4 { // 右拖：每格 1 个
		for _, s := range targets {
			if p.Cursor.Count <= 0 {
				break
			}
			if s.ID == 0 {
				*s = entity.Item{ID: p.Cursor.ID, Count: 1, Damage: p.Cursor.Damage, Ench: p.Cursor.Ench}
			} else {
				s.Count++
			}
			p.Cursor.Count--
		}
	} else { // 左拖：轮转均分
		for p.Cursor.Count > 0 {
			placed := false
			for _, s := range targets {
				if p.Cursor.Count <= 0 {
					break
				}
				if s.Count >= limit {
					continue
				}
				if s.ID == 0 {
					*s = entity.Item{ID: p.Cursor.ID, Count: 1, Damage: p.Cursor.Damage, Ench: p.Cursor.Ench}
				} else {
					s.Count++
				}
				p.Cursor.Count--
				placed = true
			}
			if !placed {
				break
			}
		}
	}
	if p.Cursor.Count <= 0 {
		p.Cursor = entity.Item{}
	}
}

// takeCraftResultToInv 连续合成并直接入包（shift 点结果槽；随身 2x2 与工作台共用）。
// maxTimes 防御配方自循环。返回是否合成过。
func (g *Game) takeCraftResultToInv(p *entity.Player, take func() (entity.Item, bool)) bool {
	any := false
	for i := 0; i < 64; i++ {
		it, ok := take()
		if !ok || it.ID == 0 {
			break
		}
		if n := g.giveItem(p, it); n < it.Count {
			break
		}
		any = true
	}
	return any
}

// craftResultOnce 取一次随身合成结果到临时物品（不落光标）。
func (g *Game) craftResultOnce(p *entity.Player) (entity.Item, bool) {
	saved := p.Cursor
	p.Cursor = entity.Item{}
	ok := g.takeCraftResult(p)
	it := p.Cursor
	p.Cursor = saved
	return it, ok
}

// craftResult3Once 取一次工作台合成结果到临时物品（不落光标）。
func (g *Game) craftResult3Once(p *entity.Player, cs *craftState) (entity.Item, bool) {
	saved := p.Cursor
	p.Cursor = entity.Item{}
	ok := g.takeCraftResult3(p, cs)
	it := p.Cursor
	p.Cursor = saved
	return it, ok
}

// anvilResultToInv shift 取铁砧输出：结果直接入包。
func (g *Game) anvilResultToInv(p *entity.Player) bool {
	as, ok := g.anvils[p.EntityID]
	if !ok || as.in0.ID == 0 {
		return false
	}
	saved := p.Cursor
	p.Cursor = entity.Item{}
	took := g.takeAnvilResult(p)
	it := p.Cursor
	p.Cursor = saved
	if !took {
		return false
	}
	if n := g.giveItem(p, it); n < it.Count {
		// 包满：结果放回输入（退料语义）
		if as.in0.ID == 0 {
			as.in0 = it
		} else {
			g.spawnItem(p.X, p.Y+1, p.Z, entity.Item{ID: g.canonItem(p, it.ID), Count: it.Count - n})
		}
	}
	return true
}

// handleDragStep 处理拖拽三段（开始/加槽/结束）。返回 true 表示已处理。
func (g *Game) handleDragStep(p *entity.Player, window, button int32, slot int32, ref func(int32) *entity.Item) {
	switch button {
	case 0, 4, 8: // 开始
		if p.Cursor.ID != 0 {
			g.drags[p.EntityID] = &dragState{window: window, button: button}
		}
	case 1, 5, 9: // 加槽
		if d := g.drags[p.EntityID]; d != nil && d.window == window {
			d.slots = append(d.slots, slot)
		}
	case 2, 6, 10: // 结束
		if d := g.drags[p.EntityID]; d != nil && d.window == window {
			d.button = d.button &^ 1 // 归一化为 0（左）/4（右）
			g.applyDrag(p, ref)
		}
	}
}

// ---- 各窗口的快速移动 ----

// quickMoveWindow0 玩家物品栏（窗口 0）shift：背包↔快捷栏/盔甲/合成格。
func (g *Game) quickMoveWindow0(p *entity.Player, slot int32) {
	switch {
	case slot == 0: // 随身合成结果：连续合成入包
		g.takeCraftResultToInv(p, func() (entity.Item, bool) { return g.craftResultOnce(p) })
	case slot >= 1 && slot <= 4: // 2x2 网格退回背包
		g.moveStack(p, &p.Slots[slot], invRefs(p))
	case slot >= 5 && slot <= 8: // 盔甲退回背包
		g.moveStack(p, &p.Slots[slot], invRefs(p))
	case slot >= 9 && slot <= 35: // 主区 → 快捷栏
		g.moveStack(p, &p.Slots[slot], hotbarRefs(p))
	case slot >= 36 && slot <= 44: // 快捷栏 → 主区
		g.moveStack(p, &p.Slots[slot], mainRefs(p))
	case slot == 45: // 副手 → 物品栏
		g.moveStack(p, &p.Slots[45], invRefs(p))
	}
}

// quickMoveCraft 工作台窗口（窗口 1）shift。
func (g *Game) quickMoveCraft(p *entity.Player, cs *craftState, slot int32) {
	switch {
	case slot == 0:
		g.takeCraftResultToInv(p, func() (entity.Item, bool) { return g.craftResult3Once(p, cs) })
	case slot >= 1 && slot <= 9: // 网格退回背包
		if ref := g.craftSlotRef(p, cs, slot); ref != nil {
			g.moveStack(p, ref, invRefs(p))
		}
	case slot >= 10 && slot <= 35: // 主区 → 快捷栏
		if ref := g.craftSlotRef(p, cs, slot); ref != nil {
			g.moveStack(p, ref, hotbarRefs(p))
		}
	case slot >= 36 && slot <= 44: // 快捷栏 → 主区
		if ref := g.craftSlotRef(p, cs, slot); ref != nil {
			g.moveStack(p, ref, mainRefs(p))
		}
	}
}

// quickMoveFurnace 熔炉窗口（窗口 2）shift：可冶→输入槽、燃料→燃料槽、产物入包。
func (g *Game) quickMoveFurnace(p *entity.Player, fs *furnaceState, slot int32) {
	switch {
	case slot >= 0 && slot <= 2: // 容器 → 物品栏
		if ref := furnaceSlotRef(fs, p, slot); ref != nil {
			g.moveStack(p, ref, invRefs(p))
		}
	case slot >= 10 && slot <= 44: // 物品栏 → 熔炉
		ref := furnaceSlotRef(fs, p, slot)
		if ref == nil || ref.ID == 0 {
			return
		}
		name := g.itemNameOf(p, ref.ID)
		if g.smelting[name] != "" {
			if g.moveStack(p, ref, []*entity.Item{&fs.input}) > 0 {
				return
			}
		}
		if g.fuelTime(name) > 0 {
			g.moveStack(p, ref, []*entity.Item{&fs.fuel})
			return
		}
		g.moveStack(p, ref, mainRefs(p)) // 普通：背包↔快捷栏
	}
}

// quickMoveChest 箱子窗口（窗口 3）shift：箱 ↔ 物品栏。
func (g *Game) quickMoveChest(p *entity.Player, cs *chestState, slot int32) {
	if slot >= 0 && slot <= 26 {
		if ref := chestSlotRef(cs, p, slot); ref != nil {
			g.moveStack(p, ref, invRefs(p))
		}
		return
	}
	if ref := chestSlotRef(cs, p, slot); ref != nil && ref.ID != 0 {
		dsts := make([]*entity.Item, 0, chestSlots)
		for i := 0; i < chestSlots; i++ {
			dsts = append(dsts, &cs.slots[i])
		}
		g.moveStack(p, ref, dsts)
	}
}

// quickMoveBrewing 酿造台窗口（窗口 4）shift：药水/瓶→药水槽、材料→材料槽。
func (g *Game) quickMoveBrewing(p *entity.Player, bs *brewingState, slot int32) {
	if slot >= 0 && slot <= 3 {
		if ref := brewingSlotRef(bs, p, slot); ref != nil {
			g.moveStack(p, ref, invRefs(p))
		}
		return
	}
	ref := brewingSlotRef(bs, p, slot)
	if ref == nil || ref.ID == 0 {
		return
	}
	switch g.itemNameOf(p, ref.ID) {
	case "potion", "splash_potion", "lingering_potion", "glass_bottle", "honey_bottle":
		g.moveStack(p, ref, []*entity.Item{&bs.slots[0], &bs.slots[1], &bs.slots[2]})
	default:
		g.moveStack(p, ref, []*entity.Item{&bs.slots[3]})
	}
}

// quickMoveEnchant 附魔台窗口（窗口 5）shift：物品/青金石退回背包。
func (g *Game) quickMoveEnchant(p *entity.Player, slot int32) {
	switch {
	case slot == 0:
		g.moveStack(p, &p.EnchSlots[0], invRefs(p))
	case slot == 1:
		g.moveStack(p, &p.EnchSlots[1], invRefs(p))
	case slot >= 2 && slot <= 28: // 主区 → 快捷栏
		g.moveStack(p, &p.Slots[9+(slot-2)], hotbarRefs(p))
	case slot >= 29 && slot <= 37: // 快捷栏 → 主区
		g.moveStack(p, &p.Slots[36+(slot-29)], mainRefs(p))
	}
}

// quickMoveAnvil 铁砧窗口（窗口 6）shift：输入退包、结果入包、背包物品上砧。
func (g *Game) quickMoveAnvil(p *entity.Player, as *anvilSession, slot int32) {
	switch {
	case slot == 0 || slot == 1:
		if ref := anvilSlotRef(as, p, slot); ref != nil {
			g.moveStack(p, ref, invRefs(p))
		}
	case slot == 2:
		g.anvilResultToInv(p)
	case slot >= 3 && slot <= 29: // 主区：优先上砧，否则 → 快捷栏
		ref := &p.Slots[9+(slot-3)]
		if as.in0.ID == 0 {
			g.moveStack(p, ref, []*entity.Item{&as.in0})
			return
		}
		if as.in1.ID == 0 {
			g.moveStack(p, ref, []*entity.Item{&as.in1})
			return
		}
		g.moveStack(p, ref, hotbarRefs(p))
	case slot >= 30 && slot <= 38: // 快捷栏：优先上砧，否则 → 主区
		ref := &p.Slots[36+(slot-30)]
		if as.in0.ID == 0 {
			g.moveStack(p, ref, []*entity.Item{&as.in0})
			return
		}
		if as.in1.ID == 0 {
			g.moveStack(p, ref, []*entity.Item{&as.in1})
			return
		}
		g.moveStack(p, ref, mainRefs(p))
	}
}

// doubleClickWindow 双击收集：光标为空先拾取，再从窗口全槽收集同类。
func (g *Game) doubleClickWindow(p *entity.Player, slot int32, ref func(int32) *entity.Item, allSlots []int32) {
	if p.Cursor.ID == 0 {
		if s := ref(slot); s != nil && s.ID != 0 {
			p.Cursor = *s
			*s = entity.Item{}
		}
	}
	refs := make([]*entity.Item, 0, len(allSlots))
	for _, sl := range allSlots {
		if s := ref(sl); s != nil {
			refs = append(refs, s)
		}
	}
	g.collectToCursor(p, refs)
}

// slotRange 生成 [a,b] 槽位列表。
func slotRange(a, b int32) []int32 {
	out := make([]int32, 0, b-a+1)
	for i := a; i <= b; i++ {
		out = append(out, i)
	}
	return out
}

var _ = packet.ClickModeClick
