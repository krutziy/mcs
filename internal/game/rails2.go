package game

// 动力铁轨与探测铁轨：
//   动力铁轨（powered_rail）——通电时给驶过矿车加速（沿轨道轴向提速至
//   0.4 格/刻），未通电时刹车（速度大幅衰减）；powered 属性随邻电切换。
//   探测铁轨（detector_rail）——矿车驶上时 powered=true 并作为红石源，
//   驶离断电。两轨均可手放、参与弯轨自动连接（shape 属性同 rail）。

import (
	"math/rand"
	"strings"
)

// stateByFullName 规范名 → 状态（loadFacingTables 时构建）。
var stateByFullName = map[string]uint32{}

// isPoweredRail / isDetectorRail 按短名判定（含全部 shape/powered 形态）。
func isPoweredRail(s uint32) bool  { return blockNameLookup[s] == "powered_rail" }
func isDetectorRail(s uint32) bool { return blockNameLookup[s] == "detector_rail" }

// isAnyRail 普通轨 + 动力/探测轨（矿车可行驶面）。
func isAnyRail(s uint32) bool { return isRail(s) || isPoweredRail(s) || isDetectorRail(s) }

// railPoweredProp 状态的 powered 属性是否为 true。
func railPoweredProp(s uint32) bool {
	return strings.Contains(fullStateNames[s], "powered=true")
}

// flipRailPowered 同方块同 shape、powered 取反的状态（0 = 无）。
func flipRailPowered(s uint32) uint32 {
	full := fullStateNames[s]
	if full == "" {
		return 0
	}
	var want string
	if strings.Contains(full, "powered=true") {
		want = strings.Replace(full, "powered=true", "powered=false", 1)
	} else if strings.Contains(full, "powered=false") {
		want = strings.Replace(full, "powered=false", "powered=true", 1)
	} else {
		return 0
	}
	return stateByFullName[want]
}

// shapeForSpecialRail 按邻轨给动力/探测轨选 shape（同名族未充能态）。
func shapeForSpecialRail(g *Game, dim, x, y, z int32, kind string) uint32 {
	n := isAnyRail(g.wd(dim).GetBlock(x, y, z-1))
	s := isAnyRail(g.wd(dim).GetBlock(x, y, z+1))
	e := isAnyRail(g.wd(dim).GetBlock(x+1, y, z))
	w := isAnyRail(g.wd(dim).GetBlock(x-1, y, z))
	var shape string
	switch {
	case n && e:
		shape = "north_east"
	case n && w:
		shape = "north_west"
	case s && e:
		shape = "south_east"
	case s && w:
		shape = "south_west"
	case e || w:
		shape = "east_west"
	default:
		shape = "north_south"
	}
	return stateByFullName["minecraft:"+kind+"[powered=false,shape="+shape+",waterlogged=false]"]
}

// autoShapeSpecialRail 放置后按邻轨定形。
func (g *Game) autoShapeSpecialRail(dim, x, y, z int32) {
	s := g.wd(dim).GetBlock(x, y, z)
	kind := ""
	switch {
	case isPoweredRail(s):
		kind = "powered_rail"
	case isDetectorRail(s):
		kind = "detector_rail"
	default:
		return
	}
	if ns := shapeForSpecialRail(g, dim, x, y, z, kind); ns != 0 && ns != s {
		g.applyBlock(dim, x, y, z, ns, 0, nil)
	}
}

// cartOnBlock 该方块正上方（同格浮起）是否有矿车。
func (g *Game) cartOnBlock(dim, x, y, z int32) bool {
	for _, mc := range g.carts {
		if mc.dim == dim && floorI(mc.x) == x && floorI(mc.y-0.06) == y && floorI(mc.z) == z {
			return true
		}
	}
	return false
}

// updateSpecialRailPower 单点更新：动力轨按邻电、探测轨按有车。
func (g *Game) updateSpecialRailPower(dim, x, y, z int32) {
	s := g.wd(dim).GetBlock(x, y, z)
	if isPoweredRail(s) {
		if powered := g.posPowered(dim, x, y, z); powered != railPoweredProp(s) {
			if ns := flipRailPowered(s); ns != 0 {
				g.setRS(dim, x, y, z, ns)
			}
		}
		return
	}
	if isDetectorRail(s) {
		if has := g.cartOnBlock(dim, x, y, z); has != railPoweredProp(s) {
			if ns := flipRailPowered(s); ns != 0 {
				g.setRS(dim, x, y, z, ns)
			}
		}
	}
}

// tickDetectorRails 每 2 刻：矿车所在轨即时检测 + 曾通电探测轨复查（车走断电）。
func (g *Game) tickDetectorRails() {
	if g.tick%2 != 0 {
		return
	}
	for _, mc := range g.carts {
		g.updateSpecialRailPower(mc.dim, floorI(mc.x), floorI(mc.y-0.06), floorI(mc.z))
		// 记录涉及的探测轨（离开检测用）
		s := g.wd(mc.dim).GetBlock(floorI(mc.x), floorI(mc.y-0.06), floorI(mc.z))
		if isDetectorRail(s) {
			key := [4]int32{mc.dim, floorI(mc.x), floorI(mc.y - 0.06), floorI(mc.z)}
			g.detectorHot[key] = true
		}
	}
	for key := range g.detectorHot {
		dim, x, y, z := key[0], key[1], key[2], key[3]
		s := g.wd(dim).GetBlock(x, y, z)
		if !isDetectorRail(s) || !g.cartOnBlock(dim, x, y, z) {
			// 车走/轨被拆：若仍通电则断电并重算红石
			if isDetectorRail(s) && railPoweredProp(s) {
				if ns := flipRailPowered(s); ns != 0 {
					g.setRS(dim, x, y, z, ns)
					g.rsDirty = true
				}
			}
			delete(g.detectorHot, key)
		}
	}
}

// cartRailBoost 矿车所在动力轨的加速/刹车（tickCarts 每刻调用）。
// 返回 true = 该格为动力轨（已处理加速/刹车）。
func (g *Game) cartRailBoost(mc *minecart) bool {
	dim := mc.dim
	s := g.wd(dim).GetBlock(floorI(mc.x), floorI(mc.y-0.06), floorI(mc.z))
	if !isPoweredRail(s) {
		return false
	}
	if railPoweredProp(s) {
		// 通电：沿轨道轴向加速到上限 0.4（静止车给 0.06 起步推力）
		full := fullStateNames[s]
		nsAxis := strings.Contains(full, "shape=north_south")
		if nsAxis {
			if mc.vx == 0 && mc.vz == 0 {
				mc.vz = 0.06
			}
			if mc.vz >= 0 {
				mc.vz = 0.4
			} else {
				mc.vz = -0.4
			}
			mc.vx = 0
		} else {
			if mc.vx == 0 && mc.vz == 0 {
				mc.vx = 0.06
			}
			if mc.vx >= 0 {
				mc.vx = 0.4
			} else {
				mc.vx = -0.4
			}
			mc.vz = 0
		}
		mc.moving = true
	} else {
		// 未通电：刹车
		mc.vx *= 0.5
		mc.vz *= 0.5
	}
	return true
}

// ---- 漏斗随机槽 ----

// hopperScanOrder 原版漏斗从随机槽开始扫描：这里 1/4 概率返回随机起点，
// 其余从首格（近似 FIFO 与随机混合）。
func hopperScanOrder() int32 {
	if rand.Intn(4) == 0 {
		return int32(rand.Intn(5))
	}
	return 0
}
