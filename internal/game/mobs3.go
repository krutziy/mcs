package game

// 马（平原生成、驯服概率、上鞍骑乘加速）与村民职业分化（4 职业贸易表）。

import (
	"math/rand"

	"mcserver/internal/assets"
	"mcserver/internal/entity"
	"mcserver/internal/packet"
	"mcserver/internal/protocol"
)

// ---- 马 ----

// horseState 马的驯服状态（按 eid）。
type horseState struct {
	tamed   bool
	saddled bool
	temper  int32 // 驯服进度（每次骑行 +1，≥5 驯服概率大增）
	speed   float64
}

// tickHorseSpawning 马群生成（平原草方块，稀有）。
func (g *Game) tickHorseSpawning() {
	if g.tick%400 != 0 {
		return
	}
	n := 0
	for _, m := range g.mobs {
		if m.kind.name == "horse" {
			n++
		}
	}
	if n >= 4 {
		return
	}
	if x, y, z, ok := g.findSpawnSpot(32, 8 /*grass*/); ok {
		g.spawnMobIn(DimOverworld, "horse", x, y, z)
	}
}

// tryHorseInteract 马交互：驯服 / 上鞍 / 骑乘。
func (g *Game) tryHorseInteract(p *entity.Player, m *mob) bool {
	if m.kind.name != "horse" {
		return false
	}
	hs := g.horses[m.eid]
	if hs == nil {
		hs = &horseState{speed: 0.18 + rand.Float64()*0.1}
		g.horses[m.eid] = hs
	}
	held := p.Held()
	heldName := ""
	if held.ID != 0 {
		heldName = g.itemNameOf(p, held.ID)
	}
	// 上鞍
	if heldName == "saddle" && hs.tamed && !hs.saddled {
		hs.saddled = true
		if !g.isCreative(p) {
			h := &p.Slots[36+p.HeldSlot]
			h.Count--
			if h.Count <= 0 {
				*h = entity.Item{}
			}
			g.syncInventory(p)
		}
		g.mobSound(m, "entity.horse.saddle")
		return true
	}
	// 骑乘（已驯服+鞍）
	if hs.tamed && hs.saddled && heldName != "saddle" {
		g.mountHorse(p, m, hs)
		return true
	}
	// 驯服尝试：骑上 → 概率甩下/驯服
	hs.temper++
	tamed := hs.temper >= 3 && rand.Intn(3) == 0
	if tamed {
		hs.tamed = true
		g.mobSound(m, "entity.horse.angelic")
		g.notify(p, "§7马被驯服了！装上鞍即可骑乘")
	} else {
		g.mobSound(m, "entity.horse.angry")
		g.notify(p, "§7马把你甩了下来…再试试")
	}
	return true
}

// mountHorse 骑马（复用猪骑乘的输入驱动，速度更快）。
func (g *Game) mountHorse(p *entity.Player, m *mob, hs *horseState) {
	if g.riding[p.EntityID] != 0 {
		return
	}
	g.riding[p.EntityID] = m.eid
	p.Vehicle = m.eid
	eid := m.eid
	rider := p.EntityID
	for _, v := range g.players {
		if v.Dim != m.dim {
			continue
		}
		v.Send.SendPlay("minecraft:set_passengers", func(w *protocol.Writer) {
			w.VarInt(eid)
			w.VarInt(1)
			w.VarInt(rider)
		})
	}
	p.X, p.Y, p.Z = m.x, m.y+1.1, m.z
	p.HorseSpeed = hs.speed
}

// ---- 村民职业 ----

// villagerProfessions 4 职业贸易表。
var villagerProfessions = map[string][]villagerTrade{
	"farmer": {
		{"wheat", "", 20, 0, "emerald", 1},
		{"carrot", "", 15, 0, "emerald", 1},
		{"potato", "", 15, 0, "emerald", 1},
		{"emerald", "", 1, 0, "bread", 6},
		{"emerald", "", 3, 0, "apple", 4},
	},
	"butcher": {
		{"porkchop", "", 7, 0, "emerald", 1},
		{"beef", "", 7, 0, "emerald", 1},
		{"chicken", "", 8, 0, "emerald", 1},
		{"emerald", "", 1, 0, "cooked_porkchop", 5},
		{"emerald", "", 1, 0, "cooked_beef", 4},
	},
	"librarian": {
		{"paper", "", 24, 0, "emerald", 1},
		{"book", "", 4, 0, "emerald", 1},
		{"emerald", "", 5, 0, "bookshelf", 1},
		{"emerald", "book", 8, 1, "enchanted_book", 1},
	},
	"blacksmith": {
		{"coal", "", 15, 0, "emerald", 1},
		{"iron_ingot", "", 4, 0, "emerald", 1},
		{"emerald", "", 4, 0, "iron_pickaxe", 1},
		{"emerald", "", 2, 0, "arrow", 16},
		{"emerald", "", 8, 0, "saddle", 1},
	},
}

// villagerProfession 村民的职业（按 eid 稳定派生）。
func villagerProfession(m *mob) string {
	profs := []string{"farmer", "butcher", "librarian", "blacksmith"}
	return profs[int(m.eid)%len(profs)]
}

// professionTrades 职业贸易表。
func professionTrades(prof string) []villagerTrade {
	if t, ok := villagerProfessions[prof]; ok {
		return t
	}
	return villagerProfessions["farmer"]
}

// sendTradeListProf 按职业下发贸易表。
func (g *Game) sendTradeListProf(p *entity.Player, prof string) {
	trades := professionTrades(prof)
	p.Send.SendPlay("minecraft:trade_list", func(w *protocol.Writer) {
		w.VarInt(7)
		w.VarInt(int32(len(trades)))
		for _, t := range trades {
			w.VarInt(g.itemFor(p, assets.ItemIDs(777)[t.input1]))
			w.VarInt(t.count1)
			w.VarInt(0)
			packet.WriteSlot(w, g.itemFor(p, assets.ItemIDs(777)[t.output]), t.outCount)
			if t.input2 != "" {
				w.Bool(true)
				w.VarInt(g.itemFor(p, assets.ItemIDs(777)[t.input2]))
				w.VarInt(t.count2)
				w.VarInt(0)
			} else {
				w.Bool(false)
			}
			w.Bool(false)
			w.Int32(0)
			w.Int32(12)
			w.Int32(0)
			w.Int32(0)
			w.Float(0)
			w.Int32(0)
		}
		w.VarInt(1)
		w.VarInt(0)
		w.Bool(true)
		w.Bool(false)
	})
}
