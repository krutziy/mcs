package game

// 玩家物品栏：全量同步（container_set_content）、创造槽位、容器点击、丢弃。
// 服务端权威：客户端点击只取 槽位/按键/模式 三个字段做逻辑，
// 之后一律回全量同步（状态号 +1），客户端预测差异会被自动纠正。

import (
	"math"

	"mcserver/internal/entity"
	"mcserver/internal/packet"
	"mcserver/internal/protocol"
)

// syncInventory 全量同步玩家物品栏（窗口 0，附魔/耐久随组件）。
func (g *Game) syncInventory(p *entity.Player) {
	p.ContainerState++
	state := p.ContainerState
	slots := make([]packet.ItemSlot, packet.InvSlots)
	for i, s := range p.Slots {
		slots[i] = itemSlotOf(s)
	}
	carried := itemSlotOf(p.Cursor)
	p.Send.SendPlay("minecraft:container_set_content", func(w *protocol.Writer) {
		packet.WriteContainerSetContent(w, 0, state, slots, carried)
	})
}

// handleCreativeSlot 处理创造模式物品栏的槽位更新（0-45 映射见 packet.InvSlots 注释）。
func (g *Game) handleCreativeSlot(ev *playerCreativeSlotEvent) {
	if ev.p == nil {
		return
	}
	slot := ev.cs.Slot
	if slot == -999 { // 丢到窗口外
		if ev.cs.HasItem {
			g.dropFromHand(ev.p, entity.Item{ID: ev.cs.ItemID, Count: ev.cs.Count}, 0)
		}
		return
	}
	if slot < 0 || slot >= packet.InvSlots || slot <= 4 { // 合成区不可设
		return
	}
	if ev.cs.HasItem {
		ev.p.Slots[slot] = entity.Item{ID: ev.cs.ItemID, Count: ev.cs.Count}
	} else {
		ev.p.Slots[slot] = entity.Item{}
	}
}

