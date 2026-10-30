package game

// 箱子：右键打开（menu generic_9x3=2），27 格共享容器（窗口 3），
// 状态持久化；挖掉时内容物掉落。

import (
	"encoding/json"
	"os"
	"path/filepath"

	"mcserver/internal/entity"
	"mcserver/internal/packet"
	"mcserver/internal/protocol"
)

// chestState27 是箱子容量。
const chestSlots = 27

type chestState struct {
	slots [54]entity.Item // 双箱 54；单箱用前 27
	large bool
	pair  [4]int32 // 双箱另一半（维度+位置）
}

// tryOpenChest 右键箱子方块打开窗口；返回 true 表示已拦截。
func (g *Game) tryOpenChest(p *entity.Player, u packet.UseItemOn) bool {
	if g.w(p).GetBlock(u.X, u.Y, u.Z) != chestBlockState {
		return false
	}
	pos := [4]int32{p.Dim, u.X, u.Y, u.Z}
	g.openChest[p.EntityID] = pos
	if g.chests[pos] == nil {
		g.chests[pos] = &chestState{}
	}
	g.linkLargeChest(pos)
	cs := g.chests[pos]
	g.blockSound(p.Dim, float64(u.X), float64(u.Y), float64(u.Z), "block.chest.open")
	menu, title := menuGeneric9x3, "Chest"
	if cs.large {
		menu, title = 5, "Large Chest" // menu 注册表 generic_9x6
	}
	p.Send.SendPlay("minecraft:open_screen", func(w *protocol.Writer) {
		packet.WriteOpenScreen(w, menu, title)
	})
	g.syncChest(p, pos)
	return true
}

// closeChest 关闭箱子窗口（内容留在箱内）。
func (g *Game) closeChest(p *entity.Player) {
	if pos, ok := g.openChest[p.EntityID]; ok {
		g.blockSound(p.Dim, float64(pos[1]), float64(pos[2]), float64(pos[3]), "block.chest.close")
	}
	delete(g.openChest, p.EntityID)
	p.Send.SendPlay("minecraft:container_close", func(w *protocol.Writer) {
		packet.WriteContainerClose(w, 3)
	})
	g.syncInventory(p)
}

// chestSlotRef 窗口 3 槽位 → 物品引用（单箱 0-26 / 双箱 0-53，27-53 背包……
// 双箱时背包/快捷栏偏移为 54-80 / 81-89，按 large 区分）。
func chestSlotRef(cs *chestState, p *entity.Player, slot int32) *entity.Item {
	if cs.large {
		switch {
		case slot >= 0 && slot < 54:
			return &cs.slots[slot]
		case slot >= 54 && slot <= 80:
			return &p.Slots[9+(slot-54)]
		case slot >= 81 && slot <= 89:
			return &p.Slots[36+(slot-81)]
		}
		return nil
	}
	switch {
	case slot >= 0 && slot < chestSlots:
		return &cs.slots[slot]
	case slot >= 27 && slot <= 53:
		return &p.Slots[9+(slot-27)]
	case slot >= 54 && slot <= 62:
		return &p.Slots[36+(slot-54)]
	}
	return nil
}

// linkLargeChest 相邻箱子合并为双箱（共享状态指针，两个键指向同一实例）。
func (g *Game) linkLargeChest(pos [4]int32) {
	cs := g.chests[pos]
	if cs == nil || cs.large {
		return
	}
	for _, d := range [][2]int32{{1, 0}, {-1, 0}, {0, 1}, {0, -1}} {
		npos := [4]int32{pos[0], pos[1] + d[0], pos[2], pos[3] + d[1]}
		if g.wd(npos[0]).GetBlock(npos[1], npos[2], npos[3]) != chestBlockState {
			continue
		}
		if ncs := g.chests[npos]; ncs != nil && ncs != cs {
			if ncs.large {
				// 已是双箱：并入（三箱不受支持时以既有双箱为准）
				if ncs.pair != pos && !g.hasChestKey(ncs, pos) {
					g.chests[pos] = ncs
					ncs.pair = npos
				}
				return
			}
			// 合并：保留两份内容
			ncs.large = true
			ncs.pair = pos
			copy(ncs.slots[27:54], cs.slots[0:27])
			g.chests[pos] = ncs
			return
		}
	}
}

func (g *Game) hasChestKey(cs *chestState, pos [4]int32) bool {
	return g.chests[pos] == cs
}

