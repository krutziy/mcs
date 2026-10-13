package game

import (
	"os"
	"testing"

	"mcserver/internal/assets"
	"mcserver/internal/entity"
	"mcserver/internal/packet"
	"mcserver/internal/protocol"
	"mcserver/internal/world"
)

// TestDigDropsSurvival 生存挖掘：泥土掉泥土；石头空手不掉、持镐掉圆石。
func TestDigDropsSurvival(t *testing.T) {
	g, p, _ := newTestGame(packet.ModeSurvival)

	// 泥土（状态 10 → 物品 55 泥土）
	g.world.SetBlock(8, -60, 8, world.StateDirt)
	g.handleDig(&blockDigEvent{p: p, act: packet.PlayerAction{Status: packet.DigFinish, X: 8, Y: -60, Z: 8, Sequence: 1}})
	if len(g.items) != 1 {
		t.Fatalf("泥土挖掘应掉落 1 个物品实体，实际 %d", len(g.items))
	}
	for _, v := range g.items {
		if v.item.ID != 55 {
			t.Errorf("泥土掉落应为泥土物品 55，实际 %d", v.item.ID)
		}
	}

	// 石头：空手不掉（需镐）
	for eid := range g.items {
		delete(g.items, eid)
	}
	g.world.SetBlock(8, -60, 8, world.StateStone)
	g.handleDig(&blockDigEvent{p: p, act: packet.PlayerAction{Status: packet.DigFinish, X: 8, Y: -60, Z: 8, Sequence: 2}})
	if len(g.items) != 0 {
		t.Error("空手挖石头不应掉落")
	}

	// 石头：木镐（材质位 0；石头掩码 63 = 任意镐可采）掉圆石（物品 62）
	g.world.SetBlock(8, -60, 8, world.StateStone)
	wooden, ok := findPickaxe(g, 0)
	if !ok {
		t.Skip("资产表无木镐")
	}
	p.Slots[36] = entity.Item{ID: wooden, Count: 1}
	g.handleDig(&blockDigEvent{p: p, act: packet.PlayerAction{Status: packet.DigFinish, X: 8, Y: -60, Z: 8, Sequence: 3}})
	if len(g.items) != 1 {
		t.Fatal("木镐挖石头应掉落")
	}
	for _, v := range g.items {
		if v.item.ID != 62 {
			t.Errorf("石头掉落应为圆石 62，实际 %d", v.item.ID)
		}
	}
}

// findPickaxe 在资产表中找指定材质位的镐物品 ID。
func findPickaxe(g *Game, bit uint8) (int32, bool) {
	for id, b := range g.pickaxes[777] {
		if b == bit {
			return id, true
		}
	}
	return 0, false
}

// TestItemPickup 拾取：玩家身边的掉落物自动入栏并广播 take_item_entity。
func TestItemPickup(t *testing.T) {
	g, p, fs := newTestGame(packet.ModeSurvival)
	g.world.SetBlock(8, -61, 8, world.StateStone)
	g.spawnItem(8.5, -60.5, 8.5, entity.Item{ID: 55, Count: 3})
	for i := 0; i < itemPickupWait+20; i++ {
		g.tickItems()
	}
	if len(g.items) != 0 {
		t.Fatal("玩家脚下的掉落物应被拾取")
	}
	if fs.find("minecraft:take_item_entity") == nil {
		t.Error("应广播 take_item_entity")
	}
	if fs.find("minecraft:container_set_content") == nil {
		t.Error("拾取后应同步物品栏")
	}
	if p.Slots[36].ID != 55 || p.Slots[36].Count != 3 {
		t.Errorf("物品应进快捷栏 0: %+v", p.Slots[36])
	}
}

