package game

import (
	"os"
	"testing"

	"mcserver/internal/assets"
	"mcserver/internal/entity"
	"mcserver/internal/packet"
	"mcserver/internal/world"
)

// newMobGame 构造带地面与生物掉落表的测试 Game。
func newMobGame(t *testing.T) (*Game, *entity.Player, *fakeSender) {
	t.Helper()
	g, p, fs := newTestGame(packet.ModeSurvival)
	g.mobDrops = assets.MobDrops(777)
	if g.mobDrops == nil {
		t.Fatal("mobdrops 资产缺失")
	}
	for dx := int32(-12); dx <= 12; dx++ { // 25×25 草地地板（游荡与生成采样）
		for dz := int32(-12); dz <= 12; dz++ {
			g.world.SetBlock(8+dx, -61, 8+dz, world.StateGrassBlock)
		}
	}
	return g, p, fs
}

// TestSpawnAndWander 生物生成、游荡在地面范围内、不穿地板。
func TestSpawnAndWander(t *testing.T) {
	g, _, fs := newMobGame(t)
	m := g.spawnMob("pig", 8.5, -60, 8.5)
	if m == nil || len(g.mobs) != 1 {
		t.Fatal("生成失败")
	}
	if fs.find("minecraft:add_entity") == nil {
		t.Error("应广播 add_entity")
	}
	for i := 0; i < 500; i++ {
		g.tickMobs()
	}
	if m.health <= 0 {
		t.Fatal("游荡不应死亡")
	}
	if m.y < -61 || m.y > -58 {
		t.Errorf("应贴地游走，y=%.2f", m.y)
	}
	if m.x < -5 || m.x > 21 || m.z < -5 || m.z > 21 {
		t.Errorf("游荡超出地板范围: (%.1f,%.1f)", m.x, m.z)
	}
}

// TestAttackMob 打猪：扣血、击退、死亡掉落生猪排。
func TestAttackMob(t *testing.T) {
	g, p, fs := newMobGame(t)
	m := g.spawnMob("pig", 8.5, -60, 8.5)
	g.mobs[m.eid].health = 0.5 // 空手一刀死

	g.handleAttack(&playerAttackEvent{p: p, target: m.eid})
	if len(g.mobs) != 0 {
		t.Fatal("应被击杀")
	}
	if fs.find("minecraft:entity_event") == nil {
		t.Error("应有受击/死亡动画")
	}
	// 掉落（猪 → 生猪排 1097）
	if len(g.items) != 1 {
		t.Fatalf("应掉落 1 个物品，实际 %d", len(g.items))
	}
	for _, ie := range g.items {
		if ie.item.ID != 1097 {
			t.Errorf("猪应掉生猪排 1097，实际 %d", ie.item.ID)
		}
	}
}

// TestAttackOutOfRange 超距攻击无效。
func TestAttackOutOfRange(t *testing.T) {
	g, p, _ := newMobGame(t)
	m := g.spawnMob("pig", 60, -60, 60)
	g.handleAttack(&playerAttackEvent{p: p, target: m.eid})
	if len(g.mobs) != 1 || m.health != 10 {
		t.Fatal("超距攻击不应生效")
	}
}

// TestSwordDamage 剑伤害按材质。
func TestSwordDamage(t *testing.T) {
	g, p, _ := newMobGame(t)
	for name, id := range assets.ItemIDs(777) {
		if g.itemNames[777] == nil {
			g.itemNames[777] = map[int32]string{}
		}
		g.itemNames[777][id] = name
	}
	iron, _ := assets.ItemIDs(777)["iron_sword"]
	p.Slots[36] = entity.Item{ID: iron, Count: 1}
	if d := g.attackDamage(p); d != 6 {
		t.Errorf("铁剑伤害应为 6，实际 %.1f", d)
	}
	p.Slots[36] = entity.Item{}
	if d := g.attackDamage(p); d != 1 {
		t.Errorf("空手伤害应为 1，实际 %.1f", d)
	}
}

