package game

import (
	"testing"

	"mcserver/internal/assets"
	"mcserver/internal/entity"
	"mcserver/internal/packet"
	"mcserver/internal/protocol"
	"mcserver/internal/version"
	"mcserver/internal/world"
)

// fakeSender 记录主循环发出的包，供断言。
type fakeSender struct {
	sent []struct {
		name string
		body []byte
	}
}

func (f *fakeSender) SendPlay(name string, build func(w *protocol.Writer)) error {
	w := protocol.NewWriter()
	build(w)
	f.sent = append(f.sent, struct {
		name string
		body []byte
	}{name, w.Bytes()})
	return nil
}

func (f *fakeSender) SendRawPlay(name string, body []byte) error {
	f.sent = append(f.sent, struct {
		name string
		body []byte
	}{name, body})
	return nil
}

func (f *fakeSender) Addr() string { return "fake" }

func (f *fakeSender) names() []string {
	out := make([]string, len(f.sent))
	for i, s := range f.sent {
		out[i] = s.name
	}
	return out
}

func (f *fakeSender) find(name string) []byte {
	for _, s := range f.sent {
		if s.name == name {
			return s.body
		}
	}
	return nil
}

// newTestGame 构造不启动主循环的 Game（主循环由 New 启动，测试直接调 handler）。
func newTestGame(mode packet.GameMode) (*Game, *entity.Player, *fakeSender) {
	g := &Game{
		Cfg:           Config{GameMode: mode, ViewDistance: 3},
		Suite:         version.SuiteFor(777),
		Bus:           NewEventBus(),
		players:       map[int32]*entity.Player{},
		world:         world.NewManager(world.VoidGenerator{}),
		itemBlocks:    map[int32]map[int32]uint32{777: assets.ItemBlocks(777)},
		entityTypes:   map[int32]map[string]int32{777: assets.EntityTypes(777)},
		blockDrops:    map[int32]map[uint32]int32{777: assets.BlockDrops(777)},
		mineable:      map[int32]map[uint32]uint8{777: assets.MineableMask(777)},
		pickaxes:      map[int32]map[int32]uint8{777: assets.PickaxeBits(777)},
		items:         map[int32]*itemEntity{},
		mobs:          map[int32]*mob{},
		itemNames:     map[int32]map[int32]string{},
		pendingBlocks: map[[3]int32]uint32{},
	}
	fs := &fakeSender{}
	p := &entity.Player{
		EntityID: 1, Name: "Tester",
		X: 8.5, Y: -59, Z: 8.5,
		Send:       fs,
		SentChunks: map[[2]int32]bool{{0, 0}: true},
	}
	p.Health, p.Food, p.Saturation = 20, 20, 5
	g.players[1] = p
	return g, p, fs
}

// TestDigBreaksBlockAndAcks 创造模式挖掘：世界置空、广播 block_update、回 ack、发破坏粒子。
func TestDigBreaksBlockAndAcks(t *testing.T) {
	g, p, fs := newTestGame(packet.ModeCreative)
	g.world.SetBlock(8, -60, 8, world.StateStone)

	var evs []BlockDig
	g.Bus.On(BlockDig{}, func(e Event) { evs = append(evs, e.(BlockDig)) })
	g.handleDig(&blockDigEvent{p: p, act: packet.PlayerAction{Status: packet.DigStart, X: 8, Y: -60, Z: 8, Face: 1, Sequence: 42}})

	if got := g.world.GetBlock(8, -60, 8); got != world.StateAir {
		t.Fatalf("挖掘后方块应为空气，实际 %d", got)
	}
	if b := fs.find("minecraft:block_update"); b == nil {
		t.Fatal("未发送 block_update")
	} else if want := []byte{0x00, 0x00, 0x02, 0x00, 0x00, 0x00, 0x8f, 0xc4, 0x00}; string(b) != string(want) {
		t.Errorf("block_update 字节不符: %x", b)
	}
	if b := fs.find("minecraft:block_changed_ack"); b == nil {
		t.Fatal("未发送 block_changed_ack")
	} else if len(b) != 1 || b[0] != 42 {
		t.Errorf("ack 序列号不符: %x", b)
	}
	if fs.find("minecraft:level_event") == nil {
		t.Error("未发送破坏粒子 level_event")
	}
	if len(evs) != 1 || !evs[0].Broke || evs[0].Sequence != 42 || evs[0].OldState != world.StateStone {
		t.Errorf("BlockDig 事件不符: %+v", evs)
	}
}

