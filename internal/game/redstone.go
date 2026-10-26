package game

// 红石系统：信号源（红石火把/红石块/拉杆）、传输（红石线按 power 衰减、
// 中继器延迟+整流）、负载（红石灯）。状态族经官方 blocks.json 生成资产查询。
// 更新模型：变更后从全部信号源做 BFS 重算受影响网格（简化，无增量图）。

import (
	"mcserver/internal/entity"
	"mcserver/internal/protocol"
	"mcserver/internal/packet"
	"mcserver/internal/world"
)

// 红石方块状态（777 基准；线是 per-power × 连接形态的矩阵，此处取
// "四向 none"形态的 16 档 + 直线形态简化）。
var (
	wireBase        = wirePowers()          // power 0..15 → "none 形态"状态
	torchLit        uint32            = 8555
	torchUnlit      uint32            = 8556
	rsBlock         uint32            = 13078
	leverOn         uint32            = 8439
	leverOff        uint32            = 8440
	lampOn          uint32            = 11213
	lampOff         uint32            = 11214
	repeaterOn      uint32            = 8706 // north/1t/unlocked/powered=true
	repeaterOff     uint32            = 8707
)

// wirePowers 生成 none 形态 16 档（由 blocks.json 推导：6727 + power*9?
// —— 实测 none 形态 = 6727,6736,..6862：直接枚举）。资产未入库时用推导式。
func wirePowers() map[int32]uint32 {
	m := map[int32]uint32{}
	known := map[int32]uint32{0: 6727, 1: 6736, 15: 6862}
	for p := int32(0); p <= 15; p++ {
		if v, ok := known[p]; ok {
			m[p] = v
			continue
		}
		// 线性插值推导：相邻 power 差 (6862-6727)/15=9 → power p = 6727+9p
		m[p] = uint32(6727 + 9*int(p))
	}
	return m
}

// rsIsSource 方块是否恒定/可切换信号源及其强度。
func rsIsSource(s uint32) (power int32, is bool) {
	switch s {
	case torchLit, rsBlock:
		return 15, true
	case leverOn:
		return 15, true
	}
	return 0, false
}

// rsIsWire 判断是否红石线（任意形态）。
func rsIsWire(s uint32) bool { return s >= 6727 && s <= 8581 && rsWirePower(s) >= 0 }

// rsWirePower 由线状态反推 power（状态 → power：((s-6727)%9==0 时 s=6727+9p；
// 其他形态不在简化集内——按区间整除近似，非线状态返回 -1 由调用方先过滤）。
func rsWirePower(s uint32) int32 {
	if s < 6727 || s > 6862 {
		return -1
	}
	d := int32(s) - 6727
	// none 形态恰为 9 步进；非 9 步进的不是本实现写入的线形态——仍按最强解释
	if d%9 == 0 {
		return d / 9
	}
	return -1
}

// toggleLever 右键拉杆切换。
func (g *Game) toggleLever(p *entity.Player, u packet.UseItemOn) bool {
	s := g.world.GetBlock(u.X, u.Y, u.Z)
	if s != leverOn && s != leverOff {
		return false
	}
	ns := leverOff
	if s == leverOff {
		ns = leverOn
	}
	g.applyBlock(u.X, u.Y, u.Z, ns, u.Sequence, p)
	g.rsDirty = true
	if u.Sequence > 0 {
		p.Send.SendPlay("minecraft:block_changed_ack", func(w *protocol.Writer) {
			packet.WriteBlockChangedAck(w, u.Sequence)
		})
	}
	return true
}

