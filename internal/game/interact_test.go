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

// TestXPAndOrb 经验：给经验升级 + 经验球拾取。
func TestXPAndOrb(t *testing.T) {
	g, p, fs := newInteractGame(t)
	g.world.SetBlock(8, -61, 8, world.StateStone)
	g.giveXP(p, 10) // 7+0*2=7 升 1 级余 3；9 升 2 级余 0
	if p.Level < 1 || p.TotalXP != 10 {
		t.Fatalf("经验不符: level=%d total=%d", p.Level, p.TotalXP)
	}
	if fs.find("minecraft:set_experience") == nil {
		t.Fatal("应同步 set_experience")
	}
	// 经验球拾取
	g.spawnXPOrb(8.5, -60.5, 8.5, 5)
	for i := 0; i < itemPickupWait+5; i++ {
		g.tickOrbs()
	}
	if p.TotalXP != 15 {
		t.Fatalf("拾取经验球后应为 15: %d", p.TotalXP)
	}
	if len(g.orbs) != 0 {
		t.Fatal("经验球应消失")
	}
}

// TestDurability 工具耐久：铁剑 250，攻击 249 次后仍在，第 250 次破坏。
func TestDurability(t *testing.T) {
	g, p, _ := newInteractGame(t)
	ids := assets.ItemIDs(777)
	ironSword := ids["iron_sword"]
	p.Slots[36] = entity.Item{ID: ironSword, Count: 1}
	p.Slots[36].Damage = 248
	m := g.spawnMob("chicken", 8.5, -60, 8.5)
	g.mobs[m.eid].health = 999

	for i := 0; i < 1; i++ { // 249
		g.handleAttack(&playerAttackEvent{p: p, target: m.eid})
	}
	if p.Slots[36].ID != ironSword {
		t.Fatalf("249 损伤不应破坏: %+v", p.Slots[36])
	}
	g.handleAttack(&playerAttackEvent{p: p, target: m.eid})
	if p.Slots[36].ID != 0 {
		t.Fatalf("250 损伤应破坏: %+v", p.Slots[36])
	}
}

// TestArmorAbsorb 盔甲减伤：全套铁甲 15 点 → 伤害 10 减 60%。
func TestArmorAbsorb(t *testing.T) {
	g, p, _ := newInteractGame(t)
	ids := assets.ItemIDs(777)
	p.Slots[5] = entity.Item{ID: ids["iron_helmet"], Count: 1}    // 2
	p.Slots[6] = entity.Item{ID: ids["iron_chestplate"], Count: 1} // 6
	p.Slots[7] = entity.Item{ID: ids["iron_leggings"], Count: 1}  // 5
	p.Slots[8] = entity.Item{ID: ids["iron_boots"], Count: 1}     // 2
	if pts := g.totalArmor(p); pts != 15 {
		t.Fatalf("铁甲总点数应 15: %d", pts)
	}
	g.damage(p, 10, damageGeneric)
	// 10 * (1 - 15*0.04) = 4 → 20-4=16
	if p.Health != 16 {
		t.Fatalf("减伤后应为 16: %.1f", p.Health)
	}
}

// TestDeathDrops 死亡掉落：物品栏清空 + 经验折半掉球。
func TestDeathDrops(t *testing.T) {
	g, p, _ := newInteractGame(t)
	ids := assets.ItemIDs(777)
	p.Slots[36] = entity.Item{ID: ids["stone"], Count: 32}
	p.TotalXP = 20
	g.damage(p, 1000, damageGeneric)
	if !p.Dead {
		t.Fatal("应死亡")
	}
	if p.Slots[36].ID != 0 {
		t.Fatal("死亡后物品栏应清空")
	}
	if p.TotalXP != 0 {
		t.Fatal("死亡后经验应清零")
	}
	items := 0
	for _, ie := range g.items {
		if ie.item.ID == ids["stone"] {
			items++
		}
	}
	if items != 1 {
		t.Fatalf("石头应掉落: %d", items)
	}
	if len(g.orbs) == 0 {
		t.Fatal("经验应折半掉球")
	}
}