// TestDigSurvivalTiming 生存模式：start 不破坏，finish 才破坏（时长校验后做）。
func TestDigSurvivalTiming(t *testing.T) {
	g, p, fs := newTestGame(packet.ModeSurvival)
	g.world.SetBlock(8, -60, 8, world.StateStone)

	g.handleDig(&blockDigEvent{p: p, act: packet.PlayerAction{Status: packet.DigStart, X: 8, Y: -60, Z: 8, Sequence: 1}})
	if got := g.world.GetBlock(8, -60, 8); got != world.StateStone {
		t.Fatal("生存模式 start 不应破坏方块")
	}
	if len(fs.sent) != 0 {
		t.Fatalf("生存模式 start 不应发包，实际 %v", fs.names())
	}
	g.handleDig(&blockDigEvent{p: p, act: packet.PlayerAction{Status: packet.DigFinish, X: 8, Y: -60, Z: 8, Sequence: 2}})
	if got := g.world.GetBlock(8, -60, 8); got != world.StateAir {
		t.Fatal("生存模式 finish 应破坏方块")
	}
	if fs.find("minecraft:block_changed_ack") == nil {
		t.Error("finish 后应回 ack")
	}
}

// TestDigCancel 取消挖掘不应破坏方块。
func TestDigCancel(t *testing.T) {
	g, p, fs := newTestGame(packet.ModeSurvival)
	g.world.SetBlock(8, -60, 8, world.StateStone)
	g.handleDig(&blockDigEvent{p: p, act: packet.PlayerAction{Status: packet.DigCancel, X: 8, Y: -60, Z: 8, Sequence: 3}})
	if got := g.world.GetBlock(8, -60, 8); got != world.StateStone {
		t.Fatal("取消不应破坏方块")
	}
	if len(fs.sent) != 0 {
		t.Errorf("取消不应发包: %v", fs.names())
	}
}

// TestPlaceWithBlockItem 手持石头放置：目标为点击面的外侧，空气可替换。
func TestPlaceWithBlockItem(t *testing.T) {
	g, p, fs := newTestGame(packet.ModeCreative)
	g.world.SetBlock(8, -61, 8, world.StateStone) // 脚下垫一块石头作点击目标
	p.Slots[36] = entity.Item{ID: 1, Count: 1}    // 快捷栏 0 = 物品 1（石头）

	var evs []BlockPlace
	g.Bus.On(BlockPlace{}, func(e Event) { evs = append(evs, e.(BlockPlace)) })
	g.handlePlace(&blockPlaceEvent{p: p, use: packet.UseItemOn{
		Hand: 0, X: 8, Y: -61, Z: 8, Face: 1, // 点击石头顶面 → 放到其上方
		CursorX: 0.5, CursorY: 1.0, CursorZ: 0.5, Sequence: 7,
	}})

	if got := g.world.GetBlock(8, -60, 8); got != 1 {
		t.Fatalf("放置后应为石头状态 1，实际 %d", got)
	}
	if b := fs.find("minecraft:block_update"); b == nil {
		t.Fatal("未广播 block_update")
	}
	if b := fs.find("minecraft:block_changed_ack"); b == nil || b[0] != 7 {
		t.Errorf("ack 序列号不符: %x", b)
	}
	if len(evs) != 1 || evs[0].State != 1 || evs[0].Y != -60 || evs[0].AgainstY != -61 {
		t.Errorf("BlockPlace 事件不符: %+v", evs)
	}
}

// TestPlaceEmptyHand 空手与目标被占：不放置，但回滚对（两处 block_update + ack）。
func TestPlaceEmptyHand(t *testing.T) {
	g, p, fs := newTestGame(packet.ModeCreative)
	g.world.SetBlock(8, -61, 8, world.StateStone)

	g.handlePlace(&blockPlaceEvent{p: p, use: packet.UseItemOn{X: 8, Y: -61, Z: 8, Face: 1, Sequence: 1}})
	if fs.find("minecraft:block_changed_ack") == nil {
		t.Error("空手 use_item_on 也应回 ack（官方行为）")
	}
	n := 0
	for _, s := range fs.sent {
		if s.name == "minecraft:block_update" {
			n++
		}
	}
	if n != 2 { // 点击位 + 目标位的权威状态回显
		t.Errorf("空手应回回滚对（2 个 block_update），实际 %d", n)
	}
	if got := g.world.GetBlock(8, -60, 8); got != world.StateAir {
		t.Error("空手不应放置")
	}

	fs.sent = nil
	p.Slots[36] = entity.Item{ID: 1, Count: 1}
	g.world.SetBlock(8, -60, 8, world.StateDirt) // 目标已被占
	g.handlePlace(&blockPlaceEvent{p: p, use: packet.UseItemOn{X: 8, Y: -61, Z: 8, Face: 1, Sequence: 2}})
	if got := g.world.GetBlock(8, -60, 8); got != world.StateDirt {
		t.Error("目标被占时不应覆盖放置")
	}
	if fs.find("minecraft:block_changed_ack") == nil {
		t.Error("目标被占也应回滚 + ack")
	}
}

