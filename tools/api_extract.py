#!/usr/bin/env python3
"""Extract the REST contract (path, method, response type) from app/api/* classes."""
import struct, sys, json, importlib.util

sp = importlib.util.spec_from_file_location("dd", "dexdump.py")
dd = importlib.util.module_from_spec(sp); sp.loader.exec_module(dd)
sp2 = importlib.util.spec_from_file_location("dv", "dalvik.py")
dv = importlib.util.module_from_spec(sp2); sp2.loader.exec_module(dv)

METHOD_NAMES = {'GET': 'GET', 'POST': 'POST', 'PUT': 'PUT', 'PATCH': 'PATCH', 'DELETE': 'DELETE', 'HEAD': 'HEAD'}


def short(t):
    return t.rsplit('/', 1)[-1].rstrip(';')


def analyze(dex_path):
    d = dd.Dex(open(dex_path, 'rb').read())
    res = {'endpoints': [], 'data_classes': {}}

    # field helper
    def fld(idx):
        n, off = d.field_ids
        cls, typ, name = struct.unpack_from('<HHI', d.d, off + idx * 8)
        return short(d.type(cls)), short(d.type(typ)), d.string(name)

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
        inst_fields = []
        for _ in range(inf):
            diff, p = d.uleb(p); fi += diff; _, p = d.uleb(p)
            inst_fields.append(fi)
        if inst_fields:
            res['data_classes'][short(cn)] = [
                {'name': fld(f)[2], 'type': fld(f)[1]} for f in inst_fields
            ]

        for kind, cnt in (("direct", dm), ("virtual", vm)):
            mi = 0
            for _ in range(cnt):
                diff, p = d.uleb(p); mi += diff
                acc, p = d.uleb(p); code, p = d.uleb(p)
                if code == 0:
                    continue
                cls, nm, ret, params = d.method(mi)
                try:
                    insns, _, _, _ = dv.disasm(d, code)
                except Exception:
                    continue
                paths = []
                methods = []
                for addr, txt in insns:
                    if 'const-string' in txt:
                        # recover index from raw word printed as [0x....] is unreliable; re-read
                        pass
                    if 'RequestBuilder$Method->' in txt:
                        for m in METHOD_NAMES:
                            if (m + ':') in txt or (m + ' ') in txt:
                                methods.append(m)
                res['endpoints'].append({
                    'class': short(cn), 'method_name': nm, 'ret': short(ret),
                    'params': [short(x) for x in params],
                })
    return res


if __name__ == '__main__':
    r = analyze(sys.argv[1] if len(sys.argv) > 1 else 'apk/classes.dex')
    print(json.dumps(r, indent=1, ensure_ascii=False)[:2000])
