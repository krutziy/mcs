// Package world 实现区块构建与世界生成。
//
// 26.1 区块包（level_chunk_with_light）结构——经官方抓包逐字节破解：
//
//	body := X(i32) Z(i32)
//	  + 高度图: VarInt 条目数 × { VarInt 类型, VarInt long数, i64[] }
//	    （256 列、9 位/值（值=表面y−min_y）、每 long 7 值不跨界、LSB-first）
//	  + 区块数据: VarInt 字节数 +
//	      24 个节: { i16 非空计数, 生物群系容器, 方块容器 }
//	        （26.x 把生物群系容器放在方块容器之前！）
//	  + 方块实体: VarInt 数（此处恒 0）
//	  + 光照: 天空/方块 掩码与空掩码 bitset + 天空光照数组 + 方块光照数组 + 末尾 0x00
//
// 容器（paletted container）: bits=0 → 单值(VarInt)；1..8 位 → 调色板间接编码；
// 每种维度的数据 long 数 = ceil(条目数 / (64/bits))，值不跨 long、LSB-first。
package world

import (
	"mcserver/internal/assets"
	"mcserver/internal/protocol"
)

// 世界维度参数（overworld：-64..320）。
const (
	MinY        = -64
	WorldHeight = 384
	NumSections = WorldHeight / 16
	SectionVol  = 4096
	BiomeVol    = 64
)

// 方块状态 ID（基准 777 / 26.3 空间，来自官方 --reports blocks.json；
// 旧版本协议的状态 ID 有偏移，多版本状态翻译是后续工作）。
const (
	StateAir        uint32 = 0
	StateStone      uint32 = 1
	StateGrassBlock uint32 = 8  // snowy=true（官方平坦预设同款）
	StateDirt       uint32 = 10
	StateBedrock    uint32 = 88
	StateWater      uint32 = 89 // level=0（静止水源）
	StateSand       uint32 = 121
	StateGravel     uint32 = 127
)

// ChunkStyle 描述协议版本间的区块编码差异（均经官方抓包验证）。
type ChunkStyle struct {
	HeightmapOrder [3]int32 // 高度图类型顺序；1.21.x: [1,5,4]，26.x: [4,5,1]
	BiomeID        int32    // 平原生物群系注册表 ID；1.21.x: 40，26.x: 0
	LegacyEmpty    bool     // true=1.21.x 规范空节(6字节,方块在前)；false=26.x 官方 8 字节模式
	// StateMap 把世界内部的基准(777)状态 ID 翻译到该协议版本（nil = 777 无需翻译）
	StateMap map[uint32]uint32
}

// StyleFor 返回协议版本对应的区块编码风格。
func StyleFor(proto int32) ChunkStyle {
	if proto <= 774 { // 1.21.11 及更早
		return ChunkStyle{HeightmapOrder: [3]int32{1, 5, 4}, BiomeID: 40, LegacyEmpty: true,
			StateMap: assets.StateMap(proto)}
	}
	return ChunkStyle{HeightmapOrder: [3]int32{4, 5, 1}, BiomeID: 0,
		StateMap: assets.StateMap(proto)}
}

// tr 把基准状态 ID 翻译到目标协议（无映射时原样返回）。
func (s ChunkStyle) tr(state uint32) uint32 {
	if s.StateMap == nil {
		return state
	}
	if to, ok := s.StateMap[state]; ok {
		return to
	}
	return state
}

// Section 是一个 16×16×16 的方块存储，索引 = y<<8 | z<<4 | x（相对节内坐标）。
type Section struct {
	Blocks [SectionVol]uint32
	NonAir int16
}

