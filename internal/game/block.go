package game

// 方块交互核心：挖掘 / 放置 / 方块变更广播与变更序列号确认（ack）。
//
// 服务端权威模型：所有方块状态只存在于 world.Manager；
// 玩家的挖掘/放置请求经事件进入主循环，应用到世界后再向能看到该区块的
// 玩家广播增量更新，并向动作发起者回 block_changed_ack（客户端用它来
// 结束本地的"幽灵方块"预测）。

import (
	"math"
	"math/rand"
	"strings"

	"mcserver/internal/assets"
	"mcserver/internal/entity"
	"mcserver/internal/packet"
	"mcserver/internal/protocol"
	"mcserver/internal/world"
)

// levelEventBlockBreak 是方块破坏粒子 + 音效的 level_event 事件 ID。
const levelEventBlockBreak int32 = 2001

// digUnbreakable 表示不可破坏方块（基岩等，硬度 < 0）的预期挖掘刻。
const digUnbreakable int32 = 1 << 30

// handleDig 处理挖掘：创造模式在 start 即时破坏，生存模式在 start 记录
// 预期挖掘时长（硬度 × 工具速度），finish 时校验耗时（<80% 视为作弊回滚）。
func (g *Game) handleDig(ev *blockDigEvent) {
	p := ev.p
	if p == nil || p.Dead {
		return
	}
	a := ev.act
	bus := BlockDig{Player: p, X: a.X, Y: a.Y, Z: a.Z, Status: a.Status, Face: a.Face, Sequence: a.Sequence}
	defer func() { g.Bus.emit(bus) }()

	start := a.Status == packet.DigStart
	finish := a.Status == packet.DigFinish
	if !start && !finish {
		if a.Status == packet.DigCancel {
			p.DigActive = false // 取消挖掘
		}
		if a.Status == packet.ReleaseUse {
			g.handleReleaseUse(p) // 松开右键：中断进食
		}
		return // 丢物品等：仅通知插件
	}
	if start && !g.isCreative(p) {
		// 生存模式 start 只记录预期时长，等 finish 再破坏
		p.DigActive = true
		p.DigX, p.DigY, p.DigZ = a.X, a.Y, a.Z
		p.DigTick, p.DigExpected = g.tick, g.digExpectedTicks(p, g.world.GetBlock(a.X, a.Y, a.Z))
		return
	}
	if finish && !g.isCreative(p) {
		// 挖掘时长校验：同一起点且耗时不足预期 80% → 回滚权威状态
		if p.DigActive && p.DigX == a.X && p.DigY == a.Y && p.DigZ == a.Z {
			p.DigActive = false
			if p.DigExpected >= digUnbreakable {
				g.rejectDig(p, a)
				return
			}
			if elapsed := g.tick - p.DigTick; elapsed*100 < int64(p.DigExpected)*80 {
				g.rejectDig(p, a)
				return
			}
		} else {
			p.DigActive = false
		}
	}
	if !g.inDigRange(p, a.X, a.Y, a.Z) {
		return
	}
	old := g.w(p).GetBlock(a.X, a.Y, a.Z)
	bus.OldState = old
	if old == world.StateAir {
		return
	}
	// 破坏粒子 + 音效（data = 被破坏方块的 ID，按观察者协议翻译），再改世界状态
	if old == portalStateX || old == portalStateZ {
		g.breakPortalCascade(p.Dim, a.X, a.Y, a.Z)
	}
	for _, v := range g.chunkViewers(p.Dim, a.X>>4, a.Z>>4) {
		data := g.stateFor(v, old)
		v.Send.SendPlay("minecraft:level_event", func(w *protocol.Writer) {
			packet.WriteLevelEvent(w, levelEventBlockBreak, a.X, a.Y, a.Z, int32(data), false)
		})
	}
	dpos := [4]int32{p.Dim, a.X, a.Y, a.Z}
	g.dropFurnaceContents(dpos)
	g.dropChestContents(dpos)
	g.dropBrewingContents(dpos)
	g.harvestWheatIfMature(p.Dim, a.X, a.Y, a.Z, old)
	g.breakBedIfHit(p.Dim, a.X, a.Y, a.Z, old)
	g.breakDoorIfHit(p.Dim, a.X, a.Y, a.Z, old)
	g.applyBlock(p.Dim, a.X, a.Y, a.Z, world.StateAir, a.Sequence, p)
	bus.Broke = true
	g.damageHeld(p)
	if xp, ok := oreXP[g.blockNames[old]]; ok && xp > 0 {
		g.spawnXPOrb(p.Dim, float64(a.X)+0.5, float64(a.Y)+0.5, float64(a.Z)+0.5, 1+rand.Int31n(xp))
	}
	g.triggerGravity(p.Dim, a.X, a.Y+1, a.Z) // 上方重力方块塌落
	g.scheduleWaterFlow(p.Dim, a.X, a.Y, a.Z) // 邻水流入
	g.scheduleLeafCheck(p.Dim, a.X, a.Y, a.Z)
	g.scheduleRedstoneUpdate(p.Dim, a.X, a.Y, a.Z)
	g.scheduleObserverCheck(p.Dim, a.X, a.Y, a.Z)
	// 生存模式：按掉落表生成掉落物（需镐方块还要材质门槛）
	if !g.isCreative(p) {
		bus.Drops = g.digDrops(p, old)
		for _, d := range bus.Drops {
			g.spawnItem(p.Dim, float64(a.X)+0.5, float64(a.Y)+0.5, float64(a.Z)+0.5, d)
		}
	}
}

