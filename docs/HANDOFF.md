# HANDOFF — Clash for Android 3.0.3.premium 复活工程

> 目标：保留原 UI 与使用逻辑，把已停更的内核替换为支持新协议（VLESS / Reality / Hysteria2 / TUIC）的实现，延续 Shizuku / root 的 TUN 方案。
>
> 当前状态：**代码完成到「有硬证据支撑的上限」，等待真机验证一件只有真机能回答的事。**

---

## 0. 阻塞项已解除：真机验证通过（后半段）

### ✅ `fork+exec` 成功

2026-10-29 真机安装首发：app 正常启动、UI 完整，**内核成功被 exec**（宿主报 `sendmsg failed: EPIPE`，说明它在往通道写数据时对端已关闭 —— 即内核进程曾经存在过）。

**结论：`.so` 能被 `fork+exec` 拉起，`extractNativeLibs=true` 的推断在真机成立。** 架构不需要推倒重来。

### ✅ 首发失败原因与修复：`O_NONBLOCK`

宿主 `nativeStart` 对 socketpair 做了 `fcntl(sv[1], F_SETFL, O_NONBLOCK)`（反汇编 0x1644）。我们的读循环是阻塞式的，首次 `Recvmsg` 在宿主尚未发消息时立即返回 `EAGAIN`，被当成致命错误 → 内核 **2.3ms 即退出** → 宿主随后 `sendmsg` 得到 EPIPE。

修复（两处，双保险）：

```go
// main.go —— attach 前清除非阻塞标志
syscall.SetNonblock(fd, false)

// transport.go —— 且把 EAGAIN/EINTR 当可重试，不当致命错误
if err == syscall.EAGAIN || err == syscall.EINTR { time.Sleep(10ms); continue }
```

修复后用非阻塞 socketpair 复现测试：
```
修复前: bridge read ended err="recvmsg: resource temporarily unavailable" / exited uptime=2.3ms
修复后: bridge attached via CHANNEL fd=4 blocking=true
        recv type=Call callId=1 method=1 → reply Done 帧
        "Start initial configuration in progress"   ← mihomo 真的起来了
```

**这条的普适教训**：fork+exec 的通道 fd 如果由父进程设为非阻塞，子进程必须显式处理，否则「没有数据」会被误判成「通道坏了」。

### 仍有待验证（真机第二轮）

app 现在能启动内核，但**能否完成整条链路**（initialize → start → HTTP 端点 → UI 加载出订阅列表）尚未验证。

---

## 0.1 原阻塞项说明（保留作为背景）

**`fork+exec` 在真机上能否成功。**

整个方案建立在一个判断上：原内核 `libclash.so` 不是 JNI 库，而是一个被 `:core` 进程 `fork+exec` 起来的独立 Go 程序。证据是：

```
e_type = 3 (ET_DYN/PIE)
PT_INTERP = /system/bin/linker64
导出 main.main / _start
导出符号中 Java_* 数量 = 0
```

而 `libcompat.so` 里有 `Java_..._AndroidProcess_nativeStart(String)` 负责 `fork` + `execve`。

**已确认**：`AndroidManifest.xml` 的 `extractNativeLibs=true`（与原版逐字节一致），所以 `.so` 会解压到 `nativeLibraryDir`。

**未确认**：那个目录在某些厂商 ROM 上是否可执行（`noexec` 挂载）。这不是静态分析能回答的。

- 若成功 → 继续补端点、修细节
- 若失败（`execve` 返回 EACCES/EPERM）→ 需改为 JNI 库加载架构，工作量大

---

## 1. 架构（逆向所得）

```
app 进程 ── Compose UI + clash/app/api/*（Ktor 客户端，消费 JSON over HTTP）
   │          Binder / ClashProvider / ForwardService
:core 进程 ── Kotlin：AndroidPlatform + AndroidProcess(fork/exec) + AndroidChannel(socketpair)
   │          双向 RPC：20 字节帧头 + 载荷
libclash.so ── Go 内核（新实现）：chi HTTP server + mihomo 引擎
```

### 线路协议（逐字节确认）

```
帧头 20 字节，大端
  int32  type    0=Call 1=Done 2=Error 3=Cancel
  int64  callId
  int32  method
  int32  length
载荷：length 字节
```

通道：环境变量 `CHANNEL=<fd>` 交接，类型 `SOCK_SEQPACKET`，fd 通过 `SCM_RIGHTS` 附带。

