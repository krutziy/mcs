package packet

import "mcserver/internal/protocol"

// ---- 物品栏与物品实体编解码（26.3，槽位布局经官方 container_set_content 抓包确认：
//      0-4 合成 5-8 盔甲 9-35 背包 36-44 快捷栏 45 副手，共 46 槽） ----

// InvSlots 是玩家物品栏（窗口 0）的槽位数。
const InvSlots = 46

// ItemSlot 是物品槽（ID 0 = 空）。
type ItemSlot struct {
	ID    int32
	Count int32
}

// WriteEmptySlot 写入空 Slot 字段。
func WriteEmptySlot(w *protocol.Writer) {
	w.Bool(false)
}

// WriteSlot 写入一个无自定义组件的物品 Slot 字段。
func WriteSlot(w *protocol.Writer, itemID, count int32) {
	w.Bool(true)
	w.VarInt(itemID)
	w.VarInt(count)
	w.VarInt(0) // 增补组件数
	w.VarInt(0) // 移除组件数
}

// WriteContainerSetContent 写入 clientbound container_set_content：
// VarInt 窗口 + VarInt 状态号 + 槽数组 + 光标物品。
func WriteContainerSetContent(w *protocol.Writer, window, state int32, slots []ItemSlot, carried ItemSlot) {
	w.VarInt(window)
	w.VarInt(state)
	w.VarInt(int32(len(slots)))
	for _, s := range slots {
		if s.ID == 0 {
			WriteEmptySlot(w)
		} else {
			WriteSlot(w, s.ID, s.Count)
		}
	}
	if carried.ID == 0 {
		WriteEmptySlot(w)
	} else {
		WriteSlot(w, carried.ID, carried.Count)
	}
}

// ContainerClick 是 serverbound container_click 的服务端关心的字段。
// 26.x 里变更槽位列表与光标是 Hashed Slot（内容哈希），服务端权威模型下忽略，
// 处理后回 container_set_content 全量同步即可。
type ContainerClick struct {
	Window int32
	State  int32
	Slot   int16
	Button int8
	Mode   int32
}

// ReadContainerClick 解析 container_click 的前 5 个字段（其余忽略）。
func ReadContainerClick(r *protocol.Reader) ContainerClick {
	return ContainerClick{
		Window: r.VarInt(),
		State:  r.VarInt(),
		Slot:   r.Int16(),
		Button: r.Int8(),
		Mode:   r.VarInt(),
	}
}

// WriteSetEntityData 写入 clientbound set_entity_data（元数据由调用者构造）。
func WriteSetEntityData(w *protocol.Writer, entityID int32, metadata []byte) {
	w.VarInt(entityID)
	w.RawBytes(metadata)
}

// BuildItemEntityMetadata 构造物品实体的元数据：index 8 = Slot（类型 7）+ 终止符。
func BuildItemEntityMetadata(itemID, count int32) []byte {
	w := protocol.NewWriter()
	w.Uint8(8) // index
	w.VarInt(7)
	WriteSlot(w, itemID, count)
	w.Uint8(0xFF) // 元数据终止
	return w.Bytes()
}

// WriteTakeItemEntity 写入 clientbound take_item_entity：被拾取实体 + 拾取者 + 数量。
func WriteTakeItemEntity(w *protocol.Writer, collected, collector, count int32) {
	w.VarInt(collected)
	w.VarInt(collector)
	w.VarInt(count)
}

// 容器点击模式（serverbound container_click Mode 字段）。
const (
	ClickModeClick      int32 = 0 // 普通（左/右由 Button 区分）
	ClickModeShift      int32 = 1 // shift 移动
	ClickModeSwap       int32 = 2 // 数字键/副手交换
	ClickModeClone      int32 = 3 // 中键（创造取物）
	ClickModeThrow      int32 = 4 // 丢弃（Q / 点击窗外）
	ClickModeQuickCraft int32 = 5 // 拖拽
	ClickModeDouble     int32 = 6 // 双击收集
)

// WriteOpenScreen 写入 clientbound open_screen：VarInt 容器类型（menu 注册表）+ 标题文本组件。
func WriteOpenScreen(w *protocol.Writer, menuType int32, title string) {
	w.VarInt(menuType)
	protocol.WriteNBT(w, protocol.Compound{"text": title})
}

// WriteContainerClose 写入 clientbound container_close：VarInt 窗口。
func WriteContainerClose(w *protocol.Writer, window int32) {
	w.VarInt(window)
}

// WriteContainerSetData 写入 clientbound container_set_data：
// VarInt 窗口 + Short 属性 + Short 值（熔炉火/进度条）。
func WriteContainerSetData(w *protocol.Writer, window int32, property int16, value int16) {
	w.VarInt(window)
	w.Int16(property)
	w.Int16(value)
}
