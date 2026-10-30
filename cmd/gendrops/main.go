// gendrops 从官方 server.jar（bundler 内层数据包）与 --reports 报告生成
// 挖掘掉落相关数据表（只读 jar 内的 JSON 数据文件，不反编译代码）：
//
//	blockdrops_<proto>.txt   方块状态 ID → 掉落定义（含数量范围/时运/精准采集）
//	                                 格式: state item min max flags silk(-=无)
//	                                 flags bit0 = 时运加成（apply_bonus fortune）
//	mineable_<proto>.txt     需镐才掉落的方块：状态 ID → 可采材质位掩码
//	                                 bit0木/金 bit1石 bit2铜 bit3铁 bit4钻 bit5合金
//	pickaxes_<proto>.txt     镐物品：物品 ID → 材质位
//	mobdrops_<proto>.txt     生物 → 掉落物品 + 数量范围（首个常规条目）
//	itemstack_<proto>.txt    物品短名 → 最大堆叠（官方 components 报告）
//	blockhardness_<proto>.txt 方块短名 → 硬度（Prismarine blocks.json 公开数据）
//
// 用法（产物提交入库，开发期一次性）：
//
//	go run ./cmd/gendrops -jar .tools/v263/server/server.jar \
//	    -reports .tools/v263/generated/reports -proto 777 -out internal/assets/data \
//	    -mcblocks .tools/mcdata/blocks.json
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
	"strconv"
	"strings"
)

type blockEntry struct {
	States []struct {
		Default bool  `json:"default"`
		ID      int32 `json:"id"`
	} `json:"states"`
}

// 26.x 战利品表：函数列表字段为 "modifier"（1.21.4 及更早为 "functions"，两者都读）；
// 字段值可为数组或单对象（zombie.json 等用单对象形式）。
type lootFuncs []lootFunc

func (l *lootFuncs) UnmarshalJSON(b []byte) error {
	s := strings.TrimSpace(string(b))
	if s == "null" || s == "" {
		return nil
	}
	if s[0] == '[' {
		var arr []lootFunc
		if err := json.Unmarshal(b, &arr); err != nil {
			return err
		}
		*l = arr
		return nil
	}
	var one lootFunc
	if err := json.Unmarshal(b, &one); err != nil {
		return err
	}
	*l = lootFuncs{one}
	return nil
}

type lootEntry struct {
	Type       string      `json:"type"`
	Condition  interface{} `json:"condition"`
	Conditions interface{} `json:"conditions"`
	Name       string      `json:"name"`
	Modifier   lootFuncs   `json:"modifier"`
	Functions  lootFuncs   `json:"functions"`
	Children   []lootEntry `json:"children"`
}

type lootFunc struct {
	Type        string      `json:"type"`
	Count       interface{} `json:"count"`
	Enchantment string      `json:"enchantment"`
}