// digDrops 依据官方 jar 战利品表生成的掉落定义计算掉落：数量取 [min,max] 随机，
// 时运提高上限（+等级），精准采集改掉方块本身（玻璃/矿石/草方块等）。
// 需镐方块（mineable/pickaxe）只有手持足够材质的镐才掉落。
// 返回的物品 ID 为 777 基准空间。
func (g *Game) digDrops(p *entity.Player, state uint32) []entity.Item {
	// 小麦作物由 harvestWheatIfMature 专用处理（掉落表是"任意年龄掉麦"的简化）
	for _, wa := range wheatAges {
		if state == wa {
			return nil
		}
	}
	// 多池/概率掉落的原版修正（掉落表资产只取首个常规条目）
	if name := g.blockNames[state]; name != "" {
		switch name {
		case "gravel":
			// 10% 燧石（时运 I/II/III → 14.3%/25%/100%）
			chance := float32(0.1)
			if fl := g.heldEnchantLevel(p, "fortune"); fl > 0 {
				chance = []float32{0.1, 0.14285715, 0.25, 1}[minI32(fl, 3)]
			}
			if rand.Float32() < chance {
				return []entity.Item{{ID: g.itemID777("flint"), Count: 1}}
			}
			return []entity.Item{{ID: g.itemID777("gravel"), Count: 1}}
		case "oak_leaves", "birch_leaves", "spruce_leaves", "jungle_leaves",
			"acacia_leaves", "dark_oak_leaves", "cherry_leaves", "mangrove_leaves":
			if g.heldEnchantLevel(p, "silk_touch") > 0 || g.itemNameOf(p, p.Held().ID) == "shears" {
				return []entity.Item{{ID: g.itemID777(name[:len(name)-7]), Count: 1}}
			}
			out := []entity.Item{}
			if rand.Intn(20) == 0 { // 树苗 5%
				sapling := map[string]string{"oak_leaves": "oak_sapling", "birch_leaves": "birch_sapling",
					"spruce_leaves": "spruce_sapling", "jungle_leaves": "jungle_sapling",
					"acacia_leaves": "acacia_sapling", "dark_oak_leaves": "dark_oak_sapling",
					"cherry_leaves": "cherry_sapling", "mangrove_leaves": "mangrove_propagule"}[name]
				out = append(out, entity.Item{ID: g.itemID777(sapling), Count: 1})
			}
			if rand.Intn(50) == 0 { // 木棍 2%
				out = append(out, entity.Item{ID: g.itemID777("stick"), Count: 1})
			}
			return out
		case "short_grass", "fern", "tall_grass":
			if rand.Intn(8) == 0 { // 种子 12.5%
				return []entity.Item{{ID: g.itemID777("wheat_seeds"), Count: 1}}
			}
			return nil
		}
	}
	d, ok := g.blockDrops[state]
	if !ok {
		return nil
	}
	// 需镐门槛先于精准采集（材质不够的镐即使精准也不掉）
	if mask, needsPick := g.mineable[777][state]; needsPick {
		held := g.canonItem(p, p.Held().ID)
		bit, isPick := g.pickaxes[777][held]
		if !isPick || mask&(1<<bit) == 0 {
			return nil
		}
	}
	if g.heldEnchantLevel(p, "silk_touch") > 0 && d.Silk != 0 {
		return []entity.Item{{ID: d.Silk, Count: 1}}
	}
	if d.Item == 0 {
		return nil
	}
	maxN := d.Max
	if d.Fortune {
		if fl := g.heldEnchantLevel(p, "fortune"); fl > 0 {
			maxN = d.Max + fl
		}
	}
	n := d.Min
	if maxN > d.Min {
		n += rand.Int31n(maxN - d.Min + 1)
	}
	if n <= 0 {
		return nil
	}
	return []entity.Item{{ID: d.Item, Count: n}}
}