// TestSkeletonShootsArrow 骷髅射箭：箭生成、飞行、命中玩家。
func TestSkeletonShootsArrow(t *testing.T) {
	g, p, fs := newInteractGame(t)
	g.world.SetBlock(6, -61, 8, world.StateStone)
	g.world.SetBlock(7, -61, 8, world.StateStone)
	m := g.spawnMob("skeleton", 6.5, -60, 8.5)
	for i := 0; i < 60 && len(g.arrows) == 0; i++ {
		g.tickHostileAI(m)
	}
	if len(g.arrows) == 0 {
		t.Fatal("骷髅应射箭")
	}
	if fs.find("minecraft:add_entity") == nil {
		t.Fatal("应广播箭实体")
	}
	// 飞行到命中
	for i := 0; i < 60 && p.Health == 20; i++ {
		g.tickArrows()
	}
	if p.Health >= 20 {
		t.Fatal("箭应命中玩家")
	}
}

// TestCreeperExplodes 苦力怕引信到点爆炸：破坏方块+伤害玩家+自灭。
func TestCreeperExplodes(t *testing.T) {
	g, p, _ := newInteractGame(t)
	g.world.SetBlock(8, -61, 8, world.StateStone) // 站位地板
	m := g.spawnMob("creeper", 8.5, -60, 8.5)
	// 贴脸触发引信
	g.tickHostileAI(m)
	if g.creepers[m.eid] == nil {
		t.Fatal("近身应点燃苦力怕")
	}
	for i := int32(0); i < 31; i++ {
		g.tickCreepers()
	}
	if _, exists := g.mobs[m.eid]; exists {
		t.Fatal("爆炸后苦力怕应消失")
	}
	if p.Health >= 20 {
		t.Fatal("爆炸应伤害玩家")
	}
}

// TestExplodeBlocks 爆炸半径破坏。
func TestExplodeBlocks(t *testing.T) {
	g, _, _ := newInteractGame(t)
	for dx := int32(-2); dx <= 2; dx++ {
		for dz := int32(-2); dz <= 2; dz++ {
			g.world.SetBlock(50+dx, -61, 50+dz, world.StateStone)
		}
	}
	g.explode(50.5, -60.5, 50.5, 3)
	if got := g.world.GetBlock(50, -61, 50); got != world.StateAir {
		t.Fatalf("中心应被炸空: %d", got)
	}
}

// TestUndeadBurn 亡灵白天燃烧，夜里不烧。
func TestUndeadBurn(t *testing.T) {
	g, _, _ := newInteractGame(t)
	z := g.spawnMob("zombie", 100.5, -60, 100.5)
	g.world.SetBlock(100, -61, 100, world.StateStone)
	g.dayTime = 6000 // 正午
	g.tick = 20
	g.tickUndeadBurn()
	if z.health >= 20 {
		t.Fatal("白天无遮挡僵尸应燃烧扣血")
	}
	g.dayTime = 14000
	z.health = 20
	g.tick = 40
	g.tickUndeadBurn()
	if z.health != 20 {
		t.Fatal("夜里不应燃烧")
	}
}

