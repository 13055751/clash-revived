#!/usr/bin/env python3
"""APK Signature Scheme v2 —— 纯 Python 实现（读 / 写 / 自验）。

本文件被 tools/resign.py（重打包+签名）与 tools/verify_apk.py（独立验证器）共用。

格式要点（全部小端）：
  [local entries][APK Signing Block][Central Directory][EOCD]
  APK Signing Block:
    uint64  size          # = 后续内容长度（pairs + trailing size + magic）
    pairs:  uint64 value_len_plus_4, uint32 id, value[value_len_plus_4 - 4]
    uint64  size          # 与开头相同
    "APK Sig Block 42"    # 16 字节
  pair id 0x7109871a = v2：
    uint32 len(signers_seq)
      for each signer:
        uint32 len(signer)
          uint32 len(signed_data); signed_data
          uint32 len(signatures_seq)      # 注意：v2 没有 minSdk/maxSdk（那是 v3）
            for each: uint32 len(sig_entry); uint32 alg; uint32 len(sig); sig
          uint32 len(public_key); public_key (SPKI DER)
  signed_data:
    uint32 len(digests_seq)
      for each: uint32 len(digest_entry); uint32 alg; uint32 len(digest); digest
    uint32 len(certs_seq)
      for each: uint32 len(cert); cert DER
    uint32 len(attrs_seq)   # 附加属性，本项目写 0

内容摘要（关键，易错）：
  APK 切成三段：① sig block 之前全部内容 ② Central Directory ③ EOCD（其中
  "central directory offset" 字段改写为 sig block 的起始偏移）
  每段各自按 1 MiB 切块（不跨段合并），块摘要 = SHA256(0xa5 || uint32le(len) || block)
  最终摘要        = SHA256(0x5a || uint32le(总块数) || 各块摘要依次拼接)

算法 id：0x0103 = RSASSA-PKCS1-v1_5 + SHA-256
"""
import hashlib
import struct
import subprocess
import zlib

APK_SIG_BLOCK_MAGIC = b'APK Sig Block 42'
V2_PAIR_ID = 0x7109871A
ALG_RSA_PKCS1_SHA256 = 0x0103
CHUNK = 1048576


# --------------------------------------------------------------------------
# 1) 定位 / 解析
# --------------------------------------------------------------------------
def find_eocd(d):
    """返回 EOCD 起始偏移（从尾部向前找，且要求其注释长度自洽）。"""
    pos = len(d) - 22
    while pos >= 0:
        if d[pos:pos + 4] == b'PK\x05\x06':
            clen = struct.unpack_from('<H', d, pos + 20)[0]
            if pos + 22 + clen == len(d):
                return pos
        pos -= 1
    raise ValueError('EOCD not found')


def zip_layout(d):
    """-> dict(eocd, cd_off, cd_size, count, comment)"""
    eocd = find_eocd(d)
    count = struct.unpack_from('<H', d, eocd + 10)[0]
    cd_size = struct.unpack_from('<I', d, eocd + 12)[0]
    cd_off = struct.unpack_from('<I', d, eocd + 16)[0]
    clen = struct.unpack_from('<H', d, eocd + 20)[0]
    return dict(eocd=eocd, cd_off=cd_off, cd_size=cd_size, count=count,
                comment=d[eocd + 22:eocd + 22 + clen])


def parse_sig_block(d):
    """-> dict(block_start, block_size, pairs={id: value}) 或 None"""
    lay = zip_layout(d)
    cd_off = lay['cd_off']
    if cd_off < 16 or d[cd_off - 16:cd_off] != APK_SIG_BLOCK_MAGIC:
        return None
    size_end = struct.unpack_from('<Q', d, cd_off - 24)[0]
    block_start = cd_off - (size_end + 8)
    if block_start < 0:
        return None
    size_head = struct.unpack_from('<Q', d, block_start)[0]
    if size_head != size_end:
        raise ValueError('signing block size mismatch: %d vs %d' % (size_head, size_end))
    pairs = {}
    p = block_start + 8
    end = cd_off - 24
    while p < end:
        plen = struct.unpack_from('<Q', d, p)[0]
        pid = struct.unpack_from('<I', d, p + 8)[0]
        if plen < 4:
            raise ValueError('bad pair length %d' % plen)
        pairs[pid] = d[p + 12:p + 8 + plen]
        p += 8 + plen
    if p != end:
        raise ValueError('pairs overrun: %d != %d' % (p, end))
    return dict(block_start=block_start, block_size=size_end + 8, pairs=pairs,
                layout=lay)


