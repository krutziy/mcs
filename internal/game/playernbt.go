package game

// 玩家档案 NBT（vanilla playerdata 兼容）：gzip NBT 文件（根名空串，
// 字符串长度为无符号短整型——与网络 NBT 的 VarInt 不同）。
// 字段：Pos/Motion/Rotation/Inventory(含 components)/Health/foodLevel/
// Xp*/Spawn*/Dimension/Air/Fire 等；槽位映射 vanilla(热0-8 主9-35 甲36-39
// 副40) ↔ 线上(主9-35 甲5-8 热36-44 副45)。
// config world.player_format:"nbt" 启用（默认 json；读取时 NBT 存在则优先）。

import (
	"bytes"
	"compress/gzip"
	"encoding/binary"
	"io"
	"math"
	"os"
	"strings"

	"mcserver/internal/entity"
)

// ---- NBT 文件格式编解码（本文件专用小实现） ----

const (
	tEnd byte = 0
	tByte byte = 1
	tShort byte = 2
	tInt byte = 3
	tLong byte = 4
	tFloat byte = 5
	tDouble byte = 6
	tByteArray byte = 7
	tString byte = 8
	tList byte = 9
	tCompound byte = 10
)

type nbtFW struct{ buf bytes.Buffer }

func (w *nbtFW) b(v byte)      { w.buf.WriteByte(v) }
func (w *nbtFW) i16(v int16)   { binary.Write(&w.buf, binary.BigEndian, v) }
func (w *nbtFW) i32(v int32)   { binary.Write(&w.buf, binary.BigEndian, v) }
func (w *nbtFW) i64(v int64)   { binary.Write(&w.buf, binary.BigEndian, v) }
func (w *nbtFW) f32(v float32) { binary.Write(&w.buf, binary.BigEndian, v) }
func (w *nbtFW) f64(v float64) { binary.Write(&w.buf, binary.BigEndian, v) }
func (w *nbtFW) str(s string)  { w.i16(int16(len(s))); w.buf.WriteString(s) }

type nbtFR struct {
	buf []byte
	off int
	err bool
}

func (r *nbtFR) b() byte {
	if r.off >= len(r.buf) {
		r.err = true
		return 0
	}
	v := r.buf[r.off]
	r.off++
	return v
}
func (r *nbtFR) i16() int16  { return int16(binary.BigEndian.Uint16([]byte{r.b(), r.b()})) }
func (r *nbtFR) i32() int32  { v := int32(0); for i := 0; i < 4; i++ { v = v<<8 | int32(r.b()) }; return v }
func (r *nbtFR) i64() int64  { v := int64(0); for i := 0; i < 8; i++ { v = v<<8 | int64(r.b()) }; return v }
func (r *nbtFR) str() string { n := int(r.i16()); if r.err || n < 0 || r.off+n > len(r.buf) { r.err = true; return "" }; s := string(r.buf[r.off : r.off+n]); r.off += n; return s }

func (r *nbtFR) payload(id byte) any {
	switch id {
	case tByte:
		return int8(r.b())
	case tShort:
		return r.i16()
	case tInt:
		return r.i32()
	case tLong:
		return r.i64()
	case tFloat:
		return math.Float32frombits(uint32(r.i32()))
	case tDouble:
		return math.Float64frombits(uint64(r.i64()))
	case tString:
		return r.str()
	case tList:
		elem := r.b()
		n := int(r.i32())
		out := make([]any, 0, max(n, 0))
		for i := 0; i < n && !r.err; i++ {
			out = append(out, r.payload(elem))
		}
		return out
	case tCompound:
		c := map[string]any{}
		for {
			tid := r.b()
			if r.err || tid == tEnd {
				return c
			}
			name := r.str()
			if r.err {
				return c
			}
			c[name] = r.payload(tid)
		}
	}
	r.err = true
	return nil
}

func nbtFileDecode(data []byte) map[string]any {
	r := &nbtFR{buf: data}
	if r.b() != tCompound {
		return nil
	}
	r.str() // 根名
	c, _ := r.payload(tCompound).(map[string]any)
	return c
}

func nbtFileEncode(root map[string]any) []byte {
	// 直接手写根：类型 + 空名 + 复合载荷
	var buf bytes.Buffer
	w := &nbtFW{}
	_ = buf
	w.b(tCompound)
	w.i16(0) // 空根名
	encodeCompoundOrdered(w, root)
	return w.buf.Bytes()
}

