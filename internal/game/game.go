package game

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"mcserver/internal/assets"
	"mcserver/internal/entity"
	"mcserver/internal/packet"
	"mcserver/internal/protocol"
	"mcserver/internal/version"
	"mcserver/internal/world"
)

// Config 是游戏层配置。
type Config struct {
	SpawnX, SpawnY, SpawnZ float64
	ViewDistance           int32 // 区块半径
	GameMode               packet.GameMode
	MOTD                   string
	MaxPlayers             int32
	WorldType              string // "flat" | "noise"
	WorldSeed              int64
	SaveDir                string // 世界存档目录（空 = 不持久化）
	Anvil                  bool   // 存档用官方 Anvil 格式（region 互操作）
}

// Game 聚合全部游戏状态，由主循环独占访问。
type Game struct {
	Cfg   Config
	Suite *version.Suite
	Bus   *EventBus

	events chan Event
	joinCh chan *Session

	players map[int32]*entity.Player
	nextEID atomic.Int32
	count   atomic.Int32

	tick     int64
	worldAge int64
	dayTime  int64
	clockID  int32 // 白天时钟 ID（官方录制为 1）

	gen   world.Generator
	world *world.Manager // 服务端权威方块状态（区块缓存）
	// 各版本数据表（物品→方块 / 实体类型 / 掉落 / 工具门槛；世界统一 777 状态空间）
	itemBlocks    map[int32]map[int32]uint32
	entityTypes   map[int32]map[string]int32
	blockDrops    map[int32]map[uint32]int32
	mineable      map[int32]map[uint32]uint8
	pickaxes      map[int32]map[int32]uint8
	itemToCanon   map[int32]map[int32]int32 // 玩家协议物品 ID → 777 基准 ID（反向翻译表）
	itemNames     map[int32]map[int32]string // 玩家协议物品 ID → 物品短名（进食判定）
	items         map[int32]*itemEntity     // 掉落物实体（item.ID 恒为 777 基准）
	mobs          map[int32]*mob           // 生物实体
	mobDrops      map[string]int32         // 生物 → 掉落物品（777 基准）
	recipes      []craftRecipe           // 合成配方（基准版本）
	itemTags     map[string]map[string]bool // 物品标签（配方匹配用）
	crafting     map[int32]*craftState     // 打开中的工作台（按玩家 EID）
	furnaces     map[[3]int32]*furnaceState // 熔炉状态（按方块位置）
	openFurnace  map[int32][3]int32        // 打开中的熔炉（玩家 EID → 位置）
	smelting     map[string]string         // 冶炼表（777 基准）
	bedByState   map[uint32]*bedInfo       // 床状态反查
	doorByState  map[uint32]*doorInfo      // 门状态反查
	doorOpenFlip map[[2]uint32]uint32      // 门状态开/关互换
	chests       map[[3]int32]*chestState  // 箱子（位置 → 27 格）
	openChest    map[int32][3]int32        // 打开中的箱子
	fallings     map[int32]*fallingBlock   // 下落方块实体
	waterQueue   [][3]int32                // 待流水位置
	saplings     map[[3]int32]int32        // 树苗（位置 → 树龄）
	raining      bool                     // 天气
	rainTimer    int                      // 雨剩余决策周期
	orbs         map[int32]*xpOrb          // 经验球实体
	arrows       map[int32]*arrow         // 箭矢实体
	creepers     map[int32]*creeperFuse   // 苦力怕引信
	leafQueue    [][3]int32               // 待枯萎判定的叶子
	breeds       map[int32]*breedState    // 动物繁殖状态
	crops        map[[3]int32]int32       // 小麦作物（位置 → 种植标记）
	brews        map[[3]int32]*brewingState // 酿造台（位置 → 状态）
	openBrew     map[int32][3]int32        // 打开中的酿造台
	brewing      map[[2]string]string      // 酿造表（输入药水+材料 → 产物）
	enchOpen     map[int32]bool           // 打开附魔台的玩家
	enchants     map[string]assets.EnchantInfo // 附魔表
	rsDirty      bool                     // 红石网络待重算
	observerPulse [][4]int32                // 观察者脉冲（位置+到期刻）
	anvils       map[int32]*anvilSession  // 打开中的铁砧
	blockNames   map[uint32]string         // 方块状态 → 名（777 基准）
	itemNameCache map[int32]string         // 777 物品 ID → 短名（缓存）
	pendingBlocks map[[3]int32]uint32       // 待广播的方块变更

	closed atomic.Bool
}

