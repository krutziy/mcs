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
					ch.SetBlock(x, y, z, world.StateStone)
				}
			}
			for y := h + 1; y <= g.SeaLevel; y++ {
				ch.SetBlock(x, y, z, world.StateWater)
			}
		}
	}
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
