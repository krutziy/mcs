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
