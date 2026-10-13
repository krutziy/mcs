// Package assets 内嵌各支持协议版本的关键字节数据（用 cmd/recorder 从官方
// 服务端录制，方法见 README）：
//
//	data/<proto>/registry_XX.bin  configuration 阶段 registry_data 包体
//	                             （id-only 条目，依赖客户端回显 minecraft:core）
//	data/<proto>/known_packs.bin  select_known_packs 包体
//	data/chunk_template.bin       26.1 平坦世界区块包体（黄金测试基准）
//	data/itemblocks_<proto>.txt   物品 ID → 方块默认状态 ID 映射（genitemblocks 生成）
package assets

import (
	"embed"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

//go:embed data
var files embed.FS

// Registries 返回指定协议版本的 registry_data 包体列表（按序）。
func Registries(proto int32) [][]byte {
	dir := fmt.Sprintf("data/%d", proto)
	entries, err := files.ReadDir(dir)
	if err != nil {
		return nil
	}
	var names []string
	for _, e := range entries {
		n := e.Name()
		if len(n) >= 9 && n[:9] == "registry_" {
			names = append(names, n)
		}
	}
	sort.Slice(names, func(i, j int) bool {
		a, _ := strconv.Atoi(names[i][9 : len(names[i])-4])
		b, _ := strconv.Atoi(names[j][9 : len(names[j])-4])
		return a < b
	})
	out := make([][]byte, 0, len(names))
	for _, n := range names {
		b, _ := files.ReadFile(dir + "/" + n)
		out = append(out, b)
	}
	return out
}

// KnownPacks 返回指定协议版本的 select_known_packs 包体。
func KnownPacks(proto int32) []byte {
	b, _ := files.ReadFile(fmt.Sprintf("data/%d/known_packs.bin", proto))
	return b
}

// ChunkTemplate 返回 26.1 平坦世界区块包体（不含坐标前缀），黄金测试用。
func ChunkTemplate() []byte {
	b, _ := files.ReadFile("data/chunk_template.bin")
	return b
}

// ChunkTemplate774 返回 1.21.11 平坦世界区块包体（含坐标前缀），黄金测试用。
func ChunkTemplate774() []byte {
	b, _ := files.ReadFile("data/chunk_template_774.bin")
	return b[8:] // 去掉坐标
}

// ItemBlocks 返回指定协议版本的"物品网络 ID → 方块默认状态 ID"映射
// （基准 777 状态空间；由 cmd/genitemblocks 从官方 reports 生成）。
// 没有对应数据表的版本返回 nil（该版本玩家暂时无法放置方块）。
func ItemBlocks(proto int32) map[int32]uint32 {
	b, err := files.ReadFile(fmt.Sprintf("data/itemblocks_%d.txt", proto))
	if err != nil {
		return nil
	}
	m := map[int32]uint32{}
	for _, line := range strings.Split(string(b), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		item, err1 := strconv.Atoi(fields[0])
		state, err2 := strconv.Atoi(fields[1])
		if err1 != nil || err2 != nil {
			continue
		}
		m[int32(item)] = uint32(state)
	}
	return m
}
