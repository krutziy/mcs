package game

// 敌对生物 A* 寻路（XZ 平面 BFS 网格，可站立判定，允许 1 格台阶）。
// 直线可走时走直线；被挡则按 BFS 路径绕行；每 10 刻重算。

import (
	"container/list"

	"mcserver/internal/entity"
)

// pathNode 寻路网格点。
type pathNode struct{ x, z int32 }

// mobPathState 生物的寻路缓存。
type mobPathState struct {
	path   []pathNode
	recalc int32 // 重算倒计时（<=0 触发重算）
}

// walkableAtAt 指定脚部 y 处 (x,z) 可站立。
func (g *Game) walkableAt(dim, x, y, z int32) bool {
	return g.blockAtDim(dim, x, y-1, z) &&
		!g.blockAtDim(dim, x, y, z) && !g.blockAtDim(dim, x, y+1, z)
}

// findPathXZ BFS 找 (sx,sz)→(tx,tz) 的可走路径（±r 网格内，脚部 y 允许 ±1）。
// 返回路径点（不含起点；终点列尽量贴近目标）。找不到返回 nil。
func (g *Game) findPathXZ(dim, sx, sy, sz, tx, tz, r int32) []pathNode {
	if sx < tx-r || sx > tx+r || sz < tz-r || sz > tz+r {
		return nil
	}
	stepOK := func(nx, nz int32) bool {
		return g.walkableAt(dim, nx, sy, nz) || g.walkableAt(dim, nx, sy+1, nz) || g.walkableAt(dim, nx, sy-1, nz)
	}
	start := pathNode{sx, sz}
	goal := pathNode{tx, tz}
	if start == goal {
		return nil
	}
	prev := map[pathNode]pathNode{start: start}
	queue := list.New()
	queue.PushBack(start)
	limit := (2*r + 1) * (2*r + 1)
	for visited := int32(0); queue.Len() > 0 && visited < limit; visited++ {
		cur := queue.Remove(queue.Front()).(pathNode)
		if cur == goal {
			var path []pathNode
			n := cur
			for n != start {
				path = append([]pathNode{n}, path...)
				n = prev[n]
			}
			return path
		}
		for _, d := range [][2]int32{{1, 0}, {-1, 0}, {0, 1}, {0, -1}} {
			np := pathNode{cur.x + d[0], cur.z + d[1]}
			if _, seen := prev[np]; seen {
				continue
			}
			if stepOK(np.x, np.z) {
				prev[np] = cur
				queue.PushBack(np)
			}
		}
	}
	return nil
}

// chaseStep 敌对生物向目标移动一步（直线优先，被挡绕行，台阶跳跃）。
// 写入 m.x/m.z，必要时给 m.vy 跳跃初速。
func (g *Game) chaseStep(m *mob, t *entity.Player) {
	dx, dz := t.X-m.x, t.Z-m.z
	d := sqrtF(dx*dx + dz*dz)
	if d < 0.01 {
		return
	}
	nx, nz := m.x+dx/d*m.kind.speed, m.z+dz/d*m.kind.speed
	feetY := floorI(m.y)
	// 直线可达（当前高度或台阶）
	if g.walkableAt(m.dim, floorI(nx), feetY, floorI(nz)) {
		m.x, m.z = nx, nz
		return
	}
	if g.walkableAt(m.dim, floorI(nx), feetY+1, floorI(nz)) &&
		!g.blockAtDim(m.dim, floorI(nx), feetY+2, floorI(nz)) {
		if m.vy == 0 {
			m.vy = 0.42 // 跳上台阶
		}
		m.x, m.z = nx, nz
		return
	}
	// 绕行：缓存路径
	ps := g.mobPaths[m.eid]
	if ps == nil {
		ps = &mobPathState{}
		g.mobPaths[m.eid] = ps
	}
	ps.recalc--
	if ps.recalc <= 0 || len(ps.path) == 0 {
		ps.recalc = 10
		ps.path = g.findPathXZ(m.dim, floorI(m.x), feetY, floorI(m.z), floorI(t.X), floorI(t.Z), 12)
	}
	if len(ps.path) > 0 {
		wp := ps.path[0]
		wx, wz := float64(wp.x)+0.5, float64(wp.z)+0.5
		wdx, wdz := wx-m.x, wz-m.z
		wd := sqrtF(wdx*wdx + wdz*wdz)
		if wd < 0.3 {
			ps.path = ps.path[1:]
			return
		}
		// 上台阶跳
		if g.walkableAt(m.dim, wp.x, feetY+1, wp.z) && m.vy == 0 {
			m.vy = 0.42
		}
		m.x += wdx / wd * m.kind.speed
		m.z += wdz / wd * m.kind.speed
	}
}

func sqrtF(v float64) float64 {
	if v <= 0 {
		return 0.01
	}
	// 牛顿一次迭代（性能足够、避免 import math 的调用开销差异）
	x := v
	for i := 0; i < 8; i++ {
		x = (x + v/x) / 2
	}
	return x
}
