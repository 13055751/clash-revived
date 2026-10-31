#!/usr/bin/env python3
"""Disassemble selected classes with resolved operands."""
import struct, sys, importlib.util

sp = importlib.util.spec_from_file_location("dd", "dexdump.py")
dd = importlib.util.module_from_spec(sp); sp.loader.exec_module(dd)
sp2 = importlib.util.spec_from_file_location("dv", "dalvik.py")
dv = importlib.util.module_from_spec(sp2); sp2.loader.exec_module(dv)


def make_resolver(d):
    def r(idx, kind):
        try:
            if kind == 'method':
                cls, nm, ret, params = d.method(idx)
                return "%s->%s(%s)%s" % (short(cls), nm,
                                         ','.join(short(p) for p in params), short(ret))
            if kind == 'field':
                n, off = d.field_ids
                cls, typ, name = struct.unpack_from('<HHI', d.d, off + idx * 8)
                return "%s->%s:%s" % (short(d.type(cls)), d.string(name), short(d.type(typ)))
            if kind == 'type':
                return short(d.type(idx))
            return '"%s"' % d.string(idx)[:80].replace('\n', '\\n')
        except Exception:
            return "@%d?" % idx
    return r


def short(t):
    return t.rsplit('/', 1)[-1].rstrip(';')


def iter_classes(d, needle):
    for class_idx, access, super_idx, cdat, src in d.classes():
        cn = d.type(class_idx)
        if needle in cn:
            yield cn, super_idx, cdat


def dump(path, needle, only=None):
    d = dd.Dex(open(path, 'rb').read())
    res = make_resolver(d)
    for cn, super_idx, cdat in iter_classes(d, needle):
        print("\n" + "=" * 78)
        print("CLASS %s  (super=%s)" % (cn, short(d.type(super_idx)) if super_idx != dd.NO_INDEX else '-'))
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
                if only and only not in nm:
                    continue
                print("\n--- %s %s %s(%s) : %s  [flags=0x%x]" % (
                    kind, short(ret), nm, ', '.join(short(x) for x in params), 'code@%d' % code, acc))
                if code == 0:
                    continue
                insns, regs, ins, outs = dv.disasm(d, code, resolve=res)
                print("    (regs=%d ins=%d outs=%d)" % (regs, ins, outs))
                for addr, txt in insns:
                    print("    %4d: %s" % (addr, txt.rstrip()))


if __name__ == '__main__':
    dump(sys.argv[1], sys.argv[2], sys.argv[3] if len(sys.argv) > 3 else None)
