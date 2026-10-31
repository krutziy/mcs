package game

// 要塞/末影之眼/喷溅药水/唱片机/音符盒/钓鱼/持久化附魔 测试。

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"mcserver/internal/assets"
	"mcserver/internal/entity"
	"mcserver/internal/packet"
	"mcserver/internal/world"

	"mcserver/plugins/terrain"
)

// TestStrongholdGeneration 要塞生成：石砖室 + 12 框环（eye=false）+ 岩浆池。
func TestStrongholdGeneration(t *testing.T) {
	sx, sz := terrain.StrongholdPos(20261010)
	w := world.NewManager(world.NewGenerator("noise", 20261010))
	// 触发生成门环可能横跨的 2x2 区块
	for dx := int32(-1); dx <= 1; dx++ {
		for dz := int32(-1); dz <= 1; dz++ {
			w.Chunk(sx>>4+dx, sz>>4+dz)
		}
	}
	ry := int32(world.MinY + 30)
	if s := w.GetBlock(sx, ry, sz); s != 105 { // 岩浆
		t.Fatalf("中心应为岩浆: %d", s)
	}
	frames := 0
	for dx := int32(-2); dx <= 2; dx++ {
		for dz := int32(-2); dz <= 2; dz++ {
			if s := w.GetBlock(sx+dx, ry+1, sz+dz); s >= 11207 && s <= 11210 {
				frames++
			}
		}
	}
	if frames != 12 {
		t.Fatalf("应有 12 个门框: %d", frames)
	}
	if s := w.GetBlock(sx, ry-2, sz); s != 449 { // 石砖地板
		t.Fatalf("地板应为石砖: %d", s)
	}
}

// TestEyeFramePortal 框放末影之眼 → 12 框全满激活传送门。
func TestEyeFramePortal(t *testing.T) {
	g, p, _ := newInteractGame(t)
	// 直接搭门环（等价要塞生成结果）
	cx, cz := int32(8), int32(8)
	y := int32(-60)
	for dx := int32(-2); dx <= 2; dx++ {
		for dz := int32(-2); dz <= 2; dz++ {
			if abs32(dx) != 2 && abs32(dz) != 2 {
				continue
			}
			if abs32(dx) == 2 && abs32(dz) == 2 {
				continue
			}
			frame := uint32(11209)
			switch {
			case dz == -2:
				frame = 11208
			case dz == 2:
				frame = 11207
			case dx == -2:
				frame = 11210
			}
			g.world.SetBlock(cx+dx, y, cz+dz, frame)
		}
	}
	g.world.SetBlock(cx, y, cz, world.StateAir)
	eye := g.itemFor(p, g.itemID777("ender_eye"))
	p.Slots[36] = entity.Item{ID: eye, Count: 64}
	// 逐框放眼
	for dx := int32(-2); dx <= 2; dx++ {
		for dz := int32(-2); dz <= 2; dz++ {
			if g.world.GetBlock(cx+dx, y, cz+dz) < 11207 {
				continue
			}
			g.handlePlace(&blockPlaceEvent{p: p,
				use: packet.UseItemOn{X: cx + dx, Y: y, Z: cz + dz, Face: 1, Sequence: 1}})
		}
	}
	// 全满 → 中央 3x3 end_portal
	n := 0
	for dx := int32(-1); dx <= 1; dx++ {
		for dz := int32(-1); dz <= 1; dz++ {
			if g.world.GetBlock(cx+dx, y, cz+dz) == 11202 {
				n++
			}
		}
	}
	if n != 9 {
		t.Fatalf("传送门应填充 9 格: %d", n)
	}
	if p.Slots[36].Count != 52 {
		t.Fatalf("应消耗 12 只眼: %d", p.Slots[36].Count)
	}
}

// TestEnderEyeThrow 投掷末影之眼：生成实体并朝要塞方向飞。
func TestEnderEyeThrow(t *testing.T) {
	g, p, _ := newInteractGame(t)
	eye := g.itemFor(p, g.itemID777("ender_eye"))
	p.Slots[36] = entity.Item{ID: eye, Count: 3}
	p.X, p.Z = 100, 100 // 要塞离得远（默认种子 0 时 StrongholdPos 距原点 ~800+）
	if !g.throwEnderEye(p) {
		t.Fatal("应投出末影之眼")
	}
	if len(g.thrown) != 1 {
		t.Fatal("应有实体")
	}
	for _, tp := range g.thrown {
		if tp.kind != "ender_eye" {
			t.Fatal("kind 应为 ender_eye")
		}
		// 60 刻后消散/掉落
		for i := 0; i < 61; i++ {
			g.tickThrown()
		}
	}
	if len(g.thrown) != 0 {
		t.Fatal("60 刻后应消散")
	}
}

