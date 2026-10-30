package game

// 末影龙战斗：黑曜石柱环上的末影水晶（存活时治疗龙）、龙绕岛盘旋 +
// 周期俯冲、Boss 血条（minecraft:boss_event）、近战/箭矢伤害、
// 死亡 → 大量经验 + 出口门上方龙蛋。endstate.json 持久化已击败标记。

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"

	"mcserver/internal/entity"
	"mcserver/internal/packet"
	"mcserver/internal/protocol"
	"mcserver/internal/world"

	"mcserver/plugins/terrain"
)

// dragonState 一场龙战。
type dragonState struct {
	eid      int32
	uuid     protocol.UUID
	dim      int32
	health   float32
	angle    float64 // 盘旋相位
	radius   float64
	px, pz   float64 // 当前位置
	y        float64
	swoop    int32 // 俯冲剩余刻（0 = 盘旋）
	swoopTgt [3]float64
	attackCd int32 // 近身伤害冷却
	barSent  map[int32]bool // 已发血条的玩家
}

// endCrystal 一个末影水晶。
type endCrystal struct {
	eid    int32
	uuid   protocol.UUID
	dim    int32
	x, y, z float64
}

// bossBarSend 发送 Boss 血条（action: 0=add 1=remove 2=health）。
func (g *Game) bossBarSend(p *entity.Player, uuid protocol.UUID, action int32, health float32) {
	u := uuid
	act := action
	h := health
	switch action {
	case 0:
		p.Send.SendPlay("minecraft:boss_event", func(w *protocol.Writer) {
			w.UUID(u)
			w.VarInt(act)
			protocol.WriteNBT(w, protocol.Compound{"text": "Ender Dragon"})
			w.Float(h)
			w.VarInt(1)  // color: pink
			w.VarInt(1)  // dividers: 6 段
			w.Uint8(0)   // flags
		})
	case 1:
		p.Send.SendPlay("minecraft:boss_event", func(w *protocol.Writer) {
			w.UUID(u)
			w.VarInt(act)
		})
	case 2:
		p.Send.SendPlay("minecraft:boss_event", func(w *protocol.Writer) {
			w.UUID(u)
			w.VarInt(act)
			w.Float(h)
		})
	}
}

// ensureDragonFight 玩家进入末地时确保龙战存在（已击败则跳过）。
func (g *Game) ensureDragonFight() {
	if g.dragon != nil || g.dragonDefeated {
		return
	}
	// 龙
	eid := g.nextEID.Add(1)
	d := &dragonState{eid: eid, uuid: entityUUID(eid), dim: DimEnd,
		health: 200, radius: 35, y: float64(world.MinY + 96), barSent: map[int32]bool{}}
	g.dragon = d
	for _, v := range g.players {
		if v.Dim != DimEnd {
			continue
		}
		typ := g.entityTypeID(v, "ender_dragon")
		eid2 := eid
		v.Send.SendPlay("minecraft:add_entity", func(w *protocol.Writer) {
			packet.SpawnEntity{EntityID: eid2, UUID: d.uuid, Type: typ,
				X: 0, Y: d.y, Z: 0}.Write(w)
		})
	}
	// 柱顶水晶
	for _, pl := range terrain.EndPillars(g.Cfg.WorldSeed) {
		ceid := g.nextEID.Add(1)
		c := &endCrystal{eid: ceid, uuid: entityUUID(ceid), dim: DimEnd,
			x: float64(pl.X) + 0.5, y: float64(world.MinY + pl.TopRel + 1), z: float64(pl.Z) + 0.5}
		g.crystals[ceid] = c
		for _, v := range g.players {
			if v.Dim != DimEnd {
				continue
			}
			typ := g.entityTypeID(v, "end_crystal")
			ceid2, cx, cy, cz := ceid, c.x, c.y, c.z
			v.Send.SendPlay("minecraft:add_entity", func(w *protocol.Writer) {
				packet.SpawnEntity{EntityID: ceid2, UUID: c.uuid, Type: typ,
					X: cx, Y: cy, Z: cz}.Write(w)
			})
		}
	}
	g.BroadcastSystem("§5末影龙苏醒了……")
}

