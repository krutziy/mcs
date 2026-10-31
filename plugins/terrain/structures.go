// Package terrain 结构生成：下界要塞（十字廊桥 + 下界疣农场 + 烈焰刷怪笼）、
// 末地外岛（末地城塔 + 战利品位）、要塞走廊（通往传送门室的石砖甬道 + 蠹虫刷怪笼）。
// 全部按种子确定性放置，跨区块按列裁剪写入。
package terrain

import (
	"math"

	"mcserver/internal/world"
)

// 结构方块状态（777 基准）。
const (
	stateNetherBrick  uint32 = 11068
	stateSoulSand2    uint32 = 8668
	stateNetherWart0  uint32 = 11181 // age=0
	stateNetherWart3  uint32 = 11184 // age=3
	stateSpawner      uint32 = 5444
	statePurpur       uint32 = 16687
	stateEndRod       uint32 = 16611 // end_rod 默认（up=false 等）
	stateStoneBricks2 uint32 = 449
)

// NetherFortressPos 下界要塞中心（确定性，距原点 100-180 格）。
func NetherFortressPos(seed int64) (int32, int32) {
	angle := hash2(17, 3, seed+707) * 2 * math.Pi
	dist := 100 + hash2(31, 11, seed+808)*80
	return int32(math.Round(math.Cos(angle) * dist)), int32(math.Round(math.Sin(angle) * dist))
}

// buildNetherFortress 廊桥十字：N-S 与 E-W 各 48 长、5 宽，桥面 rel y=78；
// 十字中心平台 + 烈焰刷怪笼；北端下界疣农场（灵魂沙 5x5 + 疣）。
func (g NetherGenerator) buildNetherFortress(ch *world.Chunk) {
	fx, fz := NetherFortressPos(g.Seed)
	const half = int32(24)
	if !spanHit(fx-half, fx+half, ch.CX*16, ch.CX*16+15) ||
		!spanHit(fz-half, fz+half, ch.CZ*16, ch.CZ*16+15) {
		return
	}
	y := int32(78)
	put := func(wx, wy, wz int32, s uint32) {
		lx, lz := wx-ch.CX*16, wz-ch.CZ*16
		if lx < 0 || lx > 15 || lz < 0 || lz > 15 || wy < 0 || wy > 126 {
			return
		}
		ch.SetBlock(lx, wy, lz, s)
	}
	onBridge := func(dx, dz int32) bool {
		return (abs(dx) <= 2 && abs(dz) <= half) || (abs(dz) <= 2 && abs(dx) <= half)
	}
	for dx := -half; dx <= half; dx++ {
		for dz := -half; dz <= half; dz++ {
			if !onBridge(dx, dz) {
				continue
			}
			wx, wz := fx+dx, fz+dz
			put(wx, y-1, wz, stateNetherBrick) // 桥面
			// 侧墙 h2（带窗洞：每 4 格开一格）
			if (abs(dx) == 2 || abs(dz) == 2) && !(abs(dx) == 2 && abs(dz) == 2) {
				if dx%4 != 0 {
					put(wx, y, wz, stateNetherBrick)
					put(wx, y+1, wz, stateNetherBrick)
				}
			}
		}
	}
	// 十字中心：平台 + 刷怪笼
	for dx := int32(-3); dx <= 3; dx++ {
		for dz := int32(-3); dz <= 3; dz++ {
			put(fx+dx, y-1, fz+dz, stateNetherBrick)
		}
	}
	put(fx, y, fz, stateSpawner)
	put(fx, y+1, fz, stateNetherBrick) // 笼上封顶装饰
	// 北端疣农场：5x5 灵魂沙 + 疣（age 3）
	for dx := int32(-2); dx <= 2; dx++ {
		for dz := int32(-2); dz <= 2; dz++ {
			wx, wz := fx+dx, fz-half+dz
			put(wx, y, wz, stateSoulSand2)
			if abs(dx) <= 1 && abs(dz) <= 1 {
				put(wx, y+1, wz, stateNetherWart3)
			}
		}
	}
}

// EndCityPos 末地外岛城市位（确定性，距中心 150-240 格，4 座）。
func EndCityPos(seed int64, i int) (int32, int32) {
	angle := float64(i)/4*2*math.Pi + hash2(int64(i*7), 5, seed+909)*0.8
	dist := 150 + hash2(int64(i*13), 17, seed+1010)*90
	return int32(math.Round(math.Cos(angle) * dist)), int32(math.Round(math.Sin(angle) * dist))
}

