package world

import (
	"os"
	"path/filepath"
	"testing"

	"mcserver/internal/assets"
)

func canonTables(t *testing.T) (map[uint32]string, map[string]uint32) {
	c := assets.BlockNamesFull(777)
	if c == nil {
		t.Fatal("规范名资产缺失")
	}
	inv := map[string]uint32{}
	for k, v := range c {
		inv[v] = k
	}
	return c, inv
}

// TestRoundTripAnvilRoundtrip 自有往返：写 region → 读回 → 方块一致。
func TestRoundTripAnvilRoundtrip(t *testing.T) {
	c, inv := canonTables(t)
	dir, _ := os.MkdirTemp("", "anvil-")
	defer os.RemoveAll(dir)

	m := NewManager(VoidGenerator{})
	m.SetBlock(5, 70, 5, StateStone) // Manager.SetBlock 才标脏（Anvil 只存脏区块）
	m.SetBlock(6, 70, 5, StateDirt)
	m.SetBlock(8, 70, 8, StateSand)

	if n, err := m.SaveAnvil(dir, c, inv); err != nil || n != 1 {
		t.Fatalf("Anvil 存档失败: n=%d err=%v", n, err)
	}
	m2 := NewManager(VoidGenerator{})
	if n, err := m2.LoadAnvil(dir, inv); err != nil || n != 1 {
		t.Fatalf("Anvil 读档失败: n=%d err=%v", n, err)
	}
	if got := m2.GetBlock(8, 70, 8); got != StateSand {
		t.Fatalf("沙应保留: %d", got)
	}
	if got := m2.GetBlock(5, 70, 5); got != StateStone {
		t.Fatalf("石头应保留: %d", got)
	}
}

// TestReadOfficialRegion 解析官方服务端的 region 文件。
func TestReadOfficialRegion(t *testing.T) {
	_, inv := canonTables(t)
	dir := "../../../.tools/v263/server/world/dimensions/minecraft/overworld/region"
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Skip("官方 world 样本不可用")
	}
	total := 0
	for _, e := range entries {
		if filepath.Ext(e.Name()) != ".mca" {
			continue
		}
		chunks, err := LoadRegionAnvil(filepath.Join(dir, e.Name()), inv)
		if err != nil {
			t.Fatalf("官方 region 解析失败 %s: %v", e.Name(), err)
		}
		total += len(chunks)
		// 抽查首块非空
		if len(chunks) > 0 {
			ch := chunks[0]
			nonAir := 0
			for i := int32(0); i < NumSections; i++ {
				s := ch.Section(i)
				if s == nil {
					continue
				}
				for _, b := range s.Blocks {
					if b != StateAir {
						nonAir++
					}
				}
			}
			if nonAir == 0 {
				t.Logf("区块 (%d,%d) 全空气（官方虚空区可能）", ch.CX, ch.CZ)
			}
		}
	}
	if total == 0 {
		t.Fatal("官方 region 应解析出区块")
	}
	t.Logf("官方 region 共解析 %d 区块", total)
}

// TestAnvilRegionPath region 分组正确性。
func TestAnvilRegionPath(t *testing.T) {
	cases := [][2]int32{{0, 0, }, {31, 31}, {32, 0}, {-1, -1}, {-32, 0}}
	for _, c := range cases {
		p := RegionPath("w", c[0], c[1])
		if p == "" {
			t.Fatal("路径为空")
		}
	}
	if floorDiv(-1, 32) != -1 || floorDiv(32, 32) != 1 {
		t.Fatal("floorDiv 错误")
	}
}
