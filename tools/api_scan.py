#!/usr/bin/env python3
"""Precise instruction scan: pull const-string + sget-object(Method) per method,
then reconstruct the REST contract of app/api/*."""
import struct, sys, json, importlib.util
from collections import OrderedDict

sp = importlib.util.spec_from_file_location("dd", "dexdump.py")
dd = importlib.util.module_from_spec(sp); sp.loader.exec_module(dd)
sp2 = importlib.util.spec_from_file_location("dv", "dalvik.py")
dv = importlib.util.module_from_spec(sp2); sp2.loader.exec_module(dv)

WIDTH = {}


def _w(fmt, n):
    WIDTH[fmt] = n


for f, n in [('10x', 1), ('12x', 1), ('11n', 1), ('11x', 1), ('10t', 1),
             ('20t', 2), ('22x', 2), ('21s', 2), ('21h', 2), ('21c', 2), ('21t', 2),
             ('23x', 2), ('22b', 2), ('22s', 2), ('22t', 2), ('22c', 2), ('35c', 3),
             ('31i', 3), ('31t', 3), ('31c', 3), ('32x', 3), ('30t', 3), ('3rc', 3),
             ('51l', 5)]:
    _w(f, n)

FMT_OF = {c: v[1] for c, v in dv.OPS.items()}
NAME_OF = {c: v[0] for c, v in dv.OPS.items()}


def short(t):
    return t.rsplit('/', 1)[-1].rstrip(';')


def iter_insns(d, code_off):
    regs, ins, outs, tries, debug_off, n = struct.unpack_from('<HHHHII', d.d, code_off)
    base = code_off + 16
    pc = 0
    while pc < n:
        w = struct.unpack_from('<H', d.d, base + pc * 2)[0]
        op = w & 0xff
        if op == 0x00:
            if w == 0x0100:
                size = struct.unpack_from('<H', d.d, base + pc * 2 + 2)[0]
                pc += size * 2 + 4
                continue
            if w == 0x0200:
                size = struct.unpack_from('<H', d.d, base + pc * 2 + 2)[0]
                pc += size * 4 + 2
                continue
            if w == 0x0300:
                esize = struct.unpack_from('<H', d.d, base + pc * 2 + 2)[0]
                cnt = struct.unpack_from('<I', d.d, base + pc * 2 + 4)[0]
                pc += (esize * cnt + 1) // 2 + 4
                continue
        fmt = FMT_OF.get(op, '10x')
        width = WIDTH.get(fmt, 1)
        w2 = struct.unpack_from('<H', d.d, base + pc * 2 + 2)[0] if width > 1 else 0
        w3 = struct.unpack_from('<H', d.d, base + pc * 2 + 4)[0] if width > 2 else 0
        yield pc, op, w2, w3
        pc += width


def build(path):
    d = dd.Dex(open(path, 'rb').read())

    def fld(idx):
        nn, off = d.field_ids
        cls, typ, name = struct.unpack_from('<HHI', d.d, off + idx * 8)
        return short(d.type(cls)), short(d.type(typ)), d.string(name)

    endpoints = []
    dataclasses = OrderedDict()
    for class_idx, access, super_idx, cdat, src in d.classes():
        cn = d.type(class_idx)
        if '/app/api/' not in cn or cdat == 0:
            continue
        p = cdat
        sf, p = d.uleb(p); inf, p = d.uleb(p); dm, p = d.uleb(p); vm, p = d.uleb(p)
        fi = 0
        for _ in range(sf):
            diff, p = d.uleb(p); fi += diff; _, p = d.uleb(p)
        fi = 0
        fields = []
        for _ in range(inf):
            diff, p = d.uleb(p); fi += diff; _, p = d.uleb(p)
            fields.append(fi)
        if fields:
            dataclasses[short(cn)] = [{'name': fld(f)[2], 'type': fld(f)[1]} for f in fields]

        for kind, cnt in (("direct", dm), ("virtual", vm)):
            mi = 0
            for _ in range(cnt):
                diff, p = d.uleb(p); mi += diff
                acc, p = d.uleb(p); code, p = d.uleb(p)
                if code == 0:
                    continue
                cls, nm, ret, params = d.method(mi)
                strings, verbs = [], []
                cur = None
                for addr, op, w2, w3 in iter_insns(d, code):
                    if op == 0x1a:      # const-string
                        try:
                            strings.append(d.string(w2))
                            cur = d.string(w2)
                        except Exception:
                            pass
                    elif op == 0x62:    # sget-object
                        try:
                            c, t, n2 = fld(w2)
                            if c == 'RequestBuilder$Method':
                                verbs.append(n2)
                        except Exception:
                            pass
                paths = [s for s in strings if s.startswith('/')]
                if paths or verbs:
                    endpoints.append({
                        'class': short(cn), 'fn': nm, 'ret': short(ret),
                        'params': [short(x) for x in params],
                        'paths': paths, 'verbs': verbs,
                    })
    return endpoints, dataclasses


if __name__ == '__main__':
    ep, dc = build(sys.argv[1] if len(sys.argv) > 1 else 'apk/classes.dex')
    print("endpoints with path/verb:", len(ep))
    for e in ep:
        print("%-28s %-28s %-8s %s %s" % (
            e['class'], e['fn'], '/'.join(e['verbs']) or '?',
            ' '.join(e['paths']), '->' + e['ret']))
