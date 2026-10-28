package world

// Anvil 存档格式读写：与官方服务端 region 文件（r.X.Z.mca）互操作。
// 布局（经官方 world 目录抓样逐字节确认）：
//   region/r.X.Z.mca = 4KB 头（1024×位置表 + 1024×时间戳）+ 4KB 扇区数据
//   每区块 = [长度 i32 | 压缩类型 u8 | zlib(NBT)]，长度含类型字节
//   NBT 根 → "" → { Status, xPos, zPos, yPos, sections[], Heightmaps, ... }
//   section = { Y i8, block_states{palette[], data i64[]}, biomes{palette[]} }
//   palette 项 = "minecraft:stone"（含属性时 "[k=v,...]" 规范名）
// 我们写入：全 sections（palette=运行时状态集合 → 规范名），yPos=-4，
//   Heightmaps 由 c.heightOf 重算；读取：palette 规范名 → 777 状态 ID。

import (
	"bytes"
	"compress/gzip"
	"compress/zlib"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// NBT 写入器（Anvil 用的大端无压缩根格式）。
type nbtWriter struct {
	buf bytes.Buffer
}

func (n *nbtWriter) byteAt(b byte)     { n.buf.WriteByte(b) }
func (n *nbtWriter) i16(v int16)       { binary.Write(&n.buf, binary.BigEndian, v) }
func (n *nbtWriter) i32(v int32)       { binary.Write(&n.buf, binary.BigEndian, v) }
func (n *nbtWriter) i64(v int64)       { binary.Write(&n.buf, binary.BigEndian, v) }
func (n *nbtWriter) str(s string)      { n.i16(int16(len(s))); n.buf.WriteString(s) }
func (n *nbtWriter) name(t byte, s string) { n.byteAt(t); n.str(s) }

// 匿名 Compound 开/闭（TAG_Compound 0x0a）
func (n *nbtWriter) compound(name string) { n.name(0x0a, name) }
func (n *nbtWriter) end()                 { n.byteAt(0) }

// WriteNBTChunk 编码区块为 Anvil NBT（根 compound 匿名）。
// canonical：777 状态 → 规范名（由 assets 提供的映射）。
func (c *Chunk) WriteNBTChunk(canonical map[uint32]string) []byte {
	n := &nbtWriter{}
	n.compound("") // 根（匿名）
	// Status
	n.name(0x08, "Status"); n.str("minecraft:full")
	// xPos/zPos
	n.name(0x03, "xPos"); n.i32(c.CX)
	n.name(0x03, "zPos"); n.i32(c.CZ)
	n.name(0x03, "yPos"); n.i32(-4)
	n.name(0x04, "LastUpdate"); n.i64(time.Now().UnixMilli())
	n.name(0x04, "InhabitedTime"); n.i64(0)
	// block_entities
	n.name(0x09, "block_entities")
	n.byteAt(10); n.i32(0) // 空列表（类型占位 TAG_Compound）
	// Heightmaps（全 0 的 37 long，外部工具兼容即可）
	for _, hm := range []string{"MOTION_BLOCKING", "WORLD_SURFACE", "OCEAN_FLOOR", "MOTION_BLOCKING_NO_LEAVES"} {
		n.name(0x0c, hm); n.i32(37)
		for i := 0; i < 37; i++ { n.i64(0) }
	}
	// sections
	n.name(0x09, "sections")
	n.byteAt(10); n.i32(NumSections)
	for i := int32(0); i < NumSections; i++ {
		sec := c.sections[i]
		// TAG_List 元素不带 tag/name 前缀，直接写字段
		n.name(0x01, "Y"); n.byteAt(byte(int8(i - 4)))
		if sec == nil {
			// 空节：单空气 palette、无 data（官方对全空气节可省 data）
			n.compound("block_states")
			n.name(0x09, "palette"); n.byteAt(8); n.i32(1); n.str("minecraft:air")
			n.end()
		} else {
			palette, indices := buildAnvilPalette(&sec.Blocks, canonical)
			n.compound("block_states")
			n.name(0x09, "palette"); n.byteAt(8); n.i32(int32(len(palette)))
			for _, p := range palette { n.str(p) }
			bits := anvilBits(len(palette))
			if len(palette) > 1 {
				longs := packValues(indices, bits)
				n.name(0x0c, "data"); n.i32(int32(len(longs))) // TAG_Long_Array
				for _, L := range longs { n.i64(int64(L)) }
			}
			n.end()
		}
		// biomes
		n.compound("biomes")
		n.name(0x09, "palette"); n.byteAt(8); n.i32(1); n.str("minecraft:plains")
		n.end()
		n.end() // 元素 compound 的结束符（TAG_List 元素无前缀但仍以 0x00 收尾）
	}
	n.end() // sections
	n.end() // 根
	return n.buf.Bytes()
}

// buildAnvilPalette 运行时节 → palette 规范名 + 索引。
func buildAnvilPalette(blocks *[SectionVol]uint32, canonical map[uint32]string) ([]string, []uint32) {
	order := []uint32{}
	idx := map[uint32]uint32{}
	for _, b := range blocks {
		if _, ok := idx[b]; !ok {
			idx[b] = uint32(len(order))
			order = append(order, b)
		}
	}
	palette := make([]string, len(order))
	for i, s := range order {
		p := canonical[s]
		if p == "" {
			p = "minecraft:air"
		}
		palette[i] = p
	}
	indices := make([]uint32, SectionVol)
	for i, b := range blocks {
		indices[i] = idx[b]
	}
	return palette, indices
}

// anvilBits palette 间接位宽（官方 4..8，>256 直连 15）。
func anvilBits(n int) uint32 {
	if n <= 16 { return 4 }
	if n <= 32 { return 5 }
	if n <= 64 { return 6 }
	if n <= 128 { return 7 }
	if n <= 256 { return 8 }
	return 15
}

// ReadNBTChunk 解析 Anvil NBT 回区块（sections → 777 状态）。
// inverse：规范名 → 777 状态。返回解析出的 CX/CZ。
func ReadNBTChunk(data []byte, inverse map[string]uint32) (cx, cz int32, ch *Chunk) {
	p := &nbtParser{buf: data, defaults: defaultsOf(inverse)}
	ch = NewChunk(0, 0, 0)
	if t := p.tag(); t != 0x0a { return 0, 0, nil }
	p.name() // 根名（空）
	for {
		t := p.tag()
		if t == 0 { break }
		nm := p.name()
		switch nm {
		case "xPos":
			cx = p.i32()
		case "zPos":
			cz = p.i32()
		case "sections":
			p.skipListHeader() // TAG_List: elementTag=0x0a count
			secCount := p.listCount // 局部保存（parseSection 内嵌套列表会覆盖共享状态）
			for i := int32(0); i < secCount; i++ {
				p.parseSection(ch, inverse)
			}
		default:
			p.skipValue(t)
		}
	}
	ch.CX, ch.CZ = cx, cz
	// NonAir 重算
	for i := int32(0); i < NumSections; i++ {
		s := ch.sections[i]
		if s == nil { continue }
		na := int16(0)
		for _, b := range s.Blocks {
			if b != StateAir { na++ }
		}
		s.NonAir = na
	}
	return cx, cz, ch
}

// parseSection 读单个 section compound。
func (p *nbtParser) parseSection(ch *Chunk, inverse map[string]uint32) {
	var y int32
	var palette []uint32
	var data []uint64
	for {
		t := p.tag()
		if t == 0 { break }
		nm := p.name()
		switch nm {
		case "Y":
			y = int32(int8(p.i8()))
		case "block_states":
			for {
				st := p.tag()
				if st == 0 { break }
				sn := p.name()
				switch sn {
				case "palette":
					p.skipListHeader()
					for i := int32(0); i < p.listCount; i++ {
						name := p.str()
						id := inverse[name]
						if id == 0 && p.defaults != nil {
							// 无属性名回退该方块默认状态（官方 flat palette 用基础名）
							base := name
							if i := strings.IndexByte(base, '['); i >= 0 {
								base = base[:i]
							}
							id = p.defaults[base]
						}
						palette = append(palette, id)
					}
				case "data": // TAG_Long_Array(12)：[count:4][long×count]
					n := p.i32()
					for i := int32(0); i < n; i++ {
						data = append(data, uint64(p.i64()))
					}
				default:
					p.skipValue(st)
				}
			}
		default:
			p.skipValue(t)
		}
	}
	if y < -4 || y >= NumSections-4 { return }
	si := y + 4
	if len(palette) == 0 { return }
	sec := &Section{}
	if len(palette) == 1 || len(data) == 0 {
		v := palette[0]
		if v != StateAir {
			for i := range sec.Blocks { sec.Blocks[i] = v }
		}
	} else {
		bits := anvilBits(len(palette))
		per := 64 / bits
		for i := 0; i < SectionVol; i++ {
			L := data[i/int(per)]
			off := uint32(i%int(per)) * bits
			idx := uint32(L>>off) & ((1<<bits)-1)
			if int(idx) < len(palette) {
				sec.Blocks[i] = palette[idx]
			}
		}
	}
	ch.sections[si] = sec
}

// ---- NBT 解析器 ----
type nbtParser struct {
	buf       []byte
	off       int
	listCount int32
	defaults  map[string]uint32 // 无属性方块名 → 默认状态（palette 回退）
}

func (p *nbtParser) tag() byte {
	if p.off >= len(p.buf) {
		return 0 // 越界视作 TAG_End，安全终止
	}
	b := p.buf[p.off]
	p.off++
	return b
}
func (p *nbtParser) i8() byte {
	if p.off >= len(p.buf) {
		return 0
	}
	b := p.buf[p.off]
	p.off++
	return b
}
func (p *nbtParser) i32() int32 {
	if p.off+4 > len(p.buf) {
		return 0
	}
	v := int32(binary.BigEndian.Uint32(p.buf[p.off:]))
	p.off += 4
	return v
}
func (p *nbtParser) i64() int64 {
	if p.off+8 > len(p.buf) {
		return 0
	}
	v := int64(binary.BigEndian.Uint64(p.buf[p.off:]))
	p.off += 8
	return v
}
func (p *nbtParser) str() string {
	if p.off+2 > len(p.buf) {
		return ""
	}
	l := int(binary.BigEndian.Uint16(p.buf[p.off:]))
	p.off += 2
	if p.off+l > len(p.buf) {
		return ""
	}
	s := string(p.buf[p.off : p.off+l])
	p.off += l
	return s
}
func (p *nbtParser) name() string { return p.str() }

// skipListHeader TAG_List 头。
func (p *nbtParser) skipListHeader() {
	p.tag()               // 元素类型
	p.listCount = p.i32()
}

// skipValue 跳过任意 NBT 值（含容器递归）。
func (p *nbtParser) skipValue(t byte) {
	switch t {
	case 1: p.off++
	case 2: p.off += 2
	case 3: p.off += 4
	case 4: p.off += 8
	case 5: p.off += 4
	case 6: p.off += 8
	case 7: // TAG_Byte_Array：长度单位是字节
		n := p.i32(); p.off += int(n)
	case 11: // TAG_Int_Array
		n := p.i32(); p.off += int(n) * 4
	case 12: // TAG_Long_Array
		n := p.i32(); p.off += int(n) * 8
	case 8:
		p.str()
	case 9:
		et := p.tag(); n := p.i32()
		for i := int32(0); i < n; i++ {
			if et == 0 { break }
			p.skipValue(et)
		}
	case 10:
		for {
			st := p.tag()
			if st == 0 { return }
			p.name()
			p.skipValue(st)
		}
	}
}

// ---- Region 文件 ----

const sectorSize = 4096

// SaveRegionAnvil 把一组区块写入 r.X.Z.mca（Anvil 格式）。
func SaveRegionAnvil(path string, chunks []*Chunk, canonical map[uint32]string) error {
	return SaveRegionAnvilBE(path, chunks, canonical, nil)
}

// SaveRegionAnvilBE 带方块实体的 region 写入。
func SaveRegionAnvilBE(path string, chunks []*Chunk, canonical map[uint32]string, bes []BlockEntity) error {
	// 排序保证确定性
	sort.Slice(chunks, func(i, j int) bool {
		a, b := chunks[i], chunks[j]
		lz := localIdx(a.CX, a.CZ)
		return lz < localIdx(b.CX, b.CZ)
	})
	// 排序保证确定性
	sort.Slice(chunks, func(i, j int) bool {
		a, b := chunks[i], chunks[j]
		lz := localIdx(a.CX, a.CZ)
		return lz < localIdx(b.CX, b.CZ)
	})
	var body bytes.Buffer
	locations := make([]byte, 4096)
	stamps := make([]byte, 4096)
	sector := uint32(2) // 头两扇区
	for _, ch := range chunks {
		nbt := ch.WriteNBTChunkWithBE(canonical, bes)
		var z bytes.Buffer
		zw := zlib.NewWriter(&z)
		zw.Write(nbt)
		zw.Close()
		payload := z.Bytes()
		need := uint32(5+len(payload)+sectorSize-1) / sectorSize
		lenn := make([]byte, 4)
		binary.BigEndian.PutUint32(lenn, uint32(len(payload)+1))
		body.Write(lenn)
		body.WriteByte(2) // zlib
		body.Write(payload)
		for body.Len()%int(sectorSize) != 0 {
			body.WriteByte(0)
		}
		idx := localIdx(ch.CX, ch.CZ)
		binary.BigEndian.PutUint32(locations[idx*4:], (sector<<8)|need)
		binary.BigEndian.PutUint32(stamps[idx*4:], uint32(time.Now().Unix()))
		sector += need
	}
	f, err := os.Create(path)
	if err != nil { return err }
	defer f.Close()
	f.Write(locations)
	f.Write(stamps)
	f.Write(body.Bytes())
	return nil
}

// localIdx 区块在 region 中的槽位（32×32）。
func localIdx(cx, cz int32) int32 {
	lx := ((cx % 32) + 32) % 32
	lz := ((cz % 32) + 32) % 32
	return lz*32 + lx
}

// LoadRegionAnvil 读取 region 全部区块。
func LoadRegionAnvil(path string, inverse map[string]uint32) ([]*Chunk, error) {
	d, err := os.ReadFile(path)
	if err != nil { return nil, err }
	var out []*Chunk
	for idx := int32(0); idx < 1024; idx++ {
		loc := binary.BigEndian.Uint32(d[idx*4:])
		if loc == 0 { continue }
		off := int64(loc>>8) * sectorSize
		cnt := int64(loc&0xFF) * sectorSize
		if off+cnt > int64(len(d)) { continue }
		sec := d[off : off+cnt]
		ln := int64(binary.BigEndian.Uint32(sec[0:4])) - 1
		if ln <= 0 || ln+5 > cnt { continue }
		var raw []byte
		switch sec[4] {
		case 2:
			zr, err := zlib.NewReader(bytes.NewReader(sec[5 : 5+ln]))
			if err != nil { continue }
			buf := &bytes.Buffer{}
			if _, err := buf.ReadFrom(zr); err != nil { continue }
			raw = buf.Bytes()
		case 1:
			gr, err := gzip.NewReader(bytes.NewReader(sec[5 : 5+ln]))
			if err != nil { continue }
			buf := &bytes.Buffer{}
			if _, err := buf.ReadFrom(gr); err != nil { continue }
			raw = buf.Bytes()
		default:
			continue
		}
		if _, _, ch := ReadNBTChunk(raw, inverse); ch != nil {
			out = append(out, ch)
		}
	}
	return out, nil
}

// RegionPath 由区块坐标推 region 文件路径。
func RegionPath(dir string, cx, cz int32) string {
	return filepath.Join(dir, fmt.Sprintf("r.%d.%d.mca", floorDiv(cx, 32), floorDiv(cz, 32)))
}

func floorDiv(a, b int32) int32 {
	q := a / b
	if (a%b != 0) && ((a < 0) != (b < 0)) { q-- }
	return q
}

// SaveAnvil 全部脏区块按 region 分组写 Anvil（合并旧 region 内容）。
func (m *Manager) SaveAnvil(dir string, canonical map[uint32]string, inverse map[string]uint32) (int, error) {
	return m.SaveAnvilBE(dir, canonical, inverse, nil)
}

// SaveAnvilBE 带方块实体的 Anvil 存档。
func (m *Manager) SaveAnvilBE(dir string, canonical map[uint32]string, inverse map[string]uint32, bes []BlockEntity) (int, error) {
	rd := filepath.Join(dir, "region")
	if err := os.MkdirAll(rd, 0o755); err != nil { return 0, err }
	byRegion := map[[2]int32][]*Chunk{}
	dirtyKeys := map[[2]int32]bool{}
	for key, ch := range m.chunks {
		if !m.dirty[key] { continue }
		rk := [2]int32{floorDiv(key[0], 32), floorDiv(key[1], 32)}
		byRegion[rk] = append(byRegion[rk], ch)
		dirtyKeys[key] = true
	}
	n := 0
	for rk, list := range byRegion {
		path := filepath.Join(rd, fmt.Sprintf("r.%d.%d.mca", rk[0], rk[1]))
		if old, err := LoadRegionAnvil(path, inverse); err == nil {
			for _, oc := range old {
				dup := false
				for _, nc := range list {
					if nc.CX == oc.CX && nc.CZ == oc.CZ { dup = true; break }
				}
				if !dup { list = append(list, oc) }
			}
		}
		if err := SaveRegionAnvilBE(path, list, canonical, bes); err != nil {
			return n, err
		}
	}
	for key := range dirtyKeys {
		delete(m.dirty, key)
	}
	return len(dirtyKeys), nil
}

// LoadAnvil 从 Anvil 目录读全部 region。
func (m *Manager) LoadAnvil(dir string, inverse map[string]uint32) (int, error) {
	rd := filepath.Join(dir, "region")
	entries, err := os.ReadDir(rd)
	if err != nil {
		if os.IsNotExist(err) { return 0, nil }
		return 0, err
	}
	n := 0
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".mca") { continue }
		chunks, err := LoadRegionAnvil(filepath.Join(rd, e.Name()), inverse)
		if err != nil { continue }
		for _, ch := range chunks {
			m.chunks[[2]int32{ch.CX, ch.CZ}] = ch
			n++
		}
	}
	return n, nil
}