// TestLeafDecay 树叶枯萎：原木移除后远处叶子消失。
func TestLeafDecay(t *testing.T) {
	g, _, _ := newInteractGame(t)
	ids := assets.ItemIDs(777)
	if g.itemNames[777] == nil {
		g.itemNames[777] = map[int32]string{}
	}
	for name, id := range ids {
		g.itemNames[777][id] = name
	}
	// 独立叶子块（无原木支撑）
	g.world.SetBlock(80, -60, 80, leavesDistance[7])
	g.scheduleLeafCheck(80, -60, 80)
	for i := 0; i < 6; i++ {
		g.tick = int64(i*10 + 10)
		g.tickLeafDecay()
	}
	if got := g.world.GetBlock(80, -60, 80); got != world.StateAir {
		t.Fatalf("无支撑叶子应枯萎: %d", got)
	}
	// 原木旁的叶子保留
	g.world.SetBlock(90, -61, 90, logState)
	g.world.SetBlock(90, -60, 90, leavesDistance[3])
	g.scheduleLeafCheck(90, -60, 90)
	for i := 0; i < 6; i++ {
		g.tick = int64(i*10 + 10)
		g.tickLeafDecay()
	}
	if got := g.world.GetBlock(90, -60, 90); !isLeaves(got) {
		t.Fatal("原木旁叶子不应枯萎")
	}
}

// TestWeatherRain 天气：强制下雨广播 game_event，计时归零转晴。
func TestWeatherRain(t *testing.T) {
	g, p, fs := newInteractGame(t)
	g.setRaining(true)
	if !g.raining {
		t.Fatal("应进入雨天")
	}
	if fs.find("minecraft:game_event") == nil {
		t.Fatal("应广播 game_event 开始下雨")
	}
	// 雨停
	fs.sent = nil
	g.rainTimer = 1
	g.tick = 100
	g.tickWeather()
	g.tick += 100
	g.tickWeather() // 计时归零 → 停雨
	if g.raining {
		t.Fatal("计时归零应停雨")
	}
	if fs.find("minecraft:game_event") == nil {
		t.Fatal("应广播停止下雨")
	}
	_ = p
}

// TestBreedAnimals 动物繁殖：两头牛各喂麦 → 生成幼崽，进入繁殖冷却。
func TestBreedAnimals(t *testing.T) {
	g, p, fs := newInteractGame(t)
	ids := assets.ItemIDs(777)
	wheat := ids["wheat"]
	p.Slots[36] = entity.Item{ID: wheat, Count: 3}
	cow1 := g.spawnMob("cow", 8.5, -60, 8.5)
	cow2 := g.spawnMob("cow", 9.5, -60, 8.5)

	g.handleInteract(&playerInteractEvent{p: p, target: cow1.eid})
	if g.breeds[cow1.eid] == nil || g.breeds[cow1.eid].loveCd <= 0 {
		t.Fatal("喂食后应进入爱慕状态")
	}
	if fs.find("minecraft:level_event") == nil {
		t.Fatal("应有爱心粒子")
	}
	if p.Slots[36].Count != 2 {
		t.Fatal("应消耗 1 个小麦")
	}
	before := len(g.mobs)
	g.handleInteract(&playerInteractEvent{p: p, target: cow2.eid})
	if len(g.mobs) != before+1 {
		t.Fatal("双亲爱慕应生成幼崽")
	}
	if g.breeds[cow1.eid].loveCd < 2000 {
		t.Fatal("繁殖后应有长冷却")
	}
	// 爱慕中不可再喂
	p.Slots[36] = entity.Item{ID: wheat, Count: 1}
	cnt := len(g.mobs)
	g.handleInteract(&playerInteractEvent{p: p, target: cow1.eid})
	if len(g.mobs) != cnt || g.breeds[cow1.eid].loveCd < 2000 {
		t.Fatal("冷却中喂食应无效")
	}
}

