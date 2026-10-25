package game

// 天气：随机周期切换 晴/雨（雨 1-3 分钟），game_event 13=开始下雨 14=停止
// （官方抓包确认格式：Int8 事件 + Float 值），天气状态持久化随时间存档。

import (
	"encoding/json"
	"log"

	"mcserver/internal/protocol"
	"math/rand"
	"os"
	"path/filepath"

)

const (
	gameEventStartRain int8 = 13
	gameEventEndRain   int8 = 14
)

// tickWeather 天气调度：雨歇随机计时，切换时广播。
func (g *Game) tickWeather() {
	if g.tick%100 != 0 { // 每 5 秒决策一次
		return
	}
	if g.rainTimer > 0 {
		g.rainTimer--
		if g.rainTimer == 0 && g.raining {
			g.setRaining(false)
		}
		return
	}
	// 晴天随机转雨（约 1/40 概率/5s → 平均 3-4 分钟一场）
	if !g.raining && rand.Intn(40) == 0 {
		g.setRaining(true)
		g.rainTimer = 12 + rand.Intn(24) // 雨 1-2 分钟（12-36 个决策周期）
	}
}

// setRaining 切换天气并广播。
func (g *Game) setRaining(r bool) {
	g.raining = r
	ev := gameEventEndRain
	if r {
		ev = gameEventStartRain
	}
	for _, p := range g.players {
		e := ev
		p.Send.SendPlay("minecraft:game_event", func(w *protocol.Writer) {
			w.Int8(e)
			w.Float(0)
		})
	}
	if r {
		g.BroadcastSystem("§7开始下雨了…")
	}
	logf("[game] 天气切换 raining=%v", r)
}

// saveWeather 天气持久化。
func (g *Game) saveWeather() {
	b, _ := json.Marshal(map[string]any{"raining": g.raining, "timer": g.rainTimer})
	_ = os.WriteFile(filepath.Join(g.Cfg.SaveDir, "weather.json"), b, 0o644)
}

// loadWeather 读回天气。
func (g *Game) loadWeather() {
	b, err := os.ReadFile(filepath.Join(g.Cfg.SaveDir, "weather.json"))
	if err != nil {
		return
	}
	var w struct {
		Raining bool `json:"raining"`
		Timer   int  `json:"timer"`
	}
	if json.Unmarshal(b, &w) == nil {
		g.raining, g.rainTimer = w.Raining, w.Timer
	}
}


// logf 天气模块日志。
func logf(format string, a ...any) {
	log.Printf("[weather] "+format, a...)
}
