// Package terrain 提供地形生成插件：超平坦与分形噪声起伏地形。
// 通过 init() 向 world 注册 "flat" / "noise" 两种世界类型。
// 壳子（cmd/server）通过空白导入装配本插件，删除导入即得到纯空壳。
package terrain

import (
	"math"

	"mcserver/internal/world"
)

// FlatGenerator 生成经典超平坦：基岩 + 泥土×2 + 草方块（表面 y = MinY+4 = -60）。
type FlatGenerator struct{}

// Generate 填充区块。
func (FlatGenerator) Generate(ch *world.Chunk) {
	for z := int32(0); z < 16; z++ {
		for x := int32(0); x < 16; x++ {
			ch.SetBlock(x, 0, z, world.StateBedrock)
			ch.SetBlock(x, 1, z, world.StateDirt)
			ch.SetBlock(x, 2, z, world.StateDirt)
			ch.SetBlock(x, 3, z, 9) // 官方平坦预设使用 snowy=true 的草方块状态
		}
	}
}

// SurfaceY 返回平坦世界玩家站立 y（绝对坐标）。
func (FlatGenerator) SurfaceY(_, _ int32) int32 { return world.MinY + 4 }

// NoiseGenerator 用分形值噪声生成起伏地形 + 海平面。
// 同一 (cx,cz) 永远生成相同结果（确定性）。
type NoiseGenerator struct {
	Seed      int64
	Amplitude int32   // 起伏幅度（格）
	BaseY     int32   // 平均地表相对高度（相对 min_y）
	SeaLevel  int32   // 水面相对高度
	Scale     float64 // 噪声尺度（格/周期）
}

// DefaultNoise 返回默认参数的噪声生成器。
func DefaultNoise(seed int64) *NoiseGenerator {
	return &NoiseGenerator{
		Seed:      seed,
		Amplitude: 24,
		BaseY:     40, // 绝对 y ≈ -24
		SeaLevel:  34, // 绝对 y ≈ -30
		Scale:     64,
	}
}

// SurfaceY 返回 (x,z) 处玩家可站立的 y（绝对坐标，x/z 为世界坐标）。
func (g *NoiseGenerator) SurfaceY(x, z int32) int32 {
	h := g.HeightAt(x, z)
	if h <= g.SeaLevel {
		return g.SeaLevel + 1 + world.MinY
	}
	return h + 1 + world.MinY
}

// Generate 填充区块。
func (g *NoiseGenerator) Generate(ch *world.Chunk) {
	for z := int32(0); z < 16; z++ {
		for x := int32(0); x < 16; x++ {
			wx, wz := ch.CX*16+x, ch.CZ*16+z
			h := g.HeightAt(wx, wz)
			for y := int32(0); y <= h; y++ {
				switch {
				case y == 0:
					ch.SetBlock(x, y, z, world.StateBedrock)
				case y == h:
					if h <= g.SeaLevel+1 {
						ch.SetBlock(x, y, z, world.StateSand)
					} else {
						ch.SetBlock(x, y, z, world.StateGrassBlock)
					}
				case y >= h-3:
					ch.SetBlock(x, y, z, world.StateDirt)
				default:
					if y < deepslateY {
						ch.SetBlock(x, y, z, stateDeepslate)
					} else {
						ch.SetBlock(x, y, z, world.StateStone)
					}
				}
			}
			for y := h + 1; y <= g.SeaLevel; y++ {
				ch.SetBlock(x, y, z, world.StateWater)
			}
		}
	}
	g.plantOres(ch)
	g.plantTrees(ch)
}

// HeightAt 返回 (x,z) 的地表相对高度（0..WorldHeight-1，相对 min_y）。
func (g *NoiseGenerator) HeightAt(x, z int32) int32 {
	n := g.fbm(float64(x)/g.Scale, float64(z)/g.Scale)
	h := g.BaseY + int32(n*float64(g.Amplitude))
	if h < 1 {
		h = 1
	}
	if h > world.WorldHeight-80 {
		h = world.WorldHeight - 80
	}
	return h
}

// fbm 分形值噪声（4 个八度），输出约 -1..1。
func (g *NoiseGenerator) fbm(x, y float64) float64 {
	var total, amp, freq, norm float64 = 0, 1, 1, 0
	for o := 0; o < 4; o++ {
		total += g.valueNoise(x*freq, y*freq, g.Seed+int64(o)*17) * amp
		norm += amp
		amp *= 0.5
		freq *= 2
	}
	return total / norm
}

