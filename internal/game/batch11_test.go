package game

// 批次11测试：压力板/按钮/活板门/栅栏门、漏斗传输、潜影盒、发射器、马、村民职业。

import (
	"testing"

	"mcserver/internal/assets"
	"mcserver/internal/entity"
	"mcserver/internal/packet"
	"mcserver/internal/world"
)

// TestPressurePlate 压力板：玩家站立通电、离开断电。
func TestPressurePlate(t *testing.T) {
	g, p, _ := newInteractGame(t)
	g.world.SetBlock(8, -60, 8, plateStoneOff)
	g.trackPlate(DimOverworld, 8, -60, 8, plateStoneOff)
	p.X, p.Y, p.Z = 8.5, -59, 8.5
	p.OnGround = true
	g.tick = 5
	g.tickPlates()
	if got := g.world.GetBlock(8, -60, 8); got != plateStoneOn {
		t.Fatalf("站立应通电: %d", got)
	}
	// 离开
	p.X = 20
	g.tick = 10
	g.tickPlates()
	if got := g.world.GetBlock(8, -60, 8); got != plateStoneOff {
		t.Fatalf("离开应断电: %d", got)
	}
}

// TestButtonPulse 按钮：按下 20 刻后自动弹起。
func TestButtonPulse(t *testing.T) {
	g, p, _ := newInteractGame(t)
	g.world.SetBlock(8, -60, 8, buttonOff)
	for st := range fullStateNames {
		if fullStateNames[st] == "minecraft:stone_button[face=floor,facing=north,powered=false]" {
			g.world.SetBlock(8, -60, 8, st)
			break
		}
	}
	if !g.pressButton(p, packet.UseItemOn{X: 8, Y: -60, Z: 8, Face: 1, Sequence: 5}) {
		t.Fatal("应按下按钮")
	}
	s := g.world.GetBlock(8, -60, 8)
	if s%2 != 1 {
		t.Fatalf("应 powered=true: %d", s)
	}
	for i := 0; i < 21; i++ {
		g.tick++
		g.tickButtons()
	}
	if s2 := g.world.GetBlock(8, -60, 8); s2%2 != 0 {
		t.Fatalf("应弹起 powered=false: %d", s2)
	}
}

// TestTrapdoorGate 活板门/栅栏门右键开关。
func TestTrapdoorGate(t *testing.T) {
	g, p, _ := newInteractGame(t)
	// 活板门：默认关（open=false）
	for st, full := range fullStateNames {
		if full == "minecraft:oak_trapdoor[facing=north,half=bottom,open=false,powered=false,waterlogged=false]" {
			g.world.SetBlock(8, -60, 8, st)
			break
		}
	}
	if !g.toggleTrapdoorOrGate(p, packet.UseItemOn{X: 8, Y: -60, Z: 8, Face: 1}) {
		t.Fatal("活板门应可开关")
	}
	open := g.world.GetBlock(8, -60, 8)
	if !contains(fullStateNames[open], "open=true") {
		t.Fatalf("应 open=true: %s", fullStateNames[open])
	}
	// 再点关回
	g.toggleTrapdoorOrGate(p, packet.UseItemOn{X: 8, Y: -60, Z: 8, Face: 1})
	if !contains(fullStateNames[g.world.GetBlock(8, -60, 8)], "open=false") {
		t.Fatal("应翻回 open=false")
	}
	// 栅栏门
	for st, full := range fullStateNames {
		if full == "minecraft:oak_fence_gate[facing=north,in_wall=false,open=false,powered=false]" {
			g.world.SetBlock(9, -60, 8, st)
			break
		}
	}
	if !g.toggleTrapdoorOrGate(p, packet.UseItemOn{X: 9, Y: -60, Z: 8, Face: 1}) {
		t.Fatal("栅栏门应可开关")
	}
	if !contains(fullStateNames[g.world.GetBlock(9, -60, 8)], "open=true") {
		t.Fatal("栅栏门应 open=true")
	}
}

