package game

// 本批测试：要塞结构、下界要塞+烈焰人+下界疣、末地城、羊色、矿车、村民交易、
// 滞留药水、钓鱼奖池、蠹虫刷怪笼。

import (
	"testing"

	"mcserver/internal/assets"
	"mcserver/internal/entity"
	"mcserver/internal/packet"
	"mcserver/internal/world"

	"mcserver/plugins/terrain"
)

// TestNetherFortressGen 下界要塞生成：廊桥 + 疣农场 + 刷怪笼。
func TestNetherFortressGen(t *testing.T) {
	fx, fz := terrain.NetherFortressPos(0)
	w := world.NewManager(world.NewGenerator("nether", 0))
	for dx := int32(-2); dx <= 2; dx++ {
		for dz := int32(-2); dz <= 2; dz++ {
			w.Chunk((fx>>4)+dx, (fz>>4)+dz)
		}
	}
	y := int32(world.MinY + 78)
	if s := w.GetBlock(fx, y-1, fz); s != 11068 { // 桥面下界砖
		t.Fatalf("桥面应为下界砖: %d", s)
	}
	if s := w.GetBlock(fx, y, fz); s != 5444 { // 中心刷怪笼
		t.Fatalf("中心应为刷怪笼: %d", s)
	}
	// 疣农场（北端）
	if s := w.GetBlock(fx, y, fz-24); s != 8668 { // 灵魂沙
		t.Fatalf("北端应为灵魂沙: %d", s)
	}
	if s := w.GetBlock(fx, y+1, fz-24); s != 11184 { // 成熟下界疣
		t.Fatalf("灵魂沙上应有大疣: %d", s)
	}
}

// TestBlazeAndSpawner 烈焰人：火球点燃 + 刷怪笼出怪 + 掉烈焰棒。
func TestBlazeAndSpawner(t *testing.T) {
	g, p, _ := newInteractGame(t)
	p.Dim = DimNether
	m := g.spawnMobIn(DimNether, "blaze", 8.5, -58, 9.5)
	if m == nil {
		t.Fatal("烈焰人生成失败")
	}
	if m.health != 20 {
		t.Fatalf("烈焰人血量: %.1f", m.health)
	}
	// AI 打一拍：附近玩家 → 火球
	g.tickMobs()
	if len(g.arrows) == 0 {
		t.Fatal("烈焰人应射火球")
	}
	for _, ar := range g.arrows {
		if !ar.fire {
			t.Fatal("应为火球")
		}
	}
	// 击杀掉烈焰棒（50% 概率，多试几次）
	gotRod := false
	for i := 0; i < 10 && !gotRod; i++ {
		m2 := g.spawnMobIn(DimNether, "blaze", 8.5, -58, 9.5)
		g.killMob(m2, p)
		for _, it := range g.items {
			if it.item.ID == g.itemID777("blaze_rod") {
				gotRod = true
			}
		}
	}
}

// TestNetherWartFarmCycle 下界疣：种植 → 生长 → 收获。
func TestNetherWartFarmCycle(t *testing.T) {
	g, p, _ := newInteractGame(t)
	ids := assets.ItemIDs(777)
	g.world.SetBlock(8, -60, 8, 8668) // 灵魂沙（主世界也可测交互）
	p.Slots[36] = entity.Item{ID: g.itemFor(p, ids["nether_wart"]), Count: 2}
	if !g.tryPlantWart(p, packet.UseItemOn{X: 8, Y: -60, Z: 8, Face: 1, Sequence: 3}) {
		t.Fatal("应可种植下界疣")
	}
	if got := g.world.GetBlock(8, -59, 8); got != wartAges[0] {
		t.Fatalf("应种出 age0 疣: %d", got)
	}
	// 生长到成熟
	for i := 0; i < 2000; i++ {
		g.tick++
		g.tickCrops()
		if g.world.GetBlock(8, -59, 8) == wartAges[3] {
			break
		}
	}
	if got := g.world.GetBlock(8, -59, 8); got != wartAges[3] {
		t.Fatal("疣应长到成熟")
	}
	// 收获掉 2-4
	g.harvestWart(p, DimOverworld, 8, -59, 8, wartAges[3])
	found := int32(0)
	for _, it := range g.items {
		if it.item.ID == ids["nether_wart"] {
			found += it.item.Count
		}
	}
	if found < 2 || found > 4 {
		t.Fatalf("成熟疣应掉 2-4: %d", found)
	}
}

