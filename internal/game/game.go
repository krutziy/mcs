package game

import (
	"fmt"
	"log"
	"sync/atomic"
	"time"

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

	gen world.Generator

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
		dayTime: 6000, // 正午
		clockID: 1,
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

// Stop 结束主循环。
func (g *Game) Stop() { g.closed.Store(true) }

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

	if g.tick%20 == 0 { // 每秒
		g.broadcastTime()
		g.checkKeepAlive()
	}

	g.Bus.emit(Tick{Count: g.tick})
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
	}
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
	p.BroadcastX, p.BroadcastY, p.BroadcastZ = p.X, p.Y, p.Z
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

	g.sendChunks(s, int32(g.Cfg.SpawnX)>>4, int32(g.Cfg.SpawnZ)>>4)

	// 互相可见
	for _, other := range g.players {
		if other.EntityID == eid {
			continue
		}
		other.Send.SendPlay("minecraft:player_info_update", func(w *protocol.Writer) {
			packet.WritePlayerInfoAdd(w, p.UUID, p.Name, g.Cfg.GameMode)
		})
		other.Send.SendPlay("minecraft:add_entity", func(w *protocol.Writer) {
			packet.SpawnEntity{EntityID: p.EntityID, UUID: p.UUID, Type: EntityTypePlayer,
				X: p.X, Y: p.Y, Z: p.Z}.Write(w)
		})
		s.SendPlay("minecraft:player_info_update", func(w *protocol.Writer) {
			packet.WritePlayerInfoAdd(w, other.UUID, other.Name, g.Cfg.GameMode)
		})
		s.SendPlay("minecraft:add_entity", func(w *protocol.Writer) {
			packet.SpawnEntity{EntityID: other.EntityID, UUID: other.UUID, Type: EntityTypePlayer,
				X: other.X, Y: other.Y, Z: other.Z}.Write(w)
		})
	}

	g.Bus.emit(PlayerJoin{Player: p})
	log.Printf("[game] %s 加入，在线 %d 人", p.Name, len(g.players))
	g.broadcastSystem(fmt.Sprintf("§e%s 加入了游戏", p.Name))
}

// sendChunks 以指定区块为中心，成批发送世界区块。
func (g *Game) sendChunks(s *Session, cx, cz int32) {
	r := g.Cfg.ViewDistance
	s.SendPlay("minecraft:chunk_batch_start", func(w *protocol.Writer) {})
	n := int32(0)
	for dx := -r; dx <= r; dx++ {
		for dz := -r; dz <= r; dz++ {
			x, z := cx+dx, cz+dz
			ch := world.NewChunk(x, z, 0)
			g.gen.Generate(ch)
			body := ch.EncodeBody(world.StyleFor(int32(s.suite.Version)))
			s.SendPlay("minecraft:level_chunk_with_light", func(w *protocol.Writer) {
				packet.WriteLevelChunk(w, x, z, body)
			})
			n++
		}
	}
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

// EntityTypePlayer 是玩家实体在 entity_type 注册表中的 ID（26.1 = 155）。
const EntityTypePlayer int32 = 155
