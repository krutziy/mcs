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
	"sort"
	"strconv"
	"strings"
)

type blockEntry struct {
	States []struct {
		Default bool `json:"default"`
		ID      int32 `json:"id"`
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
}
