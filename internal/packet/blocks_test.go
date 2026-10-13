package packet

import (
	"testing"

	"mcserver/internal/protocol"
)

// 黄金字节按公开协议文档的编码公式独立计算（26.3 / 协议 777），
// 并经官方服务端抓包比对（见 .tools 录制资产与 README 流程）。

// TestBlockUpdateGolden 验证 block_update：位置 long（x26/z26/y12）+ VarInt 状态。
func TestBlockUpdateGolden(t *testing.T) {
	w := protocol.NewWriter()
	WriteBlockUpdate(w, 8, -60, 8, 1)
	want := mustHex(t, "0000020000008fc401")
	if got := w.Bytes(); string(got) != string(want) {
		t.Errorf("block_update 编码不符\n got  %x\n want %x", got, want)
	}
}

// TestSectionBlocksUpdateGolden 验证 section_blocks_update：
// 节坐标 long（x 高 22 位 / z 中 22 位 / y 低 20 位）+ VarInt 数量 + VarLong 数组
// （状态 << 12 | lx<<8 | lz<<4 | ly）。
func TestSectionBlocksUpdateGolden(t *testing.T) {
	w := protocol.NewWriter()
	WriteSectionBlocksUpdate(w, 0, -4, 0, []SectionEntry{
		{LocalX: 0, LocalY: 0, LocalZ: 0, State: 1},
		{LocalX: 3, LocalY: 1, LocalZ: 2, State: 8},
	})
	want := mustHex(t, "00000000000ffffc028020a18602")
	if got := w.Bytes(); string(got) != string(want) {
		t.Errorf("section_blocks_update 编码不符\n got  %x\n want %x", got, want)
	}

	// 负数节坐标的位组合（手工计算两例，防符号位处理错误）
	w2 := protocol.NewWriter()
	WriteSectionBlocksUpdate(w2, -1, 0, 2, []SectionEntry{{State: 0}})
	if got, want := w2.Bytes()[:8], mustHex(t, "fffffc0000200000"); string(got) != string(want) {
		t.Errorf("secpos(-1,0,2) = %x, want %x", got, want)
	}
	w3 := protocol.NewWriter()
	WriteSectionBlocksUpdate(w3, 1, -4, -1, []SectionEntry{{State: 0}})
	if got, want := w3.Bytes()[:8], mustHex(t, "000007fffffffffc"); string(got) != string(want) {
		t.Errorf("secpos(1,-4,-1) = %x, want %x", got, want)
	}
}

// TestBlockChangedAck 验证 ack 只有一个 VarInt 序列号。
func TestBlockChangedAck(t *testing.T) {
	w := protocol.NewWriter()
	WriteBlockChangedAck(w, 5)
	if got, want := w.Bytes(), []byte{0x05}; string(got) != string(want) {
		t.Errorf("ack = %x, want %x", got, want)
	}
}

// TestPlayerActionRoundTrip player_action 往返：VarInt 状态 + 位置 + 字节面 + VarInt 序列。
func TestPlayerActionRoundTrip(t *testing.T) {
	r := protocol.NewReader(mustHex(t, "020000020000008fc40107"))
	a := ReadPlayerAction(r)
	if r.Err != nil || r.Remaining() != 0 {
		t.Fatalf("解析不完整: err=%v remaining=%d", r.Err, r.Remaining())
	}
	if a.Status != 2 || a.X != 8 || a.Y != -60 || a.Z != 8 || a.Face != 1 || a.Sequence != 7 {
		t.Errorf("解析结果不符: %+v", a)
	}
}

// TestUseItemOnRoundTrip use_item_on 往返：手 + 位置 + VarInt 面 + 光标 + 布尔 + 序列。
func TestUseItemOnRoundTrip(t *testing.T) {
	r := protocol.NewReader(mustHex(t, "000000020000008fc4013f0000003f8000003f000000000003"))
	u := ReadUseItemOn(r)
	if r.Err != nil || r.Remaining() != 0 {
		t.Fatalf("解析不完整: err=%v remaining=%d", r.Err, r.Remaining())
	}
	if u.Hand != 0 || u.X != 8 || u.Y != -60 || u.Z != 8 || u.Face != 1 || u.Sequence != 3 {
		t.Errorf("解析结果不符: %+v", u)
	}
	if u.CursorX != 0.5 || u.CursorY != 1.0 || u.CursorZ != 0.5 || u.InsideBlock || u.WorldBorder {
		t.Errorf("光标/标志不符: %+v", u)
	}
}

// TestCreativeSlotParsing 创造模式槽位包：Short 槽位 + 无组件物品。
func TestCreativeSlotParsing(t *testing.T) {
	// slot=36(0x0024) + present(01) + itemID 1 + count 1 + 增补0 + 移除0
	r := protocol.NewReader(mustHex(t, "00240101010000"))
	cs := ReadCreativeSlot(r)
	if r.Err != nil || r.Remaining() != 0 {
		t.Fatalf("解析不完整: err=%v remaining=%d", r.Err, r.Remaining())
	}
	if cs.Slot != 36 || !cs.HasItem || cs.ItemID != 1 || cs.Count != 1 {
		t.Errorf("解析结果不符: %+v", cs)
	}
	// 空物品（清空槽位）
	r2 := protocol.NewReader(mustHex(t, "002400"))
	cs2 := ReadCreativeSlot(r2)
	if cs2.HasItem {
		t.Errorf("空槽位应为无物品: %+v", cs2)
	}
}

// TestLevelEventGolden 验证 level_event（破坏粒子事件 2001）。
func TestLevelEventGolden(t *testing.T) {
	w := protocol.NewWriter()
	WriteLevelEvent(w, 2001, 8, -60, 8, 9, false)
	want := mustHex(t, "000007d10000020000008fc40000000900")
	if got := w.Bytes(); string(got) != string(want) {
		t.Errorf("level_event 编码不符\n got  %x\n want %x", got, want)
	}
}

// TestFaceOffset 面法线方向。
func TestFaceOffset(t *testing.T) {
	cases := []struct {
		face            int32
		dx, dy, dz      int32
	}{
		{0, 0, -1, 0}, {1, 0, 1, 0}, {2, 0, 0, -1},
		{3, 0, 0, 1}, {4, -1, 0, 0}, {5, 1, 0, 0},
	}
	for _, c := range cases {
		dx, dy, dz := FaceOffset(c.face)
		if dx != c.dx || dy != c.dy || dz != c.dz {
			t.Errorf("FaceOffset(%d) = (%d,%d,%d), want (%d,%d,%d)", c.face, dx, dy, dz, c.dx, c.dy, c.dz)
		}
	}
}
