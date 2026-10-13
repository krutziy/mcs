// recorder 是协议录制客户端：连接一个真实服务端（默认本地官方 26.1），
// 完整走完 登录→配置→游戏 流程，把每个收到的包按官方包名保存为原始字节文件，
// 作为实现服务端时的线格式真值来源。
package main

import (
	"encoding/hex"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"time"

	"crypto/md5"

	"mcserver/internal/conn"
	"mcserver/internal/packet"
	"mcserver/internal/protocol"
	"mcserver/internal/version"
)

var (
	addr       = flag.String("addr", "127.0.0.1:25566", "目标服务端地址")
	outDir     = flag.String("out", ".tools/captures", "抓包输出目录")
	username   = flag.String("user", "Recorder", "登录用户名")
	duration   = flag.Duration("t", 8*time.Second, "游戏阶段录制时长")
	verName    = flag.String("version", "", "协议版本名（如 26.3 / 1.21.11），默认最新")
)

// offlineUUID 按离线模式规则（UUID v3 of "OfflinePlayer:<name>"）派生玩家 UUID。
func offlineUUID(name string) protocol.UUID {
	sum := md5.Sum([]byte("OfflinePlayer:" + name))
	sum[6] = (sum[6] & 0x0f) | 0x30 // version 3
	sum[8] = (sum[8] & 0x3f) | 0x80 // variant
	var u protocol.UUID
	copy(u[:], sum[:])
	return u
}

func main() {
	flag.Parse()
	ver := version.Latest()
	if *verName != "" {
		p, err := version.Parse(*verName)
		if err != nil {
			log.Fatal(err)
		}
		for _, v := range version.All() {
			if v.Protocol == p {
				ver = v
			}
		}
	}
	suite := version.SuiteFor(ver.Protocol)
	if suite == nil {
		log.Fatal("无可用版本套件")
	}
	if err := os.MkdirAll(*outDir, 0o755); err != nil {
		log.Fatal(err)
	}

	tcp, err := net.DialTimeout("tcp", *addr, 5*time.Second)
	if err != nil {
		log.Fatalf("连接失败: %v", err)
	}
	c := conn.New(tapConn{tcp})
	log.Printf("已连接 %s", *addr)

	// --- 握手 ---
	host, portStr, _ := net.SplitHostPort(*addr)
	port := 25566
	fmt.Sscanf(portStr, "%d", &port)
	c.WritePacket(suite.MustID("handshake", "serverbound", "minecraft:intention"), func(w *protocol.Writer) {
		w.VarInt(int32(ver.Protocol))
		w.String(host)
		w.Uint16(uint16(port))
		w.VarInt(2) // 下一个状态：登录
	})
	c.SetState(conn.StateLogin)

	// --- 登录 ---
	c.WritePacket(suite.MustID("login", "serverbound", "minecraft:hello"), func(w *protocol.Writer) {
		w.String(*username)
		w.UUID(offlineUUID(*username))
	})

	seq := 0
	// --- 登录/配置阶段 ---
	for {
		id, r, err := c.ReadPacket()
		if err != nil {
			log.Fatalf("读取失败(登录/配置): %v", err)
		}
		stateName := "login"
		if c.State() == conn.StateConfiguration {
			stateName = "configuration"
		}
		name := lookup(suite, stateName, id)
		body := r.Rest()
		log.Printf("[%s] id=0x%02x 长度=%d", name, id, len(body))
		savePacket(seq, "preplay", name, id, body)
		seq++
		r = protocol.NewReader(body)

		switch {
		case c.State() == conn.StateLogin && name == "minecraft:login_compression":
			threshold := r.VarInt()
			log.Printf("启用压缩, 阈值=%d", threshold)
			c.SetCompression(threshold)
		case c.State() == conn.StateLogin && name == "minecraft:login_finished":
			sendLoginAck(c, suite)
			c.SetState(conn.StateConfiguration)
		case c.State() == conn.StateConfiguration && name == "minecraft:select_known_packs":
			// 回显服务器给出的第一个包；未提供则回空列表
			count := int(r.VarInt())
			firstNS, firstID, firstVer := "", "", ""
			if count > 0 {
				firstNS, firstID, firstVer = r.String(), r.String(), r.String()
				for i := 1; i < count; i++ { // 丢弃其余
					_, _, _ = r.String(), r.String(), r.String()
				}
			}
			sendKnownPacks(c, suite, count, firstNS, firstID, firstVer)
		case c.State() == conn.StateConfiguration && name == "minecraft:keep_alive":
			id64 := r.Int64()
			sendKeepAliveConf(c, suite, id64)
		case c.State() == conn.StateConfiguration && name == "minecraft:ping":
			payload := r.Int64()
			sendPongConf(c, suite, payload)
		case c.State() == conn.StateConfiguration && name == "minecraft:finish_configuration":
			sendFinishConf(c, suite)
			c.SetState(conn.StatePlay)
			log.Printf("进入游戏状态，录制 %v", *duration)
			recordPlay(c, suite, ver.Protocol, &seq)
			return
		}
	}
}

