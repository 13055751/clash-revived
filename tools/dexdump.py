#!/usr/bin/env python3
"""Minimal DEX parser: dump classes, native/external methods, and cross-refs."""
import struct, sys, io

NO_INDEX = 0xFFFFFFFF
ACC_NATIVE = 0x0100


class Dex:
    def __init__(self, data):
        self.d = data
        magic = data[:8]
        if not magic.startswith(b'dex\n'):
            raise SystemExit("not dex: %r" % magic)
        v = struct.unpack_from('<20I', data, 32)
        (file_size, hdr_size, endian, link_size, link_off, map_off, string_ids_size, string_ids_off,
         type_ids_size, type_ids_off, proto_ids_size, proto_ids_off,
         field_ids_size, field_ids_off, method_ids_size, method_ids_off,
         class_defs_size, class_defs_off, data_size, data_off) = v
        self.string_ids = (string_ids_size, string_ids_off)
        self.type_ids = (type_ids_size, type_ids_off)
        self.proto_ids = (proto_ids_size, proto_ids_off)
        self.field_ids = (field_ids_size, field_ids_off)
        self.method_ids = (method_ids_size, method_ids_off)
        self.class_defs = (class_defs_size, class_defs_off)
        self._str_cache = {}

    def uleb(self, pos):
        r = 0; s = 0
        while True:
            b = self.d[pos]; pos += 1
            r |= (b & 0x7f) << s
            if not b & 0x80:
                return r, pos
            s += 7

    def string(self, idx):
        if idx in self._str_cache:
            return self._str_cache[idx]
        n, off = self.string_ids
        if idx >= n:
            return "<bad-str>"
        p = struct.unpack_from('<I', self.d, off + idx * 4)[0]
        _, p = self.uleb(p)          # utf16 length
        end = self.d.index(b'\x00', p)
        s = self.d[p:end].decode('utf-8', 'replace')
        self._str_cache[idx] = s
        return s

    def type(self, idx):
        n, off = self.type_ids
        if idx >= n:
            return "<bad-type>"
        sidx = struct.unpack_from('<I', self.d, off + idx * 4)[0]
        return self.string(sidx)

    def proto(self, idx):
        n, off = self.proto_ids
        shorty, ret, params_off = struct.unpack_from('<III', self.d, off + idx * 12)
        ret_t = self.type(ret)
        plist = []
        if params_off != 0:
            sz = struct.unpack_from('<I', self.d, params_off)[0]
            for i in range(sz):
                t = struct.unpack_from('<H', self.d, params_off + 4 + i * 2)[0]
                plist.append(self.type(t))
        return ret_t, plist

    def method(self, idx):
        n, off = self.method_ids
        class_idx, proto_idx, name_idx = struct.unpack_from('<HHI', self.d, off + idx * 8)
        ret, params = self.proto(proto_idx)
        return self.type(class_idx), self.string(name_idx), ret, params

    def classes(self):
        n, off = self.class_defs
        out = []
        for i in range(n):
            (class_idx, access, superclass_idx, interfaces_off, source_file_idx,
             annotations_off, class_data_off, static_values_off) = struct.unpack_from('<IIIIIIII', self.d, off + i * 32)
            out.append((class_idx, access, superclass_idx, class_data_off, source_file_idx))
        return out

    def class_data(self, off):
        if off == 0:
            return None
        p = off
        sf, p = self.uleb(p)
        inf, p = self.uleb(p)
        dm, p = self.uleb(p)
        vm, p = self.uleb(p)
        for _ in range(sf + inf):
            _, p = self.uleb(p)   # field_idx_diff
            _, p = self.uleb(p)   # access_flags
        methods = []
        idx = 0
        for _ in range(dm):
            diff, p = self.uleb(p); idx += diff
            acc, p = self.uleb(p)
            code_off, p = self.uleb(p)
            methods.append((idx, acc, 'direct', code_off))
        idx = 0
        for _ in range(vm):
            diff, p = self.uleb(p); idx += diff
            acc, p = self.uleb(p)
            code_off, p = self.uleb(p)
            methods.append((idx, acc, 'virtual', code_off))
        return methods


def main(path):
    data = open(path, 'rb').read()
    d = Dex(data)
    ncls, _ = d.class_defs
    natives = []
    for class_idx, access, super_idx, cdat, src in d.classes():
        cname = d.type(class_idx)
        if cdat == 0:
            continue
        for midx, acc, kind, code in d.class_data(cdat):
            if acc & ACC_NATIVE or (code == 0 and kind == 'direct' and acc & 0x0100):
                cls, name, ret, params = d.method(midx)
                natives.append((cname, name, ret, params, acc, kind, code))
    print("classes: %d   native-ish methods: %d" % (ncls, len(natives)))
    for cname, name, ret, params, acc, kind, code in natives:
        print("%s -> %s %s(%s)  flags=0x%x kind=%s code=%s" % (
            cname, ret, name, ", ".join(params), acc, kind, code))


if __name__ == '__main__':
    main(sys.argv[1])


def dump_class(path, needle):
    data = open(path, 'rb').read()
    d = Dex(data)
    for class_idx, access, super_idx, cdat, src in d.classes():
        cname = d.type(class_idx)
        if needle not in cname:
            continue
        print("=== %s  (flags=0x%x, super=%s)" % (cname, access, d.type(super_idx) if super_idx != NO_INDEX else "-"))
        if cdat == 0:
            print("   (no class data)")
            continue
        # re-read full class_data with fields
        p = cdat
        sf, p = d.uleb(p); inf, p = d.uleb(p); dm, p = d.uleb(p); vm, p = d.uleb(p)
        fi = 0
        for is_static, cnt in ((True, sf), (False, inf)):
            fi = 0
            for _ in range(cnt):
                diff, p = d.uleb(p); fi += diff
                acc, p = d.uleb(p)
                print("   FIELD %s%s  idx=%d flags=0x%x" % ("static " if is_static else "", fi, fi, acc))
        for kind, cnt in (("direct", dm), ("virtual", vm)):
            mi = 0
            for _ in range(cnt):
                diff, p = d.uleb(p); mi += diff
                acc, p = d.uleb(p)
                code, p = d.uleb(p)
                cls, nm, ret, params = d.method(mi)
                print("   %-8s %s %s(%s)  flags=0x%x code=%s" % (kind, ret, nm, ", ".join(params), acc, code))