// New 创建游戏并启动主循环。
func New(cfg Config, suite *version.Suite) *Game {
	g := &Game{
		Cfg:            cfg,
		Suite:          suite,
		Bus:            NewEventBus(),
		events:         make(chan Event, 4096),
		joinCh:         make(chan *Session, 64),
		players:        map[int32]*entity.Player{},
		items:          map[int32]*itemEntity{},
		mobs:           map[int32]*mob{},
		crafting:       map[int32]*craftState{},
		furnaces:       map[[3]int32]*furnaceState{},
		openFurnace:    map[int32][3]int32{},
		breeds:         map[int32]*breedState{},
		crops:          map[[3]int32]int32{},
		brews:         map[[3]int32]*brewingState{},
		openBrew:      map[int32][3]int32{},
		enchOpen:     map[int32]bool{},
		anvils:       map[int32]*anvilSession{},
		chests:         map[[3]int32]*chestState{},
		openChest:      map[int32][3]int32{},
		orbs:           map[int32]*xpOrb{},
		arrows:         map[int32]*arrow{},
		creepers:       map[int32]*creeperFuse{},
		fallings:       map[int32]*fallingBlock{},
		saplings:       map[[3]int32]int32{},
		dayTime:        6000, // 正午
		clockID:        1,
		itemBlocks:     map[int32]map[int32]uint32{},
		entityTypes:    map[int32]map[string]int32{},
		blockDrops:     map[int32]map[uint32]int32{},
		mineable:       map[int32]map[uint32]uint8{},
		pickaxes:       map[int32]map[int32]uint8{},
		itemToCanon:    map[int32]map[int32]int32{},
		itemNames:      map[int32]map[int32]string{},
		pendingBlocks:  map[[3]int32]uint32{},
	}
	// 世界类型由注册表解析：核心只有虚空，地形等由插件提供
	g.gen = world.NewGenerator(cfg.WorldType, cfg.WorldSeed)
	g.world = world.NewManager(g.gen)
	for _, v := range version.All() {
		p := int32(v.Protocol)
		g.itemBlocks[p] = assets.ItemBlocks(p)
		g.entityTypes[p] = assets.EntityTypes(p)
		g.blockDrops[p] = assets.BlockDrops(p)
		g.mineable[p] = assets.MineableMask(p)
		g.pickaxes[p] = assets.PickaxeBits(p)
		if im := assets.ItemMap(p); len(im) > 0 {
			rev := make(map[int32]int32, len(im))
			for from, to := range im {
				rev[to] = from
			}
			g.itemToCanon[p] = rev
		}
		if names := assets.ItemIDs(p); len(names) > 0 {
			rev := make(map[int32]string, len(names))
			for name, id := range names {
				rev[id] = name
			}
			g.itemNames[p] = rev
		}
		if md := assets.MobDrops(p); len(md) > 0 {
			g.mobDrops = md
		}
		if rs := loadRecipes(p); len(rs) > 0 {
			g.recipes = rs
		}
		if tg := assets.ItemTags(p); len(tg) > 0 {
			g.itemTags = tg
		}
		if ec := assets.Enchants(p); len(ec) > 0 {
			g.enchants = ec
		}
		if bw := assets.Brewing(p); len(bw) > 0 {
			g.brewing = bw
		}
		if sm := assets.Smelting(p); len(sm) > 0 {
			g.smelting = sm
		}
		if bn := assets.BlockNames(p); len(bn) > 0 {
			g.blockNames = bn
		}
	}
	// 775 无官方报告：借用相邻 776 的表
	if g.entityTypes[775] == nil {
		g.entityTypes[775] = g.entityTypes[776]
	}
	if g.itemToCanon[775] == nil {
		g.itemToCanon[775] = g.itemToCanon[776]
	}
	if g.itemNames[775] == nil {
		g.itemNames[775] = g.itemNames[776]
	}
	// 世界持久化：读档优先于生成器
	if cfg.SaveDir != "" {
		if cfg.Anvil {
			canon := assets.BlockNamesFull(777)
			inv := map[string]uint32{}
			for k, v := range canon {
				inv[v] = k
			}
			if n, err := g.world.LoadAnvil(cfg.SaveDir, inv); err != nil {
				log.Printf("[game] Anvil 读档失败: %v", err)
			} else if n > 0 {
				log.Printf("[game] Anvil 读档 %d 个区块", n)
			}
		}
		if n, err := g.world.Load(cfg.SaveDir); err != nil {
			log.Printf("[game] 世界读档失败: %v", err)
		} else if n > 0 {
			log.Printf("[game] 世界读档 %d 个区块", n)
		}
		if tb, err := os.ReadFile(filepath.Join(cfg.SaveDir, "time.json")); err == nil {
			var tm struct {
				Age int64 `json:"age"`
				Day int64 `json:"day"`
			}
			if json.Unmarshal(tb, &tm) == nil {
				g.worldAge, g.dayTime = tm.Age, tm.Day
				log.Printf("[game] 恢复世界时间 age=%d day=%d", g.worldAge, g.dayTime)
			}
		}
		g.loadFurnaces()
	g.loadWeather()
	g.loadChests()
	g.loadBrewings()
	g.loadInteractTables()
	}
	// 世界类型由注册表解析：核心只有虚空，地形等由插件提供
	g.gen = world.NewGenerator(cfg.WorldType, cfg.WorldSeed)
	// 生存端：配置 spawn y=0 时按地形自动取站立表面
	if cfg.SpawnY == 0 {
		if sp, ok := g.gen.(interface{ SurfaceY(x, z int32) int32 }); ok {
			g.Cfg.SpawnY = float64(sp.SurfaceY(int32(cfg.SpawnX), int32(cfg.SpawnZ)))
		} else {
			g.Cfg.SpawnY = 0
		}
	}
	g.nextEID.Store(100)
	go g.run()
	return g
}