// recordPlay 录制游戏阶段的包，并应答 keepalive/ping/传送确认。
// 环境变量脚本动作（进入游戏 2 秒后执行，用于方块交互抓包）：
//
//	DIG=1        挖脚下方块：发 player_action(start)；SURVIVAL_WAIT=1 时
//	             再等 1.5 秒发 finish（对官方生存服）
//	PLACE=1      放置：先发 set_creative_mode_slot(快捷栏0, PLACE_ITEM_ID)
//	             与 set_carried_item(0)，再对挖掉的洞底发 use_item_on
//	PLACE_ITEM_ID=1  放置用的物品网络 ID（默认 1 = 石头，777 空间）
func recordPlay(c *conn.Conn, suite *version.Suite, proto version.Protocol, seq *int) {
	deadline := time.Now().Add(*duration)
	var pendingTeleport int32 = -1
	walk := os.Getenv("WALK") == "1"
	dig := os.Getenv("DIG") == "1"
	place := os.Getenv("PLACE") == "1"
	survivalWait := os.Getenv("SURVIVAL_WAIT") == "1"
	placeItem := int32(1)
	if v := os.Getenv("PLACE_ITEM_ID"); v != "" {
		n := 0
		fmt.Sscanf(v, "%d", &n)
		if n > 0 {
			placeItem = int32(n)
		}
	}
	var x, y, z = 8.0, -60.0, 8.0
	var acted bool
	nextSeq := int32(0)
	// 26.x 真实客户端进入游戏即声明 player_loaded，并每刻发 client_tick_end；
	// 官方服缺了它们会丢弃部分输入（挖掘 start 被无视 → finish 时 mismatch）
	if id, ok := suite.ID("play", "serverbound", "minecraft:player_loaded"); ok {
		c.WritePacket(id, func(w *protocol.Writer) {})
	}
	tickEndID, tickEndOK := suite.ID("play", "serverbound", "minecraft:client_tick_end")
	stopTicks := make(chan struct{})
	go func() {
		if !tickEndOK {
			return
		}
		t := time.NewTicker(50 * time.Millisecond)
		defer t.Stop()
		for {
			select {
			case <-stopTicks:
				return
			case <-t.C:
				c.WritePacket(tickEndID, func(w *protocol.Writer) {})
			}
		}
	}()
	defer close(stopTicks)
	// DIG_OFF=x,z：挖掘目标相对出生点的格子偏移（对照实验用）
	offX, offZ := int32(0), int32(0)
	if v := os.Getenv("DIG_OFF"); v != "" {
		fmt.Sscanf(v, "%d,%d", &offX, &offZ)
	}
	// DIG_ABS=x,y,z：直接指定挖掘目标（绝对坐标，覆盖偏移）
	var digAbs [3]int32
	haveAbs := false
	if v := os.Getenv("DIG_ABS"); v != "" {
		if n, _ := fmt.Sscanf(v, "%d,%d,%d", &digAbs[0], &digAbs[1], &digAbs[2]); n == 3 {
			haveAbs = true
		}
	}
	for time.Now().Before(deadline) {
		if walk {
			x += 0.35
			c.WritePacket(suite.MustID("play", "serverbound", "minecraft:move_player_pos"), func(w *protocol.Writer) {
				w.Double(x)
				w.Double(y)
				w.Double(z)
				w.Uint8(1) // onGround
			})
			time.Sleep(100 * time.Millisecond)
		}
		// 方块交互脚本：位置由 player_position 更新，等 2 秒让区块加载完成
		if (dig || place) && !acted && time.Until(deadline) < *duration-2*time.Second {
			acted = true
			bx, by, bz := int32(x)+offX, int32(y)-1, int32(z)+offZ
			if haveAbs {
				bx, by, bz = digAbs[0], digAbs[1], digAbs[2]
			}
			nextSeq++
			seqID := nextSeq
			if swingID, ok := suite.ID("play", "serverbound", "minecraft:punch"); ok {
				c.WritePacket(swingID, func(w *protocol.Writer) {}) // 26.3: 空载荷
			}
			c.WritePacket(suite.MustID("play", "serverbound", "minecraft:player_action"), func(w *protocol.Writer) {
				w.VarInt(0) // start digging
				w.Position(protocol.Position{X: bx, Y: by, Z: bz})
				w.Int8(1)   // face: top
				w.VarInt(seqID)
			})
			log.Printf("已发送挖掘 start seq=%d pos=(%d,%d,%d)", seqID, bx, by, bz)
			if survivalWait {
				waitMs := 3000
				if v := os.Getenv("DIG_WAIT_MS"); v != "" {
					fmt.Sscanf(v, "%d", &waitMs)
				}
				time.Sleep(time.Duration(waitMs) * time.Millisecond)
				nextSeq++
				seqID = nextSeq
				c.WritePacket(suite.MustID("play", "serverbound", "minecraft:player_action"), func(w *protocol.Writer) {
					w.VarInt(2) // finish digging
					w.Position(protocol.Position{X: bx, Y: by, Z: bz})
					w.Int8(1)
					w.VarInt(seqID)
				})
				log.Printf("已发送挖掘 finish seq=%d pos=(%d,%d,%d)", seqID, bx, by, bz)
			}
			if place {
				time.Sleep(500 * time.Millisecond)
				// 石头放进快捷栏第 0 格并选中
				c.WritePacket(suite.MustID("play", "serverbound", "minecraft:set_creative_mode_slot"), func(w *protocol.Writer) {
					w.Int16(36) // 快捷栏槽 0
					packet.WriteCreativeSlotItem(w, placeItem, 1)
				})
				c.WritePacket(suite.MustID("play", "serverbound", "minecraft:set_carried_item"), func(w *protocol.Writer) {
					w.Int16(0)
				})
				nextSeq++
				seqID = nextSeq
				px, py, pz := bx, by-1, bz // 点击洞底的顶面 → 放回挖掉的位置
				c.WritePacket(suite.MustID("play", "serverbound", "minecraft:use_item_on"), func(w *protocol.Writer) {
					w.VarInt(0) // main hand
					w.Position(protocol.Position{X: px, Y: py, Z: pz})
					w.VarInt(1) // face: top
					w.Float(0.5)
					w.Float(1.0)
					w.Float(0.5)
					w.Bool(false) // insideBlock
					w.Bool(false) // worldBorderHit
					w.VarInt(seqID)
				})
				log.Printf("已发送放置 seq=%d 点击=(%d,%d,%d) 物品ID=%d", seqID, px, py, pz, placeItem)
			}
		}
		_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
		id, r, err := c.ReadPacket()
		if err != nil {
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				continue
			}
			log.Printf("读取结束: %v", err)
			return
		}
		name := lookup(suite, "play", id)
		body := r.Rest()
		log.Printf("[play] %s id=0x%02x 长度=%d", name, id, len(body))
		savePacket(*seq, "play", name, id, body)
		*seq++
		r = protocol.NewReader(body)

		switch name {
		case "minecraft:keep_alive":
			id64 := r.Int64()
			c.WritePacket(suite.MustID("play", "serverbound", "minecraft:keep_alive"), func(w *protocol.Writer) {
				w.Int64(id64)
			})
		case "minecraft:ping":
			payload := r.Int64()
			c.WritePacket(suite.MustID("play", "serverbound", "minecraft:pong"), func(w *protocol.Writer) {
				w.Int64(payload)
			})
		case "minecraft:player_position":
			// 26.1+: tid + pos(3×f64) + vel(3×f64) + yaw/pitch(f32) + flags(4B)
			teleportID := r.VarInt()
			px, py, pz := r.Double(), r.Double(), r.Double()
			_ = r.FixedBytes(24) // 速度增量
			yaw, pitch := r.Float(), r.Float()
			_ = r.FixedBytes(4) // flags
			x, y, z = px, py, pz
			pendingTeleport = teleportID
			echoPos := proto >= 777 // 26.3 起确认包回显位置与朝向
			c.WritePacket(suite.MustID("play", "serverbound", "minecraft:accept_teleportation"), func(w *protocol.Writer) {
				w.VarInt(pendingTeleport)
				if echoPos {
					w.Double(px)
					w.Double(py)
					w.Double(pz)
					w.Float(yaw)
					w.Float(pitch)
				}
			})
			log.Printf("已确认传送 %d pos=(%.1f,%.1f,%.1f)", teleportID, px, py, pz)
		}
	}
	log.Printf("录制完成，共 %d 个包", *seq)
}

