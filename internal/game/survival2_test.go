package game

// 剩余生存要素的测试：容器 shift/双击/拖拽、非堆叠上限、时运/精准采集掉落、
// 溺水/火焰环境伤害、暴击。

import (
	"testing"

	"mcserver/internal/assets"
	"mcserver/internal/entity"
	"mcserver/internal/packet"
	"mcserver/internal/world"
)

// openChestFor 打开一个测试箱子并返回状态。
func openChestFor(t *testing.T, g *Game, p *entity.Player) *chestState {
	t.Helper()
	g.world.SetBlock(8, -60, 8, chestBlockState)
	if !g.tryOpenChest(p, packet.UseItemOn{X: 8, Y: -60, Z: 8}) {
		t.Fatal("打开箱子失败")
	}
	pos := g.openChest[p.EntityID]
	if pos == [4]int32{} || g.chests[pos] == nil {
		t.Fatal("箱子状态缺失")
	}
	return g.chests[pos]
}

// TestQuickMoveChestShift 箱子窗口 shift：箱→物品栏、物品栏→箱。
func TestQuickMoveChestShift(t *testing.T) {
	g, p, _ := newInteractGame(t)
	cs := openChestFor(t, g, p)
	stone, ok := assets.ItemIDs(777)["stone"]
	if !ok {
		t.Fatal("物品表缺石头")
	}
	cs.slots[0] = entity.Item{ID: stone, Count: 32}
	g.handleContainerClick(&playerContainerClickEvent{p: p,
		click: packet.ContainerClick{Window: 3, Slot: 0, Button: 0, Mode: packet.ClickModeShift}})
	if cs.slots[0].ID != 0 {
		t.Fatalf("shift 应移出箱子: %+v", cs.slots[0])
	}
	if p.Slots[36].ID != stone || p.Slots[36].Count != 32 {
		t.Fatalf("应进快捷栏首格: %+v", p.Slots[36])
	}
	// 反向：物品栏 → 箱
	g.handleContainerClick(&playerContainerClickEvent{p: p,
		click: packet.ContainerClick{Window: 3, Slot: 54, Button: 0, Mode: packet.ClickModeShift}})
	if cs.slots[0].ID != stone || cs.slots[0].Count != 32 {
		t.Fatalf("shift 应移回箱子: %+v", cs.slots[0])
	}
	if p.Slots[36].ID != 0 {
		t.Fatal("快捷栏应清空")
	}
}

// TestDragDistribute 左拖均分：光标 6 个石头拖到两个空槽各得 3。
func TestDragDistribute(t *testing.T) {
	g, p, _ := newInteractGame(t)
	cs := openChestFor(t, g, p)
	stone, _ := assets.ItemIDs(777)["stone"]
	p.Cursor = entity.Item{ID: stone, Count: 6}
	// 开始(0) → 加槽(1)×2 → 结束(2)
	g.handleContainerClick(&playerContainerClickEvent{p: p,
		click: packet.ContainerClick{Window: 3, Slot: -999, Button: 0, Mode: packet.ClickModeQuickCraft}})
	g.handleContainerClick(&playerContainerClickEvent{p: p,
		click: packet.ContainerClick{Window: 3, Slot: 5, Button: 1, Mode: packet.ClickModeQuickCraft}})
	g.handleContainerClick(&playerContainerClickEvent{p: p,
		click: packet.ContainerClick{Window: 3, Slot: 6, Button: 1, Mode: packet.ClickModeQuickCraft}})
	g.handleContainerClick(&playerContainerClickEvent{p: p,
		click: packet.ContainerClick{Window: 3, Slot: -999, Button: 2, Mode: packet.ClickModeQuickCraft}})
	if cs.slots[5].Count != 3 || cs.slots[6].Count != 3 || cs.slots[5].ID != stone {
		t.Fatalf("左拖应均分: %+v %+v", cs.slots[5], cs.slots[6])
	}
	if p.Cursor.ID != 0 {
		t.Fatalf("光标应清空: %+v", p.Cursor)
	}
}

// TestDoubleClickCollect 双击收集：两槽同类合并到光标。
func TestDoubleClickCollect(t *testing.T) {
	g, p, _ := newInteractGame(t)
	cs := openChestFor(t, g, p)
	stone, _ := assets.ItemIDs(777)["stone"]
	cs.slots[0] = entity.Item{ID: stone, Count: 30}
	cs.slots[10] = entity.Item{ID: stone, Count: 30}
	// 第一次双击：光标为空 → 拾取槽 0
	g.handleContainerClick(&playerContainerClickEvent{p: p,
		click: packet.ContainerClick{Window: 3, Slot: 0, Button: 0, Mode: packet.ClickModeDouble}})
	// 第二次：收集同类
	g.handleContainerClick(&playerContainerClickEvent{p: p,
		click: packet.ContainerClick{Window: 3, Slot: 0, Button: 0, Mode: packet.ClickModeDouble}})
	if p.Cursor.ID != stone || p.Cursor.Count != 60 {
		t.Fatalf("双击应收集到 60: %+v", p.Cursor)
	}
}