// defaultsOf 从 inverse 推基础名→默认态（各基础名的最小状态 ID 近似；
// 官方 flat palette 用基础名，此回退已足够；正式路径可注入 BlockDefaults）。
func defaultsOf(inverse map[string]uint32) map[string]uint32 {
	m := map[string]uint32{}
	for full, id := range inverse {
		base := full
		if i := strings.IndexByte(base, '['); i >= 0 {
			base = base[:i]
		}
		if old, ok := m[base]; !ok || id < old {
			m[base] = id
		}
	}
	return m
}

// ---- 方块实体 + level.dat ----

// BlockEntityData 由 game 层提供的方块实体导出（位置 → NBT 体字段）。
// 接口约定：返回 {id, x, y, z, Items[]} 形式的中间结构。
type BEItem struct {
	ID     int32
	Count  int32
	Damage int32
	Slot   int32
}

type BlockEntity struct {
	X, Y, Z int32
	ID      string // minecraft:chest / minecraft:furnace / minecraft:brewing_stand
	Items   []BEItem
	Extra   map[string]int64 // 熔炉进度等（BurnTime/CookTime）
}

// SetBlockEntityExporter 注册导出器（game 层把箱/熔炉/酿造台状态交给存档层）。
type BEExporter func() []BlockEntity

