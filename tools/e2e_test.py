#!/usr/bin/env python3
"""端到端功能测试：真实 store.db + 真实 mihomo + 全部端点。

流程复刻真机：
  socketpair(非阻塞) -> exec core-linux with CHANNEL
  -> initialize(cacheDir, store.db, clash.socket, uuid)
  -> start
  -> PATCH /executor/config {activeProfile}   (触发加载 profile 的 content)
  -> 遍历所有端点，记录 HTTP 状态与响应体
"""
import json, os, socket, sqlite3, struct, subprocess, sys, tempfile, time, urllib.request

CORE = sys.argv[1] if len(sys.argv) > 1 else './core-linux'
PORT = int(sys.argv[2]) if len(sys.argv) > 2 else 18090
BASE = tempfile.mkdtemp(prefix='clash-e2e-')
CACHE, FILES, DB = (os.path.join(BASE, x) for x in ('cache', 'files', 'databases'))
for d in (CACHE, FILES, DB): os.makedirs(d, exist_ok=True)
STORE = os.path.join(DB, 'store.db')

PROFILE_ID = '11111111-1111-4111-8111-111111111111'

# 一个真实可用的 mihomo 配置：内联 proxy + rule provider + 规则
CONFIG = """mixed-port: 7890
mode: rule
log-level: info
proxies:
  - name: "node-ss"
    type: ss
    server: 127.0.0.1
    port: 8388
    cipher: aes-128-gcm
    password: testpass
    udp: true
proxy-groups:
  - name: "PROXY"
    type: select
    proxies: ["node-ss", DIRECT]
rules:
  - DOMAIN-SUFFIX,example.com,PROXY
  - GEOIP,CN,DIRECT
  - MATCH,PROXY
"""

# 建库（严格按挖出来的 DDL）
con = sqlite3.connect(STORE)
con.executescript("""
CREATE TABLE preferences (key VARCHAR(128) PRIMARY KEY, value BLOB);
CREATE TABLE profiles (
    id UUID PRIMARY KEY, version INTEGER, name TEXT, remoteURL TEXT,
    updateExpr TEXT, updatedAt TIMESTAMP, content BLOB, modeOverride VARCHAR(32));
CREATE TABLE user_infos (profileID UUID PRIMARY KEY, uploaded INTEGER, downloaded INTEGER,
    total INTEGER, expireAt TIMESTAMP,
    FOREIGN KEY(profileID) REFERENCES profiles(id) ON DELETE CASCADE ON UPDATE CASCADE);
CREATE TABLE providers (profileID UUID, type VARCHAR(16), name TEXT, updatedAt TIMESTAMP,
    content BLOB, FOREIGN KEY (profileID) REFERENCES profiles(id) ON DELETE CASCADE ON UPDATE CASCADE,
    PRIMARY KEY (profileID, type, name));
CREATE TABLE proxy_selections (profileID UUID, groupName TEXT, proxyName TEXT,
    FOREIGN KEY (profileID) REFERENCES profiles(id) ON DELETE CASCADE ON UPDATE CASCADE,
    PRIMARY KEY (profileID, groupName));
""")
con.execute("INSERT INTO profiles VALUES (?,?,?,?,?,?,?,?)",
            (PROFILE_ID, 1, '测试订阅', 'https://example.com/sub', '', '2024-01-01 00:00:00',
             CONFIG.encode(), None))
con.execute("INSERT INTO user_infos VALUES (?,?,?,?,?)", (PROFILE_ID, 1024, 2048, 4096000, '2025-01-01 00:00:00'))
con.execute("INSERT INTO proxy_selections VALUES (?,?,?)", (PROFILE_ID, 'PROXY', 'node-ss'))
con.commit(); con.close()

# 帧协议
def frame(t, cid, method, payload=b''):
    return struct.pack('>iqii', t, cid, method, len(payload)) + payload