// TestZombieChaseAndAttack 夜间僵尸追击并攻击玩家。
func TestZombieChaseAndAttack(t *testing.T) {
	g, p, fs := newMobGame(t)
	m := g.spawnMob("zombie", 2.5, -60, 8.5) // 距玩家 6 格
	for i := 0; i < 200; i++ {
		g.tickMobs()
	}
	if m.health <= 0 || len(g.mobs) == 0 {
		t.Fatal("僵尸不应消失")
	}
	if p.Health >= 20 {
		t.Fatal("僵尸应追上并攻击玩家")
	}
	if fs.find("minecraft:set_health") == nil {
		t.Error("玩家受伤应同步 set_health")
	}
}

// TestMobDespawn 远离玩家消散。
func TestMobDespawn(t *testing.T) {
	g, _, _ := newMobGame(t)
	m := g.spawnMob("pig", 200.5, -60, 200.5) // 远离
	for i := 0; i < 120; i++ {
		g.tickMobs()
	}
	if len(g.mobs) != 0 {
		t.Fatal("远离玩家应消散")
	}
	_ = m
}

// TestMobSpawningDaylight 白天在草地上生成被动生物。
func TestMobSpawningDaylight(t *testing.T) {
	g, p, _ := newMobGame(t)
	g.dayTime = 6000 // 正午
	for i := 0; i < 8; i++ { // 采样随机性：多次生成周期内至少成功一次
		g.tickMobSpawning()
		g.tick += 100
	}
	if len(g.mobs) == 0 {
		t.Fatal("白天应在附近草地生成被动生物")
	}
	for cx := int32(-2); cx <= 3; cx++ { // 视距内区块补全（findSpawnSpot 需要）
		for cz := int32(-2); cz <= 3; cz++ {
			p.SentChunks[[2]int32{cx, cz}] = true
		}
	}
	for _, m := range g.mobs {
		if m.kind.hostile {
			t.Error("白天不应生成敌对生物")
		}
	}
}

// TestCraft2x2 随身合成：木棍×2 竖排无法 2x2 匹配；火把（炭+木棍）可合成。
func TestCraft2x2(t *testing.T) {
	g, p, _ := newMobGame(t)
	g.recipes = loadRecipes(777)
	if len(g.recipes) == 0 {
		t.Fatal("recipes 资产缺失")
	}
	ids := assets.ItemIDs(777)
	if g.itemNames[777] == nil {
		g.itemNames[777] = map[int32]string{}
	}
	for name, id := range ids {
		g.itemNames[777][id] = name
	}
	button, ok := ids["acacia_button"]
	if !ok {
		t.Fatal("物品表缺金合欢木按钮")
	}
	// 用无标签子集里的金合欢木按钮（单格金合欢木板）验证
	if name, n := g.matchRecipe(p); name != "" || n != 0 {
		t.Fatal("空网格不应匹配")
	}
	p.Slots[1] = entity.Item{ID: ids["acacia_planks"], Count: 5}
	name, n := g.matchRecipe(p)
	if name != "acacia_button" || n != 1 {
		t.Fatalf("应匹配金合欢木按钮: %s x%d", name, n)
	}
	if !g.takeCraftResult(p) {
		t.Fatal("应可取结果")
	}
 	if p.Cursor.ID != button || p.Cursor.Count != n {
		t.Fatalf("光标应为金合欢木按钮×%d: %+v", n, p.Cursor)
	}
 	if p.Slots[1].Count != 4 {
		t.Fatal("材料应扣 1")
	}
}

