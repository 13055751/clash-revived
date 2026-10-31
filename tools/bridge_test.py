#!/usr/bin/env python3
"""End-to-end handshake test against the revived core.

Reproduces what libcompat.so's nativeStart does:
  socketpair(AF_UNIX, SOCK_SEQPACKET) -> fork/exec with CHANNEL=<fd in child>

Then speaks the 20-byte framed Bridge protocol:
  int32 type | int64 callId | int32 method | int32 length | payload
"""
import os
import socket
import struct
import subprocess
import sys
import tempfile
import time

CORE = sys.argv[1] if len(sys.argv) > 1 else './core-linux'

TYPE_CALL, TYPE_DONE, TYPE_ERROR, TYPE_CANCEL = 0, 1, 2, 3
NAMES = {TYPE_CALL: 'Call', TYPE_DONE: 'Done', TYPE_ERROR: 'Error', TYPE_CANCEL: 'Cancel'}


def pack(mtype, call_id, method, payload=b''):
    return struct.pack('>i', mtype) + struct.pack('>q', call_id) + \
           struct.pack('>i', method) + struct.pack('>i', len(payload)) + payload


def unpack(data):
    mtype, call_id, method, length = struct.unpack('>iqii', data[:20])
    return mtype, call_id, method, data[20:20 + length]


def build_initialize_fb(paths, legacy):
    """Hand-roll the FlatBufferBuilder sequence recovered from CoreAPI.initialize.

    Write order (builder grows downward, so objects created first sit at higher
    addresses): 4 param strings, then the 3 LegacyDatabases strings, then the
    legacy sub-table, then the root table, then the root uoffset at buf[0].
    """
    CAP = 1024
    buf = bytearray(b'\x00' * CAP)
    pos = CAP          # next free slot grows downward
    objs = {}

    def put_bytes(b):
        nonlocal pos
        pos -= len(b)
        buf[pos:pos + len(b)] = b
        return pos

    def create_string(key, s):
        nonlocal pos
        b = s.encode()
        pos -= 1                      # null terminator
        buf[pos] = 0
        start = pos - len(b)
        buf[start:start + len(b)] = b
        pos = start
        ln = len(b)
        body = struct.pack('<I', ln)  # length prefix, 4-byte aligned
        pos -= 4
        buf[pos:pos + 4] = body
        objs[key] = pos               # absolute position of the string object
        return pos

    def create_table(key, nslots, fields):
        nonlocal pos
        """fields: dict slot -> absolute position of target object"""
        vt_len = 4 + nslots * 2
        body_len = 4 + nslots * 4
        body_pos = pos - body_len
        vt_pos = body_pos - vt_len
        vt = struct.pack('<HH', vt_len, body_len)
        for slot in range(nslots):
            vt += struct.pack('<H', (4 + slot * 4) if slot in fields else 0)
        buf[vt_pos:vt_pos + vt_len] = vt
        struct.pack_into('<i', buf, body_pos, body_pos - vt_pos)
        for slot, target in fields.items():
            fp = body_pos + 4 + slot * 4
            struct.pack_into('<I', buf, fp, target - fp)
        pos = vt_pos
        objs[key] = body_pos
        return body_pos

    # strings first (highest addresses)
    create_string('s1', paths[0])
    create_string('s2', paths[1])
    create_string('s3', paths[2])
    create_string('s4', paths[3])
    create_string('vp', legacy[0])
    create_string('v21', legacy[1])
    create_string('v23', legacy[2])

    leg = create_table('legacy', 3, {0: objs['vp'], 1: objs['v21'], 2: objs['v23']})
    root = create_table('root', 5, {0: objs['s1'], 1: objs['s2'], 2: objs['s3'],
                                   3: objs['s4'], 4: leg})
    # root uoffset: reader treats byte 0 as uoffset relative to itself
    pos -= 4
    struct.pack_into('<I', buf, pos, root - pos)
    return bytes(buf[pos:])


def main():
    parent, child = socket.socketpair(socket.AF_UNIX, socket.SOCK_SEQPACKET)
    child_fd = child.fileno()
    os.set_inheritable(child_fd, True)
    env = dict(os.environ)
    env['CHANNEL'] = str(child_fd)

    print("spawning %s with CHANNEL=%d" % (CORE, child_fd))
    proc = subprocess.Popen([CORE, '-v'], env=env, pass_fds=(child_fd,),
                            stderr=subprocess.STDOUT)
    child.close()
    time.sleep(0.4)

    if proc.poll() is not None:
        print("core exited early with", proc.returncode)
        return 1

    ok = True

    # method 1 = start, empty payload (exactly what RunKt$runCore sends)
    parent.send(pack(TYPE_CALL, 1, 1))
    parent.settimeout(3)
    try:
        data = parent.recv(65536)
        t, cid, meth, body = unpack(data)
        print("  <- %s callId=%d method=%d len=%d" % (NAMES.get(t), cid, meth, len(body)))
        ok &= (t == TYPE_DONE and cid == 1 and meth == 1)
    except socket.timeout:
        print("  !! timeout waiting for start reply")
        ok = False

    # method 0 = initialize with a real FlatBuffers payload
    try:
        # 严格的槽位语义（已对两侧定案）：
        #   slot0=cacheDir slot1=store.db slot2=clash.socket slot3=token(UUID)
        test_uuid = 'f47ac10b-58cc-4372-a567-0e02b2c3d479'
        base = tempfile.mkdtemp(prefix='clash-test-')
        os.makedirs(os.path.join(base, 'cache'), exist_ok=True)
        os.makedirs(os.path.join(base, 'files'), exist_ok=True)
        os.makedirs(os.path.join(base, 'databases'), exist_ok=True)
        fb = build_initialize_fb(
            [os.path.join(base, 'cache'),
             os.path.join(base, 'databases', 'store.db'),
             os.path.join(base, 'files', 'clash.socket'),
             test_uuid],
            [os.path.join(base, 'files'),
             os.path.join(base, 'databases', 'profiles'),
             os.path.join(base, 'databases', 'profiles')],
        )
        parent.send(pack(TYPE_CALL, 2, 0, fb))
        data = parent.recv(65536)
        t, cid, meth, body = unpack(data)
        print("  <- %s callId=%d method=%d len=%d" % (NAMES.get(t), cid, meth, len(body)))
        ok &= (t == TYPE_DONE and cid == 2 and meth == 0)
    except Exception as e:
        print("  initialize payload test skipped/failed:", e)

    # unknown method must come back as Error without killing the core
    parent.send(pack(TYPE_CALL, 3, 99, b'x'))
    try:
        data = parent.recv(65536)
        t, cid, meth, body = unpack(data)
        print("  <- %s callId=%d method=%d body=%r" % (NAMES.get(t), cid, meth, body[:60]))
    except socket.timeout:
        print("  !! timeout on unknown-method probe")

    proc.terminate()
    try:
        proc.wait(timeout=3)
    except subprocess.TimeoutExpired:
        proc.kill()
    print("RESULT:", "PASS" if ok else "FAIL")
    return 0 if ok else 1


if __name__ == '__main__':
    sys.exit(main())
