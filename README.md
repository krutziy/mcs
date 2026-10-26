# mcserver-26.3 —— 类原版生存端（26.3 分支）

从 `main`（空壳）分叉出的**类原版生存服务端**：目标是用 Go 提供与官方 Java 服务端
行为等价的原版生存体验，主线跟随最新版本 **26.3（协议 777）**，同时天然继承空壳的
多版本同服能力（1.21.11 / 26.1 / 26.2 / 26.3 可同时在线）。

> 本分支默认世界为噪声地形（`world.type: noise`）、生存模式、按地形自动选取出生点，
> 修改过的区块与玩家状态持久化到 `world.save_dir`（默认 `world_data/`），
> 30 秒自动存档 + 退出/中断保存。空壳的核心与插件机制说明见 `main` 分支 README。

## 当前状态

- [x] 继承空壳全部能力（多版本、离线登录、多人互见、聊天、事件总线）
- [x] 地形生成（噪声起伏 + 海平面 + 沙滩，区块编码与官方逐字节一致）
- [x] 按地形自动出生点（config `spawn` y 设为 0）
- [ ] **生存要素路线图**（自上而下逐步实现）：
  - [x] 方块交互：破坏/放置 + 服务端权威方块存储 + 区块增量更新
    （26.3/1.21.11/26.2 可放；26.1 暂无物品映射表只可挖。生存模式挖掘
    时长校验未做，finish 即破坏；创造 start 即时破坏）
  - [x] 世界持久化：修改过的区块存盘/读盘（自有 gzip 格式）+ 玩家状态
    （位置/物品栏）持久化 + 时间存档待做
  - [x] 物品栏与物品：46 槽物品栏 + 挖掘掉落（官方 jar 战利品表生成，
    含"需镐"材质门槛）+ 掉落物实体 + 自动拾取 + 生存放置消耗
  - [x] 生命值/饥饿/伤害/死亡重生：set_health/damage_event/respawn 编解码，
    摔落（距离-3）与虚空伤害、死亡动画与重生流程、移动消耗体力、
    食物≥18 回血、饥饿见底掉血（止于半心）、进食（use_item + 食物表）
  - [x] 昼夜循环与世界时间持久化（time.json，30 秒随世界自动存档）
  - [x] 生物生成与 AI：被动群（猪/羊/牛/鸡/兔）草方块定时生成、游荡 AI
    （边缘感知不坠崖）、贴地物理、夜间僵尸追击攻击玩家、attack 打击
    （剑/斧伤害表）、击退、死亡掉落（官方 jar 战利利表 mobdrops）、
    远离玩家消散、PVP 互击；骷髅/苦力怕等特殊 AI 未做
  - [x] 合成：官方 jar recipe 数据 1076 张配方（含物品标签引用展开），
    随身 2x2 + 3x3 工作台（右键工作台方块 open_screen，关闭材料退回）；
    客户端本地预测结果图标、服务端点击结果槽验证并扣材料；熔炉（jar smelting 配方 123 条、燃料热值、燃烧 lit 状态切换、进度条、状态持久化、挖掉掉落）
  - [x] AES-CFB8 加密 + 正版验证：config `online_mode=true` 启用
    （RSA 握手 → Mojang hasJoined → 双向 CFB8 流加密；默认离线模式不受影响，
    在线流程需真实正版客户端联测）

## 关于"Java 转 Go"的实现方式（重要）

本分支**不是**对官方 Java 服务端的代码移植，而是**行为等价重写**（clean-room
reimplementation）：所有协议格式与游戏行为通过抓包观察、官方数据报告和黑盒实验
推导，Go 代码全部原创。这与 Glowstone（Java）、Cuberite（C++）、Minestom 等
长期公开存在的第三方服务端性质相同。

### 合规边界（EULA / 版权）

Minecraft EULA 约束的是 Mojang 软件本身的使用。本项目：

✅ **安全区**（本项目所做）
- 依据公开协议文档、网络抓包、官方 `--reports` 数据生成输出做协议实现
- 原创实现游戏机制（自己的方块物理、物品、AI 代码）
- 长期先例：Glowstone / Cuberite / Minestom / Prismarine 等开源服务端存在十余年，
  Mojang/Microsoft 从未针对清洁室服务端采取行动，且官方生态明确容纳第三方服务端

⚠️ **不要做**
- 不要把反编译出的 Mojang Java 源码逐行翻译成 Go（衍生作品，侵犯版权且违反 EULA）
- 不要再分发官方 jar、材质、音效等受版权保护的资源
- 不要在命名/宣传上暗示官方出品（避免 "Minecraft 官方" 类表述）

