// genitemblocks 从官方 --reports 数据报告生成"物品 ID → 方块默认状态 ID"映射表，
// 输出到 internal/assets/data/itemblocks_<proto>.txt（每行 "itemID stateID"）。
//
// 用法（开发期一次性，产物提交入库）：
//
//	go run ./cmd/genitemblocks -reports .tools/v263/generated/reports -proto 777 \
//	    -canonical .tools/v263/generated/reports -out internal/assets/data/itemblocks_777.txt
//
// 物品表按 -reports 版本自身的 item 注册表解析；方块状态取 -canonical（基准版本，
// 当前为 777）的默认状态——世界内部统一用基准版本的状态 ID 空间，跨版本玩家
// 放置时按其协议把物品 ID 换算回基准状态。
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

type blockEntry struct {
	States []struct {
		Default    bool                   `json:"default"`
		ID         int32                  `json:"id"`
		Properties map[string]interface{} `json:"properties"`
	} `json:"states"`
}

func main() {
	reports := flag.String("reports", "", "该协议版本的 generated/reports 目录")
	canonical := flag.String("canonical", "", "基准版本（状态 ID 空间）的 reports 目录")
	proto := flag.Int("proto", 0, "该协议版本号（输出文件名用）")
	out := flag.String("out", "", "输出文件路径")
	flag.Parse()
	if *reports == "" || *canonical == "" || *proto == 0 || *out == "" {
		flag.Usage()
		os.Exit(2)
	}

	regFile := *reports + "/registries.json"
	blkFile := *canonical + "/blocks.json"
	regRaw, err := os.ReadFile(regFile)
	if err != nil {
		fmt.Fprintln(os.Stderr, "读注册表失败:", err)
		os.Exit(1)
	}
	blkRaw, err := os.ReadFile(blkFile)
	if err != nil {
		fmt.Fprintln(os.Stderr, "读方块报告失败:", err)
		os.Exit(1)
	}

	var reg map[string]struct {
		Entries map[string]struct {
			ProtocolID int32 `json:"protocol_id"`
		} `json:"entries"`
	}
	if err := json.Unmarshal(regRaw, &reg); err != nil {
		fmt.Fprintln(os.Stderr, "解析注册表失败:", err)
		os.Exit(1)
	}
	blocks := map[string]blockEntry{}
	if err := json.Unmarshal(blkRaw, &blocks); err != nil {
		fmt.Fprintln(os.Stderr, "解析方块报告失败:", err)
		os.Exit(1)
	}

	items := reg["minecraft:item"].Entries
	type pair struct{ item, state int32 }
	var pairs []pair
	for name, e := range items {
		b, ok := blocks[name]
		if !ok || len(b.States) == 0 {
			continue // 非方块物品
		}
		def := b.States[0].ID
		for _, s := range b.States {
			if s.Default {
				def = s.ID
				break
			}
		}
		pairs = append(pairs, pair{e.ProtocolID, def})
	}
	sort.Slice(pairs, func(i, j int) bool { return pairs[i].item < pairs[j].item })

	var sb strings.Builder
	for _, p := range pairs {
		sb.WriteString(strconv.FormatInt(int64(p.item), 10))
		sb.WriteByte(' ')
		sb.WriteString(strconv.FormatInt(int64(p.state), 10))
		sb.WriteByte('\n')
	}
	if err := os.WriteFile(*out, []byte(sb.String()), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "写出失败:", err)
		os.Exit(1)
	}
	fmt.Printf("%s: %d 项物品→方块映射\n", *out, len(pairs))

	// 顺带输出实体类型表（entitytype_<proto>.txt：名称 → 网络 ID）
	var entLines, itemIDLines []string
	for name, e := range reg["minecraft:entity_type"].Entries {
		short := strings.TrimPrefix(name, "minecraft:")
		entLines = append(entLines, fmt.Sprintf("%s %d", short, e.ProtocolID))
	}
	for name, e := range reg["minecraft:item"].Entries {
		short := strings.TrimPrefix(name, "minecraft:")
		itemIDLines = append(itemIDLines, fmt.Sprintf("%s %d", short, e.ProtocolID))
	}
	sort.Strings(entLines)
	sort.Strings(itemIDLines)
	entPath := filepath.Join(filepath.Dir(*out), fmt.Sprintf("entitytype_%d.txt", *proto))
	if err := os.WriteFile(entPath, []byte(strings.Join(entLines, "\n")+"\n"), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "写出失败:", err)
		os.Exit(1)
	}
	fmt.Printf("%s: %d 项实体类型\n", entPath, len(entLines))
	itemPath := filepath.Join(filepath.Dir(*out), fmt.Sprintf("itemid_%d.txt", *proto))
	if err := os.WriteFile(itemPath, []byte(strings.Join(itemIDLines, "\n")+"\n"), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "写出失败:", err)
		os.Exit(1)
	}
	fmt.Printf("%s: %d 项物品名→ID\n", itemPath, len(itemIDLines))

	// --- 跨版本 ID 翻译表（当 -reports 是非基准版本时）---
	if *reports != *canonical {
		writeTranslateTables(*reports, *canonical, *proto, filepath.Dir(*out))
	}
}

