# -*- coding: utf-8 -*-
p = 'internal/game/session.go'
s = open(p, encoding='utf-8', newline='').read()
old = '\t\tcase "minecraft:attack":\n\t\t\ts.game.Submit(&playerAttackEvent{p: p, target: pr.VarInt()})\n'
if old not in s:
    # 换行风格可能是 \r\n 或缩进不同——按行定位
    lines = s.split('\n')
    out = []
    inserted = False
    for i, ln in enumerate(lines):
        out.append(ln)
        if not inserted and ln.strip() == 's.game.Submit(&playerAttackEvent{p: p, target: pr.VarInt()})':
            indent = ln[:len(ln) - len(ln.lstrip('\t'))]
            out.append(indent + 'case "minecraft:interact":')
            out.append(indent + '\ttarget := pr.VarInt()')
            out.append(indent + '\ttyp := pr.VarInt()')
            out.append(indent + '\tsneaking := pr.Bool()')
            out.append(indent + '\tif typ == 0 {')
            out.append(indent + '\t\ts.game.Submit(&playerInteractEvent{p: p, target: target, sneaking: sneaking})')
            out.append(indent + '\t}')
            inserted = True
    assert inserted, 'case line not found'
    s = '\n'.join(out)
else:
    new = old + '\t\tcase "minecraft:interact":\n\t\t\ttarget := pr.VarInt()\n\t\t\ttyp := pr.VarInt()\n\t\t\tsneaking := pr.Bool()\n\t\t\tif typ == 0 {\n\t\t\t\ts.game.Submit(&playerInteractEvent{p: p, target: target, sneaking: sneaking})\n\t\t\t}\n'
    s = s.replace(old, new, 1)

old2 = 'type playerAttackEvent struct {\n\tp      *entity.Player\n\ttarget int32\n}'
assert old2 in s, 'struct anchor'
new2 = old2 + '\n\ntype playerInteractEvent struct {\n\tp        *entity.Player\n\ttarget   int32\n\tsneaking bool\n}'
s = s.replace(old2, new2, 1)
open(p, 'w', encoding='utf-8', newline='\n').write(s)
print('session OK')

p = 'internal/game/game.go'
s = open(p, encoding='utf-8', newline='').read()
old3 = 'case *playerAttackEvent:\n\t\tg.handleAttack(ev)'
assert old3 in s, 'dispatch anchor'
s = s.replace(old3, old3 + '\n\tcase *playerInteractEvent:\n\t\tg.handleInteract(ev)', 1)
open(p, 'w', encoding='utf-8', newline='\n').write(s)
print('dispatch OK')