// TestFarmTillAndPlant 锄地 + 种麦 + 生长 + 收获。
func TestFarmTillAndPlant(t *testing.T) {
	g, p, fs := newInteractGame(t)
	ids := assets.ItemIDs(777)
	g.world.SetBlock(8, -62, 8, world.StateStone) // 地基（悬空不可锄）
	g.world.SetBlock(8, -61, 8, world.StateDirt)

	// 锄头锄地
	p.Slots[36] = entity.Item{ID: ids["wooden_hoe"], Count: 1}
	if !g.tryTill(p, packet.UseItemOn{X: 8, Y: -61, Z: 8, Face: 1, Sequence: 1}) {
		t.Fatal("锄头右键泥土应耕地")
	}
	if s := g.world.GetBlock(8, -61, 8); s != farmlandState && s != farmlandWet {
		t.Fatalf("应为耕地: %d", s)
	}
	if fs.find("minecraft:block_update") == nil && fs.find("minecraft:section_blocks_update") == nil {
		t.Fatal("应广播耕地变更")
	}

	// 种子种植
	p.Slots[36] = entity.Item{ID: ids["wheat_seeds"], Count: 3}
	if !g.tryPlantSeed(p, packet.UseItemOn{X: 8, Y: -61, Z: 8, Face: 1, Sequence: 2}) {
		t.Fatal("种子右键耕地应种植")
	}
	if g.world.GetBlock(8, -60, 8) != wheatAges[0] {
		t.Fatalf("应为 age0 小麦: %d", g.world.GetBlock(8, -60, 8))
	}
	if p.Slots[36].Count != 2 {
		t.Fatal("应消耗 1 种子")
	}

	// 生长循环到成熟
	for i := 0; i < 400 && g.world.GetBlock(8, -60, 8) != wheatAges[7]; i++ {
		g.tick += 30
		g.tickCrops()
	}
	if g.world.GetBlock(8, -60, 8) != wheatAges[7] {
		t.Fatalf("应长到成熟: %d", g.world.GetBlock(8, -60, 8))
	}

	// 收获：掉麦+种子
	g.handleDig(&blockDigEvent{p: p, act: packet.PlayerAction{Status: packet.DigFinish, X: 8, Y: -60, Z: 8, Sequence: 3}})
	wheatN, seedN := 0, 0
	for _, ie := range g.items {
		if ie.item.ID == ids["wheat"] {
			wheatN++
		}
		if ie.item.ID == ids["wheat_seeds"] {
			seedN++
		}
	}
	if wheatN != 1 || seedN < 1 {
		t.Fatalf("收获应掉 1 麦 + ≥1 种子: %d %d", wheatN, seedN)
	}
}

// TestBonemeal 骨粉：小麦跳龄、树苗立即成树。
func TestBonemeal(t *testing.T) {
	g, p, _ := newInteractGame(t)
	ids := assets.ItemIDs(777)
	g.world.SetBlock(8, -62, 8, world.StateStone)
	g.world.SetBlock(8, -61, 8, farmlandState)
	g.world.SetBlock(8, -60, 8, wheatAges[1])
	g.crops[[3]int32{8, -60, 8}] = 0

	p.Slots[36] = entity.Item{ID: ids["bone_meal"], Count: 2}
	if !g.tryBonemeal(p, packet.UseItemOn{X: 8, Y: -60, Z: 8, Face: 1, Sequence: 1}) {
		t.Fatal("骨粉右键小麦应生效")
	}
	s := g.world.GetBlock(8, -60, 8)
	age := -1
	for i, wa := range wheatAges {
		if s == wa {
			age = i
		}
	}
	if age < 3 {
		t.Fatalf("应 +2~3 龄: %d", age)
	}
	if p.Slots[36].Count != 1 {
		t.Fatal("应消耗 1 骨粉")
	}

	// 树苗立即成树
	g.world.SetBlock(20, -60, 20, saplingState)
	if !g.tryBonemeal(p, packet.UseItemOn{X: 20, Y: -60, Z: 20, Face: 1, Sequence: 2}) {
		t.Fatal("骨粉右键树苗应成树")
	}
	if g.world.GetBlock(20, -60, 20) != logState {
		t.Fatal("树苗应长成原木")
	}
}