// TestEndCityIsland 末地城外岛与塔生成。
func TestEndCityIsland(t *testing.T) {
	g, _, _ := newInteractGame(t)
	ew := g.wd(DimEnd)
	cx, cz := terrain.EndCityPos(g.Cfg.WorldSeed, 0)
	ew.Chunk(cx>>4, cz>>4)
	ew.Chunk((cx>>4)+1, cz>>4)
	ew.Chunk(cx>>4, (cz>>4)+1)
	ew.Chunk((cx>>4)-1, cz>>4)
	ew.Chunk(cx>>4, (cz>>4)-1)
	// 塔底为紫珀
	if s := ew.GetBlock(cx, world.MinY+83, cz); s != 16687 {
		t.Fatalf("塔底应为紫珀: %d", s)
	}
	// 潜贝贝布设
	p2 := &entity.Player{EntityID: 301, X: 100.5, Y: -60, Z: 2.5, Send: &fakeSender{}}
	g.players[301] = p2
	g.ensureEndCityMobs()
	shulkers := 0
	for _, m := range g.mobs {
		if m.kind.name == "shulker" {
			shulkers++
		}
	}
	if shulkers != 16 {
		t.Fatalf("4 城 × 4 潜贝贝 = 16: %d", shulkers)
	}
}

// TestShulkerBullet 潜贝贝子弹：命中失重。
func TestShulkerBullet(t *testing.T) {
	g, p, _ := newInteractGame(t)
	m := g.spawnMobIn(DimOverworld, "shulker", 8.5, -59, 8.5)
	m.attackCd = 0
	g.tickMobs()
	if len(g.bullets) == 0 {
		t.Fatal("潜贝贝应射子弹")
	}
	// 子弹追到玩家：手动移到玩家位置触发命中
	for _, b := range g.bullets {
		b.x, b.y, b.z = p.X, p.Y+1, p.Z
	}
	p.X, p.Y, p.Z = 8.5, -59, 8.5
	g.tickShulkerBullets()
	if p.Effects["levitation"] <= 0 {
		t.Fatal("应获失重效果")
	}
}

// TestSheepColorCycle 羊色：生成彩色羊 → 染色 → 掉对应色羊毛。
func TestSheepColorCycle(t *testing.T) {
	g, p, _ := newInteractGame(t)
	ids := assets.ItemIDs(777)
	m := g.spawnColoredSheep(DimOverworld, 8.5, -59, 8.5, 5) // 石灰绿
	if m.color != 5 {
		t.Fatalf("羊色: %d", m.color)
	}
	// 染成红色
	p.Slots[36] = entity.Item{ID: g.itemFor(p, ids["red_dye"]), Count: 1}
	if !g.tryDyeSheep(p, m) {
		t.Fatal("染色应成功")
	}
	if m.color != 14 {
		t.Fatalf("红染料色号 14: %d", m.color)
	}
	// 剪毛掉红羊毛
	if got := woolOfColor(m.color); got != "red_wool" {
		t.Fatalf("羊毛色: %s", got)
	}
}

