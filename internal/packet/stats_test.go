package packet

import (
	"os"
	"testing"

	"mcserver/internal/protocol"
)

// TestJoinGameMatchesVanilla263 与官方 26.3 服务端 login 抓包
// （.tools/cap263/039_play_minecraft_login.bin）逐字节一致。
// 参数即官方录制会话的实际值（含 777 特有的末尾布尔字节）。
func TestJoinGameMatchesVanilla263(t *testing.T) {
	w := protocol.NewWriter()
	WriteJoinGame(w, JoinGame{
		EntityID:            6,
		WorldNames:          []string{"minecraft:overworld", "minecraft:the_nether", "minecraft:the_end"},
		MaxPlayers:          5,
		ViewDistance:        4,
		SimulationDistance:  4,
		EnableRespawnScreen: true,
		DimensionID:         0,
		WorldName:           "minecraft:overworld",
		HashedSeed:          0x1bc78cabee737695,
		GameMode:            ModeSurvival,
		PreviousGameMode:    0,
		IsFlat:              true,
		SeaLevel:            -63,
		TailApplies:         true,
	})
	want, err := os.ReadFile("../../../.tools/cap263/039_play_minecraft_login.bin")
	if err != nil {
		t.Skip("官方抓包资产不可用:", err)
	}
	if string(w.Bytes()) != string(want) {
		t.Errorf("login 编码与官方不符\n got  %x\n want %x", w.Bytes(), want)
	}
}

// TestSetHealthGolden set_health：Float + VarInt + Float。
func TestSetHealthGolden(t *testing.T) {
	w := protocol.NewWriter()
	WriteSetHealth(w, 17.5, 18, 4.2)
	// 17.5f = 0x418C0000, 18 = 0x12, 4.2f = 0x40866666
	want := mustHex(t, "418c00001240866666")
	if string(w.Bytes()) != string(want) {
		t.Errorf("set_health = %x, want %x", w.Bytes(), want)
	}
}

// TestDamageEventGolden damage_event 无位置源形式。
func TestDamageEventGolden(t *testing.T) {
	w := protocol.NewWriter()
	WriteDamageEvent(w, 101, 14, -1, -1) // 摔落
	want := mustHex(t, "650effffffff0fffffffff0f") // VarInt(-1) 各 5 字节
	if string(w.Bytes()) != string(want) {
		t.Errorf("damage_event = %x, want %x", w.Bytes(), want)
	}
}
