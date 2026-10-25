package terrain

import (
	"testing"

	"mcserver/internal/world"
)

func TestOresGenerated(t *testing.T) {
	g := DefaultNoise(20261010)
	counts := map[uint32]int{}
	for cx := int32(-2); cx <= 2; cx++ {
		for cz := int32(-2); cz <= 2; cz++ {
			ch := world.NewChunk(cx, cz, 0)
			g.Generate(ch)
			for y := int32(1); y < 120; y++ {
				for x := int32(0); x < 16; x++ {
					for z := int32(0); z < 16; z++ {
						s := ch.GetBlock(x, y, z)
						if s != world.StateStone && s != world.StateAir && s != world.StateDirt &&
							s != world.StateGrassBlock && s != world.StateSand && s != world.StateBedrock &&
							s != world.StateWater && s != stateDeepslate {
							counts[s]++
						}
					}
				}
			}
		}
	}
	total := 0
	for _, n := range counts {
		total += n
	}
	if total < 100 {
		t.Fatalf("矿石生成过少: %d 格（%v）", total, counts)
	}
	// 深层应有深板岩
	ch := world.NewChunk(0, 0, 0)
	g.Generate(ch)
	if ch.GetBlock(8, 20, 8) != stateDeepslate {
		t.Fatalf("深层应为深板岩: %d", ch.GetBlock(8, 20, 8))
	}
}

// TestBiomesDecorated 群系装饰：多种群系在同一大区域内出现且各带特征方块。
func TestBiomesDecorated(t *testing.T) {
	g := DefaultNoise(20261010)
	found := map[int]bool{}
	features := map[uint32]bool{}
	for cx := int32(-180); cx <= 180; cx += 12 {
		for cz := int32(-180); cz <= 180; cz += 12 {
			ch := world.NewChunk(cx, cz, 0)
			g.Generate(ch)
			for z := int32(0); z < 16; z += 3 {
				for x := int32(0); x < 16; x += 3 {
					wx, wz := cx*16+x, cz*16+z
					biome := g.BiomeAt(wx, wz)
					found[biome] = true
					h := g.HeightAt(wx, wz)
					if h <= g.SeaLevel+1 {
						continue
					}
					top := ch.GetBlock(x, h+1, z)
					switch biome {
					case 1:
						if top == stateDeadBush || top == stateCactus {
							features[top] = true
						}
					case 2:
						if top == stateSnowLayer {
							features[top] = true
						}
					case 3, 4:
						if top == stateShortGrass {
							features[stateShortGrass] = true
						}
					}
				}
			}
		}
	}
	if !found[0] || !found[1] || !found[2] {
		t.Fatalf("应出现多种群系: %v", found)
	}
	if len(features) < 2 {
		t.Fatalf("群系特征方块应出现: %v", features)
	}
}