📋 **运营注意**
- 公开运营服务器需遵守 [Minecraft Usage Guidelines](https://www.minecraft.net/en-us/usage-guidelines)
  （品牌使用、商业化限制等），这与任何第三方服务端一致

## 开发

```bash
. .tools/env.sh
"$GO" build -o server.exe ./cmd/server/
./server.exe     # 噪声地形 + 生存模式，26.3 客户端直连 127.0.0.1:25565
```

生存要素的实现入口：
- 方块交互 → `internal/game/block.go`（挖掘/放置/广播/ack）+ `internal/game/session.go`（block_dig/block_place 入站包）+ `internal/world/manager.go`（服务端权威方块存储）
- 持久化 → `internal/world`（Chunk 存取盘）
- 生命值等状态 → `internal/entity` + 新增 play 状态包编解码（`internal/packet`）
- 每加一个包先看官方表：`.tools/v263/generated/reports/packets.json`

### 方块交互的抓包验证（26.3）

相关包格式均经官方参考服黑盒对拍（录制资产 `.tools/capBlock263*`，方法：
`DIG=1 SURVIVAL_WAIT=1 PLACE=1 ./recorder.exe -addr 127.0.0.1:25566 ...`）：

| 包 | 方向 | 格式要点 |
|---|---|---|
| `block_update` | 下行 | 位置 long（x26/z26/y12）+ VarInt 状态 |
| `section_blocks_update` | 下行 | 节坐标 long（x22/z22/y20）+ VarInt 数 + VarLong（状态<<12 \| lx<<8\|lz<<4\|ly）|
| `block_changed_ack` | 下行 | VarInt 序列号 |
| `player_action` | 上行 | VarInt 状态 + 位置 + **字节**面 + VarInt 序列 |
| `use_item_on` | 上行 | VarInt 手 + 位置 + **VarInt** 面 + 3×光标 + 2×布尔 + VarInt 序列 |
| `set_creative_mode_slot` | 上行 | Short 槽位 + Slot 物品（无组件时 present+ID+数量+0+0）|
| `set_carried_item` | 上行 | Short 槽位 |
| `level_event` | 下行 | Int 事件 + 位置 + Int 数据 + 布尔（破坏=2001，数据=方块状态）|

经验证的原版行为（我们的实现已对齐）：
- 放置被拒（空手/目标被占）回「点击位+目标位」两个 block_update + ack（幽灵方块回滚）
- 机器人必须发 `player_loaded` 并持续 `client_tick_end`，否则官方服丢弃挖掘 start
- 26.3 的 `punch`（挥手）是空载荷

物品→方块映射表由 `cmd/genitemblocks` 从官方 reports 生成
（`internal/assets/data/itemblocks_<proto>.txt`，世界统一用 777 状态空间）。

### 从官方 jar 提取数据（合规边界内）

**只读取 jar 内的 JSON 数据文件（原版数据包），不反编译任何 .class 代码。**
`cmd/gendrops` 解开 server.jar（bundler 壳 → 内层 jar）读取：
- `data/minecraft/loot_table/blocks/*.json` → 挖掘掉落表（`blockdrops_<proto>.txt`，
  取首个非精准采集掉落、数量 1 的简化）
- `data/minecraft/tags/block/mineable|incorrect_for_*.json` → 需镐方块的
  材质位掩码门槛（`mineable_` / `pickaxes_`）
- `cmd/genitemblocks` 同时输出跨版本翻译表（`statemap_` / `itemmap_`，按
  同名方块+同属性组合/同名物品匹配）：世界内部统一 777 状态空间，
  区块编码与 block_update/掉落物显示按玩家协议版本即时翻译，
  1.21.11/26.2 客户端看到的方块与物品也是正确的。

### 已知简化（后续里程碑完善）

- 挖掘掉落数量恒 1，精准采集/时运未实现；挖掘时长校验未做（客户端节奏为准）
- 物品实体物理为单点近似（无水面漂浮/合并）；非堆叠物品上限按 64
- 进食为即时完成（原版按住 1.6 秒的进度动画未实现）；无溺水/中毒/着火状态伤害
- respawn 包的 26.3 插入字段按 login 同构处理（未单独抓包验证）
- 生物为简化 AI（无寻路/无骷髅射箭/无苦力怕爆炸）；攻击无暴击/击退抗性
- 拖拽/shift 点击等容器高级操作未实现（点击后全量同步会回滚客户端预测）
  - [x] 农业链：锄头耕地、种麦、生长、收获
  - [x] 生物群系：温度/湿度噪声划分 沙漠/平原/雪原/森林/针叶林（沙岩/枯木/仙人掌、雪层、密度草地、桦/云杉树种）
  - [x] 骨粉：小麦跳龄催熟、树苗立即成树
  - [x] 酿造与药水：官方 jar brewing 数据 153 张配方、酿造台窗口（三药水槽
    +材料，400 刻/次）、药水物品带 potion_contents 组件、饮用生效
    （治疗/速度/力量/再生/防火/夜视/水下呼吸/跳跃/缓降）、效果计时与
    update_mob_effect 同步、状态持久化
  - [x] 附魔台：官方 jar enchantment 数据 43 种（权重/最高级/适用标签），
    三档消耗（等级+青金石）、加权随机抽取、enchantments 组件（ID 23）
    线上下发；锋利/亡灵杀手/节肢杀手攻击加成、保护系附魔减伤生效
  - [x] 红石（基础）：信号源（红石火把/红石块/拉杆）、红石线传输
    （15 格衰减、per-power 状态族）、中继器（整流+再放大）、红石灯负载、
    火把反相器（下方被充能则熄灭）、变更触发全网重算（2 刻节拍）；
    活塞/比较器/观测器未做
