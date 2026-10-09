package world

import (
	"bytes"
	"testing"

	"mcserver/internal/assets"
)

// TestFlatChunkMatchesVanillaTemplate 用破解出的区块格式自行编码平坦世界，
// 与官方 26.1 服务端下发的区块包体逐字节比对——地形生成正确性的最强证据。
func TestFlatChunkMatchesVanillaTemplate(t *testing.T) {
	ch := NewChunk(0, 0, 0)
	FlatGenerator{}.Generate(ch)
	got := ch.EncodeBody(StyleFor(775))
	want := assets.ChunkTemplate()
	if !bytes.Equal(got, want) {
		i := 0
		for i < len(got) && i < len(want) && got[i] == want[i] {
			i++
		}
		t.Fatalf("平坦区块编码与官方模板不符：长度 %d vs %d，首个差异在偏移 %d\n got …%x\nwant …%x",
			len(got), len(want), i, got[clamp(i-8, len(got)):clamp(i+16, len(got))], want[clamp(i-8, len(want)):clamp(i+16, len(want))])
	}
}

// TestNoiseChunkStructure 生成噪声地形并校验包体结构完整。
func TestNoiseChunkStructure(t *testing.T) {
	g := DefaultNoise(20261010)
	ch := NewChunk(3, -7, 0)
	g.Generate(ch)
	body := ch.EncodeBody(StyleFor(775))

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
	if h < 5 || h > WorldHeight-80 {
		t.Fatalf("地形高度异常 %d", h)
	}
	if s := ch.GetBlock(8, h, 8); s != StateGrassBlock && s != StateSand {
		t.Errorf("地表方块 = %d", s)
	}
	if s := ch.GetBlock(8, h+1, 8); s != StateAir && s != StateWater {
		t.Errorf("地表上方 = %d", s)
	}
}

// TestNoiseDeterministic 同一种子两次生成结果一致。
func TestNoiseDeterministic(t *testing.T) {
	g := DefaultNoise(42)
	a := NewChunk(1, 2, 0)
	b := NewChunk(1, 2, 0)
	g.Generate(a)
	g.Generate(b)
	if !bytes.Equal(a.EncodeBody(StyleFor(775)), b.EncodeBody(StyleFor(775))) {
		t.Error("同种子生成结果不一致")
	}
}

// TestFlatChunkMatchesVanilla774 1.21.11 风格的平坦区块与官方 774 抓包逐字节比对。
func TestFlatChunkMatchesVanilla774(t *testing.T) {
	ch := NewChunk(0, 0, 0)
	FlatGenerator{}.Generate(ch)
	got := ch.EncodeBody(StyleFor(774))
	want := assets.ChunkTemplate774()
	if !bytes.Equal(got, want) {
		i := 0
		for i < len(got) && i < len(want) && got[i] == want[i] {
			i++
		}
		t.Fatalf("774 平坦区块编码与官方不符：长度 %d vs %d，首差异@%d got…%x want…%x",
			len(got), len(want), i, got[clamp(i-8, len(got)):clamp(i+16, len(got))], want[clamp(i-8, len(want)):clamp(i+16, len(want))])
	}
}

func TestPackValues(t *testing.T) {
	// 9 位打包：值 4 连续 256 个 → 37 long，首个与官方模板一致
	vals := make([]uint32, 256)
	for i := range vals {
		vals[i] = 4
	}
	longs := packValues(vals, 9)
	if len(longs) != 37 {
		t.Fatalf("256×9bit 应为 37 long，得到 %d", len(longs))
	}
	if longs[0] != 0x0100804020100804 {
		t.Errorf("首个 long = %016x，期望 0100804020100804", longs[0])
	}
	// 4 位打包：[1,2] → 0x21（低半字节在前）
	if got := packValues([]uint32{1, 2}, 4); len(got) != 1 || got[0] != 0x21 {
		t.Errorf("4位打包 = %x", got)
	}
}

// ---- 测试辅助 ----

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
