package game

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync/atomic"
	"time"

	"mcserver/internal/assets"
	"mcserver/internal/entity"
	"mcserver/internal/packet"
	"mcserver/internal/protocol"
	"mcserver/internal/version"
	"mcserver/internal/world"
)

// Config 是游戏层配置。
type Config struct {
	SpawnX, SpawnY, SpawnZ float64
	ViewDistance           int32 // 区块半径
	GameMode               packet.GameMode
	MOTD                   string
	MaxPlayers             int32
	WorldType              string // "flat" | "noise"
	WorldSeed              int64
	SaveDir                string // 世界存档目录（空 = 不持久化）
}

// Game 聚合全部游戏状态，由主循环独占访问。
type Game struct {
	Cfg   Config
	Suite *version.Suite
	Bus   *EventBus

	events chan Event
	joinCh chan *Session

	players map[int32]*entity.Player
	nextEID atomic.Int32
	count   atomic.Int32

	tick     int64
	worldAge int64
	dayTime  int64
	clockID  int32 // 白天时钟 ID（官方录制为 1）

	gen   world.Generator
	world *world.Manager // 服务端权威方块状态（区块缓存）
	// 各版本数据表（物品→方块 / 实体类型 / 掉落 / 工具门槛；世界统一 777 状态空间）
	itemBlocks    map[int32]map[int32]uint32
	entityTypes   map[int32]map[string]int32
	blockDrops    map[int32]map[uint32]int32
	mineable      map[int32]map[uint32]uint8
	pickaxes      map[int32]map[int32]uint8
	itemToCanon   map[int32]map[int32]int32 // 玩家协议物品 ID → 777 基准 ID（反向翻译表）
	items         map[int32]*itemEntity     // 掉落物实体（item.ID 恒为 777 基准）
	pendingBlocks map[[3]int32]uint32       // 待广播的方块变更

	closed atomic.Bool
}

// New 创建游戏并启动主循环。
func New(cfg Config, suite *version.Suite) *Game {
	g := &Game{
		Cfg:            cfg,
		Suite:          suite,
		Bus:            NewEventBus(),
		events:         make(chan Event, 4096),
		joinCh:         make(chan *Session, 64),
		players:        map[int32]*entity.Player{},
		items:          map[int32]*itemEntity{},
		dayTime:        6000, // 正午
		clockID:        1,
		itemBlocks:     map[int32]map[int32]uint32{},
		entityTypes:    map[int32]map[string]int32{},
		blockDrops:     map[int32]map[uint32]int32{},
		mineable:       map[int32]map[uint32]uint8{},
		pickaxes:       map[int32]map[int32]uint8{},
		itemToCanon:    map[int32]map[int32]int32{},
		pendingBlocks:  map[[3]int32]uint32{},
	}
	// 世界类型由注册表解析：核心只有虚空，地形等由插件提供
	g.gen = world.NewGenerator(cfg.WorldType, cfg.WorldSeed)
	g.world = world.NewManager(g.gen)
	for _, v := range version.All() {
		p := int32(v.Protocol)
		g.itemBlocks[p] = assets.ItemBlocks(p)
		g.entityTypes[p] = assets.EntityTypes(p)
		g.blockDrops[p] = assets.BlockDrops(p)
		g.mineable[p] = assets.MineableMask(p)
		g.pickaxes[p] = assets.PickaxeBits(p)
		if im := assets.ItemMap(p); len(im) > 0 {
			rev := make(map[int32]int32, len(im))
			for from, to := range im {
				rev[to] = from
			}
			g.itemToCanon[p] = rev
		}
	}
	// 775 无官方报告：借用相邻 776 的表
	if g.entityTypes[775] == nil {
		g.entityTypes[775] = g.entityTypes[776]
	}
	if g.itemToCanon[775] == nil {
		g.itemToCanon[775] = g.itemToCanon[776]
	}
	// 世界持久化：读档优先于生成器
	if cfg.SaveDir != "" {
		if n, err := g.world.Load(cfg.SaveDir); err != nil {
			log.Printf("[game] 世界读档失败: %v", err)
		} else if n > 0 {
			log.Printf("[game] 世界读档 %d 个区块", n)
		}
	}
	// 世界类型由注册表解析：核心只有虚空，地形等由插件提供
	g.gen = world.NewGenerator(cfg.WorldType, cfg.WorldSeed)
	// 生存端：配置 spawn y=0 时按地形自动取站立表面
	if cfg.SpawnY == 0 {
		if sp, ok := g.gen.(interface{ SurfaceY(x, z int32) int32 }); ok {
			g.Cfg.SpawnY = float64(sp.SurfaceY(int32(cfg.SpawnX), int32(cfg.SpawnZ)))
		} else {
			g.Cfg.SpawnY = 0
		}
	}
	g.nextEID.Store(100)
	go g.run()
	return g
}

