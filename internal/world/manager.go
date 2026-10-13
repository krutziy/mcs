package world

// Manager 是服务端权威的世界状态：缓存已生成区块并提供世界坐标的方块存取。
// 区块首次访问时经 Generator 生成，之后常驻内存直到卸载（未来持久化用），
// 因此玩家的挖掘/放置直接落到缓存的区块上，退出重进世界不变。
type Manager struct {
	gen    Generator
	chunks map[[2]int32]*Chunk
}

// NewManager 创建世界管理器。
func NewManager(gen Generator) *Manager {
	return &Manager{gen: gen, chunks: map[[2]int32]*Chunk{}}
}

// chunkKey 由区块坐标构造缓存键。
func chunkKey(cx, cz int32) [2]int32 { return [2]int32{cx, cz} }

// Chunk 返回指定区块（不存在则生成并缓存；同一坐标恒返回同一实例）。
func (m *Manager) Chunk(cx, cz int32) *Chunk {
	key := chunkKey(cx, cz)
	if ch, ok := m.chunks[key]; ok {
		return ch
	}
	ch := NewChunk(cx, cz, 0)
	m.gen.Generate(ch)
	m.chunks[key] = ch
	return ch
}

// ChunkIfLoaded 返回已生成的区块，未生成时返回 nil。
func (m *Manager) ChunkIfLoaded(cx, cz int32) *Chunk {
	return m.chunks[chunkKey(cx, cz)]
}

// SetBlock 设置世界坐标 (x,y,z) 处的方块状态。y 为绝对坐标（-64..319），
// 越界或区块尚未生成时忽略（区块可先经 Chunk 生成）。
func (m *Manager) SetBlock(x, y, z int32, state uint32) {
	if y < MinY || y >= MinY+WorldHeight {
		return
	}
	m.Chunk(x>>4, z>>4).SetBlock(x&15, y-MinY, z&15, state)
}

// GetBlock 读取世界坐标处的方块状态（未生成区块视为空气）。
func (m *Manager) GetBlock(x, y, z int32) uint32 {
	if y < MinY || y >= MinY+WorldHeight {
		return StateAir
	}
	ch := m.ChunkIfLoaded(x>>4, z>>4)
	if ch == nil {
		return StateAir
	}
	return ch.GetBlock(x&15, y-MinY, z&15)
}