// TestBrewingAndDrink 酿造全流程：水瓶+烈焰粉→力量药水，饮用生效。
func TestBrewingAndDrink(t *testing.T) {
	g, p, fs := newInteractGame(t)
	ids := assets.ItemIDs(777)
	g.brewing = assets.Brewing(777)
	if g.brewing[[2]string{"", "nether_wart"}] == "" && g.brewing[[2]string{"awkward", "blaze_powder"}] != "strength" {
		t.Fatal("酿造表资产异常")
	}
	// 放酿造台并打开
	g.world.SetBlock(8, -61, 8, brewingStandState)
	if !g.tryOpenBrewing(p, packet.UseItemOn{X: 8, Y: -61, Z: 8, Face: 1}) {
		t.Fatal("右键酿造台应打开")
	}
	if fs.find("minecraft:open_screen") == nil {
		t.Fatal("应发 open_screen")
	}
	pos := [3]int32{8, -61, 8}
	bs := g.brews[pos]
	// awkward 药水在 0 槽 + 烈焰粉材料
	bs.slots[0] = entity.Item{ID: itemPotion, Count: 1}
	bs.potion[0] = "awkward"
	bs.slots[3] = entity.Item{ID: ids["blaze_powder"], Count: 1}
	for i := 0; i < 410; i++ {
		g.tickBrewings()
	}
	if bs.potion[0] != "strength" {
		t.Fatalf("应酿出力量药水: %q", bs.potion[0])
	}
	if bs.slots[3].ID != 0 {
		t.Fatal("材料应消耗")
	}

	// 取药水到手（光标）再放到快捷栏，饮用
	p.Cursor = entity.Item{ID: itemPotion, Count: 1}
	p.PotionOf[[2]int32{-1, 0}] = "strength"
	p.Slots[36] = entity.Item{}
	leftClickSlot(p, 36)
	if p.PotionOf[[2]int32{36, 0}] != "strength" || p.Slots[36].ID != itemPotion {
		t.Fatalf("药水名记账应迁移: %+v %v", p.Slots[36], p.PotionOf)
	}
	g.handleUseItem(&playerUseItemEvent{p: p, sequence: 9})
	if p.Effects["strength"] <= 0 {
		t.Fatal("饮用力量药水应生效")
	}
	if p.Slots[36].ID != 0 {
		t.Fatal("药水应消耗")
	}
	// 效果到期
	p.Effects["strength"] = 20
	g.tick = 25
	g.tickEffects()
	if _, ok := p.Effects["strength"]; ok {
		t.Fatal("效果应到期")
	}
}

// TestEnchantTable 附魔台：放剑+青金石+等级 → 附魔生效与消耗。
func TestEnchantTable(t *testing.T) {
	g, p, fs := newInteractGame(t)
	ids := assets.ItemIDs(777)
	g.enchants = assets.Enchants(777)
	if len(g.enchants) < 30 {
		t.Fatal("附魔表资产缺失")
	}
	// itemtags 里应有 enchantable 标签（genench 用的标签名）
	g.itemTags = assets.ItemTags(777)

	g.world.SetBlock(8, -61, 8, enchTableState)
	if !g.tryOpenEnchantTable(p, packet.UseItemOn{X: 8, Y: -61, Z: 8, Face: 1}) {
		t.Fatal("右键附魔台应打开")
	}
	if fs.find("minecraft:open_screen") == nil {
		t.Fatal("应发 open_screen")
	}

	// 放铁剑 + 青金石 + 等级
	p.EnchSlots[0] = entity.Item{ID: ids["iron_sword"], Count: 1}
	p.EnchSlots[1] = entity.Item{ID: itemLapis, Count: 3}
	p.Level = 5
	g.applyEnchant(p, 1)
	if len(p.EnchItem) == 0 {
		t.Fatal("一档应至少附上 1 条")
	}
	if p.Level != 4 {
		t.Fatalf("应消耗 1 级: %d", p.Level)
	}
	if p.EnchSlots[1].Count != 2 {
		t.Fatal("应消耗 1 青金石")
	}
	// 附魔应为剑类适用项
	for _, e := range p.EnchItem {
		info, ok := g.enchants[e.Name]
		if !ok || e.Level < 1 || e.Level > info.MaxLevel {
			t.Fatalf("附魔非法: %+v", e)
		}
	}
	// 锋利加成路径
	p.EnchItem = append(p.EnchItem, entity.EnchEntry{Name: "sharpness", Level: 5})
	if b := g.enchantAttackBonus(p); b < 3 {
		t.Fatalf("锋利 V 加成应 ≥3: %.1f", b)
	}
	// 关闭退回
	g.closeEnchantTable(p)
	if p.EnchSlots[0].ID != 0 || p.Slots[36].ID != ids["iron_sword"] {
		t.Fatal("关闭应退回物品")
	}
}

