// Package server 实现 TCP 监听与协议状态机：
// Handshaking → Status(服务器列表) / Login(离线登录) → Configuration → Play。
package server

import (
	"bytes"
	"crypto/md5"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha1"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"strings"
	"time"

	"mcserver/internal/assets"
	"mcserver/internal/conn"
	"mcserver/internal/game"
	"mcserver/internal/packet"
	"mcserver/internal/protocol"
	"mcserver/internal/version"
)

// Config 是服务端配置（config.json）。
type Config struct {
	Port         int         `json:"port"`
	MOTD         string      `json:"motd"`
	MaxPlayers   int32       `json:"max_players"`
	ViewDistance int32       `json:"view_distance"`
	Spawn        [3]float64  `json:"spawn"`
	GameMode     string      `json:"gamemode"`
	OnlineMode   bool        `json:"online_mode"`
	World        WorldConfig `json:"world"`
}

// WorldConfig 世界配置。
type WorldConfig struct {
	Type string `json:"type"` // flat | noise
	Seed int64  `json:"seed"`
}

// DefaultConfig 返回默认配置。
func DefaultConfig() Config {
	return Config{
		Port:         25565,
		MOTD:         "§aGo MC 框架 §7| 小游戏定制服务端",
		MaxPlayers:   20,
		ViewDistance: 3,
		Spawn:        [3]float64{8, -60, 8},
		GameMode:     "creative",
		World:        WorldConfig{Type: "void", Seed: 20261010},
	}
}

// Server 是 TCP 服务端。
type Server struct {
	cfg   Config
	ver   version.Version
	suite *version.Suite
	game  *game.Game
	ln    net.Listener
	auth  *authClient // 正版验证客户端，nil 时按默认（测试可打桩）
}

// New 创建服务端。
func New(cfg Config) (*Server, error) {
	ver := version.Latest()
	suite := version.SuiteFor(ver.Protocol)
	if suite == nil {
		return nil, fmt.Errorf("无可用协议套件")
	}
	mode := packet.ModeCreative
	switch strings.ToLower(cfg.GameMode) {
	case "survival", "0":
		mode = packet.ModeSurvival
	case "adventure", "2":
		mode = packet.ModeAdventure
	case "spectator", "3":
		mode = packet.ModeSpectator
	}
	g := game.New(game.Config{
		SpawnX: cfg.Spawn[0], SpawnY: cfg.Spawn[1], SpawnZ: cfg.Spawn[2],
		ViewDistance: cfg.ViewDistance,
		GameMode:     mode,
		MOTD:         cfg.MOTD,
		MaxPlayers:   cfg.MaxPlayers,
		WorldType:    cfg.World.Type,
		WorldSeed:    cfg.World.Seed,
	}, suite)
	return &Server{cfg: cfg, ver: ver, suite: suite, game: g}, nil
}

// Game 返回游戏实例（供扩展模块注册事件）。
func (s *Server) Game() *game.Game { return s.game }

// ListenAndServe 开始监听并阻塞接受连接。
func (s *Server) ListenAndServe() error {
	ln, err := net.Listen("tcp", fmt.Sprintf(":%d", s.cfg.Port))
	if err != nil {
		return err
	}
	s.ln = ln
	log.Printf("[server] 监听 :%d，协议 %s (%d)，按 Ctrl+C 停止", s.cfg.Port, s.ver.Name, s.ver.Protocol)
	for {
		tcp, err := ln.Accept()
		if err != nil {
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				continue
			}
			return err
		}
		go s.handleConn(tcp)
	}
}

// handleConn 处理一条新连接的握手与状态分派。
func (s *Server) handleConn(tcp net.Conn) {
	c := conn.New(tcp)

	// --- 握手 ---
	id, r, err := c.ReadPacket()
	if err != nil {
		c.Close()
		return
	}
	if id != s.suite.MustID("handshake", "serverbound", "minecraft:intention") {
		c.Close()
		return
	}
	protoVer := r.VarInt()
	_ = r.String() // server address（忽略）
	_ = r.Uint16() // port
	nextState := r.VarInt()

	// 多版本：握手协议号决定本次连接使用的版本套件
	ver, ok := version.Lookup(version.Protocol(protoVer))
	if !ok {
		if nextState == 1 {
			s.handleStatus(c, version.Version{Protocol: version.Protocol(protoVer)}, true)
		} else {
			c.SetState(conn.StateLogin)
			s.rejectLogin(c, protoVer)
		}
		c.Close()
		return
	}
	suite := version.SuiteFor(ver.Protocol)

	switch nextState {
	case 1:
		c.SetState(conn.StateStatus)
		s.handleStatus(c, ver, false)
		c.Close()
	case 2:
		c.SetState(conn.StateLogin)
		s.handleLogin(c, ver, suite)
	}
}