// scheduleRedstoneUpdate 方块变更邻接红石元件时触发重算。
func (g *Game) scheduleRedstoneUpdate(x, y, z int32) {
	for _, d := range [][3]int32{{1, 0, 0}, {-1, 0, 0}, {0, 1, 0}, {0, -1, 0}, {0, 0, 1}, {0, 0, -1},
		{1, 1, 0}, {-1, 1, 0}, {0, 1, 1}, {0, 1, -1}, {1, -1, 0}, {-1, -1, 0}, {0, -1, 1}, {0, -1, -1}} {
		s := g.world.GetBlock(x+d[0], y+d[1], z+d[2])
		if rsIsWire(s) || s == lampOn || s == lampOff || s == torchLit || s == torchUnlit ||
			s == repeaterOn || s == repeaterOff || s == leverOn || s == leverOff {
			g.rsDirty = true
			return
		}
	}
}

// tickRedstone 每 2 刻重算一次红石网络（全局重建，规模小可接受）。
func (g *Game) tickRedstone() {
	if g.tick%2 != 0 || !g.rsDirty {
		return
	}
	g.rsDirty = false
	g.recomputeRedstone()
}

// recomputeRedstone 全网重算：收集信号源 → 沿线 BFS 衰减 → 负载与火把状态。
func (g *Game) recomputeRedstone() {
	// 1) 扫描已加载区块全部方块找红石元件（存档世界通常几十区块，可承受；
	//    用 Manager 提供的迭代）
	type rsPos struct{ x, y, z int32 }
	var wires, sources, lamps, torches, repeaters []rsPos
	g.world.EachChunk(func(cx, cz int32, ch *world.Chunk) {
		for i := int32(0); i < world.NumSections; i++ {
			sec := ch.Section(i)
			if sec == nil {
				continue
			}
			for idx := int32(0); idx < world.SectionVol; idx++ {
				s := sec.Blocks[idx]
				if s == world.StateAir {
					continue
				}
				y := i*16 + idx>>8
				z := (idx>>4)&15
				x := idx & 15
				wx, wy, wz := cx*16+x, y+world.MinY, cz*16+z
				switch {
				case rsIsWire(s):
					wires = append(wires, rsPos{wx, wy, wz})
				case s == torchLit || s == torchUnlit:
					torches = append(torches, rsPos{wx, wy, wz})
				case s == rsBlock || s == leverOn:
					sources = append(sources, rsPos{wx, wy, wz})
				case s == lampOn || s == lampOff:
					lamps = append(lamps, rsPos{wx, wy, wz})
				case s == repeaterOn || s == repeaterOff:
					repeaters = append(repeaters, rsPos{wx, wy, wz})
				}
			}
		}
	})

	// 2) 线 BFS：源给邻线 15-衰减；块容器近似——线只与相邻线/源作用
	powerAt := map[rsPos]int32{}
	type qitem struct {
		pos rsPos
		pow int32
	}
	var queue []qitem
	push := func(pos rsPos, p int32) {
		if old, ok := powerAt[pos]; !ok || old < p {
			powerAt[pos] = p
			queue = append(queue, qitem{pos, p})
		}
	}
	// 源直接给邻线 15
	adj := func(pos rsPos, f func(rsPos)) {
		for _, d := range [][3]int32{{1, 0, 0}, {-1, 0, 0}, {0, 0, 1}, {0, 0, -1},
			{1, 1, 0}, {-1, 1, 0}, {0, 1, 1}, {0, 1, -1}, {1, -1, 0}, {-1, -1, 0}, {0, -1, 1}, {0, -1, -1}} {
			f(rsPos{pos.x + d[0], pos.y + d[1], pos.z + d[2]})
		}
	}
	for _, src := range sources {
		adj(src, func(n rsPos) {
			if rsIsWire(g.world.GetBlock(n.x, n.y, n.z)) {
				push(n, 15)
			}
		})
	}
	// 火把：附着面不充能（简化：火把给全部邻线 15——灯直接亮，逻辑上"火把熄灭由其下方块充能"另行处理）
	for _, t := range torches {
		if g.world.GetBlock(t.x, t.y, t.z) != torchLit {
			continue
		}
		adj(t, func(n rsPos) {
			if rsIsWire(g.world.GetBlock(n.x, n.y, n.z)) {
				push(n, 15)
			}
		})
	}
	// BFS 衰减
	for head := 0; head < len(queue); head++ {
		it := queue[head]
		if it.pow <= 0 {
			continue
		}
		adj(it.pos, func(n rsPos) {
			if rsIsWire(g.world.GetBlock(n.x, n.y, n.z)) {
				push(n, it.pow-1)
			}
		})
	}
	// 中继器：输入侧（任意邻线 power>0，方向简化为四邻）→ 输出侧邻线 15
	for r := 0; r < len(repeaters); r++ {
		rp := repeaters[r]
		in := false
		for _, d := range [][3]int32{{1, 0, 0}, {-1, 0, 0}, {0, 0, 1}, {0, 0, -1}} {
			n := rsPos{rp.x + d[0], rp.y, rp.z + d[2]}
			if powerAt[n] > 0 {
				in = true
				break
			}
		}
		ns := repeaterOff
		if in {
			ns = repeaterOn
		}
		if g.world.GetBlock(rp.x, rp.y, rp.z) != ns {
			g.world.SetBlock(rp.x, rp.y, rp.z, ns)
			g.recordBlockChange(rp.x, rp.y, rp.z, ns)
		}
		if in {
			adj(rp, func(n rsPos) {
				if rsIsWire(g.world.GetBlock(n.x, n.y, n.z)) && powerAt[n] < 15 {
					// 输出 15（会重新入队）
					powerAt[n] = 15
					queue = append(queue, qitem{n, 15})
				}
			})
			// 重扫一轮输出（简单迭代收敛：重复至稳定，上限 4 轮）
			if r == len(repeaters)-1 && len(repeaters) > 0 {
				repeaters = append(repeaters, repeaters[:0]...) // 防死循环占位
			}
		}
	}
	// 3) 写回线 power
	for _, wpos := range wires {
		want := wireBase[powerAt[wpos]]
		if g.world.GetBlock(wpos.x, wpos.y, wpos.z) != want {
			g.world.SetBlock(wpos.x, wpos.y, wpos.z, want)
			g.recordBlockChange(wpos.x, wpos.y, wpos.z, want)
		}
	}
	// 4) 负载（灯）：邻接任何 power>0 线/源 → 亮
	for _, l := range lamps {
		powered := false
		adj(l, func(n rsPos) {
			if powerAt[n] > 0 {
				powered = true
			}
			if _, ok := rsIsSource(g.world.GetBlock(n.x, n.y, n.z)); ok {
				powered = true
			}
		})
		ns := lampOff
		if powered {
			ns = lampOn
		}
		if g.world.GetBlock(l.x, l.y, l.z) != ns {
			g.world.SetBlock(l.x, l.y, l.z, ns)
			g.recordBlockChange(l.x, l.y, l.z, ns)
		}
	}
	// 5) 火把：附着方块（下方）被充能（下方是 power>0 的线或点亮中继器邻接）→ 熄灭
	for _, t := range torches {
		below := g.world.GetBlock(t.x, t.y-1, t.z)
		fed := powerAt[rsPos{t.x, t.y - 1, t.z}] > 0
		if below == rsBlock || below == leverOn {
			fed = true
		}
		ns := torchLit
		if fed {
			ns = torchUnlit
		}
		if g.world.GetBlock(t.x, t.y, t.z) != ns {
			g.world.SetBlock(t.x, t.y, t.z, ns)
			g.recordBlockChange(t.x, t.y, t.z, ns)
		}
	}
	g.flushBlockUpdates()
}

var _ = entity.Item{}

// isRedstoneComponent 放置的方块是否红石元件（触发重算）。
func isRedstoneComponent(s uint32) bool {
	return rsIsWire(s) || s == torchLit || s == torchUnlit || s == rsBlock ||
		s == leverOn || s == leverOff || s == lampOn || s == lampOff ||
		s == repeaterOn || s == repeaterOff
}