// TestSplashPotionEffect 喷溅药水：命中后范围内玩家获得效果。
func TestSplashPotionEffect(t *testing.T) {
	g, p, _ := newInteractGame(t)
	tp := &thrownProjectile{kind: "splash_potion", dim: DimOverworld,
		x: p.X, y: p.Y, z: p.Z, potion: "swiftness"}
	g.splashPotionEffect(tp)
	if p.Effects["speed"] <= 0 {
		t.Fatal("范围内应获得速度效果")
	}
	// 范围外无效果
	p2 := &entity.Player{EntityID: 300, X: p.X + 20, Y: p.Y, Z: p.Z, Send: &fakeSender{}}
	p2.Effects = map[string]int32{}
	g.players[300] = p2
	tp2 := &thrownProjectile{kind: "splash_potion", dim: DimOverworld,
		x: p.X, y: p.Y, z: p.Z, potion: "strength"}
	g.splashPotionEffect(tp2)
	if p2.Effects["strength"] > 0 {
		t.Fatal("范围外不应获得效果")
	}
}

// TestPoisonBrewAndDamage 毒药水酿造 + 中毒伤害（玩家/生物）。
func TestPoisonBrewAndDamage(t *testing.T) {
	g, p, _ := newInteractGame(t)
	if got := g.brewing[[2]string{"awkward", "spider_eye"}]; got != "poison" {
		t.Fatalf("蛛眼应酿出毒药: %q", got)
	}
	// 玩家中毒掉血止于半心
	p.Effects["poison"] = 250
	p.Health = 10
	for i := 0; i < 25; i++ {
		g.tick++
		g.tickEnvironmental()
	}
	if p.Health != 9 {
		t.Fatalf("中毒应每 25 刻 1 伤: %.1f", p.Health)
	}
	// 生物中毒
	m := g.spawnMob("pig", 8.5, -59, 8.5)
	m.poison = 26 // 减 1 后 25 的倍数即时掉血
	hp := m.health
	g.tick = 25
	g.tickMobs()
	if m.health >= hp {
		t.Fatalf("生物中毒应掉血: %.1f", m.health)
	}
}

// TestWeaknessHalvesMelee 虚弱减半近战。
func TestWeaknessHalvesMelee(t *testing.T) {
	g, p, _ := newInteractGame(t)
	ids := assets.ItemIDs(777)
	p.Slots[36] = entity.Item{ID: ids["iron_sword"], Count: 1}
	base := g.attackDamage(p)
	p.Effects["weakness"] = 100
	m := g.spawnMob("pig", 8.5, -59, 9.5)
	hp := m.health
	g.handleAttack(&playerAttackEvent{p: p, target: m.eid})
	if dmg := hp - m.health; dmg > base*0.55+0.1 || dmg < base*0.45 {
		t.Fatalf("虚弱应减半近战: base=%.1f dmg=%.1f", base, dmg)
	}
}

// TestJukeboxPlayEject 唱片机：放碟记录 + 取碟退回。
func TestJukeboxPlayEject(t *testing.T) {
	g, p, _ := newInteractGame(t)
	jukeboxState := g.itemBlocks[777][g.itemFor(p, g.itemID777("jukebox"))]
	if jukeboxState == 0 {
		t.Skip("无唱片机映射")
	}
	g.world.SetBlock(8, -60, 8, jukeboxState)
	disc := g.itemFor(p, g.itemID777("music_disc_cat"))
	p.Slots[36] = entity.Item{ID: disc, Count: 1}
	if !g.tryUseJukebox(p, packet.UseItemOn{X: 8, Y: -60, Z: 8, Face: 1}) {
		t.Fatal("放碟应拦截")
	}
	if g.jukeboxes[[4]int32{DimOverworld, 8, -60, 8}] != g.itemID777("music_disc_cat") {
		t.Fatal("唱片机应记录碟片")
	}
	// 空手取碟
	p.Slots[36] = entity.Item{}
	g.tryUseJukebox(p, packet.UseItemOn{X: 8, Y: -60, Z: 8, Face: 1})
	if p.Slots[36].ID != disc {
		t.Fatal("取碟应退回碟片")
	}
}

// TestNoteBlockTune 音符盒右键调音：note+1 且状态切换。
func TestNoteBlockTune(t *testing.T) {
	g, p, _ := newInteractGame(t)
	g.world.SetBlock(8, -60, 8, noteBlockState[0])
	if !g.tryTuneNoteBlock(p, packet.UseItemOn{X: 8, Y: -60, Z: 8, Face: 1}) {
		t.Fatal("调音应拦截")
	}
	if got := g.world.GetBlock(8, -60, 8); got != noteBlockState[1] {
		t.Fatalf("应升到 note=1: %d", got)
	}
	// note 24 → 回 0
	g.world.SetBlock(8, -60, 8, noteBlockState[24])
	g.tryTuneNoteBlock(p, packet.UseItemOn{X: 8, Y: -60, Z: 8, Face: 1})
	if got := g.world.GetBlock(8, -60, 8); got != noteBlockState[0] {
		t.Fatalf("24 应回 0: %d", got)
	}
}