def lp(buf, off):
    """读一个 uint32 长度前缀 + 内容 -> (content, next_off)"""
    n = struct.unpack_from('<I', buf, off)[0]
    off += 4
    return buf[off:off + n], off + n


def parse_v2_value(val):
    """解析 pair 0x7109871a 的 value -> signer 列表"""
    signers_seq, _ = lp(val, 0)
    signers = []
    off = 0
    while off < len(signers_seq):
        signer, off = lp(signers_seq, off)
        p = 0
        signed_data, p = lp(signer, p)
        sigs_seq, p = lp(signer, p)
        pubkey, p = lp(signer, p)
        sigs = []
        q = 0
        while q < len(sigs_seq):
            entry, q = lp(sigs_seq, q)
            alg = struct.unpack_from('<I', entry, 0)[0]
            sig, _ = lp(entry, 4)
            sigs.append((alg, sig))
        # signed_data 内部
        digests_seq, sp = lp(signed_data, 0)
        digests = []
        r = 0
        while r < len(digests_seq):
            entry, r = lp(digests_seq, r)
            alg = struct.unpack_from('<I', entry, 0)[0]
            dig, _ = lp(entry, 4)
            digests.append((alg, dig))
        certs_seq, sp = lp(signed_data, sp)
        certs = []
        r = 0
        while r < len(certs_seq):
            cert, r = lp(certs_seq, r)
            certs.append(cert)
        attrs_seq, sp = lp(signed_data, sp)
        signers.append(dict(signed_data=signed_data, sigs=sigs, pubkey=pubkey,
                            digests=digests, certs=certs, attrs=attrs_seq,
                            signer_raw=signer))
    return signers


# --------------------------------------------------------------------------
# 2) 内容摘要
# --------------------------------------------------------------------------
def chunk_digests(sections):
    """三段分别按 1 MiB 切块，返回所有块的摘要（顺序即文件顺序）。"""
    out = []
    for sec in sections:
        for i in range(0, len(sec), CHUNK):
            blk = sec[i:i + CHUNK]
            h = hashlib.sha256()
            h.update(b'\xa5' + struct.pack('<I', len(blk)))
            h.update(blk)
            out.append(h.digest())
    return out


def digest_sections(sec1, sec2, eocd_with_block_offset):
    """三段内容 -> (digest, chunk_count)。"""
    chunks = chunk_digests([sec1, sec2, eocd_with_block_offset])
    top = hashlib.sha256()
    top.update(b'\x5a' + struct.pack('<I', len(chunks)))
    for c in chunks:
        top.update(c)
    return top.digest(), len(chunks)


def content_digest(d, block_start=None):
    """对 d（含 signing block 的成品，或未签名的裸 zip）计算 v2 内容摘要。

    block_start=None 时，假定 d 里没有 signing block（cd_off 即分界）。
    """
    lay = zip_layout(d)
    if block_start is None:
        block_start = lay['cd_off']
    sec1 = d[:block_start]
    sec2 = d[lay['cd_off']:lay['cd_off'] + lay['cd_size']]
    eocd = bytearray(d[lay['eocd']:])
    struct.pack_into('<I', eocd, 16, block_start)
    return digest_sections(sec1, sec2, bytes(eocd))


# --------------------------------------------------------------------------
# 3) DER / RSA（纯 python 校验，避免依赖 java 的 apksigner）
# --------------------------------------------------------------------------
def der_read(buf, pos):
    """读一个 TLV -> (tag, value_bytes, next_pos)"""
    tag = buf[pos]
    pos += 1
    ln = buf[pos]
    pos += 1
    if ln & 0x80:
        n = ln & 0x7F
        ln = int.from_bytes(buf[pos:pos + n], 'big')
        pos += n
    return tag, buf[pos:pos + ln], pos + ln


