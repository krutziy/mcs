# -*- coding: utf-8 -*-
p = 'internal/game/farm.go'
s = open(p, encoding='utf-8').read()
old = '''\ts := g.world.GetBlock(u.X, u.Y, u.Z)
\tif s != world.StateGrassBlock && s != world.StateDirt && s != world.StateGrassBlock && s != 9 && s != 8 {
\t\treturn false
\t}'''
new = '''\ts := g.world.GetBlock(u.X, u.Y, u.Z)
\tif !grassStates[s] && s != world.StateDirt {
\t\treturn false
\t}'''
assert old in s, 'grass anchor'
s = s.replace(old, new, 1)
open(p, 'w', encoding='utf-8', newline='\n').write(s)
print('farm OK')

p = 'internal/game/block.go'
s = open(p, encoding='utf-8', newline='').read()
NL = chr(10)
old2 = '\t\tg.trySleepBed(p, u) || g.toggleDoor(p, u) {'
assert old2 in s, 'hook anchor'
s = s.replace('\t\tg.tryOpenCrafting(p, u) || g.tryOpenFurnace(p, u) || g.tryOpenChest(p, u) ||' + NL + old2,
              '\t\tg.tryOpenCrafting(p, u) || g.tryOpenFurnace(p, u) || g.tryOpenChest(p, u) ||' + NL + '\t\tg.tryTill(p, u) || g.tryPlantSeed(p, u) ||' + NL + old2, 1)
# 挖掘收获小麦
old3 = '\tg.breakBedIfHit(a.X, a.Y, a.Z, old)'
assert old3 in s, 'dig anchor'
s = s.replace(old3, '\tg.harvestWheatIfMature(a.X, a.Y, a.Z, old)' + NL + old3, 1)
open(p, 'w', encoding='utf-8', newline='\n').write(s)
print('block OK')

p = 'internal/game/game.go'
s = open(p, encoding='utf-8', newline='').read()
old4 = '\tbreeds       map[int32]*breedState    // 动物繁殖状态'
assert old4 in s, 'field anchor'
s = s.replace(old4, old4 + NL + '\tcrops        map[[3]int32]int32       // 小麦作物（位置 → 剩余生长轮）', 1)
old5 = '\t\tbreeds:        map[int32]*breedState{},'
assert old5 in s, 'init anchor'
s = s.replace(old5, old5 + NL + '\t\tcrops:         map[[3]int32]int32{},', 1)
old6 = '\tg.tickBreeds()'
assert old6 in s, 'tick anchor'
s = s.replace(old6, old6 + NL + '\tg.tickCrops()', 1)
open(p, 'w', encoding='utf-8', newline='\n').write(s)
print('game OK')
