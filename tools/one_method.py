#!/usr/bin/env python3
"""按 code_off 精确 dump 单个方法（用修正版 dalvik2 解码）。

用法：python3 tools/one_method.py <dex> <code_off> [from_pc] [to_pc]
"""
import sys
import os
import importlib.util
import struct

HERE = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, HERE)
import dalvik2 as dv2  # noqa: E402


def main():
    dex_path = sys.argv[1]
    code_off = int(sys.argv[2], 0)
    lo = int(sys.argv[3]) if len(sys.argv) > 3 else 0
    hi = int(sys.argv[4]) if len(sys.argv) > 4 else 10 ** 9
    d = dv2.dd.Dex(open(dex_path, 'rb').read())
    res = dv2.make_resolver(d)
    # 找到拥有该 code_off 的方法，打印名字
    name = '?'
    for cn, kind, mi, acc, code in _iter(d):
        if code == code_off:
            cls, nm, ret, params = d.method(mi)
            name = "%s [%s] %s %s(%s)" % (cn, kind, dv2.short(ret), nm,
                                          ', '.join(dv2.short(p) for p in params))
            break
    print("METHOD", name, "code@%d" % code_off)
    insns, regs, ins, outs = dv2.disasm(d, code_off, resolve=res)
    print("(regs=%d ins=%d outs=%d)" % (regs, ins, outs))
    for it in insns:
        if lo <= it['pc'] <= hi:
            print("  %4d: %-58s [0x%04x]" % (it['pc'], it['text'], it['word']))


def _iter(d):
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


if __name__ == '__main__':
    main()