// statusResponse 是服务器列表 JSON。
type statusResponse struct {
	Version struct {
		Name     string `json:"name"`
		Protocol int32  `json:"protocol"`
	} `json:"version"`
	Players struct {
		Max    int32 `json:"max"`
		Online int32 `json:"online"`
		Sample []any `json:"sample"`
	} `json:"players"`
	Description struct {
		Text string `json:"text"`
	} `json:"description"`
	Favicon string `json:"favicon,omitempty"`
}

// handleStatus 处理服务器列表 ping。
func (s *Server) handleStatus(c *conn.Conn, ver version.Version, wrongVersion bool) {
	id, r, err := c.ReadPacket()
	if err != nil {
		return
	}
	switch id {
	case s.suite.MustID("status", "serverbound", "minecraft:status_request"):
		resp := statusResponse{}
		resp.Version.Name = ver.Name + " / 支持 " + s.versionsString()
		if wrongVersion {
			resp.Version.Protocol = -1 // 触发客户端版本不匹配提示
		} else {
			resp.Version.Protocol = int32(ver.Protocol)
		}
		resp.Players.Max = s.cfg.MaxPlayers
		resp.Players.Online = int32(0) // 由状态查询补充
		resp.Description.Text = s.cfg.MOTD
		data, _ := json.Marshal(resp)
		c.WritePacket(s.suite.MustID("status", "clientbound", "minecraft:status_response"), func(w *protocol.Writer) {
			w.String(string(data))
		})
	case s.suite.MustID("status", "serverbound", "minecraft:ping_request"):
		timeNs := r.Int64()
		c.WritePacket(s.suite.MustID("status", "clientbound", "minecraft:pong_response"), func(w *protocol.Writer) {
			packet.WritePing(w, packet.PingPayload{Time: timeNs})
		})
	}
	_ = r
}

// versionsString 返回受支持版本列表（状态响应用）。
func (s *Server) versionsString() string {
	names := ""
	for _, v := range version.All() {
		if names != "" {
			names += " / "
		}
		names += v.Name
	}
	return names
}

// rejectLogin 拒绝不支持的协议版本。
func (s *Server) rejectLogin(c *conn.Conn, got int32) {
	msg := fmt.Sprintf(`{"text":"不支持的协议版本 %d，本服运行 %s (%d)"}`, got, s.ver.Name, s.ver.Protocol)
	c.WritePacket(s.suite.MustID("login", "clientbound", "minecraft:login_disconnect"), func(w *protocol.Writer) {
		packet.WriteDisconnectLogin(w, msg)
	})
}

// handleLogin 处理离线登录 → 配置 → 进入游戏。
func (s *Server) handleLogin(c *conn.Conn, ver version.Version, suite *version.Suite) {
	id, r, err := c.ReadPacket()
	if err != nil {
		c.Close()
		return
	}
	if id != suite.MustID("login", "serverbound", "minecraft:hello") {
		c.Close()
		return
	}
	start := packet.ReadLoginStart(r)
	if r.Err != nil {
		c.Close()
		return
	}
	if len(start.Username) < 3 || len(start.Username) > 16 {
		s.rejectLogin(c, int32(s.ver.Protocol))
		return
	}
	name := start.Username
	uuid := offlineUUID(name)
	var props []packet.Property
	if s.cfg.OnlineMode {
		var ok bool
		uuid, name, props, ok = s.handleOnlineLogin(c, suite, start)
		if !ok {
			return // 失败原因已在内部写出并断开
		}
	}

	// 启用压缩（先发压缩包再发成功包，顺序不能反）
	c.WritePacket(suite.MustID("login", "clientbound", "minecraft:login_compression"), func(w *protocol.Writer) {
		packet.WriteSetCompression(w, 256)
	})
	c.SetCompression(256)

	c.WritePacket(suite.MustID("login", "clientbound", "minecraft:login_finished"), func(w *protocol.Writer) {
		packet.WriteLoginFinishedWithProperties(w, uuid, name, props)
	})

	// 等待 login_acknowledged
	if !awaitPacket(c, suite.MustID("login", "serverbound", "minecraft:login_acknowledged")) {
		c.Close()
		return
	}
	c.SetState(conn.StateConfiguration)
	s.handleConfiguration(c, ver, suite, name, uuid)
}