证据链：`Transport.readHeader` 的 `ByteBuffer.allocate(20)` + `getInt/getLong/getInt/getInt`；`sub_19c8` 里 `vsnprintf(buf, 32, "CHANNEL=%d", fd)`（`.rodata[0x1060]`）。

### method 语义

| 方向 | method | 名称 | 载荷 |
|---|---|---|---|
| host→core | 0 | initialize | FlatBuffers：4 string + LegacyDatabases 子表 |
| host→core | 1 | start | 空 |
| host→core | 2 | sendEvent | 单槽 int32 事件码 |
| core→host | 0..12 | 13 项 Platform 能力 | 见 `PLATFORM_RPC.md` |

**initialize 的槽位（已定案）**：

```
slot0 = cacheDir
slot1 = store.db 完整路径（含文件名）
slot2 = clash.socket 路径
slot3 = appToken（UUID，宿主生成）
slot4 = LegacyDatabases{filesPath, v21xPath, v23xPath}
```

---

## 2. 端点状态

### 2.1 已实现（47 个 handler / 96 个测试全绿）

| 组 | 端点 | 证据强度 |
|---|---|---|
| 状态 | `/status` `/healthcheck` `/stage` | `/status` 硬；`/stage` 推断 |
| 监听器 | `/listeners`、`PUT .../enabled`、`PATCH .../config` | **硬** |
| 代理 | `/proxies`、`/proxies/{name}` GET/PUT | 路径硬；**裸 Map + 节点名列表是推断** |
| 配置 | `/config`、`/attributes` | **无证据，返回 `{}`** |
| provider | `/providers/{proxies,rules}`(+单个 +healthcheck) | **硬**（`Providers$Type` = Proxy/Rule 支撑复用包装体） |
| profiles | `GET` / `DELETE` / `PUT`（=重下发） | **硬**（`PUT` 无 `$body`） |
| profiles/config | GET/PUT，`preferences` 表，key `profiles_config` | **key 名未确认** |
| sessions | 8 个 | 4 个路径硬，其余推断 |
| executor | GET / PATCH（切换即重载） | **硬**（`ViewModel$select$1`） |
| general | GET/PATCH（mode 真生效，小写） | **硬**（唯一带名字数组的枚举） |
| network | 5 资源 × GET/PATCH（写快照，重启生效） | 硬；`hosts` 形状推断 |
| 流 | `/traffic` `/bandwidth`（WebSocket） | **硬**（`Transmit$Bytes` 序列化器） |
| 计数 | `/connections/count` | 硬 |

### 2.2 未实现（8 个 501 桩，响应体自带 `todo` 说明）

| 端点 | 原因 |
|---|---|
| `/events` | `Payload.Source` 枚举值无证据（`<clinit>` 无 const-string，静态字段被 R8 删）。**做错会让事件页崩溃** |
| `/config/update` | 语义未明 |
| `/store` | 语义未明 |
| `/api/tokens` | token 可能经 method 2 下发，该端点或许是"重新生成" |
| `/shizuku`、`/shizuku/request` | 仅 Shizuku 模式有意义 |
| `PUT /providers/proxies/{name}` | 触发更新语义未读全 |
| `PUT /providers/rules/{name}` | 同上 |

---

## 3. 真机验证清单（按"一次能解多少"排序）

1. **`/config`、`/attributes` 的 logcat 异常** —— 一次抓包即可拿到字段名，比猜十次准
2. **`/proxies` 是否裸 Map**、sealed 判别符是否 `"type"` —— 不匹配会立刻抛异常
3. **`/listeners` 的 8 个 key** 是否与 Kotlin 枚举同名
4. **`/events`**：先用 501 兜住，看 Ktor 异常文本，它会打印 `<value> is not among valid ...` 或 `Field 'source' is required`
5. **切换订阅**后 `/status.profileId` 是否变、tunnel 是否重载
6. **`/providers/*` 的 `proxies`** 是名字还是对象（现按名字实现）
7. **`/general` 的 mode 小写**是否被接受
8. `/traffic` 帧里 `direct*/proxy*` 恒 0 是否让图表异常

诊断命令：

```bash
adb logcat -s "Clash" "*:E" | grep -i clash
# 或看内核自己的日志文件（需要 root 或 debuggable，当前 release 包不可读）
# /data/user/0/com.github.kr328.clash/cache/revived-core.log
```

---

## 4. 已知风险（按严重度）

