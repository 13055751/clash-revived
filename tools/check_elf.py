#!/usr/bin/env python3
"""ELF 产物自查：形状是否匹配原版 libclash.so，是否满足 Android 新系统要求。

用法: python3 check_elf.py <elf-file> [更多文件...]
"""
import struct
import sys

EXPECTED = {
    'e_type': 3,          # ET_DYN / PIE
    'interp': '/system/bin/linker64',
    'machine': 0xB7,      # AArch64
}
MIN_ALIGN = 0x4000        # 16KB 页安全阈值


def check(path):
    try:
        d = open(path, 'rb').read()
    except OSError as e:
        return False, ["无法读取: %s" % e]

    fails, notes = [], []
    if len(d) < 64 or d[:4] != b'\x7fELF':
        return False, ["不是 ELF 文件"]

    is64 = d[4] == 2
    if not is64:
        return False, ["不是 ELF64"]

    e_type = struct.unpack_from('<H', d, 16)[0]
    e_machine = struct.unpack_from('<H', d, 18)[0]
    e_phoff, = struct.unpack_from('<Q', d, 0x20)
    e_phentsize, e_phnum = struct.unpack_from('<HH', d, 0x36)

    if e_type != EXPECTED['e_type']:
        fails.append("e_type=%d，期望 %d (ET_DYN/PIE)" % (e_type, EXPECTED['e_type']))
    if e_machine != EXPECTED['machine']:
        fails.append("e_machine=0x%x，期望 0x%x (AArch64)" % (e_machine, EXPECTED['machine']))

    interp = None
    aligns = []
    for i in range(e_phnum):
        o = e_phoff + i * e_phentsize
        if o + 56 > len(d):
            fails.append("程序头越界")
            break
        p_type, p_flags, p_offset, p_vaddr, p_paddr, p_filesz, p_memsz, p_align = \
            struct.unpack_from('<IIQQQQQQ', d, o)
        if p_type == 3:  # PT_INTERP
            interp = d[p_offset:p_offset + p_filesz].split(b'\x00')[0].decode('utf-8', 'replace')
        elif p_type == 1:  # PT_LOAD
            aligns.append(p_align)

    if interp != EXPECTED['interp']:
        fails.append("PT_INTERP=%r，期望 %r" % (interp, EXPECTED['interp']))
    if not aligns:
        fails.append("没有 PT_LOAD 段")
    else:
        bad = [a for a in aligns if a < MIN_ALIGN]
        if bad:
            fails.append("p_align 过小(4KB) = %s，16KB 页设备会 dlopen 失败"
                         % [hex(a) for a in bad])
        else:
            notes.append("p_align = %s（16KB 安全）" % [hex(a) for a in aligns])

    notes.append("体积 %.2f MB" % (len(d) / 1048576.0))
    return (not fails), fails + notes


def main(argv):
    if len(argv) < 2:
        print(__doc__)
        return 2
    all_ok = True
    for p in argv[1:]:
        ok, lines = check(p)
        print("%s %s" % ("PASS" if ok else "FAIL", p))
        for ln in lines:
            print("   %s %s" % ("!" if not ok else "-", ln))
        all_ok &= ok
    return 0 if all_ok else 1


if __name__ == '__main__':
    sys.exit(main(sys.argv))