// valueNoise 平滑插值值噪声，输出 -1..1。
func (g *NoiseGenerator) valueNoise(x, y float64, seed int64) float64 {
	x0, y0 := math.Floor(x), math.Floor(y)
	fx, fy := x-x0, y-y0
	sx := fx * fx * (3 - 2*fx)
	sy := fy * fy * (3 - 2*fy)
	v00 := hash2(int64(x0), int64(y0), seed)
	v10 := hash2(int64(x0)+1, int64(y0), seed)
	v01 := hash2(int64(x0), int64(y0)+1, seed)
	v11 := hash2(int64(x0)+1, int64(y0)+1, seed)
	ix0 := v00 + (v10-v00)*sx
	ix1 := v01 + (v11-v01)*sx
	return (ix0 + (ix1-ix0)*sy) * 2 - 1
}

// hash2 网格点伪随机（整数雪崩哈希），输出 [0,1)。
func hash2(ix, iy, seed int64) float64 {
	h := uint64(ix)*0x9E3779B1 + uint64(iy)*0x85EBCA77 + uint64(seed)*0xC2B2AE3D
	h ^= h >> 29
	h *= 0xBF58476D1CE4E5B9
	h ^= h >> 32
	return float64(h>>11) / float64(1<<53)
}

// 向壳子注册本插件提供的世界类型。
func init() {
	world.RegisterGenerator("flat", func(seed int64) world.Generator { return FlatGenerator{} })
	world.RegisterGenerator("noise", func(seed int64) world.Generator { return DefaultNoise(seed) })
}

// 橡树方块状态（777 基准，官方 reports）。
const (
	stateOakLog      uint32 = 8070
	stateOakLeaves   uint32 = 209
)

// treeAt 确定性判定某列是否长树（约 1/64 概率，且在岸边之上）。
func (g *NoiseGenerator) treeAt(wx, wz int32) bool {
	if g.valueNoise(float64(wx)*0.7, float64(wz)*0.7, g.Seed+999) < 0.72 {
		return false
	}
	h := g.HeightAt(wx, wz)
	return h > g.SeaLevel+2 && h < world.WorldHeight-90
}

// plantTrees 在区块内种树（跨区块树冠简化为不越界）。
func (g *NoiseGenerator) plantTrees(ch *world.Chunk) {
	for z := int32(2); z < 14; z++ {
		for x := int32(2); x < 14; x++ {
			wx, wz := ch.CX*16+x, ch.CZ*16+z
			if !g.treeAt(wx, wz) {
				continue
			}
			h := g.HeightAt(wx, wz)
			// 树干 4-6 格
			trunk := int32(4 + int(g.valueNoise(float64(wx), float64(wz), g.Seed+5)*3))
			for t := int32(1); t <= trunk; t++ {
				ch.SetBlock(x, h+t, z, stateOakLog)
			}
			top := h + trunk
			// 树冠：顶部十字 + 下方两层 5×5（去角）
			for dy := int32(-2); dy <= 0; dy++ {
				r := int32(2)
				if dy == 0 {
					r = 1
				}
				for dx := -r; dx <= r; dx++ {
					for dz := -r; dz <= r; dz++ {
						if dx == 0 && dz == 0 && dy < 0 {
							continue
						}
						if abs(dx) == r && abs(dz) == r && dy != 0 {
							continue
						}
						if ch.GetBlock(x+dx, top+dy, z+dz) == world.StateAir {
							ch.SetBlock(x+dx, top+dy, z+dz, stateOakLeaves)
						}
					}
				}
			}
			ch.SetBlock(x, top+1, z, stateOakLeaves)
		}
	}
}

func abs(v int32) int32 {
	if v < 0 {
		return -v
	}
	return v
}

// 777 基准矿石状态（官方 reports）。
const (
	stateDeepslate    uint32 = 33774
	stateCoalOre      uint32 = 136
	stateIronOre      uint32 = 134
	stateCopperOre    uint32 = 31147
	stateGoldOre      uint32 = 132
	stateDiamondOre   uint32 = 6863
	stateEmeraldOre   uint32 = 11315
	stateLapisOre     uint32 = 662
	stateRedstoneOre  uint32 = 8552
	stateDSCoalOre    uint32 = 137
	stateDSIronOre    uint32 = 135
	stateDSCopperOre  uint32 = 31148
	stateDSGoldOre    uint32 = 133
	stateDSDiamondOre uint32 = 6864
	stateDSEmerald    uint32 = 11316
	stateDSLapisOre   uint32 = 663
	stateDSRedstone   uint32 = 8554
)

