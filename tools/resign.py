#!/usr/bin/env python3
"""纯 Python 重打包 + APK Signature Scheme v2 签名（无 java / 无 apksigner / 无 zipalign）。

用法：
  python3 tools/resign.py <input.apk> <new-libclash.so> <output.apk> [选项]

选项：
  --abis arm64-v8a[,x86_64]  保留的 ABI（默认 arm64-v8a），其它 ABI 的 lib/ 全删
  --store-so                 把 lib/*.so 改为 STORED 并 16 KiB 对齐
                             （仅在 AndroidManifest 里 extractNativeLibs=false 时需要）
  --key PATH                 私钥（默认 tools/keys/apk.key.pem）
  --cert PATH                DER 证书（默认 tools/keys/apk.cert.der）；缺失时自动生成
  --no-verify                跳过签名后自验（不推荐）
  --keep-signatures          保留原 META-INF 的 v1 签名文件（默认删除）

做的事：
  1. 用 zipfile 读原 APK 全部条目（保持原有压缩方式，除 lib 外原样保留）
  2. 替换 lib/<abi>/libclash.so，删掉不用的 ABI
  3. 自己写 zip（精确控制每条目的数据偏移，4 字节对齐；STORED .so 时 16 KiB 对齐）
  4. 计算 v2 chunk digest（1 MiB 分块，0xa5 前缀；顶层 0x5a 前缀）
  5. 组 signed data → openssl 做 RSA PKCS#1 v1.5 + SHA-256 签名
  6. 拼 APK Signing Block（pair 0x7109871a），插到 Central Directory 之前，修正 EOCD
  7. 用 tools/verify_apk.py 的同一套代码自验：内容摘要 + RSA 验签 + openssl 交叉验证

注意：原始 APK 的签名私钥不可得，所以产物的签名证书是新的自签证书，
      与官方包签名不同 ⇒ 必须卸载原版再装（除非设备允许同签名覆盖）。
"""
import os
import subprocess
import sys
import zipfile

HERE = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, HERE)
import apkv2  # noqa: E402

KEYDIR = os.path.join(HERE, 'keys')
V1_SIG_SUFFIX = ('.SF', '.RSA', '.DSA', '.EC')


def log(msg):
    print(msg, flush=True)


# --------------------------------------------------------------------------
def ensure_keys(key_path, cert_der_path):
    """没有就用 openssl 造一对 RSA 2048 自签证书（不冒充原作者）。"""
    os.makedirs(os.path.dirname(key_path), exist_ok=True)
    cert_pem = os.path.splitext(cert_der_path)[0] + '.pem'
    if os.path.exists(key_path) and os.path.exists(cert_der_path):
        return
    log("[key] 未找到密钥/证书，生成 RSA 2048 自签证书：%s" % cert_der_path)
    subj = '/C=CN/O=clash-revived/CN=Clash Revived (unofficial build)/emailAddress=noreply@localhost'
    r = subprocess.run(['openssl', 'req', '-x509', '-newkey', 'rsa:2048', '-nodes',
                        '-sha256', '-days', '10000', '-keyout', key_path,
                        '-out', cert_pem, '-subj', subj],
                       capture_output=True)
    if r.returncode != 0:
        raise SystemExit('openssl 生成证书失败: %s' % r.stderr.decode()[:500])
    r = subprocess.run(['openssl', 'x509', '-in', cert_pem, '-outform', 'DER',
                        '-out', cert_der_path], capture_output=True)
    if r.returncode != 0:
        raise SystemExit('openssl 转 DER 失败: %s' % r.stderr.decode()[:500])
    os.chmod(key_path, 0o600)