def der_wrap(tag, content):
    """按 DER 编码把 (tag, content) 重新包成完整 TLV。"""
    n = len(content)
    if n < 0x80:
        return bytes([tag, n]) + content
    nb = n.to_bytes((n.bit_length() + 7) // 8, 'big')
    return bytes([tag, 0x80 | len(nb)]) + nb + content


def parse_spki_content(content):
    """SubjectPublicKeyInfo 的 SEQUENCE 内容 -> (n, e)"""
    tag, alg, p = der_read(content, 0)
    tag, bits, p = der_read(content, p)
    if tag != 0x03:
        raise ValueError('SPKI: no BIT STRING')
    bits = bits[1:]
    tag, rsa_seq, _ = der_read(bits, 0)
    tag, nb, p = der_read(rsa_seq, 0)
    tag, eb, p = der_read(rsa_seq, p)
    return int.from_bytes(nb, 'big'), int.from_bytes(eb, 'big')


def parse_spki(pubkey_der):
    """SubjectPublicKeyInfo（完整 TLV）-> (n, e)"""
    tag, seq, _ = der_read(pubkey_der, 0)
    if tag != 0x30:
        raise ValueError('SPKI: not SEQUENCE')
    tag, alg, p = der_read(seq, 0)
    tag, bits, p = der_read(seq, p)
    if tag != 0x03:
        raise ValueError('SPKI: no BIT STRING')
    bits = bits[1:]                      # 去掉 unused-bits 字节
    tag, rsa_seq, _ = der_read(bits, 0)
    tag, nb, p = der_read(rsa_seq, 0)
    tag, eb, p = der_read(rsa_seq, p)
    return int.from_bytes(nb, 'big'), int.from_bytes(eb, 'big')


def parse_cert_der_n_e(cert_der):
    """X.509 cert -> SPKI -> (n, e)"""
    tag, cert, _ = der_read(cert_der, 0)
    tag, tbs, p = der_read(cert, 0)
    # tbsCertificate: [0] version?, serial, sigalg, issuer, validity, subject, spki
    p = 0
    tag, _, p2 = der_read(tbs, p)
    if tag == 0xA0:                       # 显式 version
        p = p2
    for _ in range(5):                    # serial, sigalg, issuer, validity, subject
        tag, _, p = der_read(tbs, p)
    tag, spki_content, p = der_read(tbs, p)
    return parse_spki_content(spki_content)


SHA256_DIGEST_INFO_PREFIX = bytes.fromhex('3031300d060960864801650304020105000420')


def rsa_verify_pkcs1_sha256(n, e, msg, sig):
    """RSASSA-PKCS1-v1_5 + SHA-256 验签（纯 python）。"""
    k = (n.bit_length() + 7) // 8
    if len(sig) != k:
        return False, 'signature length %d != modulus %d' % (len(sig), k)
    s = int.from_bytes(sig, 'big')
    if s >= n:
        return False, 'signature >= modulus'
    m = pow(s, e, n)
    em = m.to_bytes(k, 'big')
    digest = hashlib.sha256(msg).digest()
    expected = b'\x00\x01' + b'\xff' * (k - 3 - len(SHA256_DIGEST_INFO_PREFIX) - 32) \
        + b'\x00' + SHA256_DIGEST_INFO_PREFIX + digest
    if em != expected:
        return False, 'PKCS#1 v1.5 padding/digest mismatch'
    return True, 'ok'


# --------------------------------------------------------------------------
# 4) 验证
# --------------------------------------------------------------------------
def verify_apk(path, openssl_crosscheck=True):
    """完整验证 v2：结构 + 内容摘要 + 签名（并可选用 openssl 交叉验证）。

    -> dict(ok=bool, steps=[(name, ok, detail), ...])
    """
    d = open(path, 'rb').read()
    steps = []

    def step(name, ok, detail=''):
        steps.append((name, ok, detail))
        return ok

    blk = parse_sig_block(d)
    if not blk:
        step('signing block 存在', False, '未找到 "APK Sig Block 42"')
        return dict(ok=False, steps=steps)
    step('signing block 存在', True,
         'start=0x%x size=%d pairs=%d' % (blk['block_start'], blk['block_size'],
                                          len(blk['pairs'])))
    if V2_PAIR_ID not in blk['pairs']:
        step('v2 pair (0x7109871a)', False, '缺失')
        return dict(ok=False, steps=steps)
    val = blk['pairs'][V2_PAIR_ID]
    step('v2 pair (0x7109871a)', True, 'value %d 字节' % len(val))
    try:
        signers = parse_v2_value(val)
    except Exception as exc:
        step('v2 结构解析', False, repr(exc))
        return dict(ok=False, steps=steps)
    step('v2 结构解析', True, 'signers=%d' % len(signers))
    if not signers:
        return dict(ok=False, steps=steps)
    sg = signers[0]
    step('signer 字段', True,
         'digests=%d certs=%d sigs=%d pubkey=%dB（v2 无 minSdk/maxSdk，那是 v3）'
         % (len(sg['digests']), len(sg['certs']), len(sg['sigs']), len(sg['pubkey'])))

    # 内容摘要
    got, nchunks = content_digest(d, block_start=blk['block_start'])
    declared = dict(sg['digests'])
    exp = declared.get(ALG_RSA_PKCS1_SHA256)
    if exp is None:
        step('内容摘要 (SHA-256)', False, '没有 alg=0x0103 的摘要，实际有 %s'
             % [hex(a) for a in declared])
        return dict(ok=False, steps=steps)
    ok = got == exp
    step('内容摘要 (SHA-256)', ok,
         'chunks=%d computed=%s declared=%s' % (nchunks, got.hex(), exp.hex()))

    # 签名：对 signed_data 验证
    sig = dict(sg['sigs']).get(ALG_RSA_PKCS1_SHA256)
    if sig is None:
        step('签名 (RSA PKCS#1 SHA-256)', False, '没有 alg=0x0103 的签名')
        return dict(ok=False, steps=steps)
    try:
        n, e = parse_spki(sg['pubkey'])
    except Exception as exc:
        step('public key 解析', False, repr(exc))
        return dict(ok=False, steps=steps)
    step('public key 解析', True, 'RSA %d bit, e=%d' % (n.bit_length(), e))

    # 公钥必须与证书一致
    if sg['certs']:
        try:
            cn, ce = parse_cert_der_n_e(sg['certs'][0])
            same = (cn == n and ce == e)
            step('公钥 == 证书公钥', same, '证书 %d bit' % cn.bit_length())
        except Exception as exc:
            step('公钥 == 证书公钥', False, repr(exc))
    ok_sig, why = rsa_verify_pkcs1_sha256(n, e, sg['signed_data'], sig)
    step('签名 (RSA PKCS#1 SHA-256, 纯 python)', ok_sig, why)

    if openssl_crosscheck:
        okx, detail = _openssl_check(sg['pubkey'], sg['signed_data'], sig)
        if okx is not None:
            step('签名 (openssl 交叉验证)', okx, detail)

    return dict(ok=all(s[1] for s in steps), steps=steps)


def _openssl_check(spki_der, msg, sig):
    """用 openssl 独立验一遍（不同实现，避免自证自话）。"""
    import os
    import tempfile
    try:
        with tempfile.TemporaryDirectory() as td:
            pk = os.path.join(td, 'pub.der')
            ms = os.path.join(td, 'msg.bin')
            sg = os.path.join(td, 'sig.bin')
            open(pk, 'wb').write(spki_der)
            open(ms, 'wb').write(msg)
            open(sg, 'wb').write(sig)
            r = subprocess.run(['openssl', 'pkey', '-pubin', '-inform', 'DER',
                                '-in', pk, '-out', os.path.join(td, 'pub.pem')],
                               capture_output=True)
            if r.returncode != 0:
                return None, r.stderr.decode()[:200]
            r = subprocess.run(['openssl', 'dgst', '-sha256', '-verify',
                                os.path.join(td, 'pub.pem'), '-signature', sg, ms],
                               capture_output=True)
            return r.returncode == 0, (r.stdout + r.stderr).decode().strip()[:200]
    except FileNotFoundError:
        return None, 'openssl 不可用'
    except Exception as exc:
        return None, repr(exc)


# --------------------------------------------------------------------------
# 5) 写 APK（对齐）+ 生成 v2 签名
# --------------------------------------------------------------------------
def deflate_raw(data, level=9):
    c = zlib.compressobj(level, zlib.DEFLATED, -15)
    return c.compress(data) + c.flush()


def crc32_of(data):
    return zlib.crc32(data) & 0xFFFFFFFF


def _pad_extra(header_off, name_len, base_extra, align):
    """计算使 data 起点对齐所需补齐后 extra 字段（zipalign 等价）。

    extra 必须是若干 (id:u16, size:u16, data) 段。若需要补 1..3 字节，
    段头自身就占了 4 字节，所以只能补 align+gap —— 结果仍然对齐。
    """
    data_off = header_off + 30 + name_len + len(base_extra)
    gap = (-data_off) % align
    if gap == 0:
        return base_extra
    if gap < 4:
        gap += align
    pad = gap - 4
    return base_extra + struct.pack('<HH', 0xD935, pad) + b'\x00' * pad


def build_zip(entries, align_of=None):
    """entries: list of dict(name, compress_type, data)。-> (blob_up_to_cd, cd_bytes, cd_off)

    blob_up_to_cd 以 PK\\x03\\x04 开头，末尾即 Central Directory 起点。
    """
    if align_of is None:
        align_of = lambda name: 4
    out = bytearray()
    central = bytearray()
    for e in entries:
        name = e['name'].encode('utf-8')
        data = e['data']
        method = e['compress_type']
        if method == 0:
            payload = data
        else:
            payload = deflate_raw(data)
            if len(payload) >= len(data):     # 压缩无收益则改存
                method = 0
                payload = data
        crc = crc32_of(data)
        align = align_of(e['name'])
        hdr_off = len(out)
        extra = _pad_extra(hdr_off, len(name), b'', align)
        flags = 0x0800                         # UTF-8 文件名
        out += struct.pack('<IHHHHHIIIHH', 0x04034B50, 20 if method == 8 else 10,
                           flags, method, 0, 0x21, crc, len(payload), len(data),
                           len(name), len(extra))
        out += name + extra
        assert len(out) % 1 == 0
        data_off = len(out)
        if method == 0:
            assert data_off % align == 0, (e['name'], data_off, align)
        out += payload
        central += struct.pack('<IHHHHHHIIIHHHHHII', 0x02014B50, 0x031E, 20,
                               flags, method, 0, 0x21, crc, len(payload), len(data),
                               len(name), len(extra), 0, 0, 0, 0, hdr_off)
        central += name + extra
    cd_off = len(out)
    return bytes(out), bytes(central), cd_off


def build_eocd(count, cd_size, cd_off, comment=b''):
    return struct.pack('<IHHHHIIH', 0x06054B50, 0, 0, count, count, cd_size, cd_off,
                       len(comment)) + comment


def _seq(items):
    """把若干「已带长度前缀的元素」拼成再带一层长度前缀的序列。"""
    body = b''.join(items)
    return struct.pack('<I', len(body)) + body


def _lp_bytes(b):
    return struct.pack('<I', len(b)) + b


def build_v2_value(signed_data, signature, pubkey_der):
    """v2 signer = signed_data + signatures + public key（均长度前缀；无 minSdk/maxSdk）。"""
    sig_entry = struct.pack('<I', ALG_RSA_PKCS1_SHA256) + _lp_bytes(signature)
    signer = (_lp_bytes(signed_data)
              + _seq([_lp_bytes(sig_entry)])
              + _lp_bytes(pubkey_der))
    return _seq([_lp_bytes(signer)])


def _assemble_block(pairs_bytes):
    size = len(pairs_bytes) + 8 + 16
    return struct.pack('<Q', size) + pairs_bytes + struct.pack('<Q', size) \
        + APK_SIG_BLOCK_MAGIC


def build_signed_data(digest, cert_der):
    digests = _seq([_lp_bytes(struct.pack('<I', ALG_RSA_PKCS1_SHA256) + _lp_bytes(digest))])
    certs = _seq([_lp_bytes(cert_der)])
    attrs = struct.pack('<I', 0)
    return digests + certs + attrs


def sign_bytes(key_pem, msg):
    """调用 openssl 做 RSASSA-PKCS1-v1_5 + SHA-256 签名。"""
    import os
    import tempfile
    with tempfile.TemporaryDirectory() as td:
        ms = os.path.join(td, 'm.bin')
        sg = os.path.join(td, 's.bin')
        open(ms, 'wb').write(msg)
        r = subprocess.run(['openssl', 'dgst', '-sha256', '-sign', key_pem,
                            '-out', sg, ms], capture_output=True)
        if r.returncode != 0:
            raise RuntimeError('openssl 签名失败: %s' % r.stderr.decode()[:400])
        return open(sg, 'rb').read()


def spki_from_cert(cert_der):
    """从 X.509 DER 证书里取出 SubjectPublicKeyInfo（DER）。"""
    tag, cert, _ = der_read(cert_der, 0)
    tag, tbs, p = der_read(cert, 0)
    p = 0
    tag, _, p2 = der_read(tbs, p)
    if tag == 0xA0:
        p = p2
    for _ in range(5):
        tag, _, p = der_read(tbs, p)
    tag, spki_content, p = der_read(tbs, p)
    return der_wrap(0x30, spki_content)
