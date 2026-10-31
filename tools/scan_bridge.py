#!/usr/bin/env python3
"""穷举扫描 dex 里所有「调向 Bridge.call / Platform.call」的调用点，静态定出 method 常量。

用法：python3 tools/scan_bridge.py [classes.dex classes2.dex ...]

产出：每个调用点的 类 / 方法 / pc / 被调目标 / 实参寄存器表 / 其后向数据流找到的常量。
（用 tools/dalvik2.py 的修正版解码器；35c 的 argc 与第 5 个寄存器已按 AOSP 布局纠正）
"""
import sys
import os
import struct

HERE = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, HERE)
import dalvik2 as dv2  # noqa: E402

WD = os.path.dirname(HERE)

# 各格式「第一个操作数」是否为写入（目的寄存器）
DEST_FIRST = set("""move move/from16 move/16 move-wide move-wide/from16 move-wide/16
move-object move-object/from16 move-object/16 move-result move-result-wide move-result-object
move-exception const/4 const/16 const const/high16 const-wide/16 const-wide/32 const-wide
const-wide/high16 const-string const-string/jumbo const-class new-instance new-array
filled-new-array filled-new-array/range array-length check-cast instance-of
iget iget-wide iget-object iget-boolean iget-byte iget-char iget-short
sget sget-wide sget-object sget-boolean sget-byte sget-char sget-short
neg-int not-int neg-long not-long neg-float neg-double int-to-long int-to-float
int-to-double long-to-int long-to-float long-to-double float-to-int float-to-long
float-to-double double-to-int double-to-long double-to-float int-to-byte int-to-char
int-to-short add-int sub-int mul-int div-int rem-int and-int or-int xor-int shl-int
shr-int ushr-int""".split())

BINOP_DEST = set("""add-int/2addr sub-int/2addr mul-int/2addr div-int/2addr rem-int/2addr
and-int/2addr or-int/2addr xor-int/2addr shl-int/2addr shr-int/2addr ushr-int/2addr
add-long/2addr sub-long/2addr mul-long/2addr div-long/2addr rem-long/2addr and-long/2addr
or-long/2addr xor-long/2addr shl-long/2addr shr-long/2addr ushr-long/2addr
add-float/2addr sub-float/2addr mul-float/2addr div-float/2addr rem-float/2addr
add-double/2addr sub-double/2addr mul-double/2addr div-double/2addr rem-double/2addr""".split())


def writes(it):
    """返回该指令写入的寄存器集合（只关心标量/引用寄存器的覆盖判断）。"""
    n, fmt = it['name'], it['fmt']
    r = it['regs']
    if not r:
        return set()
    if fmt in ('12x',) or n in DEST_FIRST or n in BINOP_DEST:
        return {r[0]}
    if fmt in ('22c', '23x'):
        return {r[0]}
    if fmt in ('22b', '22s', '22t'):
        return {r[0]} if fmt == '22b' or fmt == '22s' else set()
    if fmt in ('21s', '21h', '21t', '21c', '31i', '31t', '31c', '51l', '11x', '11n'):
        return {r[0]}
    if fmt == '32x':
        return {r[0]}
    if fmt == '45cc':
        return set()
    return set()


def const_of(it):
    """若该指令把常量塞进目的寄存器，返回 (dst, value)；可能返回 dst 对应的宽值 0.."""
    n = it['name']
    r = it['regs']
    if not r:
        return None
    dst = r[0]
    if n == 'const/4':
        v = int(it['text'].split('#')[-1])
        return dst, v
    if n == 'const/16':
        v = int(it['text'].split('#')[-1])
        return dst, v
    if n == 'const':
        v = int(it['text'].split('#')[-1])
        return dst, v
    if n == 'const/high16':
        v = int(it['text'].split('#')[-1])
        return dst, v
    return None


def def_of(insns, idx, reg):
    """从 insns[idx] 向前找寄存器 reg 最近一次被写入的指令。"""
    for j in range(idx - 1, -1, -1):
        it = insns[j]
        if reg in writes(it):
            return it
    return None


def iter_methods(d):
    for class_idx, access, super_idx, cdat, src in d.classes():
        cn = d.type(class_idx)
        if not cdat:
            continue
        p = cdat
        sf, p = d.uleb(p); inf, p = d.uleb(p); dm, p = d.uleb(p); vm, p = d.uleb(p)
        for cnt in (sf, inf):
            fi = 0
            for _ in range(cnt):
                diff, p = d.uleb(p); fi += diff; acc, p = d.uleb(p)
        for kind, cnt in (("D", dm), ("V", vm)):
            mi = 0
            for _ in range(cnt):
                diff, p = d.uleb(p); mi += diff
                acc, p = d.uleb(p); code, p = d.uleb(p)
                yield cn, kind, mi, acc, code


def main():
    files = sys.argv[1:] or [os.path.join(WD, 'apk/classes.dex'),
                             os.path.join(WD, 'apk/classes2.dex')]
    total = 0
    for f in files:
        if not os.path.exists(f):
            continue
        d = dv2.dd.Dex(open(f, 'rb').read())
        res = dv2.make_resolver(d)
        print("=" * 96)
        print("FILE", f)
        for cn, kind, mi, acc, code in iter_methods(d):
            if not code:
                continue
            mcls, mname, mret, mparams = d.method(mi)
            try:
                insns, regs, ins, outs = dv2.disasm(d, code, resolve=res)
            except Exception as e:
                print("decode fail", cn, mname, e)
                continue
            for i, it in enumerate(insns):
                if not it['name'].startswith('invoke'):
                    continue
                if not it['ref']:
                    continue
                tgt = d.method(it['ref'])
                tcls, tname = dv2.short(tgt[0]), tgt[1]
                if tname not in ('call', 'call$suspendImpl'):
                    continue
                if tcls not in ('Bridge', 'UnsignedKt', 'Platform'):
                    continue
                total += 1
                print("\n%s.%s  %s" % (dv2.short(cn), mname, '%s->%s' % (tcls, tname)))
                print("   pc=%-5d argc=%d regs=%s" % (it['pc'], len(it['regs']), it['regs']))
                print("   %s" % it['text'])
                for pos, rr in enumerate(it['regs']):
                    defi = def_of(insns, i, rr)
                    if defi is None:
                        continue
                    c = const_of(defi)
                    if c and c[0] == rr:
                        print("     arg%d v%-3d <= pc %-4d %s" % (pos, rr, defi['pc'], defi['text']))
    print("\ntotal call sites: %d" % total)


if __name__ == '__main__':
    main()
