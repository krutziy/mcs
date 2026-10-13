// gendrops 从官方 server.jar（bundler 内层数据包）与 --reports 报告生成
// 挖掘掉落相关数据表（只读 jar 内的 JSON 数据文件，不反编译代码）：
//
//	blockdrops_<proto>.txt   方块默认状态 ID → 掉落物品网络 ID（无掉落则不出现）
//	                                 简化规则：取战利品表首个非精准采集物品，数量 1
//	mineable_<proto>.txt     需镐才掉落的方块：状态 ID → 可采材质位掩码
//	                                 bit0木/金 bit1石 bit2铜 bit3铁 bit4钻 bit5合金
//	pickaxes_<proto>.txt     镐物品：物品 ID → 材质位
//
// 用法（产物提交入库，开发期一次性）：
//
//	go run ./cmd/gendrops -jar .tools/v263/server/server.jar \
//	    -reports .tools/v263/generated/reports -proto 777 -out internal/assets/data
package main

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type blockEntry struct {
	States []struct {
		Default bool  `json:"default"`
		ID      int32 `json:"id"`
	} `json:"states"`
}

type lootEntry struct {
	Type      string      `json:"type"`
	Condition interface{} `json:"condition"`
	Name      string      `json:"name"`
	Children  []lootEntry `json:"children"`
}

type lootTable struct {
	Pools []struct {
		Entries []lootEntry `json:"entries"`
	} `json:"pools"`
}

// innerJar 打开 bundler 壳里的真正 server jar。
func innerJar(path string) (*zip.Reader, error) {
	outer, err := zip.OpenReader(path)
	if err != nil {
		return nil, err
	}
	defer outer.Close()
	for _, f := range outer.File {
		if !strings.HasPrefix(f.Name, "META-INF/versions/") || !strings.HasSuffix(f.Name, ".jar") {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return nil, err
		}
		data, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			return nil, err
		}
		return zip.NewReader(bytes.NewReader(data), int64(len(data)))
	}
	return nil, fmt.Errorf("bundler 内未找到 server jar")
}

func readZipJSON(z *zip.Reader, name string, v interface{}) bool {
	for _, f := range z.File {
		if f.Name != name {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return false
		}
		defer rc.Close()
		return json.NewDecoder(rc).Decode(v) == nil
	}
	return false
}

// plainDrop 从战利品条目树里取首个"普通掉落"物品（跳过带精准采集条件的分支）。
func plainDrop(e lootEntry) string {
	if e.Type == "minecraft:alternatives" {
		for _, c := range e.Children {
			if !isSilkCondition(c.Condition) {
				if n := plainDrop(c); n != "" {
					return n
				}
			}
		}
		return ""
	}
	if e.Type == "minecraft:item" && !isSilkCondition(e.Condition) {
		return e.Name
	}
	return ""
}

func isSilkCondition(c interface{}) bool {
	switch v := c.(type) {
	case string:
		return v == "minecraft:tool/can_silk_touch"
	case map[string]interface{}:
		return v["type"] == "minecraft:tool/can_silk_touch"
	}
	return false
}

