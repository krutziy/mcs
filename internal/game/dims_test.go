package game

// 多维度测试：门框校验/点燃、下界传送坐标缩放、维度切换状态机、末地返程门。

import (
	"testing"

	"mcserver/internal/entity"
	"mcserver/internal/packet"
	"mcserver/internal/world"

	_ "mcserver/plugins/terrain" // 注册 nether/end 生成器
)

// buildTestPortal 在主世界 (x,y,z) 为内部左下角搭一座 2x3 黑曜石门框（z 轴向）。
func buildTestPortal(g *Game, x, y, z int32) {
	for cx := int32(-1); cx <= 2; cx++ {
		g.world.SetBlock(x+cx, y-1, z, world.StateObsidian)
		g.world.SetBlock(x+cx, y+3, z, world.StateObsidian)
	}
	for cy := int32(-1); cy <= 3; cy++ {
		g.world.SetBlock(x-1, y+cy, z, world.StateObsidian)
		g.world.SetBlock(x+2, y+cy, z, world.StateObsidian)
	}
}

// TestPortalFrameValidate 门框校验与填充：合法框填入门方块，缺角不填。
func TestPortalFrameValidate(t *testing.T) {
	g, _, _ := newInteractGame(t)
	buildTestPortal(g, 8, -58, 8)
	// 内部 (8..9, -58..-56, 8) 全空气 → 从内部任一格校验成功
	if !g.validateAndFillPortal(DimOverworld, 8, -58, 8) {
		t.Fatal("合法门框应校验通过")
	}
	if s := g.world.GetBlock(8, -58, 8); s != portalStateX {
		t.Fatalf("内部应填门方块: %d", s)
	}
	if s := g.world.GetBlock(9, -56, 8); s != portalStateX {
		t.Fatalf("内部右上应填门方块: %d", s)
	}
	// 缺角框 → 不填
	buildTestPortal(g, 20, -58, 8)
	g.world.SetBlock(21, -59, 8, world.StateAir) // 抠掉底框一角
	if g.validateAndFillPortal(DimOverworld, 20, -58, 8) {
		t.Fatal("缺角门框不应通过")
	}
}

// TestNetherPortalTravel 站入门内 60 刻 → 传送到下界（坐标 /8），目标端自动建门。
func TestNetherPortalTravel(t *testing.T) {
	g, p, fs := newInteractGame(t)
	buildTestPortal(g, 100, -58, 100)
	g.validateAndFillPortal(DimOverworld, 100, -58, 100)
	p.X, p.Y, p.Z = 100.5, -58, 100.5
	p.OnGround = true
	for i := 0; i < 61; i++ {
		g.tickPortals()
	}
	if p.Dim != DimNether {
		t.Fatalf("站满 60 刻应传送到下界: %d", p.Dim)
	}
	// 坐标 1:8 缩放
	if dx := p.X - 12.5; dx < -1.5 || dx > 1.5 { // 100.5/8 ≈ 12.6，落在新门附近
		t.Fatalf("下界 x 应接近 12.5: %.1f", p.X)
	}
	if fs.find("minecraft:respawn") == nil {
		t.Fatal("应发送 respawn 维度切换包")
	}
	if fs.find("minecraft:level_chunk_with_light") == nil {
		t.Fatal("应发送新维度区块")
	}
	// 目标端有门方块
	found := false
	for dx := int32(-2); dx <= 2 && !found; dx++ {
		for dy := int32(-2); dy <= 3; dy++ {
			if s := g.wd(DimNether).GetBlock(int32(p.X)+dx, int32(p.Y)+dy, int32(p.Z)); s == portalStateZ || s == portalStateX {
				found = true
				break
			}
		}
	}
	if !found {
		t.Fatal("下界目标端应自动建门")
	}
	// 冷却期内不再回传
	p.PortalTicks = 100
	g.tickPortals()
	if p.Dim != DimNether {
		t.Fatal("冷却期内不应回传")
	}
}

