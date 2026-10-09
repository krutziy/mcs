# Go MC 框架 —— 面向小游戏定制的 Minecraft 服务端

用 Go 从零实现的 Minecraft Java 版服务端框架。目标是作为**起床战争、多人逃杀等小游戏玩法的定制底座**：
框架负责协议、登录、多人并发与事件分发，玩法以模块形式叠加。

当前支持 **Java 版 26.1（协议 775）**，离线登录（任意用户名），多玩家互相可见、聊天、事件总线。

## 快速开始

```bash
# Go 工具链用项目内便携版（不污染系统环境）
. .tools/env.sh          # 设置 $GO 与项目内缓存
"$GO" build -o server.exe ./cmd/server/
./server.exe             # 读取 config.json（首次自动生成）
```

用 Minecraft 26.1 客户端多人游戏 → 添加服务器 `127.0.0.1:25565` → 进入。
离线模式：用户名任意输入即可登录。

内置命令：`/list`（在线列表）、`/spawn`（回出生点）、`/help`。

## 架构

```
cmd/server        入口
cmd/recorder      协议录制客户端（开发工具，见下文）
internal/
├── protocol/     线格式：VarInt、String、Position、UUID、NBT（网络格式）
├── conn/         连接层：长度分帧、zlib 压缩、包读写（读独占/写带锁）
├── version/      ★多版本抽象：Suite = 一张"包名→ID"表，按握手协议号选取
├── packet/       各状态的具体包编解码（带官方字节级回归测试）
├── assets/       内嵌官方录制数据：注册表包、平坦世界区块模板
├── entity/       玩家状态
├── game/         ★框架核心：事件总线、会话、20TPS 主循环
└── server/       TCP 监听、握手/状态ping/登录/配置 状态机、config.json
```

### 并发模型（写玩法不需要锁的关键）

```
连接A读goroutine ─┐
连接B读goroutine ─┼──→ events channel ──→ ┌──────────────┐
连接C读goroutine ─┘                        │ 游戏主循环     │ ← 唯一拥有游戏状态
                                           │ 20 TPS tick   │
             ┌── 会话A写(带锁) ←────────────┤ 事件总线分发   │
             ├── 会话B写(带锁) ←────────────└──────────────┘
             └── 会话C写(带锁)
```

- 每连接一个读 goroutine：解帧、解析包、投递事件（不碰共享状态）
- **单个主循环 goroutine** 消费全部事件并驱动 tick——游戏逻辑天然单线程
- 广播输出走各连接的写方法（`conn.WritePacket` 内部互斥）

### 协议状态机

```
Handshaking ──→ Status(服务器列表ping)
            └─→ Login(离线登录+压缩) → Configuration(注册表) → Play(游戏)
```

## 如何开发一个玩法模块

玩法模块 = 订阅事件总线 + 使用框架 API。所有回调在主循环中执行，无需加锁：

```go
srv, _ := server.New(cfg)
g := srv.Game()

// 例：起床战争的队伍出生点保护
g.Bus.On(game.PlayerMoved{}, func(e game.Event) {
    ev := e.(game.PlayerMoved)
    if inProtectedZone(ev.Player.X, ev.Player.Z) && ev.Player.Sneaking {
        g.BroadcastSystem("§c出生点内禁止下蹲!") // 按需添加此类 API
    }
})

srv.ListenAndServe()
```

现有事件：`PlayerJoin / PlayerQuit / PlayerMoved / PlayerChatted / PlayerCommand / Tick`。

## 关键技术决策与发现（26.1 协议）

开发中逐字节验证过的事实，对后续维护非常重要：

1. **包 ID 以 Mojang 官方数据为准**。`java -DbundlerMainClass=net.minecraft.data.Main
   -jar server.jar --reports` 生成的 `packets.json` 是权威包名→ID 表，
   `internal/version/packets_775.go` 由它生成。第三方数据（minecraft-data）存在个别偏差。
2. **known packs 机制**：服务端下发 `select_known_packs(minecraft:core 26.1)`，
   客户端回显后，注册表数据即可发送 **id-only 条目**（每条目 `00` 标记 + 键名，无 NBT），
   客户端使用内置数据。框架把官方的 28 个 registry_data 包**原样字节复用**（internal/assets）。
3. **区块字节复用**：平坦世界所有区块内容相同，`level_chunk_with_light` 的包体
   （含光照、高度图）直接复用官方录制模板，仅替换坐标（i32 BE ×2，注意不是 VarInt）。
   真正的区块生成器（调色板、高度图打包）是后续里程碑。
4. **26.1 新格式**（与 1.21 时代差异，均已验证）：
   - `player_position`/`entity_position_sync`：位置与速度都是 f64×3，朝向 f32×2，
     前者尾部 4 字节相对标志位图
   - `set_time`：世界年龄 + **时钟列表** `{id varint, totalTicks i64, partialTick f32, rate f32}`，
     白天时钟 id=1
   - `login`（进入游戏）：SpawnInfo 用 dimension_type 注册表 **holder ID**（varint），
     尾部含 `portalCooldown`、`seaLevel`（-63 的五字节 varint）
   - 玩家实体类型 id=155；玩家经 `add_entity` 生成（非旧版专用包）
5. **压缩帧**：阈值下包 = `[len+1][0x00][payload]`，阈值上 = `[len][dataLength][zlib]`。
   官方服对不合规帧报 "Badly compressed packet"（调试时深受其害，见 recorder 的 tapConn）。

## 开发工具链（.tools/，已 gitignore）

| 工具 | 用途 |
|---|---|
| `.tools/go` | 便携版 Go 1.27（`. .tools/env.sh` 后用 `$GO`） |
| `cmd/recorder` | 协议录制客户端：连接任意服务端，把收到的包按官方名存为 .bin |
| `.tools/vanilla/server` | 官方 26.1 参考服（25566，离线平坦世界），行为对照用 |
| `.tools/vanilla/generated` | 官方数据报告（packets.json、blocks.json、registries.json） |
| `.tools/mcdata` | minecraft-data 26.1 数据 + 抓包分析脚本 |

**更新嵌入资产的方法**（升级版本或修改世界模板时）：

```bash
cd .tools/vanilla/server && java -jar server.jar nogui   # 启动官方参考服
cd ../.. && ./recorder.exe -addr 127.0.0.1:25566 -out .tools/captures -t 10s
# 把抓到的 registry_*/chunk/known_packs .bin 拷入 internal/assets/data/ 对应文件名
```

**新增协议版本**：运行官方 `--reports` 拿到新版 packets.json → 生成 `packets_NNN.go` →
在 `version.go` 的 `supported` 表登记 → 比对差异包的编解码。

## 路线图

- [x] **F1** 协议线格式 + 分帧/压缩 + Status ping
- [x] **F2** 离线登录、配置流（注册表）、进服、平坦世界、移动/聊天、多玩家互见、事件总线
- [ ] **G1** Game/Arena/Team 抽象、地图模板加载（真实区块生成与下发）、物品栏 API、示例玩法
- [ ] **G2** 起床战争模块
- [ ] **G3** 多人逃杀模块
- [ ] **G4** 多版本回填（26.2/26.3/1.21.x）、正版验证（Mojang session server）、AES-CFB8 加密

## 测试

```bash
. .tools/env.sh && "$GO" test ./internal/...
```

- `protocol`：VarInt/NBT/Position 往返与官方样例字节
- `conn`：分帧/压缩往返（含"低于阈值包带 0x00 标记"的字节级断言）
- `packet`：**黄金字节测试**——与官方服务端抓包逐字节比对（player_position、entity_position_sync）