// syncChest 同步箱子窗口内容。
func (g *Game) syncChest(p *entity.Player, pos [4]int32) {
	cs := g.chests[pos]
	if cs == nil {
		return
	}
	var slots []packet.ItemSlot
	if cs.large {
		slots = make([]packet.ItemSlot, 90) // 0-53 双箱 + 54-80 背包 + 81-89 快捷栏
		for i := 0; i < 54; i++ {
			slots[i] = itemSlotOf(cs.slots[i])
		}
		for i := int32(54); i <= 80; i++ {
			slots[i] = itemSlotOf(p.Slots[9+(i-54)])
		}
		for i := int32(81); i <= 89; i++ {
			slots[i] = itemSlotOf(p.Slots[36+(i-81)])
		}
	} else {
		slots = make([]packet.ItemSlot, 63)
		for i := 0; i < chestSlots; i++ {
			slots[i] = itemSlotOf(cs.slots[i])
		}
		for i := int32(27); i <= 53; i++ {
			slots[i] = itemSlotOf(p.Slots[9+(i-27)])
		}
		for i := int32(54); i <= 62; i++ {
			slots[i] = itemSlotOf(p.Slots[36+(i-54)])
		}
	}
	p.ContainerState++
	state := p.ContainerState
	carried := packet.ItemSlot{ID: p.Cursor.ID, Count: p.Cursor.Count}
	p.Send.SendPlay("minecraft:container_set_content", func(w *protocol.Writer) {
		packet.WriteContainerSetContent(w, 3, state, slots, carried)
	})
}

// dropChestContents 挖掉箱子时掉落内容物。
func (g *Game) dropChestContents(pos [4]int32) {
	cs := g.chests[pos]
	if cs == nil {
		return
	}
	delete(g.chests, pos)
	if cs.large { // 双箱：另一半方块一并消失
		pair := cs.pair
		if pair == pos {
			pair = [4]int32{}
			for k, v := range g.chests {
				if v == cs && k != pos {
					pair = k
					break
				}
			}
		}
		if pair != [4]int32{} && g.chests[pair] == cs {
			delete(g.chests, pair)
			if g.wd(pair[0]).GetBlock(pair[1], pair[2], pair[3]) == chestBlockState {
				g.applyBlock(pair[0], pair[1], pair[2], pair[3], 0 /*air*/, 0, nil)
			}
		}
	}
	for eid, cpos := range g.openChest {
		if cpos == pos {
			delete(g.openChest, eid)
		}
	}
	for _, it := range cs.slots {
		if it.ID != 0 {
			g.spawnItem(pos[0], float64(pos[1])+0.5, float64(pos[2])+0.5, float64(pos[3])+0.5, it)
		}
	}
}

type chestJSON struct {
	Pos   [4]int32    `json:"pos"`
	Slots [][2]int32 `json:"slots"`
	Large bool       `json:"large,omitempty"`
	Pair  [4]int32   `json:"pair,omitempty"`
}

func (g *Game) saveChests() {
	var list []chestJSON
	saved := map[*chestState]bool{}
	for pos, cs := range g.chests {
		if saved[cs] {
			continue // 双箱只存一份
		}
		saved[cs] = true
		c := chestJSON{Pos: pos, Large: cs.large, Pair: cs.pair}
		n := 27
		if cs.large {
			n = 54
		}
		for i := 0; i < n; i++ {
			s := cs.slots[i]
			c.Slots = append(c.Slots, [2]int32{s.ID, s.Count})
		}
		list = append(list, c)
	}
	b, _ := json.Marshal(list)
	_ = os.WriteFile(filepath.Join(g.Cfg.SaveDir, "chests.json"), b, 0o644)
}

func (g *Game) loadChests() {
	b, err := os.ReadFile(filepath.Join(g.Cfg.SaveDir, "chests.json"))
	if err != nil {
		return
	}
	var list []chestJSON
	if json.Unmarshal(b, &list) != nil {
		return
	}
	for _, e := range list {
		cs := &chestState{large: e.Large, pair: e.Pair}
		for i, s := range e.Slots {
			if i < 54 {
				cs.slots[i] = entity.Item{ID: s[0], Count: s[1]}
			}
		}
		g.chests[e.Pos] = cs
		if e.Large {
			// 另一半键指向同一实例
			pair := e.Pair
			if pair == [4]int32{} {
				for k, v := range g.chests {
					if v == cs && k != e.Pos {
						pair = k
						break
					}
				}
			}
			if pair != [4]int32{} {
				g.chests[pair] = cs
			}
		}
	}
}