// Submit 从会话读 goroutine 投递事件（非阻塞，满则丢弃并记日志）。
func (g *Game) Submit(e Event) {
	select {
	case g.events <- e:
	default:
		log.Printf("[game] 事件队列溢出，丢弃 %T", e)
	}
}

// JoinSession 由 server 层在配置阶段完成后调用。
func (g *Game) JoinSession(s *Session) {
	select {
	case g.joinCh <- s:
	default:
		s.conn.Close()
	}
}

// PlayerCount 返回在线人数（供状态查询等外部读取）。
func (g *Game) PlayerCount() int32 { return g.count.Load() }

// run 是唯一的游戏主循环 goroutine：20TPS + 事件消费。
func (g *Game) run() {
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for !g.closed.Load() {
		select {
		case <-ticker.C:
			g.tickNow()
		case s := <-g.joinCh:
			g.handleJoin(s)
		default:
			select {
			case e := <-g.events:
				g.handleEvent(e)
			case s := <-g.joinCh:
				g.handleJoin(s)
			case <-ticker.C:
				g.tickNow()
			}
		}
	}
}

// tickNow 推进一个游戏刻。
func (g *Game) tickNow() {
	g.tick++
	g.worldAge++
	g.dayTime = (g.dayTime + 1) % 24000

	g.tickItems()
	g.tickMobs()
	g.tickMobSpawning()
	g.tickFurnaces()
	g.tickFallings()
	g.tickWater()
	g.tickSaplings()
	g.tickOrbs()
	g.tickArrows()
	g.tickCreepers()
	g.tickUndeadBurn()
	g.tickLeafDecay()
	g.tickWeather()
	g.tickBreeds()
	g.tickCrops()
	g.tickBrewings()
	g.tickEffects()
	g.tickRedstone()
	g.tickPistons()
	g.tickComparators()
	g.tickObservers()
	g.tickStats()

	if g.tick%20 == 0 { // 每秒
		g.broadcastTime()
		g.checkKeepAlive()
	}
	if g.Cfg.SaveDir != "" && g.tick%600 == 0 { // 每 30 秒自动存档
		g.saveAll()
	}

	g.Bus.emit(Tick{Count: g.tick})
}

// saveAll 保存世界脏区块、时间与玩家状态。
func (g *Game) saveAll() {
	if g.Cfg.Anvil {
		canon := assets.BlockNamesFull(777)
		inv := map[string]uint32{}
		for k, v := range canon {
			inv[v] = k
		}
		if n, err := g.world.SaveAnvilBE(g.Cfg.SaveDir, canon, inv, g.exportBlockEntities()); err != nil {
			log.Printf("[game] Anvil 存档失败: %v", err)
		} else if n > 0 {
			log.Printf("[game] Anvil 存档 %d 个区块", n)
		}
		// level.dat（官方工具识别用）
		world.SetBEItemNames(g.itemNameCache)
		_ = os.WriteFile(filepath.Join(g.Cfg.SaveDir, "level.dat"),
			world.WriteLevelDat(int32(g.Cfg.SpawnX), int32(g.Cfg.SpawnY), int32(g.Cfg.SpawnZ), g.Cfg.WorldSeed, g.worldAge), 0o644)
	}
	if n, err := g.world.Save(g.Cfg.SaveDir); err != nil {
		log.Printf("[game] 世界存档失败: %v", err)
	} else if n > 0 {
		log.Printf("[game] 世界存档 %d 个区块", n)
	}
	// 时间存档（世界年龄 + 一天内时间）
	mb, _ := json.Marshal(map[string]int64{"age": g.worldAge, "day": g.dayTime})
	_ = os.WriteFile(filepath.Join(g.Cfg.SaveDir, "time.json"), mb, 0o644)
	g.saveFurnaces()
	g.saveChests()
	g.saveBrewings()
	g.saveWeather()
	for _, p := range g.players {
		g.savePlayer(p)
	}
}

