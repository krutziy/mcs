# mcserver-26.3 —— 类原版生存端（26.3 分支）

从 `main`（空壳）分叉出的**类原版生存服务端**：目标是用 Go 提供与官方 Java 服务端
行为等价的原版生存体验，主线跟随最新版本 **26.3（协议 777）**，同时天然继承空壳的
多版本同服能力（1.21.11 / 26.1 / 26.2 / 26.3 可同时在线）。

> 本分支默认世界为噪声地形（`world.type: noise`）、生存模式、按地形自动选取出生点。
> 空壳的核心与插件机制说明见 `main` 分支 README。

## 当前状态

- [x] 继承空壳全部能力（多版本、离线登录、多人互见、聊天、事件总线）
- [x] 地形生成（噪声起伏 + 海平面 + 沙滩，区块编码与官方逐字节一致）
- [x] 按地形自动出生点（config `spawn` y 设为 0）
- [ ] **生存要素路线图**（自上而下逐步实现）：
  - [ ] 方块交互：破坏/放置 + 服务端权威方块存储 + 区块增量更新
  - [ ] 世界持久化：区块存盘/读盘（退出重进世界不变）
  - [ ] 物品栏与物品：快捷栏/背包、挖掘掉落、拾取
  - [ ] 生命值/饥饿/伤害/死亡重生
  - [ ] 昼夜循环与世界时间持久化
  - [ ] 生物生成与 AI（先被动后敌对）
  - [ ] 合成/熔炉等原版机制
  - [ ] 正版验证（Mojang session server）与 AES-CFB8 加密

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
- 方块交互 → `internal/game/session.go`（block_dig/block_place 入站包）+ `internal/world`（方块存储）
- 持久化 → `internal/world`（Chunk 存取盘）
- 生命值等状态 → `internal/entity` + 新增 play 状态包编解码（`internal/packet`）
- 每加一个包先看官方表：`.tools/v263/generated/reports/packet_table.txt`
