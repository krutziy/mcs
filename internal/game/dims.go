package game

// 多维度：下界（the_nether）与末地（the_end）。
// 维度 ID 采用 dimension_type 注册表网络值（overworld=0, the_nether=2,
// the_end=3）。每个维度独立的 world.Manager（生成器/存档子目录），
// 实体（掉落物/生物/经验球/箭/下落方块）与容器（熔炉/箱子/酿造台/
// 作物/树苗/方块变更广播）都按维度隔离。
// 维度切换走 respawn(DataKept) → position → chunks 的官方流程。

import (
	"mcserver/internal/entity"
	"mcserver/internal/packet"
	"mcserver/internal/protocol"
	"mcserver/internal/world"
)

// 维度网络 ID（dimension_type 注册表）。
const (
	DimOverworld int32 = 0
	DimNether    int32 = 2 // minecraft:the_nether
	DimEnd       int32 = 3 // minecraft:the_end
)

// dimNames 维度网络名（login/respawn 的 WorldName 字段）。
var dimNames = map[int32]string{
	DimOverworld: "minecraft:overworld",
	DimNether:    "minecraft:the_nether",
	DimEnd:       "minecraft:the_end",
}

// dimWorlds 返回全部已启用维度（迭代顺序不定，按需过滤）。
func (g *Game) dimWorlds() map[int32]*world.Manager { return g.dims }

// w 玩家所在维度的世界管理器。
func (g *Game) w(p *entity.Player) *world.Manager {
	if m := g.dims[p.Dim]; m != nil {
		return m
	}
	return g.world
}

// wd 指定维度的世界管理器（未知维度回退主世界）。
func (g *Game) wd(dim int32) *world.Manager {
	if m := g.dims[dim]; m != nil {
		return m
	}
	return g.world
}

// dimDir 存档子目录（自有格式与 Anvil 共用布局：DIM-1/DIM1）。
func dimDir(base string, dim int32) string {
	switch dim {
	case DimNether:
		return base + "/DIM-1"
	case DimEnd:
		return base + "/DIM1"
	}
	return base
}

