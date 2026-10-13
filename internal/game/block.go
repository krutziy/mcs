package game

// 方块交互核心：挖掘 / 放置 / 方块变更广播与变更序列号确认（ack）。
//
// 服务端权威模型：所有方块状态只存在于 world.Manager；
// 玩家的挖掘/放置请求经事件进入主循环，应用到世界后再向能看到该区块的
// 玩家广播增量更新，并向动作发起者回 block_changed_ack（客户端用它来
// 结束本地的"幽灵方块"预测）。

import (
	"mcserver/internal/assets"
	"mcserver/internal/entity"
	"mcserver/internal/packet"
	"mcserver/internal/protocol"
	"mcserver/internal/world"
)

// levelEventBlockBreak 是方块破坏粒子 + 音效的 level_event 事件 ID。
const levelEventBlockBreak int32 = 2001

// handleDig 处理挖掘：创造模式在 start 即时破坏，其余模式在 finish 破坏
// （客户端本地播放破坏进度动画，服务端先不做按方块类型的时长校验）。
func (g *Game) handleDig(ev *blockDigEvent) {
	p := ev.p
	if p == nil {
		return
	}
	a := ev.act
	bus := BlockDig{Player: p, X: a.X, Y: a.Y, Z: a.Z, Status: a.Status, Face: a.Face, Sequence: a.Sequence}
	defer func() { g.Bus.emit(bus) }()

	start := a.Status == packet.DigStart
	finish := a.Status == packet.DigFinish
	if !start && !finish {
		return // 取消 / 丢物品等：仅通知插件
	}
	if start && g.Cfg.GameMode != packet.ModeCreative {
		return // 生存模式 start 只开始进度，等 finish 再破坏
	}
	if !g.inDigRange(p, a.X, a.Y, a.Z) {
		return
	}
	old := g.world.GetBlock(a.X, a.Y, a.Z)
	bus.OldState = old
	if old == world.StateAir {
		return
	}
	// 破坏粒子 + 音效（data = 被破坏方块的 ID，按观察者协议翻译），再改世界状态
	for _, v := range g.chunkViewers(a.X>>4, a.Z>>4) {
		data := g.stateFor(v, old)
		v.Send.SendPlay("minecraft:level_event", func(w *protocol.Writer) {
			packet.WriteLevelEvent(w, levelEventBlockBreak, a.X, a.Y, a.Z, int32(data), false)
		})
	}
	g.applyBlock(a.X, a.Y, a.Z, world.StateAir, a.Sequence, p)
	bus.Broke = true
	// 生存模式：按掉落表生成掉落物（需镐方块还要材质门槛）
	if g.Cfg.GameMode != packet.ModeCreative {
		bus.Drops = g.digDrops(p, old)
		for _, d := range bus.Drops {
			g.spawnItem(float64(a.X)+0.5, float64(a.Y)+0.5, float64(a.Z)+0.5, d)
		}
	}
}

// digDrops 依据官方 jar 战利品表生成的掉落表计算掉落（数量恒 1 的简化）。
// 需镐方块（mineable/pickaxe）只有手持足够材质的镐才掉落。
// 返回的物品 ID 为 777 基准空间。
func (g *Game) digDrops(p *entity.Player, state uint32) []entity.Item {
	item, ok := g.blockDrops[777][state]
	if !ok {
		return nil
	}
	if mask, needsPick := g.mineable[777][state]; needsPick {
		held := g.canonItem(p, p.Held().ID)
		bit, isPick := g.pickaxes[777][held]
		if !isPick || mask&(1<<bit) == 0 {
			return nil
		}
	}
	return []entity.Item{{ID: item, Count: 1}}
}

