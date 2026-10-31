#!/usr/bin/env python3
"""复现 nativeStart：非阻塞 socketpair + CHANNEL env。"""
import os, socket, struct, subprocess, sys, time
CORE = sys.argv[1]
parent, child = socket.socketpair(socket.AF_UNIX, socket.SOCK_SEQPACKET)
child.setblocking(False)                 # nativeStart 对 sv[1] 做了 O_NONBLOCK
cfd = child.fileno()
os.set_inheritable(cfd, True)
env = dict(os.environ); env['CHANNEL'] = str(cfd)
p = subprocess.Popen([CORE, '-v'], env=env, pass_fds=(cfd,), stderr=subprocess.STDOUT)
child.close()
time.sleep(0.6)
alive = p.poll() is None
print("core alive:", alive)
if alive:
    parent.settimeout(3)
    parent.send(struct.pack('>iqii', 0, 1, 1, 0))
    try:
        r = parent.recv(4096)
        print("reply type=%d callId=%d" % (struct.unpack_from('>i', r, 0)[0], struct.unpack_from('>q', r, 4)[0]))
    except socket.timeout:
        print("no reply")
    p.terminate(); p.wait(timeout=3)
else:
    print("EXITED", p.returncode)