| # | 风险 | 症状 | 处置 |
|---|---|---|---|
| 1 | **MMDB 下载阻塞启动 90s** | 打开 app 一直转圈、不报错 | 配置带 `dns.fallback` + 无网 + home 无缓存。新装用户会踩。可预先放置 MMDB 文件 |
| 2 | **sing-tun 可能关掉宿主 fd** | Stop 后 fd 悬空 | 与 platform 的"只借不关"约定冲突，待真机验证 |
| 3 | **provider 不喂缓存 → 依赖环** | 切换订阅后长时间无节点 | 远程 provider 需代理才能下载。**先补喂缓存，不要调 mihomo 超时** |
| 4 | `updatedAt` 是"最后观察到的变化时刻" | 不是内核真值 | `0` 渲染为 `0001-01-01` 表示未知 |
| 5 | mihomo 内置出站已过滤 | `/proxies` 里看不到 COMPATIBLE/PASS 等 | engine 内部保留，规则仍可用 |
| 6 | `/network` PATCH 只写快照 | 重启才生效 | 有意为之：立刻重建 tunnel 会断网 |
| 7 | `proxy_selections` 只在有激活 profile 时落库 | 未切换过则选择不持久 | 零值 profileID 时跳过并 warn |

---

## 5. 工具与方法（可复用）

### 逆向工具（纯 Python，无需 java）

| 文件 | 用途 |
|---|---|
| `axml.py` | AndroidManifest 二进制 XML 解析 |
| `dexdump.py` | dex 解析：类/方法/字段/原生方法 |
| `dalvik.py` / `tools/dalvik2.py` | Dalvik 反汇编（后者修正了 35c 的 A/G 位序与 22x） |
| `dis.py` | 带符号解析的反汇编驱动 |
| `elfsym.py` | ELF 动态符号表 |
| `arm64.py` | AArch64 部分反汇编（BL/ADRP+ADD/MOVZ） |
| `api_scan.py` | 从 dex 提取 REST 端点 |
| `gen_model.py` | 从 `api_dataclasses.json` 生成 Go 模型 |

### 打包与验证

| 文件 | 用途 |
|---|---|
| `tools/resign.py` | 重打包 + v2 签名（纯 Python + openssl） |
| `tools/verify_apk.py` | 独立 v2 验证器（只读 APK 内证书，不依赖私钥） |
| `check_elf.py` | 产物形态自查（PIE / linker64 / 16KB 对齐） |
| `bridge_test.py` | 端到端握手测试（复现 `nativeStart`） |
| `build.sh` | 受限构建：绑 cpu0,1 + nice 19 + `-p 2` + 禁用超大核 |

### 三条重要纪律（踩过坑总结）

1. **每次出包必须重编 + `sha256` 比对包内内核与源内核。**
   曾出现包内是过期内核、但大小/CRC/verifier 全绿的情况，差异只在 `.note.go.buildid`。

2. **不要看字节数判断新旧。**
   多轮出现过"大小完全相同、sha 不同"的产物（链接器分节对齐）。`sha256` 或"grep 新代码字符串 PRESENT / 旧桩文案 MISSING"才可靠。

3. **改契约文档必须回填实测差异。**
   `STORE_SCHEMA.md` 里 `TIMESTAMP` 经 modernc driver 变 `time.Time`、UUID 有 16 字节 BLOB 形态，这两条静态分析看不出来。

---

## 6. 关键文件

```
work/
├── ARCHITECTURE.md      架构逆向报告
├── API_CONTRACT.md      接口契约（35 组端点、143 个数据类）
├── STORE_SCHEMA.md      SQLite 五张表 DDL + API 模型映射
├── PROGRESS.md          逐阶段进展与决策记录
├── HANDOFF.md           本文件
├── core-new/            新内核源码
│   ├── main.go transport.go flatbuf.go core.go api.go
│   └── internal/{engine,endpoint,platform,model}/
├── tools/
│   ├── resign.py verify_apk.py apkv2.py
│   ├── keys/            自签 RSA 2048（CN=Clash Revived (unofficial build)）
│   └── dist/            产出 APK
└── apk/                 解包的原始 APK
```

---

## 7. 产出物

```
APK      work/tools/dist/cfa-revived-3.0.3.apk   20.51 MB
内核     sha256 116717e766b489faf0233266ef7a1352b2cd8b470a97fddc22405244633fae32
签名     v2（自签 RSA 2048，CN=Clash Revived (unofficial build)）
ELF      e_type=3 / PT_INTERP=/system/bin/linker64 / p_align 全 0x10000
测试     96 个（engine 45 + endpoint 51）全绿
```

**安装前必读**：签名与原版不同，**必须卸载原版**。原版配置会丢失，请先备份或导出订阅。若要共存需改包名并重编 `resources.arsc`（未做）。
