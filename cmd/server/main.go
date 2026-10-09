// mcserver 是一个面向小游戏定制的 Minecraft Java 版服务端框架。
// 当前支持 26.1（协议 775），离线登录，多玩家可见、聊天、事件总线。
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	"mcserver/internal/server"
)

func main() {
	cfgPath := flag.String("config", "config.json", "配置文件路径")
	flag.Parse()

	cfg := server.DefaultConfig()
	if b, err := os.ReadFile(*cfgPath); err == nil {
		if err := json.Unmarshal(b, &cfg); err != nil {
			fmt.Println("配置解析失败，使用默认配置:", err)
		}
	} else {
		// 写出默认配置方便修改
		if b, err := json.MarshalIndent(cfg, "", "  "); err == nil {
			_ = os.WriteFile(*cfgPath, b, 0o644)
		}
	}

	srv, err := server.New(cfg)
	if err != nil {
		fmt.Println("启动失败:", err)
		os.Exit(1)
	}
	if err := srv.ListenAndServe(); err != nil {
		fmt.Println("服务退出:", err)
		os.Exit(1)
	}
	_ = time.Now()
}
