package game

// 酿造台：右键打开（menu brewing_stand=11，窗口 4），四槽（0 药水、1 药水、
// 2 药水、3 材料 + 燃料在下文简化为不消耗），配方来自官方 jar brewing
// 数据（brewing_777.txt）。药水物品 = potion 物品 ID + potion_contents
// 组件（组件 ID 26），实现带组件 Slot 的最小子集：item_id + potion 组件。

import (
	"encoding/json"
	"os"
	"path/filepath"

	"mcserver/internal/entity"
	"mcserver/internal/packet"
	"mcserver/internal/protocol"
)

const menuBrewing int32 = 11

const brewingStandState uint32 = 11193 // 默认（has_bottle=0）

// potionItemIDs 各协议 potion/splash/lingering 物品 ID（777 基准）。
const (
	itemPotion    = 1271
	itemSplash    = 1442
	itemLingering = 1515
)

// brewingState 酿造台（0-2 药水槽、3 材料槽、brew 进度）。
type brewingState struct {
	slots     [4]entity.Item // slots[3].Data 复用为药水名哈希不行——用 potionName map
	potion    [3]string      // 各药水槽的药水短名（""=非药水）
	brew, brewMax int32
}

// tryOpenBrewing 右键酿造台打开窗口。
func (g *Game) tryOpenBrewing(p *entity.Player, u packet.UseItemOn) bool {
	if g.world.GetBlock(u.X, u.Y, u.Z) != brewingStandState {
		return false
	}
	pos := [3]int32{u.X, u.Y, u.Z}
	g.openBrew[p.EntityID] = pos
	if g.brews[pos] == nil {
		g.brews[pos] = &brewingState{}
	}
	p.Send.SendPlay("minecraft:open_screen", func(w *protocol.Writer) {
		packet.WriteOpenScreen(w, menuBrewing, "Brewing Stand")
	})
	g.syncBrewing(p, pos)
	return true
}

// closeBrewing 关闭窗口。
func (g *Game) closeBrewing(p *entity.Player) {
	delete(g.openBrew, p.EntityID)
	p.Send.SendPlay("minecraft:container_close", func(w *protocol.Writer) {
		packet.WriteContainerClose(w, 4)
	})
	g.syncInventory(p)
}

// brewingSlotRef 窗口 4 槽位 → 引用（0-2 药水、3 材料、4-30 背包、31-39 快捷栏）。
func brewingSlotRef(bs *brewingState, p *entity.Player, slot int32) *entity.Item {
	switch {
	case slot >= 0 && slot <= 3:
		return &bs.slots[slot]
	case slot >= 4 && slot <= 30:
		return &p.Slots[9+(slot-4)]
	case slot >= 31 && slot <= 39:
		return &p.Slots[36+(slot-31)]
	}
	return nil
}

// syncBrewing 同步酿造台窗口。
func (g *Game) syncBrewing(p *entity.Player, pos [3]int32) {
	bs := g.brews[pos]
	if bs == nil {
		return
	}
	slots := make([]packet.ItemSlot, 40)
	for i := 0; i < 4; i++ {
		s := bs.slots[i]
		slot := packet.ItemSlot{ID: s.ID, Count: s.Count}
		if i < 3 && s.ID != 0 {
			slot.Potion = bs.potion[i] // 药水名（客户端组件重建）
		}
		slots[i] = slot
	}
	for i := int32(4); i <= 30; i++ {
		s := p.Slots[9+(i-4)]
		slots[i] = packet.ItemSlot{ID: s.ID, Count: s.Count}
	}
	for i := int32(31); i <= 39; i++ {
		s := p.Slots[36+(i-31)]
		slots[i] = packet.ItemSlot{ID: s.ID, Count: s.Count}
	}
	p.ContainerState++
	state := p.ContainerState
	carried := packet.ItemSlot{ID: p.Cursor.ID, Count: p.Cursor.Count}
	p.Send.SendPlay("minecraft:container_set_content", func(w *protocol.Writer) {
		packet.WriteContainerSetContentPotion(w, 4, state, slots, carried)
	})
	// 进度：0=酿速(0-400)、1=燃料余量(0-20)
	p.Send.SendPlay("minecraft:container_set_data", func(w *protocol.Writer) {
		packet.WriteContainerSetData(w, 4, 0, int16(bs.brew))
	})
}

