package game

// 音效系统：minecraft:sound 包（VarInt 音效注册表 ID + VarInt 分类 +
// Int32 坐标 + 音量/音调 f32 + 种子 i64；格式经官方服 410 个抓包验证）。
// 音效名 → ID 按观察者协议版本查 sounds_<proto>.txt（774/776/777）。

import (
	"math/rand"

	"mcserver/internal/entity"
	"mcserver/internal/protocol"
)

// 音效分类（soundSource 注册表）。
const (
	SoundMaster  int32 = 0
	SoundMusic   int32 = 1
	SoundRecord  int32 = 2
	SoundWeather int32 = 3
	SoundBlock   int32 = 4
	SoundHostile int32 = 5
	SoundNeutral int32 = 6
	SoundPlayer  int32 = 7
	SoundAmbient int32 = 8
)

// soundIDFor 音效短名 → 观察者协议的注册表 ID（未知音效 ok=false）。
func (g *Game) soundIDFor(p *entity.Player, name string) (int32, bool) {
	if m := g.soundIDs[g.playerProto(p)]; m != nil {
		id, ok := m[name]
		return id, ok
	}
	return 0, false
}

// playSoundAt 在指定维度位置向区块观察者播放命名音效（按各观察者协议翻译 ID）。
func (g *Game) playSoundAt(dim int32, x, y, z float64, category int32, name string, volume, pitch float32) {
	viewers := g.chunkViewers(dim, int32(x)>>4, int32(z)>>4)
	if len(viewers) == 0 {
		return
	}
	seed := rand.Int63()
	ix, iy, iz := int32(x), int32(y), int32(z)
	for _, v := range viewers {
		id, ok := g.soundIDFor(v, name)
		if !ok {
			continue
		}
		// 距离衰减：听者越远音量越低（原版 max(volume,1)×16 格可闻）
		d := dist2(v.X, v.Y+1.6, v.Z, x, y, z)
		reach := float64(volume)
		if reach < 1 {
			reach = 1
		}
		vol := volume * float32(1-d/(reach*16*reach*16))
		if d > reach*16*reach*16 {
			continue // 超出可闻范围
		}
		if vol < 0.02 {
			continue
		}
		sid, cat := id, category
		v.Send.SendPlay("minecraft:sound", func(w *protocol.Writer) {
			w.VarInt(sid)
			w.VarInt(cat)
			w.Int32(ix)
			w.Int32(iy)
			w.Int32(iz)
			w.Float(vol)
			w.Float(pitch)
			w.Int64(seed)
		})
	}
}

// playSoundTo 仅向单个玩家播放（自身相关音效）。
func (g *Game) playSoundTo(p *entity.Player, category int32, name string, volume, pitch float32) {
	id, ok := g.soundIDFor(p, name)
	if !ok {
		return
	}
	ix, iy, iz := int32(p.X), int32(p.Y), int32(p.Z)
	seed := rand.Int63()
	sid, cat := id, category
	p.Send.SendPlay("minecraft:sound", func(w *protocol.Writer) {
		w.VarInt(sid)
		w.VarInt(cat)
		w.Int32(ix)
		w.Int32(iy)
		w.Int32(iz)
		w.Float(volume)
		w.Float(pitch)
		w.Int64(seed)
	})
}

// ---- 常用音效包装 ----

// blockSound 方块类音效（分类 block，音量 0.8、音调随机 0.8-1.2 原版观感）。
func (g *Game) blockSound(dim int32, x, y, z float64, name string) {
	g.playSoundAt(dim, x, y, z, SoundBlock, name, 0.8, 0.8+rand.Float32()*0.4)
}

// mobSound 生物类音效（被动 neutral / 敌对 hostile）。
func (g *Game) mobSound(m *mob, name string) {
	cat := SoundNeutral
	if m.kind.hostile {
		cat = SoundHostile
	}
	g.playSoundAt(m.dim, m.x, m.y, m.z, cat, name, 1, 0.9+rand.Float32()*0.2)
}

// mobAmbientSoundName 生物种类 → 环境音效名（无则空）。
func mobAmbientSoundName(kind string) string {
	switch kind {
	case "pig", "sheep", "cow", "chicken", "rabbit", "zombie", "skeleton", "blaze":
		return "entity." + kind + ".ambient"
	case "creeper":
		return "entity.creeper.primed"
	case "enderman":
		return "entity.enderman.ambient"
	case "silverfish":
		return "entity.silverfish.ambient"
	case "villager":
		return "entity.villager.ambient"
	case "shulker":
		return "entity.shulker.ambient"
	}
	return ""
}

// mobHurtSoundName / mobDeathSoundName 受击/死亡音效名。
func mobHurtSoundName(kind string) string {
	switch kind {
	case "pig", "sheep", "cow", "chicken", "rabbit", "zombie", "skeleton", "creeper",
		"enderman", "blaze", "silverfish", "shulker", "villager":
		return "entity." + kind + ".hurt"
	}
	return ""
}

func mobDeathSoundName(kind string) string {
	switch kind {
	case "pig", "sheep", "cow", "chicken", "rabbit", "zombie", "skeleton", "creeper",
		"enderman", "blaze", "silverfish", "shulker", "villager":
		return "entity." + kind + ".death"
	}
	return ""
}

// tickMobSounds 生物环境音效：每 8 刻对观察者附近的生物小概率播放。
func (g *Game) tickMobSounds() {
	if g.tick%8 != 0 {
		return
	}
	for _, m := range g.mobs {
		name := mobAmbientSoundName(m.kind.name)
		if name == "" || rand.Intn(3) != 0 {
			continue
		}
		if g.nearestPlayerDistDim(m.dim, m.x, m.y, m.z) > 24 {
			continue
		}
		g.mobSound(m, name)
	}
}