// digExpectedTicks 按方块硬度与手持工具计算预期挖掘刻数（原版公式近似）：
// 伤害/刻 = 工具速度倍率 / 硬度；可采 = ceil(硬度×30/伤害)，不可采 ×100/30。
// 效率附魔按 等级²+1 叠加；硬度 0 即时破坏；硬度 < 0（基岩）不可破坏。
func (g *Game) digExpectedTicks(p *entity.Player, state uint32) int32 {
	name := g.blockNames[state]
	h, ok := g.hardness[name]
	if !ok {
		h = 1 // 未知方块按泥土/石头之间
	}
	if h < 0 {
		return 1 << 30 // 不可破坏（正常客户端挖不动）
	}
	if h == 0 {
		return 0
	}
	heldName := g.itemNameOf(p, p.Held().ID)
	speed := float64(1)
	if g.toolMatches(p, state, heldName) {
		speed = toolSpeed(heldName) // 类别匹配才有材质倍率
	}
	if lv := g.heldEnchantLevel(p, "efficiency"); lv > 0 {
		speed += float64(lv*lv) + 1
	}
	canHarvest := true
	if mask, needsPick := g.mineable[777][state]; needsPick {
		held := g.canonItem(p, p.Held().ID)
		bit, isPick := g.pickaxes[777][held]
		canHarvest = isPick && mask&(1<<bit) != 0
	}
	// 原版公式：秒 = 硬度 × (可采 ? 1.5 : 5) / 速度倍率 → 刻 = ×20
	base := h * 30
	if !canHarvest {
		base = h * 100
	}
	ticks := int32(math.Ceil(base / speed))
	if ticks < 1 {
		ticks = 1
	}
	if ticks < 1 {
		ticks = 1
	}
	return ticks
}

// toolMatches 手持工具类别是否匹配方块（镐↔mineable/pickaxe，斧/锹/锄 ↔ blocktoolclass）。
// 剑对蛛网等特例不在此处理；空手恒不匹配。
func (g *Game) toolMatches(p *entity.Player, state uint32, heldName string) bool {
	class, ok := heldToolClass(heldName)
	if !ok {
		return false
	}
	if class == 0 { // 镐
		_, isPickBlock := g.mineable[777][state]
		return isPickBlock
	}
	return g.toolClass[state] == class
}

