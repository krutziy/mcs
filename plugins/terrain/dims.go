// Package terrain 维度地形：下界（下界岩洞窟 + 岩浆海 + 灵魂沙/萤石/下界矿）
// 与末地（末地石主岛 + 黑曜石平台 + 中央返程传送门）。
// 均为确定性噪声生成，与世界类型同源（seed 一致）。
package terrain

import (
	"math"

	"mcserver/internal/world"
)

// 下界方块状态（777 基准）。
const (
	stateNetherrack   uint32 = 8667
	stateSoulSand     uint32 = 8668
	stateGlowstone    uint32 = 8686
	stateNetherQuartz uint32 = 13079
	stateNetherGold   uint32 = 138
	stateMagma        uint32 = 16826
	stateNetherBricks uint32 = 11068
)

// NetherGenerator 下界：固定顶部基岩盖 + 下界岩体内 3D 噪声洞窟 + 岩浆海。
type NetherGenerator struct {
	Seed int64
}

// Generate 填充下界区块（使用下半世界高度：MinY..MinY+127）。
func (g NetherGenerator) Generate(ch *world.Chunk) {
	top := int32(126) // 相对 min_y 的下界顶部
	lava := int32(32) // 岩浆海面（相对）
	for z := int32(0); z < 16; z++ {
		for x := int32(0); x < 16; x++ {
			wx, wz := ch.CX*16+x, ch.CZ*16+z
			for y := int32(0); y <= top; y++ {
				switch {
				case y == 0 || y == top:
					ch.SetBlock(x, y, z, world.StateBedrock) // 底/顶基岩盖
				case g.isCave(wx, y, wz):
					if y <= lava {
						ch.SetBlock(x, y, z, world.StateLava) // 洞窟低洼填岩浆
					}
				default:
					ch.SetBlock(x, y, z, stateNetherrack)
				}
			}
			// 地表附近替换：岩浆海岸为岩浆块、灵魂沙斑块
			if !g.isCave(wx, lava+1, wz) {
				r := hash2(int64(wx), int64(wz), g.Seed+41)
				if r > 0.82 {
					ch.SetBlock(x, lava+1, z, stateSoulSand)
				} else if r < 0.06 {
					ch.SetBlock(x, lava+1, z, stateMagma)
				}
			}
		}
	}
	g.plantNetherOres(ch)
	g.plantGlowstone(ch)
}

// isCave 3D 值噪声洞窟判定（阈值控制洞窟密度）。
func (g NetherGenerator) isCave(x, y, z int32) bool {
	n := g.noise3(float64(x)/24, float64(y)/16, float64(z)/24)
	return n > 0.08
}

// noise3 3D 平滑值噪声（-1..1，三线性插值）。
func (g NetherGenerator) noise3(x, y, z float64) float64 {
	x0, y0, z0 := math.Floor(x), math.Floor(y), math.Floor(z)
	fx, fy, fz := x-x0, y-y0, z-z0
	sx := fx * fx * (3 - 2*fx)
	sy := fy * fy * (3 - 2*fy)
	sz := fz * fz * (3 - 2*fz)
	c := func(dx, dy, dz float64) float64 {
		return hash3f(int64(x0+dx), int64(y0+dy), int64(z0+dz), g.Seed)
	}
	ix00 := c(0, 0, 0) + (c(1, 0, 0)-c(0, 0, 0))*sx
	ix10 := c(0, 1, 0) + (c(1, 1, 0)-c(0, 1, 0))*sx
	ix01 := c(0, 0, 1) + (c(1, 0, 1)-c(0, 0, 1))*sx
	ix11 := c(0, 1, 1) + (c(1, 1, 1)-c(0, 1, 1))*sx
	iy0 := ix00 + (ix10-ix00)*sy
	iy1 := ix01 + (ix11-ix01)*sy
	return (iy0 + (iy1-iy0)*sz)*2 - 1
}

// hash3f 三维格点哈希（[0,1)）。
func hash3f(ix, iy, iz int64, seed int64) float64 {
	h := uint64(ix)*0x9E3779B1 + uint64(iy)*0x85EBCA77 + uint64(iz)*0xC2B2AE3D + uint64(seed)*0x27D4EB2F
	h ^= h >> 29
	h *= 0xBF58476D1CE4E5B9
	h ^= h >> 32
	return float64(h>>11) / float64(1<<53)
}

// plantNetherOres 下界矿（石英/下界金）。
func (g NetherGenerator) plantNetherOres(ch *world.Chunk) {
	for try := 0; try < 6; try++ {
		for i, ore := range []uint32{stateNetherQuartz, stateNetherGold} {
			h1 := hash3(int64(ch.CX)*31, int64(ch.CZ)+int64(try), int64(i), g.Seed+7)
			h2 := hash3(int64(ch.CZ), int64(try*13+i), int64(ch.CX)*7, g.Seed+17)
			x, z := int32(h1%16), int32(h2%16)
			if x < 0 {
				x = -x
			}
			if z < 0 {
				z = -z
			}
			y := int32(8 + (h1>>8)%100)
			// 小脉（4 块随机游走替换下界岩）
			cx, cy, cz := x, y, z
			for n := 0; n < 4; n++ {
				if cy > 1 && cy < 126 && ch.GetBlock(cx, cy, cz) == stateNetherrack {
					ch.SetBlock(cx, cy, cz, ore)
				}
				d := hash3(int64(cx)+int64(n), int64(cy), int64(cz), g.Seed+int64(i*100+n))
				switch d % 3 {
				case 0:
					cx += int32(d%3) - 1
				case 1:
					cy += int32((d>>8)%3) - 1
				default:
					cz += int32((d>>16)%3) - 1
				}
				cx, cz = clamp16(cx), clamp16(cz)
				if cy < 2 {
					cy = 2
				}
			}
		}
	}
}