// handlePlace 处理对方块使用物品（放置）：按玩家手持物品查物品→方块表，
// 目标位置为被点击方块的面外侧，可替换目标（空气/水）时放置。
func (g *Game) handlePlace(ev *blockPlaceEvent) {
	p := ev.p
	if p == nil {
		return
	}
	u := ev.use
	dx, dy, dz := packet.FaceOffset(u.Face)
	tx, ty, tz := u.X+dx, u.Y+dy, u.Z+dz
	bus := BlockPlace{Player: p, X: tx, Y: ty, Z: tz,
		AgainstX: u.X, AgainstY: u.Y, AgainstZ: u.Z,
		Face: u.Face, Sequence: u.Sequence}
	defer func() { g.Bus.emit(bus) }()

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
	if !g.inDigRange(p, tx, ty, tz) {
		return
	}
	cur := g.world.GetBlock(tx, ty, tz)
	if cur != world.StateAir && cur != world.StateWater {
		g.rejectPlace(p, u, tx, ty, tz) // 目标被占：客户端会预测放置，需回滚
		return
	}
	g.applyBlock(tx, ty, tz, state, u.Sequence, p)
	bus.State, bus.ItemID = state, held.ID
	// 生存模式：放置消耗一个手持物品
	if g.Cfg.GameMode != packet.ModeCreative {
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
		state := g.stateFor(p, g.world.GetBlock(x, y, z))
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

// handleHeldSlot 处理 set_carried_item。
func (g *Game) handleHeldSlot(ev *playerHeldSlotEvent) {
	if ev.p != nil && ev.slot >= 0 && ev.slot <= 8 {
		ev.p.HeldSlot = ev.slot
	}
}

// applyBlock 应用一次方块变更并广播：写世界 → 广播增量更新 → 回 ack。
// ack 必须晚于方块更新包到达客户端（客户端用 ack 提交预测状态）。
func (g *Game) applyBlock(x, y, z int32, state uint32, sequence int32, actor *entity.Player) {
	g.world.SetBlock(x, y, z, state)
	g.recordBlockChange(x, y, z, state)
	g.flushBlockUpdates()
	if actor != nil && sequence > 0 {
		actor.Send.SendPlay("minecraft:block_changed_ack", func(w *protocol.Writer) {
			packet.WriteBlockChangedAck(w, sequence)
		})
	}
}

// recordBlockChange 记入待广播缓冲（同位置多次变更以最后一次为准）。
func (g *Game) recordBlockChange(x, y, z int32, state uint32) {
	if g.pendingBlocks == nil {
		g.pendingBlocks = map[[3]int32]uint32{}
	}
	g.pendingBlocks[[3]int32{x, y, z}] = state
}

// flushBlockUpdates 把待广播的方块变更按节分组发给能看到该区块的玩家：
// 单个变更用 block_update，同节 ≥2 个变更用 section_blocks_update。
func (g *Game) flushBlockUpdates() {
	if len(g.pendingBlocks) == 0 {
		return
	}
	type secKey struct{ cx, sy, cz int32 }
	type change struct {
		entry   packet.SectionEntry
		x, y, z int32
	}
	sections := map[secKey][]change{}
	for pos, state := range g.pendingBlocks {
		x, y, z := pos[0], pos[1], pos[2]
		// 线上节 Y 是世界坐标节号（overworld 为 -4..19）；节内 y 取相对维度底部
		k := secKey{x >> 4, y >> 4, z >> 4}
		sections[k] = append(sections[k], change{
			entry: packet.SectionEntry{LocalX: x & 15, LocalY: (y - world.MinY) & 15, LocalZ: z & 15, State: state},
			x:     x, y: y, z: z,
		})
	}
	g.pendingBlocks = nil
	for k, list := range sections {
		viewers := g.chunkViewers(k.cx, k.cz)
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

// chunkViewers 返回已收到该区块且在视距内的玩家。
func (g *Game) chunkViewers(cx, cz int32) []*entity.Player {
	r := g.Cfg.ViewDistance
	var out []*entity.Player
	for _, p := range g.players {
		if p.SentChunks == nil || !p.SentChunks[[2]int32{cx, cz}] {
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
			body := g.world.Chunk(x, z).EncodeBody(style)
			sess.SendPlay("minecraft:level_chunk_with_light", func(w *protocol.Writer) {
				packet.WriteLevelChunk(w, x, z, body)
			})
			p.SentChunks[key] = true
		}
	}
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
