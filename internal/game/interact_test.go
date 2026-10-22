package game

import (
	"os"
	"testing"

	"mcserver/internal/assets"
	"mcserver/internal/entity"
	"mcserver/internal/packet"
	"mcserver/internal/world"
)

// newInteractGame 交互类测试 Game（加载床/门表）。
func newInteractGame(t *testing.T) (*Game, *entity.Player, *fakeSender) {
	t.Helper()
	g, p, fs := newTestGame(packet.ModeSurvival)
	g.loadInteractTables()
	g.smelting = assets.Smelting(777)
	if len(g.bedByState) == 0 || len(g.doorByState) == 0 {
		t.Fatal("床/门状态资产缺失")
	}
	if g.itemNames[777] == nil {
		g.itemNames[777] = map[int32]string{}
	}
	for name, id := range assets.ItemIDs(777) {
		g.itemNames[777][id] = name
	}
	return g, p, fs
}

// TestChestRoundtrip 箱子：开箱存取 + 持久化 + 挖掉掉落。
func TestChestRoundtrip(t *testing.T) {
	g, p, fs := newInteractGame(t)
	ids := assets.ItemIDs(777)
	g.world.SetBlock(8, -61, 8, chestBlockState)

	if !g.tryOpenChest(p, packet.UseItemOn{X: 8, Y: -61, Z: 8, Face: 1}) {
		t.Fatal("右键箱子应打开")
	}
	if fs.find("minecraft:open_screen") == nil {
		t.Fatal("应发送 open_screen")
	}
	// 放一个石头进箱子第 0 格
	g.handleContainerClick(&playerContainerClickEvent{p: p,
		click: packet.ContainerClick{Window: 3, Slot: 54, Button: 0, Mode: 0}}) // 拿起快捷栏物品（先放石头到快捷栏）
	stone := ids["stone"]
	p.Slots[36] = entity.Item{ID: stone, Count: 10}
	p.Cursor = entity.Item{ID: stone, Count: 10} // 直接模拟光标持有
	g.handleContainerClick(&playerContainerClickEvent{p: p,
		click: packet.ContainerClick{Window: 3, Slot: 0, Button: 0, Mode: 0}})
	pos := [3]int32{8, -61, 8}
	if g.chests[pos].slots[0].ID != stone || g.chests[pos].slots[0].Count != 10 {
		t.Fatalf("箱子第 0 格应有 10 石头: %+v", g.chests[pos].slots[0])
	}

	// 持久化往返
	dir, _ := os.MkdirTemp("", "chest-")
	defer os.RemoveAll(dir)
	g.Cfg.SaveDir = dir
	g.saveChests()
	g2, _, _ := newTestGame(packet.ModeSurvival)
	g2.Cfg.SaveDir = dir
	g2.loadChests()
	if g2.chests[pos].slots[0].Count != 10 {
		t.Fatal("箱子读档不符")
	}

	// 挖掉 → 掉内容
	g.handleDig(&blockDigEvent{p: p, act: packet.PlayerAction{Status: packet.DigFinish, X: 8, Y: -61, Z: 8, Sequence: 1}})
	if _, exists := g.chests[pos]; exists {
		t.Fatal("挖掉后箱子状态应删除")
	}
	found := false
	for _, ie := range g.items {
		if ie.item.ID == stone {
			found = true
		}
	}
	if !found {
		t.Fatal("箱子内容应掉落")
	}
}

// TestBedPlaceAndSleep 床：双格放置 + 睡觉设重生点 + 跳天亮。
func TestBedPlaceAndSleep(t *testing.T) {
	g, p, _ := newInteractGame(t)
	ids := assets.ItemIDs(777)
	redBed := ids["red_bed"]
	if _, ok := g.itemBlocks[777][redBed]; !ok {
		t.Fatal("床物品无方块映射")
	}
	p.Yaw = 180 // 面朝北 → facing north → head 在北(z-1)
	p.Slots[36] = entity.Item{ID: redBed, Count: 1}
	g.handlePlace(&blockPlaceEvent{p: p, use: packet.UseItemOn{X: 8, Y: -61, Z: 8, Face: 1, Sequence: 1}})

	footState := g.world.GetBlock(8, -60, 8)
	if _, isBed := g.bedByState[footState]; !isBed {
		t.Fatalf("床 foot 未放置: %d", footState)
	}
	if _, isBed := g.bedByState[g.world.GetBlock(8, -60, 7)]; !isBed {
		t.Fatal("床 head 未放置")
	}

	// 夜晚右键床 → 重生点 + 天亮
	g.dayTime = 14000
	g.trySleepBed(p, packet.UseItemOn{X: 8, Y: -60, Z: 8, Face: 1})
	if p.SpawnPos == [3]float64{} {
		t.Fatal("睡觉应设重生点")
	}
	if g.dayTime != 1000 {
		t.Fatalf("单人睡觉应跳到清晨: %d", g.dayTime)
	}

	// 重生用床点
	p.Dead = true
	p.Health = 0
	g.handleRespawn(p)
	if p.Z != 7.5 { // head 在 z=7
		t.Fatalf("重生应在床边: z=%.1f", p.Z)
	}

	// 挖 foot → 两半都清
	g.handleDig(&blockDigEvent{p: p, act: packet.PlayerAction{Status: packet.DigFinish, X: 8, Y: -60, Z: 8, Sequence: 2}})
	if _, isBed := g.bedByState[g.world.GetBlock(8, -60, 7)]; isBed {
		t.Fatal("挖一半另一半应一起清")
	}
}