// Stop 结束主循环（先存档）。
func (g *Game) Stop() {
	if g.closed.CompareAndSwap(false, true) {
		g.saveAll()
	}
}

// handleEvent 分发会话事件。
func (g *Game) handleEvent(e Event) {
	switch ev := e.(type) {
	case *playerQuitEvent:
		g.handleQuit(ev)
	case *playerMoveEvent:
		p := ev.p
		prevX, prevZ := p.BroadcastX, p.BroadcastZ
		prevOnGround := p.OnGround
		p.X, p.Y, p.Z = ev.x, ev.y, ev.z
		p.Yaw, p.Pitch, p.OnGround = ev.yaw, ev.pitch, ev.onGround
		if !p.Dead {
			dx, dz := p.X-prevX, p.Z-prevZ
			g.exhaustMovement(p, dx, dz, p.Sprinting)
			if !prevOnGround || !p.OnGround { // 起跳/落地时推进摔落追踪
				g.trackFall(p, p.Y, p.OnGround)
			}
		}
		g.syncMovement(p)
		g.syncChunks(p)
		g.Bus.emit(PlayerMoved{Player: p})
	case *playerChatEvent:
		if ev.p != nil {
			g.Bus.emit(PlayerChatted{Player: ev.p, Message: ev.msg})
			g.broadcastChat(ev.p, ev.msg)
		}
	case *playerCommandEvent:
		if ev.p != nil {
			g.handleCommand(ev)
		}
	case *playerKeepAliveEvent:
		if ev.p != nil && ev.id == ev.p.KeepAlivePending {
			ev.p.KeepAlivePending = 0
		}
	case *playerTeleportConfirmEvent:
		if ev.p != nil && ev.id == ev.p.TeleportID {
			ev.p.TeleportID = -1
		}
	case *blockDigEvent:
		g.handleDig(ev)
	case *blockPlaceEvent:
		g.handlePlace(ev)
	case *playerHeldSlotEvent:
		g.handleHeldSlot(ev)
	case *playerCreativeSlotEvent:
		g.handleCreativeSlot(ev)
	case *playerContainerClickEvent:
		g.handleContainerClick(ev)
	case *playerRespawnEvent:
		if ev.p != nil {
			g.handleRespawn(ev.p)
		}
	case *playerUseItemEvent:
		g.handleUseItem(ev)
	case *playerAttackEvent:
		g.handleAttack(ev)
	case *playerInteractEvent:
		g.handleInteract(ev)
	case *playerCloseCraftEvent:
		if ev.p != nil {
			g.closeCrafting(ev.p)
		}
	case *playerCloseFurnaceEvent:
		if ev.p != nil {
			g.closeFurnace(ev.p)
		}
	case *playerCloseChestEvent:
		if ev.p != nil {
			g.closeChest(ev.p)
		}
	case *playerCloseBrewEvent:
		if ev.p != nil {
			g.closeBrewing(ev.p)
		}
	case *playerCloseEnchEvent:
		if ev.p != nil {
			g.closeEnchantTable(ev.p)
		}
	case *playerCloseAnvilEvent:
		if ev.p != nil {
			g.closeAnvil(ev.p)
		}
	case *playerEnchantEvent:
		if ev.p != nil {
			g.applyEnchant(ev.p, ev.tier)
		}
	}
	g.flushBlockUpdates()
}

// playerStateFile 是玩家状态的 JSON 存档（位置 + 物品栏 + 选中槽）。
type playerStateFile struct {
	X        float64      `json:"x"`
	Y        float64      `json:"y"`
	Z        float64      `json:"z"`
	Yaw      float32      `json:"yaw"`
	Pitch    float32      `json:"pitch"`
	Slots    []entity.Item `json:"slots"`
	HeldSlot int32        `json:"held"`
	Spawn    *[3]float64  `json:"spawn"`
}

// savePlayer 保存玩家状态到存档目录。
func (g *Game) savePlayer(p *entity.Player) {
	dir := g.Cfg.SaveDir + "/players"
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return
	}
	st := playerStateFile{X: p.X, Y: p.Y, Z: p.Z, Yaw: p.Yaw, Pitch: p.Pitch, Slots: p.Slots[:], HeldSlot: p.HeldSlot}
	if p.SpawnPos != [3]float64{} {
		sp := p.SpawnPos
		st.Spawn = &sp
	}
	b, err := json.Marshal(st)
	if err != nil {
		return
	}
	_ = os.WriteFile(filepath.Join(dir, p.Name+".json"), b, 0o644)
}

