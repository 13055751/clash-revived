#!/usr/bin/env python3
"""用一个粗糙但够用的 AArch64 解码器，反向读 Go 函数里的关键常量。

用途：FlatBuffers 生成的 Go 访问器形状固定 ——
    o := UOffsetT(t.Offset(N)); if o != 0 { ... }
所以只要把 N 读出来，就能确定字段的 vtable 偏移（N = 4 + 2*field_index）。

用法：python3 tools/go_trace.py <elf> <符号子串> [max_insns]
"""
import os
import sys
import struct
import importlib.util

HERE = os.path.dirname(os.path.abspath(__file__))
WD = os.path.dirname(HERE)


def load(name):
    sp = importlib.util.spec_from_file_location(name, os.path.join(WD, name + '.py'))
    m = importlib.util.module_from_spec(sp)
    sp.loader.exec_module(m)
    return m


sys.path.insert(0, WD)
import gosym  # noqa: E402
import arm64  # noqa: E402


def build_symmap(path):
    d = open(path, 'rb').read()
    cands = gosym.find_pclntab(d)
    best = max(cands, key=lambda c: c[1])
    syms, hdr = gosym.parse(d, best[0])
    taddr, toff = gosym.text_delta(d)
    if hdr['textStart'] == 0 and taddr:
        syms = [(taddr + a, sz, nm) for a, sz, nm in syms]
    m = {}
    for a, sz, nm in syms:
        m[a] = nm
    return m, syms


def main():
    path = sys.argv[1]
    needle = sys.argv[2]
    limit = int(sys.argv[3]) if len(sys.argv) > 3 else 40
    symmap, syms = build_symmap(path)
    elf = arm64.Elf(path)
    hits = [(a, sz, nm) for a, sz, nm in syms if needle in nm]
    if not hits:
        print("no symbol matching %r" % needle)
        return 1
    for addr, size, nm in hits:
        print("\n=== %s  addr=0x%x size=%d" % (nm, addr, size))
        for a, txt, kind, detail in arm64.disasm_fn(elf, addr, size, syms, symmap, limit):
            mark = ''
            if kind == 'call' and detail and ('Offset' in str(detail) or 'ByteVector' in str(detail)
                                              or 'String' in str(detail) or 'Vector' in str(detail)):
                mark = '   <-- flatbuffers'
            print("   0x%06x  %s%s" % (a, txt, mark))
    return 0


if __name__ == '__main__':
    sys.exit(main())
