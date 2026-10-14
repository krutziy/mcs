package game

// 随身 2x2 合成：窗口 0 的槽 1-4 为网格、槽 0 为结果。客户端本地预测
// 结果图标，服务端在点击结果槽时验证配方（官方 jar recipe 数据生成的
// recipes 资产，无标签子集）并应用：结果入光标、材料各扣 1。

import (
	"strconv"
	"strings"

	"mcserver/internal/assets"
	"mcserver/internal/entity"
)

type craftRecipe struct {
	grid   [3][3]string // 物品短名，"." = 空
	result string
	count  int32
}

// loadRecipes 解析 recipes_<proto>.txt（"结果 数量 r1/r2/r3"，格空格分隔）。
func loadRecipes(proto int32) []craftRecipe {
	b, err := assets.RecipeFile(proto)
	if err != nil {
		return nil
	}
	var out []craftRecipe
	for _, line := range strings.Split(string(b), "\n") {
		parts := strings.SplitN(line, " ", 3)
		if len(parts) != 3 {
			continue
		}
		cnt, err := strconv.Atoi(parts[1])
		if err != nil || cnt <= 0 {
			continue
		}
		rows := strings.Split(parts[2], "/")
		if len(rows) != 3 {
			continue
		}
		var rec craftRecipe
		ok := true
		for r, row := range rows {
			cells := strings.Split(row, " ")
			if len(cells) != 3 {
				ok = false
				break
			}
			copy(rec.grid[r][:], cells)
		}
		if !ok {
			continue
		}
		rec.result, rec.count = parts[0], int32(cnt)
		out = append(out, rec)
	}
	return out
}

// matchRecipe 匹配玩家 2x2 网格（槽 1-4：左上/右上/左下/右下），返回结果。
func (g *Game) matchRecipe(p *entity.Player) (string, int32) {
	grid := [2][2]string{}
	for r := 0; r < 2; r++ {
		for c := 0; c < 2; c++ {
			if id := p.Slots[1+r*2+c].ID; id != 0 {
				grid[r][c] = g.itemNameOf(p, id)
			}
		}
	}
	for _, rec := range g.recipes {
		if fits2x2(rec.grid, grid) {
			return rec.result, rec.count
		}
	}
	return "", 0
}

// fits2x2 判断 3x3 配方能否由 2x2 网格满足（双方有效格集合一致）。
func fits2x2(rec [3][3]string, grid [2][2]string) bool {
	// 配方的有效格集合
	recCells := map[[2]int]string{}
	for r := 0; r < 3; r++ {
		for c := 0; c < 3; c++ {
			if rec[r][c] != "." {
				recCells[[2]int{r, c}] = rec[r][c]
			}
		}
	}
	gridCells := map[[2]int]string{}
	for r := 0; r < 2; r++ {
		for c := 0; c < 2; c++ {
			if grid[r][c] != "" {
				gridCells[[2]int{r, c}] = grid[r][c]
			}
		}
	}
	if len(recCells) == 0 || len(recCells) != len(gridCells) {
		return false
	}
	// 配方平移到网格原点后逐格比对
	minR, minC := 3, 3
	for pos := range recCells {
		if pos[0] < minR {
			minR = pos[0]
		}
		if pos[1] < minC {
			minC = pos[1]
		}
	}
	shifted := map[[2]int]string{}
	for pos, v := range recCells {
		np := [2]int{pos[0] - minR, pos[1] - minC}
		if np[0] > 1 || np[1] > 1 {
			return false // 超出 2x2
		}
		shifted[np] = v
	}
	for pos, v := range gridCells {
		if shifted[pos] != v {
			return false
		}
	}
	return true
}

// takeCraftResult 点击结果槽 0：匹配则结果入光标（或叠加），材料各扣 1。
func (g *Game) takeCraftResult(p *entity.Player) bool {
	name, count := g.matchRecipe(p)
	if name == "" {
		return false
	}
	resultID, ok := assets.ItemIDs(g.playerProto(p))[name]
	if !ok {
		return false
	}
	cur := p.Cursor
	if cur.ID != 0 && (cur.ID != resultID || cur.Count+count > itemMaxStack) {
		return false
	}
	if cur.ID == 0 {
		p.Cursor = entity.Item{ID: resultID, Count: count}
	} else {
		p.Cursor.Count += count
	}
	for i := int32(1); i <= 4; i++ {
		s := &p.Slots[i]
		if s.ID != 0 {
			s.Count--
			if s.Count <= 0 {
				*s = entity.Item{}
			}
		}
	}
	return true
}