// TestRedstoneLeverLamp 拉杆 → 灯：开拉杆灯亮、关灯灭。
func TestRedstoneLeverLamp(t *testing.T) {
	g, p, fs := newInteractGame(t)
	// 地板 + 拉杆 + 灯（相邻）
	g.world.SetBlock(8, -62, 8, world.StateStone)
	g.world.SetBlock(8, -61, 8, leverOff)
	g.world.SetBlock(9, -61, 8, lampOff)

	g.toggleLever(p, packet.UseItemOn{X: 8, Y: -61, Z: 8, Face: 1, Sequence: 1})
	g.rsDirty = true
	g.tick = 2
	g.tickRedstone()
	if got := g.world.GetBlock(9, -61, 8); got != lampOn {
		t.Fatalf("拉杆开灯应亮: %d", got)
	}
	if fs.find("minecraft:block_update") == nil && fs.find("minecraft:section_blocks_update") == nil {
		t.Fatal("应广播灯变更")
	}

	g.toggleLever(p, packet.UseItemOn{X: 8, Y: -61, Z: 8, Face: 1, Sequence: 2})
	g.rsDirty = true
	g.tick += 2
	g.tickRedstone()
	if got := g.world.GetBlock(9, -61, 8); got != lampOff {
		t.Fatalf("拉杆关灯应灭: %d", got)
	}
}

// TestRedstoneWireDecay 线衰减：火把 15 → 5 格线 → 末端灯。
func TestRedstoneWireDecay(t *testing.T) {
	g, _, _ := newInteractGame(t)
	// 火把(源) + 5 格线 + 灯
	g.world.SetBlock(0, -61, 0, torchLit)
	for i := int32(1); i <= 5; i++ {
		g.world.SetBlock(i, -61, 0, wireBase[0])
	}
	g.world.SetBlock(6, -61, 0, lampOff)

	g.rsDirty = true
	g.tick = 2
	g.tickRedstone()
	// 线 1 应为 15、线 5 应为 11
	if got := rsWirePower(g.world.GetBlock(1, -61, 0)); got != 15 {
		t.Fatalf("线 1 应 power15: %d", got)
	}
	if got := rsWirePower(g.world.GetBlock(5, -61, 0)); got != 11 {
		t.Fatalf("线 5 应 power11: %d", got)
	}
	if g.world.GetBlock(6, -61, 0) != lampOn {
		t.Fatal("末端灯应亮")
	}
	// 拉远：15 格线后灯不亮（衰减到 0）
	for i := int32(7); i <= 20; i++ {
		g.world.SetBlock(i, -61, 0, wireBase[0])
	}
	g.world.SetBlock(21, -61, 0, lampOn)
	g.rsDirty = true
	g.tick += 2
	g.tickRedstone()
	if g.world.GetBlock(21, -61, 0) != lampOff {
		t.Fatal("15 格外灯应不亮")
	}
}