// tickBrewings 酿造推进（400 刻/次，材料消耗 1，产出替换三槽药水）。
func (g *Game) tickBrewings() {
	for pos, bs := range g.brews {
		reagent := bs.slots[3]
		reagentName := g.itemNameOf777(reagent.ID)
		can := false
		if reagent.ID != 0 {
			for i := 0; i < 3; i++ {
				if bs.slots[i].ID == itemPotion || bs.slots[i].ID == itemSplash || bs.slots[i].ID == itemLingering {
					if out, ok := g.brewing[[2]string{bs.potion[i], reagentName}]; ok && out != "" {
						can = true
						break
					}
				}
			}
		}
		was := bs.brew > 0
		if can {
			bs.brew++
			if bs.brew >= 400 {
				bs.brew = 0
				for i := 0; i < 3; i++ {
					if bs.slots[i].ID == itemPotion || bs.slots[i].ID == itemSplash || bs.slots[i].ID == itemLingering {
						if out, ok := g.brewing[[2]string{bs.potion[i], reagentName}]; ok && out != "" {
							bs.potion[i] = out
						}
					}
				}
				bs.slots[3].Count--
				if bs.slots[3].Count <= 0 {
					bs.slots[3] = entity.Item{}
				}
			}
		} else if bs.brew > 0 {
			bs.brew -= 4
			if bs.brew < 0 {
				bs.brew = 0
			}
		}
		if (bs.brew > 0) != was || bs.brew%20 == 0 {
			for eid, bpos := range g.openBrew {
				if bpos == pos {
					if p := g.players[eid]; p != nil {
						g.syncBrewing(p, pos)
					}
				}
			}
		}
	}
}

// dropBrewingContents 挖掉酿造台掉内容。
func (g *Game) dropBrewingContents(pos [3]int32) {
	bs := g.brews[pos]
	if bs == nil {
		return
	}
	delete(g.brews, pos)
	for eid, bpos := range g.openBrew {
		if bpos == pos {
			delete(g.openBrew, eid)
		}
	}
	for _, it := range bs.slots {
		if it.ID != 0 {
			g.spawnItem(float64(pos[0])+0.5, float64(pos[1])+0.5, float64(pos[2])+0.5, it)
		}
	}
}

type brewJSON struct {
	Pos    [3]int32  `json:"pos"`
	Slots  [][2]int32 `json:"slots"`
	Potion [3]string `json:"potion"`
	Brew   int32     `json:"brew"`
}

func (g *Game) saveBrewings() {
	var list []brewJSON
	for pos, bs := range g.brews {
		c := brewJSON{Pos: pos, Potion: bs.potion, Brew: bs.brew}
		for _, s := range bs.slots {
			c.Slots = append(c.Slots, [2]int32{s.ID, s.Count})
		}
		list = append(list, c)
	}
	b, _ := json.Marshal(list)
	_ = os.WriteFile(filepath.Join(g.Cfg.SaveDir, "brewings.json"), b, 0o644)
}

func (g *Game) loadBrewings() {
	b, err := os.ReadFile(filepath.Join(g.Cfg.SaveDir, "brewings.json"))
	if err != nil {
		return
	}
	var list []brewJSON
	if json.Unmarshal(b, &list) != nil {
		return
	}
	for _, e := range list {
		bs := &brewingState{potion: e.Potion, brew: e.Brew}
		for i, s := range e.Slots {
			if i < 4 {
				bs.slots[i] = entity.Item{ID: s[0], Count: s[1]}
			}
		}
		g.brews[e.Pos] = bs
	}
}


// ---- 药水饮用与效果 ----