// TestItemGravity 掉落物下落到地面即停（3×3 地板；远离玩家避免被拾取）。
func TestItemGravity(t *testing.T) {
	g, _, _ := newTestGame(packet.ModeSurvival)
	for dx := int32(-1); dx <= 1; dx++ { // 物品会水平散开，地板要够宽
		for dz := int32(-1); dz <= 1; dz++ {
			g.world.SetBlock(100+dx, -61, 100+dz, world.StateStone)
		}
	}
	g.spawnItem(100.5, -58.5, 100.5, entity.Item{ID: 55, Count: 1})
	for i := 0; i < 40; i++ {
		g.tickItems()
	}
	ie := firstItem(g)
	if ie == nil {
		t.Fatal("掉落物不应消失")
	}
	if !(ie.y > -61.0 && ie.y < -59.5) {
		t.Errorf("掉落物应停在地面附近，y=%.2f", ie.y)
	}
	if ie.vy != 0 {
		t.Errorf("落地后垂直速度应为 0，vy=%.3f", ie.vy)
	}
}

func firstItem(g *Game) *itemEntity {
	for _, v := range g.items {
		return v
	}
	return nil
}

// TestSurvivalPlaceConsumesItem 生存放置消耗物品；物品耗尽后不能再放。
func TestSurvivalPlaceConsumesItem(t *testing.T) {
	g, p, _ := newTestGame(packet.ModeSurvival)
	g.world.SetBlock(8, -61, 8, world.StateStone)
	p.Slots[36] = entity.Item{ID: 1, Count: 2}

	g.handlePlace(&blockPlaceEvent{p: p, use: packet.UseItemOn{X: 8, Y: -61, Z: 8, Face: 1, Sequence: 1}})
	if p.Slots[36].Count != 1 {
		t.Fatalf("放置后应剩 1 个，实际 %d", p.Slots[36].Count)
	}
	// 把刚放的石头挖掉（空手不掉落），再放第二个
	g.handleDig(&blockDigEvent{p: p, act: packet.PlayerAction{Status: packet.DigFinish, X: 8, Y: -60, Z: 8, Sequence: 2}})
	for eid := range g.items {
		delete(g.items, eid)
	}
	g.handlePlace(&blockPlaceEvent{p: p, use: packet.UseItemOn{X: 8, Y: -61, Z: 8, Face: 1, Sequence: 3}})
	if p.Slots[36].ID != 0 {
		t.Error("最后一个物品放置后槽位应清空")
	}
	g.handleDig(&blockDigEvent{p: p, act: packet.PlayerAction{Status: packet.DigFinish, X: 8, Y: -60, Z: 8, Sequence: 4}})
	for eid := range g.items {
		delete(g.items, eid)
	}
	g.handlePlace(&blockPlaceEvent{p: p, use: packet.UseItemOn{X: 8, Y: -61, Z: 8, Face: 1, Sequence: 5}})
	if got := g.world.GetBlock(8, -60, 8); got != world.StateAir {
		t.Error("无物品时不应放置")
	}
}

// TestContainerClickBasics 物品栏点击：左键拾取/放置、右键取半、数字键交换。
func TestContainerClickBasics(t *testing.T) {
	g, p, _ := newTestGame(packet.ModeSurvival)
	click := func(mode int32, slot int16, button int8) {
		g.handleContainerClick(&playerContainerClickEvent{p: p, click: packet.ContainerClick{Window: 0, Slot: slot, Button: button, Mode: mode}})
	}
	p.Slots[10] = entity.Item{ID: 55, Count: 40}

	click(packet.ClickModeClick, 10, 0) // 左键拾取
	if p.Cursor.ID != 55 || p.Cursor.Count != 40 || p.Slots[10].ID != 0 {
		t.Fatalf("拾取失败: cursor=%+v slot=%+v", p.Cursor, p.Slots[10])
	}
	click(packet.ClickModeClick, 12, 0) // 放到空位
	if p.Slots[12].ID != 55 || p.Cursor.ID != 0 {
		t.Fatalf("放置失败: slot12=%+v cursor=%+v", p.Slots[12], p.Cursor)
	}
	click(packet.ClickModeClick, 12, 1) // 右键取半
	if p.Cursor.Count != 20 || p.Slots[12].Count != 20 {
		t.Fatalf("取半失败: cursor=%+v slot=%+v", p.Cursor, p.Slots[12])
	}
	click(packet.ClickModeSwap, 12, 0) // 数字键 1 ↔ 快捷栏 0
	if p.Slots[36].ID != 55 || p.Slots[12].ID != 0 {
		t.Fatalf("数字键交换失败: hotbar=%+v slot12=%+v", p.Slots[36], p.Slots[12])
	}
}

