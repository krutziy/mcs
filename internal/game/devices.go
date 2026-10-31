package game

// 唱片机（放碟播放 level_event 1010 / 取碟 1011）与音符盒（右键调音 + 播放；
// 音高 2^(note/12)，状态族 note_block[instrument=harp,note=N]）。
// 钓鱼（抛竿 → 浮标实体 → 收线得鱼 + 经验 + 耐久）。

import (
	"math"
	"strings"

	"mcserver/internal/assets"
	"mcserver/internal/entity"
	"mcserver/internal/packet"
	"mcserver/internal/protocol"
)

// 音符盒 harp 状态族：note N powered=false = 681 + N*2（由 blocknames_full
// 启动时解析，解析失败退回该推导式）。
var noteBlockState = [25]uint32{}

func init() {
	for n := int32(0); n < 25; n++ {
		noteBlockState[n] = uint32(681 + n*2)
	}
	// 从全名资产校准（instrument=harp,note=N,powered=false）
	for st, full := range assets.BlockNamesFull(777) {
		if !strings.HasPrefix(full, "minecraft:note_block[instrument=harp,note=") {
			continue
		}
		if !strings.Contains(full, ",powered=false]") {
			continue
		}
		rest := full[len("minecraft:note_block[instrument=harp,note="):]
		note := int32(0)
		for _, c := range rest {
			if c < '0' || c > '9' {
				break
			}
			note = note*10 + int32(c-'0')
		}
		if note >= 0 && note < 25 {
			noteBlockState[note] = st
		}
	}
}

// noteOfState 音符盒状态 → 音高（非 harp/powered 族按区间近似）。
func noteOfState(s uint32) int32 {
	if s < 680 || s > 729 {
		return -1
	}
	// harp 族：note N = 680+N*2(powered)/681+N*2(false) → 统一 (s-680)/2
	return (int32(s) - 680) / 2
}

// tryTuneNoteBlock 右键音符盒：音高 +1（循环 25）并播放。
func (g *Game) tryTuneNoteBlock(p *entity.Player, u packet.UseItemOn) bool {
	s := g.w(p).GetBlock(u.X, u.Y, u.Z)
	note := noteOfState(s)
	if note < 0 || g.blockNames[s] != "note_block" {
		return false
	}
	next := (note + 1) % 25
	g.applyBlock(p.Dim, u.X, u.Y, u.Z, noteBlockState[next], u.Sequence, p)
	g.playNote(p.Dim, u.X, u.Y, u.Z, next)
	return true
}

// noteInstrument 按下方方块选乐器（原版映射的常用子集，缺省竖琴）。
func noteInstrument(below string) string {
	switch below {
	case "stone", "cobblestone", "stone_bricks", "deepslate", "granite", "diorite", "andesite",
		"obsidian", "bedrock", "coal_ore", "iron_ore", "diamond_ore":
		return "basedrum"
	case "oak_planks", "spruce_planks", "birch_planks", "oak_log", "spruce_log", "bookshelf",
		"crafting_table", "chest", "note_block":
		return "bass"
	case "sand", "gravel", "glass", "soul_sand":
		return "snare"
	case "grass_block", "dirt":
		return "hat"
	case "gold_block", "glowstone":
		return "bell"
	case "clay", "packed_ice":
		return "flute"
	case "white_wool", "red_wool", "blue_wool":
		return "guitar"
	case "nether_bricks", "netherrack":
		return "basedrum"
	}
	return "harp"
}

// playNote 播放音符（音高 = 2^(note/12)，音色按下方方块）。
func (g *Game) playNote(dim int32, x, y, z int32, note int32) {
	pitch := float32(math.Pow(2, float64(note-12)/12.0)) // note 12 = 中央 C 上方基准
	below := ""
	if g.blockNames[g.wd(dim).GetBlock(x, y-1, z)] != "" {
		below = g.blockNames[g.wd(dim).GetBlock(x, y-1, z)]
	}
	g.playSoundAt(dim, float64(x), float64(y), float64(z), SoundBlock,
		"block.note_block."+noteInstrument(below), 1, pitch)
}

// ---- 唱片机 ----

