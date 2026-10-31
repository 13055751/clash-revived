#!/usr/bin/env python3
"""复现宿主行为：帧头与载荷分两次 sendmsg（SEQPACKET 下是两个包）。"""
import os, socket, struct, subprocess, sys, time
CORE = sys.argv[1]
parent, child = socket.socketpair(socket.AF_UNIX, socket.SOCK_SEQPACKET)
child.setblocking(False)
cfd = child.fileno(); os.set_inheritable(cfd, True)
env = dict(os.environ); env['CHANNEL'] = str(cfd)
p = subprocess.Popen([CORE, '-v'], env=env, pass_fds=(cfd,), stderr=subprocess.STDOUT)
child.close(); time.sleep(0.6)
if p.poll() is not None:
    print("FAIL core 提前退出", p.returncode); sys.exit(1)

# start (空载荷)
parent.send(struct.pack('>iqii', 0, 1, 1, 0))
print("start reply:", parent.recv(4096)[:20].hex())

# 构造一个带载荷的 Call：故意分两次发
payload = b'{"probe":"payload"}'
hdr = struct.pack('>iqii', 0, 2, 99, len(payload))
parent.send(hdr)                # 第一包：只有 20 字节帧头
time.sleep(0.05)
parent.send(payload)            # 第二包：载荷
parent.settimeout(4)
try:
    r = parent.recv(4096)
    t, cid = struct.unpack_from('>i', r, 0)[0], struct.unpack_from('>q', r, 4)[0]
    print("reply type=%d callId=%d  -> %s" % (t, cid, "PASS" if t in (1,2) else "?"))
except socket.timeout:
    print("FAIL 超时（内核可能已退出）")
p.terminate()
try: p.wait(timeout=3)
except Exception: p.kill()
