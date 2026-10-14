package game

import (
	"testing"

	"mcserver/internal/assets"
	"mcserver/internal/entity"
	"mcserver/internal/packet"
	"mcserver/internal/version"
	"mcserver/internal/world"
)

// TestDamageAndDeath 伤害扣血、归零死亡、重生恢复。
func TestDamageAndDeath(t *testing.T) {
	g, p, fs := newTestGame(packet.ModeSurvival)

	g.damage(p, 6, damageFall)
	if p.Health != 14 {
		t.Fatalf("扣血后应为 14，实际 %.1f", p.Health)
	}
	if fs.find("minecraft:set_health") == nil || fs.find("minecraft:damage_event") == nil {
		t.Error("应发送 set_health 与 damage_event")
	}

	g.damage(p, 99, damageGeneric)
	if !p.Dead || p.Health != 0 {
		t.Fatalf("应死亡: dead=%v health=%.1f", p.Dead, p.Health)
	}
	if fs.find("minecraft:entity_event") == nil {
		t.Error("应广播死亡动画")
	}

	g.handleRespawn(p)
	if p.Dead || p.Health != 20 || p.Food != 20 {
		t.Fatalf("重生应恢复状态: dead=%v health=%.1f food=%d", p.Dead, p.Health, p.Food)
	}
	if fs.find("minecraft:respawn") == nil || fs.find("minecraft:player_position") == nil {
		t.Error("重生应发送 respawn 与传送")
	}
}

// TestCreativeImmune 创造模式免疫伤害。
func TestCreativeImmune(t *testing.T) {
	g, p, fs := newTestGame(packet.ModeCreative)
	g.damage(p, 10, damageFall)
	if p.Health != 20 || len(fs.sent) != 0 {
		t.Fatalf("创造模式不应受伤: health=%.1f packets=%d", p.Health, len(fs.sent))
	}
}

// TestFallDamage 摔落：3 格内免伤、更高按 距离-3 伤害。
func TestFallDamage(t *testing.T) {
	g, p, fs := newTestGame(packet.ModeSurvival)

	// 下落 3 格：免伤
	g.trackFall(p, -70, false) // 起跳点记录
	g.trackFall(p, -67, false)
	g.trackFall(p, -67, true) // 落地
	if p.Health != 20 {
		t.Fatalf("3 格摔落不应受伤: %.1f", p.Health)
	}

	// 下落 8 格：5 点伤害（从 -67 掉到 -75）
	fs.sent = nil
	g.trackFall(p, -67, false)
	g.trackFall(p, -75, true)
	if p.Health != 15 {
		t.Fatalf("8 格摔落应扣 5: %.1f", p.Health)
	}
	if fs.find("minecraft:damage_event") == nil {
		t.Error("摔落应发 damage_event")
	}
}

// TestHungerDrainAndRegen 饥饿：移动消耗食物、食物足回血、饥饿见底掉血。
func TestHungerDrainAndRegen(t *testing.T) {
	g, p, _ := newTestGame(packet.ModeSurvival)

	// 长距离奔跑耗尽饱和与食物（简化系数 0.1/格）
	for i := 0; i < 200; i++ {
		g.exhaustMovement(p, 1, 0, true) // 每次 0.1 累积
	}
	if p.Food != 20 {
		t.Fatalf("200 格奔跑不应耗尽 5 饱和+食物（0.1/格×200=20 点，饱和 5 只能抵 5）: food=%d", p.Food)
	}
	for i := 0; i < 3000; i++ {
		g.exhaustMovement(p, 1, 0, true)
	}
	if p.Food >= 20 {
		t.Fatalf("长跑应消耗食物: food=%d", p.Food)
	}

	// 回血：受伤 + 食物充足 → 每 4 秒 +1
	p.Food, p.Saturation, p.Health = 20, 0, 10
	g.tick = 80
	g.tickStats()
	if p.Health != 11 || p.Food != 19 {
		t.Fatalf("应回血耗食物: health=%.1f food=%d", p.Health, p.Food)
	}

	// 饥饿伤害：食物 0 → 每周期 1 点伤害，止于 1
	p.Food, p.Health = 0, 5
	g.tickStats()
	if p.Health != 4 {
		t.Fatalf("饥饿应扣血: %.1f", p.Health)
	}
	p.Health = 1
	g.tickStats()
	if p.Health != 1 {
		t.Fatalf("饥饿伤害应止于半心: %.1f", p.Health)
	}
}

// TestVoidDamage 虚空伤害。
func TestVoidDamage(t *testing.T) {
	g, p, _ := newTestGame(packet.ModeSurvival)
	p.Y = -200
	g.tick = 80
	g.tickStats()
	if p.Health >= 20 {
		t.Fatalf("虚空应扣血: %.1f", p.Health)
	}
}

// TestEating 进食：手持面包恢复饥饿并消耗物品。
func TestEating(t *testing.T) {
	g, p, fs := newTestGame(packet.ModeSurvival)
	g.entityTypes[777] = assets.EntityTypes(777)
	bread, ok := assets.ItemIDs(777)["bread"]
	if !ok {
		t.Fatal("物品表缺面包")
	}
	if g.itemNames[777] == nil {
		g.itemNames[777] = map[int32]string{}
	}
	for name, id := range assets.ItemIDs(777) {
		g.itemNames[777][id] = name
	}
	p.Food, p.Saturation = 10, 0
	p.Slots[36] = entity.Item{ID: bread, Count: 2}

	g.handleUseItem(&playerUseItemEvent{p: p, sequence: 9})
	if p.Food != 15 {
		t.Fatalf("面包应 +5 食物: %d", p.Food)
	}
	if p.Slots[36].Count != 1 {
		t.Fatalf("应消耗 1 个面包: %d", p.Slots[36].Count)
	}
	if fs.find("minecraft:block_changed_ack") == nil {
		t.Error("use_item 应回 ack")
	}

	// 满饥饿不吃
	p.Food = 20
	g.handleUseItem(&playerUseItemEvent{p: p})
	if p.Slots[36].Count != 1 {
		t.Error("满饥饿不应进食")
	}
}

// TestDeadPlayerActionsDropped 死亡玩家不能挖掘/放置。
func TestDeadPlayerActionsDropped(t *testing.T) {
	g, p, fs := newTestGame(packet.ModeSurvival)
	g.world.SetBlock(8, -60, 8, world.StateStone)
	p.Dead = true
	g.handleDig(&blockDigEvent{p: p, act: packet.PlayerAction{Status: packet.DigFinish, X: 8, Y: -60, Z: 8}})
	if got := g.world.GetBlock(8, -60, 8); got != world.StateStone {
		t.Error("死亡玩家不应能挖掘")
	}
	if len(fs.sent) != 0 {
		t.Errorf("死亡玩家不应发包: %d", len(fs.sent))
	}
	_ = version.SuiteFor(777)
}
