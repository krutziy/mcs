package game

// 多植被：甜浆果丛（采拔/种植/生长/荆棘伤害）与蘑菇（种植 + 骨粉巨型蘑菇）。
// 方块状态：sweet_berry_bush age 0-3（24298-24301）、brown/red_mushroom（3828/3829）、
// 巨型蘑菇帽/茎（棕色 9500、红色 9564、茎 9691，全朝向真值形态）。

import (
	"math/rand"

	"mcserver/internal/assets"
	"mcserver/internal/entity"
	"mcserver/internal/packet"
	"mcserver/internal/world"
)

// 浆果丛与蘑菇状态（777 基准）。
const (
	bushAge0 uint32 = 24298
	bushAge1 uint32 = 24299
	bushAge2 uint32 = 24300
	bushAge3 uint32 = 24301
	brownShroom uint32 = 3828
	redShroom   uint32 = 3829
	brownCap    uint32 = 9500 // brown_mushroom_block 全朝向真
	redCap      uint32 = 9564 // red_mushroom_block 全朝向真
	shroomStem  uint32 = 9691 // mushroom_stem 全朝向真
)

var bushAges = [4]uint32{bushAge0, bushAge1, bushAge2, bushAge3}

func bushAgeOf(s uint32) int32 {
	for i, a := range bushAges {
		if s == a {
			return int32(i)
		}
	}
	return -1
}

// tryHarvestBerryBush 右键采浆果：age≥2 掉 1-3 颗并回置 age 1。返回 true 拦截。
func (g *Game) tryHarvestBerryBush(p *entity.Player, u packet.UseItemOn) bool {
	s := g.w(p).GetBlock(u.X, u.Y, u.Z)
	age := bushAgeOf(s)
	if age < 2 {
		return false
	}
	n := int32(1 + rand.Intn(2)) // age2: 1-2, age3: 2-3
	if age == 3 {
		n++
	}
	if !g.isCreative(p) {
		g.giveItem(p, entity.Item{ID: g.itemFor(p, assets.ItemIDs(777)["sweet_berries"]), Count: n})
	}
	g.applyBlock(p.Dim, u.X, u.Y, u.Z, bushAge1, u.Sequence, p)
	g.blockSound(p.Dim, float64(u.X), float64(u.Y), float64(u.Z), "block.sweet_berry_bush.pick_berries")
	return true
}

// tryPlantBerry 手持浆果右键草/泥土顶面：种浆果丛（age 0）。
func (g *Game) tryPlantBerry(p *entity.Player, u packet.UseItemOn) bool {
	if g.itemNameOf(p, p.Held().ID) != "sweet_berries" {
		return false
	}
	if u.Face != 1 { // 只在顶面种
		return false
	}
	s := g.w(p).GetBlock(u.X, u.Y, u.Z)
	if s != world.StateGrassBlock && s != 8 && s != 9 && s != world.StateDirt {
		return false
	}
	if g.w(p).GetBlock(u.X, u.Y+1, u.Z) != world.StateAir {
		return false
	}
	g.applyBlock(p.Dim, u.X, u.Y+1, u.Z, bushAge0, u.Sequence, p)
	g.trackBerryBush(p.Dim, u.X, u.Y+1, u.Z, bushAge0)
	if !g.isCreative(p) {
		held := &p.Slots[36+p.HeldSlot]
		held.Count--
		if held.Count <= 0 {
			*held = entity.Item{}
		}
		g.syncInventory(p)
	}
	return true
}

// trackBerryBush 玩家种植的浆果丛登记生长。
func (g *Game) trackBerryBush(dim, x, y, z int32, state uint32) {
	if age := bushAgeOf(state); age >= 0 {
		g.bushes[[4]int32{dim, x, y, z}] = age
	}
}

// tickBerryBushes 浆果丛随机生长（每 30 刻 15% 长一龄，封顶 age 3）。
func (g *Game) tickBerryBushes() {
	if g.tick%30 != 0 {
		return
	}
	for pos := range g.bushes {
		s := g.wd(pos[0]).GetBlock(pos[1], pos[2], pos[3])
		cur := bushAgeOf(s)
		if cur < 0 {
			delete(g.bushes, pos)
			continue
		}
		if cur >= 3 || rand.Intn(100) >= 15 {
			continue
		}
		g.applyBlock(pos[0], pos[1], pos[2], pos[3], bushAges[cur+1], 0, nil)
	}
}

// berryBushPrickle 荆棘伤害：玩家没入 age≥2 的浆果丛，每秒 1 点。
func (g *Game) berryBushPrickle(p *entity.Player) {
	feet := g.w(p).GetBlock(floorI(p.X), floorI(p.Y+0.2), floorI(p.Z))
	if bushAgeOf(feet) >= 2 && g.tick%20 == 0 {
		g.damage(p, 1, damageGeneric)
	}
}

// tryGiantMushroom 骨粉蘑菇 → 巨型蘑菇（棕色平顶 / 红色圆顶）。返回 true 拦截。
func (g *Game) tryGiantMushroom(p *entity.Player, u packet.UseItemOn, s uint32) bool {
	if s != brownShroom && s != redShroom {
		return false
	}
	x, y, z := u.X, u.Y, u.Z
	w := g.w(p)
	// 高度检查（防穿顶）
	h := int32(4 + rand.Intn(2))
	for t := int32(1); t <= h+1; t++ {
		if w.GetBlock(x, y+t, z) != world.StateAir && !isLeaves(w.GetBlock(x, y+t, z)) {
			return false
		}
	}
	// 茎
	g.applyBlock(p.Dim, x, y, z, shroomStem, 0, nil)
	for t := int32(1); t < h; t++ {
		g.applyBlock(p.Dim, x, y+t, z, shroomStem, 0, nil)
	}
	top := y + h
	if s == brownShroom {
		// 棕色：5×5 平顶帽
		for dx := int32(-2); dx <= 2; dx++ {
			for dz := int32(-2); dz <= 2; dz++ {
				if w.GetBlock(x+dx, top, z+dz) == world.StateAir {
					g.applyBlock(p.Dim, x+dx, top, z+dz, brownCap, 0, nil)
				}
			}
		}
	} else {
		// 红色：5×5 帽（去角）+ 上一层 3×3
		for dx := int32(-2); dx <= 2; dx++ {
			for dz := int32(-2); dz <= 2; dz++ {
				if abs32(dx) == 2 && abs32(dz) == 2 {
					continue
				}
				if w.GetBlock(x+dx, top, z+dz) == world.StateAir {
					g.applyBlock(p.Dim, x+dx, top, z+dz, redCap, 0, nil)
				}
			}
		}
		for dx := int32(-1); dx <= 1; dx++ {
			for dz := int32(-1); dz <= 1; dz++ {
				if w.GetBlock(x+dx, top+1, z+dz) == world.StateAir {
					g.applyBlock(p.Dim, x+dx, top+1, z+dz, redCap, 0, nil)
				}
			}
		}
	}
	g.blockSound(p.Dim, float64(x), float64(y), float64(z), "item.bone_meal.use")
	g.consumeBonemeal(p)
	return true
}