// TestRedstoneTorchInvert 火把反相：火把下被充能 → 熄灭。
func TestRedstoneTorchInvert(t *testing.T) {
	g, p, _ := newInteractGame(t)
	// 布局：拉杆 → 线 →（线爬上石头侧面经顶面）火把站在被充能的石头上。
	// 简化模型：火把下方直接是线（实现按"下方被充能则熄灭"）。
	g.world.SetBlock(0, -61, 0, leverOff)
	g.world.SetBlock(1, -62, 0, wireBase[0]) // 火把下方这条线
	g.world.SetBlock(1, -61, 0, torchLit)

	g.rsDirty = true
	g.tick = 2
	g.tickRedstone()
	if g.world.GetBlock(1, -61, 0) != torchLit {
		t.Fatal("未充能火把应保持亮")
	}
	g.toggleLever(p, packet.UseItemOn{X: 0, Y: -61, Z: 0, Face: 1, Sequence: 1})
	g.rsDirty = true
	g.tick += 2
	g.tickRedstone()
	if g.world.GetBlock(1, -61, 0) != torchUnlit {
		t.Fatal("被充能的火把应熄灭")
	}
}

// TestPistonExtendRetract 活塞：通电推出头并推方块，断电收回。
func TestPistonExtendRetract(t *testing.T) {
	g, p, _ := newInteractGame(t)
	// 布局：拉杆(0,-61,0) 活塞(1,-61,0 朝north=-z) 石头(1,-61,-1) 空气(1,-61,-2)
	g.world.SetBlock(0, -61, 0, leverOff)
	g.world.SetBlock(1, -61, 0, pistonRet["north"])
	g.world.SetBlock(1, -61, -1, world.StateStone)

	g.toggleLever(p, packet.UseItemOn{X: 0, Y: -61, Z: 0, Face: 1, Sequence: 1})
	g.rsDirty = true
	g.tick = 2
	g.tickRedstone()
	g.tickPistons()
	// 伸出：本体变 extended，头在 (1,-61,-1)，石头被推到 (1,-61,-2)
	if got := g.world.GetBlock(1, -61, 0); got != pistonExt["north"] {
		t.Fatalf("活塞应伸出: %d", got)
	}
	if got := g.world.GetBlock(1, -61, -1); got != pistonHeadN {
		t.Fatalf("头应出现: %d", got)
	}
	if got := g.world.GetBlock(1, -61, -2); got != world.StateStone {
		t.Fatalf("石头应被推: %d", got)
	}

	g.toggleLever(p, packet.UseItemOn{X: 0, Y: -61, Z: 0, Face: 1, Sequence: 2})
	g.rsDirty = true
	g.tick += 2
	g.tickRedstone()
	g.rsDirty = true
	g.tick += 2
	g.tickPistons()
	if got := g.world.GetBlock(1, -61, 0); got != pistonRet["north"] {
		t.Fatalf("活塞应收回: %d", got)
	}
	if got := g.world.GetBlock(1, -61, -1); got != world.StateAir {
		t.Fatalf("头应消失: %d", got)
	}
}

// TestStickyPistonPull 粘活塞收回拉回方块。
func TestStickyPistonPull(t *testing.T) {
	g, p, _ := newInteractGame(t)
	g.world.SetBlock(0, -61, 0, leverOff)
	g.world.SetBlock(1, -61, 0, stickyRet["north"])
	g.world.SetBlock(1, -61, -1, world.StateDirt) // 泥土直接贴活塞面

	// 先伸出：泥土被推到 -2、头占 -1
	g.toggleLever(p, packet.UseItemOn{X: 0, Y: -61, Z: 0, Face: 1, Sequence: 1})
	g.rsDirty = true
	g.tick = 2
	g.tickRedstone()
	g.tickPistons()
	if g.world.GetBlock(1, -61, -2) != world.StateDirt {
		t.Fatalf("泥土应被推到 -2: %d", g.world.GetBlock(1, -61, -2))
	}
	// 收回：粘性把泥土从 -2 拉回 -1
	g.toggleLever(p, packet.UseItemOn{X: 0, Y: -61, Z: 0, Face: 1, Sequence: 2})
	g.rsDirty = true
	g.tick += 2
	g.tickRedstone()
	g.rsDirty = true
	g.tick += 2
	g.tickPistons()
	if got := g.world.GetBlock(1, -61, -1); got != world.StateDirt {
		t.Fatalf("粘活塞应拉回泥土到 -1: %d", got)
	}
	if got := g.world.GetBlock(1, -61, -2); got != world.StateAir {
		t.Fatal("原位应清空")
	}
}

