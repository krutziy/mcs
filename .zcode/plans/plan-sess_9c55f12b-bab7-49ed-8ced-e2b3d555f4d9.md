# Go 多人游戏服务端框架（小游戏定制方向）— 实施计划

## 定位

不追求复刻原版生存服。做一个**框架核心**：协议/网络、登录、多人并发、事件驱动架构。之后在框架上以独立模块形式开发起床战争、多人逃杀等小游戏定制服务端。

## 架构

```
D:\zwork\mc\
├── go.mod / config.toml / README.md(中文)
├── cmd/server/main.go        # 入口
└── internal/
    ├── protocol/  # 线格式：VarInt、String、Position、UUID、NBT 编解码
    ├── net/       # 连接层：分帧、zlib 压缩、状态机
    │              #   Handshaking → Status / Login → Configuration → Play
    ├── version/   # 多版本兼容：握手协议号 → version.Suite(包ID映射)
    │              #   先实现最新版(26.3=777)，旧版本以后按 Suite 回填
    ├── packet/    # 各状态包结构体与编解码器
    ├── world/     # 轻量世界：区块(调色板)、平坦世界生成；小游戏地图模板后期加
    ├── entity/    # 玩家实体、位置/视角、实体同步
    ├── game/      # ★框架扩展点：事件总线(EventBus)、Game/Arena 接口占位、tick 调度
    └── server/    # 监听、接受连接、配置装配
```

## 多人并发模型（核心设计）

- **每个连接一个 goroutine** 负责读包、解帧，解析后封装为事件投入 channel
- **单个游戏主循环 goroutine**（20 TPS）消费所有玩家事件、推进逻辑——游戏逻辑单线程化，与 Minecraft 原版语义一致，写玩法模块时**不需要锁**
- 需要广播的输出（位置同步、聊天）由主循环写入各会话的发送 channel，每连接一个写 goroutine
- 事件总线：玩法模块订阅 Join/Quit/Move/Chat/Interact 等事件即可扩展行为，不侵入核心

## 里程碑

| 阶段 | 内容 | 验收 |
|---|---|---|
| **F1** | 协议线格式层+测试、分帧/压缩、状态机、Status ping | 服务器列表显示 MOTD |
| **F2** | 离线登录、Configuration、进服、平坦世界、移动/聊天、多玩家互见、tick 循环、事件总线 | **多个客户端同时在线，互相看到移动和聊天** |
| G1(下轮) | Game/Arena/Team 抽象、地图模板加载、物品栏 API、示例小游戏 | 一个示例玩法跑通 |
| G2+(以后) | 起床战争模块、逃杀模块、多版本回填、正版验证 | — |

## 本次执行范围：F1 + F2

## 关键技术决策

- 纯 Go 标准库，零第三方依赖
- 离线模式登录（任意用户名，UUID 离线派生）；加密/正版验证后期配置项
- 先只实现 protocol 777（26.3），但版本抽象层从第一天就存在
- 框架期世界用平坦世界，不做存盘（小游戏地图由模板加载，不需要生存存档）

## 执行步骤

1. 检查/安装 Go 环境
2. go.mod + 目录骨架
3. protocol 线格式层 + 单元测试(VarInt/NBT/Position)
4. net 分帧/压缩/状态机 + Status ping，go test 验证
5. Login/Configuration/Join Game(注册表用最小 NBT 集)、平坦世界区块下发
6. 会话/主循环/事件总线：位置同步、聊天广播、KeepAlive
7. go build + go test 全绿；README：运行方法、架构图、并发模型讲解、如何写一个玩法模块（方便后续接手）