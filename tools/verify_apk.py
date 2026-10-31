#!/usr/bin/env python3
"""独立 APK v2 签名验证器（纯 python，可选 openssl 交叉验证）。

用法：
  python3 tools/verify_apk.py <apk> [--quiet]

输出每一步的 PASS/FAIL 与关键数值；全部 PASS 时退出码 0，否则 1。
"""
import sys
import os

HERE = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, HERE)
import apkv2  # noqa: E402


def main():
    args = [a for a in sys.argv[1:] if not a.startswith('--')]
    quiet = '--quiet' in sys.argv
    if not args:
        print(__doc__)
        return 2
    path = args[0]
    res = apkv2.verify_apk(path)
    if not quiet:
        print("APK: %s  (%d bytes)" % (path, os.path.getsize(path)))
        print("-" * 78)
    for name, ok, detail in res['steps']:
        print("  %-4s %-38s %s" % ('PASS' if ok else 'FAIL', name, detail))
    print("-" * 78)
    print("RESULT: %s" % ('PASS' if res['ok'] else 'FAIL'))
    return 0 if res['ok'] else 1


if __name__ == '__main__':
    sys.exit(main())
