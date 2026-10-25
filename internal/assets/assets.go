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
	"sync"
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

// EntityTypes 返回协议版本的实体类型表（短名 → 网络 ID，如 "player"→159）。
func EntityTypes(proto int32) map[string]int32 {
	return loadTextTable(proto, "entitytype")
}

// ItemIDs 返回协议版本的物品表（短名 → 网络 ID）。
func ItemIDs(proto int32) map[string]int32 {
	return loadTextTable(proto, "itemid")
}

// MobDrops 返回生物掉落表（短名 → 掉落物品网络 ID，777 基准空间）。
func MobDrops(proto int32) map[string]int32 {
	return loadTextTable(proto, "mobdrops")
}

// BlockDrops 返回方块默认状态 → 掉落物品网络 ID（cmd/gendrops 生成，仅基准版本）。
func BlockDrops(proto int32) map[uint32]int32 {
	b, err := files.ReadFile(fmt.Sprintf("data/blockdrops_%d.txt", proto))
	if err != nil {
		return nil
	}
	m := map[uint32]int32{}
	for _, line := range strings.Split(string(b), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		state, err1 := strconv.Atoi(fields[0])
		item, err2 := strconv.Atoi(fields[1])
		if err1 != nil || err2 != nil {
			continue
		}
		m[uint32(state)] = int32(item)
	}
	return m
}

// MineableMask 返回需镐方块的状态 → 可采材质位掩码（bit0木/金 1石 2铜 3铁 4钻 5合金）。
func MineableMask(proto int32) map[uint32]uint8 {
	b, err := files.ReadFile(fmt.Sprintf("data/mineable_%d.txt", proto))
	if err != nil {
		return nil
	}
	m := map[uint32]uint8{}
	for _, line := range strings.Split(string(b), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		state, err1 := strconv.Atoi(fields[0])
		mask, err2 := strconv.Atoi(fields[1])
		if err1 != nil || err2 != nil || mask < 0 || mask > 63 {
			continue
		}
		m[uint32(state)] = uint8(mask)
	}
	return m
}

// PickaxeBits 返回镐物品 ID → 材质位。
func PickaxeBits(proto int32) map[int32]uint8 {
	b, err := files.ReadFile(fmt.Sprintf("data/pickaxes_%d.txt", proto))
	if err != nil {
		return nil
	}
	m := map[int32]uint8{}
	for _, line := range strings.Split(string(b), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		item, err1 := strconv.Atoi(fields[0])
		bit, err2 := strconv.Atoi(fields[1])
		if err1 != nil || err2 != nil || bit < 0 || bit > 5 {
			continue
		}
		m[int32(item)] = uint8(bit)
	}
	return m
}

// loadTextTable 读取 "名称 数值" 两列表。
func loadTextTable(proto int32, prefix string) map[string]int32 {
	b, err := files.ReadFile(fmt.Sprintf("data/%s_%d.txt", prefix, proto))
	if err != nil {
		return nil
	}
	m := map[string]int32{}
	for _, line := range strings.Split(string(b), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		v, err := strconv.Atoi(fields[1])
		if err != nil {
			continue
		}
		m[fields[0]] = int32(v)
	}
	return m
}

// ---- 跨版本 ID 翻译（基准 777 → 目标协议）----
// 775 无官方报告，借用 776 的表（相邻版本，同名匹配）。
var (
	transOnce sync.Once
	stateMaps map[int32]map[uint32]uint32
	itemMaps  map[int32]map[int32]int32
)

