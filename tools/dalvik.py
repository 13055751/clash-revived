#!/usr/bin/env python3
"""Minimal Dalvik bytecode disassembler for targeted method inspection."""
import struct, sys, importlib.util

spec = importlib.util.spec_from_file_location("dd", "dexdump.py")
dd = importlib.util.module_from_spec(spec)
spec.loader.exec_module(dd)

# opcode table: name, format
OPS = {}


def _add(code, name, fmt):
    OPS[code] = (name, fmt)


for c, n, f in [
    (0x00, 'nop', '10x'), (0x01, 'move', '12x'), (0x02, 'move/from16', '22x'),
    (0x03, 'move/16', '32x'), (0x04, 'move-wide', '12x'), (0x05, 'move-wide/from16', '22x'),
    (0x06, 'move-wide/16', '32x'), (0x07, 'move-object', '12x'), (0x08, 'move-object/from16', '22x'),
    (0x09, 'move-object/16', '32x'), (0x0a, 'move-result', '11x'), (0x0b, 'move-result-wide', '11x'),
    (0x0c, 'move-result-object', '11x'), (0x0d, 'move-exception', '11x'),
    (0x0e, 'return-void', '10x'), (0x0f, 'return', '11x'), (0x10, 'return-wide', '11x'),
    (0x11, 'return-object', '11x'), (0x12, 'const/4', '11n'), (0x13, 'const/16', '21s'),
    (0x14, 'const', '31i'), (0x15, 'const/high16', '21h'), (0x16, 'const-wide/16', '21s'),
    (0x17, 'const-wide/32', '31i'), (0x18, 'const-wide', '51l'), (0x19, 'const-wide/high16', '21h'),
    (0x1a, 'const-string', '21c'), (0x1b, 'const-string/jumbo', '31c'),
    (0x1c, 'const-class', '21c'), (0x1d, 'monitor-enter', '11x'), (0x1e, 'monitor-exit', '11x'),
    (0x1f, 'check-cast', '21c'), (0x20, 'instance-of', '22c'), (0x21, 'array-length', '12x'),
    (0x22, 'new-instance', '21c'), (0x23, 'new-array', '22c'),
    (0x24, 'filled-new-array', '35c'), (0x25, 'filled-new-array/range', '3rc'),
    (0x26, 'fill-array-data', '31t'), (0x27, 'throw', '11x'),
    (0x28, 'goto', '10t'), (0x29, 'goto/16', '20t'), (0x2a, 'goto/32', '30t'),
    (0x2b, 'packed-switch', '31t'), (0x2c, 'sparse-switch', '31t'),
    (0x2d, 'cmpl-float', '23x'), (0x2e, 'cmpg-float', '23x'),
    (0x2f, 'cmpl-double', '23x'), (0x30, 'cmpg-double', '23x'), (0x31, 'cmp-long', '23x'),
    (0x32, 'if-eq', '22t'), (0x33, 'if-ne', '22t'), (0x34, 'if-lt', '22t'),
    (0x35, 'if-ge', '22t'), (0x36, 'if-gt', '22t'), (0x37, 'if-le', '22t'),
    (0x38, 'if-eqz', '21t'), (0x39, 'if-nez', '21t'), (0x3a, 'if-ltz', '21t'),
    (0x3b, 'if-gez', '21t'), (0x3c, 'if-gtz', '21t'), (0x3d, 'if-lez', '21t'),
    (0x44, 'aget', '23x'), (0x45, 'aget-wide', '23x'), (0x46, 'aget-object', '23x'),
    (0x47, 'aget-boolean', '23x'), (0x48, 'aget-byte', '23x'), (0x49, 'aget-char', '23x'),
    (0x4a, 'aget-short', '23x'), (0x4b, 'aput', '23x'),
    (0x52, 'iget', '22c'), (0x53, 'iget-wide', '22c'), (0x54, 'iget-object', '22c'),
    (0x55, 'iget-boolean', '22c'), (0x56, 'iget-byte', '22c'), (0x57, 'iget-char', '22c'),
    (0x58, 'iget-short', '22c'), (0x59, 'iput', '22c'), (0x5a, 'iput-wide', '22c'),
    (0x5b, 'iput-object', '22c'), (0x5c, 'sget', '21c'), (0x5d, 'sget-wide', '21c'),
    (0x5e, 'sget-object', '21c'), (0x5f, 'sget-boolean', '21c'), (0x60, 'sget-byte', '21c'),
    (0x61, 'sget-char', '21c'), (0x62, 'sget-short', '21c'), (0x63, 'sput', '21c'),
    (0x64, 'sput-wide', '21c'), (0x65, 'sput-object', '21c'),
    (0x66, 'sput-boolean', '21c'), (0x67, 'sput-byte', '21c'),
    (0x6e, 'invoke-virtual', '35c'), (0x6f, 'invoke-super', '35c'),
    (0x70, 'invoke-direct', '35c'), (0x71, 'invoke-static', '35c'),
    (0x72, 'invoke-interface', '35c'), (0x74, 'invoke-virtual/range', '3rc'),
    (0x75, 'invoke-super/range', '3rc'), (0x76, 'invoke-direct/range', '3rc'),
    (0x77, 'invoke-static/range', '3rc'), (0x78, 'invoke-interface/range', '3rc'),
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
    (0x99, 'shr-int', '23x'), (0x9a, 'ushr-int', '23x'),
    (0xad, 'add-int/2addr', '12x'), (0xae, 'sub-int/2addr', '12x'),
    (0xaf, 'mul-int/2addr', '12x'), (0xb0, 'div-int/2addr', '12x'),
    (0xb1, 'rem-int/2addr', '12x'), (0xb2, 'and-int/2addr', '12x'),
    (0xb3, 'or-int/2addr', '12x'), (0xb4, 'xor-int/2addr', '12x'),
    (0xb5, 'shl-int/2addr', '12x'), (0xb6, 'shr-int/2addr', '12x'),
    (0xb7, 'ushr-int/2addr', '12x'),
    (0xd0, 'add-int/lit16', '22s'), (0xd1, 'rsub-int', '22s'), (0xd2, 'mul-int/lit16', '22s'),
    (0xd3, 'div-int/lit16', '22s'), (0xd4, 'rem-int/lit16', '22s'),
    (0xd5, 'and-int/lit16', '22s'), (0xd6, 'or-int/lit16', '22s'), (0xd7, 'xor-int/lit16', '22s'),
    (0xd8, 'add-int/lit8', '22b'), (0xd9, 'rsub-int/lit8', '22b'), (0xda, 'mul-int/lit8', '22b'),
    (0xdb, 'div-int/lit8', '22b'), (0xdc, 'rem-int/lit8', '22b'),
    (0xdd, 'and-int/lit8', '22b'), (0xde, 'or-int/lit8', '22b'), (0xdf, 'xor-int/lit8', '22b'),
    (0xe0, 'shl-int/lit8', '22b'), (0xe1, 'shr-int/lit8', '22b'), (0xe2, 'ushr-int/lit8', '22b'),
]:
    _add(c, n, f)


