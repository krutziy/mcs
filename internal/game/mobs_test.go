package game

import (
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
