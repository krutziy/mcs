package packet

import "mcserver/internal/protocol"

// ---- 生命值 / 伤害 / 重生编解码（字段格式见公开协议文档，26.3 / 777） ----

// WriteSetHealth 写入 clientbound set_health：Float 生命 + VarInt 食物 + Float 饱和。
func WriteSetHealth(w *protocol.Writer, health float32, food int32, saturation float32) {
	w.Float(health)
	w.VarInt(food)
	w.Float(saturation)
}

// WriteDamageEvent 写入 clientbound damage_event（无位置源的简化形式）：
// VarInt 实体 + VarInt 伤害源类型 + VarInt 触发实体(-1 无) + VarInt 直接触发(-1)。
// 位置仅在源类型带方块时需要（摔落/饥饿等无）。
func WriteDamageEvent(w *protocol.Writer, entityID, sourceType, cause, directCause int32) {
	w.VarInt(entityID)
	w.VarInt(sourceType)
	w.VarInt(cause)
	w.VarInt(directCause)
}

// WriteEntityEvent 写入 clientbound entity_event：Int32 实体 + Int8 事件
// （2=受击 3=死亡）。老式动画通道，用于广播给其他玩家。
func WriteEntityEvent(w *protocol.Writer, entityID int32, event int8) {
	w.Int32(entityID)
	w.Int8(event)
}

// WriteHurtAnimation 写入 clientbound hurt_animation：VarInt 实体 + Float 朝向。
func WriteHurtAnimation(w *protocol.Writer, entityID int32, yaw float32) {
	w.VarInt(entityID)
	w.Float(yaw)
}

// Respawn 是 clientbound respawn（与 login 的 SpawnInfo 同构）。
type Respawn struct {
	DimensionID      int32
	WorldName        string
	HashedSeed       int64
	GameMode         GameMode
	PreviousGameMode int32
	IsDebug          bool
	IsFlat           bool
	PortalCooldown   int32
	SeaLevel         int32
	DataKept         bool // 保留死亡时数据（死亡不掉落暂未实现，发 false）
	// TailApplies/ExtraTail: 26.3(777) 起 respawn 末尾多出的布尔（与 login 同款）
	TailApplies bool
	ExtraTail   bool
}

// WriteRespawn 写入 respawn 包。
func WriteRespawn(w *protocol.Writer, r Respawn) {
	w.VarInt(r.DimensionID)
	w.String(r.WorldName)
	w.Int64(r.HashedSeed)
	w.VarInt(int32(r.GameMode))
	w.VarInt(r.PreviousGameMode)
	w.Bool(r.IsDebug)
	w.Bool(r.IsFlat)
	w.Bool(false) // Has Death Location
	w.VarInt(r.PortalCooldown)
	if r.TailApplies { // 26.3：与 login 同款插入字段（未抓包验证，按同构处理）
		w.VarInt(0)
	}
	w.VarInt(r.SeaLevel)
	w.Bool(r.DataKept)
	if r.TailApplies {
		w.Bool(r.ExtraTail)
	}
}
