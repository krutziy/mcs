package world

import (
	"compress/gzip"
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// Manager 是服务端权威的世界状态：缓存已生成区块并提供世界坐标的方块存取。
// 区块首次访问时经 Generator 生成，之后常驻内存直到卸载（未来持久化用），
// 因此玩家的挖掘/放置直接落到缓存的区块上，退出重进世界不变。
// 经 SetBlock 修改过的区块记为脏，Save 持久化到磁盘（自有二进制格式）。
type Manager struct {
	gen    Generator
	chunks map[[2]int32]*Chunk
	dirty  map[[2]int32]bool
}

// NewManager 创建世界管理器。
func NewManager(gen Generator) *Manager {
	return &Manager{gen: gen, chunks: map[[2]int32]*Chunk{}, dirty: map[[2]int32]bool{}}
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
	key := chunkKey(x>>4, z>>4)
	m.Chunk(x>>4, z>>4).SetBlock(x&15, y-MinY, z&15, state)
	m.dirty[key] = true
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

// 存档魔数 "MCSW" + 版本 1。
var saveMagic = [4]byte{'M', 'C', 'S', 'W'}

// Save 把所有脏区块写入目录（每个区块一个 gzip 文件）。
// 已保存过的区块从脏集合移除；返回写入的区块数。
func (m *Manager) Save(dir string) (int, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return 0, err
	}
	n := 0
	for key := range m.dirty {
		ch, ok := m.chunks[key]
		if !ok {
			delete(m.dirty, key)
			continue
		}
		path := filepath.Join(dir, fmt.Sprintf("c.%d.%d.bin", key[0], key[1]))
		if err := writeChunkFile(path, ch); err != nil {
			return n, err
		}
		delete(m.dirty, key)
		n++
	}
	return n, nil
}

// writeChunkFile 写单个区块：魔数/版本 + 坐标 + 生物群系 + 逐节的非空计数与方块数组（gzip）。
func writeChunkFile(path string, ch *Chunk) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	gz := gzip.NewWriter(f)
	defer gz.Close()

	var head [16]byte
	copy(head[:4], saveMagic[:])
	binary.BigEndian.PutUint32(head[4:8], 1)
	binary.BigEndian.PutUint32(head[8:12], uint32(ch.CX))
	binary.BigEndian.PutUint32(head[12:16], uint32(ch.CZ))
	if _, err := gz.Write(head[:]); err != nil {
		return err
	}
	if _, err := gz.Write([]byte{byte(ch.Biome)}); err != nil {
		return err
	}
	for i := int32(0); i < NumSections; i++ {
		s := ch.sections[i]
		if s == nil {
			if _, err := gz.Write([]byte{0, 0}); err != nil { // 非空计数 0 = 空节
				return err
			}
			continue
		}
		if err := binary.Write(gz, binary.BigEndian, s.NonAir); err != nil {
			return err
		}
		if err := binary.Write(gz, binary.BigEndian, &s.Blocks); err != nil {
			return err
		}
	}
	return nil
}

// Load 从目录读取全部区块存档。读取的区块直接进入缓存（清除脏标记，
// 相同坐标的存档优先于生成器）。返回加载数量。
func (m *Manager) Load(dir string) (int, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	n := 0
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		ch, err := readChunkFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue // 单块损坏不致命：跳过（按生成器重建）
		}
		m.chunks[chunkKey(ch.CX, ch.CZ)] = ch
		n++
	}
	return n, nil
}

// readChunkFile 解析单个区块存档；坐标以文件内容为准。
func readChunkFile(path string) (*Chunk, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return nil, err
	}
	defer gz.Close()

	var head [16]byte
	if _, err := io.ReadFull(gz, head[:]); err != nil {
		return nil, err
	}
	if string(head[:4]) != string(saveMagic[:]) || binary.BigEndian.Uint32(head[4:8]) != 1 {
		return nil, fmt.Errorf("world: 存档头不符 %s", path)
	}
	cx := int32(binary.BigEndian.Uint32(head[8:12]))
	cz := int32(binary.BigEndian.Uint32(head[12:16]))

	var biome [1]byte
	if _, err := io.ReadFull(gz, biome[:]); err != nil {
		return nil, err
	}
	ch := NewChunk(cx, cz, int32(int8(biome[0])))
	for i := int32(0); i < NumSections; i++ {
		var nonAir int16
		if err := binary.Read(gz, binary.BigEndian, &nonAir); err != nil {
			return nil, err
		}
		if nonAir == 0 {
			continue // 空节保持 nil（全空气）
		}
		s := &Section{NonAir: nonAir}
		if err := binary.Read(gz, binary.BigEndian, &s.Blocks); err != nil {
			return nil, err
		}
		ch.sections[i] = s
	}
	return ch, nil
}

// DirtyCount 返回未保存的脏区块数（监控/测试用）。
func (m *Manager) DirtyCount() int { return len(m.dirty) }

// EachChunk 迭代全部已生成区块（只读遍历，主循环内使用）。
func (m *Manager) EachChunk(f func(cx, cz int32, ch *Chunk)) {
	for key, ch := range m.chunks {
		f(key[0], key[1], ch)
	}
}

// Section 返回区块内指定节（越界返回 nil）。
func (c *Chunk) Section(i int32) *Section {
	if i < 0 || i >= NumSections {
		return nil
	}
	return c.sections[i]
}
