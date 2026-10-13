package world

import "log"

// Generator 是世界生成器接口：按需填充区块，输出确定性。
// 壳子核心只内置空世界（VoidGenerator），其余生成器由插件注册。
type Generator interface {
	Generate(ch *Chunk)
}

// VoidGenerator 生成全空（虚空）世界——空壳的默认形态。
type VoidGenerator struct{}

// Generate 什么都不做：区块保持全空气。
func (VoidGenerator) Generate(*Chunk) {}

var generators = map[string]func(seed int64) Generator{}

// RegisterGenerator 注册一个世界生成器（插件在 init() 中调用）。
func RegisterGenerator(name string, factory func(seed int64) Generator) {
	generators[name] = factory
}

// NewGenerator 按名称取生成器；未注册时回退为虚空世界并提示。
func NewGenerator(name string, seed int64) Generator {
	if f, ok := generators[name]; ok {
		return f(seed)
	}
	if name != "" && name != "void" {
		log.Printf("[world] 未知世界类型 %q（插件未编译？），回退为虚空世界", name)
	}
	return VoidGenerator{}
}