KIND = {}
for _c, _k in [(0x1a, 'string'), (0x1b, 'string'), (0x1c, 'type'), (0x1f, 'type'),
               (0x22, 'type'), (0x23, 'type'), (0x24, 'type'), (0x25, 'type'),
               (0x20, 'type'),
               (0x5c, 'field'), (0x5d, 'field'), (0x5e, 'field'), (0x5f, 'field'),
               (0x60, 'field'), (0x61, 'field'), (0x62, 'field'),
               (0x52, 'field'), (0x53, 'field'), (0x54, 'field'), (0x55, 'field'),
               (0x56, 'field'), (0x57, 'field'), (0x58, 'field')]:
    KIND[_c] = _k


def disasm(dex, code_off, limit=None, resolve=None):
    """code_item at code_off -> list of (addr, text)."""
    d = dex.d
    regs, ins, outs, tries, debug_off, insns_size = struct.unpack_from('<HHHHII', d, code_off)
    base = code_off + 16
    out = []
    pc = 0
    while pc < insns_size:
        w = struct.unpack_from('<H', d, base + pc * 2)[0]
        op = w & 0xff
        if op == 0x00 and w == 0x0100:   # packed-switch-payload
            size = struct.unpack_from('<H', d, base + pc * 2 + 2)[0]
            out.append((pc, "  .packed-switch-payload size=%d" % size))
            pc += size * 2 + 4
            continue
        if op == 0x00 and w == 0x0200:
            size = struct.unpack_from('<H', d, base + pc * 2 + 2)[0]
            out.append((pc, "  .sparse-switch-payload size=%d" % size))
            pc += size * 4 + 2
            continue
        if op == 0x00 and w == 0x0300:
            esize = struct.unpack_from('<H', d, base + pc * 2 + 2)[0]
            n = struct.unpack_from('<I', d, base + pc * 2 + 4)[0]
            out.append((pc, "  .array-data esize=%d n=%d" % (esize, n)))
            pc += (esize * n + 1) // 2 + 4
            continue
        name, fmt = OPS.get(op, ('op_%02x' % op, '10x'))
        w2 = struct.unpack_from('<H', d, base + pc * 2 + 2)[0] if pc + 1 < insns_size else 0
        w3 = struct.unpack_from('<H', d, base + pc * 2 + 4)[0] if pc + 2 < insns_size else 0
        a = (w >> 8) & 0xf
        b = (w >> 12) & 0xf
        desc = ''
        step = 1
        if fmt == '10x':
            desc = ''
        elif fmt == '12x':
            desc = 'v%d, v%d' % (a, b)
        elif fmt == '11x':
            desc = 'v%d' % a
        elif fmt == '11n':
            lit = b if b < 8 else b - 16
            desc = 'v%d, #%d' % (a, lit)
        elif fmt in ('21s', '21t', '21h'):
            val = struct.unpack('<h', struct.pack('<H', w2))[0]
            desc = 'v%d, #%d' % (a, val); step = 2
        elif fmt in ('21c', '22c', '22t', '22s', '22b', '23x'):
            if fmt == '21c':
                idx = w2; k = KIND.get(op, 'string')
                desc = 'v%d, %s' % (a, resolve(idx, k) if resolve else '@%d' % idx)
            elif fmt == '22c':
                idx = w2; k = KIND.get(op, 'type')
                desc = 'v%d, v%d, %s' % (a, b, resolve(idx, k) if resolve else '@%d' % idx)
            elif fmt == '22t':
                off = struct.unpack('<h', struct.pack('<H', w2))[0]
                desc = 'v%d, v%d, ->%d' % (a, b, pc + off)
            elif fmt == '22s':
                off = struct.unpack('<h', struct.pack('<H', w2))[0]
                desc = 'v%d, v%d, #%d' % (a, b, off)
            elif fmt == '22b':
                lit = (w2 >> 8) & 0xff
                lit = lit if lit < 128 else lit - 256
                desc = 'v%d, v%d, #%d' % (a, b, lit)
            else:  # 23x
                desc = 'v%d, v%d, v%d' % (a, (w2 >> 8) & 0xff, w2 & 0xff)
            step = 2
        elif fmt in ('31i', '31t', '31c'):
            if fmt == '31i':
                val = struct.unpack('<i', struct.pack('<I', (w3 << 16) | w2))[0]
                desc = 'v%d, #%d' % (a, val)
            elif fmt == '31t':
                off = struct.unpack('<i', struct.pack('<I', (w3 << 16) | w2))[0]
                desc = 'v%d, ->%d' % (a, pc + off)
            else:
                idx = (w3 << 16) | w2
                desc = 'v%d, %s' % (a, resolve(idx, 'string') if resolve else '@%d' % idx)
            step = 3
        elif fmt == '51l':
            val = struct.unpack('<q', struct.pack('<IIII', w2, w3,
                  struct.unpack_from('<H', d, base + pc*2 + 6)[0],
                  struct.unpack_from('<H', d, base + pc*2 + 8)[0]))[0]
            desc = 'v%d, #%d' % (a, val); step = 5
        elif fmt == '35c':
            # AOSP 布局: A|G|op  BBBB  F|E|D|C
            #   A (argc) = w>>12,  G = w>>8,  F/E/D/C 在 w3 的四个 nibble
            cnt = b                      # b 由本解码器定义为 (w>>12)&0xf = A
            midx_ = w2
            regs35 = [w3 & 0xf, (w3 >> 4) & 0xf, (w3 >> 8) & 0xf, (w3 >> 12) & 0xf, a]
            desc = '%s {%s}' % (resolve(midx_, 'method') if resolve else '@%d' % midx_,
                                ', '.join('v%d' % r for r in regs35[:cnt]))
            step = 3
        elif fmt == '3rc':
            cnt = (w >> 8) & 0xff
            midx_ = w2
            first = w3
            desc = '%s {%s}' % (resolve(midx_, 'method') if resolve else '@%d' % midx_,
                                ', '.join('v%d' % (first + i) for i in range(cnt)))
            step = 3
        elif fmt == '10t':
            off = struct.unpack('<b', struct.pack('<B', a))[0]
            desc = '->%d' % (pc + off)
        elif fmt == '20t':
            off = struct.unpack('<h', struct.pack('<H', w2))[0]
            desc = '->%d' % (pc + off); step = 2
        elif fmt == '30t':
            off = struct.unpack('<i', struct.pack('<I', (w3 << 16) | w2))[0]
            desc = '->%d' % (pc + off); step = 3
        elif fmt == '32x':
            desc = 'v%d, v%d' % (w2, w3); step = 3
        out.append((pc, '%-34s ' % (name + ' ' + desc) + ('[0x%04x]' % w)))
        pc += step
        if limit and len(out) > limit:
            break
    return out, regs, ins, outs
