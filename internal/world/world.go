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

// 26.1 方块状态 ID（来自官方 --reports blocks.json，见 README 更新方法）。
const (
	StateAir        uint32 = 0
	StateStone      uint32 = 1
	StateGrassBlock uint32 = 8  // snowy=false
	StateDirt       uint32 = 10
	StateBedrock    uint32 = 85
	StateWater      uint32 = 86 // level=0（静止水源）
	StateSand       uint32 = 118
	StateGravel     uint32 = 124
)

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

// EncodeBody 编码 level_chunk_with_light 包体（不含坐标前缀）。
func (c *Chunk) EncodeBody() []byte {
	w := protocol.NewWriter()
	c.writeHeightmaps(w)
	c.writeSections(w)
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
func (c *Chunk) writeHeightmaps(w *protocol.Writer) {
	types := []int32{4, 5, 1}
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
func (c *Chunk) writeSections(w *protocol.Writer) {
	buf := protocol.NewWriter()
	for i := int32(0); i < NumSections; i++ {
		s := c.sections[i]
		if s == nil {
			// 官方空节固定 8 字节模式（语义未明，经抓包验证客户端接受）
			buf.FixedBytes([]byte{0x00, 0x28, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00})
			continue
		}
		buf.Int16(s.NonAir)
		writeSingleContainer(buf, c.Biome)
		writeBlockContainer(buf, &s.Blocks)
	}
	body := buf.Bytes()
	// 官方在末尾还有 2 字节（00 28，语义未明）
	body = append(body, 0x00, 0x28)
	w.VarInt(int32(len(body)))
	w.RawBytes(body)
}

// writeBlockContainer 编码方块调色板容器（4096 项）。
func writeBlockContainer(w *protocol.Writer, blocks *[SectionVol]uint32) {
	palette, lookup := buildPalette(blocks)
	switch len(palette) {
	case 1:
		writeSingleContainer(w, int32(palette[0]))
		return
	case 0:
		writeSingleContainer(w, 0)
		return
	}
	bits := paletteBits(len(palette))
	w.VarInt(int32(bits))
	w.VarInt(int32(len(palette)))
	for _, p := range palette {
		w.VarInt(int32(p))
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

// writeLight 写入光照段。复刻官方 26.1 平坦世界的下发模式：
//   - 天空光数据只发地表之上两节：topSec+1（前 1/4 为 0，官方如此）与 topSec+2（全亮）
//   - 地表节标记进"空天空光"掩码，更低的节不提及
//   - 方块光：地表以下三节标记为空，无数据数组
//   - 末尾一个 0x00 字节（语义未知，官方抓包存在）
func (c *Chunk) writeLight(w *protocol.Writer) {
	top := int32(0)
	for x := int32(0); x < 16; x++ {
		for z := int32(0); z < 16; z++ {
			if h := c.heightOf(x, z, isOpaque); h > top {
				top = h
			}
		}
	}
	topSec := clampI(top/16, 0, NumSections-3)

	skyData := make([]bool, NumSections)
	skyEmpty := make([]bool, NumSections)
	blockEmpty := make([]bool, NumSections)
	if topSec+1 < NumSections {
		skyData[topSec+1] = true
	}
	if topSec+2 < NumSections {
		skyData[topSec+2] = true
	}
	skyEmpty[topSec] = true
	for i := int32(0); i <= topSec+2 && i < NumSections; i++ {
		blockEmpty[i] = true
	}

	writeBitset(w, skyData)
	w.VarInt(0) // 方块光数据掩码：0 个 long（官方如此）
	writeBitset(w, skyEmpty)
	writeBitset(w, blockEmpty)

	skyArrayCount := int32(0)
	if topSec+1 < NumSections {
		skyArrayCount++
	}
	if topSec+2 < NumSections {
		skyArrayCount++
	}
	w.VarInt(skyArrayCount) // 天空光数组数量前缀
	if topSec+1 < NumSections {
		arr := make([]byte, 2048)
		for i := 512; i < 2048; i++ { // 前 512 字节 0，其余 0xFF（官方模式）
			arr[i] = 0xFF
		}
		w.ByteArray(arr)
	}
	if topSec+2 < NumSections {
		w.ByteArray(bytesOf(0xFF, 2048))
	}
	w.VarInt(0) // 方块光数组数量
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