// heldToolClass 物品名 → 工具类别（0镐 1斧 2锹 3锄；空手/其他 false）。
func heldToolClass(name string) (uint8, bool) {
	for _, c := range []struct {
		suffix string
		class  uint8
	}{{"_pickaxe", 0}, {"_axe", 1}, {"_shovel", 2}, {"_hoe", 3}} {
		if len(name) > len(c.suffix) && name[len(name)-len(c.suffix):] == c.suffix {
			return c.class, true
		}
	}
	return 0, false
}

// toolSpeed 工具材质 → 挖掘速度倍率（原版公开数值；镐/斧/锹/锄均适用）。
func toolSpeed(itemName string) float64 {
	switch {
	case strings.HasPrefix(itemName, "golden_"):
		return 12
	case strings.HasPrefix(itemName, "netherite_"):
		return 9
	case strings.HasPrefix(itemName, "diamond_"):
		return 8
	case strings.HasPrefix(itemName, "iron_"):
		return 6
	case strings.HasPrefix(itemName, "copper_"):
		return 5
	case strings.HasPrefix(itemName, "stone_"):
		return 4
	case strings.HasPrefix(itemName, "wooden_"):
		return 2
	case itemName == "shears":
		return 5
	}
	return 1
}

func minI32(a, b int32) int32 {
	if a < b {
		return a
	}
	return b
}

// rejectDig 挖掘被拒（时长不足/不可破坏）：回显权威方块状态并 ack。
func (g *Game) rejectDig(p *entity.Player, a packet.PlayerAction) {
	x, y, z := a.X, a.Y, a.Z
	state := g.stateFor(p, g.world.GetBlock(x, y, z))
	p.Send.SendPlay("minecraft:block_update", func(w *protocol.Writer) {
		packet.WriteBlockUpdate(w, x, y, z, state)
	})
	if a.Sequence > 0 {
		p.Send.SendPlay("minecraft:block_changed_ack", func(w *protocol.Writer) {
			packet.WriteBlockChangedAck(w, a.Sequence)
		})
	}
}