// Submit 从会话读 goroutine 投递事件（非阻塞，满则丢弃并记日志）。
func (g *Game) Submit(e Event) {
	select {
	case g.events <- e:
	default:
		log.Printf("[game] 事件队列溢出，丢弃 %T", e)
	}
}

// JoinSession 由 server 层在配置阶段完成后调用。
func (g *Game) JoinSession(s *Session) {
	select {
	case g.joinCh <- s:
	default:
		s.conn.Close()
	}
}

// PlayerCount 返回在线人数（供状态查询等外部读取）。
func (g *Game) PlayerCount() int32 { return g.count.Load() }

// run 是唯一的游戏主循环 goroutine：20TPS + 事件消费。
func (g *Game) run() {
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for !g.closed.Load() {
		select {
		case <-ticker.C:
			g.tickNow()
		case s := <-g.joinCh:
			g.handleJoin(s)
		default:
			select {
			case e := <-g.events:
				g.handleEvent(e)
			case s := <-g.joinCh:
				g.handleJoin(s)
			case <-ticker.C:
				g.tickNow()
			}
		}
	}
}

// tickNow 推进一个游戏刻。
func (g *Game) tickNow() {
	g.tick++
	g.worldAge++
	g.dayTime = (g.dayTime + 1) % 24000

	g.tickItems()

	if g.tick%20 == 0 { // 每秒
		g.broadcastTime()
		g.checkKeepAlive()
	}
	if g.Cfg.SaveDir != "" && g.tick%600 == 0 { // 每 30 秒自动存档
		g.saveAll()
	}

	g.Bus.emit(Tick{Count: g.tick})
}

// saveAll 保存世界脏区块与玩家状态。
func (g *Game) saveAll() {
	if n, err := g.world.Save(g.Cfg.SaveDir); err != nil {
		log.Printf("[game] 世界存档失败: %v", err)
	} else if n > 0 {
		log.Printf("[game] 世界存档 %d 个区块", n)
	}
	for _, p := range g.players {
		g.savePlayer(p)
	}
}

// Stop 结束主循环（先存档）。
func (g *Game) Stop() {
	if g.closed.CompareAndSwap(false, true) {
		g.saveAll()
	}
}

// handleEvent 分发会话事件。
func (g *Game) handleEvent(e Event) {
	switch ev := e.(type) {
	case *playerQuitEvent:
		g.handleQuit(ev)
	case *playerMoveEvent:
		p := ev.p
		p.X, p.Y, p.Z = ev.x, ev.y, ev.z
		p.Yaw, p.Pitch, p.OnGround = ev.yaw, ev.pitch, ev.onGround
		g.syncMovement(p)
		g.syncChunks(p)
		g.Bus.emit(PlayerMoved{Player: p})
	case *playerChatEvent:
		if ev.p != nil {
			g.Bus.emit(PlayerChatted{Player: ev.p, Message: ev.msg})
			g.broadcastChat(ev.p, ev.msg)
		}
	case *playerCommandEvent:
		if ev.p != nil {
			g.handleCommand(ev)
		}
	case *playerKeepAliveEvent:
		if ev.p != nil && ev.id == ev.p.KeepAlivePending {
			ev.p.KeepAlivePending = 0
		}
	case *playerTeleportConfirmEvent:
		if ev.p != nil && ev.id == ev.p.TeleportID {
			ev.p.TeleportID = -1
		}
	case *blockDigEvent:
		g.handleDig(ev)
	case *blockPlaceEvent:
		g.handlePlace(ev)
	case *playerHeldSlotEvent:
		g.handleHeldSlot(ev)
	case *playerCreativeSlotEvent:
		g.handleCreativeSlot(ev)
	case *playerContainerClickEvent:
		g.handleContainerClick(ev)
	}
	g.flushBlockUpdates()
}

// playerStateFile 是玩家状态的 JSON 存档（位置 + 物品栏 + 选中槽）。
type playerStateFile struct {
	X        float64      `json:"x"`
	Y        float64      `json:"y"`
	Z        float64      `json:"z"`
	Yaw      float32      `json:"yaw"`
	Pitch    float32      `json:"pitch"`
	Slots    []entity.Item `json:"slots"`
	HeldSlot int32        `json:"held"`
}