// handleOnlineLogin 执行正版登录：加密握手 → Mojang hasJoined 验证。
// 成功时返回正版 UUID、规范化用户名与皮肤属性；失败时向客户端写明原因
// 并断开（返回 ok=false）。
func (s *Server) handleOnlineLogin(c *conn.Conn, suite *version.Suite, start packet.LoginStart) (protocol.UUID, string, []packet.Property, bool) {
	priv, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		s.rejectLoginPacket(c, suite, "服务器密钥生成失败，请稍后重试")
		c.Close()
		return protocol.UUID{}, "", nil, false
	}
	pubDER, err := x509.MarshalPKIXPublicKey(&priv.PublicKey)
	if err != nil {
		s.rejectLoginPacket(c, suite, "服务器密钥编码失败，请稍后重试")
		c.Close()
		return protocol.UUID{}, "", nil, false
	}
	verifyToken := make([]byte, 16)
	if _, err := rand.Read(verifyToken); err != nil {
		s.rejectLoginPacket(c, suite, "服务器随机数生成失败，请稍后重试")
		c.Close()
		return protocol.UUID{}, "", nil, false
	}

	// clientbound hello：请求加密（公钥 + 验证令牌）
	c.WritePacket(suite.MustID("login", "clientbound", "minecraft:hello"), func(w *protocol.Writer) {
		packet.WriteEncryptionRequest(w, packet.EncryptionRequest{
			ServerID:           "",
			PublicKey:          pubDER,
			VerifyToken:        verifyToken,
			ShouldAuthenticate: true,
		})
	})

	keyID := suite.MustID("login", "serverbound", "minecraft:key")
	id, r, err := c.ReadPacket()
	if err != nil || id != keyID {
		s.rejectLoginPacket(c, suite, "未收到加密握手回应，请使用正版客户端连接")
		c.Close()
		return protocol.UUID{}, "", nil, false
	}
	resp := packet.ReadEncryptionResponse(r)
	if r.Err != nil {
		s.rejectLoginPacket(c, suite, "加密握手回应格式错误")
		c.Close()
		return protocol.UUID{}, "", nil, false
	}

	secret := decryptKeyExchange(priv, resp.SharedSecret)
	if len(secret) != 16 {
		// 客户端发完 key 包即启用加密，此后无法再明文沟通，只能直接断开
		c.Close()
		return protocol.UUID{}, "", nil, false
	}
	gotToken := decryptKeyExchange(priv, resp.VerifyToken)
	if !bytes.Equal(gotToken, verifyToken) {
		c.Close()
		return protocol.UUID{}, "", nil, false
	}

	// 客户端发完 key 包即已启用加密，此处必须同步启用，之后的包（含踢出消息）全走密文
	if err := c.SetEncryption(secret); err != nil {
		c.Close()
		return protocol.UUID{}, "", nil, false
	}

	// Mojang 正版验证：serverID 恒空串的认证哈希规则
	serverHash := AuthHash("", secret, pubDER)
	auth := s.auth
	if auth == nil {
		auth = newAuthClient()
	}
	uuid, name, props, err := auth.HasJoined(start.Username, serverHash)
	if err != nil {
		reason := "正版验证暂时不可用，请稍后重试"
		if errors.Is(err, ErrAuthRejected) {
			reason = "正版验证未通过：请先用正版客户端登录此账号（session server 未确认该玩家）"
		}
		s.rejectLoginPacket(c, suite, reason)
		c.Close()
		log.Printf("[server] %s 正版验证失败: %v", start.Username, err)
		return protocol.UUID{}, "", nil, false
	}
	log.Printf("[server] %s 正版验证通过 (uuid=%s)", name, hex.EncodeToString(uuid[:]))
	return uuid, name, props, true
}

// decryptKeyExchange 解密 key 包里的 RSA 载荷。官方客户端（1.21.11~26.3
// 实测）使用 PKCS#1 v1.5 填充；再以 OAEP-SHA1 兜底，兼容沿用旧约定的
// 第三方实现。两种填充都失败返回 nil。
func decryptKeyExchange(priv *rsa.PrivateKey, ct []byte) []byte {
	if out, err := rsa.DecryptPKCS1v15(rand.Reader, priv, ct); err == nil {
		return out
	}
	out, err := rsa.DecryptOAEP(sha1.New(), rand.Reader, priv, ct, nil)
	if err != nil {
		return nil
	}
	return out
}

