// Package version 实现多版本兼容的核心抽象：每个协议版本对应一个
// Suite（包 ID 注册表 + 差异化编解码钩子）。握手包中的协议号用于查表，
// 服务器核心逻辑只面向 Suite 接口编写，新增版本 = 新增一份 ID 表。
package version

import "fmt"

// Protocol 是协议版本号（如 775 = 26.1）。
type Protocol int32

// Version 描述一个受支持的协议版本。
type Version struct {
	Protocol Protocol
	Name     string // 显示名，如 "26.1"
}

// 支持的版本列表（新增版本时在此登记并添加对应的包 ID 表）。
var supported = []Version{
	{775, "26.1"},
}

// Lookup 按协议号查找受支持的版本。
func Lookup(p Protocol) (Version, bool) {
	for _, v := range supported {
		if v.Protocol == p {
			return v, true
		}
	}
	return Version{}, false
}

// Latest 返回支持的最新版本。
func Latest() Version { return supported[len(supported)-1] }

// All 返回全部受支持版本。
func All() []Version { return supported }

// Parse 把版本名解析为协议号。
func Parse(name string) (Protocol, error) {
	for _, v := range supported {
		if v.Name == name {
			return v.Protocol, nil
		}
	}
	return 0, fmt.Errorf("version: 未知版本 %q", name)
}

// StateAndDir 标识一个包 ID 表的归属（状态 × 方向）。
type StateAndDir struct {
	State string // handshake/status/login/configuration/play
	To    string // clientbound/serverbound
}

// Suite 是某个协议版本的包 ID 集合，键为官方包名（如 "minecraft:registry_data"）。
type Suite struct {
	Version Protocol
	// IDs[state][direction][packetName] = packetID
	IDs map[string]map[string]map[string]int32
}

// ID 返回指定包的 ID。
func (s *Suite) ID(state, dir, packet string) (int32, bool) {
	d, ok := s.IDs[state]
	if !ok {
		return 0, false
	}
	m, ok := d[dir]
	if !ok {
		return 0, false
	}
	id, ok := m[packet]
	return id, ok
}

// MustID 返回指定包的 ID，未注册时 panic（仅用于开发期断言）。
func (s *Suite) MustID(state, dir, packet string) int32 {
	id, ok := s.ID(state, dir, packet)
	if !ok {
		panic(fmt.Sprintf("version %d: 包 %s/%s/%s 未注册", s.Version, state, dir, packet))
	}
	return id
}

// SuiteFor 返回指定协议版本的 Suite（目前每个版本一张表）。
func SuiteFor(p Protocol) *Suite {
	switch p {
	case 775:
		return suite775
	}
	return nil
}
