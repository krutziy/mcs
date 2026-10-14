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

// syncInventory 全量同步玩家物品栏（窗口 0）。
func (g *Game) syncInventory(p *entity.Player) {
	p.ContainerState++
	state := p.ContainerState
	slots := make([]packet.ItemSlot, packet.InvSlots)
	for i, s := range p.Slots {
		slots[i] = packet.ItemSlot{ID: s.ID, Count: s.Count}
	}
	carried := packet.ItemSlot{ID: p.Cursor.ID, Count: p.Cursor.Count}
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

// handleContainerClick 处理物品栏点击（点击窗口 0；其他窗口忽略）。
// 实现常用模式：左/右键拾取放置交换、数字键交换、Q 丢弃、窗外丢弃；
// shift 移动与拖拽暂不实现（同步回合会回滚客户端预测）。
func (g *Game) handleContainerClick(ev *playerContainerClickEvent) {
	p := ev.p
	if p == nil {
		return
	}
	// 熔炉窗口 2：独立路由
	if ev.click.Window == 2 {
		pos, ok := g.openFurnace[p.EntityID]
		if !ok || g.furnaces[pos] == nil {
			return
		}
		fs := g.furnaces[pos]
		slot := int32(ev.click.Slot)
		if ev.click.Mode == packet.ClickModeClick {
			if ref := furnaceSlotRef(fs, p, slot); ref != nil {
				if ev.click.Button == 0 {
					leftClickSlotRef(p, ref)
				} else if ev.click.Button == 1 {
					rightClickSlotRef(p, ref)
				}
			}
		} else if ev.click.Mode == packet.ClickModeThrow {
			if ref := furnaceSlotRef(fs, p, slot); ref != nil && ref.ID != 0 {
				n := int32(1)
				if ev.click.Button != 0 {
					n = ref.Count
				}
				g.dropFromHand(p, entity.Item{ID: ref.ID, Count: n}, 1)
				ref.Count -= n
				if ref.Count <= 0 {
					*ref = entity.Item{}
				}
			}
		}
		g.syncFurnace(p, pos)
		return
	}
	// 工作台窗口 1：独立路由
	if ev.click.Window == 1 {
		cs, ok := g.crafting[p.EntityID]
		if !ok {
			return
		}
		slot := int32(ev.click.Slot)
		if ev.click.Mode == packet.ClickModeClick && slot == 0 {
			g.takeCraftResult3(p, cs)
		} else if ev.click.Mode == packet.ClickModeClick && slot > 0 {
			if ref := g.craftSlotRef(p, cs, slot); ref != nil {
				if ev.click.Button == 0 {
					leftClickSlotRef(p, ref)
				} else if ev.click.Button == 1 {
					rightClickSlotRef(p, ref)
				}
			}
		} else if ev.click.Mode == packet.ClickModeThrow && slot > 0 {
			if ref := g.craftSlotRef(p, cs, slot); ref != nil && ref.ID != 0 {
				n := int32(1)
				if ev.click.Button != 0 {
					n = ref.Count
				}
				g.dropFromHand(p, entity.Item{ID: ref.ID, Count: n}, 1)
				ref.Count -= n
				if ref.Count <= 0 {
					*ref = entity.Item{}
				}
			}
		}
		g.syncCrafting(p, cs)
		return
	}
	if ev.click.Window != 0 {
		return
	}
	c := ev.click
	slot := int32(c.Slot)
	valid := slot >= 0 && slot < packet.InvSlots && slot > 4 // 5..45 可交互

	switch c.Mode {
	case packet.ClickModeClick:
		if slot == 0 && g.takeCraftResult(p) { // 随身合成结果槽
			g.syncInventory(p)
			return
		}
		switch {
		case slot == -999: // 点击窗口外：丢弃光标
			if p.Cursor.ID != 0 {
				g.dropFromHand(p, p.Cursor, 1)
				p.Cursor = entity.Item{}
			}
		case c.Button == 0 && valid:
			leftClickSlot(p, slot)
		case c.Button == 1 && valid:
			rightClickSlot(p, slot)
		}
	case packet.ClickModeSwap: // 数字键 40=副手
		if c.Button >= 0 && c.Button <= 8 && valid {
			hotbar := 36 + int32(c.Button)
			p.Slots[hotbar], p.Slots[slot] = p.Slots[slot], p.Slots[hotbar]
		} else if c.Button == 40 && valid {
			p.Slots[45], p.Slots[slot] = p.Slots[slot], p.Slots[45]
		}
	case packet.ClickModeThrow: // Q 键丢弃（button 0 一个，其余整组）
		if valid && c.Button >= 0 {
			s := &p.Slots[slot]
			if s.ID != 0 {
				n := int32(1)
				if c.Button != 0 {
					n = s.Count
				}
				g.dropFromHand(p, entity.Item{ID: s.ID, Count: n}, 1)
				s.Count -= n
				if s.Count <= 0 {
					*s = entity.Item{}
				}
			}
		}
	}
	g.syncInventory(p)
}

// leftClickSlot 左键（窗口 0 槽位）。
func leftClickSlot(p *entity.Player, slot int32) {
	leftClickSlotRef(p, &p.Slots[slot])
}

// leftClickSlotRef 左键：光标与任意槽位引用 拾取/放置/交换/合并。
func leftClickSlotRef(p *entity.Player, s *entity.Item) {
	cur := p.Cursor
	switch {
	case cur.ID == 0 && s.ID != 0: // 拾取
		p.Cursor, *s = *s, entity.Item{}
	case cur.ID != 0 && s.ID == 0: // 放置
		*s, p.Cursor = cur, entity.Item{}
	case cur.ID == s.ID: // 同类合并
		add := min32(itemMaxStack-s.Count, cur.Count)
		s.Count += add
		cur.Count -= add
		if cur.Count <= 0 {
			cur = entity.Item{}
		}
		p.Cursor = cur
	case cur.ID != 0 && s.ID != 0: // 交换
		p.Cursor, *s = *s, cur
	}
}

// rightClickSlot 右键（窗口 0 槽位）。
func rightClickSlot(p *entity.Player, slot int32) {
	rightClickSlotRef(p, &p.Slots[slot])
}

// rightClickSlotRef 右键：空手取半 / 放一个（任意槽位引用）。
func rightClickSlotRef(p *entity.Player, s *entity.Item) {
	cur := p.Cursor
	if cur.ID == 0 && s.ID != 0 {
		half := (s.Count + 1) / 2
		p.Cursor = entity.Item{ID: s.ID, Count: half}
		s.Count -= half
		if s.Count <= 0 {
			*s = entity.Item{}
		}
	} else if cur.ID != 0 && (s.ID == 0 || s.ID == cur.ID) && s.Count < itemMaxStack {
		if s.ID == 0 {
			*s = entity.Item{ID: cur.ID, Count: 1}
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
		g.spawnItem(p.X, p.Y+1.0, p.Z, canonical)
		return
	}
	x, y, z := p.X+dirX*0.3, p.Y+1.4, p.Z+dirZ*0.3
	g.spawnItemRaw(x, y, z, canonical, dirX*0.3, 0.1, dirZ*0.3)
}

// spawnItemRaw 指定速度生成掉落物（Q 键丢弃用）。
func (g *Game) spawnItemRaw(x, y, z float64, item entity.Item, vx, vy, vz float64) {
	if item.ID == 0 || item.Count <= 0 {
		return
	}
	eid := g.nextEID.Add(1)
	ie := &itemEntity{eid: eid, uuid: entityUUID(eid), item: item,
		x: x, y: y, z: z, vx: vx, vy: vy, vz: vz}
	g.items[eid] = ie
	for _, v := range g.chunkViewers(int32(x)>>4, int32(z)>>4) {
		g.sendItemSpawn(v, ie)
	}
}