// rejectLoginPacket 在登录阶段写出踢出包（不关连接，由调用方决定）。
func (s *Server) rejectLoginPacket(c *conn.Conn, suite *version.Suite, reason string) {
	c.WritePacket(suite.MustID("login", "clientbound", "minecraft:login_disconnect"), func(w *protocol.Writer) {
		packet.WriteDisconnectLogin(w, fmt.Sprintf(`{"text":"%s"}`, reason))
	})
}

// handleConfiguration 处理配置阶段：known packs 回显 → 注册表 → 完成配置。
func (s *Server) handleConfiguration(c *conn.Conn, ver version.Version, suite *version.Suite, name string, uuid protocol.UUID) {
	// 请求已知包：真实客户端会回显 minecraft:core，从而允许注册表只发条目名
	knownPacks := suite.MustID("configuration", "clientbound", "minecraft:select_known_packs")
	c.WritePacket(knownPacks, func(w *protocol.Writer) {
		w.RawBytes(assets.KnownPacks(int32(ver.Protocol)))
	})

	hasCore := false
	registryDataID := suite.MustID("configuration", "clientbound", "minecraft:registry_data")
	finishID := suite.MustID("configuration", "clientbound", "minecraft:finish_configuration")
	keepAliveOut := suite.MustID("configuration", "clientbound", "minecraft:keep_alive")
	_ = keepAliveOut

	for {
		id, r, err := c.ReadPacket()
		if err != nil {
			c.Close()
			return
		}
		body := r.Rest()
		pr := protocol.NewReader(body)
		switch id {
		case suite.MustID("configuration", "serverbound", "minecraft:select_known_packs"):
			packs := packet.ReadSelectKnownPacks(pr)
			for _, p := range packs {
				if p.Namespace == "minecraft" && p.ID == "core" {
					hasCore = true
				}
			}
			log.Printf("[server] %s known packs: %d 项 (core=%v)", name, len(packs), hasCore)
			if !hasCore {
				s.rejectConf(c, suite, "本服需要客户端内置原版数据包（minecraft:core）")
				c.Close()
				return
			}
			// 复用官方注册表包体（id-only 条目，按版本）
			for _, rb := range assets.Registries(int32(ver.Protocol)) {
				c.WritePacket(registryDataID, func(w *protocol.Writer) {
					w.RawBytes(rb)
				})
			}
			c.WritePacket(finishID, func(w *protocol.Writer) {
				packet.WriteFinishConfiguration(w)
			})
		case suite.MustID("configuration", "serverbound", "minecraft:finish_configuration"):
			c.SetState(conn.StatePlay)
			sess := game.NewSession(c, suite, s.game, name, uuid)
			go sess.PlayLoop()
			return
		case suite.MustID("configuration", "serverbound", "minecraft:keep_alive"):
			ka := pr.Int64()
			c.WritePacket(keepAliveOut, func(w *protocol.Writer) {
				packet.WriteKeepAlive(w, ka)
			})
		case suite.MustID("configuration", "serverbound", "minecraft:client_information"),
			suite.MustID("configuration", "serverbound", "minecraft:custom_payload"),
			suite.MustID("configuration", "serverbound", "minecraft:cookie_response"):
			// 忽略
		default:
			// 未识别包安全跳过
		}
	}
}

// rejectConf 在配置阶段踢出。
func (s *Server) rejectConf(c *conn.Conn, suite *version.Suite, reason string) {
	c.WritePacket(suite.MustID("configuration", "clientbound", "minecraft:disconnect"), func(w *protocol.Writer) {
		packet.WriteSystemChat(w, reason, false)
	})
}

// awaitPacket 等待指定 ID 的包（忽略期间的其他 keepalive）。
func awaitPacket(c *conn.Conn, want int32) bool {
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
		id, _, err := c.ReadPacket()
		if err != nil {
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				continue
			}
			return false
		}
		if id == want {
			_ = c.SetReadDeadline(time.Time{})
			return true
		}
	}
	return false
}

// offlineUUID 按离线模式规则派生 UUID v3("OfflinePlayer:"+name)。
func offlineUUID(name string) protocol.UUID {
	sum := md5.Sum([]byte("OfflinePlayer:" + name))
	sum[6] = (sum[6] & 0x0f) | 0x30
	sum[8] = (sum[8] & 0x3f) | 0x80
	var u protocol.UUID
	copy(u[:], sum[:])
	return u
}