// TestContainerSyncEmptyMatchesVanilla 空物品栏的 container_set_content
// 与官方抓包（.tools/cap263/060，window 0 state 1 46 空槽）逐字节一致。
func TestContainerSyncEmptyMatchesVanilla(t *testing.T) {
	slots := make([]packet.ItemSlot, 46)
	w := protocol.NewWriter()
	packet.WriteContainerSetContent(w, 0, 1, slots, packet.ItemSlot{})
	got := w.Bytes()
	want := make([]byte, 0, 50)
	want = append(want, 0x00, 0x01, 0x2e) // window 0, state 1, 46 槽
	for i := 0; i < 47; i++ {            // 46 空槽 + 空光标
		want = append(want, 0x00)
	}
	if string(got) != string(want) {
		t.Errorf("container_set_content 编码不符\n got  % x\n want % x", got, want)
	}
}

// TestWorldPersistence 世界持久化：改动→存档→新管理器读档→改动保留。
func TestWorldPersistence(t *testing.T) {
	dir, err := os.MkdirTemp("", "mcsw-test-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	m1 := world.NewManager(world.VoidGenerator{})
	m1.SetBlock(4, -60, 4, world.StateStone)
	if n, err := m1.Save(dir); err != nil || n != 1 {
		t.Fatalf("存档失败: n=%d err=%v", n, err)
	}
	m1.Chunk(9, 9) // 未修改的区块
	if n, _ := m1.Save(dir); n != 0 {
		t.Errorf("未修改区块不应产生存档，多写 %d 个", n)
	}

	m2 := world.NewManager(world.VoidGenerator{})
	if n, err := m2.Load(dir); err != nil || n != 1 {
		t.Fatalf("读档失败: n=%d err=%v", n, err)
	}
	if got := m2.GetBlock(4, -60, 4); got != world.StateStone {
		t.Fatalf("读档后方块应保留，实际 %d", got)
	}
	if m2.Chunk(0, 0).GetBlock(4, -60-world.MinY, 4) != world.StateStone {
		t.Fatal("Chunk() 不应覆盖存档内容")
	}
}

// TestPlayerPersistence 玩家状态持久化：物品栏与位置往返。
func TestPlayerPersistence(t *testing.T) {
	dir, err := os.MkdirTemp("", "mcsw-player-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	g, p, _ := newTestGame(packet.ModeSurvival)
	g.Cfg.SaveDir = dir
	p.X, p.Y, p.Z = 100.5, -50, -200.5
	p.Slots[36] = entity.Item{ID: 55, Count: 7}
	g.savePlayer(p)

	p2 := &entity.Player{Name: p.Name, Send: p.Send}
	g.loadPlayer(p2)
	if p2.Slots[36].ID != 55 || p2.Slots[36].Count != 7 {
		t.Errorf("物品栏未恢复: %+v", p2.Slots[36])
	}
	if p2.X != 100.5 || p2.Z != -200.5 {
		t.Errorf("位置未恢复: %+v", p2)
	}
}

// TestEntityTypePerVersion 玩家/物品实体类型按版本查表（26.3: 159/72）。
func TestEntityTypePerVersion(t *testing.T) {
	g, p, _ := newTestGame(packet.ModeCreative)
	if id := g.entityTypeID(p, "player"); id != 159 {
		t.Errorf("26.3 player 实体类型 = %d, want 159", id)
	}
	if id := g.entityTypeID(p, "item"); id != 72 {
		t.Errorf("26.3 item 实体类型 = %d, want 72", id)
	}
	if m := assets.EntityTypes(777); m["player"] != 159 {
		t.Error("assets 表不一致")
	}
}