// savePlayer 保存玩家状态到存档目录。
func (g *Game) savePlayer(p *entity.Player) {
	dir := g.Cfg.SaveDir + "/players"
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return
	}
	st := playerStateFile{X: p.X, Y: p.Y, Z: p.Z, Yaw: p.Yaw, Pitch: p.Pitch, Slots: p.Slots[:], HeldSlot: p.HeldSlot}
	b, err := json.Marshal(st)
	if err != nil {
		return
	}
	_ = os.WriteFile(filepath.Join(dir, p.Name+".json"), b, 0o644)
}

// loadPlayer 恢复玩家状态（无存档时保持出生点/空物品栏）。
func (g *Game) loadPlayer(p *entity.Player) {
	b, err := os.ReadFile(filepath.Join(g.Cfg.SaveDir, "players", p.Name+".json"))
	if err != nil {
		return
	}
	var st playerStateFile
	if json.Unmarshal(b, &st) != nil {
		return
	}
	if len(st.Slots) == len(p.Slots) {
		copy(p.Slots[:], st.Slots)
	}
	p.HeldSlot = st.HeldSlot
	p.X, p.Y, p.Z = st.X, st.Y, st.Z
	p.Yaw, p.Pitch = st.Yaw, st.Pitch
}

// handleJoin 完成进入世界流程并广播。
func (g *Game) handleJoin(s *Session) {
	eid := g.nextEID.Add(1)
	p := &entity.Player{
		EntityID: eid,
		UUID:     s.uuid,
		Name:     s.name,
		X:        g.Cfg.SpawnX,
		Y:        g.Cfg.SpawnY,
		Z:        g.Cfg.SpawnZ,
		Send:     s,
	}
	if g.Cfg.SaveDir != "" {
		g.loadPlayer(p) // 玩家状态持久化：位置与物品栏
	}
	p.BroadcastX, p.BroadcastY, p.BroadcastZ = p.X, p.Y, p.Z
	p.SentChunks = map[[2]int32]bool{}
	s.player.Store(p)
	g.players[eid] = p
	g.count.Store(int32(len(g.players)))

	join := packet.JoinGame{
		EntityID:            eid,
		WorldNames:          []string{"minecraft:overworld"},
		MaxPlayers:          g.Cfg.MaxPlayers,
		ViewDistance:        g.Cfg.ViewDistance,
		SimulationDistance:  g.Cfg.ViewDistance,
		EnableRespawnScreen: true,
		DimensionID:         0, // minecraft:overworld
		WorldName:           "minecraft:overworld",
		GameMode:            g.Cfg.GameMode,
		PreviousGameMode:    255,
		IsFlat:              true,
		SeaLevel:            -63,
	}
	s.SendPlay("minecraft:login", func(w *protocol.Writer) {
		packet.WriteJoinGame(w, join)
	})

	s.SendPlay("minecraft:player_abilities", func(w *protocol.Writer) {
		flags := uint8(0)
		if g.Cfg.GameMode == packet.ModeCreative {
			flags |= 0x04 | 0x08
		}
		packet.WriteAbilities(w, packet.Abilities{Flags: flags, FlyingSpeed: 0.05, WalkingSpeed: 0.1})
	})

	p.TeleportID = 1
	s.SendPlay("minecraft:player_position", func(w *protocol.Writer) {
		packet.WritePlayerPosition(w, packet.PlayerPositionOut{
			TeleportID: p.TeleportID, X: p.X, Y: p.Y, Z: p.Z, Yaw: p.Yaw, Pitch: p.Pitch,
		})
	})

	s.SendPlay("minecraft:set_chunk_cache_center", func(w *protocol.Writer) {
		packet.WriteChunkCenter(w, int32(g.Cfg.SpawnX)>>4, int32(g.Cfg.SpawnZ)>>4)
	})
	s.SendPlay("minecraft:set_chunk_cache_radius", func(w *protocol.Writer) {
		packet.WriteViewDistance(w, g.Cfg.ViewDistance)
	})
	s.SendPlay("minecraft:set_simulation_distance", func(w *protocol.Writer) {
		packet.WriteSimulationDistance(w, g.Cfg.ViewDistance)
	})
	s.SendPlay("minecraft:set_time", func(w *protocol.Writer) {
		if s.ver.ClassicTime {
			packet.WriteSetTimeClassic(w, g.worldAge, g.dayTime)
		} else {
			packet.WriteSetTime(w, g.worldAge, []packet.Clock{{ID: g.clockID, TotalTicks: g.dayTime, Rate: 1}})
		}
	})
	s.SendPlay("minecraft:set_default_spawn_position", func(w *protocol.Writer) {
		packet.WriteSpawnPosition(w, int32(g.Cfg.SpawnX), int32(g.Cfg.SpawnY), int32(g.Cfg.SpawnZ), 0, 0)
	})

	s.SendPlay("minecraft:player_info_update", func(w *protocol.Writer) {
		packet.WritePlayerInfoAdd(w, p.UUID, p.Name, g.Cfg.GameMode)
	})

	g.sendChunks(s, p, int32(g.Cfg.SpawnX)>>4, int32(g.Cfg.SpawnZ)>>4)
	g.syncInventory(p) // 初始物品栏同步

	// 视距内已有的掉落物补给新玩家
	for _, ie := range g.items {
		if abs32(int32(ie.x)>>4-int32(p.X)>>4) <= g.Cfg.ViewDistance &&
			abs32(int32(ie.z)>>4-int32(p.Z)>>4) <= g.Cfg.ViewDistance {
			g.sendItemSpawn(p, ie)
		}
	}

	// 互相可见
	playerType := func(target *entity.Player) int32 { return g.entityTypeID(target, "player") }
	for _, other := range g.players {
		if other.EntityID == eid {
			continue
		}
		other.Send.SendPlay("minecraft:player_info_update", func(w *protocol.Writer) {
			packet.WritePlayerInfoAdd(w, p.UUID, p.Name, g.Cfg.GameMode)
		})
		other.Send.SendPlay("minecraft:add_entity", func(w *protocol.Writer) {
			packet.SpawnEntity{EntityID: p.EntityID, UUID: p.UUID, Type: playerType(other),
				X: p.X, Y: p.Y, Z: p.Z}.Write(w)
		})
		s.SendPlay("minecraft:player_info_update", func(w *protocol.Writer) {
			packet.WritePlayerInfoAdd(w, other.UUID, other.Name, g.Cfg.GameMode)
		})
		s.SendPlay("minecraft:add_entity", func(w *protocol.Writer) {
			packet.SpawnEntity{EntityID: other.EntityID, UUID: other.UUID, Type: playerType(p),
				X: other.X, Y: other.Y, Z: other.Z}.Write(w)
		})
	}

	g.Bus.emit(PlayerJoin{Player: p})
	log.Printf("[game] %s 加入，在线 %d 人", p.Name, len(g.players))
	g.broadcastSystem(fmt.Sprintf("§e%s 加入了游戏", p.Name))
}

