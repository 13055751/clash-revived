#!/usr/bin/env python3
"""修正版 Dalvik 反汇编器（tools/ 内自有实现，不改动 work/dalvik.py）。

写这份的原因：work/dalvik.py 对 35c 的处理把 A 与 G 弄反了。
按 AOSP dalvik-bytecode 规范，35c 的布局是  A|G|op BBBB F|E|D|C ，
**从高位到低位** 书写，即：
    A (argc)   = (w >> 12) & 0xf
    G (第5个寄存器) = (w >> 8) & 0xf
    C,D,E,F    = w3 的四个 nibble（C 最低）
dalvik.py 取 A=(w>>8)&0xf、G=(w>>12)&0xf，于是 argc 与第 5 个寄存器互换：
- 4 个参数的调用会被算成 argc=0（打印 {}）
- 5 个参数的调用 argc=12，反而"碰巧"打印出全部 5 个寄存器，但第 5 个是 argc 本身

另外 22x（move-*/from16）没实现，导致 3 字指令被当成 1 字，后续整段错位。
本文件把格式表补全，并对 35c/3rc 给出正确的实参寄存器序列。
"""
import struct
import sys
import importlib.util
import os

HERE = os.path.dirname(os.path.abspath(__file__))
WD = os.path.dirname(HERE)


def _load(name):
    sp = importlib.util.spec_from_file_location(name, os.path.join(WD, name + '.py'))
    m = importlib.util.module_from_spec(sp)
    sp.loader.exec_module(m)
    return m


dd = _load('dexdump')

# opcode -> (name, format)
OPS = {}


def _add(code, name, fmt):
    OPS[code] = (name, fmt)