// loadPlayer 恢复玩家状态（无存档时保持出生点/空物品栏）。
func (g *Game) loadPlayer(p *entity.Player) {
	b, err := os.ReadFile(filepath.Join(g.Cfg.SaveDir, "players", p.Name+".json"))
	if err != nil {
		return
	}
	var st playerStateFile
	if json.Unmarshal(b, &st) != nil {
		return
	}
	if len(st.Slots) == len(p.Slots) {
		copy(p.Slots[:], st.Slots)
	}
	p.HeldSlot = st.HeldSlot
	if st.Spawn != nil {
		p.SpawnPos = *st.Spawn
	}
	p.X, p.Y, p.Z = st.X, st.Y, st.Z
	p.Yaw, p.Pitch = st.Yaw, st.Pitch
}

// handleJoin 完成进入世界流程并广播。
func (g *Game) handleJoin(s *Session) {
	eid := g.nextEID.Add(1)
	p := &entity.Player{
		EntityID: eid,
		UUID:     s.uuid,
		Name:     s.name,
		X:        g.Cfg.SpawnX,
		Y:        g.Cfg.SpawnY,
		Z:        g.Cfg.SpawnZ,
		Send:     s,
	}
	if g.Cfg.SaveDir != "" {
		g.loadPlayer(p) // 玩家状态持久化：位置与物品栏
	}
	p.BroadcastX, p.BroadcastY, p.BroadcastZ = p.X, p.Y, p.Z
	p.SentChunks = map[[2]int32]bool{}
	s.player.Store(p)
	g.players[eid] = p
	g.count.Store(int32(len(g.players)))

	join := packet.JoinGame{
		EntityID:            eid,
		WorldNames:          []string{"minecraft:overworld"},
		MaxPlayers:          g.Cfg.MaxPlayers,
		ViewDistance:        g.Cfg.ViewDistance,
		SimulationDistance:  g.Cfg.ViewDistance,
		EnableRespawnScreen: true,
		DimensionID:         0, // minecraft:overworld
		WorldName:           "minecraft:overworld",
		GameMode:            g.Cfg.GameMode,
		PreviousGameMode:    255,
		IsFlat:              true,
		SeaLevel:            -63,
		TailApplies:         s.ver.LoginTrailingByte, // 26.3 起末尾多一个布尔
	}
	p.Health, p.Food, p.Saturation = 20, 20, 5
	p.GameMode = int32(g.Cfg.GameMode)
	p.Effects, p.EffectAmp, p.PotionOf = map[string]int32{}, map[string]int32{}, map[[2]int32]string{}
	s.SendPlay("minecraft:login", func(w *protocol.Writer) {
		packet.WriteJoinGame(w, join)
	})

	s.SendPlay("minecraft:player_abilities", func(w *protocol.Writer) {
		flags := uint8(0)
		if g.Cfg.GameMode == packet.ModeCreative {
			flags |= 0x04 | 0x08
		}
		packet.WriteAbilities(w, packet.Abilities{Flags: flags, FlyingSpeed: 0.05, WalkingSpeed: 0.1})
	})

	p.TeleportID = 1
	s.SendPlay("minecraft:player_position", func(w *protocol.Writer) {
		packet.WritePlayerPosition(w, packet.PlayerPositionOut{
			TeleportID: p.TeleportID, X: p.X, Y: p.Y, Z: p.Z, Yaw: p.Yaw, Pitch: p.Pitch,
		})
	})

	s.SendPlay("minecraft:set_chunk_cache_center", func(w *protocol.Writer) {
		packet.WriteChunkCenter(w, int32(g.Cfg.SpawnX)>>4, int32(g.Cfg.SpawnZ)>>4)
	})
	s.SendPlay("minecraft:set_chunk_cache_radius", func(w *protocol.Writer) {
		packet.WriteViewDistance(w, g.Cfg.ViewDistance)
	})
	s.SendPlay("minecraft:set_simulation_distance", func(w *protocol.Writer) {
		packet.WriteSimulationDistance(w, g.Cfg.ViewDistance)
	})
	s.SendPlay("minecraft:set_time", func(w *protocol.Writer) {
		if s.ver.ClassicTime {
			packet.WriteSetTimeClassic(w, g.worldAge, g.dayTime)
		} else {
			packet.WriteSetTime(w, g.worldAge, []packet.Clock{{ID: g.clockID, TotalTicks: g.dayTime, Rate: 1}})
		}
	})
	s.SendPlay("minecraft:set_default_spawn_position", func(w *protocol.Writer) {
		packet.WriteSpawnPosition(w, int32(g.Cfg.SpawnX), int32(g.Cfg.SpawnY), int32(g.Cfg.SpawnZ), 0, 0)
	})

	s.SendPlay("minecraft:player_info_update", func(w *protocol.Writer) {
		packet.WritePlayerInfoAdd(w, p.UUID, p.Name, g.Cfg.GameMode)
	})

	g.sendChunks(s, p, int32(g.Cfg.SpawnX)>>4, int32(g.Cfg.SpawnZ)>>4)
	g.syncInventory(p) // 初始物品栏同步
	g.syncHealth(p)    // 初始生命/食物同步
	g.syncExperience(p)
	if g.raining {
		e := gameEventStartRain
		p.Send.SendPlay("minecraft:game_event", func(w *protocol.Writer) {
			w.Int8(e)
			w.Float(0)
		})
	}

	// 视距内已有的生物补给新玩家
	for _, m := range g.mobs {
		if abs32(int32(m.x)>>4-int32(p.X)>>4) <= g.Cfg.ViewDistance &&
			abs32(int32(m.z)>>4-int32(p.Z)>>4) <= g.Cfg.ViewDistance {
			g.sendMobSpawn(p, m)
		}
	}

	// 视距内已有的掉落物补给新玩家
	for _, ie := range g.items {
		if abs32(int32(ie.x)>>4-int32(p.X)>>4) <= g.Cfg.ViewDistance &&
			abs32(int32(ie.z)>>4-int32(p.Z)>>4) <= g.Cfg.ViewDistance {
			g.sendItemSpawn(p, ie)
		}
	}

	// 互相可见
	playerType := func(target *entity.Player) int32 { return g.entityTypeID(target, "player") }
	for _, other := range g.players {
		if other.EntityID == eid {
			continue
		}
		other.Send.SendPlay("minecraft:player_info_update", func(w *protocol.Writer) {
			packet.WritePlayerInfoAdd(w, p.UUID, p.Name, g.Cfg.GameMode)
		})
		other.Send.SendPlay("minecraft:add_entity", func(w *protocol.Writer) {
			packet.SpawnEntity{EntityID: p.EntityID, UUID: p.UUID, Type: playerType(other),
				X: p.X, Y: p.Y, Z: p.Z}.Write(w)
		})
		s.SendPlay("minecraft:player_info_update", func(w *protocol.Writer) {
			packet.WritePlayerInfoAdd(w, other.UUID, other.Name, g.Cfg.GameMode)
		})
		s.SendPlay("minecraft:add_entity", func(w *protocol.Writer) {
			packet.SpawnEntity{EntityID: other.EntityID, UUID: other.UUID, Type: playerType(p),
				X: other.X, Y: other.Y, Z: other.Z}.Write(w)
		})
	}

	g.broadcastEquipment(p)
	g.Bus.emit(PlayerJoin{Player: p})
	log.Printf("[game] %s 加入，在线 %d 人", p.Name, len(g.players))
	g.broadcastSystem(fmt.Sprintf("§e%s 加入了游戏", p.Name))
}

