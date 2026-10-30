package game

import (
	"log"

	"mcserver/internal/entity"
	"mcserver/internal/packet"
	"mcserver/internal/protocol"
	"mcserver/internal/world"
)

// ---- 生命值 / 伤害 / 重生（26.3；set_health 与 entity_event 格式经公开协议文档确认） ----

// 伤害源类型 ID（damage_event 的 source type）。
// damage_type 注册表按字母序编号（官方 26.3 jar data/minecraft/damage_type/
// 共 51 项；官方服抓包验证 mob_attack=28）：
//   0 arrow 1 bad_respawn_point 2 cactus 3 campfire 4 cramming 5 dragon_breath
//   6 drown 7 dry_out 8 ender_pearl 9 explosion 10 fall 11 falling_anvil
//   12 falling_block 13 falling_stalactite 14 fireball 15 fireworks
//   16 fly_into_wall 17 freeze 18 generic 19 generic_kill 20 hot_floor
//   21 in_fire 22 in_wall 23 indirect_magic 24 lava 25 lightning_bolt
//   26 mace_smash 27 magic 28 mob_attack ... 31 on_fire 32 out_of_world
//   ... 40 starve ...
const (
	damageArrow     int32 = 0
	damageDrown     int32 = 6
	damageExplosion int32 = 9
	damageFall      int32 = 10
	damageGeneric   int32 = 18
	damageInFire    int32 = 21
	damageLava      int32 = 24
	damageMobAttack int32 = 28
	damageOnFire    int32 = 31
	damageVoid      int32 = 32
	damageStarve    int32 = 40
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
	p.EatTicks = -1
	p.DigActive = false
	p.FireTicks = 0
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
	p.Health, p.Food, p.Saturation, p.Air = 20, 20, 5, maxAirSupply
	p.FireTicks = 0
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
	g.syncAirSupply(p)
	if sess != nil {
		g.sendChunks(sess, p, int32(x)>>4, int32(z)>>4)
	}
	log.Printf("[game] %s 重生", p.Name)
}

// ---- 环境伤害：溺水 / 火焰 / 岩浆（每刻推进） ----

const (
	maxAirSupply      int32 = 300          // 氧气满值（刻）
	lavaFireTicks     int32 = 300          // 岩浆离场后的燃烧时长
	fireBlockFireTick int32 = 160          // 火焰方块离场后的燃烧时长（8 秒）
)

// tickEnvironmental 每刻推进：氧气（头部浸水递减/出水恢复+溺水伤害）、
// 火焰与岩浆伤害、燃烧计时（入水/雨天熄灭）。
func (g *Game) tickEnvironmental() {
	for _, p := range g.players {
		if p.Dead || g.isCreative(p) {
			continue
		}
		g.berryBushPrickle(p)
		// 中毒：每 25 刻 1 伤（止于半心）
		if p.Effects["poison"] > 0 && g.tick%25 == 0 && p.Health > 1 {
			g.damage(p, 1, damageGeneric)
		}
		eye := g.w(p).GetBlock(floorI(p.X), floorI(p.Y+1.62), floorI(p.Z))
		body := g.w(p).GetBlock(floorI(p.X), floorI(p.Y), floorI(p.Z))
		feet := g.w(p).GetBlock(floorI(p.X), floorI(p.Y+0.2), floorI(p.Z))
		submerged := eye >= world.WaterMin && eye <= world.WaterMax
		inWater := submerged || (body >= world.WaterMin && body <= world.WaterMax)
		inLava := (feet >= world.LavaMin && feet <= world.LavaMax) ||
			(eye >= world.LavaMin && eye <= world.LavaMax)
		inFireBlock := eye >= world.FireMin && eye <= world.FireMax ||
			feet >= world.FireMin && feet <= world.FireMax

		fireResist := p.Effects["fire_resistance"] > 0

		// --- 氧气与溺水 ---
		prevAir := p.Air
		if submerged {
			if p.Effects["water_breathing"] <= 0 && p.Air > 0 {
				p.Air--
			}
			if p.Air <= 0 && g.tick%20 == 0 {
				g.damage(p, 2, damageDrown)
			}
		} else if p.Air < maxAirSupply {
			p.Air += 4
			if p.Air > maxAirSupply {
				p.Air = maxAirSupply
			}
		}
		if p.Air != prevAir && (p.Air == maxAirSupply || p.Air%20 == 0) {
			g.syncAirSupply(p)
		}

		// --- 着火与岩浆 ---
		switch {
		case inLava:
			if !fireResist {
				p.FireTicks = lavaFireTicks
				if g.tick%10 == 0 {
					g.damage(p, 4, damageLava)
				}
			}
		case inFireBlock:
			if !fireResist {
				p.FireTicks = fireBlockFireTick
				if g.tick%10 == 0 {
					g.damage(p, 1, damageInFire)
				}
			}
		case inWater || (g.raining && g.skyVisible(p)):
			// 水中/雨中熄灭
			if p.FireTicks != 0 {
				p.FireTicks = 0
				g.sendFireState(p, false)
			}
		}
		if p.FireTicks > 0 {
			if p.FireTicks%20 == 0 {
				g.sendFireState(p, true)
			}
			p.FireTicks--
			if p.FireTicks == 0 {
				g.sendFireState(p, false)
			}
			if !fireResist && p.FireTicks%10 == 0 {
				g.damage(p, 1, damageOnFire)
			}
		}
	}
}

// skyVisible 玩家头顶 24 格内无实体方块（雨中熄火判定，简化）。
func (g *Game) skyVisible(p *entity.Player) bool {
	x, z := floorI(p.X), floorI(p.Z)
	for dy := int32(2); dy < 26; dy++ {
		if g.blockAt(x, floorI(p.Y)+dy, z) {
			return false
		}
	}
	return true
}

// syncAirSupply 同步氧气余量（实体元数据 index 1 = VarInt）。
func (g *Game) syncAirSupply(p *entity.Player) {
	eid, air := p.EntityID, p.Air
	for _, other := range g.players {
		other.Send.SendPlay("minecraft:set_entity_data", func(w *protocol.Writer) {
			w.VarInt(eid)
			w.Uint8(1)    // index 1 = air supply
			w.VarInt(1)   // 类型 VarInt
			w.VarInt(air)
			w.Uint8(0xFF) // 元数据终止
		})
	}
}

// sendFireState 广播燃烧视觉（实体基础标志 index 0 的 0x01 位）。
func (g *Game) sendFireState(p *entity.Player, burning bool) {
	eid := p.EntityID
	for _, other := range g.players {
		other.Send.SendPlay("minecraft:set_entity_data", func(w *protocol.Writer) {
			w.VarInt(eid)
			w.Uint8(0)    // index 0 = 基础标志
			w.VarInt(0)   // 类型 Byte
			flag := byte(0)
			if burning {
				flag = 0x01
			}
			w.Int8(int8(flag))
			w.Uint8(0xFF)
		})
	}
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
		// 落水免除摔落伤害
		body := g.w(p).GetBlock(floorI(p.X), floorI(p.Y), floorI(p.Z))
		if body < world.WaterMin || body > world.WaterMax {
			dmg := int32(p.FallStart-y) - 3
			if dmg > 0 {
				g.damage(p, float32(dmg), damageFall)
			}
		}
		p.FallStart = 0
	}
}

var _ = protocol.Position{}