_TABLE = [
    (0x00, 'nop', '10x'), (0x01, 'move', '12x'), (0x02, 'move/from16', '22x'),
    (0x03, 'move/16', '32x'), (0x04, 'move-wide', '12x'), (0x05, 'move-wide/from16', '22x'),
    (0x06, 'move-wide/16', '32x'), (0x07, 'move-object', '12x'), (0x08, 'move-object/from16', '22x'),
    (0x09, 'move-object/16', '32x'), (0x0a, 'move-result', '11x'), (0x0b, 'move-result-wide', '11x'),
    (0x0c, 'move-result-object', '11x'), (0x0d, 'move-exception', '11x'),
    (0x0e, 'return-void', '10x'), (0x0f, 'return', '11x'), (0x10, 'return-wide', '11x'),
    (0x11, 'return-object', '11x'), (0x12, 'const/4', '11n'), (0x13, 'const/16', '21s'),
    (0x14, 'const', '31i'), (0x15, 'const/high16', '21h'), (0x16, 'const-wide/16', '21s'),
    (0x17, 'const-wide/32', '31i'), (0x18, 'const-wide', '51l'), (0x19, 'const-wide/high16', '21h'),
    (0x1a, 'const-string', '21c'), (0x1b, 'const-string/jumbo', '31c'), (0x1c, 'const-class', '21c'),
    (0x1d, 'monitor-enter', '11x'), (0x1e, 'monitor-exit', '11x'), (0x1f, 'check-cast', '21c'),
    (0x20, 'instance-of', '22c'), (0x21, 'array-length', '12x'), (0x22, 'new-instance', '21c'),
    (0x23, 'new-array', '22c'), (0x24, 'filled-new-array', '35c'), (0x25, 'filled-new-array/range', '3rc'),
    (0x26, 'fill-array-data', '31t'), (0x27, 'throw', '11x'), (0x28, 'goto', '10t'),
    (0x29, 'goto/16', '20t'), (0x2a, 'goto/32', '30t'), (0x2b, 'packed-switch', '31t'),
    (0x2c, 'sparse-switch', '31t'), (0x2d, 'cmpl-float', '23x'), (0x2e, 'cmpg-float', '23x'),
    (0x2f, 'cmpl-double', '23x'), (0x30, 'cmpg-double', '23x'), (0x31, 'cmp-long', '23x'),
    (0x32, 'if-eq', '22t'), (0x33, 'if-ne', '22t'), (0x34, 'if-lt', '22t'), (0x35, 'if-ge', '22t'),
    (0x36, 'if-gt', '22t'), (0x37, 'if-le', '22t'), (0x38, 'if-eqz', '21t'), (0x39, 'if-nez', '21t'),
    (0x3a, 'if-ltz', '21t'), (0x3b, 'if-gez', '21t'), (0x3c, 'if-gtz', '21t'), (0x3d, 'if-lez', '21t'),
    (0x44, 'aget', '23x'), (0x45, 'aget-wide', '23x'), (0x46, 'aget-object', '23x'),
    (0x47, 'aget-boolean', '23x'), (0x48, 'aget-byte', '23x'), (0x49, 'aget-char', '23x'),
    (0x4a, 'aget-short', '23x'), (0x4b, 'aput', '23x'), (0x4c, 'aput-wide', '23x'),
    (0x4d, 'aput-object', '23x'), (0x4e, 'aput-boolean', '23x'), (0x4f, 'aput-byte', '23x'),
    (0x50, 'aput-char', '23x'), (0x51, 'aput-short', '23x'),
    (0x52, 'iget', '22c'), (0x53, 'iget-wide', '22c'), (0x54, 'iget-object', '22c'),
    (0x55, 'iget-boolean', '22c'), (0x56, 'iget-byte', '22c'), (0x57, 'iget-char', '22c'),
    (0x58, 'iget-short', '22c'), (0x59, 'iput', '22c'), (0x5a, 'iput-wide', '22c'),
    (0x5b, 'iput-object', '22c'), (0x5c, 'iput-boolean', '22c'), (0x5d, 'iput-byte', '22c'),
    (0x5e, 'iput-char', '22c'), (0x5f, 'iput-short', '22c'), (0x60, 'sget', '21c'),
    (0x61, 'sget-wide', '21c'), (0x62, 'sget-object', '21c'), (0x63, 'sget-boolean', '21c'),
    (0x64, 'sget-byte', '21c'), (0x65, 'sget-char', '21c'), (0x66, 'sget-short', '21c'),
    (0x67, 'sput', '21c'), (0x68, 'sput-wide', '21c'), (0x69, 'sput-object', '21c'),
    (0x6a, 'sput-boolean', '21c'), (0x6b, 'sput-byte', '21c'), (0x6c, 'sput-char', '21c'),
    (0x6d, 'sput-short', '21c'), (0x6e, 'invoke-virtual', '35c'), (0x6f, 'invoke-super', '35c'),
    (0x70, 'invoke-direct', '35c'), (0x71, 'invoke-static', '35c'), (0x72, 'invoke-interface', '35c'),
    (0x74, 'invoke-virtual/range', '3rc'), (0x75, 'invoke-super/range', '3rc'),
    (0x76, 'invoke-direct/range', '3rc'), (0x77, 'invoke-static/range', '3rc'),
    (0x78, 'invoke-interface/range', '3rc'),
    (0x7b, 'neg-int', '12x'), (0x7c, 'not-int', '12x'), (0x7d, 'neg-long', '12x'),
    (0x7e, 'not-long', '12x'), (0x7f, 'neg-float', '12x'), (0x80, 'neg-double', '12x'),
    (0x81, 'int-to-long', '12x'), (0x82, 'int-to-float', '12x'), (0x83, 'int-to-double', '12x'),
    (0x84, 'long-to-int', '12x'), (0x85, 'long-to-float', '12x'), (0x86, 'long-to-double', '12x'),
    (0x87, 'float-to-int', '12x'), (0x88, 'float-to-long', '12x'), (0x89, 'float-to-double', '12x'),
    (0x8a, 'double-to-int', '12x'), (0x8b, 'double-to-long', '12x'), (0x8c, 'double-to-float', '12x'),
    (0x8d, 'int-to-byte', '12x'), (0x8e, 'int-to-char', '12x'), (0x8f, 'int-to-short', '12x'),
    (0x90, 'add-int', '23x'), (0x91, 'sub-int', '23x'), (0x92, 'mul-int', '23x'),
    (0x93, 'div-int', '23x'), (0x94, 'rem-int', '23x'), (0x95, 'and-int', '23x'),
    (0x96, 'or-int', '23x'), (0x97, 'xor-int', '23x'), (0x98, 'shl-int', '23x'),
    (0x99, 'shr-int', '23x'), (0x9a, 'ushr-int', '23x'), (0x9b, 'add-long', '23x'),
    (0x9c, 'sub-long', '23x'), (0x9d, 'mul-long', '23x'), (0x9e, 'div-long', '23x'),
    (0x9f, 'rem-long', '23x'), (0xa0, 'and-long', '23x'), (0xa1, 'or-long', '23x'),
    (0xa2, 'xor-long', '23x'), (0xa3, 'shl-long', '23x'), (0xa4, 'shr-long', '23x'),
    (0xa5, 'ushr-long', '23x'), (0xa6, 'add-float', '23x'), (0xa7, 'sub-float', '23x'),
    (0xa8, 'mul-float', '23x'), (0xa9, 'div-float', '23x'), (0xaa, 'rem-float', '23x'),
    (0xab, 'add-double', '23x'), (0xac, 'sub-double', '23x'),
    (0xad, 'add-int/2addr', '12x'), (0xae, 'sub-int/2addr', '12x'), (0xaf, 'mul-int/2addr', '12x'),
    (0xb0, 'div-int/2addr', '12x'), (0xb1, 'rem-int/2addr', '12x'), (0xb2, 'and-int/2addr', '12x'),
    (0xb3, 'or-int/2addr', '12x'), (0xb4, 'xor-int/2addr', '12x'), (0xb5, 'shl-int/2addr', '12x'),
    (0xb6, 'shr-int/2addr', '12x'), (0xb7, 'ushr-int/2addr', '12x'),
    (0xb8, 'add-long/2addr', '12x'), (0xb9, 'sub-long/2addr', '12x'), (0xba, 'mul-long/2addr', '12x'),
    (0xbb, 'div-long/2addr', '12x'), (0xbc, 'rem-long/2addr', '12x'), (0xbd, 'and-long/2addr', '12x'),
    (0xbe, 'or-long/2addr', '12x'), (0xbf, 'xor-long/2addr', '12x'), (0xc0, 'shl-long/2addr', '12x'),
    (0xc1, 'shr-long/2addr', '12x'), (0xc2, 'ushr-long/2addr', '12x'),
    (0xc3, 'add-float/2addr', '12x'), (0xc4, 'sub-float/2addr', '12x'), (0xc5, 'mul-float/2addr', '12x'),
    (0xc6, 'div-float/2addr', '12x'), (0xc7, 'rem-float/2addr', '12x'),
    (0xc8, 'add-double/2addr', '12x'), (0xc9, 'sub-double/2addr', '12x'),
    (0xca, 'mul-double/2addr', '12x'), (0xcb, 'div-double/2addr', '12x'), (0xcc, 'rem-double/2addr', '12x'),
    (0xd0, 'add-int/lit16', '22s'), (0xd1, 'rsub-int', '22s'), (0xd2, 'mul-int/lit16', '22s'),
    (0xd3, 'div-int/lit16', '22s'), (0xd4, 'rem-int/lit16', '22s'), (0xd5, 'and-int/lit16', '22s'),
    (0xd6, 'or-int/lit16', '22s'), (0xd7, 'xor-int/lit16', '22s'),
    (0xd8, 'add-int/lit8', '22b'), (0xd9, 'rsub-int/lit8', '22b'), (0xda, 'mul-int/lit8', '22b'),
    (0xdb, 'div-int/lit8', '22b'), (0xdc, 'rem-int/lit8', '22b'), (0xdd, 'and-int/lit8', '22b'),
    (0xde, 'or-int/lit8', '22b'), (0xdf, 'xor-int/lit8', '22b'), (0xe0, 'shl-int/lit8', '22b'),
    (0xe1, 'shr-int/lit8', '22b'), (0xe2, 'ushr-int/lit8', '22b'),
    (0xfa, 'invoke-polymorphic', '45cc'), (0xfb, 'invoke-polymorphic/range', '4rcc'),
    (0xfc, 'invoke-custom', '35c'), (0xfd, 'invoke-custom/range', '3rc'),
    (0xfe, 'const-method-handle', '21c'), (0xff, 'const-method-type', '21c'),
]
for _t in _TABLE:
    _add(*_t)