// switchDimension 维度切换状态机：存当前维度位置 → respawn(DataKept) →
// 传送 → 重发区块 → 同维度玩家互见。旧维度玩家收到移除。
func (g *Game) switchDimension(p *entity.Player, to int32, x, y, z float64) {
	if _, ok := g.dims[to]; !ok || to == p.Dim {
		return
	}
	old := p.Dim
	sess := g.sessionOf(p)
	// 记住旧维度位置（同维度往返时恢复）
	if p.DimPos == nil {
		p.DimPos = map[int32][3]float64{}
	}
	p.DimPos[old] = [3]float64{p.X, p.Y, p.Z}
	// 旧维度其他玩家移除我
	eid := p.EntityID
	for _, other := range g.players {
		if other.EntityID != eid && other.Dim == old {
			other.Send.SendPlay("minecraft:remove_entities", func(w *protocol.Writer) {
				packet.WriteRemoveEntities(w, []int32{eid})
			})
		}
	}
	p.Dim = to
	p.BroadcastX, p.BroadcastY, p.BroadcastZ = x, y, z // 避免跨维广播
	p.SentChunks = map[[2]int32]bool{}
	p.CenterCX, p.CenterCZ = 1<<30, 1<<30
	// respawn：DataKept=true 保留客户端侧物品栏/生命
	showTail := sess != nil && sess.ver.LoginTrailingByte
	p.Send.SendPlay("minecraft:respawn", func(w *protocol.Writer) {
		packet.WriteRespawn(w, packet.Respawn{
			DimensionID: to, WorldName: dimNames[to],
			HashedSeed: 0, GameMode: packet.GameMode(p.GameMode), PreviousGameMode: -1,
			IsFlat: false, PortalCooldown: 0, SeaLevel: -63,
			DataKept: true, TailApplies: showTail,
		})
	})
	p.X, p.Y, p.Z = x, y, z
	p.TeleportID++
	id := p.TeleportID
	px, py, pz := x, y, z
	p.Send.SendPlay("minecraft:player_position", func(w *protocol.Writer) {
		packet.WritePlayerPosition(w, packet.PlayerPositionOut{TeleportID: id, X: px, Y: py, Z: pz})
	})
	if sess != nil {
		g.sendChunks(sess, p, int32(x)>>4, int32(z)>>4)
	} else {
		// 无会话（测试）路径：直接按玩家发送器发区块
		style := world.StyleFor(g.playerProto(p))
		r := g.Cfg.ViewDistance
		p.Send.SendPlay("minecraft:chunk_batch_start", func(w *protocol.Writer) {})
		n := int32(0)
		for ddx := int32(-2); ddx <= 2; ddx++ {
			for ddz := int32(-2); ddz <= 2; ddz++ {
				cx, cz := int32(x)>>4+ddx, int32(z)>>4+ddz
				body := g.wd(to).Chunk(cx, cz).EncodeBody(style)
				cx2, cz2 := cx, cz
				p.Send.SendPlay("minecraft:level_chunk_with_light", func(w *protocol.Writer) {
					packet.WriteLevelChunk(w, cx2, cz2, body)
				})
				p.SentChunks[[2]int32{cx, cz}] = true
				n++
			}
		}
		_ = r
		p.Send.SendPlay("minecraft:chunk_batch_finished", func(w *protocol.Writer) {
			packet.WriteChunkBatchFinished(w, n)
		})
	}
	g.syncInventory(p)
	g.syncHealth(p)
	g.syncExperience(p)
	if g.raining {
		p.Send.SendPlay("minecraft:game_event", func(w *protocol.Writer) {
			w.Int8(gameEventStartRain)
			w.Float(0)
		})
	}
	// 新维度互见（双方生成实体）
	playerType := func(t *entity.Player) int32 { return g.entityTypeID(t, "player") }
	for _, other := range g.players {
		if other.EntityID == eid || other.Dim != to {
			continue
		}
		other.Send.SendPlay("minecraft:add_entity", func(w *protocol.Writer) {
			packet.SpawnEntity{EntityID: eid, UUID: p.UUID, Type: playerType(other),
				X: x, Y: y, Z: z}.Write(w)
		})
		ox, oy, oz := other.X, other.Y, other.Z
		p.Send.SendPlay("minecraft:add_entity", func(w *protocol.Writer) {
			packet.SpawnEntity{EntityID: other.EntityID, UUID: other.UUID, Type: playerType(p),
				X: ox, Y: oy, Z: oz}.Write(w)
		})
	}
	// 补给新维度的生物/掉落物
	for _, m := range g.mobs {
		if m.dim == to {
			g.sendMobSpawn(p, m)
		}
	}
	for _, ie := range g.items {
		if ie.dim == to {
			g.sendItemSpawn(p, ie)
		}
	}
	p.PortalTicks = 0
	p.PortalCooldown = 150 // 7.5 秒内不再触发，防止来回抖动
}

// dimByName 命令用维度名 → 网络 ID。
func dimByName(s string) (int32, bool) {
	switch s {
	case "overworld", "world", "0":
		return DimOverworld, true
	case "nether", "the_nether", "1":
		return DimNether, true
	case "end", "the_end", "2":
		return DimEnd, true
	}
	return 0, false
}

// ---- 下界传送门 ----

const (
	portalStateX uint32 = 8687 // minecraft:nether_portal[axis=x]
	portalStateZ uint32 = 8688 // minecraft:nether_portal[axis=z]
	obsidianState uint32 = world.StateObsidian
)