// TestComparatorChestSignal 比较器读箱：空箱 0、放物品后信号>0、减法模式。
func TestComparatorChestSignal(t *testing.T) {
	g, p, _ := newInteractGame(t)
	ids := assets.ItemIDs(777)
	// 布局（north 朝向：输入在 +z、输出在 -z）：箱子(1,-61,1) 比较器(1,-61,0) 线(1,-61,-1) 灯(1,-61,-2)
	g.world.SetBlock(1, -61, 1, chestBlockState)
	g.world.SetBlock(1, -61, 0, comparatorOff)
	g.world.SetBlock(1, -61, -1, wireBase[0])
	g.world.SetBlock(1, -61, -2, lampOff)

	g.rsDirty = true
	g.tick = 2
	g.tickRedstone()
	g.tickComparators()
	g.rsDirty = true
	g.tick += 2
	g.tickRedstone()
	if got := g.world.GetBlock(1, -61, 0); got != comparatorOff {
		t.Fatalf("空箱比较器应无输出: %d", got)
	}
	if g.world.GetBlock(1, -61, -2) != lampOff {
		t.Fatal("空箱灯应灭")
	}

	// 往箱里放 3 个石头（27 格中 1 格 → 信号 0…… 原版 1 格满堆 ≈ 1）
	// 我们的公式 filled/total*15：1/27*15=0（取整）——改放更多观察
	pos := [3]int32{1, -61, 1}
	g.chests[pos] = &chestState{}
	for i := 0; i < 14; i++ { // 14/27*15 = 7
		g.chests[pos].slots[i] = entity.Item{ID: ids["stone"], Count: 64}
	}
	g.rsDirty = true
	g.tick += 2
	g.tickRedstone()
	g.rsDirty = true
	g.tick += 2
	g.tickComparators()
	g.rsDirty = true
	g.tick += 2
	g.tickRedstone()
	if got := g.world.GetBlock(1, -61, 0); got != comparatorOn {
		t.Fatalf("半箱比较器应点亮: %d", got)
	}
	if got := rsWirePower(g.world.GetBlock(1, -61, -1)); got != 15 {
		t.Fatalf("比较器输出线应为 15: %d", got)
	}
	if g.world.GetBlock(1, -61, -2) != lampOn {
		t.Fatal("满信号灯应亮")
	}

	// 右键切减法模式
	if !g.toggleComparator(p, packet.UseItemOn{X: 1, Y: -61, Z: 0, Face: 1, Sequence: 9}) {
		t.Fatal("右键比较器应切模式")
	}
	if got := g.world.GetBlock(1, -61, 0); got != comparatorSubOn {
		t.Fatalf("应切到减法（仍供电，因侧输入 0）: %d", got)
	}
}

// TestObserverPulse 观察者：面前方块变化 → 脉冲后自动灭。
func TestObserverPulse(t *testing.T) {
	g, p, _ := newInteractGame(t)
	// 观察者(1,-61,0 朝 north) 面前(1,-61,-1)；面前放石头触发
	g.world.SetBlock(1, -61, 0, observerOff)

	g.world.SetBlock(1, -61, -1, world.StateStone)
	g.recordBlockChange(1, -61, -1, world.StateStone)
	g.scheduleObserverCheck(1, -61, -1)
	if g.world.GetBlock(1, -61, 0) != observerOn {
		t.Fatal("面前变化观察者应脉冲点亮")
	}
	// 2 刻后自动灭
	g.tick = 4
	g.tickObservers()
	if g.world.GetBlock(1, -61, 0) != observerOff {
		t.Fatal("脉冲到期应熄灭")
	}
	_ = p
}