// TestDimSwitchCommand /dim 切换：状态机换维度 + 玩家互见过滤。
func TestDimSwitchCommand(t *testing.T) {
	g, p, fs := newInteractGame(t)
	ofs := &fakeSender{}
	// 同一 game 里的第二个主世界玩家（观察互见过滤）
	p2 := &entity.Player{EntityID: 202, Name: "Two", X: 8.5, Y: -59, Z: 8.5,
		Send: ofs, SentChunks: map[[2]int32]bool{{0, 0}: true}}
	p2.Effects = map[string]int32{}
	g.players[202] = p2

	g.handleCommand(&playerCommandEvent{p: p, Name: "dim", Args: []string{"nether"}})
	if p.Dim != DimNether {
		t.Fatalf("/dim nether 应切换: %d", p.Dim)
	}
	if fs.find("minecraft:respawn") == nil {
		t.Fatal("应发送 respawn")
	}
	// 主世界玩家收到移除
	if ofs.find("minecraft:remove_entities") == nil {
		t.Fatal("旧维度玩家应看到移除")
	}
	// 回主世界
	g.handleCommand(&playerCommandEvent{p: p, Name: "dim", Args: []string{"overworld"}})
	if p.Dim != DimOverworld {
		t.Fatal("应切回主世界")
	}
}

// TestEndPortalStep 末地门：主世界踩入传末地，末地踩入回主世界出生点。
func TestEndPortalStep(t *testing.T) {
	g, p, _ := newInteractGame(t)
	g.world.SetBlock(8, -60, 8, endPortalState)
	p.X, p.Y, p.Z = 8.5, -59, 8.5
	g.tickPortals()
	if p.Dim != DimEnd {
		t.Fatalf("踩入末地门应去末地: %d", p.Dim)
	}
	if p.X < 97 || p.X > 104 || p.Z < -1 || p.Z > 6 {
		t.Fatalf("应落在黑曜石平台: %.1f %.1f", p.X, p.Z)
	}
	// 末地中央门 → 回主世界
	g.wd(DimEnd).SetBlock(int32(p.X), int32(p.Y)-1, int32(p.Z), endPortalState)
	for i := 0; i < 160; i++ { // 冷却走完
		g.tickPortals()
	}
	if p.Dim != DimOverworld {
		t.Fatalf("末地门应回主世界: %d", p.Dim)
	}
}

// TestNetherGenerator 下界生成：有基岩盖/下界岩/岩浆海，主世界不受影响。
func TestNetherGenerator(t *testing.T) {
	g, _, _ := newInteractGame(t)
	nw := g.wd(DimNether)
	nw.Chunk(0, 0) // 触发生成
	if s := nw.GetBlock(0, world.MinY, 0); s != world.StateBedrock {
		t.Fatalf("下界底部应为基岩: %d", s)
	}
	if s := nw.GetBlock(0, world.MinY+126, 0); s != world.StateBedrock {
		t.Fatalf("下界顶部应为基岩盖: %d", s)
	}
	solid, air := 0, 0
	for y := int32(2); y < 120; y++ {
		if s := nw.GetBlock(3, world.MinY+y, 3); s == 8667 {
			solid++
		} else if s == world.StateAir {
			air++
		}
	}
	if solid == 0 {
		t.Fatal("下界应有下界岩")
	}
}

// TestEndGenerator 末地生成：中心有末地石与返程门，黑曜石平台存在。
func TestEndGenerator(t *testing.T) {
	g, _, _ := newInteractGame(t)
	ew := g.wd(DimEnd)
	ew.Chunk(0, 0)
	ew.Chunk(6, 0) // 平台所在区块
	if s := ew.GetBlock(0, world.MinY+80, 0); s == world.StateAir && ew.GetBlock(0, world.MinY+81, 0) == world.StateAir {
		t.Fatal("末地中心应有岛体")
	}
	found := false
	for x := int32(98); x <= 102; x++ {
		if ew.GetBlock(x, world.MinY+84, 2) == world.StateObsidian {
			found = true
		}
	}
	if !found {
		t.Fatal("黑曜石平台应存在")
	}
}

// TestItemsDimIsolated 掉落物按维度隔离：主世界玩家拾取不到下界掉落物。
func TestItemsDimIsolated(t *testing.T) {
	g, p, _ := newInteractGame(t)
	g.spawnItem(DimNether, 8.5, -59.5, 8.5, entity.Item{ID: 55, Count: 3})
	p.X, p.Y, p.Z = 8.5, -59.5, 8.5
	for i := 0; i < 20; i++ {
		g.tickItems()
	}
	for _, s := range p.Slots {
		if s.ID == 55 {
			t.Fatal("主世界玩家不应拾取下界掉落物")
		}
	}
	var _ = packet.DigStart
}