// SetBlock 设置相对世界坐标（y 为相对 min_y 的值）的方块。
func (c *Chunk) SetBlock(x, y, z int32, state uint32) {
	if y < 0 || y >= WorldHeight || x < 0 || x >= 16 || z < 0 || z >= 16 {
		return
	}
	s := c.section(y >> 4)
	idx := (y&0xF)<<8 | z<<4 | x
	old := s.Blocks[idx]
	if old == state {
		return
	}
	if old == StateAir && state != StateAir {
		s.NonAir++
	} else if old != StateAir && state == StateAir {
		s.NonAir--
	}
	s.Blocks[idx] = state
}

// GetBlock 读取方块状态。
func (c *Chunk) GetBlock(x, y, z int32) uint32 {
	if y < 0 || y >= WorldHeight {
		return StateAir
	}
	s := c.sections[y>>4]
	if s == nil {
		return StateAir
	}
	return s.Blocks[(y&0xF)<<8|z<<4|x]
}

func (c *Chunk) section(i int32) *Section {
	if c.sections[i] == nil {
		c.sections[i] = &Section{}
	}
	return c.sections[i]
}

// Chunk 是一个 16×16 列。
type Chunk struct {
	CX, CZ   int32
	sections [NumSections]*Section
	Biome    int32 // 整块统一生物群系（注册表 ID）
}

// NewChunk 创建空区块。
func NewChunk(cx, cz, biome int32) *Chunk { return &Chunk{CX: cx, CZ: cz, Biome: biome} }

// isOpaque 报告方块是否阻断天空光。
func isOpaque(s uint32) bool { return s != StateAir && s != StateWater }

// isSolid 报告方块是否算 motion-blocking 高度（与不透明一致即可）。
func isSolid(s uint32) bool { return s != StateAir }

// EncodeBody 以指定协议风格编码 level_chunk_with_light 包体（不含坐标前缀）。
func (c *Chunk) EncodeBody(style ChunkStyle) []byte {
	w := protocol.NewWriter()
	c.writeHeightmaps(w, style)
	c.writeSections(w, style)
	w.VarInt(0) // 方块实体数
	c.writeLight(w)
	return w.Bytes()
}

// heightOf 计算某列某类高度图的值：最高满足条件的方块之上的一格（相对 min_y）。
func (c *Chunk) heightOf(x, z int32, pred func(uint32) bool) int32 {
	for y := int32(WorldHeight - 1); y >= 0; y-- {
		if pred(c.GetBlock(x, y, z)) {
			return y + 1
		}
	}
	return 0
}

// writeHeightmaps 写入 3 张高度图：4=motion_blocking 5=无叶 1=world_surface。
func (c *Chunk) writeHeightmaps(w *protocol.Writer, style ChunkStyle) {
	types := style.HeightmapOrder[:]
	preds := []func(uint32) bool{isOpaque, isOpaque, isSolid}
	w.VarInt(int32(len(types)))
	for i, t := range types {
		vals := make([]uint32, 256)
		for z := int32(0); z < 16; z++ {
			for x := int32(0); x < 16; x++ {
				vals[z*16+x] = uint32(c.heightOf(x, z, preds[i]))
			}
		}
		longs := packValues(vals, 9)
		w.VarInt(t)
		w.VarInt(int32(len(longs)))
		for _, L := range longs {
			w.Int64(int64(L))
		}
	}
}

// writeSections 写入区块数据（先写进中间缓冲再按长度前缀输出）。
func (c *Chunk) writeSections(w *protocol.Writer, style ChunkStyle) {
	buf := protocol.NewWriter()
	for i := int32(0); i < NumSections; i++ {
		s := c.sections[i]
		if s == nil {
			if style.LegacyEmpty {
				// 1.21.x 规范空节：blockCount 0 + 单值空气 + 单值生物群系（方块在前）
				buf.Int16(0)
				writeSingleContainer(buf, 0)
				writeSingleContainer(buf, style.BiomeID)
			} else {
				// 26.x 官方空节固定 8 字节模式（语义未明，经抓包验证客户端接受）
				buf.FixedBytes([]byte{0x00, 0x28, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00})
			}
			continue
		}
		buf.Int16(s.NonAir)
		if style.LegacyEmpty {
			// 1.21.x：方块容器在前，生物群系在后
			writeBlockContainer(buf, &s.Blocks, style)
			writeSingleContainer(buf, style.BiomeID)
		} else {
			// 26.x：生物群系容器在前
			writeSingleContainer(buf, style.BiomeID)
			writeBlockContainer(buf, &s.Blocks, style)
		}
	}
	body := buf.Bytes()
	if !style.LegacyEmpty {
		// 26.x 官方在末尾还有 2 字节（00 28，语义未明）
		body = append(body, 0x00, 0x28)
	}
	w.VarInt(int32(len(body)))
	w.RawBytes(body)
}