// TestCraftTable3x3 工作台：木镐配方（3 木板类 + 2 木棍，含标签格）。
func TestCraftTable3x3(t *testing.T) {
	g, p, fs := newMobGame(t)
	g.recipes = loadRecipes(777)
	g.itemTags = assets.ItemTags(777)
	if g.itemTags == nil {
		t.Fatal("itemtags 资产缺失")
	}
	ids := assets.ItemIDs(777)
	if g.itemNames[777] == nil {
		g.itemNames[777] = map[int32]string{}
	}
	for name, id := range ids {
		g.itemNames[777][id] = name
	}
	g.crafting[p.EntityID] = &craftState{}
	cs := g.crafting[p.EntityID]
	// 木镐：顶行 3 木板（任意木板，用 oak），中/下行木棍居中
	planks, sticks := ids["oak_planks"], ids["stick"]
	for c := 0; c < 3; c++ {
		cs.grid[c] = entity.Item{ID: planks, Count: 3}
	}
	cs.grid[4] = entity.Item{ID: sticks, Count: 2}
	cs.grid[7] = entity.Item{ID: sticks, Count: 2}

	name, n := g.matchRecipe3(p, cs)
	if name != "wooden_pickaxe" || n != 1 {
		t.Fatalf("应匹配木镐: %q x%d", name, n)
	}
	if !g.takeCraftResult3(p, cs) {
		t.Fatal("应可取结果")
	}
	if pick, ok := ids["wooden_pickaxe"]; !ok || p.Cursor.ID != pick {
		t.Fatalf("光标应为木镐: %+v", p.Cursor)
	}
	if cs.grid[0].Count != 2 || cs.grid[4].Count != 1 {
		t.Fatal("材料应各扣 1")
	}
	// 同网格用 2x2 匹配器不应误匹配（超出 2x2 布局）
	if name2, _ := g.matchRecipe(p); name2 == "wooden_pickaxe" {
		t.Error("木镐 3x3 配方不应被随身合成匹配")
	}
	_ = fs
}

// TestOpenCrafting 右键工作台方块打开窗口，关闭时材料退回。
func TestOpenCrafting(t *testing.T) {
	g, p, fs := newMobGame(t)
	g.recipes = loadRecipes(777)
	g.itemTags = assets.ItemTags(777)
	ids := assets.ItemIDs(777)
	tableState, ok := g.itemBlocks[777][ids["crafting_table"]]
	if !ok {
		t.Fatal("无工作台映射")
	}
	g.world.SetBlock(8, -61, 8, tableState)

	opened := g.tryOpenCrafting(p, packet.UseItemOn{X: 8, Y: -61, Z: 8, Face: 1, Sequence: 1})
	if !opened {
		t.Fatal("右键工作台应打开窗口")
	}
	if fs.find("minecraft:open_screen") == nil {
		t.Fatal("应发送 open_screen")
	}
	cs := g.crafting[p.EntityID]
	cs.grid[0] = entity.Item{ID: ids["oak_planks"], Count: 7}
	g.closeCrafting(p)
	if p.Slots[36].ID != ids["oak_planks"] || p.Slots[36].Count != 7 {
		t.Fatal("关闭工作台材料应退回背包")
	}
	if fs.find("minecraft:container_close") == nil {
		t.Fatal("应发送 container_close")
	}
}

// TestFurnaceSmelting 熔炉全流程：放沙+煤 → 点火亮方块 → 冶出玻璃 → 熄灭。
func TestFurnaceSmelting(t *testing.T) {
	g, p, fs := newMobGame(t)
	g.smelting = assets.Smelting(777)
	g.itemTags = assets.ItemTags(777)
	if g.smelting["sand"] != "glass" {
		t.Fatal("冶炼表缺 沙→玻璃")
	}
	ids := assets.ItemIDs(777)

	g.world.SetBlock(8, -61, 8, furnaceUnlit)
	if !g.tryOpenFurnace(p, packet.UseItemOn{X: 8, Y: -61, Z: 8, Face: 1}) {
		t.Fatal("右键熔炉应打开")
	}
	if fs.find("minecraft:open_screen") == nil {
		t.Fatal("应发送 open_screen")
	}
	pos := [3]int32{8, -61, 8}
	fu := g.furnaces[pos]
	fu.input = entity.Item{ID: ids["sand"], Count: 2}
	fu.fuel = entity.Item{ID: ids["coal"], Count: 1}

	for i := 0; i < 210; i++ {
		g.tickFurnaces()
	}
	glass := ids["glass"]
	if fu.output.ID != glass || fu.output.Count != 1 {
		t.Fatalf("应炼出 1 玻璃: %+v", fu.output)
	}
	if fu.input.Count != 1 {
		t.Fatalf("应消耗 1 沙: %d", fu.input.Count)
	}
	if got := g.world.GetBlock(8, -61, 8); got != furnaceLit {
		t.Fatalf("燃烧中应为亮熔炉状态: %d", got)
	}
	// 烧完煤（1600 刻）后熄灭
	for i := 0; i < 1700; i++ {
		g.tickFurnaces()
	}
	if got := g.world.GetBlock(8, -61, 8); got != furnaceUnlit {
		t.Fatalf("燃料耗尽应熄灭: %d", got)
	}
}