func clamp16(v int32) int32 {
	if v < 0 {
		return 0
	}
	if v > 15 {
		return 15
	}
	return v
}

// plantGlowstone 顶部洞窟垂挂萤石团。
func (g NetherGenerator) plantGlowstone(ch *world.Chunk) {
	for try := 0; try < 4; try++ {
		h1 := hash3(int64(ch.CX)+int64(try), int64(ch.CZ)*7, int64(try*3), g.Seed+99)
		h2 := hash3(int64(ch.CZ), int64(ch.CX)*11, int64(try*5), g.Seed+131)
		x, z := clamp16(int32(h1%16)), clamp16(int32(h2%16))
		if x < 0 {
			x = -x
		}
		if z < 0 {
			z = -z
		}
		y := int32(96 + (h1>>10)%24)
		if ch.GetBlock(x, y, z) == stateNetherrack {
			ch.SetBlock(x, y, z, stateGlowstone)
			if r := hash3(int64(x), int64(y), int64(z), g.Seed+3); r%2 == 0 && y > 2 {
				ch.SetBlock(x, y-1, z, stateGlowstone)
			}
		}
	}
}

// SurfaceY 下界可站立 y（找岩浆海面之上首个非空）。
func (g NetherGenerator) SurfaceY(x, z int32) int32 {
	return world.MinY + 34
}

// ---- 末地 ----

const stateEndStone uint32 = 11211

// EndGenerator 末地：以 (0,0) 为中心的末地石主岛 + 黑曜石平台 + 中央返程门。
type EndGenerator struct {
	Seed int64
}

// islandY 主岛表面相对高度（绝对 y = MinY + islandY + 12 附近）。
func (g EndGenerator) islandTop(wx, wz int32) (int32, bool) {
	d := math.Sqrt(float64(wx*wx + wz*wz))
	if d > 62 {
		return 0, false // 岛外虚空
	}
	// 边缘噪声收薄
	n := EndNoise(float64(wx)/40, float64(wz)/40, g.Seed)
	h := int32(10 + n*4) // 岛厚
	if d > 50 {
		h = int32(float64(h) * (62 - d) / 12)
	}
	if h < 1 {
		h = 1
	}
	return h, true
}

// EndNoise 末地专用 2D 值噪声（静态种子派生，独立于实例方法以便复用）。
func EndNoise(x, y float64, seed int64) float64 {
	return hash2(int64(x*8), int64(y*8), seed+5)*2 - 1
}

// 末地黑曜石平台：绝对 (98..102, y, 0..4)，相对 y=84。
const (
	endPlatformRel = 84
	endPlatformX   = int32(100)
	endPlatformZ   = int32(2)
)

// Generate 填充末地区块（主岛底层相对 y=74，厚度 ~10-14）。
func (g EndGenerator) Generate(ch *world.Chunk) {
	base := int32(74)
	for z := int32(0); z < 16; z++ {
		for x := int32(0); x < 16; x++ {
			wx, wz := ch.CX*16+x, ch.CZ*16+z
			h, on := g.islandTop(wx, wz)
			if !on {
				continue
			}
			for y := base; y < base+h; y++ {
				ch.SetBlock(x, y, z, stateEndStone)
			}
		}
	}
	// 中央返程门（0,0）：岛面清出基座 + end_portal 方块（踩上传送回主世界）
	if ch.CX == 0 && ch.CZ == 0 {
		h0, _ := g.islandTop(0, 0)
		top := base + h0 - 1 // 岛面
		for dx := int32(-1); dx <= 1; dx++ {
			for dz := int32(-1); dz <= 1; dz++ {
				ch.SetBlock(dx&15, top, dz&15, stateEndStone)
			}
		}
		ch.SetBlock(0, top, 0, world.StateBedrock)
		ch.SetBlock(0, top+1, 0, 11202) // minecraft:end_portal
	}
	// 黑曜石抵达平台（出生点）
	if ch.CX == 6 && ch.CZ == 0 {
		for dx := int32(-2); dx <= 2; dx++ {
			for dz := int32(-2); dz <= 2; dz++ {
				ch.SetBlock((endPlatformX+dx)&15, endPlatformRel, endPlatformZ+dz, world.StateObsidian)
			}
		}
	}
}

// SurfaceY 末地黑曜石平台站立高度。
func (g EndGenerator) SurfaceY(x, z int32) int32 {
	if x >= endPlatformX-2 && x <= endPlatformX+2 && z >= endPlatformZ-2 && z <= endPlatformZ+2 {
		return world.MinY + endPlatformRel + 1
	}
	return world.MinY + 88 // 岛面近似
}

func init() {
	world.RegisterGenerator("nether", func(seed int64) world.Generator { return NetherGenerator{Seed: seed} })
	world.RegisterGenerator("end", func(seed int64) world.Generator { return EndGenerator{Seed: seed} })
}
