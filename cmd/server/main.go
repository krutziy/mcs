// mcserver 是 Minecraft Java 版服务端【空壳】：
// 只包含协议/登录/多人并发/事件总线/空世界等服务器必备核心。
//
// 附加能力（地形生成等）以插件形式装配在下方导入区——
// 删除对应导入行即得到不含该功能的纯壳。
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"mcserver/internal/server"

	// ---- 附加功能装配区（插件在此注册，删除行即移除功能） ----
	_ "mcserver/plugins/terrain" // 地形生成：flat / noise 世界类型
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
}