// WriteNBTChunkWithBE 带方块实体的区块编码。
func (c *Chunk) WriteNBTChunkWithBE(canonical map[uint32]string, bes []BlockEntity) []byte {
	n := &nbtWriter{}
	n.compound("")
	n.name(0x08, "Status"); n.str("minecraft:full")
	n.name(0x03, "xPos"); n.i32(c.CX)
	n.name(0x03, "zPos"); n.i32(c.CZ)
	n.name(0x03, "yPos"); n.i32(-4)
	n.name(0x04, "LastUpdate"); n.i64(time.Now().UnixMilli())
	n.name(0x04, "InhabitedTime"); n.i64(0)
	// block_entities（本区块内的）
	mine := 0
	for _, be := range bes {
		if be.X>>4 == c.CX && be.Z>>4 == c.CZ {
			mine++
		}
	}
	n.name(0x09, "block_entities")
	n.byteAt(10); n.i32(int32(mine))
	for _, be := range bes {
		if be.X>>4 != c.CX || be.Z>>4 != c.CZ {
			continue
		}
		// TAG_List 元素：无前缀，字段 + 0x00
		n.name(0x08, "id"); n.str(be.ID)
		n.name(0x03, "x"); n.i32(be.X)
		n.name(0x03, "y"); n.i32(be.Y)
		n.name(0x03, "z"); n.i32(be.Z)
		n.name(0x09, "Items")
		n.byteAt(10); n.i32(int32(len(be.Items)))
		for _, it := range be.Items {
			n.name(0x08, "id"); n.str(itemNameForBE(it.ID))
			n.name(0x01, "Count"); n.byteAt(byte(it.Count))
			n.name(0x03, "Slot"); n.i32(it.Slot)
			if it.Damage > 0 {
				n.name(0x03, "Damage"); n.i32(it.Damage)
			}
			n.end()
		}
		for k, v := range be.Extra {
			n.name(0x03, k); n.i32(int32(v))
		}
		n.end()
	}
	for _, hm := range []string{"MOTION_BLOCKING", "WORLD_SURFACE", "OCEAN_FLOOR", "MOTION_BLOCKING_NO_LEAVES"} {
		n.name(0x0c, hm); n.i32(37)
		for i := 0; i < 37; i++ { n.i64(0) }
	}
	n.name(0x09, "sections")
	n.byteAt(10); n.i32(NumSections)
	for i := int32(0); i < NumSections; i++ {
		sec := c.sections[i]
		n.name(0x01, "Y"); n.byteAt(byte(int8(i - 4)))
		if sec == nil {
			n.compound("block_states")
			n.name(0x09, "palette"); n.byteAt(8); n.i32(1); n.str("minecraft:air")
			n.end()
		} else {
			palette, indices := buildAnvilPalette(&sec.Blocks, canonical)
			n.compound("block_states")
			n.name(0x09, "palette"); n.byteAt(8); n.i32(int32(len(palette)))
			for _, p := range palette { n.str(p) }
			bits := anvilBits(len(palette))
			if len(palette) > 1 {
				longs := packValues(indices, bits)
				n.name(0x0c, "data"); n.i32(int32(len(longs)))
				for _, L := range longs { n.i64(int64(L)) }
			}
			n.end()
		}
		n.compound("biomes")
		n.name(0x09, "palette"); n.byteAt(8); n.i32(1); n.str("minecraft:plains")
		n.end()
		n.end()
	}
	n.end()
	n.end()
	return n.buf.Bytes()
}

