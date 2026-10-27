package game

import (
	"log"
	"strings"
	"sync/atomic"

	"mcserver/internal/conn"
	"mcserver/internal/entity"
	"mcserver/internal/packet"
	"mcserver/internal/protocol"
	"mcserver/internal/version"
)

// Session 表示一个进入游戏阶段的玩家连接。
// 读 goroutine 解析包并投递事件；所有发送经由 conn 的写锁保证安全。
type Session struct {
	conn  *conn.Conn
	suite *version.Suite
	ver   version.Version // 协议版本（含差异化行为标志）
	game  *Game

	name   string
	uuid   protocol.UUID
	player atomic.Pointer[entity.Player]

	closed atomic.Bool
}

// NewSession 创建游戏会话（配置阶段完成后由 server 层调用）。
func NewSession(c *conn.Conn, suite *version.Suite, g *Game, name string, uuid protocol.UUID) *Session {
	ver, _ := version.Lookup(suite.Version)
	return &Session{conn: c, suite: suite, ver: ver, game: g, name: name, uuid: uuid}
}

// PlayerName 返回玩家名。
func (s *Session) PlayerName() string { return s.name }

// CurrentPlayer 返回当前玩家状态（可为 nil）。
func (s *Session) CurrentPlayer() *entity.Player { return s.player.Load() }

// --- 内部事件类型（会话 → 主循环） ---

type playerQuitEvent struct {
	p      *entity.Player
	reason string
}

type playerMoveEvent struct {
	p        *entity.Player
	x, y, z  float64
	yaw, pitch float32
	onGround bool
}

type playerChatEvent struct {
	p   *entity.Player
	msg string
}

type playerCommandEvent struct {
	p    *entity.Player
	Name string
	Args []string
}

type playerKeepAliveEvent struct {
	p  *entity.Player
	id int64
}

type playerTeleportConfirmEvent struct {
	p   *entity.Player
	id  int32
}

type blockDigEvent struct {
	p    *entity.Player
	act  packet.PlayerAction
}

type blockPlaceEvent struct {
	p     *entity.Player
	use   packet.UseItemOn
}

type playerHeldSlotEvent struct {
	p    *entity.Player
	slot int32
}

type playerCreativeSlotEvent struct {
	p  *entity.Player
	cs packet.CreativeSlot
}

type playerContainerClickEvent struct {
	p     *entity.Player
	click packet.ContainerClick
}

type playerRespawnEvent struct {
	p *entity.Player
}

type playerUseItemEvent struct {
	p        *entity.Player
	hand     int32
	sequence int32
}

type playerCloseCraftEvent struct {
	p *entity.Player
}

type playerCloseFurnaceEvent struct {
	p *entity.Player
}

type playerCloseChestEvent struct {
	p *entity.Player
}

type playerCloseBrewEvent struct {
	p *entity.Player
}

type playerCloseEnchEvent struct {
	p *entity.Player
}

type playerCloseAnvilEvent struct {
	p *entity.Player
}

type playerEnchantEvent struct {
	p    *entity.Player
	tier int32
}

type playerAttackEvent struct {
	p      *entity.Player
	target int32
}

type playerInteractEvent struct {
	p        *entity.Player
	target   int32
	sneaking bool
}

