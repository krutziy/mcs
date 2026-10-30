// Package terrain 要塞（简化）：每世界一座，按种子确定性放置在距原点
// 800-1500 格的环带上，地下石砖传送门室（7x3x7 空腔 + 3x3 岩浆池 +
// 12 框末地门环）。末影之眼指向其 X/Z，玩家挖掘向下寻室。
package terrain

import (
	"math"

	"mcserver/internal/world"
)

// StrongholdPos 要塞传送门室中心（确定性）。
func StrongholdPos(seed int64) (int32, int32) {
	angle := hash2(41, 7, seed+2026) * 2 * math.Pi
	dist := 800 + hash2(13, 29, seed+313)*700
	return int32(math.Round(math.Cos(angle) * dist)), int32(math.Round(math.Sin(angle) * dist))
}

// 要塞室方块状态（777 基准）。
const (
	stateStoneBricks    uint32 = 449
	stateFrameEyeN      uint32 = 11204 // eye=true facing=south（北缘朝内）
	stateFrameEyeS      uint32 = 11203 // eye=true facing=north
	stateFrameEyeW      uint32 = 11210 // eye=true facing=east
	stateFrameEyeE      uint32 = 11209 // eye=true facing=west
	stateFrameNoEyeN    uint32 = 11208 // eye=false facing=south
	stateFrameNoEyeS    uint32 = 11207
	stateFrameNoEyeW    uint32 = 11212 // 由下方按差值推导的兜底不用——直接查表
	stateLavaPool       uint32 = 105
)

// buildStrongholdRoom 若区块与要塞室相交则构建。
// 室：中心 (cx, ry, cz)，ry = MinY+30；石砖外壳 11x7x11（含地板/顶），
// 内腔 9x5x9 空气；地板中央 3x3 岩浆；框环在岩浆外缘一圈（5x5 边去角 = 12）。
func (g *NoiseGenerator) buildStrongholdRoom(ch *world.Chunk) {
	cx, cz := StrongholdPos(g.Seed)
	ry := int32(world.MinY + 30)
	// 室外壳范围（±5）与区块不相交则跳过
	if !spanHit(cx-5, cx+5, ch.CX*16, ch.CX*16+15) || !spanHit(cz-5, cz+5, ch.CZ*16, ch.CZ*16+15) {
		return
	}
	for dx := int32(-5); dx <= 5; dx++ {
		for dz := int32(-5); dz <= 5; dz++ {
			wx, wz := cx+dx, cz+dz
			lx, lz := wx-ch.CX*16, wz-ch.CZ*16
			if lx < 0 || lx > 15 || lz < 0 || lz > 15 {
				continue
			}
			for dy := int32(-2); dy <= 5; dy++ {
				switch {
				case dy == -2 || dy == 5 || abs(dx) == 5 || abs(dz) == 5:
					ch.SetBlock(lx, ry-world.MinY+dy, lz, stateStoneBricks)
				default:
					ch.SetBlock(lx, ry-world.MinY+dy, lz, world.StateAir)
				}
			}
			// 岩浆池（地板中央 3x3）
			if abs(dx) <= 1 && abs(dz) <= 1 {
				ch.SetBlock(lx, ry-world.MinY, lz, stateLavaPool)
			}
		}
	}
	// 末地门框环：5x5 边去角 12 个，朝向室内
	for dx := int32(-2); dx <= 2; dx++ {
		for dz := int32(-2); dz <= 2; dz++ {
			if abs(dx) != 2 && abs(dz) != 2 {
				continue
			}
			if abs(dx) == 2 && abs(dz) == 2 {
				continue // 角
			}
			wx, wz := cx+dx, cz+dz
			lx, lz := wx-ch.CX*16, wz-ch.CZ*16
			if lx < 0 || lx > 15 || lz < 0 || lz > 15 {
				continue
			}
			// eye=false 族：N=11207 S=11208 W=11209 E=11210；框面朝室内
			frame := uint32(11209) // dx==+2（东侧）→ facing=west
			switch {
			case dz == -2:
				frame = 11208 // 北缘 → facing=south
			case dz == 2:
				frame = 11207 // 南缘 → facing=north
			case dx == -2:
				frame = 11210 // 西缘 → facing=east
			}
			ch.SetBlock(lx, ry-world.MinY+1, lz, frame)
		}
	}
}

// spanHit 区间 [a,b] 与 [c,d] 相交。
func spanHit(a, b, c, d int32) bool {
	return a <= d && c <= b
}