// TestPlaceNonBlockItem 非方块物品（不在映射表中的 ID）：回滚对，不放置。
func TestPlaceNonBlockItem(t *testing.T) {
	g, p, fs := newTestGame(packet.ModeCreative)
	g.world.SetBlock(8, -61, 8, world.StateStone)
	p.Slots[36] = entity.Item{ID: -1, Count: 1}
	g.handlePlace(&blockPlaceEvent{p: p, use: packet.UseItemOn{X: 8, Y: -61, Z: 8, Face: 1, Sequence: 1}})
	if got := g.world.GetBlock(8, -60, 8); got != world.StateAir {
		t.Error("非方块物品不应放置")
	}
	if fs.find("minecraft:block_changed_ack") == nil {
		t.Error("非方块物品也应回滚 + ack")
	}
}

// TestCreativeSlotUpdatesHotbar 创造槽位与手持槽位事件更新物品栏。
func TestCreativeSlotUpdatesHotbar(t *testing.T) {
	g, p, _ := newTestGame(packet.ModeCreative)
	g.handleCreativeSlot(&playerCreativeSlotEvent{p: p, cs: packet.CreativeSlot{Slot: 37, ItemID: 1, Count: 64, HasItem: true}})
	if p.Slots[37].ID != 1 || p.Slots[37].Count != 64 {
		t.Errorf("槽位 37 应直接写入物品栏: %+v", p.Slots[37])
	}
	g.handleHeldSlot(&playerHeldSlotEvent{p: p, slot: 1})
	if p.HeldSlot != 1 {
		t.Errorf("HeldSlot = %d, want 1", p.HeldSlot)
	}
	g.handleCreativeSlot(&playerCreativeSlotEvent{p: p, cs: packet.CreativeSlot{Slot: 37}})
	if p.Slots[37].ID != 0 {
		t.Error("空物品应清空槽位")
	}
	g.handleHeldSlot(&playerHeldSlotEvent{p: p, slot: 9}) // 越界忽略
	if p.HeldSlot != 1 {
		t.Error("越界槽位应被忽略")
	}
}

// TestFlushGroupsSectionUpdates 同节两个变更应合并为 section_blocks_update。
func TestFlushGroupsSectionUpdates(t *testing.T) {
	g, _, fs := newTestGame(packet.ModeCreative)
	g.recordBlockChange(4, -60, 4, 0)
	g.recordBlockChange(5, -60, 5, 0)
	g.flushBlockUpdates()
	b := fs.find("minecraft:section_blocks_update")
	if b == nil {
		t.Fatal("同节两个变更应使用 section_blocks_update")
	}
	r := protocol.NewReader(b)
	secPos := r.Int64()
	// 节 (0,-4,0)：世界坐标 y=-60 在节 -4，编码为 (0<<42)|(0xFFFFC)|(0<<20) = 0xFFFFC
	if secPos != 0xFFFFC {
		t.Errorf("节坐标 long = %d, want %d", secPos, 0xFFFFC)
	}
	if n := r.VarInt(); n != 2 {
		t.Fatalf("条目数 = %d, want 2", n)
	}
	e1, e2 := r.VarLong(), r.VarLong()
	// 相对维度底部 y=(-60+64)&15=4：(lx,ly,lz)=(4,4,4) → 4<<8|4<<4|4；(5,4,5) → 5<<8|5<<4|4
	got := map[int64]bool{e1: true, e2: true}
	if !got[0x444] || !got[0x554] {
		t.Errorf("条目编码不符: %x %x", e1, e2)
	}
	if r.Err != nil || r.Remaining() != 0 {
		t.Errorf("包体未消费完: err=%v remaining=%d", r.Err, r.Remaining())
	}
}

// TestDigBeyondRange 超出触达范围的挖掘被忽略（防误改远处）。
func TestDigBeyondRange(t *testing.T) {
	g, p, fs := newTestGame(packet.ModeCreative)
	g.world.SetBlock(100, -60, 100, world.StateStone)
	g.handleDig(&blockDigEvent{p: p, act: packet.PlayerAction{Status: packet.DigStart, X: 100, Y: -60, Z: 100, Sequence: 1}})
	if got := g.world.GetBlock(100, -60, 100); got != world.StateStone {
		t.Fatal("超范围挖掘不应生效")
	}
	if len(fs.sent) != 0 {
		t.Errorf("超范围不应发包: %v", fs.names())
	}
}

// TestItemBlocksAsset 数据表资产可加载且含常见方块。
func TestItemBlocksAsset(t *testing.T) {
	m := assets.ItemBlocks(777)
	if m == nil {
		t.Fatal("777 物品表缺失")
	}
	if m[1] != 1 { // 石头物品 → 石头默认状态
		t.Errorf("物品 1 应映射状态 1，实际 %d", m[1])
	}
	if assets.ItemBlocks(775) != nil {
		t.Log("775 目前无官方报告数据表（放置不可用，挖掘不受影响）")
	}
}