// TestDoorPlaceAndToggle 门：双格放置 + 右键开关。
func TestDoorPlaceAndToggle(t *testing.T) {
	g, p, fs := newInteractGame(t)
	ids := assets.ItemIDs(777)
	if _, ok := g.itemBlocks[777][ids["oak_door"]]; !ok {
		t.Fatal("门物品无方块映射")
	}
	p.Slots[36] = entity.Item{ID: ids["oak_door"], Count: 1}
	g.handlePlace(&blockPlaceEvent{p: p, use: packet.UseItemOn{X: 8, Y: -61, Z: 8, Face: 1, Sequence: 1}})
	lower := g.world.GetBlock(8, -60, 8)
	upper := g.world.GetBlock(8, -59, 8)
	if g.doorByState[lower] == nil || g.doorByState[upper] == nil {
		t.Fatalf("门双格未放置: %d %d", lower, upper)
	}

	g.toggleDoor(p, packet.UseItemOn{X: 8, Y: -60, Z: 8, Face: 1})
	if g.world.GetBlock(8, -60, 8) == lower {
		t.Fatal("右键门应切换开关状态")
	}
	if fs.find("minecraft:block_update") == nil && fs.find("minecraft:section_blocks_update") == nil {
		t.Fatalf("应广播门状态变更: %v", fs.names())
	}

	// 挖下半 → 上半也清
	g.handleDig(&blockDigEvent{p: p, act: packet.PlayerAction{Status: packet.DigFinish, X: 8, Y: -60, Z: 8, Sequence: 2}})
	if g.world.GetBlock(8, -59, 8) != world.StateAir {
		t.Fatal("挖门下半上半应一起清")
	}
}

// TestSandGravity 沙重力：挖掉下方 → 上方沙转为下落实体并落地堆叠。
func TestSandGravity(t *testing.T) {
	g, _, _ := newInteractGame(t)
	g.world.SetBlock(8, -61, 8, world.StateStone)
	g.world.SetBlock(8, -60, 8, world.StateSand)
	g.world.SetBlock(8, -59, 8, world.StateSand)

	g.triggerGravity(8, -60, 8) // 从下层沙开始触发（模拟下方被挖）
	if len(g.fallings) == 0 {
		t.Fatal("悬空沙应转为下落实体")
	}
	if g.world.GetBlock(8, -60, 8) != world.StateAir {
		t.Fatal("原位沙应移除")
	}
	for i := 0; i < 80; i++ {
		g.tickFallings()
	}
	if got := g.world.GetBlock(8, -60, 8); got != world.StateSand {
		t.Fatalf("沙应落回石头上方: %d", got)
	}
}

// TestWaterFlow 简化水流：挖掉水旁方块 → 水流入。
func TestWaterFlow(t *testing.T) {
	g, _, _ := newInteractGame(t)
	g.world.SetBlock(7, -61, 7, world.StateWater) // 源
	g.world.SetBlock(8, -61, 7, world.StateStone)
	g.world.SetBlock(8, -61, 8, world.StateStone)
	// 挖掉水旁边的石头
	g.world.SetBlock(8, -61, 7, world.StateAir)
	g.scheduleWaterFlow(8, -61, 7)
	for i := 0; i < 10; i++ {
		g.tick = int64(i*5 + 5)
		g.tickWater()
	}
	if got := g.world.GetBlock(8, -61, 7); got < 89 || got > 104 {
		t.Fatalf("水应流入空位: %d", got)
	}
}

// TestSaplingGrowth 树苗登记后可生长（age 直推触发）。
func TestSaplingGrowth(t *testing.T) {
	g, _, _ := newInteractGame(t)
	g.world.SetBlock(8, -61, 8, world.StateDirt)
	g.world.SetBlock(8, -60, 8, saplingState)
	g.trackSapling(8, -60, 8, saplingState)
	g.saplings[[3]int32{8, -60, 8}] = 250 // 已到必长树龄
	for i := 0; i < 8 && g.world.GetBlock(8, -60, 8) == saplingState; i++ {
		g.tick += 40
		g.tickSaplings()
	}
	if g.world.GetBlock(8, -60, 8) != logState {
		t.Fatalf("树苗应长成原木: %d", g.world.GetBlock(8, -60, 8))
	}
	foundLeaves := false
	for y := int32(-58); y <= -52 && !foundLeaves; y++ {
		for dx := int32(-2); dx <= 2 && !foundLeaves; dx++ {
			if g.world.GetBlock(8+dx, y, 8) == leavesState {
				foundLeaves = true
			}
		}
	}
	if !foundLeaves {
		t.Fatal("树冠应生成")
	}
}