// PlayLoop 阻塞处理 play 状态的入站包，直到连接断开。在独立 goroutine 中运行。
func (s *Session) PlayLoop() {
	s.game.JoinSession(s)
	for {
		id, r, err := s.conn.ReadPacket()
		if err != nil {
			s.game.Submit(&playerQuitEvent{p: s.player.Load(), reason: err.Error()})
			s.conn.Close()
			return
		}
		body := r.Rest()
		if s.closed.Load() {
			return
		}
		name := s.lookup(id)
		pr := protocol.NewReader(body)
		p := s.player.Load()

		switch name {
		case "minecraft:move_player_pos":
			m := packet.ReadMovePlayer(pr, true, false)
			pl := p
			if pl != nil {
				s.game.Submit(&playerMoveEvent{p: p, x: m.X, y: m.Y, z: m.Z, yaw: pl.Yaw, pitch: pl.Pitch, onGround: m.OnGround})
			}
		case "minecraft:move_player_pos_rot":
			m := packet.ReadMovePlayer(pr, true, true)
			s.game.Submit(&playerMoveEvent{p: p, x: m.X, y: m.Y, z: m.Z, yaw: m.Yaw, pitch: m.Pitch, onGround: m.OnGround})
		case "minecraft:move_player_rot":
			m := packet.ReadMovePlayer(pr, false, true)
			if p != nil {
				s.game.Submit(&playerMoveEvent{p: p, x: p.X, y: p.Y, z: p.Z, yaw: m.Yaw, pitch: m.Pitch, onGround: m.OnGround})
			}
		case "minecraft:move_player_status_only":
			m := packet.ReadMovePlayer(pr, false, false)
			if p != nil {
				s.game.Submit(&playerMoveEvent{p: p, x: p.X, y: p.Y, z: p.Z, yaw: p.Yaw, pitch: p.Pitch, onGround: m.OnGround})
			}
		case "minecraft:keep_alive":
			s.game.Submit(&playerKeepAliveEvent{p: p, id: pr.Int64()})
		case "minecraft:accept_teleportation":
			id := pr.VarInt()
			if s.ver.ConfirmEchoesPos { // 26.3+：回显的位置与朝向（读取并忽略）
				_ = pr.FixedBytes(24 + 8)
			}
			s.game.Submit(&playerTeleportConfirmEvent{p: p, id: id})
		case "minecraft:chat":
			msg := packet.ReadChatMessage(pr)
			s.game.Submit(&playerChatEvent{p: p, msg: msg.Content})
		case "minecraft:chat_command":
			cmd := pr.String()
			parts := strings.Fields(cmd)
			if len(parts) > 0 {
				s.game.Submit(&playerCommandEvent{p: p, Name: parts[0], Args: parts[1:]})
			}
		case "minecraft:player_action":
			s.game.Submit(&blockDigEvent{p: p, act: packet.ReadPlayerAction(pr)})
		case "minecraft:use_item_on":
			s.game.Submit(&blockPlaceEvent{p: p, use: packet.ReadUseItemOn(pr)})
		case "minecraft:set_carried_item":
			s.game.Submit(&playerHeldSlotEvent{p: p, slot: packet.ReadSetCarriedItem(pr)})
		case "minecraft:set_creative_mode_slot":
			s.game.Submit(&playerCreativeSlotEvent{p: p, cs: packet.ReadCreativeSlot(pr)})
		case "minecraft:container_click":
			s.game.Submit(&playerContainerClickEvent{p: p, click: packet.ReadContainerClick(pr)})
		case "minecraft:client_command":
			if action := pr.VarInt(); action == 0 { // perform respawn
				s.game.Submit(&playerRespawnEvent{p: p})
			}
		case "minecraft:use_item":
			ev := &playerUseItemEvent{p: p, hand: pr.VarInt(), sequence: pr.VarInt()}
			s.game.Submit(ev)
		case "minecraft:container_close":
			switch pr.VarInt() {
			case 1:
				s.game.Submit(&playerCloseCraftEvent{p: p})
			case 2:
				s.game.Submit(&playerCloseFurnaceEvent{p: p})
			case 3:
				s.game.Submit(&playerCloseChestEvent{p: p})
			case 4:
				s.game.Submit(&playerCloseBrewEvent{p: p})
			case 5:
				s.game.Submit(&playerCloseEnchEvent{p: p})
			case 6:
				s.game.Submit(&playerCloseAnvilEvent{p: p})
			}
		case "minecraft:attack":
			s.game.Submit(&playerAttackEvent{p: p, target: pr.VarInt()})
		case "minecraft:click_enchant":
			s.game.Submit(&playerEnchantEvent{p: p, tier: pr.VarInt()})
		case "minecraft:interact":
			target := pr.VarInt()
			typ := pr.VarInt()
			sneaking := pr.Bool()
			if typ == 0 { // interact：喂食判定
				s.game.Submit(&playerInteractEvent{p: p, target: target, sneaking: sneaking})
			}
		case "minecraft:client_tick_end", "minecraft:player_loaded", "minecraft:punch",
			"minecraft:move_vehicle", "minecraft:player_input":
			// 活体心跳/输入流：无需处理
		default:
			log.Printf("[session %s] 未处理包 %s (0x%02x, %d字节)", s.name, name, id, len(body))
		}
	}
}

// lookup 把包 ID 映射为官方包名。
func (s *Session) lookup(id int32) string {
	for name, pid := range s.suite.IDs["play"]["serverbound"] {
		if pid == id {
			return name
		}
	}
	return ""
}

// --- entity.Sender 实现 ---

// SendPlay 构建并发送一个 play 状态包。
func (s *Session) SendPlay(packetName string, build func(w *protocol.Writer)) error {
	id, ok := s.suite.ID("play", "clientbound", packetName)
	if !ok {
		log.Printf("[session %s] 未知包名 %s", s.name, packetName)
		return nil
	}
	return s.conn.WritePacket(id, build)
}

// SendRawPlay 发送预构建的 play 包体。
func (s *Session) SendRawPlay(packetName string, body []byte) error {
	id, ok := s.suite.ID("play", "clientbound", packetName)
	if !ok {
		return nil
	}
	return s.conn.WritePacket(id, func(w *protocol.Writer) { w.RawBytes(body) })
}

// Addr 返回对端地址。
func (s *Session) Addr() string { return s.conn.RemoteAddr().String() }

// Kick 断开连接（主循环内调用）。
func (s *Session) Kick(reason string) {
	_ = s.SendPlay("minecraft:disconnect", func(w *protocol.Writer) {
		packet.WriteSystemChat(w, reason, false)
	})
	s.conn.Close()
	s.closed.Store(true)
}