type lootTable struct {
	Pools []struct {
		Condition interface{}   `json:"condition"`
		Entries   []lootEntry   `json:"entries"`
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

// conditionTexts 把 condition/conditions（单数或列表）打平为字符串列表。
func conditionTexts(c interface{}, out *[]string) {
	switch v := c.(type) {
	case nil:
	case string:
		*out = append(*out, v)
	case []interface{}:
		for _, e := range v {
			conditionTexts(e, out)
		}
	case map[string]interface{}:
		// 复合条件（all_of/any_of/inverted）递归 terms/term；
		// table_bonus/survives_explosion 等按 type 记录。
		if t, ok := v["type"].(string); ok {
			*out = append(*out, t)
		}
		for _, key := range []string{"terms", "term"} {
			if sub, ok := v[key]; ok {
				conditionTexts(sub, out)
			}
		}
	}
}

func entryConditions(e lootEntry) []string {
	var out []string
	conditionTexts(e.Condition, &out)
	conditionTexts(e.Conditions, &out)
	return out
}

func hasCondition(conds []string, sub string) bool {
	for _, c := range conds {
		if strings.Contains(c, sub) {
			return true
		}
	}
	return false
}

// flattenItems 把条目树打平为 item 条目列表（含 alternatives 子级）。
func flattenItems(e lootEntry, out *[]lootEntry) {
	if e.Type == "minecraft:item" {
		*out = append(*out, e)
	}
	for _, c := range e.Children {
		flattenItems(c, out)
	}
}

// dropInfo 从战利品表提取：常规掉落（物品 + 数量范围 + 时运标记）与精准采集掉落。
// 规则：优先"无条件"的非精准条目（如砾石表取砾石而非 10% 的燧石），
// 无条件条目不存在时取首个非精准条目（矿石/树苗等）。
type dropInfo struct {
	item    string
	min     int
	max     int
	fortune bool
	silk    string
}

func parseDropTable(t lootTable) dropInfo {
	var d dropInfo
	var items []lootEntry
	for _, pool := range t.Pools {
		var pc []string
		conditionTexts(pool.Condition, &pc)
		if hasCondition(pc, "silk_touch") && !hasCondition(pc, "inverted") {
			// glass/冰：整个池仅在精准采集时掉落，池内条目即精准掉落
			var poolItems []lootEntry
			for _, e := range pool.Entries {
				flattenItems(e, &poolItems)
			}
			if len(poolItems) > 0 && d.silk == "" {
				d.silk = poolItems[0].Name
			}
			continue
		}
		for _, e := range pool.Entries {
			flattenItems(e, &items)
		}
	}
	var normal []lootEntry
	for _, it := range items {
		if hasCondition(entryConditions(it), "silk_touch") {
			if d.silk == "" {
				d.silk = it.Name
			}
			continue
		}
		normal = append(normal, it)
	}
	if len(normal) == 0 {
		return d
	}
	// 优先无条件的条目（跳过概率条件条目）
	chosen := normal[0]
	for _, it := range normal {
		if len(entryConditions(it)) == 0 {
			chosen = it
			break
		}
	}
	d.item = chosen.Name
	d.min, d.max = 1, 1
	fns := chosen.Modifier
	if len(fns) == 0 {
		fns = chosen.Functions
	}
	for _, f := range fns {
		switch {
		case f.Type == "minecraft:set_count":
			d.min, d.max = countRange(f.Count)
		case f.Type == "minecraft:apply_bonus" && strings.HasSuffix(f.Enchantment, "fortune"):
			d.fortune = true
		}
	}
	return d
}

// countRange 解析 set_count 的数量（数字或 uniform {min,max}）。
func countRange(c interface{}) (int, int) {
	switch v := c.(type) {
	case float64:
		return int(v), int(v)
	case map[string]interface{}:
		mn, mx := 1, 1
		if f, ok := v["min"].(float64); ok {
			mn = int(f)
		}
		if f, ok := v["max"].(float64); ok {
			mx = int(f)
		}
		return mn, mx
	}
	return 1, 1
}

func main() {
	jarPath := flag.String("jar", "", "官方 server.jar（bundler 壳）")
	reports := flag.String("reports", "", "--reports 生成目录")
	proto := flag.Int("proto", 0, "协议版本（决定物品/方块 ID 空间，当前基准 777）")
	outDir := flag.String("out", "", "输出目录")
	mcBlocks := flag.String("mcblocks", "", "Prismarine blocks.json（方块硬度公开数据，可选）")
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

	// --- 掉落表（方块的全部状态 → 同一掉落；数量范围 + 时运/精准标记）---
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
		d := parseDropTable(t)
		if d.item == "" && d.silk == "" {
			continue
		}
		silkStr := "-"
		if d.silk != "" {
			if id, ok := itemID[d.silk]; ok {
				silkStr = strconv.Itoa(int(id))
			}
		}
		flags := 0
		if d.fortune {
			flags = 1
		}
		itemStr := "0"
		if id, ok := itemID[d.item]; ok && d.item != "" {
			itemStr = strconv.Itoa(int(id))
		}
		for _, s := range b.States {
			dropLines = append(dropLines, fmt.Sprintf("%d %s %d %d %d %s", s.ID, itemStr, d.min, d.max, flags, silkStr))
		}
	}

		var expandTag func(name string, depth int) []string
	expandTag = func(name string, depth int) []string {
		if depth > 4 {
			return nil
		}
		var tl struct{ Values []interface{} }
		if !readZipJSON(z, "data/minecraft/tags/block/"+strings.TrimPrefix(name, "minecraft:")+".json", &tl) {
			return nil
		}
		var out []string
		for _, v := range tl.Values {
			s, ok := v.(string)
			if !ok {
				continue
			}
			if strings.HasPrefix(s, "#") {
				out = append(out, expandTag(strings.TrimPrefix(s, "#"), depth+1)...)
			} else {
				out = append(out, s)
			}
		}
		return out
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
		for _, n := range expandTag("minecraft:incorrect_for_"+mat+"_tool", 0) {
			set[n] = true
		}
		incorrect[mat] = set
	}
	var mineableLines []string
	for _, name := range expandTag("minecraft:mineable/pickaxe", 0) {
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

	// --- 方块工具类别（mineable/{axe,shovel,hoe} 标签；pickaxe 已有 mineable 表）---
	// blocktoolclass_<proto>.txt：状态 ID → 1斧 2锹 3锄（镐 = mineable 表）
	// 标签值递归展开（#minecraft:logs 等嵌套引用）。
	var toolClassLines []string
	toolClasses := []struct {
		tag  string
		code int
	}{{"axe", 1}, {"shovel", 2}, {"hoe", 3}}
	for _, tc := range toolClasses {
		for _, name := range expandTag("minecraft:mineable/"+tc.tag, 0) {
			b, ok := blocks[name]
			if !ok || len(b.States) == 0 {
				continue
			}
			for _, st := range b.States {
				toolClassLines = append(toolClassLines, fmt.Sprintf("%d %d", st.ID, tc.code))
			}
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

	// --- 生物掉落表（首个常规条目 + 数量范围）---
	var mobLines []string
	for _, short := range []string{"pig", "sheep", "cow", "chicken", "rabbit", "zombie",
		"skeleton", "spider", "creeper", "enderman"} {
		var t lootTable
		if !readZipJSON(z, "data/minecraft/loot_table/entities/"+short+".json", &t) {
			continue
		}
		d := parseDropTable(t)
		if d.item == "" {
			continue
		}
		id, ok := itemID[d.item]
		if !ok {
			continue
		}
		mobLines = append(mobLines, fmt.Sprintf("%s %d %d %d", short, id, d.min, d.max))
	}

	// --- 物品最大堆叠（官方 components 报告 item/<name>.json 的 max_stack_size）---
	var stackLines []string
	compDir := *reports + "/minecraft/components/item"
	if ents, err := os.ReadDir(compDir); err == nil {
		for _, e := range ents {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
				continue
			}
			raw, err := os.ReadFile(filepath.Join(compDir, e.Name()))
			if err != nil {
				continue
			}
			var c struct {
				Components map[string]interface{} `json:"components"`
			}
			if json.Unmarshal(raw, &c) != nil {
				continue
			}
			stack := 64
			if v, ok := c.Components["minecraft:max_stack_size"].(float64); ok {
				stack = int(v)
			}
			stackLines = append(stackLines, fmt.Sprintf("%s %d", strings.TrimSuffix(e.Name(), ".json"), stack))
		}
		sort.Strings(stackLines)
	}

	// --- 方块硬度（Prismarine blocks.json 公开数据）---
	var hardLines []string
	if *mcBlocks != "" {
		raw, err := os.ReadFile(*mcBlocks)
		if err != nil {
			fmt.Fprintln(os.Stderr, "读 blocks.json 失败:", err)
			os.Exit(1)
		}
		var list []struct {
			Name      string  `json:"name"`
			Hardness  float64 `json:"hardness"`
			Diggable  bool    `json:"diggable"`
		}
		if json.Unmarshal(raw, &list) != nil {
			fmt.Fprintln(os.Stderr, "解析 blocks.json 失败")
			os.Exit(1)
		}
		for _, b := range list {
			h := b.Hardness
			if h < 0 {
				h = -1 // 基岩/传送门框架（不可破坏）
			}
			hardLines = append(hardLines, fmt.Sprintf("%s %g", b.Name, h))
		}
		sort.Strings(hardLines)
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
	sort.Strings(mobLines)
	write(fmt.Sprintf("blockdrops_%d.txt", *proto), dropLines)
	write(fmt.Sprintf("mineable_%d.txt", *proto), mineableLines)
	write(fmt.Sprintf("pickaxes_%d.txt", *proto), pickaxeLines)
	write(fmt.Sprintf("mobdrops_%d.txt", *proto), mobLines)
	sort.Strings(toolClassLines)
	write(fmt.Sprintf("blocktoolclass_%d.txt", *proto), toolClassLines)
	if len(stackLines) > 0 {
		write(fmt.Sprintf("itemstack_%d.txt", *proto), stackLines)
	}
	if len(hardLines) > 0 {
		write(fmt.Sprintf("blockhardness_%d.txt", *proto), hardLines)
	}
}