// writeBlockContainer 编码方块调色板容器（4096 项，状态 ID 经版本翻译）。
func writeBlockContainer(w *protocol.Writer, blocks *[SectionVol]uint32, style ChunkStyle) {
	palette, lookup := buildPalette(blocks)
	switch len(palette) {
	case 1:
		writeSingleContainer(w, int32(style.tr(palette[0])))
		return
	case 0:
		writeSingleContainer(w, 0)
		return
	}
	bits := paletteBits(len(palette))
	w.VarInt(int32(bits))
	w.VarInt(int32(len(palette)))
	for _, p := range palette {
		w.VarInt(int32(style.tr(p)))
	}
	indices := make([]uint32, SectionVol)
	for i, b := range blocks {
		indices[i] = lookup[b]
	}
	for _, L := range packValues(indices, bits) {
		w.Int64(int64(L))
	}
}

// paletteBits 返回间接调色板需要的位数（vanilla 最小 4 位）。
func paletteBits(n int) uint32 {
	bits := uint32(4)
	for 1<<bits < n {
		bits++
	}
	return bits
}

// buildPalette 收集区块节中的全部状态并建立索引。
func buildPalette(blocks *[SectionVol]uint32) ([]uint32, map[uint32]uint32) {
	lookup := map[uint32]uint32{}
	palette := []uint32{}
	for _, b := range blocks {
		if _, ok := lookup[b]; !ok {
			lookup[b] = uint32(len(palette))
			palette = append(palette, b)
		}
	}
	return palette, lookup
}

// writeSingleContainer 写入单值容器。
func writeSingleContainer(w *protocol.Writer, value int32) {
	w.VarInt(0) // bits = 0
	w.VarInt(value)
}

// writeLight 写入光照段：真实天光传播。
//   - 每列从顶向下：非不透明处光 15 直射，遇不透明截断
//   - 从全亮格 BFS 横向/向下扩散（每步 -1），不透明格不透光但自身接受光照
//   - 掩码按节生成；无变化的节标记进空掩码（全 0）
//   - 方块光暂无光源，全空
func (c *Chunk) writeLight(w *protocol.Writer) {
	sky := c.computeSkyLight()

	skyData := make([]bool, NumSections)
	skyEmpty := make([]bool, NumSections)
	blockEmpty := make([]bool, NumSections)
	arrays := make([][]byte, NumSections)
	for sec := int32(0); sec < NumSections; sec++ {
		nonEmpty := false
		allMax := true
		arr := make([]byte, 2048)
		for y := int32(0); y < 16; y++ {
			for z := int32(0); z < 16; z++ {
				for x := int32(0); x < 16; x++ {
					lv := sky[sec*4096+y*256+z*16+x]
					if lv > 0 {
						nonEmpty = true
					}
					if lv < 15 {
						allMax = false
					}
					if lv > 0 {
						idx := (y*256 + z*16 + x) / 2
						if (y*256+z*16+x)%2 == 0 {
							arr[idx] |= byte(lv)
						} else {
							arr[idx] |= byte(lv) << 4
						}
					}
				}
			}
		}
		if nonEmpty && !allMax {
			skyData[sec] = true
			arrays[sec] = arr
		} else if allMax {
			skyEmpty[sec] = false // 全 15 也需下发？官方把全亮节标空省流量——但语义是"无光照数据=保持默认 0"，故全亮节必须发
			skyData[sec] = true
			arrays[sec] = bytesOf(0xFF, 2048)
		} else {
			skyEmpty[sec] = true // 全 0
		}
		blockEmpty[sec] = true
	}

	writeBitset(w, skyData)
	w.VarInt(0) // 方块光数据掩码
	writeBitset(w, skyEmpty)
	writeBitset(w, blockEmpty)
	n := int32(0)
	for _, has := range skyData {
		if has {
			n++
		}
	}
	w.VarInt(n)
	for sec := int32(0); sec < NumSections; sec++ {
		if skyData[sec] {
			w.ByteArray(arrays[sec])
		}
	}
	w.VarInt(0) // 方块光数组数量
}