// handleContainerClick 处理物品栏/容器点击：左/右键拾取放置交换、数字键交换、
// Q 丢弃、窗外丢弃、shift 快速移动、双击收集、拖拽分发（均服务端权威，
// 处理后回全量同步纠正客户端预测）。
func (g *Game) handleContainerClick(ev *playerContainerClickEvent) {
	p := ev.p
	if p == nil {
		return
	}
	window := ev.click.Window
	slot := int32(ev.click.Slot)

	// 各窗口提供：槽位引用、快速移动、结果槽特殊单击、全槽范围、同步
	var ref func(int32) *entity.Item
	var quick func(int32)
	var special func(int32) bool
	var allSlots []int32
	var sync func()

	switch window {
	case 3: // 箱子
		pos, ok := g.openChest[p.EntityID]
		if !ok || g.chests[pos] == nil {
			return
		}
		cs := g.chests[pos]
		ref = func(s int32) *entity.Item { return chestSlotRef(cs, p, s) }
		quick = func(s int32) { g.quickMoveChest(p, cs, s) }
		allSlots = slotRange(0, 62)
		sync = func() { g.syncChest(p, pos) }
	case 4: // 酿造台
		pos, ok := g.openBrew[p.EntityID]
		if !ok || g.brews[pos] == nil {
			return
		}
		bs := g.brews[pos]
		ref = func(s int32) *entity.Item { return brewingSlotRef(bs, p, s) }
		quick = func(s int32) { g.quickMoveBrewing(p, bs, s) }
		special = func(s int32) bool { // 药水槽取走时的药水名记账
			if s < 0 || s > 2 || bs.slots[s].ID == 0 || bs.potion[s] == "" {
				return false
			}
			name := bs.potion[s]
			r := brewingSlotRef(bs, p, s)
			if ev.click.Button == 1 {
				g.rightClickSlotRef(p, r)
			} else {
				g.leftClickSlotRef(p, r)
			}
			if p.Cursor.ID == itemPotion || p.Cursor.ID == itemSplash || p.Cursor.ID == itemLingering {
				p.PotionOf[[2]int32{-1, 0}] = name
			}
			return true
		}
		allSlots = slotRange(0, 39)
		sync = func() { g.syncBrewing(p, pos) }
	case 5: // 附魔台
		if !g.enchOpen[p.EntityID] {
			return
		}
		ref = func(s int32) *entity.Item {
			switch {
			case s == 0:
				return &p.EnchSlots[0]
			case s == 1:
				return &p.EnchSlots[1]
			case s >= 2 && s <= 28:
				return &p.Slots[9+(s-2)]
			case s >= 29 && s <= 37:
				return &p.Slots[36+(s-29)]
			}
			return nil
		}
		quick = func(s int32) { g.quickMoveEnchant(p, s) }
		allSlots = slotRange(0, 37)
		sync = func() { g.syncEnchant(p) }
	case 6: // 铁砧
		as, ok := g.anvils[p.EntityID]
		if !ok {
			return
		}
		ref = func(s int32) *entity.Item { return anvilSlotRef(as, p, s) }
		quick = func(s int32) { g.quickMoveAnvil(p, as, s) }
		special = func(s int32) bool {
			if s != 2 {
				return false
			}
			g.takeAnvilResult(p)
			return true
		}
		allSlots = append(slotRange(0, 1), slotRange(3, 38)...)
		sync = func() { g.syncAnvil(p) }
	case 2: // 熔炉
		pos, ok := g.openFurnace[p.EntityID]
		if !ok || g.furnaces[pos] == nil {
			return
		}
		fs := g.furnaces[pos]
		ref = func(s int32) *entity.Item { return furnaceSlotRef(fs, p, s) }
		quick = func(s int32) { g.quickMoveFurnace(p, fs, s) }
		allSlots = append(slotRange(0, 1), slotRange(10, 44)...) // 输出槽不参与收集/拖拽
		sync = func() { g.syncFurnace(p, pos) }
	case 1: // 工作台
		cs, ok := g.crafting[p.EntityID]
		if !ok {
			return
		}
		ref = func(s int32) *entity.Item { return g.craftSlotRef(p, cs, s) }
		quick = func(s int32) { g.quickMoveCraft(p, cs, s) }
		special = func(s int32) bool {
			if s != 0 {
				return false
			}
			g.takeCraftResult3(p, cs)
			return true
		}
		allSlots = slotRange(1, 44)
		sync = func() { g.syncCrafting(p, cs) }
	case 7: // 村民交易
		vs, ok := g.villagers[p.EntityID]
		if !ok {
			return
		}
		ref = func(s int32) *entity.Item { return villagerSlotRef(vs, p, s) }
		special = func(s int32) bool {
			if s != 2 {
				return false
			}
			g.takeVillagerTrade(p)
			return true
		}
		allSlots = append(slotRange(3, 38), 0, 1)
		sync = func() { g.syncVillager(p) }
	case 0: // 玩家物品栏
		ref = func(s int32) *entity.Item {
			if s >= 0 && s < packet.InvSlots {
				return &p.Slots[s]
			}
			return nil
		}
		quick = func(s int32) { g.quickMoveWindow0(p, s) }
		special = func(s int32) bool {
			if s != 0 {
				return false
			}
			g.takeCraftResult(p)
			return true
		}
		allSlots = slotRange(1, 45)
		sync = func() {
			g.syncInventory(p)
			g.broadcastEquipment(p)
		}
	default:
		return
	}

	switch ev.click.Mode {
	case packet.ClickModeClick:
		if special != nil && special(slot) {
			break
		}
		if r := ref(slot); r != nil {
			if ev.click.Button == 0 {
				if window == 0 {
					g.leftClickSlot(p, slot)
				} else {
					g.leftClickSlotRef(p, r)
				}
			} else if ev.click.Button == 1 {
				if window == 0 {
					g.rightClickSlot(p, slot)
				} else {
					g.rightClickSlotRef(p, r)
				}
			}
		}
	case packet.ClickModeShift:
		if quick != nil {
			quick(slot)
		}
	case packet.ClickModeDouble:
		g.doubleClickWindow(p, slot, ref, allSlots)
	case packet.ClickModeQuickCraft:
		g.handleDragStep(p, window, int32(ev.click.Button), slot, ref)
	case packet.ClickModeSwap: // 数字键交换（仅玩家物品栏）
		if window != 0 {
			break
		}
		if slot >= 5 && slot < packet.InvSlots {
			if ev.click.Button >= 0 && ev.click.Button <= 8 {
				hotbar := 36 + int32(ev.click.Button)
				p.Slots[hotbar], p.Slots[slot] = p.Slots[slot], p.Slots[hotbar]
			} else if ev.click.Button == 40 {
				p.Slots[45], p.Slots[slot] = p.Slots[slot], p.Slots[45]
			}
		}
	case packet.ClickModeThrow: // Q 丢弃（button 0 一个，其余整组；-999 丢光标）
		if slot == -999 {
			if p.Cursor.ID != 0 {
				g.dropFromHand(p, p.Cursor, 1)
				p.Cursor = entity.Item{}
			}
			break
		}
		if r := ref(slot); r != nil && r.ID != 0 {
			n := int32(1)
			if ev.click.Button != 0 {
				n = r.Count
			}
			g.dropFromHand(p, entity.Item{ID: r.ID, Count: n}, 1)
			r.Count -= n
			if r.Count <= 0 {
				*r = entity.Item{}
			}
		}
	}
	sync()
}

