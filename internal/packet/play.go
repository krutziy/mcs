package packet

import (
	"mcserver/internal/protocol"
)

// ---- Play: 客户端 → 服务端 ----

// MovePlayer 是四种移动包的统一形式。
type MovePlayer struct {
	X, Y, Z    float64
	Yaw, Pitch float32
	OnGround   bool
	HasPos     bool // 是否携带坐标
	HasRot     bool // 是否携带朝向
}

// ReadMovePlayer 解析 move_player_pos / move_player_pos_rot / move_player_rot /
// move_player_status_only（四包同构，按标志位区分）。
func ReadMovePlayer(r *protocol.Reader, hasPos, hasRot bool) MovePlayer {
	m := MovePlayer{HasPos: hasPos, HasRot: hasRot}
	if hasPos {
		m.X, m.Y, m.Z = r.Double(), r.Double(), r.Double()
	}
	if hasRot {
		m.Yaw, m.Pitch = r.Float(), r.Float()
	}
	flags := r.Uint8()
	m.OnGround = flags&0x01 != 0
	return m
}

// ChatMessageIn 是客户端聊天包（离线模式下忽略签名部分）。
type ChatMessageIn struct {
	Content string
}

// ReadChatMessage 解析聊天包：消息 + 时间戳 + 盐 + 可选签名 + 偏移 + 3字节确认 + 校验和。
func ReadChatMessage(r *protocol.Reader) ChatMessageIn {
	msg := r.String()
	_ = r.Int64() // timestamp
	_ = r.Int64() // salt
	if r.Bool() { // 签名存在则丢弃
		_ = r.FixedBytes(256)
	}
	_ = r.VarInt() // offset
	_ = r.FixedBytes(3)
	_ = r.Int32() // checksum
	return ChatMessageIn{Content: msg}
}

// ---- Play: 服务端 → 客户端 ----

// GameMode 是游戏模式枚举。
type GameMode int32

const (
	ModeSurvival GameMode = iota
	ModeCreative
	ModeAdventure
	ModeSpectator
)

// JoinGame 是 26.1 的 login 包（进入游戏）。
type JoinGame struct {
	EntityID            int32
	Hardcore            bool
	WorldNames          []string
	MaxPlayers          int32
	ViewDistance        int32
	SimulationDistance  int32
	ReducedDebugInfo    bool
	EnableRespawnScreen bool
	DoLimitedCrafting   bool
	// SpawnInfo
	DimensionID      int32 // dimension_type 注册表条目 ID
	WorldName        string
	HashedSeed       int64
	GameMode         GameMode
	PreviousGameMode int32
	IsDebug          bool
	IsFlat           bool
	PortalCooldown   int32
	SeaLevel         int32
	EnforcesSecure   bool
}

// WriteJoinGame 写入 login 包。字段顺序经官方 26.1 抓包逐字节验证。
func WriteJoinGame(w *protocol.Writer, p JoinGame) {
	w.Int32(p.EntityID)
	w.Bool(p.Hardcore)
	w.VarInt(int32(len(p.WorldNames)))
	for _, n := range p.WorldNames {
		w.String(n)
	}
	w.VarInt(p.MaxPlayers)
	w.VarInt(p.ViewDistance)
	w.VarInt(p.SimulationDistance)
	w.Bool(p.ReducedDebugInfo)
	w.Bool(p.EnableRespawnScreen)
	w.Bool(p.DoLimitedCrafting)
	// SpawnInfo
	w.VarInt(p.DimensionID)
	w.String(p.WorldName)
	w.Int64(p.HashedSeed)
	w.VarInt(int32(p.GameMode))
	w.VarInt(p.PreviousGameMode)
	w.Bool(p.IsDebug)
	w.Bool(p.IsFlat)
	w.VarInt(p.PortalCooldown)
	w.VarInt(p.SeaLevel)
	w.Bool(p.EnforcesSecure)
}