func loadTranslate() {
	transOnce.Do(func() {
		stateMaps = map[int32]map[uint32]uint32{}
		itemMaps = map[int32]map[int32]int32{}
		for _, p := range []int32{774, 776, 777} {
			if b, err := files.ReadFile(fmt.Sprintf("data/statemap_%d.txt", p)); err == nil {
				m := map[uint32]uint32{}
				for _, line := range strings.Split(string(b), "\n") {
					fields := strings.Fields(line)
					if len(fields) != 2 {
						continue
					}
					from, e1 := strconv.ParseUint(fields[0], 10, 32)
					to, e2 := strconv.ParseUint(fields[1], 10, 32)
					if e1 == nil && e2 == nil {
						m[uint32(from)] = uint32(to)
					}
				}
				stateMaps[p] = m
			}
			if b, err := files.ReadFile(fmt.Sprintf("data/itemmap_%d.txt", p)); err == nil {
				m := map[int32]int32{}
				for _, line := range strings.Split(string(b), "\n") {
					fields := strings.Fields(line)
					if len(fields) != 2 {
						continue
					}
					from, e1 := strconv.Atoi(fields[0])
					to, e2 := strconv.Atoi(fields[1])
					if e1 == nil && e2 == nil {
						m[int32(from)] = int32(to)
					}
				}
				itemMaps[p] = m
			}
		}
		stateMaps[775], itemMaps[775] = stateMaps[776], itemMaps[776]
	})
}

// StateMap 返回 基准(777)方块状态 ID → 目标协议状态 ID 的翻译表。
// 777 返回 nil（无需翻译）。
func StateMap(proto int32) map[uint32]uint32 {
	loadTranslate()
	return stateMaps[proto]
}

// ItemMap 返回 基准(777)物品网络 ID → 目标协议物品 ID 的翻译表（777 为 nil）。
func ItemMap(proto int32) map[int32]int32 {
	loadTranslate()
	return itemMaps[proto]
}

// RecipeFile 返回指定协议的合成配方表原始内容。
func RecipeFile(proto int32) ([]byte, error) {
	return files.ReadFile(fmt.Sprintf("data/recipes_%d.txt", proto))
}

// ItemTags 返回物品标签表（标签名 → 成员短名集合）。
func ItemTags(proto int32) map[string]map[string]bool {
	b, err := files.ReadFile(fmt.Sprintf("data/itemtags_%d.txt", proto))
	if err != nil {
		return nil
	}
	m := map[string]map[string]bool{}
	for _, line := range strings.Split(string(b), "\n") {
		parts := strings.SplitN(line, " ", 2)
		if len(parts) != 2 {
			continue
		}
		set := map[string]bool{}
		for _, name := range strings.Split(parts[1], ",") {
			if name != "" {
				set[name] = true
			}
		}
		if len(set) > 0 {
			m[parts[0]] = set
		}
	}
	return m
}

// Smelting 返回冶炼表（输入物品短名 → 产物短名，777 基准）。
func Smelting(proto int32) map[string]string {
	b, err := files.ReadFile(fmt.Sprintf("data/smelting_%d.txt", proto))
	if err != nil {
		return nil
	}
	m := map[string]string{}
	for _, line := range strings.Split(string(b), "\n") {
		parts := strings.Fields(line)
		if len(parts) == 2 {
			m[parts[0]] = parts[1]
		}
	}
	return m
}

// InteractFile 返回交互方块状态表原始内容（bedstates/doorstates）。
func InteractFile(name string) ([]byte, error) {
	return files.ReadFile(fmt.Sprintf("data/%s_777.txt", name))
}

// BlockNames 返回方块状态 → 方块短名（777 基准）。
func BlockNames(proto int32) map[uint32]string {
	b, err := files.ReadFile(fmt.Sprintf("data/blocknames_%d.txt", proto))
	if err != nil {
		return nil
	}
	m := map[uint32]string{}
	for _, line := range strings.Split(string(b), "\n") {
		parts := strings.Fields(line)
		if len(parts) == 2 {
			if v, err := strconv.ParseUint(parts[0], 10, 32); err == nil {
				m[uint32(v)] = parts[1]
			}
		}
	}
	return m
}

// Brewing 返回酿造表（输入药水名 材料 → 产物药水名；空输入名=任意水瓶）。
func Brewing(proto int32) map[[2]string]string {
	b, err := files.ReadFile(fmt.Sprintf("data/brewing_%d.txt", proto))
	if err != nil {
		return nil
	}
	m := map[[2]string]string{}
	for _, line := range strings.Split(string(b), "\n") {
		parts := strings.Fields(line)
		if len(parts) == 3 {
			m[[2]string{parts[0], parts[1]}] = parts[2]
		}
	}
	return m
}
