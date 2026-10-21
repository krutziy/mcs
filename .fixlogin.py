# -*- coding: utf-8 -*-
p = 'internal/packet/packet.go'
s = open(p, encoding='utf-8', newline='').read()
old = '''// WriteLoginFinished 写入登录成功包（uuid, name, properties）。
func WriteLoginFinished(w *protocol.Writer, uuid protocol.UUID, name string) {
	w.UUID(uuid)
	w.String(name)
	w.VarInt(0) // properties 数量：离线模式无皮肤属性
}'''
new = '''// WriteLoginFinished 写入登录成功包（uuid, name, properties）。
func WriteLoginFinished(w *protocol.Writer, uuid protocol.UUID, name string) {
	WriteLoginFinishedFor(w, uuid, name, 0)
}

// WriteLoginFinishedFor 写入登录成功包。properties 后的尾部字节经官方
// 各版本抓包逐字节确认：774 无尾部；775/776（26.1/26.2）一个 0x0a 字节；
// 777（26.3）一段固定 16 字节（语义未明，缺它真实客户端解码失败）。
func WriteLoginFinishedFor(w *protocol.Writer, uuid protocol.UUID, name string, proto int32) {
	w.UUID(uuid)
	w.String(name)
	w.VarInt(0) // properties 数量：离线模式无皮肤属性
	switch {
	case proto >= 777:
		w.RawBytes([]byte{0x80, 0x19, 0x1b, 0xaa, 0xc8, 0xec, 0x46, 0xea,
			0x85, 0xc2, 0x5d, 0x22, 0x6a, 0xf9, 0x2a, 0x58})
	case proto >= 775:
		w.Uint8(0x0a)
	}
}'''
assert old in s, 'packet anchor not found'
s = s.replace(old, new, 1)
open(p, 'w', encoding='utf-8', newline='\n').write(s)
print('packet OK')

p = 'internal/server/server.go'
s = open(p, encoding='utf-8', newline='').read()
s = s.replace('packet.WriteLoginFinishedFor(w, uuid, start.Username, protoVer)',
              'packet.WriteLoginFinishedFor(w, uuid, start.Username, int32(ver.Protocol))', 1)
open(p, 'w', encoding='utf-8', newline='\n').write(s)
print('server OK')
