// Package assets 内嵌从官方 26.1 服务端录制的关键字节数据：
//   - registry_*.bin       configuration 阶段 registry_data 包体（id-only 条目，
//                          依赖客户端回显 minecraft:core 已知包）
//   - chunk_template.bin   平坦世界 level_chunk_with_light 包体（去掉坐标前缀），
//                          所有区块内容相同，可按任意坐标复用
//   - known_packs.bin      select_known_packs 包体
//
// 录制方法见 README（用 cmd/recorder 连接官方服务端抓包）。
package assets

import (
	"embed"
	"sort"
)

//go:embed data/*.bin
var files embed.FS

// Registries 返回全部 registry_data 包体（按文件名排序）。
func Registries() [][]byte {
	entries, err := files.ReadDir("data")
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
	sort.Strings(names)
	out := make([][]byte, 0, len(names))
	for _, n := range names {
		b, _ := files.ReadFile("data/" + n)
		out = append(out, b)
	}
	return out
}

// ChunkTemplate 返回平坦世界区块包体（不含坐标前缀）。
func ChunkTemplate() []byte {
	b, _ := files.ReadFile("data/chunk_template.bin")
	return b
}

// KnownPacks 返回 select_known_packs 包体。
func KnownPacks() []byte {
	b, _ := files.ReadFile("data/known_packs.bin")
	return b
}
