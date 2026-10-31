#!/usr/bin/env python3
"""解析 Go 二进制（≥1.18 的 pclntab 格式）的函数名表，按子串过滤。

用法：python3 tools/gosym.py <elf> [filter] [--addr]
输出：addr（运行时虚拟地址） size 名称
      --addr 时额外给出文件偏移（仅 .text 内可靠）
"""
import sys
import re
import struct

GO_MAGICS = (0xFFFFFFF1, 0xFFFFFFF0, 0xFFFFFFFA, 0xFFFFFFF9)


def find_pclntab(d):
    """扫描 .rodata/.data.rel.ro 找 pclntab 头（魔数 + 合理的 nfunc/nfiles）。"""
    cands = []
    for m in re.finditer(b'\xf1\xff\xff\xff\x00\x00', d):
        off = m.start()
        try:
            pad1, pad2, minlc, ptrsize = struct.unpack_from('<BBBB', d, off + 4)
            if pad1 or pad2:
                continue
            if minlc not in (1, 2, 4) or ptrsize not in (4, 8):
                continue
            e = '<'
            nfunc, = struct.unpack_from(e + ('Q' if ptrsize == 8 else 'I'), d, off + 8)
            nfiles, = struct.unpack_from(e + ('Q' if ptrsize == 8 else 'I'), d,
                                         off + 8 + ptrsize)
            if not (100 < nfunc < 2000000 and nfiles < 2000000):
                continue
            cands.append((off, nfunc, nfiles, minlc, ptrsize))
        except Exception:
            continue
    return cands


def parse(d, off):
    e = '<'
    pad1, pad2, minlc, ptrsize = struct.unpack_from('<BBBB', d, off + 4)
    w = 'Q' if ptrsize == 8 else 'I'
    p = off + 8
    nfunc, = struct.unpack_from(e + w, d, p); p += ptrsize
    nfiles, = struct.unpack_from(e + w, d, p); p += ptrsize
    textStart, = struct.unpack_from(e + w, d, p); p += ptrsize
    funcnameOff, = struct.unpack_from(e + w, d, p); p += ptrsize
    cuOff, = struct.unpack_from(e + w, d, p); p += ptrsize
    filetabOff, = struct.unpack_from(e + w, d, p); p += ptrsize
    pctabOff, = struct.unpack_from(e + w, d, p); p += ptrsize
    pclnOff, = struct.unpack_from(e + w, d, p); p += ptrsize
    base = off
    funcname = base + funcnameOff
    pcln = base + pclnOff
    out = []
    for i in range(nfunc):
        entryoff, funcoff = struct.unpack_from('<II', d, pcln + i * 8)
        fo = pcln + funcoff
        nameoff, = struct.unpack_from('<i', d, fo + 4)
        # _func: entryOff uint32, nameOff int32, args int32, deferreturn uint32, pcsp...
        q = funcname + nameoff
        end = d.index(b'\x00', q)
        nm = d[q:end].decode('utf-8', 'replace')
        # funcID 之后的 pcfile/pcln 等不重要；用下一个函数入口推算 size
        out.append((textStart + entryoff, nm, i))
    out.sort()
    sized = []
    for k, (addr, nm, i) in enumerate(out):
        nxt = out[k + 1][0] if k + 1 < len(out) else addr
        sized.append((addr, max(0, nxt - addr), nm))
    return sized, dict(textStart=textStart, nfunc=nfunc, nfiles=nfiles,
                       minlc=minlc, ptrsize=ptrsize, base=base)


def text_delta(d):
    """返回 (text_addr, text_off)，用于 addr -> file offset。"""
    e_shoff, = struct.unpack_from('<Q', d, 0x28)
    e_shentsize, e_shnum, e_shstrndx = struct.unpack_from('<HHH', d, 0x3A)
    secs = []
    for i in range(e_shnum):
        o = e_shoff + i * e_shentsize
        secs.append(struct.unpack_from('<IIQQQQIIQQ', d, o))
    sh = secs[e_shstrndx]
    def sname(off):
        q = sh[4] + off
        return d[q:d.index(b'\x00', q)].decode('utf-8', 'replace')
    for s in secs:
        if sname(s[0]) == '.text':
            return s[3], s[4]
    return 0, 0


def main():
    path = sys.argv[1]
    filt = None
    show_addr = '--addr' in sys.argv
    for a in sys.argv[2:]:
        if not a.startswith('--'):
            filt = a
    d = open(path, 'rb').read()
    cands = find_pclntab(d)
    if not cands:
        raise SystemExit("pclntab not found")
    best = max(cands, key=lambda c: c[1])
    syms, hdr = parse(d, best[0])
    taddr, toff = text_delta(d)
    # PIE 构建下 header 里的 textStart 常为 0：此时 entryoff 是相对 .text 起点的偏移
    if hdr['textStart'] == 0 and taddr:
        syms = [(taddr + a, sz, nm) for a, sz, nm in syms]
        hdr['textStart'] = taddr
    print("pclntab@0x%x nfunc=%d nfiles=%d minLC=%d ptrSize=%d textStart=0x%x text(.text)=0x%x/off 0x%x"
          % (best[0], hdr['nfunc'], hdr['nfiles'], hdr['minlc'], hdr['ptrsize'],
             hdr['textStart'], taddr, toff))
    n = 0
    for addr, size, nm in syms:
        if filt and filt not in nm:
            continue
        n += 1
        if show_addr:
            foff = addr - taddr + toff if addr >= taddr else -1
            print("0x%08x size=%-6d file=0x%08x  %s" % (addr, size, foff, nm))
        else:
            print("%-6d %s" % (size, nm))
    print("# matched %d" % n)


if __name__ == '__main__':
    main()