// writeTranslateTables 生成 基准(777)→目标版本 的方块状态 ID 与物品 ID 翻译表
// （statemap_<proto>.txt / itemmap_<proto>.txt，按"同名方块+同属性组合/同名物品"匹配）。
func writeTranslateTables(reports, canonical string, proto int, outDir string) {
	cblk := loadBlocks(canonical + "/blocks.json")
	tblk := loadBlocks(reports + "/blocks.json")
	var lines []string
	for name, cb := range cblk {
		tb, ok := tblk[name]
		if !ok {
			continue
		}
		for _, cs := range cb.States {
			for _, ts := range tb.States {
				if propsEqual(cs.Properties, ts.Properties) {
					lines = append(lines, fmt.Sprintf("%d %d", cs.ID, ts.ID))
					break
				}
			}
		}
	}
	sort.Strings(lines)
	path := filepath.Join(outDir, fmt.Sprintf("statemap_%d.txt", proto))
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "写出失败:", err)
		os.Exit(1)
	}
	fmt.Printf("%s: %d 项状态翻译\n", path, len(lines))

	// 物品 ID 翻译
	citems := loadItems(canonical + "/registries.json")
	titems := loadItems(reports + "/registries.json")
	var ilines []string
	for name, cid := range citems {
		if tid, ok := titems[name]; ok {
			ilines = append(ilines, fmt.Sprintf("%d %d", cid, tid))
		}
	}
	sort.Strings(ilines)
	ipath := filepath.Join(outDir, fmt.Sprintf("itemmap_%d.txt", proto))
	if err := os.WriteFile(ipath, []byte(strings.Join(ilines, "\n")+"\n"), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "写出失败:", err)
		os.Exit(1)
	}
	fmt.Printf("%s: %d 项物品翻译\n", ipath, len(ilines))
}

func loadBlocks(path string) map[string]blockEntry {
	raw, err := os.ReadFile(path)
	if err != nil {
		fmt.Fprintln(os.Stderr, "读方块报告失败:", err)
		os.Exit(1)
	}
	blocks := map[string]blockEntry{}
	if err := json.Unmarshal(raw, &blocks); err != nil {
		fmt.Fprintln(os.Stderr, "解析方块报告失败:", err)
		os.Exit(1)
	}
	return blocks
}

func loadItems(path string) map[string]int32 {
	raw, err := os.ReadFile(path)
	if err != nil {
		fmt.Fprintln(os.Stderr, "读注册表失败:", err)
		os.Exit(1)
	}
	var reg map[string]struct {
		Entries map[string]struct {
			ProtocolID int32 `json:"protocol_id"`
		} `json:"entries"`
	}
	if err := json.Unmarshal(raw, &reg); err != nil {
		fmt.Fprintln(os.Stderr, "解析注册表失败:", err)
		os.Exit(1)
	}
	m := map[string]int32{}
	for name, e := range reg["minecraft:item"].Entries {
		m[name] = e.ProtocolID
	}
	return m
}

func propsEqual(a, b map[string]interface{}) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}