// encodeCompoundOrdered 递归写复合载荷（键值成对：类型 + 键名 + 载荷）。
func encodeCompoundOrdered(w *nbtFW, c map[string]any) {
	for k, v := range c {
		switch t := v.(type) {
		case int8:
			w.b(tByte)
			w.str(k)
			w.b(byte(t))
		case int16:
			w.b(tShort)
			w.str(k)
			w.i16(t)
		case int32:
			w.b(tInt)
			w.str(k)
			w.i32(t)
		case int64:
			w.b(tLong)
			w.str(k)
			w.i64(t)
		case float32:
			w.b(tFloat)
			w.str(k)
			w.f32(t)
		case float64:
			w.b(tDouble)
			w.str(k)
			w.f64(t)
		case string:
			w.b(tString)
			w.str(k)
			w.str(t)
		case []any:
			w.b(tList)
			w.str(k)
			if len(t) == 0 {
				w.b(tEnd)
				w.i32(0)
				continue
			}
			var elem byte
			switch t[0].(type) {
			case float64:
				elem = tDouble
			case float32:
				elem = tFloat
			case string:
				elem = tString
			case map[string]any:
				elem = tCompound
			case int64:
				elem = tLong
			case int32:
				elem = tInt
			}
			w.b(elem)
			w.i32(int32(len(t)))
			for _, e := range t {
				switch elem {
				case tDouble:
					w.f64(e.(float64))
				case tFloat:
					w.f32(e.(float32))
				case tString:
					w.str(e.(string))
				case tInt:
					w.i32(e.(int32))
				case tLong:
					w.i64(e.(int64))
				case tCompound:
					encodeCompoundOrdered(w, e.(map[string]any))
				}
			}
		case map[string]any:
			w.b(tCompound)
			w.str(k)
			encodeCompoundOrdered(w, t)
		}
	}
	w.b(tEnd)
}

// ---- 玩家档案读写 ----

// vanillaSlot 线上槽位 → vanilla 槽位。
func vanillaSlot(wire int32) (int32, bool) {
	switch {
	case wire >= 36 && wire <= 44: // 快捷栏 → 0-8
		return wire - 36, true
	case wire >= 9 && wire <= 35: // 主背包同名
		return wire, true
	case wire >= 5 && wire <= 8: // 盔甲 头/胸/腿/靴 → 39/38/37/36
		return 44 - wire, true
	case wire == 45: // 副手 → 40
		return 40, true
	}
	return 0, false
}

// wireSlot vanilla 槽位 → 线上槽位。
func wireSlot(v int32) int32 {
	switch {
	case v >= 0 && v <= 8:
		return 36 + v
	case v >= 9 && v <= 35:
		return v
	case v >= 36 && v <= 39:
		return 44 - v
	case v == 40:
		return 45
	}
	return -1
}

// playerNBT 玩家状态 → vanilla NBT 复合。
func (g *Game) playerNBT(p *entity.Player) map[string]any {
	var inv []any
	for i, s := range p.Slots {
		if s.ID == 0 {
			continue
		}
		vs, ok := vanillaSlot(int32(i))
		if !ok {
			continue
		}
		e := map[string]any{
			"Slot":  int8(vs),
			"id":    "minecraft:" + g.itemNameOf777(g.canonItem(p, s.ID)),
			"count": int32(s.Count),
		}
		var comps map[string]any
		if s.Damage > 0 {
			comps = map[string]any{"minecraft:damage": s.Damage}
		}
		if len(s.Ench) > 0 {
			levels := map[string]any{}
			for _, e2 := range s.Ench {
				levels["minecraft:"+e2.Name] = int32(e2.Level)
			}
			if comps == nil {
				comps = map[string]any{}
			}
			comps["minecraft:enchantments"] = map[string]any{"levels": levels}
		}
		if comps != nil {
			e["components"] = comps
		}
		inv = append(inv, e)
	}
	if inv == nil {
		inv = []any{}
	}
	spawnDim := "minecraft:overworld"
	return map[string]any{
		"Pos":        []any{p.X, p.Y, p.Z},
		"Motion":     []any{float64(0), float64(0), float64(0)},
		"Rotation":   []any{p.Yaw, p.Pitch},
		"Health":     p.Health,
		"foodLevel":  int32(p.Food),
		"foodSaturationLevel": p.Saturation,
		"foodExhaustionLevel": float32(p.Exhaustion),
		"Air":        int32(p.Air),
		"Fire":       int16(p.FireTicks),
		"OnGround":   int8(0),
		"fallDistance": float32(0),
		"XpLevel":    p.Level,
		"XpTotal":    p.TotalXP,
		"XpP":        float32(p.XPPending) / float32(xpToNext(p.Level)),
		"XpSeed":     int32(0),
		"Inventory":  inv,
		"EnderItems": []any{},
		"SelectedItemSlot": p.HeldSlot,
		"playerGameType":   p.GameMode,
		"Dimension":   spawnDim,
		"SpawnX":      int32(p.SpawnPos[0]),
		"SpawnY":      int32(p.SpawnPos[1]),
		"SpawnZ":      int32(p.SpawnPos[2]),
		"SpawnAngle":  float32(0),
		"abilities":   map[string]any{"invulnerable": int8(0), "mayfly": int8(0), "flying": int8(0)},
	}
}

