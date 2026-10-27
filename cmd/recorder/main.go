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
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/x509"

	"mcserver/internal/conn"
	"mcserver/internal/packet"
	"mcserver/internal/protocol"
	"mcserver/internal/server"
	"mcserver/internal/version"
)

var (
	addr     = flag.String("addr", "127.0.0.1:25566", "目标服务端地址")
	outDir   = flag.String("out", ".tools/captures", "抓包输出目录")
	username = flag.String("user", "Recorder", "登录用户名")
	duration = flag.Duration("t", 8*time.Second, "游戏阶段录制时长")
	verName  = flag.String("version", "", "协议版本名（如 26.3 / 1.21.11），默认最新")
	online   = flag.Bool("online", false, "回应服务器加密握手（正版模式对照；无正版会话，hasJoined 必然失败）")
	rsaPad   = flag.String("rsa-pad", "pkcs1", "key 包 RSA 填充：pkcs1 | oaep-sha1 | oaep-sha256（官方 1.21.11~26.3 实测为 pkcs1）")
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
		case c.State() == conn.StateLogin && name == "minecraft:hello":
			if !*online {
				log.Fatalf("服务器要求加密（正版模式），当前未开启 -online")
			}
			handleEncryptionRequest(c, suite, body, *username)
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
func recordPlay(c *conn.Conn, suite *version.Suite, proto version.Protocol, seq *int) {
	deadline := time.Now().Add(*duration)
	var pendingTeleport int32 = -1
	walk := os.Getenv("WALK") == "1"
	var x, y, z = 8.0, -60.0, 8.0
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

// handleEncryptionRequest 回应服务器的加密请求：生成共享密钥并用服务器
// RSA 公钥加密回传（minecraft:key），发出后立即启用 AES-CFB8——与客户端
// 约定一致，此后双向全密文。最后打印服务器将用于 hasJoined 的 serverId
// 供对照（recorder 无正版会话，正版验证止步于该请求的构造）。
func handleEncryptionRequest(c *conn.Conn, s *version.Suite, body []byte, user string) {
	r := protocol.NewReader(body)
	req := packet.ReadEncryptionRequest(r)
	if r.Err != nil { // 兼容无 ShouldAuthenticate 尾字段的旧格式
		r = protocol.NewReader(body)
		req = packet.EncryptionRequest{ServerID: r.String(), PublicKey: r.ByteArray(), VerifyToken: r.ByteArray()}
		if r.Err != nil {
			log.Fatalf("解析 minecraft:hello 失败: %v", r.Err)
		}
	}
	pubAny, err := x509.ParsePKIXPublicKey(req.PublicKey)
	if err != nil {
		log.Fatalf("解析服务器公钥失败: %v", err)
	}
	pub, ok := pubAny.(*rsa.PublicKey)
	if !ok {
		log.Fatal("服务器公钥不是 RSA")
	}
	secret := make([]byte, 16)
	if _, err := rand.Read(secret); err != nil {
		log.Fatal(err)
	}
	encSecret, err := rsaEncrypt(pub, secret, *rsaPad)
	if err != nil {
		log.Fatalf("加密共享密钥失败: %v", err)
	}
	encToken, err := rsaEncrypt(pub, req.VerifyToken, *rsaPad)
	if err != nil {
		log.Fatalf("加密验证令牌失败: %v", err)
	}
	c.WritePacket(s.MustID("login", "serverbound", "minecraft:key"), func(w *protocol.Writer) {
		packet.WriteEncryptionResponse(w, packet.EncryptionResponse{SharedSecret: encSecret, VerifyToken: encToken})
	})
	if err := c.SetEncryption(secret); err != nil {
		log.Fatalf("启用加密失败: %v", err)
	}
	serverID := server.AuthHash(req.ServerID, secret, req.PublicKey)
	log.Printf("加密已启用；服务器侧将发起 hasJoined (username=%s serverId=%s)", user, serverID)
}

// rsaEncrypt 按 -rsa-pad 指定的填充加密（对照不同服务端实现用）。
func rsaEncrypt(pub *rsa.PublicKey, data []byte, pad string) ([]byte, error) {
	switch pad {
	case "pkcs1":
		return rsa.EncryptPKCS1v15(rand.Reader, pub, data)
	case "oaep-sha256":
		return rsa.EncryptOAEP(sha256.New(), rand.Reader, pub, data, nil)
	default: // oaep-sha1：Java "RSA/ECB/OAEPWithSHA-1AndMGF1Padding" 的等价
		return rsa.EncryptOAEP(sha1.New(), rand.Reader, pub, data, nil)
	}
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
