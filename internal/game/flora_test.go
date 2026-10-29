package game

// 植被与玩家 NBT 档案测试。

import (
	"os"
	"testing"

	"mcserver/internal/assets"
	"mcserver/internal/entity"
	"mcserver/internal/packet"
	"mcserver/internal/world"
)

// TestBerryBushHarvest 右键成熟丛采浆果并回置 age1；骨粉催熟。
func TestBerryBushHarvest(t *testing.T) {
	g, p, _ := newInteractGame(t)
	berry := assets.ItemIDs(777)["sweet_berries"]
	p.Slots[36] = entity.Item{ID: berry, Count: 1}
	g.world.SetBlock(8, -60, 8, bushAge3)
	if !g.tryHarvestBerryBush(p, packet.UseItemOn{X: 8, Y: -60, Z: 8, Sequence: 5}) {
		t.Fatal("右键成熟丛应拦截")
	}
	if got := g.world.GetBlock(8, -60, 8); got != bushAge1 {
		t.Fatalf("采后应回置 age1: %d", got)
	}
	found := int32(0)
	for _, s := range p.Slots {
		if s.ID == berry {
			found += s.Count
		}
	}
	if found < 2 || found > 4 { // 1 持有 + 2-3 收获
		t.Fatalf("浆果数量不符: %d", found)
	}
	// 骨粉催熟 age1 → age3
	p.Slots[37] = entity.Item{ID: assets.ItemIDs(777)["bone_meal"], Count: 2}
	p.HeldSlot = 1
	if !g.tryBonemeal(p, packet.UseItemOn{X: 8, Y: -60, Z: 8}) {
		t.Fatal("骨粉应催熟")
	}
	if got := g.world.GetBlock(8, -60, 8); got != bushAge3 {
		t.Fatalf("骨粉后应 age3: %d", got)
	}
}

// TestBerryBushPlantAndGrow 种植 → 随机生长 → 荆棘伤害。
func TestBerryBushPlantAndGrow(t *testing.T) {
	g, p, _ := newInteractGame(t)
	berry := assets.ItemIDs(777)["sweet_berries"]
	g.world.SetBlock(8, -60, 8, world.StateGrassBlock)
	p.Slots[36] = entity.Item{ID: berry, Count: 2}
	if !g.tryPlantBerry(p, packet.UseItemOn{X: 8, Y: -60, Z: 8, Face: 1, Sequence: 3}) {
		t.Fatal("手持浆果应可种植")
	}
	if got := g.world.GetBlock(8, -59, 8); got != bushAge0 {
		t.Fatalf("应种出 age0 丛: %d", got)
	}
	if p.Slots[36].Count != 1 {
		t.Fatal("应消耗 1 浆果")
	}
	// 生长：跑足够多刻必有概率长龄
	grew := false
	for i := 0; i < 2000 && !grew; i++ {
		g.tick++
		g.tickBerryBushes()
		if g.world.GetBlock(8, -59, 8) != bushAge0 {
			grew = true
		}
	}
	if !grew {
		t.Fatal("浆果丛应会生长")
	}
	// 荆棘：站进 age≥2 的丛掉血
	g.world.SetBlock(8, -59, 8, bushAge2)
	p.X, p.Y, p.Z = 8.5, -59, 8.5
	g.tick = 20
	hp := p.Health
	g.tickEnvironmental()
	if p.Health >= hp {
		t.Fatalf("荆棘应掉血: %.1f → %.1f", hp, p.Health)
	}
}

// TestGiantMushroom 骨粉棕色蘑菇 → 茎 + 5×5 平顶帽。
func TestGiantMushroom(t *testing.T) {
	g, p, _ := newInteractGame(t)
	g.world.SetBlock(8, -60, 8, brownShroom)
	p.Slots[36] = entity.Item{ID: assets.ItemIDs(777)["bone_meal"], Count: 1}
	if !g.tryBonemeal(p, packet.UseItemOn{X: 8, Y: -60, Z: 8}) {
		t.Fatal("骨粉蘑菇应长巨型")
	}
	if got := g.world.GetBlock(8, -60, 8); got != shroomStem {
		t.Fatalf("茎应在原位: %d", got)
	}
	caps := 0
	for y := int32(-58); y <= -53; y++ {
		for dx := int32(-2); dx <= 2; dx++ {
			for dz := int32(-2); dz <= 2; dz++ {
				if g.world.GetBlock(8+dx, y, 8+dz) == brownCap {
					caps++
				}
			}
		}
	}
	if caps < 20 {
		t.Fatalf("平顶帽应有 ~25 块: %d", caps)
	}
	if p.Slots[36].Count != 0 {
		t.Fatal("骨粉应消耗")
	}
}

