package game

// 动物繁殖：手持对应食物右键两只同种成体动物（interact 包 type=0），
// 双亲进入 30 秒爱慕冷却并生成 1 只幼崽（体型减半、20 秒长大）。
// 食物对应（公开数值）：麦类→牛/羊、胡萝卜→猪、种子→鸡。

import (
	"mcserver/internal/entity"
	"mcserver/internal/packet"
	"mcserver/internal/protocol"
)

// breedFoods 动物 → 可喂食物短名。
var breedFoods = map[string][]string{
	"cow":   {"wheat"},
	"sheep": {"wheat"},
	"pig":   {"carrot", "potato", "beetroot"},
	"chicken": {"wheat_seeds", "melon_seeds", "pumpkin_seeds"},
}

// mob 扩展：繁殖状态存独立表（不动 mobs.go 的结构）。
type breedState struct {
	loveCd   int32 // 爱慕冷却（>0 不可再喂）
	baby     bool
	growCd   int32 // 幼崽成长
}

// handleInteract 右键生物（喂食/繁殖）。
func (g *Game) handleInteract(ev *playerInteractEvent) {
	p := ev.p
	if p == nil || p.Dead {
		return
	}
	m, ok := g.mobs[ev.target]
	if !ok {
		return
	}
	foods, breedable := breedFoods[m.kind.name]
	if !breedable {
		return
	}
	held := p.Held()
	if held.ID == 0 {
		return
	}
	foodName := g.itemNameOf(p, held.ID)
	okFood := false
	for _, f := range foods {
		if f == foodName {
			okFood = true
			break
		}
	}
	if !okFood {
		return
	}
	bs := g.breeds[ev.target]
	if bs != nil && (bs.loveCd > 0 || bs.baby) {
		return
	}
	// 消耗食物
	if !g.isCreative(p) {
		s := &p.Slots[36+p.HeldSlot]
		s.Count--
		if s.Count <= 0 {
			*s = entity.Item{}
		}
		g.syncInventory(p)
	}
	if bs == nil {
		bs = &breedState{}
		g.breeds[ev.target] = bs
	}
	bs.loveCd = 600 // 30 秒
	g.sendLoveHearts(m)

	// 找附近另一只同种成体且爱慕中的 → 生崽
	for mid, other := range g.mobs {
		if mid == ev.target || other.kind.name != m.kind.name {
			continue
		}
		obs := g.breeds[mid]
		if obs == nil || obs.loveCd <= 0 || obs.baby {
			continue
		}
		if dist2(other.x, other.y, other.z, m.x, m.y, m.z) > 36 { // 6 格内
			continue
		}
		// 繁殖：双方冷却重置为长冷却，生成幼崽
		bs.loveCd = 3000 // 5 分钟繁殖冷却（原版 5 分钟）
		obs.loveCd = 3000
		g.sendLoveHearts(other)
		baby := g.spawnMob(m.kind.name, (m.x+other.x)/2, m.y+0.5, (m.z+other.z)/2)
		if baby != nil {
			g.breeds[baby.eid] = &breedState{baby: true, growCd: 600}
		}
		return
	}
}

// sendLoveHearts 爱心粒子（level_event 18=爱心）。
func (g *Game) sendLoveHearts(m *mob) {
	for _, v := range g.chunkViewers(int32(m.x)>>4, int32(m.z)>>4) {
		x, y, z := int32(m.x), int32(m.y+1), int32(m.z)
		v.Send.SendPlay("minecraft:level_event", func(w *protocol.Writer) {
			packet.WriteLevelEvent(w, 18, x, y, z, 0, false)
		})
	}
}

// tickBreeds 繁殖状态推进：冷却递减、幼崽长大（元数据缩放暂缺，用时间到即成体）。
func (g *Game) tickBreeds() {
	for eid, bs := range g.breeds {
		if bs.loveCd > 0 {
			bs.loveCd--
		}
		if bs.baby {
			bs.growCd--
			if bs.growCd <= 0 {
				bs.baby = false
				delete(g.breeds, eid) // 成体清除状态
			}
		}
		if bs.loveCd == 0 && !bs.baby {
			delete(g.breeds, eid)
		}
	}
}

var _ = protocol.Position{}
