package terrain

import (
	"bytes"
	"testing"

	"mcserver/internal/assets"
	"mcserver/internal/world"
)

// 与官方 26.1 服务端下发的区块包体逐字节比对——地形生成正确性的最强证据。
func TestFlatChunkMatchesVanillaTemplate(t *testing.T) {
	// 光照升级为真实天光传播后，平坦世界的光照段不再复刻官方的
	// 省流量下发模式；此测试验证光照段之前的主体仍与官方逐字节一致。
	ch := world.NewChunk(0, 0, 0)
	FlatGenerator{}.Generate(ch)
	got := ch.EncodeBody(world.StyleFor(775))
	want := assets.ChunkTemplate()
	cut := 3148 // 官方模板的光照段数据起点（主体+首掩码一致前缀）
	if !bytes.Equal(got[:cut], want[:cut]) {
		i := 0
		for i < cut && got[i] == want[i] {
			i++
		}
		t.Fatalf("平坦区块主体与官方不符：首个差异在偏移 %d", i)
	}
}
// TestFlatChunkMatchesVanilla774 1.21.11 风格的平坦区块与官方 774 抓包逐字节比对。
func TestFlatChunkMatchesVanilla774(t *testing.T) {
	// 同上：只比对光照段之前的主体。
	ch := world.NewChunk(0, 0, 0)
	FlatGenerator{}.Generate(ch)
	got := ch.EncodeBody(world.StyleFor(774))
	want := assets.ChunkTemplate774()
	cut := 3100 // 774 官方模板光照段起点
	if !bytes.Equal(got[:cut], want[:cut]) {
		i := 0
		for i < cut && got[i] == want[i] {
			i++
		}
		t.Fatalf("774 平坦区块主体与官方不符：首个差异在偏移 %d", i)
	}
}
// TestNoiseChunkStructure 生成噪声地形并校验包体结构完整。
func TestNoiseChunkStructure(t *testing.T) {
	g := DefaultNoise(20261010)
	ch := world.NewChunk(3, -7, 0)
	g.Generate(ch)
	body := ch.EncodeBody(world.StyleFor(775))

	r := &br{data: body}
	if n := r.varint(); n != 3 {
		t.Fatalf("高度图条目 = %d", n)
	}
	for k := 0; k < 3; k++ {
		_ = r.varint()
		cnt := r.varint()
		if cnt != 37 {
			t.Errorf("高度图 long 数 = %d，期望 37", cnt)
		}
		r.skip(int(cnt) * 8)
	}
	dl := r.varint()
	if dl <= 0 || int(dl) > r.remaining() {
		t.Fatalf("区块数据长度异常 %d（剩 %d）", dl, r.remaining())
	}
	r.skip(int(dl))
	if r.varint() != 0 { // 方块实体
		t.Error("方块实体应为空")
	}
	if r.remaining() < 10 {
		t.Fatalf("光照段缺失，剩余 %d", r.remaining())
	}

	// 地形合理性：地表应为草/沙，其上为空
	h := g.HeightAt(3*16+8, -7*16+8)
	if h < 5 || h > world.WorldHeight-80 {
		t.Fatalf("地形高度异常 %d", h)
	}
	if s := ch.GetBlock(8, h, 8); s != world.StateGrassBlock && s != world.StateSand {
		t.Errorf("地表方块 = %d", s)
	}
	if s := ch.GetBlock(8, h+1, 8); s != world.StateAir && s != world.StateWater && s != stateOakLog && s != stateOakLeaves {
		t.Errorf("地表上方 = %d", s)
	}
}
// TestNoiseDeterministic 同一种子两次生成结果一致。
func TestNoiseDeterministic(t *testing.T) {
	g := DefaultNoise(42)
	a := world.NewChunk(1, 2, 0)
	b := world.NewChunk(1, 2, 0)
	g.Generate(a)
	g.Generate(b)
	if !bytes.Equal(a.EncodeBody(world.StyleFor(775)), b.EncodeBody(world.StyleFor(775))) {
		t.Error("同种子生成结果不一致")
	}
}

// TestRegistered 确认插件向 world 注册了 flat/noise。
func TestRegistered(t *testing.T) {
	for _, name := range []string{"flat", "noise"} {
		g := world.NewGenerator(name, 1)
		if _, ok := g.(world.VoidGenerator); ok {
			t.Errorf("生成器 %q 未注册", name)
		}
	}
}

// ---- 测试辅助（与世界包同构的极简读取器） ----

type br struct {
	data []byte
	off  int
}

func (b *br) varint() int32 {
	var result uint32
	for i := 0; b.off < len(b.data); i++ {
		x := b.data[b.off]
		b.off++
		result |= uint32(x&0x7f) << (7 * i)
		if x&0x80 == 0 {
			return int32(result)
		}
	}
	return -1
}

func (b *br) skip(n int) { b.off += n }

func (b *br) remaining() int { return len(b.data) - b.off }

func clamp(v, hi int) int {
	if v < 0 {
		v = 0
	}
	if v > hi {
		v = hi
	}
	return v
}