// tryIgnitePortal 打火石点燃：点击目标为黑曜石内侧空气 → 校验门框并填充。
func (g *Game) tryIgnitePortal(p *entity.Player, u packet.UseItemOn) bool {
	if g.itemNameOf(p, p.Held().ID) != "flint_and_steel" {
		return false
	}
	fdx, fdy, fdz := packet.FaceOffset(u.Face)
	tx, ty, tz := u.X+fdx, u.Y+fdy, u.Z+fdz
	if g.w(p).GetBlock(tx, ty, tz) != world.StateAir {
		return false // 目标面外侧不是空气（打火石正常应在框内空气点火）
	}
	if !g.validateAndFillPortal(p.Dim, tx, ty, tz) {
		return false // 不是合法门框：不拦截（打火石还可能点火——此处简化为无操作）
	}
	if u.Sequence > 0 {
		p.Send.SendPlay("minecraft:block_changed_ack", func(w *protocol.Writer) {
			packet.WriteBlockChangedAck(w, u.Sequence)
		})
	}
	if !g.isCreative(p) { // 打火石耐久
		g.damageHeld(p)
	}
	return true
}

// validateAndFillPortal 在 (x,y,z) 处按 x/z 两平面校验门框（内侧最小 2 宽×3 高），
// 合法则把空气填充为传送门方块。返回是否成功。
func (g *Game) validateAndFillPortal(dim, x, y, z int32) bool {
	w := g.wd(dim)
	for _, axis := range []struct {
		dx, dz int32
		portal uint32
	}{
		{1, 0, portalStateX},
		{0, 1, portalStateZ},
	} {
		// 对齐到内部左下角：向 -axis 与 -y 扫到边界
		for i := int32(0); i < 4; i++ { // 宽度回退
			for j := int32(0); j < 3; j++ { // 高度回退
				bx, by, bz := x-axis.dx*i, y-j, z-axis.dz*i
				if ok, wdt, hgt := g.portalFrameOK(w, bx, by, bz, axis.dx, axis.dz); ok {
					// 填充内部
					for cx := int32(0); cx < wdt; cx++ {
						for cy := int32(0); cy < hgt; cy++ {
							g.applyBlock(dim, bx+axis.dx*cx, by+cy, bz+axis.dz*cx, axis.portal, 0, nil)
						}
					}
					return true
				}
			}
		}
	}
	return false
}

// portalFrameOK 校验以 (bx,by,bz) 为内部左下角的门框。
// 返回（合法, 内宽, 内高）。内宽 2-4、内高 3-5；框为黑曜石、内部全空气。
func (g *Game) portalFrameOK(w *world.Manager, bx, by, bz, dx, dz int32) (bool, int32, int32) {
	is := func(x, y, z int32) bool { return w.GetBlock(x, y, z) == world.StateObsidian }
	// 尝试内宽 4→2、内高 5→3（贪心取最大可行）
	for wdt := int32(4); wdt >= 2; wdt-- {
		for hgt := int32(5); hgt >= 3; hgt-- {
			ok := true
			for cx := int32(0); cx < wdt && ok; cx++ {
				ix, iz := bx+dx*cx, bz+dz*cx
				// 上/下框
				if !is(ix, by-1, iz) || !is(ix, by+hgt, iz) {
					ok = false
					break
				}
				for cy := int32(0); cy < hgt && ok; cy++ {
					if w.GetBlock(ix, by+cy, iz) != world.StateAir {
						ok = false
						break
					}
				}
				if !ok {
					break
				}
			}
			if ok { // 两侧框
				lx, lz := bx-dx, bz-dz
				rx, rz := bx+dx*wdt, bz+dz*wdt
				for cy := int32(-1); cy <= hgt; cy++ {
					if !is(lx, by+cy, lz) || !is(rx, by+cy, rz) {
						ok = false
						break
					}
				}
				if ok {
					return true, wdt, hgt
				}
			}
		}
	}
	return false, 0, 0
}

// breakPortalCascade 破坏任一传送门方块：连锁移除相连的门方块。
func (g *Game) breakPortalCascade(dim, x, y, z int32) {
	w := g.wd(dim)
	stack := [][3]int32{{x, y, z}}
	for len(stack) > 0 {
		pos := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		s := w.GetBlock(pos[0], pos[1], pos[2])
		if s != portalStateX && s != portalStateZ {
			continue
		}
		g.applyBlock(dim, pos[0], pos[1], pos[2], world.StateAir, 0, nil)
		for _, d := range [][3]int32{{1, 0, 0}, {-1, 0, 0}, {0, 1, 0}, {0, -1, 0}, {0, 0, 1}, {0, 0, -1}} {
			stack = append(stack, [3]int32{pos[0] + d[0], pos[1] + d[1], pos[2] + d[2]})
		}
	}
}