// potionEffect 药水效果定义（持续刻/等级按原版常规药水）。
var potionEffects = map[string]struct {
	effect string
	ticks  int32
	amp    int32  // 等级 0=I
	heal   float32 // 瞬时类：直接治疗量
}{
	"healing":          {heal: 4},
	"strong_healing":   {heal: 8},
	"harming":          {heal: -6},
	"swiftness":        {effect: "speed", ticks: 3600, amp: 0},
	"long_swiftness":   {effect: "speed", ticks: 9600, amp: 0},
	"strong_swiftness": {effect: "speed", ticks: 1800, amp: 1},
	"strength":         {effect: "strength", ticks: 3600, amp: 0},
	"regeneration":     {effect: "regeneration", ticks: 900, amp: 0},
	"fire_resistance":  {effect: "fire_resistance", ticks: 3600, amp: 0},
	"water_breathing":  {effect: "water_breathing", ticks: 3600, amp: 0},
	"night_vision":     {effect: "night_vision", ticks: 3600, amp: 0},
	"leaping":          {effect: "jump_boost", ticks: 3600, amp: 0},
	"slow_falling":     {effect: "slow_falling", ticks: 1800, amp: 0},
}

// drinkPotion use_item 手持药水：应用效果并消耗（玻璃瓶返还简化为无）。
func (g *Game) drinkPotion(p *entity.Player) bool {
	held := p.Held()
	if held.ID != itemPotion && held.ID != itemSplash && held.ID != itemLingering {
		return false
	}
	// 手上药水的名字：从玩家光标/槽元数据（我们未存 per-slot 药水名——
	// 通过 give 时记录的 map 查；酿造产物入槽时已记录在 bs.potion，
	// 拾取到物品栏时经 giveItem 复制到 itemPotions[id]？——简化：
	// 物品栏的药水名存 Player.PotionOf map（giveItem/takeResult 时填）。
	name := p.PotionOf[heldSlotKey(p)]
	if name == "" {
		name = p.PotionOf[[2]int32{-1, 0}] // 手持时从光标记录取
	}
	if name == "" {
		name = "water" // 无记录视作水瓶
	}
	def, ok := potionEffects[name]
	if !ok {
		g.notify(p, "§7这瓶药水没有效果")
		return true
	}
	if def.heal > 0 {
		p.Health += def.heal
		if p.Health > 20 {
			p.Health = 20
		}
	} else if def.heal < 0 {
		g.damage(p, -def.heal, damageGeneric)
	}
	if def.effect != "" {
		p.Effects[def.effect] = def.ticks
		p.EffectAmp[def.effect] = def.amp
	}
	// 消耗
	s := &p.Slots[36+p.HeldSlot]
	s.Count--
	if s.Count <= 0 {
		*s = entity.Item{}
		delete(p.PotionOf, heldSlotKey(p))
	}
	g.syncHealth(p)
	g.syncEffects(p)
	return true
}

func heldSlotKey(p *entity.Player) [2]int32 { return [2]int32{36 + p.HeldSlot, 0} }

// tickEffects 效果计时：再生回血、速度/跳跃由客户端表现（服务端只记账）、到期移除。
func (g *Game) tickEffects() {
	if g.tick%25 != 0 { // 再生每 1.25 秒 +1（原版 50 刻）
		return
	}
	for _, p := range g.players {
		if p.Dead {
			continue
		}
		changed := false
		for name, t := range p.Effects {
			t -= 25
			if name == "regeneration" && p.Health < 20 {
				p.Health++
				changed = true
			}
			if t <= 0 {
				delete(p.Effects, name)
				delete(p.EffectAmp, name)
				changed = true
			} else {
				p.Effects[name] = t
			}
		}
		if changed {
			g.syncHealth(p)
			g.syncEffects(p)
		}
	}
}

// syncEffects update_mob_effect（简化：全部用"添加"模式 1）。
func (g *Game) syncEffects(p *entity.Player) {
	// 逐效果发送（effect ID 原版：1=速度 5=力量 10=再生 12=防火 13=夜视
	// 14=水下呼吸 16=跳跃 18=缓降）
	ids := map[string]int32{"speed": 1, "strength": 5, "regeneration": 10,
		"fire_resistance": 12, "night_vision": 14, "water_breathing": 13,
		"jump_boost": 16, "slow_falling": 18}
	for name, t := range p.Effects {
		id, ok := ids[name]
		if !ok {
			continue
		}
		eid, ticks, amp := p.EntityID, t, p.EffectAmp[name]
		p.Send.SendPlay("minecraft:update_mob_effect", func(w *protocol.Writer) {
			w.VarInt(eid)
			w.Int8(int8(id))
			w.Int8(int8(amp))
			w.VarInt(ticks)
			w.Int8(1) // 添加
		})
	}
}
