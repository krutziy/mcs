package game

// 熔炉：右键打开（menu 注册表 furnace=14），三槽容器（输入/燃料/产物），
// 冶炼配方来自官方 jar smelting 数据（smelting_777.txt），燃料热值取原版公开数值。
// 燃烧时切换方块 lit 状态并广播；进度经 container_set_data 同步；
// 挖掉熔炉时内容物掉落；状态持久化到存档目录。

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"mcserver/internal/assets"
	"mcserver/internal/entity"
	"mcserver/internal/packet"
	"mcserver/internal/protocol"
)

// menuFurnace 是 menu 注册表的 furnace 容器类型 ID。
const menuFurnace int32 = 14

// 熔炉方块状态（facing=north）：亮/灭。
const (
	furnaceLit   uint32 = 6883
	furnaceUnlit uint32 = 6884
)

const smeltTicks = 200 // 原版冶炼 10 秒

// furnaceState 是一个熔炉方块的状态（位置为键，存于 Game.furnaces）。
type furnaceState struct {
	input, fuel, output entity.Item
	burn, burnMax, cook int32
	xpBank              int32 // 待领取的熔炼经验
}

// 燃料热值（刻）；木板/原木类按物品标签（planks/logs）判定 300。
var fuelTimes = map[string]int32{
	"coal": 1600, "charcoal": 1600, "coal_block": 16000,
	"stick": 100, "bamboo": 50, "blaze_rod": 2400,
	"dried_kelp_block": 4000, "lava_bucket": 20000,
}

func (g *Game) fuelTime(name string) int32 {
	if t, ok := fuelTimes[name]; ok {
		return t
	}
	if g.itemTags["planks"][name] || g.itemTags["logs"][name] {
		return 300
	}
	if strings.Contains(name, "wooden_") && (strings.Contains(name, "sword") ||
		strings.Contains(name, "pickaxe") || strings.Contains(name, "axe") ||
		strings.Contains(name, "shovel") || strings.Contains(name, "hoe")) {
		return 200
	}
	return 0
}

// tryOpenFurnace 右键熔炉方块时打开窗口；返回 true 表示已拦截。
func (g *Game) tryOpenFurnace(p *entity.Player, u packet.UseItemOn) bool {
	s := g.world.GetBlock(u.X, u.Y, u.Z)
	if s != furnaceLit && s != furnaceUnlit {
		return false
	}
	pos := [3]int32{u.X, u.Y, u.Z}
	g.openFurnace[p.EntityID] = pos
	if g.furnaces[pos] == nil {
		g.furnaces[pos] = &furnaceState{}
	}
	p.Send.SendPlay("minecraft:open_screen", func(w *protocol.Writer) {
		packet.WriteOpenScreen(w, menuFurnace, "Furnace")
	})
	g.syncFurnace(p, pos)
	return true
}

// closeFurnace 关闭熔炉窗口（内容留在炉内，原版行为）。
func (g *Game) closeFurnace(p *entity.Player) {
	delete(g.openFurnace, p.EntityID)
	p.Send.SendPlay("minecraft:container_close", func(w *protocol.Writer) {
		packet.WriteContainerClose(w, 2)
	})
	g.syncInventory(p)
}

// furnaceSlotRef 窗口 2 槽位 → 物品引用。
func furnaceSlotRef(fs *furnaceState, p *entity.Player, slot int32) *entity.Item {
	switch {
	case slot == 0:
		return &fs.input
	case slot == 1:
		return &fs.fuel
	case slot == 2:
		return &fs.output
	case slot >= 10 && slot <= 35:
		return &p.Slots[9+(slot-10)]
	case slot >= 36 && slot <= 44:
		return &p.Slots[slot]
	}
	return nil
}

// syncFurnace 同步熔炉窗口内容与进度条。
func (g *Game) syncFurnace(p *entity.Player, pos [3]int32) {
	fs := g.furnaces[pos]
	if fs == nil {
		return
	}
	slots := make([]packet.ItemSlot, 45)
	slots[0] = packet.ItemSlot{ID: fs.input.ID, Count: fs.input.Count}
	slots[1] = packet.ItemSlot{ID: fs.fuel.ID, Count: fs.fuel.Count}
	slots[2] = packet.ItemSlot{ID: fs.output.ID, Count: fs.output.Count}
	for i := int32(10); i <= 35; i++ {
		slots[i] = packet.ItemSlot{ID: p.Slots[9+(i-10)].ID, Count: p.Slots[9+(i-10)].Count}
	}
	for i := int32(36); i <= 44; i++ {
		slots[i] = packet.ItemSlot{ID: p.Slots[i].ID, Count: p.Slots[i].Count}
	}
	p.ContainerState++
	state := p.ContainerState
	carried := packet.ItemSlot{ID: p.Cursor.ID, Count: p.Cursor.Count}
	p.Send.SendPlay("minecraft:container_set_content", func(w *protocol.Writer) {
		packet.WriteContainerSetContent(w, 2, state, slots, carried)
	})
	// 进度：属性 0=火剩余(0-20)，2=冶炼进度(0-200)
	fire := int32(0)
	if fs.burnMax > 0 {
		fire = fs.burn * 20 / fs.burnMax
	}
	p.Send.SendPlay("minecraft:container_set_data", func(w *protocol.Writer) {
		packet.WriteContainerSetData(w, 2, 0, int16(fire))
	})
	p.Send.SendPlay("minecraft:container_set_data", func(w *protocol.Writer) {
		packet.WriteContainerSetData(w, 2, 2, int16(fs.cook))
	})
}

