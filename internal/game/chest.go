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
	slots [chestSlots]entity.Item
}

// tryOpenChest 右键箱子方块打开窗口；返回 true 表示已拦截。
func (g *Game) tryOpenChest(p *entity.Player, u packet.UseItemOn) bool {
	if g.world.GetBlock(u.X, u.Y, u.Z) != chestBlockState {
		return false
	}
	pos := [3]int32{u.X, u.Y, u.Z}
	g.openChest[p.EntityID] = pos
	if g.chests[pos] == nil {
		g.chests[pos] = &chestState{}
	}
	p.Send.SendPlay("minecraft:open_screen", func(w *protocol.Writer) {
		packet.WriteOpenScreen(w, menuGeneric9x3, "Chest")
	})
	g.syncChest(p, pos)
	return true
}

// closeChest 关闭箱子窗口（内容留在箱内）。
func (g *Game) closeChest(p *entity.Player) {
	delete(g.openChest, p.EntityID)
	p.Send.SendPlay("minecraft:container_close", func(w *protocol.Writer) {
		packet.WriteContainerClose(w, 3)
	})
	g.syncInventory(p)
}

// chestSlotRef 窗口 3 槽位 → 物品引用（0-26 箱格、27-53 背包、54-62 快捷栏）。
func chestSlotRef(cs *chestState, p *entity.Player, slot int32) *entity.Item {
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

// syncChest 同步箱子窗口内容。
func (g *Game) syncChest(p *entity.Player, pos [3]int32) {
	cs := g.chests[pos]
	if cs == nil {
		return
	}
	slots := make([]packet.ItemSlot, 63)
	for i := 0; i < chestSlots; i++ {
		slots[i] = packet.ItemSlot{ID: cs.slots[i].ID, Count: cs.slots[i].Count}
	}
	for i := int32(27); i <= 53; i++ {
		s := p.Slots[9+(i-27)]
		slots[i] = packet.ItemSlot{ID: s.ID, Count: s.Count}
	}
	for i := int32(54); i <= 62; i++ {
		s := p.Slots[36+(i-54)]
		slots[i] = packet.ItemSlot{ID: s.ID, Count: s.Count}
	}
	p.ContainerState++
	state := p.ContainerState
	carried := packet.ItemSlot{ID: p.Cursor.ID, Count: p.Cursor.Count}
	p.Send.SendPlay("minecraft:container_set_content", func(w *protocol.Writer) {
		packet.WriteContainerSetContent(w, 3, state, slots, carried)
	})
}

// dropChestContents 挖掉箱子时掉落内容物。
func (g *Game) dropChestContents(pos [3]int32) {
	cs := g.chests[pos]
	if cs == nil {
		return
	}
	delete(g.chests, pos)
	for eid, cpos := range g.openChest {
		if cpos == pos {
			delete(g.openChest, eid)
		}
	}
	for _, it := range cs.slots {
		if it.ID != 0 {
			g.spawnItem(float64(pos[0])+0.5, float64(pos[1])+0.5, float64(pos[2])+0.5, it)
		}
	}
}

type chestJSON struct {
	Pos   [3]int32    `json:"pos"`
	Slots [][2]int32 `json:"slots"`
}

func (g *Game) saveChests() {
	var list []chestJSON
	for pos, cs := range g.chests {
		c := chestJSON{Pos: pos}
		for _, s := range cs.slots {
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
		cs := &chestState{}
		for i, s := range e.Slots {
			if i < chestSlots {
				cs.slots[i] = entity.Item{ID: s[0], Count: s[1]}
			}
		}
		g.chests[e.Pos] = cs
	}
}