KIND = {}
for _c, _k in [(0x1a, 'string'), (0x1b, 'string'), (0x1c, 'type'), (0x1f, 'type'),
               (0x22, 'type'), (0x23, 'type'), (0x24, 'type'), (0x25, 'type'), (0x20, 'type'),
               (0x52, 'field'), (0x53, 'field'), (0x54, 'field'), (0x55, 'field'),
               (0x56, 'field'), (0x57, 'field'), (0x58, 'field'),
               (0x59, 'field'), (0x5a, 'field'), (0x5b, 'field'), (0x5c, 'field'),
               (0x5d, 'field'), (0x5e, 'field'), (0x5f, 'field'),
               (0x60, 'field'), (0x61, 'field'), (0x62, 'field'), (0x63, 'field'),
               (0x64, 'field'), (0x65, 'field'), (0x66, 'field'), (0x67, 'field'),
               (0x68, 'field'), (0x69, 'field'), (0x6a, 'field'), (0x6b, 'field'),
               (0x6c, 'field'), (0x6d, 'field'),
               (0xfe, 'meth'), (0xff, 'proto')]:
    KIND[_c] = _k


def short(t):
    if not t:
        return '?'
    return t.rsplit('/', 1)[-1].rstrip(';')


def make_resolver(d):
    def r(idx, kind):
        try:
            if kind in ('method', 'meth'):
                cls, nm, ret, params = d.method(idx)
                return "%s->%s(%s)%s" % (short(cls), nm,
                                         ','.join(short(p) for p in params), short(ret))
            if kind == 'field':
                n, off = d.field_ids
                cls, typ, name = struct.unpack_from('<HHI', d.d, off + idx * 8)
                return "%s->%s:%s" % (short(d.type(cls)), d.string(name), short(d.type(typ)))
            if kind in ('type', 'proto'):
                return short(d.type(idx))
            return '"%s"' % d.string(idx)[:80].replace('\n', '\\n')
        except Exception:
            return "@%d?" % idx
    return r


