#!/usr/bin/env python3
"""Minimal AArch64 disassembler focused on call targets, string refs and immediates.

Not a general disassembler: it decodes enough to reconstruct a function's
call sequence (BL -> PLT), its ADRP/ADD string references, and MOVZ/MOVK
immediate constants.
"""
import struct, sys


class Elf:
    def __init__(self, path):
        self.d = open(path, 'rb').read()
        d = self.d
        assert d[:4] == b'\x7fELF'
        assert d[4] == 2, "expect ELF64"
        e_shoff, = struct.unpack_from('<Q', d, 0x28)
        e_shentsize, e_shnum, e_shstrndx = struct.unpack_from('<HHH', d, 0x3A)
        self.e_type = struct.unpack_from('<H', d, 16)[0]
        self.secs = []
        for i in range(e_shnum):
            o = e_shoff + i * e_shentsize
            name, typ, flags, addr, off, size, link, info, align, entsize = struct.unpack_from('<IIQQQQIIQQ', d, o)
            self.secs.append(dict(name=name, typ=typ, addr=addr, off=off, size=size,
                                  link=link, info=info, entsize=entsize))
        shstr = self.secs[e_shstrndx]
        for s in self.secs:
            p = shstr['off'] + s['name']
            s['sname'] = d[p:d.index(b'\x00', p)].decode('utf-8', 'replace')
        self.byname = {s['sname']: s for s in self.secs}

    def sec(self, n):
        return self.byname.get(n)

    def read(self, addr, n):
        """Read virtual-address range, mapping through sections."""
        for s in self.secs:
            if s['addr'] <= addr < s['addr'] + s['size'] and s['addr'] != 0:
                off = s['off'] + (addr - s['addr'])
                return self.d[off:off + n]
        return b''

    def cstr(self, addr):
        if addr == 0:
            return None
        raw = self.read(addr, 200)
        if not raw:
            return None
        return raw.split(b'\x00')[0].decode('utf-8', 'replace')

    def dynsyms(self):
        sym = self.sec('.dynsym')
        strs = self.sec('.dynstr')
        out = []
        n = sym['size'] // 24
        for i in range(n):
            o = sym['off'] + i * 24
            st_name, st_info, st_other, st_shndx, st_value, st_size = struct.unpack_from('<IBBHQQ', self.d, o)
            p = strs['off'] + st_name
            nm = self.d[p:self.d.index(b'\x00', p)].decode('utf-8', 'replace')
            out.append(dict(i=i, name=nm, value=st_value, size=st_size,
                            bind=st_info >> 4, typ=st_info & 0xF, shndx=st_shndx))
        return out

    def relaplts(self):
        r = self.sec('.rela.plt')
        if not r:
            return []
        out = []
        for i in range(r['size'] // 24):
            o = r['off'] + i * 24
            r_offset, r_info, r_addend = struct.unpack_from('<QQq', self.d, o)
            out.append(dict(offset=r_offset, sym=r_info >> 32, type=r_info & 0xFFFFFFFF, addend=r_addend))
        return out

    def reladyns(self):
        r = self.sec('.rela.dyn')
        if not r:
            return []
        out = []
        for i in range(r['size'] // 24):
            o = r['off'] + i * 24
            r_offset, r_info, r_addend = struct.unpack_from('<QQq', self.d, o)
            out.append(dict(offset=r_offset, sym=r_info >> 32, type=r_info & 0xFFFFFFFF, addend=r_addend))
        return out


def sext(v, bits):
    m = 1 << (bits - 1)
    return (v ^ m) - m


def disasm_fn(elf, start, size, syms, pltmap, limit=None):
    """Yield (addr, text, kind, detail)."""
    code = elf.read(start, size)
    n = len(code) // 4
    adrp = {}   # reg -> page address
    movz = {}   # reg -> value
    lines = []
    for i in range(n):
        if limit and i >= limit:
            break
        addr = start + i * 4
        insn = struct.unpack_from('<I', code, i * 4)[0]
        txt = ''
        kind = ''
        detail = None
        # BL / B
        if (insn & 0xFC000000) == 0x94000000:      # BL
            imm = sext(insn & 0x03FFFFFF, 26) << 2
            tgt = addr + imm
            name = pltmap.get(tgt)
            txt = 'bl  0x%x  %s' % (tgt, ('<' + name + '>') if name else '')
            kind = 'call'
            detail = name
        elif (insn & 0xFC000000) == 0x14000000:    # B
            imm = sext(insn & 0x03FFFFFF, 26) << 2
            txt = 'b   0x%x' % ((addr + imm) & 0xFFFFFFFFFFFFFFFF)
            kind = 'branch'
        elif (insn & 0xFFFFFC1F) == 0xD63F0000:    # BR
            reg = (insn >> 5) & 0x1F
            txt = 'br  x%d' % reg
        elif insn == 0xD65F03C0:
            txt = 'ret'
            kind = 'ret'
        elif (insn & 0x9F000000) == 0x90000000:    # ADRP
            rd = insn & 0x1F
            immlo = (insn >> 29) & 0x3
            immhi = (insn >> 5) & 0x7FFFF
            imm = sext((immhi << 2) | immlo, 21) << 12
            page = ((addr & ~0xFFF) + imm) & 0xFFFFFFFFFFFFFFFF
            adrp[rd] = page
            txt = 'adrp x%d, 0x%x' % (rd, page)
        elif (insn & 0x7F000000) == 0x11000000:     # ADD (imm, 32-bit form)
            rd = insn & 0x1F
            rn = (insn >> 5) & 0x1F
            imm12 = (insn >> 10) & 0xFFF
            sh = (insn >> 22) & 0x1
            val = imm12 << (12 if sh else 0)
            if rn in adrp:
                full = adrp[rn] + val
                s = elf.cstr(full)
                txt = 'add  x%d, x%d, #0x%x  ; =%r' % (rd, rn, val, s)
                kind = 'string' if s else 'addr'
                detail = s
            else:
                txt = 'add  x%d, x%d, #0x%x' % (rd, rn, val)
        elif (insn & 0x7F000000) == 0x31000000:     # ADDS (imm)
            txt = 'adds (imm)'
        elif (insn & 0x7F800000) == 0x52800000:     # MOVZ (32-bit)
            rd = insn & 0x1F
            imm16 = (insn >> 5) & 0xFFFF
            hw = (insn >> 21) & 0x3
            val = imm16 << (16 * hw)
            movz[rd] = val
            txt = 'mov  w%d, #%d' % (rd, val)
            kind = 'imm'
            detail = val
        elif (insn & 0x7F800000) == 0xD2800000:     # MOVZ (64-bit)
            rd = insn & 0x1F
            imm16 = (insn >> 5) & 0xFFFF
            hw = (insn >> 21) & 0x3
            val = imm16 << (16 * hw)
            movz[rd] = val
            txt = 'mov  x%d, #%d' % (rd, val)
            kind = 'imm'
            detail = val
        elif (insn & 0x7F800000) == 0x72800000 or (insn & 0x7F800000) == 0xF2800000:  # MOVK
            rd = insn & 0x1F
            imm16 = (insn >> 5) & 0xFFFF
            hw = (insn >> 21) & 0x3
            cur = movz.get(rd, 0)
            cur &= ~(0xFFFF << (16 * hw))
            cur |= imm16 << (16 * hw)
            movz[rd] = cur
            txt = 'movk x%d, #0x%x, lsl %d => %d' % (rd, imm16, 16 * hw, cur)
            kind = 'imm'
            detail = cur
        elif (insn & 0xFFC00000) == 0xA9800000 or (insn & 0xFFC00000) == 0xA9000000:  # STP
            txt = 'stp'
        elif (insn & 0xFFC00000) == 0xA8C00000 or (insn & 0xFFC00000) == 0xA9400000:  # LDP
            txt = 'ldp'
        elif (insn & 0xFF000000) == 0xAA000000:     # ORR / MOV (reg)
            txt = 'orr/mov'
        elif (insn & 0xFF800000) == 0xB9000000:
            txt = 'str (32)'
        else:
            txt = '.word 0x%08x' % insn
        lines.append((addr, txt, kind, detail))
    return lines


def build_pltmap(elf):
    plt = elf.sec('.plt')
    rela = elf.relaplts()
    syms = elf.dynsyms()
    out = {}
    if not plt:
        return out
    # AArch64 .plt: 16 bytes per entry, entry0 = PLT0
    entries = plt['size'] // 16
    for i in range(1, entries):
        addr = plt['addr'] + i * 16
        if i - 1 < len(rela):
            s = syms[rela[i - 1]['sym']]
            out[addr] = s['name']
    return out


if __name__ == '__main__':
    path = sys.argv[1]
    elf = Elf(path)
    syms = elf.dynsyms()
    plt = build_pltmap(elf)
    print("=== PLT map ===")
    for a, n in sorted(plt.items()):
        print("  0x%05x  %s" % (a, n))
    print("\n=== relocations (.rela.dyn) ===")
    for r in elf.reladyns():
        s = syms[r['sym']]['name'] if r['sym'] < len(syms) else '?'
        print("  off=0x%x type=%d sym=%s addend=%d" % (r['offset'], r['type'], s, r['addend']))