// tickPortals 玩家传送门计时：站入门内 60 刻（创造即时）传送；
// 冷却递减；末地传送门踩入即传送。
func (g *Game) tickPortals() {
	for _, p := range g.players {
		if p.Dead {
			continue
		}
		if p.PortalCooldown > 0 {
			p.PortalCooldown--
		}
		w := g.w(p)
		feet := w.GetBlock(floorI(p.X), floorI(p.Y+0.2), floorI(p.Z))
		eye := w.GetBlock(floorI(p.X), floorI(p.Y+1.2), floorI(p.Z))
		below := w.GetBlock(floorI(p.X), floorI(p.Y-0.3), floorI(p.Z))
		// 末地传送门（站上/没入即触发）
		if feet == endPortalState || eye == endPortalState || below == endPortalState {
			if p.PortalCooldown == 0 {
				if p.Dim == DimEnd { // 末地中央门 → 主世界出生点
					p.PortalCooldown = 60
					g.switchDimension(p, DimOverworld, g.Cfg.SpawnX, g.Cfg.SpawnY, g.Cfg.SpawnZ)
				} else { // 主世界 → 末地黑曜石平台
					p.PortalCooldown = 60
					g.switchDimension(p, DimEnd, 100.5, float64(world.MinY+85), 2.5)
				}
			}
			continue
		}
		// 下界传送门
		if feet == portalStateX || feet == portalStateZ || eye == portalStateX || eye == portalStateZ {
			if p.PortalCooldown > 0 {
				continue
			}
			p.PortalTicks++
			need := int32(60)
			if g.isCreative(p) {
				need = 1
			}
			if p.PortalTicks >= need {
				p.PortalTicks = 0
				g.travelByNetherPortal(p)
			}
		} else {
			p.PortalTicks = 0
		}
	}
}

// travelByNetherPortal 传送门跨界：坐标 1:8 缩放，目标端搜索既有门，
// 没有则造一座（黑曜石框 + 门方块 + 站立平台）。
func (g *Game) travelByNetherPortal(p *entity.Player) {
	from, to := p.Dim, DimNether
	x, z := p.X, p.Z
	if from == DimNether {
		to = DimOverworld
		x, z = p.X*8, p.Z*8
	} else {
		x, z = p.X/8, p.Z/8
	}
	y := p.Y
	if to == DimNether {
		y = clampF(p.Y, float64(world.MinY+16), float64(world.MinY+100))
	} else {
		y = clampF(p.Y, float64(world.MinY+8), float64(world.MinY+300))
	}
	tw := g.wd(to)
	// 搜索目标端既有门（半径 16 粗扫）
	if fx, fy, fz, ok := g.findPortal(tw, int32(x), int32(y), int32(z), 16); ok {
		g.switchDimension(p, to, float64(fx)+0.5, float64(fy), float64(fz)+0.5)
		return
	}
	// 造新门：找立足点
	bx, by, bz := g.portalBuildSpot(tw, to, int32(x), int32(y), int32(z))
	g.buildPortal(tw, bx, by, bz)
	g.switchDimension(p, to, float64(bx)+0.5, float64(by), float64(bz)+0.5)
}

