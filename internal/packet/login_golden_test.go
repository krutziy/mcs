package packet

import (
	"os"
	"testing"

	"mcserver/internal/protocol"
)

// TestLoginFinishedMatchesVanilla 各版本 login_finished 与官方抓包逐字节一致
// （26.3 真实客户端曾因缺失尾部 16 字节而解码失败，此测试锁定修复）。
func TestLoginFinishedMatchesVanilla(t *testing.T) {
	cases := []struct {
		proto  int32
		name   string
		golden string
	}{
		{774, "V1.21.11", "../../../.tools/final_1.21.11/001_preplay_minecraft_login_finished.bin"},
		{775, "V26.1", "../../../.tools/final_26.1/032_preplay_minecraft_login_finished.bin"},
		{776, "V26.2", "../../../.tools/final_26.2/033_preplay_minecraft_login_finished.bin"},
		{777, "Recorder", "../../../.tools/cap263/001_preplay_minecraft_login_finished.bin"},
	}
	for _, c := range cases {
		want, err := os.ReadFile(c.golden)
		if err != nil {
			t.Skipf("官方抓包资产不可用: %v", err)
		}
		// 从官方包体读回 UUID（前 16 字节），保证参数一致
		var uuid protocol.UUID
		copy(uuid[:], want[:16])
		w := protocol.NewWriter()
		WriteLoginFinishedFor(w, uuid, c.name, c.proto)
		if string(w.Bytes()) != string(want) {
			t.Errorf("proto %d login_finished 与官方不符\n got  %x\n want %x", c.proto, w.Bytes(), want)
		}
	}
}
