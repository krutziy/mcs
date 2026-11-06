package game

// 原版对齐批次测试：工具类别挖掘、特殊掉落、弓、TNT/火、朝向、
// 双箱、寻路、末影龙战斗。

import (
	"testing"

	"mcserver/internal/assets"
	"mcserver/internal/entity"
	"mcserver/internal/packet"
	"mcserver/internal/world"
)

// TestToolClassDigSpeed 工具类别匹配：斧头砍原木快、空手慢。
func TestToolClassDigSpeed(t *testing.T) {
	g, p, _ := newInteractGame(t)
	ids := assets.ItemIDs(777)
	logState := uint32(0)
	for st, name := range g.blockNames {
		if name == "oak_log" {
			logState = st
			break
		}
	}
	if logState == 0 {
		t.Fatal("缺 oak_log")
	}
	if g.toolClass[logState] != 1 { // 1 = axe
		t.Fatalf("原木应为斧类: %d", g.toolClass[logState])
	}
	// 空手：硬度 2 × 30 / 1 = 60 刻
	bare := g.digExpectedTicks(p, logState)
	if bare < 55 || bare > 65 {
		t.Fatalf("空手砍原木应 ~60 刻: %d", bare)
	}
	// 铁斧：速度 6 → 2×30/6 = 10 刻
	p.Slots[36] = entity.Item{ID: ids["iron_axe"], Count: 1}
	axe := g.digExpectedTicks(p, logState)
	if axe != 10 {
		t.Fatalf("铁斧砍原木应 10 刻: %d", axe)
	}
	// 镐砍原木：类别不匹配 → 60 刻
	p.Slots[36] = entity.Item{ID: ids["iron_pickaxe"], Count: 1}
	pick := g.digExpectedTicks(p, logState)
	if pick != bare {
		t.Fatalf("镐砍原木应与空手同速: %d vs %d", pick, bare)
	}
}

// TestGravelFlintDrop 砾石掉落：燧石约 10%（大量采样）、其余砾石。
func TestGravelFlintDrop(t *testing.T) {
	g, p, _ := newInteractGame(t)
	flint := 0
	n := 3000
	for i := 0; i < n; i++ {
		d := g.digDrops(p, world.StateGravel)
		if len(d) == 1 && d[0].ID == g.itemID777("flint") {
			flint++
		}
	}
	if flint < n*7/100 || flint > n*14/100 {
		t.Fatalf("燧石概率应约 10%%: %d/%d", flint, n)
	}
}

// TestBowChargeFire 弓：蓄力 20 刻 → 松手发射；消耗箭。
func TestBowChargeFire(t *testing.T) {
	g, p, _ := newInteractGame(t)
	ids := assets.ItemIDs(777)
	bow := g.itemFor(p, ids["bow"])
	arrow := g.itemFor(p, ids["arrow"])
	p.Slots[36] = entity.Item{ID: bow, Count: 1}
	p.Slots[37] = entity.Item{ID: arrow, Count: 5}

	g.handleUseItem(&playerUseItemEvent{p: p})
	if p.BowTicks != 0 {
		t.Fatalf("应开始蓄力: %d", p.BowTicks)
	}
	for i := 0; i < 25; i++ {
		g.tickBowCharging()
	}
	if p.BowTicks < 20 {
		t.Fatal("应已拉满")
	}
	g.handleReleaseUse(p)
	if len(g.arrows) != 1 {
		t.Fatalf("应射出 1 支箭: %d", len(g.arrows))
	}
	for _, ar := range g.arrows {
		if ar.dmg < 4.9 || ar.crit != true {
			t.Fatalf("满蓄力应 ~5 伤+暴击: %.1f crit=%v", ar.dmg, ar.crit)
		}
	}
	if p.Slots[37].Count != 4 {
		t.Fatalf("箭应消耗: %d", p.Slots[37].Count)
	}
}

// TestTntIgniteExplode TNT：打火石点燃 → 80 刻爆炸破坏方块。
func TestTntIgniteExplode(t *testing.T) {
	g, p, _ := newInteractGame(t)
	ids := assets.ItemIDs(777)
	p.Slots[36] = entity.Item{ID: g.itemFor(p, ids["flint_and_steel"]), Count: 1}
	// 地面 + TNT
	g.world.SetBlock(8, -60, 8, world.StateStone)
	g.world.SetBlock(8, -59, 8, tntState)
	if !g.tryIgniteTnt(p, packet.UseItemOn{X: 8, Y: -59, Z: 8, Face: 1, Sequence: 7}) {
		t.Fatal("打火石应点燃 TNT")
	}
	if g.world.GetBlock(8, -59, 8) != world.StateAir {
		t.Fatal("TNT 方块应转为实体")
	}
	if len(g.tnts) != 1 {
		t.Fatal("应生成引信实体")
	}
	for i := 0; i < 85; i++ {
		g.tickTnts()
	}
	if len(g.tnts) != 0 {
		t.Fatal("引信应走完")
	}
	if g.world.GetBlock(8, -60, 8) != world.StateAir {
		t.Fatal("TNT 下方应被炸毁")
	}
}