// handlePlace 处理对方块使用物品（放置）：按玩家手持物品查物品→方块表，
// 目标位置为被点击方块的面外侧，可替换目标（空气/水）时放置。
func (g *Game) handlePlace(ev *blockPlaceEvent) {
	p := ev.p
	if p == nil || p.Dead {
		return
	}
	u := ev.use
	if g.tryHarvestBerryBush(p, u) || g.tryPlantBerry(p, u) ||
		g.tryOpenCrafting(p, u) || g.tryOpenFurnace(p, u) || g.tryOpenChest(p, u) ||
		g.tryTill(p, u) || g.tryPlantSeed(p, u) || g.tryBonemeal(p, u) ||
		g.tryOpenBrewing(p, u) ||
		g.tryOpenEnchantTable(p, u) ||
		g.toggleLever(p, u) ||
		g.toggleComparator(p, u) ||
		g.tryOpenAnvil(p, u) ||
		g.trySleepBed(p, u) || g.toggleDoor(p, u) {
		return
	}
	dx, dy, dz := packet.FaceOffset(u.Face)
	tx, ty, tz := u.X+dx, u.Y+dy, u.Z+dz
	bus := BlockPlace{Player: p, X: tx, Y: ty, Z: tz,
		AgainstX: u.X, AgainstY: u.Y, AgainstZ: u.Z,
		Face: u.Face, Sequence: u.Sequence}
	defer func() { g.Bus.emit(bus) }()

	if g.tryIgniteTnt(p, u) || g.tryIgniteFire(p, u) || g.tryIgnitePortal(p, u) ||
		g.tryPlaceEyeInFrame(p, u) || g.tryRespawnDragon(p, u) ||
		g.tryTuneNoteBlock(p, u) || g.tryUseJukebox(p, u) {
		return
	}
	held := p.Held()
	if held.ID == 0 || held.Count <= 0 {
		g.rejectPlace(p, u, tx, ty, tz) // 空手：回显两处方块状态 + ack（官方行为）
		return
	}
	proto := g.playerProto(p)
	state, ok := g.itemBlocks[proto][held.ID]
	if !ok {
		g.rejectPlace(p, u, tx, ty, tz)
		return // 该物品不是方块（或该版本无映射表）
	}
	state = g.faceState(p, state) // 含 facing 属性的方块按玩家朝向放置
	if g.bedByState[state] != nil { // 床：双格放置
		if g.inDigRange(p, tx, ty, tz) {
			g.tryPlaceBed(p, u, tx, ty, tz, state)
		}
		return
	}
	if g.doorByState[state] != nil { // 门：双格放置
		if g.inDigRange(p, tx, ty, tz) {
			g.tryPlaceDoor(p, u, tx, ty, tz, state)
		}
		return
	}
	if !g.inDigRange(p, tx, ty, tz) {
		return
	}
	cur := g.w(p).GetBlock(tx, ty, tz)
	if cur != world.StateAir && cur != world.StateWater {
		g.rejectPlace(p, u, tx, ty, tz) // 目标被占：客户端会预测放置，需回滚
		return
	}
	g.applyBlock(p.Dim, tx, ty, tz, state, u.Sequence, p)
	if state >= world.FireMin && state <= world.FireMax {
		g.trackFire(p.Dim, tx, ty, tz)
	}
	bus.State, bus.ItemID = state, held.ID
	g.scheduleRedstoneUpdate(p.Dim, tx, ty, tz)
	g.scheduleObserverCheck(p.Dim, tx, ty, tz)
	g.rsDirty = g.rsDirty || isRedstoneComponent(state)
	if state == world.StateSand || state == world.StateGravel {
		g.triggerGravity(p.Dim, tx, ty, tz) // 放在悬空处 → 落下
	} else {
		g.trackSapling(p.Dim, tx, ty, tz, state)
	}
	// 生存模式：放置消耗一个手持物品
	if !g.isCreative(p) {
		s := &p.Slots[36+p.HeldSlot]
		s.Count--
		if s.Count <= 0 {
			*s = entity.Item{}
		}
		g.syncInventory(p)
	}
}

// rejectPlace 放置被拒（空手/非方块/目标被占）：回显被点击方块与目标位置的
// 服务端权威状态并 ack 序列号，客户端据此回滚本地预测的"幽灵方块"。
func (g *Game) rejectPlace(p *entity.Player, u packet.UseItemOn, tx, ty, tz int32) {
	for _, pos := range [][3]int32{{u.X, u.Y, u.Z}, {tx, ty, tz}} {
		x, y, z := pos[0], pos[1], pos[2]
		state := g.stateFor(p, g.w(p).GetBlock(x, y, z))
		p.Send.SendPlay("minecraft:block_update", func(w *protocol.Writer) {
			packet.WriteBlockUpdate(w, x, y, z, state)
		})
	}
	if u.Sequence > 0 {
		p.Send.SendPlay("minecraft:block_changed_ack", func(w *protocol.Writer) {
			packet.WriteBlockChangedAck(w, u.Sequence)
		})
	}
}

// handleHeldSlot 处理 set_carried_item（切换手持会中断挖掘计时与进食）。
func (g *Game) handleHeldSlot(ev *playerHeldSlotEvent) {
	if ev.p != nil && ev.slot >= 0 && ev.slot <= 8 {
		ev.p.HeldSlot = ev.slot
		ev.p.DigActive = false
		ev.p.EatTicks = -1
	}
}

// applyBlock 应用一次方块变更并广播：写世界 → 广播增量更新 → 光照重算
// （含光源的变更重发整区块）→ 回 ack。
func (g *Game) applyBlock(dim, x, y, z int32, state uint32, sequence int32, actor *entity.Player) {
	w := g.wd(dim)
	oldState := w.GetBlock(x, y, z)
	w.SetBlock(x, y, z, state)
	g.recordBlockChange(dim, x, y, z, state)
	g.flushBlockUpdates()
	// 光源出现/消失时重发受影响区块（含新光照数组）
	if world.IsLightSource(oldState) || world.IsLightSource(state) {
		g.resendChunkWithLight(dim, x >> 4, z >> 4)
	}
	if actor != nil && sequence > 0 {
		actor.Send.SendPlay("minecraft:block_changed_ack", func(w *protocol.Writer) {
			packet.WriteBlockChangedAck(w, sequence)
		})
	}
}

