package game

// 3x3 工作台：右键工作台方块 → open_screen（menu 注册表 crafting=12）→
// 玩家进入窗口 1 交互（槽 0 结果、1-9 网格、10-44 映射玩家物品栏），
// container_close 时网格物品退回。结果匹配复用配方表（3x3 完整网格）。

import (
	"mcserver/internal/assets"
	"mcserver/internal/entity"
	"mcserver/internal/packet"
	"mcserver/internal/protocol"
)

// craftState 是一个玩家打开的工作台网格。
type craftState struct {
	grid [9]entity.Item
}

// menuCrafting 是 menu 注册表的 crafting 类型 ID（官方 reports）。
const menuCrafting int32 = 12

// tryOpenCrafting use_item_on 点击工作台方块时打开 3x3 窗口。
// 返回 true 表示已拦截（不再走放置流程）。
func (g *Game) tryOpenCrafting(p *entity.Player, u packet.UseItemOn) bool {
	tableState, ok := g.itemBlocks[777][assets.ItemIDs(777)["crafting_table"]]
	if !ok {
		return false
	}
	if g.world.GetBlock(u.X, u.Y, u.Z) != tableState {
		return false
	}
	if _, exists := g.crafting[p.EntityID]; exists {
		return false
	}
	g.crafting[p.EntityID] = &craftState{}
	p.Send.SendPlay("minecraft:open_screen", func(w *protocol.Writer) {
		packet.WriteOpenScreen(w, menuCrafting, "Crafting")
	})
	return true
}

// closeCrafting 关闭工作台：网格物品退回背包（放不下则掉落）。
func (g *Game) closeCrafting(p *entity.Player) {
	cs, ok := g.crafting[p.EntityID]
	if !ok {
		return
	}
	delete(g.crafting, p.EntityID)
	for _, it := range cs.grid {
		if it.ID == 0 {
			continue
		}
		if n := g.giveItem(p, it); n < it.Count {
			g.spawnItem(p.X, p.Y+1, p.Z, entity.Item{ID: it.ID, Count: it.Count - n})
		}
	}
	p.Send.SendPlay("minecraft:container_close", func(w *protocol.Writer) {
		packet.WriteContainerClose(w, 1)
	})
	g.syncInventory(p)
}

// craftSlotRef 窗口 1 槽位 → 物品存储指针；无效槽返回 nil。
func (g *Game) craftSlotRef(p *entity.Player, cs *craftState, slot int32) *entity.Item {
	switch {
	case slot >= 1 && slot <= 9:
		return &cs.grid[slot-1]
	case slot >= 10 && slot <= 35: // 玩家背包 9-35
		return &p.Slots[9+(slot-10)+0]
	case slot >= 36 && slot <= 44: // 快捷栏 36-44
		return &p.Slots[slot]
	}
	return nil
}

// matchRecipe3 3x3 网格完整匹配（工作台）。
func (g *Game) matchRecipe3(p *entity.Player, cs *craftState) (string, int32) {
	for _, rec := range g.recipes {
		all := true
		any := false
		for r := 0; r < 3 && all; r++ {
			for c := 0; c < 3; c++ {
				spec := rec.grid[r][c]
				id := cs.grid[r*3+c].ID
				if spec == "." {
					if id != 0 {
						all = false
					}
					continue
				}
				if id == 0 || !g.cellMatch(spec, g.itemNameOf(p, id)) {
					all = false
					break
				}
				any = true
			}
		}
		if all && any {
			return rec.result, rec.count
		}
	}
	return "", 0
}

// takeCraftResult3 工作台结果槽。
func (g *Game) takeCraftResult3(p *entity.Player, cs *craftState) bool {
	name, count := g.matchRecipe3(p, cs)
	if name == "" {
		return false
	}
	resultID, ok := assets.ItemIDs(g.playerProto(p))[name]
	if !ok {
		return false
	}
	cur := p.Cursor
	if cur.ID != 0 && (cur.ID != resultID || cur.Count+count > itemMaxStack) {
		return false
	}
	if cur.ID == 0 {
		p.Cursor = entity.Item{ID: resultID, Count: count}
	} else {
		p.Cursor.Count += count
	}
	for i := range cs.grid {
		s := &cs.grid[i]
		if s.ID != 0 {
			s.Count--
			if s.Count <= 0 {
				*s = entity.Item{}
			}
		}
	}
	return true
}

// syncCrafting 同步工作台窗口内容（container_set_content 窗口 1）。
func (g *Game) syncCrafting(p *entity.Player, cs *craftState) {
	slots := make([]packet.ItemSlot, 45) // 0 结果 + 1-9 网格 + 10-44 物品栏
	if name, n := g.matchRecipe3(p, cs); name != "" {
		if id, ok := assets.ItemIDs(g.playerProto(p))[name]; ok {
			slots[0] = packet.ItemSlot{ID: id, Count: n}
		}
	}
	for i := 1; i <= 9; i++ {
		slots[i] = packet.ItemSlot{ID: cs.grid[i-1].ID, Count: cs.grid[i-1].Count}
	}
	for i := int32(10); i <= 35; i++ {
		s := p.Slots[9+(i-10)]
		slots[i] = packet.ItemSlot{ID: s.ID, Count: s.Count}
	}
	for i := int32(36); i <= 44; i++ {
		s := p.Slots[i]
		slots[i] = packet.ItemSlot{ID: s.ID, Count: s.Count}
	}
	p.ContainerState++
	state := p.ContainerState
	carried := packet.ItemSlot{ID: p.Cursor.ID, Count: p.Cursor.Count}
	p.Send.SendPlay("minecraft:container_set_content", func(w *protocol.Writer) {
		packet.WriteContainerSetContent(w, 1, state, slots, carried)
	})
}
