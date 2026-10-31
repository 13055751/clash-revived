#!/usr/bin/env python3
"""Minimal Android binary XML (AXML) parser -> readable tree."""
import struct, sys

RES_STRING_POOL = 0x0001
RES_XML = 0x0003
RES_XML_START_ELEMENT = 0x0102
RES_XML_END_ELEMENT = 0x0103
RES_XML_RESOURCE_MAP = 0x0180

ATTR_IDS = {
    0x01010003: "name", 0x01010000: "theme", 0x01010001: "label", 0x01010002: "icon",
    0x01010010: "debuggable", 0x0101000f: "enabled", 0x01010018: "exported",
    0x01010024: "value", 0x01010025: "resource", 0x01010026: "mimeType",
    0x01010027: "scheme", 0x01010028: "host", 0x01010029: "port", 0x0101002a: "path",
    0x0101002b: "pathPrefix", 0x0101002c: "pathPattern", 0x0101001e: "authorities",
    0x0101001d: "grantUriPermissions", 0x01010020: "initOrder",
    0x01010023: "targetActivity",
    0x01010004: "permission", 0x01010005: "readPermission", 0x01010006: "writePermission",
    0x01010007: "protectionLevel", 0x01010008: "permissionGroup", 0x01010009: "sharedUserId",
    0x0101000a: "hasCode", 0x0101000b: "include",
    0x0101000d: "priority", 0x0101000e: "required", 0x01010011: "process",
    0x01010012: "taskAffinity", 0x01010013: "multiprocess", 0x01010014: "finishOnTaskLaunch",
    0x01010015: "clearTaskOnLaunch", 0x01010016: "stateNotNeeded", 0x01010007 + 0x10: "excludeFromRecents",
    0x01010019: "contentProvider", 0x0101001b: "allowTaskReparenting",
    0x0101001c: "alwaysRetainTaskState", 0x0101020c: "minSdkVersion",
    0x0101021b: "versionCode", 0x0101021c: "versionName", 0x01010270: "targetSdkVersion",
    0x01010271: "maxSdkVersion", 0x010102b1: "compileSdkVersion",
    0x010100b3: "glEsVersion", 0x01010281: "installLocation",
    0x01010030: "supportsRtl", 0x010102ac: "largeHeap",
    0x01010540: "extractNativeLibs", 0x01010541: "usesCleartextTraffic",
    0x0101053b: "requestLegacyExternalStorage",
    0x01010636: "enableOnBackInvokedCallback",
    0x01010290: "exported",
}


def parse_pool(buf, off):
    typ, hsz, sz, count, style_count, style_off, str_off, style_ptr = struct.unpack_from("<HHIIIIII", buf, off)
    if typ != RES_STRING_POOL:
        return []
    flags = struct.unpack_from("<I", buf, off + 16)[0]
    utf8 = bool(flags & 0x100)
    offs = struct.unpack_from("<%dI" % count, buf, off + hsz)
    data_start = off + str_off
    strings = []
    for o in offs:
        p = data_start + o
        if utf8:
            n16 = buf[p]; p += 1
            if n16 & 0x80:
                n16 = ((n16 & 0x7f) << 8) | buf[p]; p += 1
            n8 = buf[p]; p += 1
            if n8 & 0x80:
                n8 = ((n8 & 0x7f) << 8) | buf[p]; p += 1
            strings.append(buf[p:p + n8].decode("utf-8", "replace"))
        else:
            n = struct.unpack_from("<H", buf, p)[0]; p += 2
            if n & 0x8000:
                n = ((n & 0x7fff) << 16) | struct.unpack_from("<H", buf, p)[0]; p += 2
            strings.append(buf[p:p + n * 2].decode("utf-16-le", "replace"))
    return strings


def parse(data):
    buf = data
    typ, hsz, total = struct.unpack_from("<HHI", buf, 0)
    if typ != RES_XML:
        raise SystemExit("not axml (type=0x%x)" % typ)
    pos = hsz
    strings, resmap, out, depth = [], [], [], 0
    while pos + 8 <= len(buf):
        ctype, chsz, csz = struct.unpack_from("<HHI", buf, pos)
        if csz == 0:
            break
        if ctype == RES_STRING_POOL:
            strings = parse_pool(buf, pos)
        elif ctype == RES_XML_RESOURCE_MAP:
            n = (csz - chsz) // 4
            resmap = list(struct.unpack_from("<%dI" % n, buf, pos + chsz))
        elif ctype == RES_XML_START_ELEMENT:
            ns_i, name_i, attr_start, attr_size, attr_count = struct.unpack_from("<IIHHH", buf, pos + chsz)
            name = strings[name_i] if name_i < len(strings) else "?"
            attrs = []
            ap = pos + chsz + attr_start
            for i in range(attr_count):
                a = ap + i * attr_size
                ans, aname, araw = struct.unpack_from("<III", buf, a)
                asize2, res0, atyp = struct.unpack_from("<HBB", buf, a + 12)
                adata = struct.unpack_from("<I", buf, a + 16)[0]
                key = None
                if 0 <= aname < len(resmap):
                    key = ATTR_IDS.get(resmap[aname])
                if key is None:
                    key = strings[aname] if aname < len(strings) else "attr_0x%x" % aname
                if araw != 0xFFFFFFFF and araw < len(strings):
                    val = repr(strings[araw])
                elif atyp == 0x12:
                    val = "true" if adata else "false"
                elif atyp == 0x10:
                    val = str(struct.unpack("<i", struct.pack("<I", adata))[0])
                elif atyp == 0x11:
                    val = hex(adata)
                elif atyp == 0x01:
                    val = "@ref0x%08x" % adata
                else:
                    val = "0x%x" % adata
                attrs.append("%s=%s" % (key, val))
            out.append("  " * depth + "<%s %s>" % (name, " ".join(attrs)))
            depth += 1
        elif ctype == RES_XML_END_ELEMENT:
            depth = max(0, depth - 1)
        pos += csz
    return "\n".join(out)


if __name__ == "__main__":
    with open(sys.argv[1], "rb") as f:
        print(parse(f.read()))