// oreVein 矿脉定义：矿种（表层/深层）、生成高度带（相对 min_y）、尝试密度与脉大小。
type oreVein struct {
	normal, deep uint32 // 石层/深板岩层状态
	yLo, yHi     int32  // 高度带（相对 min_y；overworld -64..320 → 0..384）
	tries        int    // 每区块尝试次数
	size         int    // 脉内方块数
}

// 原版近似分布（公开数值的简化：频次×高度带）。
var oreVeins = []oreVein{
	{stateCoalOre, stateDSCoalOre, 32, 152, 14, 12},
	{stateIronOre, stateDSIronOre, 8, 120, 12, 8},
	{stateCopperOre, stateDSCopperOre, 8, 100, 8, 9},
	{stateGoldOre, stateDSGoldOre, 4, 48, 4, 8},
	{stateRedstoneOre, stateDSRedstone, 4, 24, 6, 8},
	{stateDiamondOre, stateDSDiamondOre, 4, 24, 3, 7},
	{stateLapisOre, stateDSLapisOre, 4, 40, 2, 6},
	{stateEmeraldOre, stateDSEmerald, 60, 120, 1, 3},
}

// deepslateY 深板岩起始相对高度（绝对 y≈0 → 相对 64）。
const deepslateY = 60

// plantOres 在石层里撒矿脉（确定性：seed+区块坐标）。
func (g *NoiseGenerator) plantOres(ch *world.Chunk) {
	for i, v := range oreVeins {
		for try := 0; try < v.tries; try++ {
			// 每次尝试一个随机起点（区块坐标 + 矿种 + 尝试序号派生）
			h1 := int32(hash3(int64(ch.CX), int64(ch.CZ), int64(i*1000+try), g.Seed+77))
			h2 := int32(hash3(int64(ch.CZ), int64(ch.CX), int64(try*31+i), g.Seed+991))
			h3 := int32(hash3(int64(ch.CX)*31, int64(ch.CZ)+int64(try), int64(i), g.Seed+13))
			x, z := h1&15, h2&15
			if x < 0 {
				x = -x
			}
			if z < 0 {
				z = -z
			}
			span := v.yHi - v.yLo
			y := v.yLo + h3%span
			if y < 0 {
				y = -y % span
			}
			g.growVein(ch, x, y, z, v, try)
		}
	}
}

// growVein 从种子点随机游走生成矿脉（替换石/深板岩）。
func (g *NoiseGenerator) growVein(ch *world.Chunk, x, y, z int32, v oreVein, salt int) {
	ore := v.normal
	if y < deepslateY {
		ore = v.deep
	}
	for n := 0; n < v.size; n++ {
		if x >= 0 && x < 16 && z >= 0 && z < 16 && y > 1 && y < world.WorldHeight-1 {
			cur := ch.GetBlock(x, y, z)
			if cur == world.StateStone || cur == stateDeepslate {
				ch.SetBlock(x, y, z, ore)
			}
		}
		// 随机游走（确定性）
		d := hash3(int64(x)+int64(salt), int64(y), int64(z), g.Seed+int64(n))
		switch d % 3 {
		case 0:
			x += int32(d%3) - 1
		case 1:
			y += int32((d>>8)%3) - 1
		default:
			z += int32((d>>16)%3) - 1
		}
		if x < 0 {
			x = 0
		}
		if x > 15 {
			x = 15
		}
		if z < 0 {
			z = 0
		}
		if z > 15 {
			z = 15
		}
		if y < 1 {
			y = 1
		}
	}
}

// hash3 组合三坐标与种子的确定性哈希（非密码学，均匀性够用）。
func hash3(a, b, c, seed int64) int64 {
	h := seed
	h = h*6364136223846793005 + a*1442695040888963407
	h ^= h >> 33
	h = h*6364136223846793005 + b*1442695040888963407
	h ^= h >> 33
	h = h*6364136223846793005 + c*1442695040888963407
	h ^= h >> 33
	if h < 0 {
		h = -h
	}
	return h
}
