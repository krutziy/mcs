# mcserver —— Minecraft Java 版服务端空壳（main 分支）

用 Go 从零实现的多版本 Minecraft 服务端框架。**main 分支定位是"全空的世界壳子"**：
只包含搭建服务器必备的核心——协议编解码、离线登录、多人并发、事件总线、空世界（虚空）。

地形生成等一切附加能力都是**插件**（`plugins/` 目录），按需装配、删行即卸。

## 分支结构

| 分支 | 定位 |
|---|---|
| `main` | 空壳：协议/登录/并发/事件总线/虚空世界 + 插件机制 |
| `26.3` | 类原版生存端（以行为等价方式用 Go 重写，主线跟随最新 26.3） |
| 未来 | 起床战争、逃杀等玩法分支，均从 main 叉出 |

## 核心能力（空壳自带）

- **多版本同服**：1.21.11 / 26.1 / 26.2 / 26.3（协议 774-777），不同版本客户端同时在线互见
- 登录：离线模式（任意用户名，默认）或正版验证（`online_mode: true`：AES-CFB8 加密通道 + Mojang session server hasJoined 校验，取正版 UUID/皮肤属性）
- 服务器列表 ping、20TPS 主循环、KeepAlive
- 事件总线（玩法扩展点）：`PlayerJoin / PlayerQuit / PlayerMoved / PlayerChatted / PlayerCommand / Tick`
- 空世界（void）：客户端可进可动，世界为纯虚空——一切内容由插件注入
- 内置命令：`/list` `/spawn` `/help`

## 插件机制

插件 = 一个在 `init()` 中向核心注册能力的 Go 包，装配在 `cmd/server/main.go` 的导入区：

```go
// ---- 附加功能装配区（插件在此注册，删除行即移除功能） ----
_ "mcserver/plugins/terrain" // 地形生成：flat / noise 世界类型
```

现有插件：

| 插件 | 注册内容 | 说明 |
|---|---|---|
| `plugins/terrain` | 世界类型 `flat` / `noise` | 超平坦、分形噪声起伏地形+海平面；区块编码与官方逐字节一致 |

写一个新世界插件只需要：

```go
package myworld

func init() {
    world.RegisterGenerator("myworld", func(seed int64) world.Generator {
        return MyGenerator{seed}
    })
}
```

## 快速开始

```bash
. .tools/env.sh          # 项目内便携版 Go（不污染系统）
"$GO" build -o server.exe ./cmd/server/
./server.exe             # 读取 config.json（首次自动生成）
```

config.json 的 `world.type`：`void`（空壳默认）/ `flat` / `noise`（需装配 terrain 插件）。
config.json 的 `online_mode`：`false`（默认，任意用户名离线登录）/ `true`（正版验证）。

### 正版验证（online_mode: true）

login 阶段发送 clientbound `minecraft:hello`（服务器 RSA 公钥 + verify token），
客户端回 `minecraft:key`（RSA 加密的共享密钥），随后双向启用 AES-CFB8 加密，
再以认证哈希（`SHA-1(serverID="" + secret + 公钥)` 的 Java BigInteger 表示）调用
Mojang `hasJoined` 校验身份，取正版 UUID 与皮肤属性写入 login_finished；
验证失败以明确原因断开。key 包的 RSA 填充为 PKCS#1 v1.5
（对 1.21.11 与 26.3 官方服实测确认，服务器端另保留 OAEP-SHA1 兜底）。

## 架构

```
cmd/server        壳子入口（插件装配区）
cmd/recorder      协议录制客户端（开发工具）
plugins/          ★附加功能（地形生成等，main 不主动依赖）
internal/
├── protocol/     线格式：VarInt、String、Position、UUID、NBT
├── conn/         连接层：分帧、zlib 压缩、包读写
├── version/      多版本抽象：Suite 包ID表 + 差异标志
├── packet/       各状态包编解码（官方字节级回归测试）
├── assets/       按版本录制的注册表/known packs 资产
├── entity/       玩家状态
├── game/         事件总线、会话、20TPS 主循环
├── server/       TCP 监听、状态机、config.json
└── world/        区块数据结构与编码 + 生成器注册表（默认虚空）
```

### 并发模型（写玩法不需要锁）

```
连接读goroutine ×N ──→ events channel ──→ 单个游戏主循环(20TPS)
                                            │ 事件总线分发（玩法回调）
                             各会话写（conn 内部带锁）←┘
```

## 多版本兼容原理

| 差异点 | 处理方式 |
|---|---|
| 包 ID（每版不同） | 每版本一张 Suite 表，由官方 `--reports` 的 packets.json 生成 |
| 注册表/known packs | 按版本录制官方 registry_data 包体原样回放（internal/assets/data/<协议号>/） |
| 区块编码 | `world.ChunkStyle`：1.21.x 节内**方块在前/平原群系40/高度图序[1,5,4]**；26.x **群系在前/群系0/高度图序[4,5,1]/特殊8字节空节** |
| set_time | 1.21.x 经典三字段；26.x 时钟列表（`version.Version.ClassicTime`） |
| accept_teleportation | 26.3 起回显位置朝向（`ConfirmEchoesPos`） |
| 其他包 | 四版本逐字节比对确认同构，直接共用编解码 |

新增版本三步：官方 jar `--reports` → 生成 `packets_NNN.go` → 录制资产 + 登记差异标志。

## 合规说明

本项目是**协议层的清洁室重实现**（clean-room）：不含任何 Mojang 代码或资源文件，
格式知识来自对官方服务端的网络抓包与官方公开的数据生成报告，这与 Glowstone、
Cuberite、Minestom 等长期公开存在的第三方服务端同一性质。注意：
不要直接翻译/复制 Mojang 反编译源码（那是衍生作品）；不要再分发官方 jar、
材质、音效等资源；公开运营需遵守 Mojang 的商业使用准则。详见仓库 `26.3` 分支
README 的完整讨论。

## 开发工具链（.tools/，已 gitignore）

| 工具 | 用途 |
|---|---|
| `.tools/go` | 便携版 Go（`. .tools/env.sh` 后用 `$GO`） |
| `cmd/recorder` | 协议录制客户端：连任意服务端按官方包名存包；`-online` 可回应加密握手（正版模式对照，验证至 hasJoined 请求构造） |
| `.tools/v263` 等 | 各版本官方参考服 + `--reports` 数据 |

**更新嵌入资产**（升级版本时）：

```bash
cd .tools/vXXX/server && java -jar server.jar nogui   # 官方参考服
./recorder.exe -addr 127.0.0.1:25566 -out .tools/capX -t 6s -version XXX
# 把抓到的 registry_*/known_packs .bin 拷入 internal/assets/data/<协议号>/
```

## 测试

```bash
. .tools/env.sh && "$GO" test ./...
```

- `protocol`：VarInt/NBT/Position 往返与官方样例字节
- `conn`：分帧/压缩往返（含"低于阈值包带 0x00 标记"的字节级断言）；AES-CFB8 用 NIST SP 800-38A F.3.7/9/11 官方向量 + JDK 交叉验证的长流向量 + 加密/压缩组合往返
- `packet`：黄金字节测试——与官方抓包逐字节比对（player_position、entity_position_sync）
- `server`：认证哈希（Java BigInteger 风格 SHA-1）公开向量与组合向量、hasJoined 的 200/204/500 行为
- `world`：虚空区块结构 ×4 版本、生成器注册表行为
- `plugins/terrain`：区块黄金测试 ×2（775/774 生成输出与官方逐字节一致）、噪声结构/确定性