// recordBlockChange 记入待广播缓冲（同位置多次变更以最后一次为准）。
func (g *Game) recordBlockChange(dim, x, y, z int32, state uint32) {
	if g.pendingBlocks == nil {
		g.pendingBlocks = map[[4]int32]uint32{}
	}
	g.pendingBlocks[[4]int32{dim, x, y, z}] = state
}

// flushBlockUpdates 把待广播的方块变更按节分组发给能看到该区块的玩家：
// 单个变更用 block_update，同节 ≥2 个变更用 section_blocks_update。
func (g *Game) flushBlockUpdates() {
	if len(g.pendingBlocks) == 0 {
		return
	}
	type secKey struct{ dim, cx, sy, cz int32 }
	type change struct {
		entry   packet.SectionEntry
		x, y, z int32
	}
	sections := map[secKey][]change{}
	for pos, state := range g.pendingBlocks {
		dim, x, y, z := pos[0], pos[1], pos[2], pos[3]
		// 线上节 Y 是世界坐标节号（overworld 为 -4..19）；节内 y 取相对维度底部
		k := secKey{dim, x >> 4, y >> 4, z >> 4}
		sections[k] = append(sections[k], change{
			entry: packet.SectionEntry{LocalX: x & 15, LocalY: (y - world.MinY) & 15, LocalZ: z & 15, State: state},
			x:     x, y: y, z: z,
		})
	}
	g.pendingBlocks = nil
	for k, list := range sections {
		viewers := g.chunkViewers(k.dim, k.cx, k.cz)
		if len(viewers) == 0 {
			continue
		}
		if len(list) == 1 {
			c := list[0]
			for _, v := range viewers {
				x, y, z, s := c.x, c.y, c.z, g.stateFor(v, c.entry.State)
				v.Send.SendPlay("minecraft:block_update", func(w *protocol.Writer) {
					packet.WriteBlockUpdate(w, x, y, z, s)
				})
			}
			continue
		}
		entries := make([]packet.SectionEntry, len(list))
		for i, c := range list {
			entries[i] = c.entry
		}
		for _, v := range viewers {
			sx, sy, sz := k.cx, k.sy, k.cz
			es := make([]packet.SectionEntry, len(entries))
			for i, e := range entries {
				es[i] = e
				es[i].State = g.stateFor(v, e.State)
			}
			v.Send.SendPlay("minecraft:section_blocks_update", func(w *protocol.Writer) {
				packet.WriteSectionBlocksUpdate(w, sx, sy, sz, es)
			})
		}
	}
}

// chunkViewers 返回已收到该区块且在视距内的同维度玩家。
func (g *Game) chunkViewers(dim, cx, cz int32) []*entity.Player {
	r := g.Cfg.ViewDistance
	var out []*entity.Player
	for _, p := range g.players {
		if p.Dim != dim || p.SentChunks == nil || !p.SentChunks[[2]int32{cx, cz}] {
			continue
		}
		if abs32(int32(p.X)>>4-cx) <= r && abs32(int32(p.Z)>>4-cz) <= r {
			out = append(out, p)
		}
	}
	return out
}

// inDigRange 粗略的触达校验（原版约 4.5~5 格，这里放宽到 8 格，超出则忽略）。
func (g *Game) inDigRange(p *entity.Player, x, y, z int32) bool {
	return abs32(int32(p.X)-x) <= 8 && abs32(int32(p.Y)-y) <= 8 && abs32(int32(p.Z)-z) <= 8
}