// sendChunks 以指定区块为中心向玩家成批发送区块（生成一次后从世界管理器取缓存）。
func (g *Game) sendChunks(s *Session, p *entity.Player, cx, cz int32) {
	r := g.Cfg.ViewDistance
	s.SendPlay("minecraft:chunk_batch_start", func(w *protocol.Writer) {})
	n := int32(0)
	for dx := -r; dx <= r; dx++ {
		for dz := -r; dz <= r; dz++ {
			x, z := cx+dx, cz+dz
			body := g.world.Chunk(x, z).EncodeBody(world.StyleFor(int32(s.suite.Version)))
			s.SendPlay("minecraft:level_chunk_with_light", func(w *protocol.Writer) {
				packet.WriteLevelChunk(w, x, z, body)
			})
			p.SentChunks[[2]int32{x, z}] = true
			n++
		}
	}
	p.CenterCX, p.CenterCZ = cx, cz
	s.SendPlay("minecraft:chunk_batch_finished", func(w *protocol.Writer) {
		packet.WriteChunkBatchFinished(w, n)
	})
}

// handleQuit 移除玩家并广播。
func (g *Game) handleQuit(ev *playerQuitEvent) {
	p := ev.p
	if p == nil {
		return
	}
	if _, ok := g.players[p.EntityID]; !ok {
		return
	}
	delete(g.players, p.EntityID)
	g.count.Store(int32(len(g.players)))
	if g.Cfg.SaveDir != "" {
		g.savePlayer(p) // 离线即保存玩家状态
	}
	for _, other := range g.players {
		other.Send.SendPlay("minecraft:player_info_remove", func(w *protocol.Writer) {
			packet.WritePlayerInfoRemove(w, p.UUID)
		})
		other.Send.SendPlay("minecraft:remove_entities", func(w *protocol.Writer) {
			packet.WriteRemoveEntities(w, []int32{p.EntityID})
		})
	}
	g.Bus.emit(PlayerQuit{Player: p, Reason: ev.reason})
	log.Printf("[game] %s 离开（%s），在线 %d 人", p.Name, ev.reason, len(g.players))
	g.broadcastSystem(fmt.Sprintf("§e%s 离开了游戏", p.Name))
}