// savePlayerNBTFile gzip 写出 <dir>/players/<name>.dat。
func (g *Game) savePlayerNBTFile(p *entity.Player, dir string) error {
	if err := os.MkdirAll(dir+"/players", 0o755); err != nil {
		return err
	}
	raw := nbtFileEncode(g.playerNBT(p))
	var gz bytes.Buffer
	zw := gzip.NewWriter(&gz)
	if _, err := zw.Write(raw); err != nil {
		return err
	}
	zw.Close()
	return os.WriteFile(dir+"/players/"+p.Name+".dat", gz.Bytes(), 0o644)
}

// applyPlayerNBT 从 NBT 复合恢复玩家状态。
func (g *Game) applyPlayerNBT(p *entity.Player, c map[string]any) {
	if l, ok := c["Pos"].([]any); ok && len(l) == 3 {
		if x, ok := l[0].(float64); ok {
			if y, ok := l[1].(float64); ok {
				if z, ok := l[2].(float64); ok {
					p.X, p.Y, p.Z = x, y, z
				}
			}
		}
	}
	if l, ok := c["Rotation"].([]any); ok && len(l) == 2 {
		if y, ok := l[0].(float32); ok {
			if pt, ok := l[1].(float32); ok {
				p.Yaw, p.Pitch = y, pt
			}
		}
	}
	if v, ok := c["Health"].(float32); ok {
		p.Health = v
	}
	if v, ok := c["foodLevel"].(int32); ok {
		p.Food = v
	}
	if v, ok := c["foodSaturationLevel"].(float32); ok {
		p.Saturation = v
	}
	if v, ok := c["Air"].(int32); ok {
		p.Air = v
	}
	if v, ok := c["Fire"].(int16); ok {
		p.FireTicks = int32(v)
	}
	if v, ok := c["XpLevel"].(int32); ok {
		p.Level = v
	}
	if v, ok := c["XpTotal"].(int32); ok {
		p.TotalXP = v
	}
	if v, ok := c["XpP"].(float32); ok {
		p.XPPending = int32(v * float32(xpToNext(p.Level)))
	}
	if v, ok := c["SelectedItemSlot"].(int32); ok && v >= 0 && v <= 8 {
		p.HeldSlot = v
	}
	if v, ok := c["playerGameType"].(int32); ok {
		p.GameMode = v
	}
	if inv, ok := c["Inventory"].([]any); ok {
		for _, e := range inv {
			m, ok := e.(map[string]any)
			if !ok {
				continue
			}
			slotByte, _ := m["Slot"].(int8)
			idStr, _ := m["id"].(string)
			count, _ := m["count"].(int32)
			if count <= 0 {
				continue
			}
			name := strings.TrimPrefix(idStr, "minecraft:")
			id777 := g.itemID777(name)
			if id777 == 0 {
				continue
			}
			ws := wireSlot(int32(slotByte))
			if ws < 0 || ws >= int32(len(p.Slots)) {
				continue
			}
			it := entity.Item{ID: g.itemFor(p, id777), Count: count}
			if comps, ok := m["components"].(map[string]any); ok {
				if d, ok := comps["minecraft:damage"].(int32); ok && d > 0 {
					it.Damage = d
				}
				if ench, ok := comps["minecraft:enchantments"].(map[string]any); ok {
					if levels, ok := ench["levels"].(map[string]any); ok {
						for en, lv := range levels {
							if l2, ok := lv.(int32); ok && l2 > 0 {
								it.Ench = append(it.Ench, entity.EnchEntry{
									Name: strings.TrimPrefix(en, "minecraft:"), Level: l2})
							}
						}
					}
				}
			}
			p.Slots[ws] = it
		}
	}
	sx, sokx := c["SpawnX"].(int32)
	sy, soky := c["SpawnY"].(int32)
	sz, sokz := c["SpawnZ"].(int32)
	if sokx && soky && sokz && (sx != 0 || sy != 0 || sz != 0) {
		p.SpawnPos = [3]float64{float64(sx), float64(sy), float64(sz)}
	}
}

// loadPlayerNBTFile 读取并应用（无文件返回 false）。
func (g *Game) loadPlayerNBTFile(p *entity.Player, dir string) bool {
	f, err := os.Open(dir + "/players/" + p.Name + ".dat")
	if err != nil {
		return false
	}
	defer f.Close()
	zr, err := gzip.NewReader(f)
	if err != nil {
		return false
	}
	raw, err := io.ReadAll(zr)
	if err != nil {
		return false
	}
	c := nbtFileDecode(raw)
	if c == nil {
		return false
	}
	g.applyPlayerNBT(p, c)
	return true
}