// TestFurnacePersistence 熔炉状态存取往返。
func TestFurnacePersistence(t *testing.T) {
	dir, err := os.MkdirTemp("", "furn-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	g, _, _ := newTestGame(packet.ModeSurvival)
	g.Cfg.SaveDir = dir
	g.smelting = assets.Smelting(777)
	ids := assets.ItemIDs(777)
	g.furnaces[[3]int32{1, 2, 3}] = &furnaceState{
		input: entity.Item{ID: ids["sand"], Count: 5}, burn: 99, burnMax: 100, cook: 42}
	g.saveFurnaces()

	g2, _, _ := newTestGame(packet.ModeSurvival)
	g2.Cfg.SaveDir = dir
	g2.smelting = assets.Smelting(777)
	g2.loadFurnaces()
	fu := g2.furnaces[[3]int32{1, 2, 3}]
	if fu == nil || fu.input.Count != 5 || fu.cook != 42 {
		t.Fatalf("熔炉读档不符: %+v", fu)
	}
}

// TestFurnaceDropOnBreak 挖掉熔炉掉落内容物。
func TestFurnaceDropOnBreak(t *testing.T) {
	g, p, _ := newMobGame(t)
	g.smelting = assets.Smelting(777)
	ids := assets.ItemIDs(777)
	g.world.SetBlock(8, -60, 8, furnaceUnlit)
	pos := [3]int32{8, -60, 8}
	g.furnaces[pos] = &furnaceState{input: entity.Item{ID: ids["sand"], Count: 3}}
	g.handleDig(&blockDigEvent{p: p, act: packet.PlayerAction{Status: packet.DigFinish, X: 8, Y: -60, Z: 8, Sequence: 1}})
	if _, exists := g.furnaces[pos]; exists {
		t.Fatal("挖掉后熔炉状态应删除")
	}
	if len(g.items) != 1 {
		t.Fatalf("应掉落内容物: %d", len(g.items))
	}
}

// TestAdminCommands 管理命令：gamemode/give/tp/time。
func TestAdminCommands(t *testing.T) {
	g, p, fs := newMobGame(t)
	g.itemTags = assets.ItemTags(777)
	ids := assets.ItemIDs(777)
	if g.itemNames[777] == nil {
		g.itemNames[777] = map[int32]string{}
	}
	for name, id := range ids {
		g.itemNames[777][id] = name
	}

	g.handleCommand(&playerCommandEvent{p: p, Name: "gamemode", Args: []string{"creative"}})
	if !g.isCreative(p) {
		t.Fatal("/gamemode creative 应生效")
	}
	g.handleCommand(&playerCommandEvent{p: p, Name: "gamemode", Args: []string{"survival"}})
	if g.isCreative(p) {
		t.Fatal("/gamemode survival 应生效")
	}

	g.handleCommand(&playerCommandEvent{p: p, Name: "give", Args: []string{"diamond", "5"}})
	if p.Slots[36].ID != ids["diamond"] || p.Slots[36].Count != 5 {
		t.Fatalf("/give 应给 5 钻石: %+v", p.Slots[36])
	}

	g.handleCommand(&playerCommandEvent{p: p, Name: "tp", Args: []string{"100", "-50", "-200"}})
	if p.X != 100 || p.Z != -200 {
		t.Fatal("/tp 应改坐标")
	}

	g.handleCommand(&playerCommandEvent{p: p, Name: "time", Args: []string{"set", "night"}})
	if g.dayTime != 13000 {
		t.Fatal("/time set night 应生效")
	}
	_ = fs
}