// PlayerPositionOut 是 player_position 包（服务器发起的玩家传送）。
type PlayerPositionOut struct {
	TeleportID                int32
	X, Y, Z                   float64
	VX, VY, VZ                float64
	Yaw, Pitch                float32
	Flags                     [4]byte // 相对标志位图，全 0 = 绝对传送
}

// WritePlayerPosition 写入 player_position 包。
func WritePlayerPosition(w *protocol.Writer, p PlayerPositionOut) {
	w.VarInt(p.TeleportID)
	w.Double(p.X)
	w.Double(p.Y)
	w.Double(p.Z)
	w.Double(p.VX)
	w.Double(p.VY)
	w.Double(p.VZ)
	w.Float(p.Yaw)
	w.Float(p.Pitch)
	w.FixedBytes(p.Flags[:])
}

// Abilities 是玩家能力包。
type Abilities struct {
	Flags        uint8 // bit0=无敌 bit1=飞行 bit2=可飞行 bit3=创造
	FlyingSpeed  float32
	WalkingSpeed float32
}

// WriteAbilities 写入 player_abilities 包。
func WriteAbilities(w *protocol.Writer, a Abilities) {
	w.Uint8(a.Flags)
	w.Float(a.FlyingSpeed)
	w.Float(a.WalkingSpeed)
}

// Clock 是 26.x set_time 中的单个时钟。
type Clock struct {
	ID         int32
	TotalTicks int64
	PartialTick float32
	Rate       float32
}

// WriteSetTime 写入 set_time 包：世界年龄 + 时钟更新列表。
// 常规做法：白天时钟 ID 0，totalTicks 即一天内时间。
func WriteSetTime(w *protocol.Writer, worldAge int64, clocks []Clock) {
	w.Int64(worldAge)
	w.VarInt(int32(len(clocks)))
	for _, c := range clocks {
		w.VarInt(c.ID)
		w.Int64(c.TotalTicks)
		w.Float(c.PartialTick)
		w.Float(c.Rate)
	}
}

// WriteSpawnPosition 写入 set_default_spawn_position 包。
func WriteSpawnPosition(w *protocol.Writer, x, y, z int32, yaw, pitch float32) {
	// GlobalPos: dimension identifier + Position
	w.String("minecraft:overworld")
	w.Position(protocol.Position{X: x, Y: y, Z: z})
	w.Float(yaw)
	w.Float(pitch)
}

// WriteChunkCenter 写入 set_chunk_cache_center。
func WriteChunkCenter(w *protocol.Writer, x, z int32) {
	w.VarInt(x)
	w.VarInt(z)
}

// WriteViewDistance 写入 set_chunk_cache_radius。
func WriteViewDistance(w *protocol.Writer, d int32) { w.VarInt(d) }

// WriteSimulationDistance 写入 simulation_distance。
func WriteSimulationDistance(w *protocol.Writer, d int32) { w.VarInt(d) }

// WriteChunkBatchStart 写入 chunk_batch_start（空载荷）。
func WriteChunkBatchStart(w *protocol.Writer) {}

// WriteChunkBatchFinished 写入 chunk_batch_finished。
func WriteChunkBatchFinished(w *protocol.Writer, batchSize int32) { w.VarInt(batchSize) }

// WriteLevelChunk 写入 level_chunk_with_light：坐标 + 录制模板包体。
func WriteLevelChunk(w *protocol.Writer, x, z int32, template []byte) {
	w.Int32(x)
	w.Int32(z)
	w.RawBytes(template)
}

// WriteForgetChunk 写入 forget_level_chunk（卸载区块）。
func WriteForgetChunk(w *protocol.Writer, x, z int32) {
	w.Int32(x)
	w.Int32(z)
}

// PlayerInfoAction 定义 player_info_update 的动作位。
const (
	InfoAddPlayer    = 0x01
	InfoInitializeChat = 0x02
	InfoUpdateGamemode = 0x04
	InfoUpdateListed   = 0x08
	InfoUpdateLatency  = 0x10
	InfoUpdateDisplay  = 0x20
	InfoUpdateHat      = 0x40
	InfoUpdateListOrder = 0x80
)