def build_init(paths, legacy):
    CAP = 1024; buf = bytearray(b'\x00'*CAP); pos = CAP; objs = {}
    def cs(key, s):
        nonlocal pos
        b = s.encode(); pos -= 1; buf[pos] = 0
        st = pos - len(b); buf[st:st+len(b)] = b; pos = st
        pos -= 4; buf[pos:pos+4] = struct.pack('<I', len(b)); objs[key] = pos
    def ct(key, n, fields):
        nonlocal pos
        vl = 4 + n*2; bl = 4 + n*4
        bp = pos - bl; vp = bp - vl
        vt = struct.pack('<HH', vl, bl)
        for s in range(n): vt += struct.pack('<H', (4+s*4) if s in fields else 0)
        buf[vp:vp+vl] = vt
        struct.pack_into('<i', buf, bp, bp - vp)
        for s, t in fields.items():
            fp = bp + 4 + s*4
            struct.pack_into('<I', buf, fp, t - fp)
        pos = vp; objs[key] = bp
        return bp
    cs('s1', paths[0]); cs('s2', paths[1]); cs('s3', paths[2]); cs('s4', paths[3])
    cs('vp', legacy[0]); cs('v21', legacy[1]); cs('v23', legacy[2])
    leg = ct('legacy', 3, {0: objs['vp'], 1: objs['v21'], 2: objs['v23']})
    root = ct('root', 5, {0: objs['s1'], 1: objs['s2'], 2: objs['s3'], 3: objs['s4'], 4: leg})
    pos -= 4; struct.pack_into('<I', buf, pos, root - pos)
    return bytes(buf[pos:])

parent, child = socket.socketpair(socket.AF_UNIX, socket.SOCK_SEQPACKET)
child.setblocking(False)          # 真机就是这样
cfd = child.fileno(); os.set_inheritable(cfd, True)
env = dict(os.environ); env['CHANNEL'] = str(cfd)
proc = subprocess.Popen([CORE, '-v', '-http', '127.0.0.1:%d' % PORT],
                        env=env, pass_fds=(cfd,), stderr=subprocess.STDOUT)
child.close()
time.sleep(0.5)
if proc.poll() is not None:
    print("FAIL 内核提前退出 rc=", proc.returncode); sys.exit(1)

parent.settimeout(8)
parent.send(frame(0, 1, 1))                                    # start
print("start reply:", parent.recv(4096)[:20].hex())
parent.send(frame(0, 2, 0, build_init(
    [CACHE, STORE, os.path.join(FILES, 'clash.socket'), 'f47ac10b-58cc-4372-a567-0e02b2c3d479'],
    [FILES, os.path.join(DB, '21x'), os.path.join(DB, '23x')])))
print("init reply :", parent.recv(4096)[:20].hex())

def req(method, path, body=None):
    url = 'http://127.0.0.1:%d%s' % (PORT, path)
    data = json.dumps(body).encode() if body is not None else None
    r = urllib.request.Request(url, data=data, method=method,
                               headers={'Content-Type': 'application/json'})
    try:
        with urllib.request.urlopen(r, timeout=45) as resp:
            return resp.status, resp.read(400).decode('utf-8', 'replace')
    except urllib.error.HTTPError as e:
        return e.code, e.read(300).decode('utf-8', 'replace')
    except Exception as e:
        return -1, str(e)

time.sleep(1.0)
print("\n=== 加载 profile ===")
import time as _t
_t0 = _t.time()
print(req('PATCH', '/executor/config', {'activeProfile': PROFILE_ID}))
print("PATCH 耗时: %.1fs" % (_t.time() - _t0))
time.sleep(2.0)

print("\n=== 端点探测 ===")
ENDPOINTS = [
    ('GET','/status'), ('GET','/healthcheck'), ('GET','/stage'),
    ('GET','/listeners'), ('GET','/proxies'), ('GET','/proxies/PROXY'),
    ('GET','/config'), ('GET','/attributes'), ('GET','/general'),
    ('GET','/profiles'), ('GET','/profiles/config'),
    ('GET','/executor/config'), ('GET','/providers/proxies'), ('GET','/providers/rules'),
    ('GET','/connections/count'),
    ('GET','/network/interface'), ('GET','/network/resolver'),
    ('GET','/network/fakeip'), ('GET','/network/hosts'), ('GET','/network/outbound'),
]
for m, p in ENDPOINTS:
    st, body = req(m, p)
    print("%-3s %-26s %-4d %s" % (m, p, st, body[:150].replace('\n',' ')))

print("\n=== 切换代理（selector）===")
print(req('PUT', '/proxies/PROXY', {'name': 'node-ss'}))
print("\n=== 模式切换 ===")
print(req('PATCH', '/general', {'mode': 'global'}))
print(req('GET', '/general'))

proc.terminate()
try: proc.wait(timeout=3)
except Exception: proc.kill()
print("\nstore 路径:", STORE)