// tickDragon 龙战推进：盘旋/俯冲、水晶治疗、近身伤害、血条同步。
func (g *Game) tickDragon() {
	d := g.dragon
	if d == nil {
		return
	}
	// 无人在线末地：暂停（血条对离开者清理）
	inEnd := 0
	for _, p := range g.players {
		if p.Dim == DimEnd && !p.Dead {
			inEnd++
		}
		if p.Dim == DimEnd && !d.barSent[p.EntityID] {
			d.barSent[p.EntityID] = true
			g.bossBarSend(p, d.uuid, 0, d.health/200)
		}
		if p.Dim != DimEnd && d.barSent[p.EntityID] {
			delete(d.barSent, p.EntityID)
			g.bossBarSend(p, d.uuid, 1, 0)
		}
	}
	if inEnd == 0 {
		return
	}
	// 水晶治疗（每存活水晶 0.1/刻）
	for _, c := range g.crystals {
		_ = c
		if d.health < 200 {
			d.health += 0.1
		}
	}
	// 移动
	if d.swoop > 0 {
		d.swoop--
		dx, dy, dz := d.swoopTgt[0]-d.px, d.swoopTgt[1]-d.y, d.swoopTgt[2]-d.pz
		dd := math.Sqrt(dx*dx + dy*dy + dz*dz)
		if dd < 4 || d.swoop == 0 {
			d.swoop = 0 // 俯冲结束回盘旋
		} else {
			speed := 0.9
			// 前方地形阻挡：抬升绕过
			nx, nz := d.px+dx/dd*speed*4, d.pz+dz/dd*speed*4
			if g.blockAtDim(DimEnd, floorI(nx), floorI(d.y-2), floorI(nz)) {
				d.y += 0.8
			} else {
				d.px += dx / dd * speed
				d.pz += dz / dd * speed
				d.y += dy / dd * speed
			}
		}
	} else {
		d.angle += 0.02
		d.px = math.Cos(d.angle) * d.radius
		d.pz = math.Sin(d.angle) * d.radius
		d.y = float64(world.MinY+96) + math.Sin(d.angle*3)*4
		// 每 ~10 秒对最近玩家俯冲
		if g.tick%200 == 0 {
			if t := g.nearestPlayerDim(DimEnd, d.px, d.y, d.pz, 64); t != nil {
				d.swoop = 100
				d.swoopTgt = [3]float64{t.X, t.Y + 1, t.Z}
			}
		}
	}
	// 每 10 刻广播位置
	if g.tick%10 == 0 {
		eid, x, y, z := d.eid, d.px, d.y, d.pz
		for _, v := range g.players {
			if v.Dim != DimEnd {
				continue
			}
			v.Send.SendPlay("minecraft:teleport_entity", func(w *protocol.Writer) {
				packet.WriteTeleportEntity(w, eid, x, y, z, 0, 0, 0, 0, 0, true)
			})
		}
	}
	// 近身伤害（俯冲穿人）
	if d.attackCd > 0 {
		d.attackCd--
	} else {
		for _, p := range g.players {
			if p.Dim != DimEnd || p.Dead {
				continue
			}
			if dist2(p.X, p.Y, p.Z, d.px, d.y-1, d.pz) < 25 {
				g.damage(p, 10, damageGeneric)
				dx, dz := p.X-d.px, p.Z-d.pz
				dd := math.Sqrt(dx*dx + dz*dz)
				if dd > 0.1 {
					p.X += dx / dd * 3
					p.Z += dz / dd * 3
				}
				p.FallStart = 0
				d.attackCd = 20
				break
			}
		}
	}
}

// damageDragon 对龙造成伤害；致死触发战利品流程。
func (g *Game) damageDragon(amount float32, by *entity.Player) {
	d := g.dragon
	if d == nil {
		return
	}
	// 水晶全灭前龙受伤害减半（近似原版治疗压制）
	if len(g.crystals) > 0 {
		amount /= 2
	}
	d.health -= amount
	h := d.health / 200
	if h < 0 {
		h = 0
	}
	for pid := range d.barSent {
		if p := g.players[pid]; p != nil {
			g.bossBarSend(p, d.uuid, 2, h)
		}
	}
	if d.health <= 0 {
		g.killDragon(by)
	}
}