// sendChunks 以指定区块为中心向玩家成批发送区块（生成一次后从世界管理器取缓存）。
func (g *Game) sendChunks(s *Session, p *entity.Player, cx, cz int32) {
	r := g.Cfg.ViewDistance
	s.SendPlay("minecraft:chunk_batch_start", func(w *protocol.Writer) {})
	n := int32(0)
	for dx := -r; dx <= r; dx++ {
		for dz := -r; dz <= r; dz++ {
			x, z := cx+dx, cz+dz
			body := g.world.Chunk(x, z).EncodeBody(world.StyleFor(int32(s.suite.Version)))
			s.SendPlay("minecraft:level_chunk_with_light", func(w *protocol.Writer) {
				packet.WriteLevelChunk(w, x, z, body)
			})
			p.SentChunks[[2]int32{x, z}] = true
			n++
		}
	}
	p.CenterCX, p.CenterCZ = cx, cz
	s.SendPlay("minecraft:chunk_batch_finished", func(w *protocol.Writer) {
		packet.WriteChunkBatchFinished(w, n)
	})
}

// handleQuit 移除玩家并广播。
func (g *Game) handleQuit(ev *playerQuitEvent) {
	p := ev.p
	if p == nil {
		return
	}
	if _, ok := g.players[p.EntityID]; !ok {
		return
	}
	g.closeCrafting(p)
	if _, ok := g.openFurnace[p.EntityID]; ok {
		g.closeFurnace(p)
	}
	if _, ok := g.openChest[p.EntityID]; ok {
		g.closeChest(p)
	}
	if _, ok := g.openBrew[p.EntityID]; ok {
		g.closeBrewing(p)
	}
	if g.enchOpen[p.EntityID] {
		g.closeEnchantTable(p)
	}
	if _, ok := g.anvils[p.EntityID]; ok {
		g.closeAnvil(p)
	}
	delete(g.players, p.EntityID)
	g.count.Store(int32(len(g.players)))
	if g.Cfg.SaveDir != "" {
		g.savePlayer(p) // 离线即保存玩家状态
	}
	for _, other := range g.players {
		other.Send.SendPlay("minecraft:player_info_remove", func(w *protocol.Writer) {
			packet.WritePlayerInfoRemove(w, p.UUID)
		})
		other.Send.SendPlay("minecraft:remove_entities", func(w *protocol.Writer) {
			packet.WriteRemoveEntities(w, []int32{p.EntityID})
		})
	}
	g.Bus.emit(PlayerQuit{Player: p, Reason: ev.reason})
	log.Printf("[game] %s 离开（%s），在线 %d 人", p.Name, ev.reason, len(g.players))
	g.broadcastSystem(fmt.Sprintf("§e%s 离开了游戏", p.Name))
}

