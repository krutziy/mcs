package game

import (
	"log"

	"mcserver/internal/entity"
	"mcserver/internal/packet"
	"mcserver/internal/protocol"
)

// ---- 生命值 / 伤害 / 重生（26.3；set_health 与 entity_event 格式经公开协议文档确认） ----

// 伤害源类型 ID（damage_event 的 source type，取通用子集；generic=1 兜底）。
const (
	damageGeneric int32 = 1
	damageFall    int32 = 14 // minecraft:fall
	damageStarve  int32 = 20 // minecraft:starve
	damageVoid    int32 = 26 // minecraft:out_of_world
)

// syncHealth 同步玩家的生命/食物/饱和。
func (g *Game) syncHealth(p *entity.Player) {
	h, f, s := p.Health, p.Food, p.Saturation
	p.Send.SendPlay("minecraft:set_health", func(w *protocol.Writer) {
		packet.WriteSetHealth(w, h, f, s)
	})
}

// damage 对玩家施加伤害：扣血、发 damage_event（本人）+ 受击动画（旁观者），
// 归零则进入死亡流程。创造模式免疫。
func (g *Game) damage(p *entity.Player, amount float32, sourceType int32) {
	if p.Dead || g.isCreative(p) || amount <= 0 {
		return
	}
	amount = g.armorAbsorb(p, amount)
	if p.Health > amount {
		p.Health -= amount
	} else {
		p.Health = 0
	}
	g.sendDamageVisual(p, sourceType)
	g.syncHealth(p)
	if p.Health <= 0 {
		g.killPlayer(p)
	}
}

// sendDamageVisual 发送受击表现：本人 damage_event，所有观察者 entity_event(2)。
func (g *Game) sendDamageVisual(p *entity.Player, sourceType int32) {
	eid, yaw := p.EntityID, p.Yaw
	p.Send.SendPlay("minecraft:damage_event", func(w *protocol.Writer) {
		packet.WriteDamageEvent(w, eid, sourceType, -1, -1)
	})
	for _, other := range g.players {
		if other.EntityID == p.EntityID {
			continue
		}
		other.Send.SendPlay("minecraft:entity_event", func(w *protocol.Writer) {
			packet.WriteEntityEvent(w, eid, 2) // 2 = 受击
		})
		other.Send.SendPlay("minecraft:hurt_animation", func(w *protocol.Writer) {
			packet.WriteHurtAnimation(w, eid, yaw)
		})
	}
}

// killPlayer 死亡：广播死亡动画（entity_event 3），标记待重生。
func (g *Game) killPlayer(p *entity.Player) {
	p.Dead = true
	g.dropInventoryOnDeath(p)
	eid := p.EntityID
	for _, other := range g.players {
		other.Send.SendPlay("minecraft:entity_event", func(w *protocol.Writer) {
			packet.WriteEntityEvent(w, eid, 3) // 3 = 死亡动画
		})
	}
	g.BroadcastSystem("§7" + p.Name + " 死了")
	g.Bus.emit(PlayerDied{Player: p})
}

// handleRespawn 处理重生请求（client_command action 0）：发 respawn 包、
// 传送回出生点、恢复状态并重新同步物品栏与生命。
func (g *Game) handleRespawn(p *entity.Player) {
	if !p.Dead {
		return
	}
	p.Dead = false
	p.Health, p.Food, p.Saturation = 20, 20, 5
	sess := g.sessionOf(p)
	showTail := sess != nil && sess.ver.LoginTrailingByte
	p.Send.SendPlay("minecraft:respawn", func(w *protocol.Writer) {
		packet.WriteRespawn(w, packet.Respawn{
			DimensionID: 0, WorldName: "minecraft:overworld",
			HashedSeed: 0, GameMode: packet.GameMode(p.GameMode), PreviousGameMode: -1,
			IsFlat: true, PortalCooldown: 0, SeaLevel: -63,
			DataKept: false, TailApplies: showTail,
		})
	})
	spX, spY, spZ := g.Cfg.SpawnX, g.Cfg.SpawnY, g.Cfg.SpawnZ
	if p.SpawnPos != [3]float64{} {
		spX, spY, spZ = p.SpawnPos[0], p.SpawnPos[1], p.SpawnPos[2]
	}
	p.X, p.Y, p.Z = spX, spY, spZ
	p.TeleportID++
	id := p.TeleportID
	x, y, z := p.X, p.Y, p.Z
	p.Send.SendPlay("minecraft:player_position", func(w *protocol.Writer) {
		packet.WritePlayerPosition(w, packet.PlayerPositionOut{TeleportID: id, X: x, Y: y, Z: z})
	})
	g.syncHealth(p)
	g.syncInventory(p)
	if sess != nil {
		g.sendChunks(sess, p, int32(x)>>4, int32(z)>>4)
	}
	log.Printf("[game] %s 重生", p.Name)
}

// tickStats 每 80 刻（4 秒）推进：回血/挨饿/虚空伤害。
func (g *Game) tickStats() {
	if g.tick%80 != 0 {
		return
	}
	for _, p := range g.players {
		if p.Dead || g.isCreative(p) {
			continue
		}
		switch {
		case p.Y < -96: // 虚空
			g.damage(p, 4, damageVoid)
		case p.Health < 20 && p.Food >= 18: // 回血（消耗饱和→食物）
			p.Health++
			if p.Saturation > 0 {
				p.Saturation--
			} else {
				p.Food--
			}
			g.syncHealth(p)
		case p.Food <= 0 && p.Health > 1: // 饥饿伤害（普通难度：止于半心）
			g.damage(p, 1, damageStarve)
		}
	}
}

// exhaustMovement 按移动距离累积体力消耗（走 0.01/格，疾跑 0.1/格的简化）。
func (g *Game) exhaustMovement(p *entity.Player, dx, dz float64, sprint bool) {
	if p.Dead || g.isCreative(p) {
		return
	}
	p.Exhaustion += (dx*dx + dz*dz) * map[bool]float64{false: 0.01, true: 0.1}[sprint]
	for p.Exhaustion >= 4 {
		p.Exhaustion -= 4
		if p.Saturation > 0 {
			p.Saturation--
		} else if p.Food > 0 {
			p.Food--
			g.syncHealth(p)
		}
	}
}

// trackFall 由移动事件推进摔落追踪：记录下落起点，落地结算伤害。
func (g *Game) trackFall(p *entity.Player, y float64, onGround bool) {
	if g.isCreative(p) || p.Dead {
		p.FallStart = 0
		return
	}
	if !onGround {
		if p.FallStart == 0 || y > p.FallStart {
			p.FallStart = y
		}
		return
	}
	if p.FallStart != 0 {
		dmg := int32(p.FallStart-y) - 3
		if dmg > 0 {
			g.damage(p, float32(dmg), damageFall)
		}
		p.FallStart = 0
	}
}