def disasm(dex, code_off, resolve=None):
    """-> list of dict(pc, op, name, fmt, text, regs=[](实参寄存器), ref=idx, size)"""
    d = dex.d
    regs_n, ins, outs, tries, debug_off, insns_size = struct.unpack_from('<HHHHII', d, code_off)
    base = code_off + 16
    out = []
    pc = 0
    while pc < insns_size:
        w = struct.unpack_from('<H', d, base + pc * 2)[0]
        op = w & 0xff
        # payload 伪指令
        if op == 0x00 and w == 0x0100:
            size = struct.unpack_from('<H', d, base + pc * 2 + 2)[0]
            width = size * 2 + 4
            out.append(dict(pc=pc, op=op, name='.packed-switch-payload', fmt='payload',
                            text='.packed-switch-payload size=%d' % size, regs=[], ref=None,
                            size=width))
            pc += width
            continue
        if op == 0x00 and w == 0x0200:
            size = struct.unpack_from('<H', d, base + pc * 2 + 2)[0]
            width = size * 2 * 2 + 2
            out.append(dict(pc=pc, op=op, name='.sparse-switch-payload', fmt='payload',
                            text='.sparse-switch-payload size=%d' % size, regs=[], ref=None,
                            size=width))
            pc += width
            continue
        if op == 0x00 and w == 0x0300:
            esize = struct.unpack_from('<H', d, base + pc * 2 + 2)[0]
            n = struct.unpack_from('<I', d, base + pc * 2 + 4)[0]
            width = (esize * n + 1) // 2 + 4
            out.append(dict(pc=pc, op=op, name='.array-data-payload', fmt='payload',
                            text='.array-data esize=%d n=%d' % (esize, n), regs=[], ref=None,
                            size=width))
            pc += width
            continue

        name, fmt = OPS.get(op, ('op_%02x' % op, '10x'))
        A = (w >> 12) & 0xf          # 高 4 位（35c 里是 argc；12x 里是 B）
        B = (w >> 8) & 0xf           # 次 4 位（12x 里是 A/目的寄存器）
        AA = (w >> 8) & 0xff
        w2 = struct.unpack_from('<H', d, base + pc * 2 + 2)[0] if pc + 1 < insns_size else 0
        w3 = struct.unpack_from('<H', d, base + pc * 2 + 4)[0] if pc + 2 < insns_size else 0
        desc = ''
        step = 1
        regs = []
        ref = None

        if fmt == '10x':
            pass
        elif fmt == '12x':
            desc = 'v%d, v%d' % (B, A); regs = [B, A]
        elif fmt == '11x':
            desc = 'v%d' % B; regs = [B]
        elif fmt == '11n':
            lit = A if A < 8 else A - 16
            desc = 'v%d, #%d' % (B, lit); regs = [B]
        elif fmt == '10t':
            off = struct.unpack('<b', struct.pack('<B', B))[0]
            desc = '->%d' % (pc + off)
        elif fmt == '20t':
            off = struct.unpack('<h', struct.pack('<H', w2))[0]
            desc = '->%d' % (pc + off); step = 2
        elif fmt == '22x':
            desc = 'v%d, v%d' % (AA & 0xff, w2); regs = [w2]; step = 2
        elif fmt == '32x':
            desc = 'v%d, v%d' % (w2, w3); regs = [w2, w3]; step = 3
        elif fmt in ('21s', '21h', '21t', '21c', '23x', '22b', '22s', '22t', '22c'):
            if fmt == '21s':
                val = struct.unpack('<h', struct.pack('<H', w2))[0]
                desc = 'v%d, #%d' % (B, val); regs = [B]
            elif fmt == '21h':
                val = struct.unpack('<h', struct.pack('<H', w2))[0] << 16
                desc = 'v%d, #%d' % (B, val); regs = [B]
            elif fmt == '21t':
                off = struct.unpack('<h', struct.pack('<H', w2))[0]
                desc = 'v%d, ->%d' % (B, pc + off); regs = [B]
            elif fmt == '21c':
                ref = w2
                desc = 'v%d, %s' % (B, resolve(w2, KIND.get(op, 'string')) if resolve else '@%d' % w2)
                regs = [B]
            elif fmt == '22c':
                ref = w2
                desc = 'v%d, v%d, %s' % (B, A, resolve(w2, KIND.get(op, 'type')) if resolve else '@%d' % w2)
                regs = [B, A]
            elif fmt == '23x':
                desc = 'v%d, v%d, v%d' % (AA, w2 & 0xff, (w2 >> 8) & 0xff)
                regs = [AA, w2 & 0xff, (w2 >> 8) & 0xff]
            elif fmt == '22b':
                lit = (w2 >> 8) & 0xff
                lit = lit if lit < 128 else lit - 256
                desc = 'v%d, v%d, #%d' % (AA, w2 & 0xff, lit); regs = [AA, w2 & 0xff]
            elif fmt == '22s':
                val = struct.unpack('<h', struct.pack('<H', w2))[0]
                desc = 'v%d, v%d, #%d' % (B, A, val); regs = [B, A]
            elif fmt == '22t':
                off = struct.unpack('<h', struct.pack('<H', w2))[0]
                desc = 'v%d, v%d, ->%d' % (B, A, pc + off); regs = [B, A]
            step = 2
        elif fmt in ('31i', '31t', '31c'):
            if fmt == '31i':
                val = struct.unpack('<i', struct.pack('<I', (w3 << 16) | w2))[0]
                desc = 'v%d, #%d' % (B, val); regs = [B]
            elif fmt == '31t':
                off = struct.unpack('<i', struct.pack('<I', (w3 << 16) | w2))[0]
                desc = 'v%d, ->%d' % (B, pc + off); regs = [B]
            else:
                idx = (w3 << 16) | w2
                ref = idx
                desc = 'v%d, %s' % (B, resolve(idx, 'string') if resolve else '@%d' % idx)
                regs = [B]
            step = 3
        elif fmt == '51l':
            lo = (w2 | (w3 << 16)) & 0xffffffff
            hi = (struct.unpack_from('<H', d, base + pc * 2 + 6)[0]
                  | (struct.unpack_from('<H', d, base + pc * 2 + 8)[0] << 16))
            val = struct.unpack('<q', struct.pack('<II', lo, hi))[0]
            desc = 'v%d, #%d' % (B, val); regs = [B]; step = 5
        elif fmt == '30t':
            off = struct.unpack('<i', struct.pack('<I', (w3 << 16) | w2))[0]
            desc = '->%d' % (pc + off); step = 3
        elif fmt == '35c':
            argc = A                                   # ← 关键：argc 在高 4 位
            G = B                                      # ← 第 5 个寄存器在次 4 位
            C = w3 & 0xf; D = (w3 >> 4) & 0xf; E = (w3 >> 8) & 0xf; F = (w3 >> 12) & 0xf
            allregs = [C, D, E, F, G]
            regs = allregs[:argc]
            ref = w2
            desc = '{%s}, %s' % (', '.join('v%d' % x for x in regs),
                                 resolve(w2, 'method') if resolve else '@%d' % w2)
            step = 3
        elif fmt == '3rc':
            argc = AA
            ref = w2
            regs = [w3 + i for i in range(argc)]
            desc = '{%s}, %s' % (', '.join('v%d' % x for x in regs),
                                 resolve(w2, 'method') if resolve else '@%d' % w2)
            step = 3
        elif fmt == '45cc':
            argc = A
            G = B
            C = w3 & 0xf; D = (w3 >> 4) & 0xf; E = (w3 >> 8) & 0xf; F = (w3 >> 12) & 0xf
            regs = [C, D, E, F, G][:argc]
            ref = w2
            desc = '{%s}, %s, proto@%d' % (', '.join('v%d' % x for x in regs),
                                           resolve(w2, 'method') if resolve else '@%d' % w2,
                                           struct.unpack_from('<H', d, base + pc * 2 + 6)[0])
            step = 4
        elif fmt == '4rcc':
            argc = AA
            ref = w2
            regs = [w3 + i for i in range(argc)]
            desc = '{%s}, %s' % (', '.join('v%d' % x for x in regs),
                                 resolve(w2, 'method') if resolve else '@%d' % w2)
            step = 4

        text = name + (' ' + desc if desc else '')
        out.append(dict(pc=pc, op=op, name=name, fmt=fmt, text=text, regs=regs,
                        ref=ref, size=step, word=w))
        pc += step
    return out, regs_n, ins, outs


def dump_class(dex_path, needle):
    d = dd.Dex(open(dex_path, 'rb').read())
    res = make_resolver(d)
    files = []
    for class_idx, access, super_idx, cdat, src in d.classes():
        cn = d.type(class_idx)
        if needle not in cn:
            continue
        print("\n" + "=" * 78)
        print("CLASS %s  (super=%s)" % (cn, short(d.type(super_idx)) if super_idx != 0xffffffff else '-'))
        print("=" * 78)
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
                cls, nm, ret, params = d.method(mi)
                print("\n--- %s %s %s(%s) : %s [acc=0x%x]" % (
                    kind, short(ret), nm, ', '.join(short(x) for x in params),
                    'code@%d' % code, acc))
                if code == 0:
                    continue
                insns, regs, ins, outs = disasm(d, code, resolve=res)
                print("    (regs=%d ins=%d outs=%d)" % (regs, ins, outs))
                for it in insns:
                    print("    %4d: %-58s [0x%04x]" % (it['pc'], it['text'], it['word']))


if __name__ == '__main__':
    dump_class(sys.argv[1], sys.argv[2])
