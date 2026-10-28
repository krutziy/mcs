package game

// 方块实体导出到 Anvil 存档：箱子/熔炉/酿造台内容 → world.BlockEntity 列表。

import (
	"mcserver/internal/entity"
	"mcserver/internal/world"
)

// exportBlockEntities 收集全部方块实体（箱子/熔炉/酿造台）。
func (g *Game) exportBlockEntities() []world.BlockEntity {
	var out []world.BlockEntity
	// 箱子
	for pos, cs := range g.chests {
		be := world.BlockEntity{X: pos[0], Y: pos[1], Z: pos[2], ID: "minecraft:chest"}
		for i, it := range cs.slots {
			if it.ID != 0 {
				be.Items = append(be.Items, world.BEItem{ID: it.ID, Count: it.Count, Damage: it.Damage, Slot: int32(i)})
			}
		}
		out = append(out, be)
	}
	// 熔炉
	for pos, fs := range g.furnaces {
		be := world.BlockEntity{X: pos[0], Y: pos[1], Z: pos[2], ID: "minecraft:furnace"}
		addItem := func(slot int32, it entity.Item) {
			if it.ID != 0 {
				be.Items = append(be.Items, world.BEItem{ID: it.ID, Count: it.Count, Damage: it.Damage, Slot: slot})
			}
		}
		addItem(0, fs.input)
		addItem(1, fs.fuel)
		addItem(2, fs.output)
		be.Extra = map[string]int64{"BurnTime": int64(fs.burn), "CookTime": int64(fs.cook)}
		out = append(out, be)
	}
	// 酿造台
	for pos, bs := range g.brews {
		be := world.BlockEntity{X: pos[0], Y: pos[1], Z: pos[2], ID: "minecraft:brewing_stand"}
		for i := 0; i < 4; i++ {
			if it := bs.slots[i]; it.ID != 0 {
				be.Items = append(be.Items, world.BEItem{ID: it.ID, Count: it.Count, Damage: it.Damage, Slot: int32(i)})
			}
		}
		out = append(out, be)
	}
	return out
}