// TestFireIgniteAndBurnOut 火方块：点燃 → 寿命到期自熄。
func TestFireIgniteAndBurnOut(t *testing.T) {
	g, p, _ := newInteractGame(t)
	ids := assets.ItemIDs(777)
	p.Slots[36] = entity.Item{ID: g.itemFor(p, ids["flint_and_steel"]), Count: 1}
	g.world.SetBlock(8, -60, 8, world.StateStone)
	if !g.tryIgniteFire(p, packet.UseItemOn{X: 8, Y: -60, Z: 8, Face: 1, Sequence: 9}) {
		t.Fatal("应点燃火")
	}
	if got := g.world.GetBlock(8, -59, 8); got < world.FireMin {
		t.Fatalf("应放置火方块: %d", got)
	}
	// 寿命最长 300 刻 → 走完必熄
	for i := 0; i < 320; i++ {
		g.tickFires()
	}
	if got := g.world.GetBlock(8, -59, 8); got != world.StateAir {
		t.Fatalf("火应自熄: %d", got)
	}
}

// TestFacingPlacement 熔炉朝向：站在北面向南放 → facing=south 状态。
func TestFacingPlacement(t *testing.T) {
	g, p, _ := newInteractGame(t)
	ids := assets.ItemIDs(777)
	// 找熔炉物品的默认状态
	defState := g.itemBlocks[777][g.itemFor(p, ids["furnace"])]
	if defState == 0 {
		t.Fatal("缺熔炉映射")
	}
	p.Yaw = 0 // 面朝北 → 方块面向南
	s := g.faceState(p, defState)
	if s == defState {
		t.Fatal("朝向应改变状态")
	}
	full := assets.BlockNamesFull(777)[s]
	if !contains(full, "facing=south") {
		t.Fatalf("应朝南: %s", full)
	}
}

// TestDoubleChest 双箱：相邻放置 → 54 格共享。
func TestDoubleChest(t *testing.T) {
	g, p, _ := newInteractGame(t)
	g.world.SetBlock(8, -60, 8, chestBlockState)
	g.world.SetBlock(9, -60, 8, chestBlockState)
	posA := [4]int32{DimOverworld, 8, -60, 8}
	posB := [4]int32{DimOverworld, 9, -60, 8}
	g.chests[posA] = &chestState{}
	g.chests[posB] = &chestState{}
	g.chests[posA].slots[0] = entity.Item{ID: 1, Count: 5}
	g.linkLargeChest(posB)
	if !g.chests[posA].large || g.chests[posA] != g.chests[posB] {
		t.Fatal("应合并为共享 54 格双箱")
	}
	// 双箱窗口布局：槽 53 → 第二半末格；槽 54 → 玩家主背包第一格
	cs := g.chests[posA]
	if ref := chestSlotRef(cs, p, 53); ref != &cs.slots[53] {
		t.Fatal("双箱槽 53 应指向第二半")
	}
	if ref := chestSlotRef(cs, p, 54); ref != &p.Slots[9] {
		t.Fatal("双箱槽 54 应指向玩家背包")
	}
	// 挖掉一半：两格方块都消失
	g.dropChestContents(posA)
	if g.world.GetBlock(9, -60, 8) != world.StateAir {
		t.Fatal("另一半方块应一并消失")
	}
}

// TestPathfindAroundWall 寻路：绕墙到达目标。
func TestPathfindAroundWall(t *testing.T) {
	g, _, _ := newInteractGame(t)
	// 地板 12x12
	for x := int32(0); x < 12; x++ {
		for z := int32(0); z < 12; z++ {
			g.world.SetBlock(x, -60, z, world.StateStone)
		}
	}
	// 中间竖墙（留 x=10 缺口）
	for z := int32(1); z < 12; z++ {
		g.world.SetBlock(5, -59, z, world.StateStone)
	}
	path := g.findPathXZ(DimOverworld, 2, -59, 5, 8, 10, 12)
	if len(path) == 0 {
		t.Fatal("应找到绕行路径")
	}
	// 终点应为 (8,·) 列
	last := path[len(path)-1]
	if last.x != 8 {
		t.Fatalf("路径应到 8: %+v", last)
	}
}

// TestDragonFight 末影龙：进末地激活 → 水晶减伤 → 灭水晶后杀死 → 蛋+经验。
func TestDragonFight(t *testing.T) {
	g, p, _ := newInteractGame(t)
	p.Dim = DimEnd
	g.ensureDragonFight()
	if g.dragon == nil {
		t.Fatal("应激活龙战")
	}
	if len(g.crystals) == 0 {
		t.Fatal("应有水晶")
	}
	// 水晶在场：伤害减半
	g.damageDragon(10, p)
	if hp := g.dragon.health; hp < 194.9 || hp > 195.1 {
		t.Fatalf("水晶在场应减半（195）: %.1f", hp)
	}
	// 清空水晶 → 全额伤害并击杀
	for cid, c := range g.crystals {
		g.destroyCrystal(c, p)
		delete(g.crystals, cid)
	}
	g.damageDragon(200, p)
	if g.dragon != nil {
		t.Fatal("龙应死亡")
	}
	if !g.dragonDefeated {
		t.Fatal("应标记已击败")
	}
	// 经验球存在
	if len(g.orbs) == 0 {
		t.Fatal("应掉经验球")
	}
	// 龙蛋在中心柱顶
	found := false
	for y := int32(world.MinY + 60); y < world.MinY+120; y++ {
		if g.wd(DimEnd).GetBlock(0, y, 0) == 11212 {
			found = true
		}
	}
	if !found {
		t.Fatal("应放置龙蛋")
	}
	// 再次进入不再激活
	g.ensureDragonFight()
	if g.dragon != nil {
		t.Fatal("已击败后不应再激活")
	}
}
