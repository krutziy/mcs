package world

import (
	"testing"
)

// TestFlatChunkMatchesVanillaTemplate 用破解出的区块格式自行编码平坦世界，




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

// TestVoidChunkStructure 虚空世界（空壳默认）区块结构自校验。
func TestVoidChunkStructure(t *testing.T) {
	ch := NewChunk(0, 0, 0)
	VoidGenerator{}.Generate(ch)
	for _, proto := range []int32{774, 775, 777} {
		body := ch.EncodeBody(StyleFor(proto))
		r := &br{data: body}
		if n := r.varint(); n != 3 {
			t.Fatalf("proto%d: 高度图条目 = %d", proto, n)
		}
		for k := 0; k < 3; k++ {
			_ = r.varint()
			cnt := r.varint()
			if cnt != 37 {
				t.Errorf("proto%d: 高度图 long 数 = %d", proto, cnt)
			}
			r.skip(int(cnt) * 8)
		}
		dl := r.varint()
		if dl <= 0 {
			t.Fatalf("proto%d: 空区块数据异常", proto)
		}
		r.skip(int(dl))
		if r.varint() != 0 { // 方块实体
			t.Error("方块实体应为空")
		}
		if r.remaining() < 10 {
			t.Fatalf("proto%d: 光照段缺失", proto)
		}
		if ch.GetBlock(8, 4, 8) != StateAir {
			t.Error("虚空世界不应有方块")
		}
	}
}

// TestGeneratorRegistry 注册表行为。
func TestGeneratorRegistry(t *testing.T) {
	if _, ok := NewGenerator("void", 0).(VoidGenerator); !ok {
		t.Error("void 应返回虚空生成器")
	}
	if _, ok := NewGenerator("不存在的类型", 0).(VoidGenerator); !ok {
		t.Error("未注册类型应回退为虚空")
	}
}