// syncMovement 把玩家最新位置广播给其他人（位置变化时）。
func (g *Game) syncMovement(p *entity.Player) {
	if p.X == p.BroadcastX && p.Y == p.BroadcastY && p.Z == p.BroadcastZ {
		return
	}
	for _, other := range g.players {
		if other.EntityID == p.EntityID {
			continue
		}
		o := other
		o.Send.SendPlay("minecraft:entity_position_sync", func(w *protocol.Writer) {
			packet.WriteEntityPositionSync(w, p.EntityID, p.X, p.Y, p.Z, 0, 0, 0, p.Yaw, p.Pitch, p.OnGround)
		})
	}
	p.BroadcastX, p.BroadcastY, p.BroadcastZ = p.X, p.Y, p.Z
}

// broadcastChat 广播玩家聊天。
func (g *Game) broadcastChat(p *entity.Player, msg string) {
	text := fmt.Sprintf("<%s> %s", p.Name, msg)
	for _, other := range g.players {
		other.Send.SendPlay("minecraft:system_chat", func(w *protocol.Writer) {
			packet.WriteSystemChat(w, text, false)
		})
	}
}

// BroadcastSystem 向所有在线玩家广播系统消息（玩法模块可用）。
func (g *Game) BroadcastSystem(text string) { g.broadcastSystem(text) }

// broadcastSystem 广播系统消息。
func (g *Game) broadcastSystem(text string) {
	for _, other := range g.players {
		other.Send.SendPlay("minecraft:system_chat", func(w *protocol.Writer) {
			packet.WriteSystemChat(w, text, false)
		})
	}
}

// broadcastTime 每秒同步时间（按各玩家协议版本选择格式）。
func (g *Game) broadcastTime() {
	age, day, cid := g.worldAge, g.dayTime, g.clockID
	for _, other := range g.players {
		sess, _ := other.Send.(*Session)
		classic := sess != nil && sess.ver.ClassicTime
		other.Send.SendPlay("minecraft:set_time", func(w *protocol.Writer) {
			if classic {
				packet.WriteSetTimeClassic(w, age, day)
			} else {
				packet.WriteSetTime(w, age, []packet.Clock{{ID: cid, TotalTicks: day, Rate: 1}})
			}
		})
	}
}

// checkKeepAlive 每秒检查：对无未确认 keepalive 的玩家发送新的；
// 超过 30 秒未响应的玩家踢出。
func (g *Game) checkKeepAlive() {
	now := time.Now().UnixMilli()
	for _, p := range g.players {
		if p.KeepAlivePending == 0 {
			p.KeepAlivePending = now
			id := now
			p.Send.SendPlay("minecraft:keep_alive", func(w *protocol.Writer) {
				packet.WriteKeepAlive(w, id)
			})
		} else if now-p.KeepAlivePending > 30000 {
			s := p.Send.(*Session)
			if s != nil {
				s.Kick("KeepAlive 超时")
			}
		}
	}
}

// handleCommand 处理内置命令。
func (g *Game) handleCommand(ev *playerCommandEvent) {
	p := ev.p
	switch ev.Name {
	case "list":
		names := ""
		for _, other := range g.players {
			if names != "" {
				names += ", "
			}
			names += other.Name
		}
		p.Send.SendPlay("minecraft:system_chat", func(w *protocol.Writer) {
			packet.WriteSystemChat(w, fmt.Sprintf("§7在线 (%d): §f%s", len(g.players), names), false)
		})
	case "spawn":
		p.X, p.Y, p.Z = g.Cfg.SpawnX, g.Cfg.SpawnY, g.Cfg.SpawnZ
		p.TeleportID++
		id := p.TeleportID
		x, y, z := p.X, p.Y, p.Z
		p.Send.SendPlay("minecraft:player_position", func(w *protocol.Writer) {
			packet.WritePlayerPosition(w, packet.PlayerPositionOut{TeleportID: id, X: x, Y: y, Z: z})
		})
	case "help":
		p.Send.SendPlay("minecraft:system_chat", func(w *protocol.Writer) {
			packet.WriteSystemChat(w, "§7可用命令: /list /spawn /help", false)
		})
	default:
		p.Send.SendPlay("minecraft:system_chat", func(w *protocol.Writer) {
			packet.WriteSystemChat(w, "§c未知命令: /"+ev.Name, false)
		})
	}
	g.Bus.emit(PlayerCommand{Player: p, Name: ev.Name, Args: ev.Args})
}

