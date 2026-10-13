package world

import "testing"

// TestManagerChunkCache 验证区块只生成一次、同坐标返回同一实例。
func TestManagerChunkCache(t *testing.T) {
	m := NewManager(VoidGenerator{})
	a := m.Chunk(3, -7)
	b := m.Chunk(3, -7)
	if a != b {
		t.Fatal("同一坐标应返回缓存的同一区块实例")
	}
	if len(m.chunks) != 1 {
		t.Fatalf("应只缓存 1 个区块，实际 %d", len(m.chunks))
	}
	if m.ChunkIfLoaded(4, -7) != nil {
		t.Error("未生成区块 ChunkIfLoaded 应为 nil")
	}
}

// TestManagerWorldCoords 验证世界坐标（含负数/跨区块/上下界）的方块存取。
func TestManagerWorldCoords(t *testing.T) {
	m := NewManager(VoidGenerator{})
	m.SetBlock(0, -60, 0, StateStone)       // 区块 (0,0)
	m.SetBlock(-1, -59, -1, StateDirt)      // 区块 (-1,-1)
	m.SetBlock(300, 319, 300, StateGrassBlock) // 区块 (18,18)
	if got := m.GetBlock(0, -60, 0); got != StateStone {
		t.Errorf("(0,-60,0) = %d, want %d", got, StateStone)
	}
	if got := m.GetBlock(-1, -59, -1); got != StateDirt {
		t.Errorf("(-1,-59,-1) = %d, want %d", got, StateDirt)
	}
	if got := m.GetBlock(300, 319, 300); got != StateGrassBlock {
		t.Errorf("(300,319,300) = %d, want %d", got, StateGrassBlock)
	}
	// 未生成区块与越界坐标读作空气
	if got := m.GetBlock(1000, 0, 1000); got != StateAir {
		t.Errorf("未生成区块应读作空气，实际 %d", got)
	}
	if got := m.GetBlock(0, 320, 0); got != StateAir {
		t.Errorf("y 越界应读作空气，实际 %d", got)
	}
	m.SetBlock(0, 320, 0, StateStone) // 越界写入应被忽略
	if got := m.GetBlock(0, 320, 0); got != StateAir {
		t.Errorf("越界写入被忽略后仍应为空气，实际 %d", got)
	}
}

// TestManagerDigPersistAcrossRegenerate 挖掘后的区块再次取用时保持修改
// （服务端权威：区块不再重新生成）。
func TestManagerDigPersistAcrossRegenerate(t *testing.T) {
	m := NewManager(VoidGenerator{})
	m.SetBlock(5, -60, 5, StateStone)
	m.SetBlock(5, -60, 5, StateAir) // 挖掉
	if got := m.Chunk(0, 0).GetBlock(5, -60-MinY, 5); got != StateAir {
		t.Fatalf("挖掘后再次取区块应保持空气，实际 %d", got)
	}
}

// TestManagerSetBlockWithGenerator 验证 Chunk 触发生成后可在其上改写。
func TestManagerSetBlockWithGenerator(t *testing.T) {
	gen := func() Generator {
		return GeneratorFunc(func(ch *Chunk) {
			for z := int32(0); z < 16; z++ {
				for x := int32(0); x < 16; x++ {
					ch.SetBlock(x, 4, z, StateGrassBlock) // 相对 y=4 → 绝对 -60
				}
			}
		})
	}
	m := NewManager(gen())
	m.Chunk(0, 0) // 触发生成（GetBlock 不生成，只读已生成区块）
	if got := m.GetBlock(9, -60, 9); got != StateGrassBlock {
		t.Fatalf("生成器表面应为草方块，实际 %d", got)
	}
	m.SetBlock(9, -60, 9, StateAir)
	if got := m.GetBlock(9, -60, 9); got != StateAir {
		t.Fatalf("改写后应为空气，实际 %d", got)
	}
}
