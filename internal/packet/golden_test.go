package packet

import (
	"encoding/hex"
	"testing"

	"mcserver/internal/protocol"
)

// 黄金字节来自官方 26.1 服务端抓包（.tools/captures，录制方法见 README）。
// 这些测试保证我们的编码器与官方实现逐字节一致。

func TestPlayerPositionMatchesVanilla(t *testing.T) {
	golden := mustHex(t, "01c00c000000000000c04e0000000000004021000000000000000000000000000000000000000000000000000000000000000000000000000000000000")
	w := protocol.NewWriter()
	WritePlayerPosition(w, PlayerPositionOut{
		TeleportID: 1,
		X:          -3.5,
		Y:          -60.0,
		Z:          8.5,
	})
	if w.Err != nil {
		t.Fatal(w.Err)
	}
	if hex.EncodeToString(w.Bytes()) != hex.EncodeToString(golden) {
		t.Errorf("player_position 编码不符\n got  %x\n want %x", w.Bytes(), golden)
	}
}

func TestEntityPositionSyncMatchesVanilla(t *testing.T) {
	golden := mustHex(t, "57c03d5fbce265913fc04e000000000000401fa1581353ecef3facfa743" +
		"1748eb2bfb41205c28f5c293f846fd3a4301eab438b80000000000001")
	w := protocol.NewWriter()
	WriteEntityPositionSync(w, 87,
		-29.373975896641465, -60.0, 7.907562543866546,
		0.05659831145956441, -0.0784000015258789, 0.00997891754924692,
		279.0, 0.0, true)
	if w.Err != nil {
		t.Fatal(w.Err)
	}
	if hex.EncodeToString(w.Bytes()) != hex.EncodeToString(golden) {
		t.Errorf("entity_position_sync 编码不符\n got  %x\n want %x", w.Bytes(), golden)
	}
}

func TestJoinGameFieldOrder(t *testing.T) {
	// 结构性往返：编码后按官方字段顺序手工解码
	w := protocol.NewWriter()
	WriteJoinGame(w, JoinGame{
		EntityID:            101,
		WorldNames:          []string{"minecraft:overworld"},
		MaxPlayers:          20,
		ViewDistance:        3,
		SimulationDistance:  3,
		EnableRespawnScreen: true,
		DimensionID:         0,
		WorldName:           "minecraft:overworld",
		GameMode:            ModeCreative,
		PreviousGameMode:    255,
		IsFlat:              true,
		SeaLevel:            -63,
	})
	if w.Err != nil {
		t.Fatal(w.Err)
	}
	r := protocol.NewReader(w.Bytes())
	if got := r.Int32(); got != 101 {
		t.Errorf("entityId = %d", got)
	}
	if r.Bool() {
		t.Error("hardcore 应为 false")
	}
	if n := r.VarInt(); n != 1 {
		t.Fatalf("worldNames count = %d", n)
	}
	if s := r.String(); s != "minecraft:overworld" {
		t.Errorf("worldName = %q", s)
	}
	if v := r.VarInt(); v != 20 {
		t.Errorf("maxPlayers = %d", v)
	}
	if v := r.VarInt(); v != 3 {
		t.Errorf("viewDistance = %d", v)
	}
	if v := r.VarInt(); v != 3 {
		t.Errorf("simDistance = %d", v)
	}
	if r.Bool() {
		t.Error("reducedDebug")
	}
	if !r.Bool() {
		t.Error("respawnScreen 应为 true")
	}
	if r.Bool() {
		t.Error("limitedCrafting")
	}
	if v := r.VarInt(); v != 0 {
		t.Errorf("dimensionID = %d", v)
	}
	if s := r.String(); s != "minecraft:overworld" {
		t.Errorf("dimension name = %q", s)
	}
	_ = r.Int64() // seed
	if v := r.VarInt(); v != 1 {
		t.Errorf("gamemode = %d", v)
	}
	if v := r.VarInt(); v != 255 {
		t.Errorf("prevGamemode = %d", v)
	}
	if r.Bool() {
		t.Error("isDebug")
	}
	if !r.Bool() {
		t.Error("isFlat 应为 true")
	}
	if v := r.VarInt(); v != 0 {
		t.Errorf("portalCooldown = %d", v)
	}
	if v := r.VarInt(); v != -63 {
		t.Errorf("seaLevel = %d", v)
	}
	if r.Bool() {
		t.Error("enforcesSecureChat")
	}
	if r.Err != nil {
		t.Fatal(r.Err)
	}
	if r.Remaining() != 0 {
		t.Errorf("剩余 %d 字节未消费", r.Remaining())
	}
}

func TestSystemChatNBT(t *testing.T) {
	w := protocol.NewWriter()
	WriteSystemChat(w, "你好", false)
	if w.Err != nil {
		t.Fatal(w.Err)
	}
	r := protocol.NewReader(w.Bytes())
	v := protocol.ReadNBT(r)
	c, ok := v.(protocol.Compound)
	if !ok || c["text"] != "你好" {
		t.Errorf("NBT 文本组件不符: %#v", v)
	}
	if r.Bool() {
		t.Error("actionBar 应为 false")
	}
}

func mustHex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