// buildEndCities 末地外岛：半径 18 的小岛（rel y 74-84）+ 城塔（7x7、12 高、
// 楼梯间 + 顶部战利品箱位与末地烛）。
func (g EndGenerator) buildEndCities(ch *world.Chunk) {
	for i := 0; i < 4; i++ {
		cx, cz := EndCityPos(g.Seed, i)
		loX, hiX := ch.CX*16, ch.CX*16+15
		loZ, hiZ := ch.CZ*16, ch.CZ*16+15
		if cx-18 > hiX || cx+18 < loX || cz-18 > hiZ || cz+18 < loZ {
			continue
		}
		set := func(x, y, z int32, st uint32) {
			ch.SetBlock(x, y, z, st)
		}
		// 岛（endstone 圆台）
		for dx := int32(-18); dx <= 18; dx++ {
			for dz := int32(-18); dz <= 18; dz++ {
				x, z := cx+dx, cz+dz
				if x < loX || x > hiX || z < loZ || z > hiZ {
					continue
				}
				d := math.Sqrt(float64(dx*dx + dz*dz))
				if d > 18 {
					continue
				}
				h := int32(8 * (1 - d/20))
				if h < 1 {
					h = 1
				}
				for y := int32(74); y < 74+h; y++ {
					set(x-ch.CX*16, y, z-ch.CZ*16, stateEndStone)
				}
			}
		}
		// 塔：7x7、12 高（rel 84-95），石壁中空
		base := int32(84)
		for dx := int32(-3); dx <= 3; dx++ {
			for dz := int32(-3); dz <= 3; dz++ {
				x, z := cx+dx, cz+dz
				if x < loX || x > hiX || z < loZ || z > hiZ {
					continue
				}
				for dy := int32(0); dy < 12; dy++ {
					st := world.StateAir
					if abs(dx) == 3 || abs(dz) == 3 {
						st = statePurpur
					}
					set(x-ch.CX*16, base+dy, z-ch.CZ*16, st)
				}
			}
		}
		// 外螺旋梯
		for step := int32(0); step < 12; step++ {
			a := (step * 2) % 20
			var tx, tz int32
			switch {
			case a < 5:
				tx, tz = 4, -4+a
			case a < 10:
				tx, tz = 4-(a-5), 4
			case a < 15:
				tx, tz = -4, 4-(a-10)
			default:
				tx, tz = -4+(a-15), -4
			}
			x, z := cx+tx, cz+tz
			if x >= loX && x <= hiX && z >= loZ && z <= hiZ {
				set(x-ch.CX*16, base+step, z-ch.CZ*16, statePurpur)
			}
		}
		// 顶板 + 末地烛 + 战利品箱
		for dx := int32(-3); dx <= 3; dx++ {
			for dz := int32(-3); dz <= 3; dz++ {
				x, z := cx+dx, cz+dz
				if x >= loX && x <= hiX && z >= loZ && z <= hiZ {
					set(x-ch.CX*16, base-1, z-ch.CZ*16, statePurpur)
				}
			}
		}
		if cx >= loX && cx <= hiX && cz >= loZ && cz <= hiZ {
			lx, lz := cx-ch.CX*16, cz-ch.CZ*16
			set(lx-2, base, lz-2, stateEndRod)
			set(lx+2, base, lz+2, stateEndRod)
			set(lx, base, lz, 5544) // chest（首开填充末地城战利品）
		}
	}
}

// buildStrongholdCorridor 要塞走廊：从传送门室向北 24 格石砖甬道
//（3 宽 4 高，每隔 8 格一个蠹虫刷怪笼）。
func (g *NoiseGenerator) buildStrongholdCorridor(ch *world.Chunk) {
	cx, cz := StrongholdPos(g.Seed)
	ry := int32(world.MinY + 30)
	const len = int32(24)
	if !spanHit(cx-2, cx+2, ch.CX*16, ch.CX*16+15) ||
		!spanHit(cz-len-2, cz-6, ch.CZ*16, ch.CZ*16+15) {
		return
	}
	for d := int32(6); d < len; d++ { // 从室外北墙之外起挖（不破坏门室）
		for dx := int32(-1); dx <= 1; dx++ {
			wx, wz := cx+dx, cz-d
			lx, lz := wx-ch.CX*16, wz-ch.CZ*16
			if lx < 0 || lx > 15 || lz < 0 || lz > 15 {
				continue
			}
			ch.SetBlock(lx, ry-world.MinY-1, lz, stateStoneBricks2) // 地板
			ch.SetBlock(lx, ry-world.MinY+3, lz, stateStoneBricks2) // 顶
			if dx == 0 {
				ch.SetBlock(lx, ry-world.MinY, lz, world.StateAir)
				ch.SetBlock(lx, ry-world.MinY+1, lz, world.StateAir)
				ch.SetBlock(lx, ry-world.MinY+2, lz, world.StateAir)
				if d%8 == 4 {
					ch.SetBlock(lx, ry-world.MinY+2, lz, stateSpawner)
				}
			} else {
				ch.SetBlock(lx, ry-world.MinY, lz, stateStoneBricks2)
				ch.SetBlock(lx, ry-world.MinY+1, lz, stateStoneBricks2)
				ch.SetBlock(lx, ry-world.MinY+2, lz, stateStoneBricks2)
			}
		}
	}
}