// syncChunks 玩家跨区块后更新缓存中心并补发新进入视距的区块。
func (g *Game) syncChunks(p *entity.Player) {
	cx, cz := int32(p.X)>>4, int32(p.Z)>>4
	if cx == p.CenterCX && cz == p.CenterCZ {
		return
	}
	sess := g.sessionOf(p)
	if sess == nil {
		return
	}
	style := world.StyleFor(int32(sess.suite.Version))
	p.CenterCX, p.CenterCZ = cx, cz
	sess.SendPlay("minecraft:set_chunk_cache_center", func(w *protocol.Writer) {
		packet.WriteChunkCenter(w, cx, cz)
	})
	r := g.Cfg.ViewDistance
	for ddx := -r; ddx <= r; ddx++ {
		for ddz := -r; ddz <= r; ddz++ {
			x, z := cx+ddx, cz+ddz
			key := [2]int32{x, z}
			if p.SentChunks[key] {
				continue
			}
			body := g.wd(p.Dim).Chunk(x, z).EncodeBody(style)
			sess.SendPlay("minecraft:level_chunk_with_light", func(w *protocol.Writer) {
				packet.WriteLevelChunk(w, x, z, body)
			})
			p.SentChunks[key] = true
		}
	}
}

// isCreative 该玩家当前是否为创造模式。
func (g *Game) isCreative(p *entity.Player) bool {
	if p == nil {
		return g.Cfg.GameMode == packet.ModeCreative
	}
	return packet.GameMode(p.GameMode) == packet.ModeCreative
}

// playerProto 返回玩家连接的协议版本号。
func (g *Game) playerProto(p *entity.Player) int32 {
	if sess := g.sessionOf(p); sess != nil {
		return int32(sess.suite.Version)
	}
	return int32(g.Suite.Version)
}

// stateFor 把世界的基准(777)方块状态 ID 翻译到玩家协议版本。
func (g *Game) stateFor(p *entity.Player, state uint32) uint32 {
	if m := assets.StateMap(g.playerProto(p)); m != nil {
		if to, ok := m[state]; ok {
			return to
		}
	}
	return state
}

// itemFor 把基准(777)物品 ID 翻译到玩家协议版本（用于给玩家发包/入包）。
func (g *Game) itemFor(p *entity.Player, id int32) int32 {
	if m := assets.ItemMap(g.playerProto(p)); m != nil {
		if to, ok := m[id]; ok {
			return to
		}
	}
	return id
}

// canonItem 把玩家协议的物品 ID 翻译回 777 基准（工具判定用）。
func (g *Game) canonItem(p *entity.Player, id int32) int32 {
	if m := g.itemToCanon[g.playerProto(p)]; m != nil {
		if to, ok := m[id]; ok {
			return to
		}
	}
	return id
}

// entityTypeID 按玩家协议版本查实体类型网络 ID（未登记时 0）。
func (g *Game) entityTypeID(p *entity.Player, name string) int32 {
	if m, ok := g.entityTypes[g.playerProto(p)]; ok {
		return m[name]
	}
	return 0
}

// sessionOf 取玩家背后的会话（非会话发送者时为 nil）。
func (g *Game) sessionOf(p *entity.Player) *Session {
	s, _ := p.Send.(*Session)
	return s
}

func abs32(v int32) int32 {
	if v < 0 {
		return -v
	}
	return v
}

// resendChunkWithLight 重发区块（带最新光照）给其观察者。
func (g *Game) resendChunkWithLight(dim, cx, cz int32) {
	for _, p := range g.chunkViewers(dim, cx, cz) {
		sess := g.sessionOf(p)
		if sess == nil {
			continue
		}
		style := world.StyleFor(g.playerProto(p))
		x, z := cx, cz
		body := g.wd(dim).Chunk(x, z).EncodeBody(style)
		p.Send.SendPlay("minecraft:level_chunk_with_light", func(w *protocol.Writer) {
			packet.WriteLevelChunk(w, x, z, body)
		})
	}
}
