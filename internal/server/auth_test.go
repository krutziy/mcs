package server

import (
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"testing"

	"mcserver/internal/protocol"
)

// Java BigInteger.toString(16) 风格哈希的公开向量（wiki.vg 经典示例），
// 已与 JDK BigInteger / Python 独立实现双重核对一致。
func TestAuthHashJavaStyleVectors(t *testing.T) {
	cases := []struct{ in, want string }{
		{"Notch", "4ed1f46bbe04bc756bcb17c0c7ce3e4632f06a48"},
		{"jeb_", "-7c9d5b0044c130109a5d7b5fb5c317c02b4e28c1"},
		{"simon", "88e16a1019277b15d58faf0541e11910eb756f6"},
	}
	for _, tc := range cases {
		if got := AuthHash(tc.in, nil, nil); got != tc.want {
			t.Errorf("AuthHash(%q) = %s, 期望 %s", tc.in, got, tc.want)
		}
	}
}

// 组合向量（serverID + 16 字节密钥 + 16 字节公钥），期望值由 Python
// hashlib 独立计算（Java BigInteger 规则）。
func TestAuthHashCombinedVector(t *testing.T) {
	secret := make([]byte, 16)
	pub := make([]byte, 16)
	for i := 0; i < 16; i++ {
		secret[i], pub[i] = byte(i), byte(i+16)
	}
	if got := AuthHash("", secret, pub); got != "-51a4271015acdd3b266792f997f587ec6d0659be" {
		t.Errorf("AuthHash(空) = %s", got)
	}
	if got := AuthHash("Notch", secret, pub); got != "6c48ae7aefacda0f277c11afd7bc824265d9bfd4" {
		t.Errorf("AuthHash(Notch) = %s", got)
	}
}

func TestUUIDFromUndashed(t *testing.T) {
	u, err := uuidFromUndashed("069a79f444e94726a5befca90e38aaf5")
	if err != nil {
		t.Fatal(err)
	}
	if hex.EncodeToString(u[:]) != "069a79f444e94726a5befca90e38aaf5" {
		t.Errorf("uuid = %x", u)
	}
	// 带连字符的输入也应被接受（Mojang 风格两种都有）
	if _, err := uuidFromUndashed("069a79f4-44e9-4726-a5be-fca90e38aaf5"); err != nil {
		t.Errorf("带连字符 UUID 报错: %v", err)
	}
	if _, err := uuidFromUndashed("xyz"); err == nil {
		t.Errorf("非法 UUID 应报错")
	}
}

// hasJoined 的 HTTP 行为：200 解析、204 拒绝、非 200 错误。
func TestHasJoinedHTTP(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("username") == "Notch" && q.Get("serverId") == "-7c9d5b0044c130109a5d7b5fb5c317c02b4e28c1" {
			w.Write([]byte(`{"id":"069a79f444e94726a5befca90e38aaf5","name":"Notch",` +
				`"properties":[{"name":"textures","value":"BASE64","signature":"SIG"}]}`))
			return
		}
		if q.Get("username") == "Ghost" {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	a := &authClient{http: srv.Client(), baseURL: srv.URL}

	uuid, name, props, err := a.HasJoined("Notch", "-7c9d5b0044c130109a5d7b5fb5c317c02b4e28c1")
	if err != nil {
		t.Fatalf("成功路径报错: %v", err)
	}
	if hex.EncodeToString(uuid[:]) != "069a79f444e94726a5befca90e38aaf5" || name != "Notch" {
		t.Errorf("uuid=%x name=%q", uuid, name)
	}
	if len(props) != 1 || props[0].Name != "textures" || props[0].Value != "BASE64" || props[0].Signature != "SIG" {
		t.Errorf("props = %+v", props)
	}

	if _, _, _, err := a.HasJoined("Ghost", "x"); err != ErrAuthRejected {
		t.Errorf("204 应返回 ErrAuthRejected，得到 %v", err)
	}
	if _, _, _, err := a.HasJoined("Broken", "x"); err == nil {
		t.Errorf("500 应报错")
	}
	_ = protocol.UUID{}
}