// TestFishingCatch 抛竿 → 浮标落水 → 咬钩 → 收线得鱼。
func TestFishingCatch(t *testing.T) {
	g, p, _ := newInteractGame(t)
	rod := g.itemFor(p, g.itemID777("fishing_rod"))
	p.Slots[36] = entity.Item{ID: rod, Count: 1}
	p.X, p.Y, p.Z = 8.5, -58, 8.5
	// 水面在脚下
	g.world.SetBlock(8, -59, 8, world.StateWater)
	if !g.tryCastRod(p) {
		t.Fatal("应抛竿")
	}
	b := g.fishing[p.EntityID]
	if b == nil {
		t.Fatal("应有浮标")
	}
	// 浮标飞到水里
	b.x, b.y, b.z = 8.5, -58.9, 8.5
	b.vx, b.vy, b.vz = 0, -0.1, 0
	for i := 0; i < 30; i++ {
		g.tickFishing()
	}
	if !b.inWater {
		t.Fatal("浮标应入水")
	}
	// 咬钩窗口
	b.biteCd = 0
	b.biteCd = -1
	if !g.tryCastRod(p) {
		t.Fatal("应收线")
	}
	if g.fishing[p.EntityID] != nil {
		t.Fatal("收线后浮标应清除")
	}
	// 得鱼（任意物品入包或经验）
	if len(g.orbs) == 0 && p.TotalXP == 0 {
		t.Fatal("应获得经验或鱼")
	}
}

// TestChestPersistenceEnch 箱子持久化带附魔/耐久。
func TestChestPersistenceEnch(t *testing.T) {
	dir := t.TempDir()
	g, _, _ := newInteractGame(t)
	g.Cfg.SaveDir = dir
	ids := assets.ItemIDs(777)
	pos := [4]int32{DimOverworld, 1, 2, 3}
	sword := entity.Item{ID: ids["iron_sword"], Count: 1, Damage: 7,
		Ench: []entity.EnchEntry{{Name: "sharpness", Level: 2}}}
	g.chests[pos] = &chestState{}
	g.chests[pos].slots[0] = sword
	g.saveChests()
	// 磁盘上是完整对象
	raw, _ := os.ReadFile(filepath.Join(dir, "chests.json"))
	var probe []map[string]any
	if json.Unmarshal(raw, &probe) != nil || len(probe) != 1 {
		t.Fatalf("存档格式异常")
	}
	g2, _, _ := newInteractGame(t)
	g2.Cfg.SaveDir = dir
	g2.loadChests()
	if cs := g2.chests[pos]; cs == nil || cs.slots[0].Damage != 7 ||
		len(cs.slots[0].Ench) != 1 || cs.slots[0].Ench[0].Level != 2 {
		t.Fatalf("附魔/耐久应持久化: %+v", g2.chests[pos].slots[0])
	}
	// 旧格式（[id,count]）兼容
	old := `[{"pos":[0,5,6,7],"slots":[[1,10],[0,0]]}]`
	_ = os.WriteFile(filepath.Join(dir, "chests.json"), []byte(old), 0o644)
	g3, _, _ := newInteractGame(t)
	g3.Cfg.SaveDir = dir
	g3.loadChests()
	if cs := g3.chests[[4]int32{0, 5, 6, 7}]; cs == nil || cs.slots[0].ID != 1 || cs.slots[0].Count != 10 {
		t.Fatalf("旧格式应兼容: %+v", cs.slots[0])
	}
}

// TestDragonRespawn 末地水晶右键出口基座 → 重生龙。
func TestDragonRespawn(t *testing.T) {
	g, p, _ := newInteractGame(t)
	p.Dim = DimEnd
	g.ensureDragonFight()
	for cid, c := range g.crystals {
		g.destroyCrystal(c, p)
		delete(g.crystals, cid)
	}
	g.damageDragon(200, p)
	if !g.dragonDefeated || g.dragon != nil {
		t.Fatal("前置：龙应已击败")
	}
	// 出口门基座放水晶
	w := g.wd(DimEnd)
	w.Chunk(0, 0)
	var bedrockPos packet.UseItemOn
	found := false
	for y := int32(world.MinY + 60); y < world.MinY+120 && !found; y++ {
		if w.GetBlock(0, y, 0) == world.StateBedrock {
			bedrockPos = packet.UseItemOn{X: 0, Y: y, Z: 0, Face: 1}
			found = true
		}
	}
	if !found {
		t.Fatal("应找到出口基座")
	}
	crystal := g.itemFor(p, g.itemID777("end_crystal"))
	p.Slots[36] = entity.Item{ID: crystal, Count: 5}
	// 4 水晶重生（原版行为）
	for i := 0; i < 3; i++ {
		if !g.tryRespawnDragon(p, bedrockPos) {
			t.Fatal("前 3 次应放置装饰水晶")
		}
		if g.dragon != nil {
			t.Fatal("第 4 次前不应重生")
		}
	}
	if !g.tryRespawnDragon(p, bedrockPos) {
		t.Fatal("第 4 次应触发龙重生")
	}
	if g.dragon == nil || len(g.crystals) == 0 {
		t.Fatal("龙与水晶应重生")
	}
	if p.Slots[36].Count != 1 {
		t.Fatal("应消耗 4 水晶")
	}
}
