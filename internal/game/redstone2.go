package game

// 红石扩展：压力板（玩家/生物站立通电）、按钮（右键 20 刻脉冲）、
// 铁门（仅红石开启）、活板门/栅栏门（右键开关）。漏斗/潜影盒/发射器
// 见 containers2.go；马与村民职业见 mobs3.go。

import (
	"mcserver/internal/entity"
	"mcserver/internal/packet"
	"mcserver/internal/world"
)

// 触发器方块状态（777 基准）。
const (
	plateStoneOn  uint32 = 8463
	plateStoneOff uint32 = 8464
	plateOakOn    uint32 = 8529
	plateOakOff   uint32 = 8530
	buttonOff     uint32 = 8566 // stone_button floor north false
)

// 按钮脉冲（位置 → 到期刻）。
// 与 observerPulse 同款模型。

// pressButton 右键按钮：置 powered=true 并登记 20 刻回弹。
func (g *Game) pressButton(p *entity.Player, u packet.UseItemOn) bool {
	name := g.blockNames[g.w(p).GetBlock(u.X, u.Y, u.Z)]
	if name != "stone_button" && name != "oak_button" && name != "spruce_button" &&
		name != "birch_button" && name != "polished_blackstone_button" {
		return false
	}
	// 同名 powered=true 状态：+1（按钮族 true/false 相邻）
	s := g.w(p).GetBlock(u.X, u.Y, u.Z)
	ns := buttonPowered(s)
	if ns == 0 {
		return false
	}
	g.applyBlock(p.Dim, u.X, u.Y, u.Z, ns, u.Sequence, p)
	g.rsDirty = true
	g.buttonPulse = append(g.buttonPulse, [5]int32{p.Dim, u.X, u.Y, u.Z, int32(g.tick) + 20})
	g.blockSound(p.Dim, float64(u.X), float64(u.Y), float64(u.Z), "block.stone_button.click_on")
	return true
}

// buttonPowered 按钮状态 → 按下状态（true/false 相邻对；未知返回 0）。
func buttonPowered(s uint32) uint32 {
	if !isButtonState(s) {
		return 0
	}
	if s%2 == 1 { // 奇数 = powered=true（按族内排序 true 在前）
		return 0
	}
	return s + 1
}

func isButtonState(s uint32) bool {
	name := blockNameOfState(s)
	switch name {
	case "stone_button", "oak_button", "spruce_button", "birch_button",
		"polished_blackstone_button":
		return true
	}
	return false
}

// blockNameOfState 由状态查短名（需 blockNames；Game 方法包装）。
func blockNameOfState(s uint32) string { return blockNameGlobal(s) }

// 全局缓存（game 启动时由 loadInteractTables 后填充）。
var blockNameLookup = map[uint32]string{}

func blockNameGlobal(s uint32) string { return blockNameLookup[s] }

// tickButtons 按钮回弹。
func (g *Game) tickButtons() {
	if len(g.buttonPulse) == 0 {
		return
	}
	var remain [][5]int32
	for _, bp := range g.buttonPulse {
		if int32(g.tick) >= bp[4] {
			w := g.wd(bp[0])
			if isButtonState(w.GetBlock(bp[1], bp[2], bp[3])) {
				// 回弹：true → false（-1）
				s := w.GetBlock(bp[1], bp[2], bp[3])
				if s%2 == 1 {
					g.applyBlock(bp[0], bp[1], bp[2], bp[3], s-1, 0, nil)
				}
			}
			g.rsDirty = true
			continue
		}
		remain = append(remain, bp)
	}
	g.buttonPulse = remain
}

// isPlateState 压力板状态。
func isPlateState(s uint32) bool {
	n := blockNameGlobal(s)
	return n == "stone_pressure_plate" || n == "oak_pressure_plate" ||
		n == "light_weighted_pressure_plate" || n == "heavy_weighted_pressure_plate"
}

// tickPlates 压力板：每 5 刻扫玩家/生物站立 → powered 翻转。
func (g *Game) tickPlates() {
	if g.tick%5 != 0 {
		return
	}
	type platePos struct{ dim, x, y, z int32; on bool }
	pressed := map[[4]int32]bool{}
	for _, p := range g.players {
		if p.Dead {
			continue
		}
		w := g.w(p)
		if s := w.GetBlock(floorI(p.X), floorI(p.Y-0.3), floorI(p.Z)); isPlateState(s) {
			pressed[[4]int32{p.Dim, floorI(p.X), floorI(p.Y) - 1, floorI(p.Z)}] = true
		}
	}
	for _, m := range g.mobs {
		w := g.wd(m.dim)
		if s := w.GetBlock(floorI(m.x), floorI(m.y-0.3), floorI(m.z)); isPlateState(s) {
			pressed[[4]int32{m.dim, floorI(m.x), floorI(m.y) - 1, floorI(m.z)}] = true
		}
	}
	// 已登记压力板：受压状态与 powered 不一致 → 翻转
	for pos := range g.plates {
		s := g.wd(pos[0]).GetBlock(pos[1], pos[2], pos[3])
		if !isPlateState(s) {
			continue
		}
		full := fullStateNames[s]
		on := contains(full, "powered=true")
		want := pressed[pos]
		if want != on {
			var ns uint32
			if want {
				ns = findSibling(s, blockNameGlobal(s), "powered=false", "powered=true")
			} else {
				ns = findSibling(s, blockNameGlobal(s), "powered=true", "powered=false")
			}
			if ns != 0 {
				g.applyBlock(pos[0], pos[1], pos[2], pos[3], ns, 0, nil)
				g.rsDirty = true
			}
		}
	}
	// 新放置未登记的压力板自动入表（扫描一次玩家附近）
	for key := range pressed {
		if _, ok := g.plates[key]; !ok {
			g.plates[key] = true
		}
	}
}