// TestNonStackableLimit 非堆叠物品：两把剑不合并、giveItem 各占一槽。
func TestNonStackableLimit(t *testing.T) {
	g, p, _ := newInteractGame(t)
	sword, ok := assets.ItemIDs(777)["iron_sword"]
	if !ok {
		t.Fatal("物品表缺铁剑")
	}
	if got := g.stackOf777(sword); got != 1 {
		t.Fatalf("铁剑堆叠上限应为 1: %d", got)
	}
	// giveItem 两把剑 → 两槽
	if n := g.giveItem(p, entity.Item{ID: sword, Count: 2}); n != 2 {
		t.Fatalf("两把剑应各占一槽放入: %d", n)
	}
	if p.Slots[36].Count != 1 || p.Slots[37].Count != 1 || p.Slots[38].ID != 0 {
		t.Fatalf("剑应分占 36/37: %+v %+v", p.Slots[36], p.Slots[37])
	}
	// 左键合并不生效
	p.Cursor = entity.Item{ID: sword, Count: 1}
	g.leftClickSlotRef(p, &p.Slots[36])
	if p.Slots[36].Count != 1 || p.Cursor.Count != 1 {
		t.Fatalf("非堆叠不应合并: %+v", p.Slots[36])
	}
}

// TestFortuneSilkDrops 时运/精准采集：钻石矿常规 1、时运 III 2-4、精准掉矿石本身。
func TestFortuneSilkDrops(t *testing.T) {
	g, p, _ := newInteractGame(t)
	ids := assets.ItemIDs(777)
	oreState := uint32(0)
	for st, name := range g.blockNames {
		if name == "diamond_ore" {
			oreState = st
			break
		}
	}
	if oreState == 0 {
		t.Fatal("方块表缺钻石矿")
	}
	diamond, diamondOre := ids["diamond"], ids["diamond_ore"]
	ironPick := ids["iron_pickaxe"]

	// 无附魔铁镐：1 颗
	p.Slots[36] = entity.Item{ID: ironPick, Count: 1}
	d := g.digDrops(p, oreState)
	if len(d) != 1 || d[0].ID != diamond || d[0].Count != 1 {
		t.Fatalf("常规应掉 1 钻石: %+v", d)
	}
	// 精准采集：掉钻石矿石本身
	p.Slots[36].Ench = []entity.EnchEntry{{Name: "silk_touch", Level: 1}}
	d = g.digDrops(p, oreState)
	if len(d) != 1 || d[0].ID != diamondOre {
		t.Fatalf("精准应掉矿石本身: %+v", d)
	}
	// 时运 III：1-4 颗（原版 ore_drops：上限 + 等级）
	p.Slots[36].Ench = []entity.EnchEntry{{Name: "fortune", Level: 3}}
	for i := 0; i < 30; i++ {
		d = g.digDrops(p, oreState)
		if len(d) != 1 || d[0].Count < 1 || d[0].Count > 4 {
			t.Fatalf("时运 III 应 1-4 颗: %+v", d)
		}
	}
}

// TestDropCountRange 掉落数量范围：红石矿 4-5。
func TestDropCountRange(t *testing.T) {
	g, p, _ := newInteractGame(t)
	ids := assets.ItemIDs(777)
	oreState := uint32(0)
	for st, name := range g.blockNames {
		if name == "redstone_ore" {
			oreState = st
			break
		}
	}
	p.Slots[36] = entity.Item{ID: ids["iron_pickaxe"], Count: 1}
	for i := 0; i < 30; i++ {
		d := g.digDrops(p, oreState)
		if len(d) != 1 || d[0].ID != ids["redstone"] || d[0].Count < 4 || d[0].Count > 5 {
			t.Fatalf("红石应 4-5: %+v", d)
		}
	}
}

