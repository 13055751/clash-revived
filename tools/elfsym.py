#!/usr/bin/env python3
"""Dump dynamic symbols of an ELF shared object (python only, 64/32-bit)."""
import struct, sys


def parse(path, want=None):
    d = open(path, 'rb').read()
    if d[:4] != b'\x7fELF':
        raise SystemExit("not ELF")
    is64 = d[4] == 2
    little = d[5] == 1
    e = '<' if little else '>'
    if is64:
        e_shoff, = struct.unpack_from(e + 'Q', d, 0x28)
        e_shentsize, e_shnum, e_shstrndx = struct.unpack_from(e + 'HHH', d, 0x3A)
    else:
        e_shoff, = struct.unpack_from(e + 'I', d, 0x20)
        e_shentsize, e_shnum, e_shstrndx = struct.unpack_from(e + 'HHH', d, 0x2E)
    secs = []
    for i in range(e_shnum):
        o = e_shoff + i * e_shentsize
        if is64:
            name, typ, flags, addr, off, size, link, info, align, entsize = struct.unpack_from(e + 'IIQQQQIIQQ', d, o)
        else:
            name, typ, flags, addr, off, size, link, info, align, entsize = struct.unpack_from(e + 'IIIIIIIIII', d, o)
        secs.append(dict(name=name, typ=typ, off=off, size=size, link=link, entsize=entsize))
    shstr = secs[e_shstrndx]

    def sname(off):
        p = shstr['off'] + off
        return d[p:d.index(b'\x00', p)].decode('utf-8', 'replace')

    for s in secs:
        s['sname'] = sname(s['name'])
    dynsym = next((s for s in secs if s['sname'] == '.dynsym'), None)
    if not dynsym:
        print("no .dynsym")
        return []
    strtab = secs[dynsym['link']]

    def str_at(off):
        p = strtab['off'] + off
        return d[p:d.index(b'\x00', p)].decode('utf-8', 'replace')

    n = dynsym['size'] // (24 if is64 else 16)
    out = []
    for i in range(1, n):
        o = dynsym['off'] + i * (24 if is64 else 16)
        if is64:
            st_name, st_info, st_other, st_shndx, st_value, st_size = struct.unpack_from(e + 'IBBHQQ', d, o)
        else:
            st_name, st_value, st_size, st_info, st_other, st_shndx = struct.unpack_from(e + 'IIIBBH', d, o)
        nm = str_at(st_name) if st_name else ""
        bind = st_info >> 4
        typ = st_info & 0xF
        out.append((nm, st_value, st_size, bind, typ, st_shndx))
    return out


if __name__ == '__main__':
    path = sys.argv[1]
    filt = sys.argv[2] if len(sys.argv) > 2 else None
    syms = parse(path)
    print("total dynsym entries: %d" % len(syms))
    for nm, val, size, bind, typ, shndx in syms:
        if not nm or shndx == 0:
            continue
        if filt and filt not in nm:
            continue
        print("0x%08x size=%-8d bind=%d type=%d  %s" % (val, size, bind, typ, nm))