# --------------------------------------------------------------------------
def collect_entries(in_apk, new_so_path, abis, store_so, keep_sigs):
    """-> list of dict(name, compress_type, data), dropped list"""
    z = zipfile.ZipFile(in_apk)
    so_bytes = open(new_so_path, 'rb').read()
    entries = []
    dropped = []
    replaced = []
    for info in z.infolist():
        name = info.filename
        base = os.path.basename(name)
        is_v1_sig = name.startswith('META-INF/') and (
            base == 'MANIFEST.MF' or base.upper().endswith(V1_SIG_SUFFIX))
        if is_v1_sig and not keep_sigs:
            dropped.append(name)
            continue
        parts = name.split('/')
        if len(parts) >= 3 and parts[0] == 'lib' and parts[1] not in abis:
            dropped.append(name)
            continue
        if name.startswith('lib/') and name.endswith('/libclash.so') and parts[1] == 'arm64-v8a':
            data = so_bytes
            replaced.append(name)
        else:
            data = z.read(name)
        ctype = info.compress_type
        if store_so and name.startswith('lib/') and name.endswith('.so'):
            ctype = zipfile.ZIP_STORED
        entries.append(dict(name=name, compress_type=ctype, data=data))
    if not replaced:
        raise SystemExit('输入 APK 里没有 lib/arm64-v8a/libclash.so，没法替换')
    return entries, dropped, replaced


def align_of(name, store_so):
    if store_so and name.startswith('lib/') and name.endswith('.so'):
        return 16384
    return 4