// tryUseJukebox 右键唱片机：持碟播放 / 空手取碟。
func (g *Game) tryUseJukebox(p *entity.Player, u packet.UseItemOn) bool {
	if g.blockNames[g.w(p).GetBlock(u.X, u.Y, u.Z)] != "jukebox" {
		return false
	}
	pos := [4]int32{p.Dim, u.X, u.Y, u.Z}
	held := p.Held()
	if held.ID != 0 && strings.HasPrefix(g.itemNameOf(p, held.ID), "music_disc_") {
		// 放碟
		disc := g.itemNameOf(p, held.ID)
		discID777 := g.itemID777(disc)
		g.jukeboxes[pos] = discID777
		if !g.isCreative(p) {
			h := &p.Slots[36+p.HeldSlot]
			h.Count--
			if h.Count <= 0 {
				*h = entity.Item{}
			}
			g.syncInventory(p)
		}
		for _, v := range g.chunkViewers(p.Dim, u.X>>4, u.Z>>4) {
			x, y, z := u.X, u.Y, u.Z
			data := g.stateFor(v, 0) // 唱片音效走声音名（此处 data 不用）
			_ = data
			v.Send.SendPlay("minecraft:sound", func(w *protocol.Writer) {
				w.VarInt(g.soundDiscID(v, disc))
				w.VarInt(SoundRecord)
				w.Int32(x)
				w.Int32(y)
				w.Int32(z)
				w.Float(4) // 大音量可闻范围
				w.Float(1)
				w.Int64(0)
			})
		}
		g.blockSound(p.Dim, float64(u.X), float64(u.Y), float64(u.Z), "block.jukebox.use")
		return true
	}
	// 取碟
	if disc, ok := g.jukeboxes[pos]; ok {
		delete(g.jukeboxes, pos)
		g.giveItem(p, entity.Item{ID: g.itemFor(p, disc), Count: 1})
		for _, v := range g.chunkViewers(p.Dim, u.X>>4, u.Z>>4) {
			x, y, z := u.X, u.Y, u.Z
			v.Send.SendPlay("minecraft:level_event", func(w *protocol.Writer) {
				packet.WriteLevelEvent(w, 1011, x, y, z, 0, false)
			})
		}
		g.blockSound(p.Dim, float64(u.X), float64(u.Y), float64(u.Z), "block.jukebox.eject")
	}
	return true
}

// soundDiscID 唱片名 → 观察者协议音效 ID（music_disc.X）。
func (g *Game) soundDiscID(v *entity.Player, disc string) int32 {
	if m := g.soundIDs[g.playerProto(v)]; m != nil {
		return m["music_disc."+strings.TrimPrefix(disc, "music_disc_")]
	}
	return 0
}

// ---- 钓鱼 ----

// fishingBobber 一个浮标。
type fishingBobber struct {
	eid     int32
	uuid    protocol.UUID
	owner   int32
	dim     int32
	x, y, z float64
	vx, vy  float64
	vz      float64
	inWater bool
	age     int
	biteCd  int32 // 到下一次咬钩的倒计时（0 = 咬钩中）
}

// tryCastRod use_item 手持钓鱼竿：无浮标 → 抛竿；有 → 收线。
func (g *Game) tryCastRod(p *entity.Player) bool {
	if g.itemNameOf(p, p.Held().ID) != "fishing_rod" {
		return false
	}
	// 收线
	if b := g.fishing[p.EntityID]; b != nil {
		delete(g.fishing, p.EntityID)
		g.removeBobber(b)
		g.damageHeld(p)
		// 咬钩中 → 得鱼
		if b.biteCd < 0 {
			g.catchFish(p)
		}
		return true
	}
	// 抛竿
	yaw := float64(p.Yaw) * math.Pi / 180
	pitch := float64(p.Pitch) * math.Pi / 180
	dx := -math.Sin(yaw) * math.Cos(pitch)
	dy := -math.Sin(pitch)
	dz := math.Cos(yaw) * math.Cos(pitch)
	eid := g.nextEID.Add(1)
	b := &fishingBobber{eid: eid, uuid: entityUUID(eid), owner: p.EntityID, dim: p.Dim,
		x: p.X + dx, y: p.Y + 1.4, z: p.Z + dz,
		vx: dx * 1.1, vy: dy*1.1 + 0.25, vz: dz * 1.1}
	g.fishing[p.EntityID] = b
	for _, v := range g.chunkViewers(p.Dim, int32(b.x)>>4, int32(b.z)>>4) {
		typ := g.entityTypeID(v, "fishing_bobber")
		eid2, xx, yy, zz := eid, b.x, b.y, b.z
		v.Send.SendPlay("minecraft:add_entity", func(w *protocol.Writer) {
			packet.SpawnEntity{EntityID: eid2, UUID: b.uuid, Type: typ,
				X: xx, Y: yy, Z: zz, ObjectData: p.EntityID}.Write(w)
		})
	}
	g.playSoundTo(p, SoundPlayer, "entity.fishing_bobber.throw", 1, 1)
	return true
}

