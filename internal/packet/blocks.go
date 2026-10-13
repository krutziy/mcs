package packet

import "mcserver/internal/protocol"

// ---- 方块交互相关编解码（26.3 / 协议 777，经官方抓包与公开协议文档确认） ----

// PlayerActionStatus 是 player_action 包的状态枚举。
const (
	DigStart      int32 = 0 // 开始挖掘（创造模式即时破坏）
	DigCancel     int32 = 1 // 取消挖掘
	DigFinish     int32 = 2 // 完成挖掘
	DropStack     int32 = 3 // 丢弃整组物品
	DropItem      int32 = 4 // 丢弃单个物品
	ReleaseUse    int32 = 5 // 结束使用物品（吃/喝/拉弓）
	SwapHands     int32 = 6 // 交换主副手
)

// PlayerAction 是 serverbound player_action（挖掘等动作）。
// Face 为字节：0=底 1=顶 2=北(-z) 3=南(+z) 4=西(-x) 5=东(+x)。
type PlayerAction struct {
	Status   int32
	X, Y, Z  int32
	Face     int32
	Sequence int32
}

// ReadPlayerAction 解析 player_action：VarInt 状态 + 位置 + 字节面 + VarInt 序列号。
func ReadPlayerAction(r *protocol.Reader) PlayerAction {
	a := PlayerAction{Status: r.VarInt()}
	p := r.Position()
	a.X, a.Y, a.Z = p.X, p.Y, p.Z
	a.Face = int32(r.Int8())
	a.Sequence = r.VarInt()
	return a
}

// UseItemOn 是 serverbound use_item_on（对方块使用物品 / 放置）。
// Face 为 VarInt，取值同 PlayerAction.Face；Cursor 是点击处的相对坐标。
type UseItemOn struct {
	Hand        int32
	X, Y, Z     int32
	Face        int32
	CursorX     float32
	CursorY     float32
	CursorZ     float32
	InsideBlock bool
	WorldBorder bool
	Sequence    int32
}

// ReadUseItemOn 解析 use_item_on：VarInt 手 + 位置 + VarInt 面 + 3×光标
// + 布尔 insideBlock + 布尔 worldBorder + VarInt 序列号。
func ReadUseItemOn(r *protocol.Reader) UseItemOn {
	u := UseItemOn{Hand: r.VarInt()}
	p := r.Position()
	u.X, u.Y, u.Z = p.X, p.Y, p.Z
	u.Face = r.VarInt()
	u.CursorX, u.CursorY, u.CursorZ = r.Float(), r.Float(), r.Float()
	u.InsideBlock = r.Bool()
	u.WorldBorder = r.Bool()
	u.Sequence = r.VarInt()
	return u
}

// WriteBlockUpdate 写入 clientbound block_update：位置 + VarInt 方块状态。
func WriteBlockUpdate(w *protocol.Writer, x, y, z int32, state uint32) {
	w.Position(protocol.Position{X: x, Y: y, Z: z})
	w.VarInt(int32(state))
}

// SectionEntry 是 section_blocks_update 中的一个方块变更（节内相对坐标）。
type SectionEntry struct {
	LocalX, LocalY, LocalZ int32
	State                  uint32
}

// WriteSectionBlocksUpdate 写入 clientbound section_blocks_update：
// 节坐标 long（x 占高 22 位、y 低 20 位、z 中 22 位）+ VarInt 数量 +
// VarLong 数组（状态 << 12 | lx<<8 | lz<<4 | ly）。
func WriteSectionBlocksUpdate(w *protocol.Writer, secX, secY, secZ int32, entries []SectionEntry) {
	secPos := (uint64(uint32(secX))&0x3F_FFFF)<<42 |
		(uint64(uint32(secZ))&0x3F_FFFF)<<20 |
		uint64(uint32(secY))&0xF_FFFF
	w.Int64(int64(secPos))
	w.VarInt(int32(len(entries)))
	for _, e := range entries {
		v := uint64(e.State&0xFFFFF)<<12 |
			uint64(e.LocalX&0xF)<<8 | uint64(e.LocalZ&0xF)<<4 | uint64(e.LocalY&0xF)
		w.VarLong(int64(v))
	}
}

// WriteBlockChangedAck 写入 clientbound block_changed_ack：VarInt 变更序列号。
func WriteBlockChangedAck(w *protocol.Writer, sequence int32) {
	w.VarInt(sequence)
}

// WriteLevelEvent 写入 clientbound level_event：事件 ID + 位置 + 附加数据 + 音量标志。
// 方块破坏粒子/音效用事件 2001（data = 被破坏方块的 ID）。
func WriteLevelEvent(w *protocol.Writer, event int32, x, y, z int32, data int32, disableRelVolume bool) {
	w.Int32(event)
	w.Position(protocol.Position{X: x, Y: y, Z: z})
	w.Int32(data)
	w.Bool(disableRelVolume)
}

// CreativeSlot 是 serverbound set_creative_mode_slot 解析结果。
// 物品只取 ID 与数量；带自定义组件的物品无法解析 ID 时 IsBlock 为 false。
type CreativeSlot struct {
	Slot   int32  // 36..44 = 快捷栏
	ItemID int32  // 物品网络 ID
	Count  int32
	HasItem bool // false = 清空该槽（含 -999 丢弃）
}

// ReadSetCarriedItem 解析 set_carried_item：Short 槽位（0..8）。
func ReadSetCarriedItem(r *protocol.Reader) int32 { return int32(r.Int16()) }

// ReadCreativeSlot 解析 set_creative_mode_slot：Short 槽位 + 可选物品。
// 物品字段（1.20.5+ Slot 格式）：present 布尔 + VarInt 物品 ID + VarInt 数量 +
// 组件增补列表 + 组件移除列表；带增补组件的物品此处不解析（返回 HasItem=false）。
func ReadCreativeSlot(r *protocol.Reader) CreativeSlot {
	cs := CreativeSlot{Slot: int32(r.Int16())}
	if !r.Bool() {
		return cs
	}
	cs.ItemID = r.VarInt()
	cs.Count = r.VarInt()
	cs.HasItem = true
	if r.VarInt() != 0 { // 增补组件数：非空则放弃解析（置空）
		cs.HasItem, cs.ItemID, cs.Count = false, 0, 0
		return cs
	}
	n := int(r.VarInt()) // 移除组件数：逐个跳过 VarInt
	for i := 0; i < n; i++ {
		_ = r.VarInt()
	}
	return cs
}

// WriteCreativeSlotItem 构造一个无组件物品的 Slot 字段（recorder 模拟创造选物品用）。
func WriteCreativeSlotItem(w *protocol.Writer, itemID, count int32) {
	w.Bool(true)
	w.VarInt(itemID)
	w.VarInt(count)
	w.VarInt(0) // 增补组件数
	w.VarInt(0) // 移除组件数
}

// FaceOffset 返回面法线方向偏移（0=底 1=顶 2=北 3=南 4=西 5=东）。
func FaceOffset(face int32) (dx, dy, dz int32) {
	switch face {
	case 0:
		return 0, -1, 0
	case 1:
		return 0, 1, 0
	case 2:
		return 0, 0, -1
	case 3:
		return 0, 0, 1
	case 4:
		return -1, 0, 0
	default: // 5
		return 1, 0, 0
	}
}