func main() {
	jarPath := flag.String("jar", "", "官方 server.jar（bundler 壳）")
	reports := flag.String("reports", "", "--reports 生成目录")
	proto := flag.Int("proto", 0, "协议版本（决定物品/方块 ID 空间，当前基准 777）")
	outDir := flag.String("out", "", "输出目录")
	flag.Parse()
	if *jarPath == "" || *reports == "" || *proto == 0 || *outDir == "" {
		flag.Usage()
		os.Exit(2)
	}

	// 注册表：物品名 → 网络 ID
	regRaw, err := os.ReadFile(*reports + "/registries.json")
	if err != nil {
		fmt.Fprintln(os.Stderr, "读注册表失败:", err)
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
	itemID := map[string]int32{}
	for name, e := range reg["minecraft:item"].Entries {
		itemID[name] = e.ProtocolID
	}

	// 方块默认状态
	blkRaw, err := os.ReadFile(*reports + "/blocks.json")
	if err != nil {
		fmt.Fprintln(os.Stderr, "读方块报告失败:", err)
		os.Exit(1)
	}
	blocks := map[string]blockEntry{}
	if err := json.Unmarshal(blkRaw, &blocks); err != nil {
		fmt.Fprintln(os.Stderr, "解析方块报告失败:", err)
		os.Exit(1)
	}

	z, err := innerJar(*jarPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "打开内层 jar 失败:", err)
		os.Exit(1)
	}

	// --- 掉落表（方块的全部状态 → 同一掉落；仅默认态会让非默认变体不掉落）---
	var dropLines []string
	names := make([]string, 0, len(blocks))
	for name := range blocks {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		b := blocks[name]
		if len(b.States) == 0 {
			continue
		}
		var t lootTable
		if !readZipJSON(z, "data/minecraft/loot_table/blocks/"+strings.TrimPrefix(name, "minecraft:")+".json", &t) {
			continue // 无战利品表：不掉落
		}
		drop := ""
		for _, pool := range t.Pools {
			for _, e := range pool.Entries {
				if n := plainDrop(e); n != "" {
					drop = n
					break
				}
			}
			if drop != "" {
				break
			}
		}
		if drop == "" {
			continue
		}
		id, ok := itemID[drop]
		if !ok {
			continue
		}
		for _, s := range b.States {
			dropLines = append(dropLines, fmt.Sprintf("%d %d", s.ID, id))
		}
	}

	// --- 需镐门槛：材质位掩码（官方 incorrect_for_<材质>_tool 反向标签）---
	// bit0=木/金 bit1=石 bit2=铜 bit3=铁 bit4=钻 bit5=下界合金（合金与钻同级）
	materialBit := map[string]int{
		"wooden": 0, "golden": 0, "gold": 0, "stone": 1, "copper": 2,
		"iron": 3, "diamond": 4, "netherite": 5,
	}
	incorrect := map[string]map[string]bool{} // 材质 → 采不动方块集合
	for _, mat := range []string{"wooden", "stone", "copper", "iron", "diamond"} {
		set := map[string]bool{}
		var tl struct{ Values []interface{} }
		if readZipJSON(z, "data/minecraft/tags/block/incorrect_for_"+mat+"_tool.json", &tl) {
			for _, v := range tl.Values {
				if s, ok := v.(string); ok {
					set[s] = true
				}
			}
		}
		incorrect[mat] = set
	}
	var pickaxeTag struct{ Values []interface{} }
	readZipJSON(z, "data/minecraft/tags/block/mineable/pickaxe.json", &pickaxeTag)
	var mineableLines []string
	for _, v := range pickaxeTag.Values {
		name, ok := v.(string)
		if !ok {
			continue
		}
		b, ok := blocks[name]
		if !ok || len(b.States) == 0 {
			continue
		}
		mask := 0b111111
		for mat, bit := range materialBit {
			if bit != 5 && incorrect[mat][name] { // 合金没有 incorrect 标签（与钻同级）
				mask &^= 1 << bit
			}
		}
		for _, s := range b.States {
			mineableLines = append(mineableLines, fmt.Sprintf("%d %d", s.ID, mask))
		}
	}

	// --- 镐物品 → 材质位 ---
	var pickaxeItems struct{ Values []interface{} }
	readZipJSON(z, "data/minecraft/tags/item/pickaxes.json", &pickaxeItems)
	var pickaxeLines []string
	for _, v := range pickaxeItems.Values {
		name, ok := v.(string)
		if !ok {
			continue
		}
		id, ok := itemID[name]
		if !ok {
			continue
		}
		short := strings.TrimPrefix(name, "minecraft:")
		bit := 0
		for mat, b := range materialBit {
			if strings.HasPrefix(short, mat+"_") {
				bit = b
				break
			}
		}
		pickaxeLines = append(pickaxeLines, fmt.Sprintf("%d %d", id, bit))
	}

	write := func(name string, lines []string) {
		path := filepath.Join(*outDir, name)
		if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
			fmt.Fprintln(os.Stderr, "写出失败:", err)
			os.Exit(1)
		}
		fmt.Printf("%s: %d 行\n", path, len(lines))
	}
	sort.Strings(dropLines)
	sort.Strings(mineableLines)
	sort.Strings(pickaxeLines)
	write(fmt.Sprintf("blockdrops_%d.txt", *proto), dropLines)
	write(fmt.Sprintf("mineable_%d.txt", *proto), mineableLines)
	write(fmt.Sprintf("pickaxes_%d.txt", *proto), pickaxeLines)
}