// computeSkyLight 计算区块天光（0..15，索引 sec*4096 + y*256 + z*16 + x）。
// 列直射 + BFS 扩散；不含跨区块光（邻接暗边在邻块计算时由客户端插值，可接受）。
func (c *Chunk) computeSkyLight() []byte {
	light := make([]byte, NumSections*4096)
	type pos struct{ x, y, z int32 }
	queue := make([]pos, 0, 4096)

	// 列直射：从每列顶部向下，直到不透明
	for x := int32(0); x < 16; x++ {
		for z := int32(0); z < 16; z++ {
			for y := int32(WorldHeight - 1); y >= 0; y-- {
				if isOpaque(c.GetBlock(x, y, z)) {
					break
				}
				idx := y/16*4096 + (y%16)*256 + z*16 + x
				light[idx] = 15
				queue = append(queue, pos{x, y, z})
			}
		}
	}
	// BFS 扩散（6 向，每步 -1；不透明格不发出光）
	for head := 0; head < len(queue); head++ {
		p := queue[head]
		lv := light[p.y/16*4096+(p.y%16)*256+p.z*16+p.x]
		if lv <= 1 {
			continue
		}
		for _, d := range [6][3]int32{{1, 0, 0}, {-1, 0, 0}, {0, 1, 0}, {0, -1, 0}, {0, 0, 1}, {0, 0, -1}} {
			nx, ny, nz := p.x+d[0], p.y+d[1], p.z+d[2]
			if nx < 0 || nx > 15 || nz < 0 || nz > 15 || ny < 0 || ny >= WorldHeight {
				continue
			}
			if isOpaque(c.GetBlock(nx, ny, nz)) {
				continue
			}
			idx := ny/16*4096 + (ny%16)*256 + nz*16 + nx
			if light[idx] < lv-1 {
				light[idx] = lv - 1
				if lv-1 > 1 {
					queue = append(queue, pos{nx, ny, nz})
				}
			}
		}
	}
	return light
}

func clampI(v, lo, hi int32) int32 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func bytesOf(v byte, n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = v
	}
	return b
}

// packValues 把值数组按 LSB-first 打包为 long 数组，值不跨 long。
func packValues(vals []uint32, bits uint32) []uint64 {
	perLong := 64 / bits
	n := (len(vals) + int(perLong) - 1) / int(perLong)
	mask := uint64(1)<<bits - 1
	out := make([]uint64, n)
	for i, v := range vals {
		slot := uint32(i) / perLong
		off := (uint32(i) % perLong) * bits
		out[slot] |= (uint64(v) & mask) << off
	}
	return out
}


// writeBitset 写入 long 数组形式的位图。
func writeBitset(w *protocol.Writer, flags []bool) {
	n := (len(flags) + 63) / 64
	w.VarInt(int32(n))
	for i := 0; i < n; i++ {
		var L uint64
		for j := 0; j < 64; j++ {
			k := i*64 + j
			if k < len(flags) && flags[k] {
				L |= 1 << j
			}
		}
		w.Int64(int64(L))
	}
}