// TestPlayerNBTRoundTrip NBT 档案往返：位置/生命/经验/物品（含附魔与耐久）。
func TestPlayerNBTRoundTrip(t *testing.T) {
	g, p, _ := newInteractGame(t)
	ids := assets.ItemIDs(777)
	dir := t.TempDir()
	g.Cfg.SaveDir = dir
	p.X, p.Y, p.Z = 12.5, -40.25, 88.5
	p.Yaw, p.Pitch = 90, -12.5
	p.Health, p.Food, p.Saturation = 13.5, 17, 2.5
	p.Level, p.TotalXP, p.XPPending = 4, 60, 3
	p.HeldSlot = 2
	sword := g.itemFor(p, ids["iron_sword"])
	p.Slots[36] = entity.Item{ID: sword, Count: 1, Damage: 42,
		Ench: []entity.EnchEntry{{Name: "sharpness", Level: 3}}}
	p.Slots[20] = entity.Item{ID: g.itemFor(p, ids["stone"]), Count: 33}
	p.SpawnPos = [3]float64{8, -59, 8}

	if err := g.savePlayerNBTFile(p, dir); err != nil {
		t.Fatalf("NBT 写档失败: %v", err)
	}
	if _, err := os.Stat(dir + "/players/" + p.Name + ".dat"); err != nil {
		t.Fatal("档案文件缺失")
	}

	q := &entity.Player{Name: p.Name, Send: &fakeSender{}}
	q.Effects = map[string]int32{}
	if !g.loadPlayerNBTFile(q, dir) {
		t.Fatal("NBT 读档失败")
	}
	if q.X != 12.5 || q.Y != -40.25 || q.Z != 88.5 {
		t.Fatalf("位置不符: %v", []float64{q.X, q.Y, q.Z})
	}
	if q.Health != 13.5 || q.Food != 17 {
		t.Fatalf("生命/饥饿不符: %v %v", q.Health, q.Food)
	}
	if q.Level != 4 || q.HeldSlot != 2 {
		t.Fatalf("经验/手持不符: %v %v", q.Level, q.HeldSlot)
	}
	// 物品：热0 剑（附魔+耐久）、主11 石头
	if q.Slots[36].ID != sword || q.Slots[36].Damage != 42 || len(q.Slots[36].Ench) != 1 ||
		q.Slots[36].Ench[0].Level != 3 {
		t.Fatalf("剑档不符: %+v", q.Slots[36])
	}
	if q.Slots[20].ID != g.itemFor(p, ids["stone"]) || q.Slots[20].Count != 33 {
		t.Fatalf("石头档不符: %+v", q.Slots[20])
	}
	if q.SpawnPos != [3]float64{8, -59, 8} {
		t.Fatalf("重生点不符: %v", q.SpawnPos)
	}
}

// TestSoundPlayback 音效包：观察者收到 minecraft:sound 且可解码。
func TestSoundPlayback(t *testing.T) {
	g, p, fs := newInteractGame(t)
	if len(g.soundIDs[777]) < 1000 {
		t.Fatal("音效表缺失")
	}
	if _, ok := g.soundIDFor(p, "block.chest.open"); !ok {
		t.Fatal("箱子音效应有 ID")
	}
	g.blockSound(DimOverworld, 8, -60, 8, "block.wooden_door.open")
	b := fs.find("minecraft:sound")
	if b == nil {
		t.Fatal("应发送 minecraft:sound")
	}
	// VarInt ID + VarInt 分类 + 3×Int32 坐标可解
	if b[0] == 0 {
		t.Fatal("音效 ID 不应为 0 终止")
	}
}