// leftClickSlotRef 左键：光标与任意槽位引用 拾取/放置/交换/合并
// （合并受该物品的最大堆叠上限约束）。
func (g *Game) leftClickSlotRef(p *entity.Player, s *entity.Item) {
	cur := p.Cursor
	switch {
	case cur.ID == 0 && s.ID != 0: // 拾取
		p.Cursor, *s = *s, entity.Item{}
	case cur.ID != 0 && s.ID == 0: // 放置
		*s, p.Cursor = cur, entity.Item{}
	case cur.ID == s.ID: // 同类合并
		add := min32(g.stackOf(p, s.ID)-s.Count, cur.Count)
		if add > 0 {
			s.Count += add
			cur.Count -= add
		}
		if cur.Count <= 0 {
			cur = entity.Item{}
		}
		p.Cursor = cur
	case cur.ID != 0 && s.ID != 0: // 交换
		p.Cursor, *s = *s, cur
	}
}

// leftClickSlot 左键（窗口 0 槽位）。
func (g *Game) leftClickSlot(p *entity.Player, slot int32) {
	// 光标药水放置到槽位：迁移药水名记账
	cursorPotion := p.PotionOf[[2]int32{-1, 0}]
	g.leftClickSlotRef(p, &p.Slots[slot])
	if p.Cursor.ID == 0 && p.Slots[slot].ID != 0 && cursorPotion != "" {
		p.PotionOf[[2]int32{slot, 0}] = cursorPotion
		delete(p.PotionOf, [2]int32{-1, 0})
	}
	// 从槽位拾取药水到光标
	if p.Cursor.ID != 0 && p.Slots[slot].ID == 0 {
		if n := p.PotionOf[[2]int32{slot, 0}]; n != "" {
			p.PotionOf[[2]int32{-1, 0}] = n
			delete(p.PotionOf, [2]int32{slot, 0})
		}
	}
}

// rightClickSlot 右键（窗口 0 槽位）。
func (g *Game) rightClickSlot(p *entity.Player, slot int32) {
	g.rightClickSlotRef(p, &p.Slots[slot])
}

// rightClickSlotRef 右键：空手取半 / 放一个（任意槽位引用）。
func (g *Game) rightClickSlotRef(p *entity.Player, s *entity.Item) {
	cur := p.Cursor
	if cur.ID == 0 && s.ID != 0 {
		half := (s.Count + 1) / 2
		p.Cursor = entity.Item{ID: s.ID, Count: half, Damage: s.Damage, Ench: s.Ench}
		s.Count -= half
		if s.Count <= 0 {
			*s = entity.Item{}
		}
	} else if cur.ID != 0 && (s.ID == 0 || s.ID == cur.ID) && s.Count < g.stackOf(p, cur.ID) {
		if s.ID == 0 {
			*s = entity.Item{ID: cur.ID, Count: 1, Damage: cur.Damage, Ench: cur.Ench}
		} else {
			s.Count++
		}
		cur.Count--
		if cur.Count <= 0 {
			cur = entity.Item{}
		}
		p.Cursor = cur
	}
}

// dropFromHand 从玩家位置按朝向丢弃物品并生成掉落物。
// spread: 0 = 创造窗口外丢（原地），1 = Q 键（按视线抛出）。
// item 为玩家协议空间的 ID，掉落物统一转 777 基准存储。
func (g *Game) dropFromHand(p *entity.Player, item entity.Item, spread int32) {
	if item.ID == 0 || item.Count <= 0 {
		return
	}
	canonical := entity.Item{ID: g.canonItem(p, item.ID), Count: item.Count}
	yaw := float64(p.Yaw) * math.Pi / 180
	dirX, dirZ := -math.Sin(yaw), math.Cos(yaw)
	if spread == 0 {
		g.spawnItem(p.Dim, p.X, p.Y+1.0, p.Z, canonical)
		return
	}
	x, y, z := p.X+dirX*0.3, p.Y+1.4, p.Z+dirZ*0.3
	g.spawnItemRaw(p.Dim, x, y, z, canonical, dirX*0.3, 0.1, dirZ*0.3)
}

// spawnItemRaw 指定维度与速度生成掉落物（Q 键丢弃用）。
func (g *Game) spawnItemRaw(dim int32, x, y, z float64, item entity.Item, vx, vy, vz float64) {
	if item.ID == 0 || item.Count <= 0 {
		return
	}
	eid := g.nextEID.Add(1)
	ie := &itemEntity{eid: eid, uuid: entityUUID(eid), item: item, dim: dim,
		x: x, y: y, z: z, vx: vx, vy: vy, vz: vz}
	g.items[eid] = ie
	for _, v := range g.chunkViewers(dim, int32(x)>>4, int32(z)>>4) {
		g.sendItemSpawn(v, ie)
	}
}