// syncMovement 把玩家最新位置广播给其他人（位置变化时）。
func (g *Game) syncMovement(p *entity.Player) {
	if p.X == p.BroadcastX && p.Y == p.BroadcastY && p.Z == p.BroadcastZ {
		return
	}
	for _, other := range g.players {
		if other.EntityID == p.EntityID {
			continue
		}
		o := other
		o.Send.SendPlay("minecraft:entity_position_sync", func(w *protocol.Writer) {
			packet.WriteEntityPositionSync(w, p.EntityID, p.X, p.Y, p.Z, 0, 0, 0, p.Yaw, p.Pitch, p.OnGround)
		})
	}
	p.BroadcastX, p.BroadcastY, p.BroadcastZ = p.X, p.Y, p.Z
}

// broadcastChat 广播玩家聊天。
func (g *Game) broadcastChat(p *entity.Player, msg string) {
	text := fmt.Sprintf("<%s> %s", p.Name, msg)
	for _, other := range g.players {
		other.Send.SendPlay("minecraft:system_chat", func(w *protocol.Writer) {
			packet.WriteSystemChat(w, text, false)
		})
	}
}

// BroadcastSystem 向所有在线玩家广播系统消息（玩法模块可用）。
func (g *Game) BroadcastSystem(text string) { g.broadcastSystem(text) }

// broadcastSystem 广播系统消息。
func (g *Game) broadcastSystem(text string) {
	for _, other := range g.players {
		other.Send.SendPlay("minecraft:system_chat", func(w *protocol.Writer) {
			packet.WriteSystemChat(w, text, false)
		})
	}
}

// broadcastTime 每秒同步时间（按各玩家协议版本选择格式）。
func (g *Game) broadcastTime() {
	age, day, cid := g.worldAge, g.dayTime, g.clockID
	for _, other := range g.players {
		sess, _ := other.Send.(*Session)
		classic := sess != nil && sess.ver.ClassicTime
		other.Send.SendPlay("minecraft:set_time", func(w *protocol.Writer) {
			if classic {
				packet.WriteSetTimeClassic(w, age, day)
			} else {
				packet.WriteSetTime(w, age, []packet.Clock{{ID: cid, TotalTicks: day, Rate: 1}})
			}
		})
	}
}

// checkKeepAlive 每秒检查：对无未确认 keepalive 的玩家发送新的；
// 超过 30 秒未响应的玩家踢出。
func (g *Game) checkKeepAlive() {
	now := time.Now().UnixMilli()
	for _, p := range g.players {
		if p.KeepAlivePending == 0 {
			p.KeepAlivePending = now
			id := now
			p.Send.SendPlay("minecraft:keep_alive", func(w *protocol.Writer) {
				packet.WriteKeepAlive(w, id)
			})
		} else if now-p.KeepAlivePending > 30000 {
			s := p.Send.(*Session)
			if s != nil {
				s.Kick("KeepAlive 超时")
			}
		}
	}
}