func lookup(s *version.Suite, state string, id int32) string {
	for name, pid := range s.IDs[state]["clientbound"] {
		if pid == id {
			return name
		}
	}
	return fmt.Sprintf("unknown_0x%02x", id)
}

func savePacket(seq int, phase, name string, id int32, body []byte) {
	fname := filepath.Join(*outDir, fmt.Sprintf("%03d_%s_%s.bin", seq, phase, sanitize(name)))
	if err := os.WriteFile(fname, body, 0o644); err != nil {
		log.Printf("保存失败 %s: %v", fname, err)
		return
	}
	preview := body
	if len(preview) > 64 {
		preview = preview[:64]
	}
	if len(body) > 0 {
		log.Printf("    %s (%d bytes) %s...", fname, len(body), hex.EncodeToString(preview)[:min(48, len(hex.EncodeToString(preview)))])
	}
}

func sanitize(s string) string {
	out := make([]rune, 0, len(s))
	for _, r := range s {
		if r == ':' || r == '/' {
			out = append(out, '_')
		} else {
			out = append(out, r)
		}
	}
	return string(out)
}

func min(a, b int) int { if a < b { return a }; return b }

func sendLoginAck(c *conn.Conn, s *version.Suite) {
	c.WritePacket(s.MustID("login", "serverbound", "minecraft:login_acknowledged"), func(w *protocol.Writer) {})
}

func sendKnownPacks(c *conn.Conn, s *version.Suite, count int, ns, id, ver string) {
	c.WritePacket(s.MustID("configuration", "serverbound", "minecraft:select_known_packs"), func(w *protocol.Writer) {
		n := 0
		if count > 0 {
			n = 1
		}
		w.VarInt(int32(n))
		if n == 1 {
			w.String(ns)
			w.String(id)
			w.String(ver)
		}
	})
}

func sendKeepAliveConf(c *conn.Conn, s *version.Suite, id int64) {
	c.WritePacket(s.MustID("configuration", "serverbound", "minecraft:keep_alive"), func(w *protocol.Writer) {
		w.Int64(id)
	})
}

func sendPongConf(c *conn.Conn, s *version.Suite, payload int64) {
	c.WritePacket(s.MustID("configuration", "serverbound", "minecraft:pong"), func(w *protocol.Writer) {
		w.Int64(payload)
	})
}

func sendFinishConf(c *conn.Conn, s *version.Suite) {
	c.WritePacket(s.MustID("configuration", "serverbound", "minecraft:finish_configuration"), func(w *protocol.Writer) {})
}