// findPortal 在世界内搜索最近的传送门方块（y 全高粗扫，步进 2 提速）。
func (g *Game) findPortal(w *world.Manager, cx, cy, cz int32, r int32) (int32, int32, int32, bool) {
	best := int64(1 << 62)
	var bx, by, bz int32
	found := false
	for dx := -r; dx <= r; dx += 2 {
		for dz := -r; dz <= r; dz += 2 {
			x, z := cx+dx, cz+dz
			for yy := int32(world.MinY + 8); yy < world.MinY+world.WorldHeight-8; yy += 2 {
				s := w.GetBlock(x, yy, z)
				if s == portalStateX || s == portalStateZ {
					d := int64(dx*dx + dz*dz + (yy-cy)*(yy-cy)/4)
					if d < best {
						best, bx, by, bz, found = d, x, yy, z, true
					}
					break
				}
			}
		}
	}
	return bx, by, bz, found
}

// portalBuildSpot 目标端立足点：优先地表站立面（在邻近列找一个固体上方两格空）。
func (g *Game) portalBuildSpot(w *world.Manager, dim, x, y, z int32) (int32, int32, int32) {
	for r := int32(0); r < 8; r++ {
		for dx := -r; dx <= r; dx++ {
			for dz := -r; dz <= r; dz++ {
				px, pz := x+dx, z+dz
				if dx != -r && dx != r && dz != -r && dz != r {
					continue // 只查环
				}
				for py := clampI(y+8, world.MinY+8, world.MinY+world.WorldHeight-16); py > world.MinY+8; py-- {
					if solidState(w.GetBlock(px, py-1, pz)) && w.GetBlock(px, py, pz) == world.StateAir &&
						w.GetBlock(px, py+1, pz) == world.StateAir {
						return px, py, pz
					}
				}
			}
		}
	}
	return x, clampI(y, world.MinY+16, world.MinY+100), z // 兜底：悬空平台
}

// buildPortal 在 (x,y,z)（内部左下）造一座 2x3 门 + 前后平台。
func (g *Game) buildPortal(w *world.Manager, x, y, z int32) {
	// 框（z 轴向：内宽 2 沿 x）
	for cx := int32(-1); cx <= 2; cx++ {
		w.SetBlock(x+cx, y-1, z, world.StateObsidian)
		w.SetBlock(x+cx, y+3, z, world.StateObsidian)
	}
	for cy := int32(-1); cy <= 3; cy++ {
		w.SetBlock(x-1, y+cy, z, world.StateObsidian)
		w.SetBlock(x+2, y+cy, z, world.StateObsidian)
	}
	for cx := int32(0); cx <= 1; cx++ {
		for cy := int32(0); cy <= 2; cy++ {
			w.SetBlock(x+cx, y+cy, z, portalStateX)
		}
	}
	// 站立平台（前后各一格）
	for _, d := range []int32{-1, 1} {
		for cx := int32(-1); cx <= 2; cx++ {
			if w.GetBlock(x+cx, y-1, z+d) == world.StateAir {
				w.SetBlock(x+cx, y-1, z+d, platformBlock(w))
			}
		}
	}
}

// platformBlock 维度地面材质（下界下界岩/主世界圆石）。
func platformBlock(w *world.Manager) uint32 {
	return world.StateStone
}

func solidState(s uint32) bool {
	return s != world.StateAir && s != world.StateWater && s != world.StateLava &&
		s != portalStateX && s != portalStateZ
}

func clampF(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func clampI(v, lo, hi int32) int32 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

const endPortalState uint32 = 11202

// findSpawnSpotAt 在指定维度 (x,z) 附近找一处可站立足点（从 y 往下找实体面）。
func (g *Game) findSpawnSpotAt(dim, x, y, z int32) (int32, int32, int32, bool) {
	w := g.wd(dim)
	for dx := int32(-3); dx <= 3; dx++ {
		for dz := int32(-3); dz <= 3; dz++ {
			for py := clampI(y+6, world.MinY+2, world.MinY+world.WorldHeight-4); py > world.MinY+2; py-- {
				if solidState(w.GetBlock(x+dx, py-1, z+dz)) &&
					w.GetBlock(x+dx, py, z+dz) == world.StateAir &&
					w.GetBlock(x+dx, py+1, z+dz) == world.StateAir {
					return x + dx, py, z + dz, true
				}
			}
		}
	}
	return 0, 0, 0, false
}