// TestDrowning 溺水：头部浸水氧气递减，归零后周期掉血；出水恢复。
func TestDrowning(t *testing.T) {
	g, p, _ := newInteractGame(t)
	p.Air = maxAirSupply
	// 玩家位置放水（身体与眼部）
	g.world.SetBlock(8, -59, 8, world.StateWater)
	g.world.SetBlock(8, -58, 8, world.StateWater)
	g.tick = 1
	for i := int32(0); i < maxAirSupply; i++ {
		g.tickEnvironmental()
	}
	if p.Air != 0 {
		t.Fatalf("氧气应耗尽: %d", p.Air)
	}
	hp := p.Health
	for i := 0; i < 20; i++ {
		g.tick++
		g.tickEnvironmental()
	}
	if p.Health >= hp {
		t.Fatalf("溺水应周期掉血: %.1f → %.1f", hp, p.Health)
	}
	// 出水恢复
	g.world.SetBlock(8, -59, 8, world.StateAir)
	g.world.SetBlock(8, -58, 8, world.StateAir)
	for i := 0; i < 80; i++ {
		g.tickEnvironmental()
	}
	if p.Air != maxAirSupply {
		t.Fatalf("出水应恢复氧气: %d", p.Air)
	}
}

// TestFireDamage 着火：站在火中周期掉血；水中熄灭。
func TestFireDamage(t *testing.T) {
	g, p, _ := newInteractGame(t)
	g.world.SetBlock(8, -58, 8, world.FireMin) // 眼部高度有火
	hp := p.Health
	for i := 0; i < 11; i++ {
		g.tick++
		g.tickEnvironmental()
	}
	if p.Health >= hp {
		t.Fatalf("火焰应掉血: %.1f → %.1f", hp, p.Health)
	}
	if p.FireTicks <= 0 {
		t.Fatal("应有燃烧计时")
	}
	// 落水熄灭
	g.world.SetBlock(8, -58, 8, world.StateWater)
	g.tickEnvironmental()
	if p.FireTicks != 0 {
		t.Fatalf("水中应熄灭: %d", p.FireTicks)
	}
}

// TestCriticalHit 暴击：攻击者下落中 1.5 倍伤害并广播暴击粒子事件。
func TestCriticalHit(t *testing.T) {
	g, p, fs := newInteractGame(t)
	m := g.spawnMob("pig", 8.5, -59, 9.5)
	if m == nil {
		t.Fatal("生成生物失败")
	}
	p.Slots[36] = entity.Item{ID: assets.ItemIDs(777)["iron_sword"], Count: 1}
	p.OnGround = false
	p.FallStart = p.Y + 5 // 下落中
	fs.sent = nil
	g.handleAttack(&playerAttackEvent{p: p, target: m.eid})
	// 铁剑 6 + 暴击 1.5x = 9；猪 10 血 → 剩 1
	if m.health > 1.01 || m.health < 0.99 {
		t.Fatalf("暴击伤害应 9: %.2f", m.health)
	}
	if fs.find("minecraft:entity_event") == nil {
		t.Fatal("应广播暴击粒子 entity_event")
	}
}

// TestEnchFollowsItem 附魔跟随物品：手持附魔剑的攻击加成。
func TestEnchFollowsItem(t *testing.T) {
	g, p, _ := newInteractGame(t)
	sword := assets.ItemIDs(777)["iron_sword"]
	p.Slots[36] = entity.Item{ID: sword, Count: 1,
		Ench: []entity.EnchEntry{{Name: "sharpness", Level: 5}}}
	if b := g.enchantAttackBonus(p); b < 3 {
		t.Fatalf("手持锋利 V 应有加成: %.1f", b)
	}
	p.Slots[36].Ench = nil
	if b := g.enchantAttackBonus(p); b != 0 {
		t.Fatalf("取下附魔应无加成: %.1f", b)
	}
}

// TestZombieJumpChase 僵尸追击遇台阶跳跃。
func TestZombieJumpChase(t *testing.T) {
	g, p, _ := newInteractGame(t)
	// 地板
	for x := int32(6); x <= 12; x++ {
		for z := int32(6); z <= 12; z++ {
			g.world.SetBlock(x, -60, z, world.StateStone)
		}
	}
	// 僵尸与玩家之间放一堵一格高墙
	for z := int32(6); z <= 12; z++ {
		g.world.SetBlock(8, -59, z, world.StateStone)
	}
	m := g.spawnMob("zombie", 8.5, -59, 7.5)
	if m == nil {
		t.Fatal("生成僵尸失败")
	}
	p.X, p.Y, p.Z = 8.5, -59, 10.5
	jumped := false
	for i := 0; i < 100; i++ {
		g.tickMobs()
		if m.vy > 0 {
			jumped = true
		}
	}
	if !jumped {
		t.Fatal("遇墙僵尸应跳跃")
	}
}