// killDragon 龙死亡：经验雨、龙蛋、清理、血条移除、持久化。
func (g *Game) killDragon(by *entity.Player) {
	d := g.dragon
	if d == nil {
		return
	}
	for pid := range d.barSent {
		if p := g.players[pid]; p != nil {
			g.bossBarSend(p, d.uuid, 1, 0)
		}
	}
	for _, p := range g.players {
		p.Send.SendPlay("minecraft:remove_entities", func(w *protocol.Writer) {
			packet.WriteRemoveEntities(w, []int32{d.eid})
		})
	}
	// 剩余水晶一并消散
	for cid, c := range g.crystals {
		_ = c
		delete(g.crystals, cid)
		for _, p := range g.players {
			p.Send.SendPlay("minecraft:remove_entities", func(w *protocol.Writer) {
				packet.WriteRemoveEntities(w, []int32{cid})
			})
		}
	}
	// 经验雨（共 12000，24 球 × 500）
	for i := 0; i < 24; i++ {
		g.spawnXPOrb(DimEnd, float64(i%5)*2, float64(world.MinY+84), float64(i/5)*2, 500)
	}
	// 出口门顶龙蛋（中心柱 top+2）；先确保中心区块已生成
	w := g.wd(DimEnd)
	w.Chunk(0, 0)
	for y := int32(world.MinY + 60); y < world.MinY+120; y++ {
		if w.GetBlock(0, y, 0) == world.StateBedrock {
			w.SetBlock(0, y+3, 0, 11212) // minecraft:dragon_egg
			g.recordBlockChange(DimEnd, 0, y+3, 0, 11212)
			break
		}
	}
	g.flushBlockUpdates()
	g.dragon = nil
	g.dragonDefeated = true
	g.saveEndState()
	g.playSoundAt(DimEnd, 0, float64(world.MinY+90), 0, SoundHostile, "entity.ender_dragon.death", 2, 1)
	g.BroadcastSystem("§d末影龙被击败了！")
	if by != nil {
		_ = by
	}
}

// destroyCrystal 攻击/射中水晶：小爆 + 移除。
func (g *Game) destroyCrystal(c *endCrystal, by *entity.Player) {
	delete(g.crystals, c.eid)
	for _, p := range g.players {
		if p.Dim != DimEnd {
			continue
		}
		p.Send.SendPlay("minecraft:remove_entities", func(w *protocol.Writer) {
			packet.WriteRemoveEntities(w, []int32{c.eid})
		})
	}
	g.playSoundAt(DimEnd, c.x, c.y, c.z, SoundBlock, "block.glass.break", 1, 1)
	g.explode(c.dim, c.x, c.y, c.z, 2)
	_ = by
}

// saveEndState / loadEndState 龙战进度持久化。
func (g *Game) saveEndState() {
	if g.Cfg.SaveDir == "" {
		return
	}
	b, _ := json.Marshal(map[string]bool{"defeated": g.dragonDefeated})
	_ = os.WriteFile(filepath.Join(g.Cfg.SaveDir, "endstate.json"), b, 0o644)
}

func (g *Game) loadEndState() {
	if g.Cfg.SaveDir == "" {
		return
	}
	b, err := os.ReadFile(filepath.Join(g.Cfg.SaveDir, "endstate.json"))
	if err != nil {
		return
	}
	var st struct {
		Defeated bool `json:"defeated"`
	}
	if json.Unmarshal(b, &st) == nil {
		g.dragonDefeated = st.Defeated
	}
}

// crystalsHitCheck 箭矢命中检测：命中水晶则销毁并返回 true。
func (g *Game) crystalsHitCheck(x, y, z float64) (*endCrystal, bool) {
	for _, c := range g.crystals {
		if dist2(x, y, z, c.x, c.y, c.z) < 4 {
			g.destroyCrystal(c, nil)
			return c, true
		}
	}
	return nil, false
}