// handleCommand 处理内置命令。
func (g *Game) handleCommand(ev *playerCommandEvent) {
	p := ev.p
	reply := func(format string, a ...any) {
		p.Send.SendPlay("minecraft:system_chat", func(w *protocol.Writer) {
			packet.WriteSystemChat(w, fmt.Sprintf(format, a...), false)
		})
	}
	switch ev.Name {
	case "gamemode":
		if len(ev.Args) < 1 {
			reply("§7用法: /gamemode <survival|creative>")
			break
		}
		mode := int32(packet.ModeSurvival)
		if ev.Args[0] == "creative" || ev.Args[0] == "1" {
			mode = int32(packet.ModeCreative)
		} else if ev.Args[0] != "survival" && ev.Args[0] != "0" {
			reply("§c未知模式: %s", ev.Args[0])
			break
		}
		p.GameMode = mode
		m := packet.GameMode(mode)
		// 能力包 + 游戏状态同步
		flags := uint8(0)
		if m == packet.ModeCreative {
			flags |= 0x04 | 0x08
		}
		p.Send.SendPlay("minecraft:player_abilities", func(w *protocol.Writer) {
			packet.WriteAbilities(w, packet.Abilities{Flags: flags, FlyingSpeed: 0.05, WalkingSpeed: 0.1})
		})
		p.Send.SendPlay("minecraft:game_event", func(w *protocol.Writer) {
			w.Int8(3) // 改变游戏模式
			w.Float(float32(m))
		})
		reply("§7已将 %s 的游戏模式设为 %s", p.Name, ev.Args[0])
	case "give":
		if len(ev.Args) < 1 {
			reply("§7用法: /give <物品名> [数量]")
			break
		}
		id777, ok := assets.ItemIDs(777)[ev.Args[0]]
		if !ok {
			reply("§c未知物品: %s", ev.Args[0])
			break
		}
		count := int32(1)
		if len(ev.Args) > 1 {
			if n, err := strconv.Atoi(ev.Args[1]); err == nil && n > 0 && n <= 6400 {
				count = int32(n)
			}
		}
		for count > 0 {
			n := min32(itemMaxStack, count)
			g.giveItem(p, entity.Item{ID: id777, Count: n})
			count -= n
		}
		reply("§7已给予 %s ×%d", ev.Args[0], countOrOne(ev.Args))
	case "tp":
		if len(ev.Args) < 3 {
			reply("§7用法: /tp <x> <y> <z>")
			break
		}
		var x, y, z float64
		if _, err := fmt.Sscan(strings.Join(ev.Args[:3], " "), &x, &y, &z); err != nil {
			reply("§c坐标格式错误")
			break
		}
		p.X, p.Y, p.Z = x, y, z
		p.TeleportID++
		id := p.TeleportID
		p.Send.SendPlay("minecraft:player_position", func(w *protocol.Writer) {
			packet.WritePlayerPosition(w, packet.PlayerPositionOut{TeleportID: id, X: x, Y: y, Z: z})
		})
		g.syncChunks(p)
		reply("§7已传送到 (%.1f, %.1f, %.1f)", x, y, z)
	case "time":
		if len(ev.Args) < 2 || ev.Args[0] != "set" {
			reply("§7用法: /time set <day|night|数值>")
			break
		}
		switch ev.Args[1] {
		case "day":
			g.dayTime = 1000
		case "night":
			g.dayTime = 13000
		default:
			if n, err := strconv.Atoi(ev.Args[1]); err == nil && n >= 0 && n < 24000 {
				g.dayTime = int64(n)
			} else {
				reply("§c无效时间")
				break
			}
		}
		g.broadcastTime()
		reply("§7时间已设为 %d", g.dayTime)
	case "kill":
		g.damage(p, 1000, damageGeneric)
	case "list":
		names := ""
		for _, other := range g.players {
			if names != "" {
				names += ", "
			}
			names += other.Name
		}
		p.Send.SendPlay("minecraft:system_chat", func(w *protocol.Writer) {
			packet.WriteSystemChat(w, fmt.Sprintf("§7在线 (%d): §f%s", len(g.players), names), false)
		})
	case "spawn":
		p.X, p.Y, p.Z = g.Cfg.SpawnX, g.Cfg.SpawnY, g.Cfg.SpawnZ
		p.TeleportID++
		id := p.TeleportID
		x, y, z := p.X, p.Y, p.Z
		p.Send.SendPlay("minecraft:player_position", func(w *protocol.Writer) {
			packet.WritePlayerPosition(w, packet.PlayerPositionOut{TeleportID: id, X: x, Y: y, Z: z})
		})
	case "help":
		p.Send.SendPlay("minecraft:system_chat", func(w *protocol.Writer) {
			packet.WriteSystemChat(w, "§7可用命令: /list /spawn /gamemode /give /tp /time /kill /help", false)
		})
	default:
		p.Send.SendPlay("minecraft:system_chat", func(w *protocol.Writer) {
			packet.WriteSystemChat(w, "§c未知命令: /"+ev.Name, false)
		})
	}
	g.Bus.emit(PlayerCommand{Player: p, Name: ev.Name, Args: ev.Args})
}


// countOrOne give 命令回显数量。
func countOrOne(args []string) int32 {
	if len(args) > 1 {
		if n, err := strconv.Atoi(args[1]); err == nil && n > 0 {
			return int32(n)
		}
	}
	return 1
}
