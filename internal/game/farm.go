package game

// 农业链：锄头右键草/泥土 → 耕地；种子（wheat_crop 8 年龄）种在耕地上、
// 随机生长；成熟收割掉麦子+种子；耕地无作物时踩踏/时间退化回泥土（简化：邻水湿润）。

import (
	"math/rand"

	"mcserver/internal/assets"
	"mcserver/internal/entity"
	"mcserver/internal/packet"
	"mcserver/internal/world"
)

const (
	farmlandState  uint32 = 6875 // moisture=0
	farmlandWet    uint32 = 6882 // moisture=7
	wheatCropBase  uint32 = 6867 // age 0..7 = 6867..6874
)

var wheatAges = [8]uint32{6867, 6868, 6869, 6870, 6871, 6872, 6873, 6874}

// tryTill 锄头右键草/泥土 → 耕地（消耗耐久）。返回 true 拦截放置。
func (g *Game) tryTill(p *entity.Player, u packet.UseItemOn) bool {
	held := p.Held()
	if held.ID == 0 {
		return false
	}
	name := g.itemNameOf(p, held.ID)
	if len(name) < 4 || name[len(name)-4:] != "_hoe" {
		return false
	}
	s := g.world.GetBlock(u.X, u.Y, u.Z)
	if !grassStates[s] && s != world.StateDirt {
		return false
	}
	// 下方要有实体（不能悬空锄）
	if !g.blockAt(u.X, u.Y-1, u.Z) {
		return true
	}
	// 邻水 → 湿耕地
	wet := farmlandState
	for _, d := range [][3]int32{{1, 0, 0}, {-1, 0, 0}, {0, 0, 1}, {0, 0, -1}, {1, -1, 0}, {-1, -1, 0}, {0, -1, 1}, {0, -1, -1}, {0, -1, 0}} {
		if g.world.GetBlock(u.X+d[0], u.Y+d[1], u.Z+d[2]) == world.StateWater {
			wet = farmlandWet
			break
		}
	}
	g.applyBlock(u.X, u.Y, u.Z, wet, u.Sequence, p)
	g.damageHeld(p)
	return true
}

// tryPlantSeed 种子右键耕地顶面 → 种小麦（age 0）。返回 true 拦截。
func (g *Game) tryPlantSeed(p *entity.Player, u packet.UseItemOn) bool {
	held := p.Held()
	if held.ID == 0 || u.Face != 1 { // 只种顶面
		return false
	}
	name := g.itemNameOf(p, held.ID)
	if name != "wheat_seeds" {
		return false
	}
	if g.world.GetBlock(u.X, u.Y, u.Z) != farmlandState && g.world.GetBlock(u.X, u.Y, u.Z) != farmlandWet {
		return false
	}
	if g.world.GetBlock(u.X, u.Y+1, u.Z) != world.StateAir {
		return true
	}
	g.applyBlock(u.X, u.Y+1, u.Z, wheatAges[0], u.Sequence, p)
	g.crops[[3]int32{u.X, u.Y + 1, u.Z}] = 0
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

// tickCrops 作物生长（随机刻近似：每 30 刻 10% 长一龄）。
func (g *Game) tickCrops() {
	if g.tick%30 != 0 {
		return
	}
	for pos := range g.crops {
		s := g.world.GetBlock(pos[0], pos[1], pos[2])
		age := -1
		for i, wa := range wheatAges {
			if s == wa {
				age = i
				break
			}
		}
		if age < 0 || age >= 7 {
			continue
		}
		if rand.Intn(10) != 0 {
			continue
		}
		g.applyBlock(pos[0], pos[1], pos[2], wheatAges[age+1], 0, nil)
	}
}

// harvestWheatIfMature 挖成熟小麦掉麦子+种子（未成熟只掉种子）。
func (g *Game) harvestWheatIfMature(x, y, z int32, old uint32) {
	age := -1
	for i, wa := range wheatAges {
		if old == wa {
			age = i
			break
		}
	}
	if age < 0 {
		return
	}
	delete(g.crops, [3]int32{x, y, z})
	ids := assets.ItemIDs(777)
	if age == 7 {
		g.spawnItem(float64(x)+0.5, float64(y)+0.3, float64(z)+0.5,
			entity.Item{ID: ids["wheat"], Count: 1})
		g.spawnItem(float64(x)+0.5, float64(y)+0.3, float64(z)+0.5,
			entity.Item{ID: ids["wheat_seeds"], Count: int32(1 + rand.Intn(2))})
	} else {
		g.spawnItem(float64(x)+0.5, float64(y)+0.3, float64(z)+0.5,
			entity.Item{ID: ids["wheat_seeds"], Count: 1})
	}
}

// tillTargets 草方块两状态（777：8=snowy true, 9=false）都算。
var grassStates = map[uint32]bool{8: true, 9: true}
