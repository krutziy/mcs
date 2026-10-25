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