// WritePlayerInfoAdd 写入一个"添加玩家到列表"的 player_info_update。
func WritePlayerInfoAdd(w *protocol.Writer, uuid protocol.UUID, name string, mode GameMode) {
	w.Uint8(InfoAddPlayer | InfoUpdateGamemode | InfoUpdateListed | InfoUpdateLatency)
	w.VarInt(1) // 条目数
	w.UUID(uuid)
	w.String(name)
	w.VarInt(0) // properties
	w.VarInt(int32(mode))
	w.Bool(true) // listed
	w.VarInt(0)  // latency(ms)
}

// WritePlayerInfoRemove 写入 player_info_remove。
func WritePlayerInfoRemove(w *protocol.Writer, uuid protocol.UUID) {
	w.VarInt(1)
	w.UUID(uuid)
}

// WriteSystemChat 写入 system_chat（NBT 文本组件）。
func WriteSystemChat(w *protocol.Writer, text string, actionBar bool) {
	protocol.WriteNBT(w, protocol.Compound{"text": text})
	w.Bool(actionBar)
}

// WriteSpawnEntity 写入 spawn_entity。速度单位是 1/8000 格。
type SpawnEntity struct {
	EntityID    int32
	UUID        protocol.UUID
	Type        int32
	X, Y, Z     float64
	VX, VY, VZ  int16
	Pitch, Yaw  float32
	HeadPitch   float32
	ObjectData  int32
}

// WriteSpawnEntity 写入 spawn_entity 包。
func (s SpawnEntity) Write(w *protocol.Writer) {
	w.VarInt(s.EntityID)
	w.UUID(s.UUID)
	w.VarInt(s.Type)
	w.Double(s.X)
	w.Double(s.Y)
	w.Double(s.Z)
	w.Int16(s.VX)
	w.Int16(s.VY)
	w.Int16(s.VZ)
	w.Float(s.Pitch)
	w.Float(s.Yaw)
	w.Float(s.HeadPitch)
	w.VarInt(s.ObjectData)
}

// WriteEntityPositionSync 写入 entity_position_sync（绝对位置 + 速度 + 朝向）。
func WriteEntityPositionSync(w *protocol.Writer, entityID int32, x, y, z, vx, vy, vz float64, yaw, pitch float32, onGround bool) {
	w.VarInt(entityID)
	w.Double(x)
	w.Double(y)
	w.Double(z)
	w.Double(vx)
	w.Double(vy)
	w.Double(vz)
	w.Float(yaw)
	w.Float(pitch)
	w.Bool(onGround)
}

// WriteEntityHeadRotation 写入 entity_head_rotation。
func WriteEntityHeadRotation(w *protocol.Writer, entityID int32, yaw protocol.Angle) {
	w.VarInt(entityID)
	w.Uint8(uint8(yaw))
}

// WriteRemoveEntities 写入 remove_entities。
func WriteRemoveEntities(w *protocol.Writer, ids []int32) {
	w.VarInt(int32(len(ids)))
	for _, id := range ids {
		w.VarInt(id)
	}
}

// WriteHeldSlot 写入 set_held_slot。
func WriteHeldSlot(w *protocol.Writer, slot int8) { w.Int8(slot) }

// WriteTeleportEntity 写入 teleport_entity（绝对传送实体）。
func WriteTeleportEntity(w *protocol.Writer, entityID int32, x, y, z, vx, vy, vz float64, yaw, pitch float32, onGround bool) {
	w.VarInt(entityID)
	w.Double(x)
	w.Double(y)
	w.Double(z)
	w.Double(vx)
	w.Double(vy)
	w.Double(vz)
	w.Float(yaw)
	w.Float(pitch)
	w.FixedBytes([]byte{0, 0, 0, 0}) // flags：绝对
	w.Bool(onGround)
}