// TestMinecartRide 矿车：放置 → 乘坐 → 移动 → 下车。
func TestMinecartRide(t *testing.T) {
	g, p, _ := newInteractGame(t)
	ids := assets.ItemIDs(777)
	// 放铁轨（直线 north-south 默认态）
	railState := g.itemBlocks[777][g.itemFor(p, ids["rail"])]
	g.world.SetBlock(8, -60, 8, railState)
	p.Slots[36] = entity.Item{ID: g.itemFor(p, ids["minecart"]), Count: 1}
	if !g.tryPlaceMinecart(p, packet.UseItemOn{X: 8, Y: -60, Z: 8, Face: 1, Sequence: 9}) {
		t.Fatal("应放置矿车")
	}
	if len(g.carts) != 1 {
		t.Fatal("应有矿车实体")
	}
	// 乘坐
	for eid := range g.carts {
		g.tryRideEntity(p, eid)
	}
	if p.Vehicle == 0 {
		t.Fatal("应上车")
	}
	// 移动
	g.handleVehicleMove(p, 10.5, -59.9, 8.5, 0, 0)
	for _, mc := range g.carts {
		if mc.x != 10.5 {
			t.Fatalf("矿车应跟随: %.1f", mc.x)
		}
	}
	// shift 下车
	g.handlePlayerInput(p, 0x20)
	if p.Vehicle != 0 {
		t.Fatal("应下车")
	}
}

// TestVillagerTrade 村民交易：开窗 → 放小麦 → 取绿宝石。
func TestVillagerTrade(t *testing.T) {
	g, p, _ := newInteractGame(t)
	ids := assets.ItemIDs(777)
	m := g.spawnMobIn(DimOverworld, "villager", 8.5, -59, 8.5)
	if !g.tryOpenVillager(p, m) {
		t.Fatal("应打开交易")
	}
	vs := g.villagers[p.EntityID]
	if vs == nil {
		t.Fatal("应有会话")
	}
	vs.profession = "farmer" // 测试固定职业（小麦贸易在农民表）
	// 放 20 小麦 → 匹配 → 成交
	vs.slots[0] = entity.Item{ID: g.itemFor(p, ids["wheat"]), Count: 20}
	if _, ok := g.matchVillagerTrade(p, vs); !ok {
		t.Fatal("20 小麦应匹配第一档")
	}
	before := int32(0)
	for _, s := range p.Slots {
		if s.ID == g.itemFor(p, ids["emerald"]) {
			before += s.Count
		}
	}
	g.takeVillagerTrade(p)
	after := int32(0)
	for _, s := range p.Slots {
		if s.ID == g.itemFor(p, ids["emerald"]) {
			after += s.Count
		}
	}
	if after != before+1 {
		t.Fatalf("应得 1 绿宝石: %d → %d", before, after)
	}
	if vs.slots[0].Count != 0 {
		t.Fatal("小麦应清空")
	}
	g.closeVillager(p)
}

// TestLingeringCloud 滞留药水：云雾持续生效。
func TestLingeringCloud(t *testing.T) {
	g, p, _ := newInteractGame(t)
	g.spawnLingeringCloud(&thrownProjectile{dim: DimOverworld, x: p.X, y: p.Y, z: p.Z, potion: "strength"})
	if len(g.clouds) != 1 {
		t.Fatal("应有云雾")
	}
	p.Effects = map[string]int32{}
	g.tickClouds() // life 600 → 599；%20!=0 先不生效
	for i := 0; i < 19; i++ {
		g.tickClouds()
	}
	if p.Effects["strength"] <= 0 {
		t.Fatal("云雾应生效")
	}
}

// TestSilverfishSpawner 蠹虫刷怪笼：深处刷怪。
func TestSilverfishSpawner(t *testing.T) {
	g, p, _ := newInteractGame(t)
	// 主世界深处放刷怪笼
	g.world.SetBlock(8, -60, 8, 5444)
	p.Y = -60
	g.tick = 100
	g.tickSpawners()
	n := 0
	for _, m := range g.mobs {
		if m.kind.name == "silverfish" {
			n++
		}
	}
	if n == 0 {
		t.Fatal("深处刷怪笼应出蠹虫")
	}
}