// TestHopperTransfer 漏斗：箱 → 漏斗 → 下方箱。
func TestHopperTransfer(t *testing.T) {
	g, _, _ := newInteractGame(t)
	ids := assets.ItemIDs(777)
	// 上箱(8,-58,8) → 漏斗(8,-59,8) → 下箱(8,-60,8)
	g.world.SetBlock(8, -58, 8, chestBlockState)
	g.world.SetBlock(8, -59, 8, hopperBlock)
	g.world.SetBlock(8, -60, 8, chestBlockState)
	top := [4]int32{DimOverworld, 8, -58, 8}
	hp := [4]int32{DimOverworld, 8, -59, 8}
	bot := [4]int32{DimOverworld, 8, -60, 8}
	g.chests[top] = &chestState{}
	g.hoppers[hp] = &hopperState{}
	g.chests[bot] = &chestState{}
	g.chests[top].slots[0] = entity.Item{ID: ids["stone"], Count: 5}

	for i := 0; i < 20; i++ {
		g.tick += 4
		g.tickHoppers()
	}
	// 5 石头应全部落到下箱
	if got := g.chests[bot].slots[0].Count; got != 5 {
		t.Fatalf("下箱应收到 5: %d", got)
	}
	if g.hoppers[hp].slots[0].ID != 0 {
		t.Fatal("漏斗应清空")
	}
}

// TestHopperAbsorbItem 漏斗捡掉落物。
func TestHopperAbsorbItem(t *testing.T) {
	g, _, _ := newInteractGame(t)
	ids := assets.ItemIDs(777)
	g.world.SetBlock(8, -60, 8, hopperBlock)
	hp := [4]int32{DimOverworld, 8, -60, 8}
	g.hoppers[hp] = &hopperState{}
	g.spawnItem(DimOverworld, 8.5, -59.3, 8.5, entity.Item{ID: ids["dirt"], Count: 3})
	for _, ie := range g.items {
		ie.age = 20 // 跳过拾取等待
	}
	g.tick += 4
	g.tickHoppers()
	if g.hoppers[hp].slots[0].Count != 3 {
		t.Fatalf("漏斗应捡起: %+v", g.hoppers[hp].slots[0])
	}
	if len(g.items) != 0 {
		t.Fatal("掉落物应消失")
	}
}

// TestShulkerBox 潜影盒：开窗/存取/破坏掉内容。
func TestShulkerBox(t *testing.T) {
	g, p, _ := newInteractGame(t)
	g.world.SetBlock(8, -60, 8, shulkerBoxState)
	if !g.tryOpenShulker(p, packet.UseItemOn{X: 8, Y: -60, Z: 8, Face: 1}) {
		t.Fatal("应打开潜影盒")
	}
	pos := [4]int32{DimOverworld, 8, -60, 8}
	ss := g.shulkers[pos]
	if ss == nil {
		t.Fatal("应有状态")
	}
	ids := assets.ItemIDs(777)
	ss.slots[0] = entity.Item{ID: ids["diamond"], Count: 7}
	// 破坏：整盒带内容（原版行为，v26.3.17 起钻石在盒 Contents 里）
	g.dropShulkerContents(pos)
	boxFound, inBox := false, int32(0)
	for _, ie := range g.items {
		if ie.item.ID == ids["shulker_box"] {
			boxFound = true
			for _, c := range ie.item.Contents {
				if c.ID == ids["diamond"] {
					inBox += c.Count
				}
			}
		}
	}
	if !boxFound || inBox != 7 {
		t.Fatalf("应掉整盒含 7 钻: box=%v inBox=%d", boxFound, inBox)
	}
}

// TestDispenserFire 发射器：通电边沿射箭。
func TestDispenserFire(t *testing.T) {
	g, _, _ := newInteractGame(t)
	ids := assets.ItemIDs(777)
	g.world.SetBlock(8, -60, 8, dispenserState)
	pos := [4]int32{DimOverworld, 8, -60, 8}
	g.dispensers[pos] = &dispenserState8{}
	g.dispensers[pos].slots[0] = entity.Item{ID: ids["arrow"], Count: 5}
	// 通电：旁置红石块
	g.world.SetBlock(8, -60, 7, rsBlock)
	g.tick += 2
	g.tickDispensers()
	if len(g.arrows) != 1 {
		t.Fatalf("应射 1 箭: %d", len(g.arrows))
	}
	if g.dispensers[pos].slots[0].Count != 4 {
		t.Fatalf("应消耗 1 箭: %d", g.dispensers[pos].slots[0].Count)
	}
}

