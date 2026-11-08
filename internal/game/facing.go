package game

// 朝向放置：含 facing 属性的方块按玩家朝向取对应状态（熔炉/箱子/发射器/
// 楼梯/活塞/漏斗等前置朝向玩家）。状态族来自 blocknames_full 资产：
// 同名方块、除 facing 外属性相同的归为一组，组内按 facing 值建映射。

import (
	"strings"

	"mcserver/internal/assets"
	"mcserver/internal/entity"
)

// facingGroups 组键（方块名 + 除 facing 外的属性签名）→ facing 值 → 状态。
var facingGroups = map[string]map[string]uint32{}

// stateGroupKey 状态 → 所在组键（启动时构建）。
var stateGroupKey = map[uint32]string{}

// loadFacingTables 解析 blocknames_full 构建 facing 族表。
func (g *Game) loadFacingTables() {
	// 全名/短名查找表（redstone2 的开关翻转与音色判定用）
	for st, nm := range assets.BlockNames(777) {
		blockNameLookup[st] = nm
	}
	if nbf := assets.BlockNamesFull(777); nbf != nil {
		for st, full := range nbf {
			fullStateNames[st] = full
			stateByFullName[full] = st
		}
	}
	if len(facingGroups) > 0 {
		return
	}
	for state, full := range assets.BlockNamesFull(777) {
		name := full
		props := map[string]string{}
		if i := strings.Index(full, "["); i > 0 {
			name = full[:i]
			for _, kv := range strings.Split(strings.Trim(full[i+1:len(full)-1], "]"), ",") {
				if j := strings.Index(kv, "="); j > 0 {
					props[kv[:j]] = kv[j+1:]
				}
			}
		}
		fv, has := props["facing"]
		if !has {
			continue
		}
		delete(props, "facing")
		var parts []string
		for k, v := range props {
			parts = append(parts, k+"="+v)
		}
		key := name + "|" + strings.Join(parts, ",")
		if facingGroups[key] == nil {
			facingGroups[key] = map[string]uint32{}
		}
		facingGroups[key][fv] = state
		stateGroupKey[state] = key
	}
}

// faceState 依据玩家朝向修正放置状态（无 facing 属性时原样返回）。
// 常规朝向方块（熔炉/箱子等）前置朝向玩家 = 玩家视线的反向。
func (g *Game) faceState(p *entity.Player, state uint32) uint32 {
	key, ok := stateGroupKey[state]
	if !ok {
		return state
	}
	group := facingGroups[key]
	// 玩家面朝方向的反向（方块面向玩家）
	opposite := map[string]string{"north": "south", "south": "north", "east": "west", "west": "east"}
	want := opposite[facingFromYaw(p.Yaw)]
	if s, ok := group[want]; ok {
		return s
	}
	return state
}

// ---- 盔甲右键穿戴 ----

// armorSlotOfName 盔甲物品 → 盔甲槽（5头 6胸 7腿 8靴）。
func armorSlotOfName(name string) (int32, bool) {
	switch {
	case strings.HasSuffix(name, "_helmet") || name == "turtle_shell" || strings.HasSuffix(name, "_helmet"):
		return 5, true
	case strings.HasSuffix(name, "_chestplate"):
		return 6, true
	case strings.HasSuffix(name, "_leggings"):
		return 7, true
	case strings.HasSuffix(name, "_boots"):
		return 8, true
	}
	return 0, false
}

// tryEquipArmor use_item 手持盔甲：右键直接穿戴（目标槽为空时）。
func (g *Game) tryEquipArmor(ev *playerUseItemEvent) bool {
	p := ev.p
	name := g.itemNameOf(p, p.Held().ID)
	slot, ok := armorSlotOfName(name)
	if !ok {
		return false
	}
	if p.Slots[slot].ID != 0 {
		return false // 已有盔甲：不处理（走普通使用）
	}
	p.Slots[slot] = p.Slots[36+p.HeldSlot]
	p.Slots[36+p.HeldSlot] = entity.Item{}
	g.syncInventory(p)
	g.broadcastEquipment(p)
	g.playSoundTo(p, SoundPlayer, "item.armor.equip_generic", 1, 1)
	return true
}