// trackPlate 放置压力板时登记。
func (g *Game) trackPlate(dim, x, y, z int32, state uint32) {
	if isPlateState(state) {
		g.plates[[4]int32{dim, x, y, z}] = true
	}
}

// ---- 铁门 / 活板门 / 栅栏门 ----

// toggleIronDoor 铁门不可徒手开（右键无操作）。
// toggleTrapdoor / toggleFenceGate 右键开关。
func (g *Game) toggleTrapdoorOrGate(p *entity.Player, u packet.UseItemOn) bool {
	name := g.blockNames[g.w(p).GetBlock(u.X, u.Y, u.Z)]
	if name != "iron_door" {
		// 活板门/栅栏门：open 属性翻转（同组状态查找）
		if isTrapdoorName(name) || isFenceGateName(name) {
			return g.flipOpenProp(p, u, name)
		}
		return false
	}
	// 铁门：需红石（邻 posPowered → 开；否则无操作）
	return true
}

func isTrapdoorName(n string) bool {
	return len(n) > 9 && n[len(n)-9:] == "_trapdoor"
}

func isFenceGateName(n string) bool {
	return len(n) > 11 && n[len(n)-11:] == "_fence_gate"
}

// flipOpenProp 查同组 open=true/false 状态并翻转。
func (g *Game) flipOpenProp(p *entity.Player, u packet.UseItemOn, name string) bool {
	s := g.w(p).GetBlock(u.X, u.Y, u.Z)
	ns := openFlipState(s)
	if ns == 0 {
		return false
	}
	g.applyBlock(p.Dim, u.X, u.Y, u.Z, ns, u.Sequence, p)
	g.rsDirty = true
	g.blockSound(p.Dim, float64(u.X), float64(u.Y), float64(u.Z), "block.wooden_trapdoor.open")
	return true
}

// openFlipState 状态 → open 翻转的同组状态（0 = 未知）。
func openFlipState(s uint32) uint32 {
	n := blockNameGlobal(s)
	if n == "" {
		return 0
	}
	// 活板门族：64 状态按 (facing×2 half×2 open×2 waterlogged×4)？
	// 直接用 facingGroups 同款：open=true/false 相邻在属性表末两列。
	// 简化推导：活板门 open=false→true 相差 4（waterlogged 4 档在 open 之后）。
	if isTrapdoorName(n) {
		full := fullStateName(s)
		if contains(full, "open=false") {
			// open 在 waterlogged 前：+2^? —— 用查表更稳
			return findSibling(s, n, "open=false", "open=true")
		}
		return findSibling(s, n, "open=true", "open=false")
	}
	if isFenceGateName(n) {
		full := fullStateName(s)
		if contains(full, "open=false") {
			return findSibling(s, n, "open=false", "open=true")
		}
		return findSibling(s, n, "open=true", "open=false")
	}
	if n == "iron_door" || contains(fullStateName(s), "_door[") || n == "oak_door" {
		full := fullStateName(s)
		if contains(full, "open=false") {
			return findSibling(s, n, "open=false", "open=true")
		}
		return findSibling(s, n, "open=true", "open=false")
	}
	return 0
}

// fullStateName 状态 → 完整名（启动缓存）。
var fullStateNames = map[uint32]string{}

func fullStateName(s uint32) string { return fullStateNames[s] }

// findSibling 同名方块中找属性替换后的状态（线性查全名表，低频可接受）。
func findSibling(s uint32, name, fromProp, toProp string) uint32 {
	full := fullStateNames[s]
	if full == "" {
		return 0
	}
	target := replaceProp(full, fromProp, toProp)
	if target == "" {
		return 0
	}
	for st, f := range fullStateNames {
		if f == target {
			return st
		}
	}
	return 0
}

func replaceProp(full, from, to string) string {
	i := indexOfStr(full, from)
	if i < 0 {
		return ""
	}
	return full[:i] + to + full[i+len(from):]
}

func indexOfStr(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

func contains(s, sub string) bool { return indexOfStr(s, sub) >= 0 }

var _ = world.StateAir