// catchFish 收线得鱼：鱼获 + 经验 + 音效。
func (g *Game) catchFish(p *entity.Player) {
	r := int64(p.X*1000) ^ int64(p.Z*7919) ^ int64(g.tick)
	if r < 0 {
		r = -r
	}
	name := "raw_cod"
	switch {
	case r%100 < 50:
		name = "raw_cod"
	case r%100 < 70:
		name = "raw_salmon"
	case r%100 < 80:
		name = "pufferfish"
	case r%100 < 85:
		name = "tropical_fish"
	case r%100 < 95: // 垃圾 10%
		junk := []string{"stick", "string", "bowl", "leather", "rotten_flesh"}
		name = junk[r%int64(len(junk))]
	default: // 宝物 5%
		treasure := []string{"saddle", "name_tag", "lily_pad", "enchanted_book"}
		name = treasure[r%int64(len(treasure))]
	}
	g.giveItem(p, entity.Item{ID: g.itemFor(p, g.itemID777(name)), Count: 1})
	g.giveXP(p, 1+int32(r%6))
	g.playSoundTo(p, SoundPlayer, "entity.player.levelup", 0.4, 1.2)
}

// tickFishing 浮标物理：重力、落水漂浮、咬钩循环（到位 1-4 秒后咬钩 1 秒）。
func (g *Game) tickFishing() {
	for _, b := range g.fishing {
		b.age++
		st := g.wd(b.dim).GetBlock(floorI(b.x), floorI(b.y), floorI(b.z))
		inWater := st >= 89 && st <= 104
		if !b.inWater {
			b.vy -= 0.03
			nx, ny, nz := b.x+b.vx, b.y+b.vy, b.z+b.vz
			if g.blockAtDim(b.dim, floorI(nx), floorI(ny-0.05), floorI(nz)) || inWater {
				if inWater {
					b.inWater = true
					b.vx, b.vy, b.vz = 0, 0, 0
					b.biteCd = 20 + int32((b.x*1000))%60 // 1-4 秒
					if b.biteCd < 0 {
						b.biteCd = -b.biteCd
					}
					g.playSoundAt(b.dim, b.x, b.y, b.z, SoundPlayer, "entity.fishing_bobber.splash", 1, 1)
				} else {
					b.x, b.y, b.z = nx, float64(floorI(ny-0.05)+1), nz
					b.vx, b.vy, b.vz = 0, 0, 0
					continue // 落地：等待收回
				}
			} else {
				b.x, b.y, b.z = nx, ny, nz
			}
		} else {
			if !inWater { // 水退了
				b.inWater = false
				continue
			}
			if b.biteCd > 0 {
				b.biteCd--
				if b.biteCd == 0 {
					b.biteCd = -1 // 咬钩窗口
					b.vy = -0.25  // 下沉
					if p := g.players[b.owner]; p != nil {
						g.playSoundTo(p, SoundPlayer, "entity.fishing_bobber.bite", 1, 1)
					}
				}
			}
		}
	}
}

func (g *Game) removeBobber(b *fishingBobber) {
	eid := b.eid
	for _, p := range g.players {
		if p.Dim != b.dim {
			continue
		}
		p.Send.SendPlay("minecraft:remove_entities", func(w *protocol.Writer) {
			packet.WriteRemoveEntities(w, []int32{eid})
		})
	}
}

var _ = protocol.Position{}