// beItemNames 777 物品 ID → 规范名（存档层惰性注入）。
var beItemNames map[int32]string

// SetBEItemNames 注入物品名表（assets 层调用）。
func SetBEItemNames(m map[int32]string) { beItemNames = m }

func itemNameForBE(id int32) string {
	if n := beItemNames[id]; n != "" {
		return n
	}
	return "minecraft:stone"
}

// WriteLevelDat 生成官方 level.dat（gzip NBT，最小字段集）。
func WriteLevelDat(spawnX, spawnY, spawnZ int32, seed int64, time int64) []byte {
	n := &nbtWriter{}
	n.compound("")
	n.compound("Data")
	n.name(0x08, "LevelName"); n.str("mcserver")
	n.name(0x03, "SpawnX"); n.i32(spawnX)
	n.name(0x03, "SpawnY"); n.i32(spawnY)
	n.name(0x03, "SpawnZ"); n.i32(spawnZ)
	n.name(0x04, "RandomSeed"); n.i64(seed)
	n.name(0x04, "Time"); n.i64(time)
	n.name(0x04, "DayTime"); n.i64(time)
	n.name(0x01, "hardcore"); n.byteAt(0)
	n.name(0x01, "MapFeatures"); n.byteAt(1)
	n.name(0x03, "version"); n.i32(19133) // Anvil 版本号
	n.name(0x08, "Status"); n.str("minecraft:full")
	n.end()
	n.end()
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	gw.Write(n.buf.Bytes())
	gw.Close()
	return buf.Bytes()
}
