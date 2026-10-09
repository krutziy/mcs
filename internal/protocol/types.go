package protocol

// UUID 是 128 位通用唯一标识符（Mojang/离线玩家 ID、实体 UUID）。
type UUID [16]byte

// Position 是方块坐标（整数格点）。
type Position struct {
	X, Y, Z int32
}

// Angle 是以度为单位的朝向角，编码时映射到 256 级。
type Angle float32