// tickFurnaces 推进全部熔炉状态机（每刻）。
func (g *Game) tickFurnaces() {
	for pos, fs := range g.furnaces {
		canSmelt := false
		outID := int32(0)
		if name := g.itemNameOf777(fs.input.ID); name != "" {
			if res := g.smelting[name]; res != "" {
				outID = assets.ItemIDs(777)[res]
				canSmelt = fs.output.ID == 0 || (fs.output.ID == outID && fs.output.Count < itemMaxStack)
			}
		}
		wasBurning := fs.burn > 0
		if fs.burn > 0 {
			fs.burn--
			if canSmelt {
				fs.cook++
				if fs.cook >= smeltTicks {
					fs.cook = 0
					if fs.output.ID == 0 {
						fs.output = entity.Item{ID: outID, Count: 1}
					} else {
						fs.output.Count++
					}
					fs.input.Count--
					if fs.input.Count <= 0 {
						fs.input = entity.Item{}
					}
					fs.xpBank += smeltXP[g.itemNameOf777(outID)]
				}
			} else if fs.cook > 0 {
				fs.cook -= 2
				if fs.cook < 0 {
					fs.cook = 0
				}
			}
		} else {
			fs.cook = 0
		}
		if fs.burn <= 0 && canSmelt {
			if fname := g.itemNameOf777(fs.fuel.ID); fname != "" {
				if t := g.fuelTime(fname); t > 0 {
					fs.burn, fs.burnMax = t, t
					fs.fuel.Count--
					if fs.fuel.Count <= 0 {
						fs.fuel = entity.Item{}
					}
				}
			}
		}
		burning := fs.burn > 0
		if burning != wasBurning {
			target := furnaceUnlit
			if burning {
				target = furnaceLit
			}
			g.applyBlock(pos[0], pos[1], pos[2], target, 0, nil)
		}
		for eid, fpos := range g.openFurnace {
			if fpos == pos {
				if p := g.players[eid]; p != nil {
					g.syncFurnace(p, pos)
				}
			}
		}
	}
}

// itemNameOf777 把 777 基准物品 ID 转短名（熔炉内部状态用，带缓存）。
func (g *Game) itemNameOf777(id int32) string {
	if g.itemNameCache == nil {
		g.itemNameCache = map[int32]string{}
		for name, iid := range assets.ItemIDs(777) {
			g.itemNameCache[iid] = name
			itemNameToID777[name] = iid
		}
	}
	return g.itemNameCache[id]
}

// dropFurnaceContents 挖掉熔炉时掉落内容物。
func (g *Game) dropFurnaceContents(pos [3]int32) {
	fs := g.furnaces[pos]
	if fs == nil {
		return
	}
	delete(g.furnaces, pos)
	for eid, fpos := range g.openFurnace {
		if fpos == pos {
			delete(g.openFurnace, eid)
		}
	}
	for _, it := range []entity.Item{fs.input, fs.fuel, fs.output} {
		if it.ID != 0 {
			g.spawnItem(float64(pos[0])+0.5, float64(pos[1])+0.5, float64(pos[2])+0.5, it)
		}
	}
}

type furnaceJSON struct {
	Pos     [3]int32 `json:"pos"`
	Input   [2]int32 `json:"in"`
	Fuel    [2]int32 `json:"fuel"`
	Output  [2]int32 `json:"out"`
	Burn    int32    `json:"burn"`
	BurnMax int32    `json:"burnmax"`
	Cook    int32    `json:"cook"`
}

// saveFurnaces 持久化熔炉状态。
func (g *Game) saveFurnaces() {
	var list []furnaceJSON
	for pos, fs := range g.furnaces {
		list = append(list, furnaceJSON{pos,
			[2]int32{fs.input.ID, fs.input.Count}, [2]int32{fs.fuel.ID, fs.fuel.Count},
			[2]int32{fs.output.ID, fs.output.Count}, fs.burn, fs.burnMax, fs.cook})
	}
	b, _ := json.Marshal(list)
	_ = os.WriteFile(filepath.Join(g.Cfg.SaveDir, "furnaces.json"), b, 0o644)
}

// loadFurnaces 读回熔炉状态。
func (g *Game) loadFurnaces() {
	b, err := os.ReadFile(filepath.Join(g.Cfg.SaveDir, "furnaces.json"))
	if err != nil {
		return
	}
	var list []furnaceJSON
	if json.Unmarshal(b, &list) != nil {
		return
	}
	for _, e := range list {
		g.furnaces[e.Pos] = &furnaceState{
			input:   entity.Item{ID: e.Input[0], Count: e.Input[1]},
			fuel:    entity.Item{ID: e.Fuel[0], Count: e.Fuel[1]},
			output:  entity.Item{ID: e.Output[0], Count: e.Output[1]},
			burn:    e.Burn, burnMax: e.BurnMax, cook: e.Cook,
		}
	}
}

var _ = protocol.Position{}