// TestHorseTameRide 马：驯服 → 上鞍 → 骑乘。
func TestHorseTameRide(t *testing.T) {
	g, p, _ := newInteractGame(t)
	ids := assets.ItemIDs(777)
	m := g.spawnMobIn(DimOverworld, "horse", 8.5, -59, 8.5)
	if m == nil {
		t.Fatal("马生成失败")
	}
	// 反复交互直到驯服
	tamed := false
	for i := 0; i < 30 && !tamed; i++ {
		g.tryHorseInteract(p, m)
		if g.horses[m.eid] != nil && g.horses[m.eid].tamed {
			tamed = true
		}
	}
	if !tamed {
		t.Fatal("30 次内应驯服")
	}
	// 上鞍
	p.Slots[36] = entity.Item{ID: g.itemFor(p, ids["saddle"]), Count: 1}
	g.tryHorseInteract(p, m)
	if !g.horses[m.eid].saddled {
		t.Fatal("应上鞍")
	}
	// 骑乘
	g.tryHorseInteract(p, m)
	if p.Vehicle != m.eid {
		t.Fatal("应骑上马")
	}
	if p.HorseSpeed <= 0 {
		t.Fatal("应有马速")
	}
	// shift 下车
	g.handlePlayerInput(p, 0x20)
	if p.Vehicle != 0 {
		t.Fatal("应下车")
	}
}

// TestVillagerProfessions 村民职业贸易表。
func TestVillagerProfessions(t *testing.T) {
	g, p, _ := newInteractGame(t)
	ids := assets.ItemIDs(777)
	m := g.spawnMobIn(DimOverworld, "villager", 8.5, -59, 8.5)
	if !g.tryOpenVillager(p, m) {
		t.Fatal("应打开")
	}
	vs := g.villagers[p.EntityID]
	if vs.profession == "" {
		t.Fatal("应有职业")
	}
	// 屠夫表：生猪排换绿宝石
	vs.profession = "butcher"
	vs.slots[0] = entity.Item{ID: g.itemFor(p, ids["porkchop"]), Count: 7}
	if _, ok := g.matchVillagerTrade(p, vs); !ok {
		t.Fatal("7 生猪排应匹配屠夫表")
	}
	// 职业不匹配项：农民的小麦不在屠夫表
	vs.slots[0] = entity.Item{ID: g.itemFor(p, ids["wheat"]), Count: 20}
	if _, ok := g.matchVillagerTrade(p, vs); ok {
		t.Fatal("屠夫不应收小麦")
	}
	// 图书管理员双输入：绿宝石+书 → 附魔书
	vs.profession = "librarian"
	vs.slots[0] = entity.Item{ID: g.itemFor(p, ids["emerald"]), Count: 8}
	vs.slots[1] = entity.Item{ID: g.itemFor(p, ids["book"]), Count: 1}
	if _, ok := g.matchVillagerTrade(p, vs); !ok {
		t.Fatal("8 绿宝石+1 书应匹配图书管理员表")
	}
}

// TestIronDoorRedstone 铁门徒手不可开（拦截但不翻转）。
func TestIronDoorRedstone(t *testing.T) {
	g, p, _ := newInteractGame(t)
	// 铁门 lower closed 默认态
	var ironLower uint32
	for st, full := range fullStateNames {
		if full == "minecraft:iron_door[facing=north,half=lower,hinge=left,open=false,powered=false]" {
			ironLower = st
			break
		}
	}
	if ironLower == 0 {
		t.Skip("铁门状态未找到")
	}
	g.world.SetBlock(8, -60, 8, ironLower)
	g.world.SetBlock(8, -59, 8, ironLower) // upper 用 lower 占位简化
	if !g.toggleTrapdoorOrGate(p, packet.UseItemOn{X: 8, Y: -60, Z: 8, Face: 1}) {
		t.Fatal("铁门交互应被拦截")
	}
	if got := g.world.GetBlock(8, -60, 8); got != ironLower {
		t.Fatalf("徒手不应开铁门: %d", got)
	}
}

var _ = world.StateAir