# --------------------------------------------------------------------------
def main():
    argv = sys.argv[1:]
    pos = [a for a in argv if not a.startswith('--')]
    flags = [a for a in argv if a.startswith('--')]
    if len(pos) < 3:
        print(__doc__)
        return 2
    in_apk, new_so, out_apk = pos[0], pos[1], pos[2]

    def optval(name, default=None):
        for i, a in enumerate(argv):
            if a == name and i + 1 < len(argv):
                return argv[i + 1]
            if a.startswith(name + '='):
                return a.split('=', 1)[1]
        return default

    abis = set(optval('--abis', 'arm64-v8a').split(','))
    store_so = '--store-so' in flags
    key_path = optval('--key', os.path.join(KEYDIR, 'apk.key.pem'))
    cert_der_path = optval('--cert', os.path.join(KEYDIR, 'apk.cert.der'))
    keep_sigs = '--keep-signatures' in flags
    do_verify = '--no-verify' not in flags

    log("[1/7] 读入 %s" % in_apk)
    entries, dropped, replaced = collect_entries(in_apk, new_so, abis, store_so, keep_sigs)
    log("      条目 %d（删 %d：%s%s）" % (
        len(entries), len(dropped),
        ', '.join(sorted({d.split('/')[1] for d in dropped if d.startswith('lib/')})) or 'v1 签名',
        '' if len(dropped) < 4 else ' 等'))
    for r in replaced:
        log("      替换 %s <- %s (%d 字节)" % (r, new_so, os.path.getsize(new_so)))

    log("[2/7] 重建 zip（4 字节对齐%s）" % ('，.so 16 KiB 对齐+STORED' if store_so else ''))
    local, cd, cd_off0 = apkv2.build_zip(entries, lambda n: align_of(n, store_so))
    n = len(entries)
    comment = apkv2.zip_layout(open(in_apk, 'rb').read())['comment']
    log("      local=0x%x cd=0x%x(=%d 字节) 条目=%d" % (cd_off0, cd_off0, len(cd), n))

    log("[3/7] 准备签名材料")
    ensure_keys(key_path, cert_der_path)
    cert_der = open(cert_der_path, 'rb').read()
    pubkey = apkv2.spki_from_cert(cert_der)
    log("      证书 %d 字节，公钥 SPKI %d 字节" % (len(cert_der), len(pubkey)))

    # 先用占位内容算块长度（RSA-2048 签名固定 256 字节，摘要固定 32 字节）
    dummy_sd = apkv2.build_signed_data(b'\x00' * 32, cert_der)
    dummy_block = apkv2._assemble_block(
        b'' + _pair(apkv2.build_v2_value(dummy_sd, b'\x00' * 256, pubkey)))
    block_size = len(dummy_block)
    cd_off_final = cd_off0 + block_size
    eocd = apkv2.build_eocd(n, len(cd), cd_off_final, comment)
    # 摘要用的 EOCD：按规范把 "central directory offset" 换成 signing block 起点
    eocd_for_digest = apkv2.build_eocd(n, len(cd), cd_off0, comment)
    log("      signing block 预计 %d 字节，CD 最终偏移 0x%x（摘要用 CD 偏移 0x%x）"
        % (block_size, cd_off_final, cd_off0))

    log("[4/7] 计算 v2 内容摘要（三段 / 1 MiB 分块）")
    digest, nchunks = apkv2.digest_sections(local, cd, eocd_for_digest)
    log("      chunks=%d digest=%s" % (nchunks, digest.hex()))

    log("[5/7] openssl RSA PKCS#1 v1.5 + SHA-256 签名")
    signed_data = apkv2.build_signed_data(digest, cert_der)
    sig = apkv2.sign_bytes(key_path, signed_data)
    if len(sig) != 256:
        raise SystemExit('签名长度异常: %d（期望 256）' % len(sig))
    log("      signed_data=%d 字节，signature=%d 字节" % (len(signed_data), len(sig)))

    log("[6/7] 组装 APK Signing Block 并写出")
    value = apkv2.build_v2_value(signed_data, sig, pubkey)
    block = apkv2._assemble_block(_pair(value))
    if len(block) != block_size:
        raise SystemExit('signing block 长度与占位不一致: %d != %d' % (len(block), block_size))
    out = local + block + cd + eocd
    with open(out_apk, 'wb') as f:
        f.write(out)
    log("      写出 %s（%d 字节）" % (out_apk, len(out)))

    log("[7/7] 自验")
    ok = True
    # (a) v2 结构 + 摘要 + 签名
    res = apkv2.verify_apk(out_apk)
    for name, pass_, detail in res['steps']:
        log("      %-4s %-38s %s" % ('PASS' if pass_ else 'FAIL', name, detail))
    ok &= res['ok']
    # (b) 用标准库 zipfile 回读，确认 CD/偏移正确、CRC 无误
    try:
        z = zipfile.ZipFile(out_apk)
        bad = z.testzip()
        cnt = len(z.namelist())
        ok_zip = (bad is None and cnt == n)
        log("      %-4s %-38s 条目=%d testzip=%s"
            % ('PASS' if ok_zip else 'FAIL', 'zipfile 回读 + CRC', cnt, bad or 'ok'))
        ok &= ok_zip
    except Exception as exc:
        log("      FAIL zipfile 回读  %r" % (exc,))
        ok = False
    # (c) 对齐检查
    bad_align = []
    for it in _local_offsets(out):
        name, off, method, align = it
        if method == 0 and off % align != 0:
            bad_align.append((name, off, align))
    log("      %-4s %-38s %s" % ('PASS' if not bad_align else 'FAIL', '条目数据对齐',
                                 '全部满足' if not bad_align else bad_align[:4]))
    ok &= not bad_align

    # (d) 内容逐条比对：除被替换/删除的条目，其余与原 APK 字节一致
    src = zipfile.ZipFile(in_apk)
    diff = []
    with zipfile.ZipFile(out_apk) as outz:
        out_names = set(outz.namelist())
        for e in entries:
            if e['name'] in replaced:
                continue
            if outz.read(e['name']) != src.read(e['name']):
                diff.append(e['name'])
        replaced_ok = all(outz.read(r) == open(new_so, 'rb').read() for r in replaced)
    log("      %-4s %-38s %s" % ('PASS' if not diff and replaced_ok else 'FAIL',
                                 '内容逐条比对',
                                 '154 条一致，libclash.so 已替换' if not diff and replaced_ok
                                 else ('差异=%s 替换ok=%s' % (diff[:4], replaced_ok))))
    ok &= (not diff and replaced_ok)
    log("      删除的条目: %s" % (', '.join(dropped) if dropped else '(无)'))

    log("")
    log("RESULT: %s" % ('PASS' if ok else 'FAIL'))
    return 0 if ok else 1


def _pair(value):
    return (__import__('struct').pack('<Q', len(value) + 4)
            + __import__('struct').pack('<I', apkv2.V2_PAIR_ID) + value)


def _local_offsets(d):
    """遍历本地文件头，产出 (name, data_offset, method, align) —— 对齐自检用。"""
    import struct
    p = 0
    while p + 4 <= len(d) and d[p:p + 4] == b'PK\x03\x04':
        (_, _ver, _flags, method, _t, _dt, _crc, csize, usize, nlen, elen) = \
            struct.unpack_from('<IHHHHHIIIHH', d, p)
        name = d[p + 30:p + 30 + nlen].decode('utf-8', 'replace')
        data_off = p + 30 + nlen + elen
        align = 16384 if (name.startswith('lib/') and name.endswith('.so')
                          and method == 0) else 4
        yield name, data_off, method, align
        p = data_off + csize


if __name__ == '__main__':
    sys.exit(main())
