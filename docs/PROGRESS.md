# 复活进度（第 2 阶段完成：通道握手打通）

## 本阶段解决的问题

**内核进程如何与宿主建立通道。** 这是整个方案能否成立的最后一颗钉子，已用 ARM64 反汇编解开。

### 证据链（全部来自二进制，无推测）

`libcompat.so` 的 `Java_com_github_kr328_clash_core_AndroidProcess_nativeStart`（0x1570，1112 字节）：

```
015bc  access(path, R_OK|X_OK)                        ; 先确认内核可执行
015f8  socketpair(AF_UNIX, SOCK_SEQPACKET, 0, sv)     ; x1=5 -> SOCK_SEQPACKET
01610  fcntl(sv[0], F_GETFL, 0)
01620  fcntl(sv[0], F_SETFL, O_NONBLOCK)
01634  fcntl(sv[1], F_GETFL, 0)
01644  fcntl(sv[1], F_SETFL, O_NONBLOCK)
01668  fork()
       ├─ 子进程
017cc  dup2(devnull, 0) / dup2(devnull, 1) / dup2(devnull, 2)
01828  opendir("/proc/self/fd")  → 遍历
01878  close(fd)                 ; 保留 0,1,2 与 dirfd、channel fd
01884  closedir
018bc  strdup(path)
018cc  malloc(argv/envp)
0193c  sub_19c8(...)             ; 内部函数，见下
01954  execve(path, argv, envp)
```

`sub_19c8`（0x19c8）：

```
01a28  ADRP x2, 0x1000
01a2c  ADD  x2, x2, #0x60        ; -> 0x1060
01a34  MOV  x1, #32
01a3c  BL   vsnprintf            ; 0x25a0
```

`.rodata[0x1060] = "CHANNEL=%d"`。

> **结论：fd 不走 argv，而是通过环境变量 `CHANNEL=<fd号>` 交接。**
> 通道类型是 `SOCK_SEQPACKET`，所以每次 `sendmsg/recvmsg` 恰好承载一条完整消息，无需额外长度前缀即可分段。

### 一个反直觉的点

子进程会遍历 `/proc/self/fd` 关闭几乎全部 fd——**socketpair 本身也不例外**。只是那个用作通道的 fd 被显式排除在关闭集合外，其余（含父进程侧、inflight 的文件描述符）全被清掉。这是刻意的沙箱化。

---

## 端到端验证（真实执行，非纸面）

`bridge_test.py` 复现 `nativeStart`：`socketpair(SEQPACKET)` → `exec` + `CHANNEL=<fd>` → 按 20 字节帧协议通信。

```
spawning ./core-linux with CHANNEL=4
  <- Done callId=1 method=1 len=0
  <- Done callId=2 method=0 len=0
  <- Done callId=3 method=99 body=b''
RESULT: PASS
```

内核侧日志：

```
msg="bridge attached via CHANNEL" fd=4
msg="core started"
msg="api listening" addr=127.0.0.1:43221
msg=initialize slots=5
     strings="[/data/user/0/pkg/files /data/user/0/pkg/cache secret-token zh-CN]"
     legacy="{FilesPath:/data/user/0/pkg/files V21xPath:.../21x V23xPath:.../23x}"
```

四个 String 参数与 `LegacyDatabases` 三个字段**全部正确解出**，说明 FlatBuffers 读取器与逆向出的 schema 吻合。

产物一致性：

| | 原版 | 新版 |
|---|---|---|
| e_type | 3 (DYN/PIE) | **3 (DYN/PIE)** |
| INTERP | `/system/bin/linker64` | **`/system/bin/linker64`** |
| LOAD p_align | 0x1000（4KB） | **0x10000（64KB）** |
| 体积 | 28MB | 5.77MB |

---

## 已完全确定的事实

| 项 | 内容 |
|---|---|
| 帧格式 | 20 字节大端：`int32 type, int64 callId, int32 method, int32 length` + 载荷 |
| 消息类型 | `Call=0 Done=1 Error=2 Cancel=3` |
| method 路由 | `0 = initialize`（FlatBuffers），`1 = start`（空载荷） |
| initialize 载荷 | 根表 5 槽：4×string + 1 子表；子表 3 槽：`filesPath/v21xPath/v23xPath` |
| 通道交接 | 环境变量 `CHANNEL=<fd>`，`SOCK_SEQPACKET`，全大端 |
| 数据面 | JSON over 本地 HTTP，35 组端点，143 个数据类 |

---

## 仍未完成

1. **method 2+ 的语义**：`ShizukuService$coreJob` 里还有 `Bridge.call` 调用点，需精确读 35c 寄存器确认常量（当前骨架对未知方法回 `Done`，可容忍但不够准）。
2. **token 机制**：Go 侧有 `AppToken`/`EncodeToken`/`InstallAppToken`/`RawToken`，对应 `/api/tokens`，需还原生成与校验。
3. **35 组端点实现**：接 mihomo（其 `executor`/`profiles`/`providers`/`network`/`listeners` 与原内核模块一一对应）。
4. **Platform 反向调用**：16 项（`CreateVpn` 取 tun fd、`ProtectSocket` 防回环、`ReportState` 上报等），方向为 core→host 的 Call。
5. **重打包**：需换签名；建议改包名以与原版共存。

## 文件

```
work/core-new/
├── main.go        入口：读 CHANNEL，attach 或 standalone
├── transport.go   20 字节帧 + SCM_RIGHTS（含 nativeStart 行为注释）
├── flatbuf.go     最小 FlatBuffers 读取器 + table 探测
├── core.go        method 分发、initialize 解码、生命周期
├── api.go         HTTP 骨架（/status、/healthcheck、/stage 已实现）
├── libclash.so    ★ android/arm64 产物 5.77MB，16KB 对齐
└── core-linux     linux/arm64 验证产物

work/bridge_test.py  端到端握手测试（复现 nativeStart）
work/arm64.py        AArch64 部分反汇编器（BL/ADRP+ADD/MOVZ 解码）
```

---

# 第 3 阶段：并行开发（团队）

## 工程量评估

协议层已无未知，剩下的是实现量。单人估算 3–4 周：

| 模块 | 工作量 | 风险 |
|---|---|---|
| mihomo 集成 | 3–5 天 | 中 |
| 35 组端点适配 | 5–8 天 | 中 |
| Platform 16 项（含 TUN fd） | 3–5 天 | **高**（关键路径） |
| method 2+ / token | 1–2 天 | 低 |
| 重打包 + v2 重签名 | 1–2 天 | 中 |
| 真机联调 | 3–5 天 | 高 |

已验证的关键前提：**mihomo v1.19.33 可以 `CGO_ENABLED=0` 交叉编译到 android/arm64**（产物 62MB），不需要 NDK。这消除了最大的一条不确定性。

## 分工（写入范围互斥）

| 任务 | owner | 写入范围 |
|---|---|---|
| T1 数据模型 | Lead | `internal/model/` |
| T2 mihomo + 端点 | engine-dev | `internal/engine/`, `internal/endpoint/` |
| T3 Platform + TUN | platform-dev | `internal/platform/`, `PLATFORM_RPC.md` |
| T4 method2+/token/打包 | reverse-dev | `tools/`, `PROTOCOL_REMAINING.md` |
| T5 联调 | Lead | — |

规范见 `DEV_SPEC.md`。

## T1 完成情况（Lead）

- `gen_model.py` 从 `api_dataclasses.json` 生成 56 个结构体 + 全部枚举
- `api_enums.json` 从 dex 静态字段提取出 11 个枚举的取值，例如：
  - `Proxies$Type`: `Direct` / `Selector`
  - `General$Config$Mode`: `Direct` / `Global` / `Rule` / `Script`
  - `Listeners$Type`: `DNS` / `ExternalController` / `HTTPSocks5` / `Redirect` / `SystemProxy` / `TUN` / `TransProxy` / `VPN`
  - `AccessControlConfig$Mode`: `AllowAll` / `AllowPackages` / `DisallowPackages`
  - `VPN$RouteMode`: `AsDefault` / `ExcludesPrivate` / `None`
- `internal/model/model_test.go` 钉住 JSON 键名与枚举字面量，4 项全 PASS
- `check_elf.py` 产物自查工具：PASS 新内核 / FAIL 原内核（4KB 对齐，符合预期）

---

# 第 4 阶段：APK 可重打包（签名链路打通）

## 交付

`tools/resign.py` 可以把新内核塞进原 APK 并重新 v2 签名，全流程无 java。

```
python3 tools/resign.py cfa.apk core-new/libclash.so tools/dist/cfa-revived-3.0.3.apk
  154 条目，删 3 个非 arm64 ABI，替换 lib/arm64-v8a/libclash.so
  4 字节对齐重建 zip → 算 v2 内容摘要 → openssl 签名 → 写 signing block
```

产物：`tools/dist/cfa-revived-3.0.3.apk`，4.27MB（原 52MB，因为只保留 arm64 且内核从 28MB 降到 5.7MB）。

## 验证（不只是自验）

| 检查 | 方法 | 结果 |
|---|---|---|
| v2 内容摘要算法 | 用 verifier 验 **原版 cfa.apk**（apksigner 产出） | PASS，digest 完全一致 |
| 签名结构 | 独立解析 signing block（不复用实现） | signer = signed_data 1031 + sigs 268 + pubkey 294 |
| RSA 签名 | 纯 python + `openssl dgst -verify` 双路 | 两路都 PASS |
| manifest 完整性 | 与原版逐字节比对 | 11660 = 11660，identical |
| resources.arsc | 逐字节比对 | 340788 = 340788，identical |
| zip 结构 | `zipfile.testzip()` + `unzip -t` | 154 条目全部 OK |

**关键论证**：verifier 能验通 Google apksigner 的原版产物，说明摘要算法与 RSA/PKCS#1 处理不是自洽，而是被第三方真实产物证实过。

## 本轮纠正的两个错误

1. **v2 signer 结构**（我的错）：我写成了 `signed data + minSdk + maxSdk + signatures + public key`。**minSdk/maxSdk 是 v3 的字段**。v2 是 `signed_data | signatures_seq | public_key`。reverse-dev 用原版 APK 的偏移反推出 268/264/259/256 四个值才定位到错位。
2. **原版没有 v3**（reverse-dev 的误判）：原版三个 pair 是 `v2` + `SOURCE_STAMP(0x504b4453)` + `VERITY_PADDING(0x42726577)`，**不含 `v3(0xf05368c0)`**。所以丢掉的不是 v3，而是两个可选项，侧载无影响。

## 已知缺口（诚实记录）

1. **未在真机上安装过**。这是唯一未消除的风险，所有证据都是结构性的。
2. 只有 v2 签名，无 source stamp / verity padding（均可选）。
3. `.so` 保持 deflate 压缩，依赖 manifest 的 `extractNativeLibs=true` 让系统安装时解压到可执行目录。这条对我们是必需的（内核要被 execve）。
4. `--store-so`（STORED + 16KiB 对齐）路径存在但**未实测**，不要用。

---

# 第 5 阶段：initialize 槽位语义定案

## 问题

reverse-dev 在 §2 报了一个真矛盾：
- Go 侧 `InitializeRequest` 访问器实测 `DatabasePath=off4 / ListenPath=off6 / AppToken=off8 / LegacyDatabases=off12`
- 而 Kotlin 侧传值顺序是 `(cacheDir, store.db, clash.socket, UUID)`
- 按名字对齐 ⇒ `AppToken` 拿到 socket 路径，`DatabasePath` 拿到 cacheDir，自相矛盾

## 定案过程

反汇编 `RunKt$runCore$2$1$1`（initialize 调用点）拿到四个 String 的真实来源：

```
157: getCacheDir().getAbsolutePath()                      -> cacheDir
167: getDatabasePath("store.db").getAbsolutePath()        -> <db>/store.db
179: const-string "clash.socket"
181: getFilesDir().resolve("clash.socket").getAbsolutePath() -> <files>/clash.socket
189: new LegacyDatabases(filesDir, getDatabasePath(?), getDatabasePath("profiles"))
```

而 `CoreAPI.initialize` 外层是 `FlatBufferBuilder.startTable(5)`，即 5 槽 = 4 string + 1 子表。

**结论：槽位是 `slot0=cacheDir, slot1=store.db, slot2=clash.socket, slot3=token(UUID), slot4=LegacyDatabases`。**

reverse-dev 读到的 `off4/off6/off8/off12` 缺了 `off10` —— 那个缺失项正是 slot3（AppToken）。它的整张字段名表因此**偏移了一位**：`off4` 不是 DatabasePath 而是 CacheDir，真正的 DatabasePath 在 `off6`。矛盾由此消解。

## 实证

改 `decodeInitialize` 按槽位赋值后，用真实 UUID 跑 `bridge_test.py`：

```
initialize slots=5
  cacheDir      =/data/user/0/com.github.kr328.clash/cache
  databasePath  =/data/user/0/com.github.kr328.clash/databases/store.db
  listenPath    =/data/user/0/com.github.kr328.clash/files/clash.socket
  appTokenLen   =36
  appTokenIsUUID=true          <- looksLikeUUID() 校验 8-4-4-4-12 格式
  legacy        ={FilesPath:.../files V21xPath:.../databases/profiles V23xPath:.../databases/profiles}
  extra         =[]
RESULT: PASS
```

四个槽 + 子表全部落在正确语义上，`extra` 为空说明没有未消费的字段。

## 代码变化

- `core.go`：`InitRequest` 从 `Strings []string` 改为具名字段 `CacheDir/DatabasePath/ListenPath/AppToken/Legacy/Extra`；`decodeInitialize` 按槽位赋值；新增 `looksLikeUUID` 用于联调时一眼判定 token 槽；日志打印 `appTokenLen` 与 `appTokenIsUUID` 而非明文 token。

## 为什么这个结论重要

token 是 HTTP 认证（`Authorization: Bearer <token>`）的全部依据。如果槽位错一位，内核会把 socket 路径当 token，宿主随后用真 token 调 API 会全部 401 —— 表现为"UI 什么都加载不出来"，且极难定位。这条属于必须在上真机前钉死的东西。

---

# 第 6 阶段：Platform 接线完成（T3 收口）

## 接线方案（选 B：单一读者）

`core.Serve` 不再自己 `ReadMessage`，改为由 `platform.Client.Serve` 独占读循环：

```
platform.Client.Serve(onCall)
  ├─ 帧 type != Call  → Handle() 配对到 pending 的反向调用
  └─ 帧 type == Call  → onCall → core.dispatch（host→core 的 method 0/1/2）
```

**两个读者必然抢帧**，这是选 B 而非 A 的原因。

## 三处实现

1. `platformTransport`（core.go，12 行）：`Message ⇄ Frame` 双向映射，FD 透传。
2. 未消费 fd 防泄漏：`onCall` 返回后若 `m.FD >= 0` 则 `syscall.Close` 并 warn。host→core 的 Call 本不该带 fd，若真带了说明协议理解有误或版本差异，warn 本身是有用信号。
3. `NextCallID` 去重：删除 `main` 包的同名分配器，统一走 `platform.NextCallID`，经 `WithCallIDAlloc` 注入。两个包各持一个从 `1<<40` 起步的序列会撞号。

## 验证

```
ok  internal/endpoint   0.213s
ok  internal/engine     0.841s
ok  internal/model      0.023s
ok  internal/platform   0.414s
端到端握手：RESULT: PASS
```

## 仍未打通：tun fd 的消费端

`CreateVpn` 已能发出并收 fd，但**没人消费它**——因为 `engine.Start(cfgBytes)` 不接受 tun fd。需要 engine-dev 改签名，然后 core.handleStart 取 fd 交给 engine。

---

# 第 7 阶段：P0 全链路打通，首个可装包产出

## 内核侧

`main` + `core` + `engine` + `endpoint` + `platform` + `model` 六个包全部编译通过，49.69MB（含 mihomo v1.19.33）。

真实 mihomo 集成验证（engine-dev 提供）：
```
/status -> {"error":null,"profileId":"000…","type":"Loaded"}   ← 真的熔进来了
/proxies -> {"COMPATIBLE":{…},"DIRECT":{…},"GLOBAL":{…}}
```
`ParseWithBytes → ApplyConfig → tunnel.OnRunning` 全程无 panic，`Stop` 干净。

## 端点冒烟（standalone）

```
GET /healthcheck   -> {"ok":true}                        [200]
GET /status        -> {"type":"Idle"}                    [200]
GET /listeners     -> {"listeners":{8 类型…}}             [200]
GET /proxies       -> {}                                 [200]
GET /config        -> {}                                 [200]
GET /attributes    -> {}                                 [200]
PUT /listeners/VPN/enabled -> {}                         [200]
PUT /listeners/Nope/enabled -> {"error":"unknown…"}      [400]
GET /providers/proxies -> 501 + todo 说明
```

## 阿喀琉斯之踵：tun fd 的消费端

`core.handleStart` 现在：**先回 Done → 异步 `bootEngine`**。

这个顺序是硬约束：读循环由 `platform.Client.Serve` 独占，若在 `dispatch` 里同步等 `CreateVpn` 的响应，那条响应永远进不来 —— 死锁。

`bootEngine` 里按 `--tun` 开关决定是否向宿主要 tun（默认关，因为 P0 无 profile，且自动要会让不响应 method 7 的对端等满 90s 超时）。

## 打包

```
python3 tools/resign.py cfa.apk core-new/libclash.so tools/dist/cfa-revived-3.0.3.apk
  -> 19MB，v2 签名全 PASS
```

**踩过的坑**：第一次打包时用的 `libclash.so` 早于最后一次编译，包内核与源码的 Go build ID 不一致（sha256 不同但大小相同，差异在 `.note.go.buildid`）。

**新增纪律**：每次出包前必须重新编译 + `sha256` 对比包内内核与源内核。等确认：`14963d105d608c1c…` 两者一致。

## 产品决策（Lead 拍板）

| 问题 | 决定 | 理由 |
|---|---|---|
| mihomo 多出的 `COMPATIBLE/PASS/PASS-RULE/REJECT-DROP` 四个内置出站 | **过滤 /proxies 输出**，engine 内部保留 | UI 照原版布局设计，多出"节点"会让用户以为配置错了；但规则若引用它们功能必须照常 |
| `/config` `/attributes` 无证据的响应体 | 返回 `{}` | 让 kotlinx 抛 `MissingFieldException` 报出缺哪个字段，比编造字段收敛快 |
| P1 顺序 | providers → sessions → profiles | `/profiles` 依赖 store 的 SQLite 表结构，目前只见表名不见列定义 |

## 待联调闭环（按风险）

1. `/proxies` 裸 Map + sealed 判别符 `"type"`（高置信推断，非硬证据）
2. TUN 状态语义映射：`boosted←GSO`、`routeInserted←auto-route`、`dnsHijacked←len(dns-hijack)>0`
3. sing-tun 在 `Stop()` 时是否私自关闭宿主 fd（无法本机验证）
4. method 10 `QueryConnectionOwner` 两侧证据冲突（Go 侧 5 槽 vs Kotlin 侧 CoreState 形状）

---

# 第 8 阶段：P1a providers + store 契约挖掘

## P1a 交付

`/providers/proxies`、`/providers/proxies/{name}`、`/providers/proxies/{name}/healthcheck`、`/providers/rules`、`/providers/rules/{name}` 全部可用（原 5 个 501 桩替换）。

真实 mihomo（`type: inline`，不碰磁盘网络）实测：
```
/providers/proxies -> {"providers":{"inline-prov":{"name":"inline-prov","proxies":["n1"],"type":"Proxy","vehicleType":"File"}}}
/providers/rules   -> {"providers":{"rules-a":{...,"type":"Rule"}}}
/proxies           -> 只剩 GLOBAL/DIRECT/REJECT/n1（四个内置出站已过滤）
```

产物 sha256 `158f1fc8…` **与我独立重编的结果逐字节一致**，证明纯源码可复现。

## sessions 的存储形态（定案 A）

在 Go 类型表里找到：
```
*map[uuid.UUID]*sessions.Session
```
session 存在**以 UUID 为 key 的内存 map**；store（SQLite）只有 `ProfileTable`/`ProvidersTable`/`ProxySelection`/`UserInfo`，**无 SessionTable**。

⇒ P1b 做纯运行时会话（A），不等 store 契约，不会返工。

## store 完整 DDL（P1c 契约，已挖出）

详见 [STORE_SCHEMA.md](work/STORE_SCHEMA.md)。五张表：`preferences` / `profiles` / `user_infos` / `providers` / `proxy_selections`，含外键 CASCADE。

关键陷阱：
- `upload`/`download`（API）↔ `uploaded`/`downloaded`（SQLite），差一个 `ed`，取不到值也不报错
- `profiles.content` BLOB 才是 mihomo 的真实输入；`version`/`modeOverride` 不暴露给 API
- `providers.updatedAt` 是真值 —— 修正了上一轮「版本号变了就刷 now」的近似做法
- `proxy_selections` 必须承接 `PUT /proxies/{name}` 的持久化，否则重启丢选择
- SQLite driver 只能用纯 Go 的 `modernc.org/sqlite`（`CGO_ENABLED=0` 是硬约束）

---

# 第 9 阶段：P1b sessions 完成

## 端点

| 端点 | 证据 |
|---|---|
| `POST /profiles/sessions` | 硬（const-string + CreateSessionSpec） |
| `DELETE /profiles/sessions/{id}` | 硬 |
| `GET /profiles/sessions/{id}/attributes` | 硬 |
| `GET /profiles/sessions/{id}/providers` | 硬 |
| `GET/PUT .../config` | 推断（lambda 类存在，方法体被 R8 内联） |
| `GET /profiles/sessions/{id}`（StoreStatus） | **engine-dev 自加**，推断（UI 轮询进度需要） |

实测（真实 mihomo 解析 inline provider）：
```
POST  -> {"sessionId":"2466b0fe-…"}
GET /{id}          -> {"error":"","providerProgress":{"completed":2,"total":2},"stage":"Completed"}
GET /{id}/providers -> {"providers":[{"name":"inline-prov","type":"Proxy","vehicleType":"File","length":1}, …]}
```

## 实现要点

- 存储抽成 `sessionStore` 接口（Put/Get/Delete/Range），当前是 `memorySessionStore`，换落盘实现不动 handler
- `sessionStageFloor = 500ms` 让 `Verifying` 至少可观测一次（体验取舍，非协议要求）
- `length` 必须等 `Provider.Initial()` 之后回填；单个 provider 30s 超时保护，不让一个 provider 拖垮整个会话
- 快照返回值拷贝（含 slice 深拷贝），避免 handler 拿到正在被改写的对象

## engine-dev 自查出的 bug（值得记录）

`updatedAt` 零值格式化不一致：同一 provider 在列表显示 `1970-01-01`、在单个显示 `0001-01-01`。
根因：`time.Time{}` 的 `UnixMilli()` 是 `-62135596800000`，格式化得 `0001-01-01`；而 `touched()` 首次返回 `0`（epoch）。
已修：统一走 `instantOrZero()`（0 → `0001-01-01`），加"连读两次都必须是 0"的断言。

**这类 bug 的特征**：每个入口单看都合理，横向对比才见不一致。

## 产物

sha256 `543ef249e1500604c6b44141b13f2b7bf67ab6930fa2b50ade098a024baf555c`，与 engine-dev 报的逐字节一致（我独立重编验证）。

APK 18.96MB，v2 签名 PASS，包内核与源内核 sha256 一致。

## DatabasePath 交接（P1c 前置）

`InitRequest` 已含 `DatabasePath`（slot1 = `getDatabasePath("store.db").getAbsolutePath()`，**完整文件路径含文件名**，实测值 `/data/user/0/<pkg>/databases/store.db`）。

注意与 `FilesPath`（`/data/user/0/<pkg>/files`，mihomo home）是**两个不同目录**。

若按 `FilesPath + "/store.db"` 猜会得到不存在的路径 —— SQLite 会**静默创建一个空库**，`/profiles` 返回空列表，表现得像"用户没有配置"。比报错更糟。

待办：engine 加 `SetDatabasePath(path)`，core 在 `handleInitialize` 调用。

---

# 第 10 阶段：P1c 写入 + 端点覆盖 22/35

## 端点进度

| 阶段 | 端点 | 证据 |
|---|---|---|
| P0 | `/status` `/listeners`(+enabled/config) `/proxies`(+单个/切换) `/config` `/attributes` `/healthcheck` `/stage` | 真实 mihomo |
| P1a | `/providers/{proxies,rules}`(+单个/healthcheck) | inline provider 实测 |
| P1b | `/profiles/sessions` 全 8 个 | 异步 stage 可观测 |
| P1c | `GET /profiles`、`DELETE /profiles/{id}`、`PUT /profiles/{id}`、`proxy_selections` 落库 | 合成库端到端 |

剩余 13 组：`/profiles/config`、`/executor/*`、`/network/*`、`/general`、`/connections/count`、`/traffic` `/bandwidth` `/events`、`/store` `/api/tokens` `/shizuku`。

## `PUT /profiles/{uuid}` 的真实语义（重要发现）

`Profiles$update$$inlined$request$default$1` 的字段是 `$builder/$method/$path/$this$0`，**没有 `$body`**；对比 `patchConfig` 系列都有 `$body`。

⇒ **`PUT /profiles/{uuid}` 是「触发内核重新下载 remoteURL」，不是「上传配置内容」。**

风险等级因此反转：原以为 PUT 最危险（可能写坏用户配置），实际内容由内核控制，只动 `content/version/updatedAt`。真正要小心的是 `DELETE`——级联靠 SQLite 外键，而 `foreign_keys` 未生效时**不报错只留孤儿行**。

## `/executor/config` 的切换语义（硬证据）

反汇编 `app/main/profiles/ViewModel$select$1`：
```
46: const-string "Profiles" / "select"
62: new Executor$Config(UUID)          ← 只带 activeProfile
71: const-string "/executor/config"
83: Executor$patchConfig...
```
**UI 切换订阅唯一的动作就是 `PATCH /executor/config`，没有第二步。**

⇒ 「只记录、下次启动生效」不成立。必须**切换即重载**，且要原子性（先解析成功再停旧 tunnel）、失败可见（非 2xx + error）、失败后 `Status.profileId` 保持旧值。

## 级联删除测试

断言三张子表的 `COUNT(*)`，而非返回值：
```
删 A 后: profiles=1（B 存活）, user_infos=0, providers=0, proxy_selections=0
删不存在的 UUID → 404，且数据原封不动（无半截事务）
未配 store 路径 → 503
```

## preferences 的 key 名

未从二进制确认（DDL 附近 600 字节只有五条 CREATE TABLE，无 key 常量）。

**但不阻塞**：内核已被我们替换，用户不会再跑原版。key 只要自洽即可，代价仅是「原版里改过的更新设置不继承」，属迁移问题非正确性问题。

采用 `profiles_config`，读不到时返回默认值（`Profiles$Config` 有必填字段，返回 `{}` 会让 kotlinx 抛异常）。

## 产物

```
sha256  514631b981b68aa842584ed0f64c96abf6a181c0208b571c7722b9b2d08c9d52
大小    53.38 MB（P1b 基线 49.94 → +3.54 MB，预算 15MB 的 24%）
测试    65 个全绿（engine 29 + endpoint 36）
```

---

# 第 11 阶段：/profiles/config 完成，23/35 端点

## /profiles/config

key = `profiles_config`，写进 `preferences(key, value)`，value 为紧凑 JSON。

读的四个退化路径**全部回默认值，永不 500**：
| 情况 | 行为 |
|---|---|
| store 路径未配置 | 默认值 + warn |
| 无 preferences 表（老库） | 默认值 + warn |
| 无该 key（首次运行） | 默认值，不打 warn（正常状态） |
| 值损坏 | 默认值 + warn |

返回完整对象而非 `{}` —— `Profiles$Config` 字段必填，`{}` 会让 kotlinx 抛 `MissingFieldException`。

写：无表时自动 `CREATE TABLE IF NOT EXISTS`（幂等）；upsert；未配路径 → 503；坏 JSON → 400。

顺带把 `GET /profiles/sessions/{id}/config` 改成复用同一 DTO builder，消灭两处重复字段映射（那正是 `NotifyOnSuccess/Failure` 容易写反的地方）。

## sha256 纪律暴露出一个流程问题

engine-dev 报 `678ecc7b…`，我重编得 `8fcf833d…`，**大小完全相同（55968065）**。

查明原因：`handlers_executor.go` / `executor.go` 的时间戳是 00:28 / 00:29 —— 它报完 sha256 后立刻开始写 `/executor/config`，我验证时工作区已含 executor 代码。

**不是它报假数据，是我验晚了。** 但据此定了一条流程：报 sha256 时要么暂停改代码，要么明确写「报完继续改」——否则 sha256 退化成无意义的历史记录，会被误判为产物有问题。

**决策：本轮不打新包**，继续用已验证的 `514631b9`（P1c 写入完成版），不拿未定型的 executor 代码覆盖已验证产物。

## external-controller 有两个真相源（需警惕）

```
Executor$Config                                { activeProfile, externalController: String, externalControllerSecret: String }
Listeners$Listener$Config$ExternalController   { listenAt: String, secret: String }
```

`Executor.Config` 是扁平 String 对，`Listeners.*` 是带 `listenAt`+`secret` 的对象。

判断：前者是派生值/快照，真正监听控制在 `/listeners/ExternalController/*`。

要求 `/executor/config` 的 GET 从 listener 状态派生，PATCH 转去设 listener——**不要存一份并行状态**，否则会出现"UI 显示 controller 在监听，实际没有"。

## providers 缓存：先不喂，但记录一个死锁风险

触发条件：*profile 的 provider 是远程 URL，而节点本身需要代理才能访问* → 不喂缓存 ⇒ mihomo 去下载 provider ⇒ tunnel 尚未起来 ⇒ 下载失败 ⇒ **依赖环**。

P1 先不喂（喂错格式会引入更直接的失败），但该失败模式必须记录。一旦联调出现「切换订阅后长时间无节点 / provider 下载失败」，第一件事是补喂缓存，**不是去调 mihomo 超时参数**。

实现提示：`providers.content BLOB` 大概率是要写到 mihomo home 目录下的 providers 缓存文件（mihomo 是文件缓存），不存在"直接灌内存"的 API。

---

# 第 12 阶段：/executor/config 完成，25/35 端点

## 切换 profile 的最终序列

```
1. 读 content          （无行→404 / 无内容→404，tunnel 不动）
2. ParseWithBytes      （失败→400，tunnel 不动）
3. precheckStart       （tun 无 fd→400，tunnel 不动）
4. Stop → SetProfileID → Start
5. Start 失败 → 回滚 ProfileID
6. 成功 → 200 {}，ProfileID 有真值
```

关键：**第 2、3 步都在任何破坏性动作之前**。三条测试分别钉住「坏配置不停旧 tunnel」「缺 tun fd 不停旧 tunnel」「失败后 ProfileID 不变且 Running 仍 true」。

## external-controller 单一真相源

PATCH 显式忽略 `externalController`/`externalControllerSecret`，只带这两字段的请求返回 400 + `"nothing to patch: ... managed by /listeners/ExternalController"`。GET 与 `/listeners/ExternalController` 同源（读同一份 `ctrlAddr/ctrlSecret`），两者不可能打架。

## 冻结验证流程首次实测成功

engine-dev 报完 sha256 后停止改代码。我一次验证即对上：
```
fd38cab9f3d123aa4a8f6f24caa52be242da4a35d4536fcce1d1e0adc7f3c00f
```
上一轮因报完即改导致比对了三条命令才排除误导，本轮零成本。**流程成立。**

## P2 配置面的分工决策

| 端点 | PATCH 行为 | 理由 |
|---|---|---|
| `/general {mode}` | **立刻 `engine.SetMode()` 真生效** | mihomo 切 mode 轻量，不需重建 tunnel；且 mode 是用户最常改的 |
| `/network/{interface,resolver,fakeip,hosts,outbound}` | 写内存快照 + 标"下次 Start 生效" | 立刻重建 tunnel 会断网，体验更差 |

**必须守住的约束**：`/network` PATCH 后 GET 必须返回刚写入的值。否则用户"点保存→界面回旧值→以为没存上→反复点"。

实现要点：`networkState` 内存快照是唯一真相源，GET 读它、PATCH 写它、`Start()` 时才应用到 mihomo。**不要读 mihomo 实时值来响应 GET**。

**不要发明模型里没有的字段**（如 `pending`/`dirty`）——`ignoreUnknownKeys=false` 时多一个键直接抛异常。用 slog 日志替代。

## 4 个语义未明的端点：押后

`/store`、`/api/tokens`、`/shizuku*`、`/config/update`。

- `/api/tokens`：`RunUpdate.UpdateAppToken` 存在，token 可能经 method 2（`handleSendEvent`）非 HTTP 下发；该端点或许是"重新生成 token"
- `/shizuku*`：仅 Shizuku 模式有意义
- 建议**先只返回 501 + 说明**，等真机看 UI 有没有调再决定

## 产物

```
sha256  fd38cab9f3d123aa4a8f6f24caa52be242da4a35d4536fcce1d1e0adc7f3c00f
大小    53.38 MB
APK     20.45 MB，v2 签名 PASS，包内内核逐字节一致
测试    78 个（engine 37 + endpoint 41）
端点    25 / 35
```

---

# 第 13 阶段：P2 配置面完成，31/35 端点

## /general

- `GET` 返回小写 mode（唯一带 `@SerialName` 名字数组的枚举）
- `PATCH` 立刻 `engine.SetMode()`，**真生效**（轻量操作，不重建 tunnel）
- 输入容错大写，输出永远小写
- `{"mode":"script"}` → **400**（mihomo 无 script mode）。选择明确失败而非静默降级：用户选了 script 却跑 rule 比报错更难查

## /network/*（五个资源 × GET/PATCH）

快照是**唯一真相源**：GET 读快照、PATCH 写快照、`Start()` 才应用到 mihomo。

测试 `TestNetworkInterfaceRoundTrip` 钉住：PATCH `rmnet_data0` → 立刻 GET 必须返回 `rmnet_data0`。

- 不读 mihomo 实时值（否则表现为"保存了、界面回旧值、用户反复点"）
- 未发明 `pending`/`dirty` 等模型外字段（`ignoreUnknownKeys=false` 下多一键即崩），待生效靠 `slog.Info`
- 深拷贝；空集合写快照时归一为 `[]`，响应里不含 `null`
- `/network/hosts` 形状为推断（模型无 `Network$Hosts` 类），扁平 `Map<String,String>` + TODO

## Resolver 服务器列表：TODO 已解

engine-dev 原打算留空（`dns.NameServer` 无 `String()`，怕拼错）。读了 mihomo `config/config.go:1203 parseNameServer` 后，还原规则是确定的：

```
type NameServer struct { Net, Addr, ProxyName string; Params map[string]string; PreferH3 bool }
```

| Net | Addr 形态 | 还原 |
|---|---|---|
| `""`(UDP) | host:port(53) | 直接输出 Addr |
| `tcp` | host:port(53) | `tcp://` + Addr |
| `tls` | host:port(853) | `tls://` + Addr |
| `https` | **已是完整 URL** | 直接输出 Addr（源码用 url.URL.String() 重建过） |
| `quic` | host:port(853) | `quic://` + Addr |
| `h3` | host:port | `h3://` + Addr |

后缀：`#proxyName`，params 以 `&k=v` 追加（源码 `Split(u.Fragment,"&")` + `SplitN("=",2)`：1 段=proxyName，2 段=param）。

已知限制：UDP 的 `223.5.5.5` 解析后变 `223.5.5.5:53`，还原也带端口。字面不同但语义相同，**比留空好**（留空 UI 显示"没有 DNS"）。

## /events 与 handleSendEvent 的区别（防止混淆）

| | 方向 | 载体 | 用途 |
|---|---|---|---|
| `handleSendEvent`(method 2) | kernel → host | Bridge 20 字节帧 | 内核向 Kotlin 报事件码 |
| `GET /events` | kernel → UI | HTTP WebSocket | UI 订阅状态变化 |

可能承载同一批事件，但**通道完全不同**，不要复用编码。

## 产物

```
sha256  5ed1c9a61fd967093cba69e2b9bd08793cacb2d216332e1eb7a8326c8e730064
APK     20.46 MB，v2 签名 PASS，包内逐字节一致
端点    31 / 35
测试    88（engine 41 + endpoint 47）
```

---

# 第 14 阶段：P3 流式端点，46 handler + 9 桩

## /traffic 与 /bandwidth：帧类型有硬证据

从字节码读出两条流共用的序列化器：

```
Traffic$poll$1.invokeSuspend     0x0090  sget-short Transmit$Bytes;->Companion
                                 0x0094  Transmit$Bytes$Companion->serializer
Bandwidth$poll$1.invokeSuspend   0x0090  （同一句）
```

⇒ 帧形状 = `Transmit.Bytes` 的 6 个 int64：`up/down/directUp/directDown/proxyUp/proxyDown`

- `/traffic` → `statistic.DefaultManager.Now()`（每秒增量）
- `/bandwidth` → `Total()`（累计）
- 频率 1s 对齐 mihomo 统计周期，不自造 ticker；连上立刻发首帧
- `direct*/proxy*` 恒 0 + TODO（mihomo 不按直连/代理归因）

## WebSocket 断连：5 秒 → 0.01 秒

第一版只有写循环，客户端 `Close()` 后握手等超时，**每次断开拖 5 秒**（测试 15.03s）。
加读循环（丢弃数据，只为感知关闭）后 **0.01s**。

**通用经验**：任何长连接 handler，读循环不是为了读数据，是为了感知对端关闭。

## Resolver 还原（按 parseNameServer 反推）

`https` 的 `Addr` 已是完整 URL（解析时被 `url.URL.String()` 重建），不再加前缀。
fragment：proxyName + params 挂 `#`，`&` 连接，**params key 排序后才拼**（不排则每次输出不同，UI 会以为配置在抖动）。

## 两个非代码风险（重要）

### 1. MMDB 下载导致 90 秒启动延迟
配置里写 `dns.fallback` 会让 mihomo 在**解析阶段**下载 GeoIP MMDB；没网时卡满 90 秒才失败。表现：打开 app 一直转圈，且**不报错**。

- 老用户升级：`FilesPath`（mihomo home）里大概率已有 MMDB，不一定触发
- **新装用户**：没有 home 历史，会踩到
- 症状关键词：*首次启动特别慢、第二次正常*

### 2. /events 的 source 枚举无证据
`Events$Event$Payload$Source`：静态字段只剩 `$VALUES`（枚举常量被 R8 删），`<clinit>` 无任何 const-string。

候选仅能从 sealed 子类名推：`Executor`/`Listener`/`Network`/`Profile`。**但 `source` 是必填字段，值不在 Kotlin 枚举里会让 kotlinx 抛 `SerializationException`，UI 直接崩。**

⇒ 做错的代价（界面崩溃）大于不做的代价（一条流缺失），**押后到真机**。

## 决策：本版作为待验证里程碑，收尾

剩余 9 个端点：
| 端点 | 状态 |
|---|---|
| `/connections/count` | 数据源现成，可做 |
| `/events` | source 枚举无证据 |
| `/config/update` `/store` `/api/tokens` `/shizuku*` | 语义未明 |
| 两个 provider `PUT` | 触发更新语义未读全 |

**后 7 个全部缺硬证据**。继续实现的边际收益低于做错的边际成本；且再往下需要的信息只有真机能提供。

收尾动作：
1. `/connections/count` 收口
2. 最终包 + 完整自查（manifest / 签名 / sha / 端点冒烟）
3. `HANDOFF.md`：硬证据 / 推断 / 未做 三分，以及真机验证清单
4. 停，等真机

## 产物

```
sha256  51ac7bf8351ee8b82c02eeb4a3f1daeb5cafb5064ac6f72687ed3b2546d89188
APK     20.51 MB，v2 签名 PASS，包内逐字节一致
测试    94（engine 45 + endpoint 49）
路由    46 真实 handler + 9 个 501 桩
```

---

# 第 15 阶段：真机验证 —— 架构成立，两个具体 bug 修复

## 真机第一次成功（2026-10-29）

设备：Redmi 23113RKC6C / **Android 17** / HyperOS（`build.type=user`, `is_debuggable=false`）

**UI 完整工作**：底部导航、概览页七个开关、配置页、错误弹窗全部正常渲染。

这证明整条链路成立：
```
dex(原版，未改动) → :core(原版) → fork+exec 新内核 → 20 字节帧握手 → HTTP 端点 → UI 渲染
```

**`fork+exec` 这个此前无法静态验证的根本性风险，真机确认成立。**

## Bug 1：`O_NONBLOCK` 导致内核 2ms 死亡（已修）

宿主 `nativeStart` 对 socketpair 执行 `fcntl(sv[1], F_SETFL, O_NONBLOCK)`（反汇编 0x1644）。
我们的读循环是阻塞式的 → 首次 `Recvmsg` 在宿主尚未发消息时返回 `EAGAIN` → 被当致命错误 → 关 fd → 退出。
宿主随后 `sendmsg` 得到 `EPIPE`。

本地用非阻塞 socketpair 精确复现：`exited uptime=2.3ms`。

修复（双保险）：
- `main.go`：attach 前 `syscall.SetNonblock(fd, false)`
- `transport.go`：`EAGAIN`/`EINTR` 退避重试，不当致命错误

## Bug 2：Android 根证书缺失导致 TLS 失败（已修）

现象：
```
下载资源: Get "https://kaze1.aisaka-taiga.com/…":
tls: failed to verify certificate: x509: certificate signed by unknown authority.
```

根因：Go 的 `crypto/x509` 在 Android 上只读 `/system/etc/security/cacerts` 一条固定路径。
但 **Android 14+ 系统 CA 移到 APEX**：`/apex/com.android.conscrypt/cacerts`；用户 CA 在 `/data/misc/keychain/cacerts-added`。
这台 Android 17 上该固定路径没有可用 PEM → 根池为空 → 任何 https 都报 unknown authority。

修复（`ca.go`）：遍历所有候选目录收集 PEM，用 `x509.SetFallbackRoots` 注册为兜底根池（Go 1.18+，只在系统根池不足时生效，不覆盖正常情况）。

## 日志可达性（已修）

原先日志镜像要等 `initialize` 才启用，而真机失败恰恰发生在 `initialize` 之前 —— 最需要日志时没有日志。

现在（`logsetup.go`）：进程启动第一毫秒就建立日志，从 `/proc/self/cmdline` 取包名（`com.github.kr328.clash:core` → `com.github.kr328.clash`），写入：
```
/storage/emulated/0/Android/data/com.github.kr328.clash/files/revived-core.log
```
该目录写入不需权限，文件管理器/插电脑可见，**无需 root、无需 adb**。

## BugReport 分析（用户提供）

内容：`build.prop` / `application-info.txt` / `ip-rule` 等，**`logcat.txt` 为 0 字节** —— 原版的日志收集在 `build.type=user` 设备上拿不到 logcat。

有价值的字段：
```
build.model=23113RKC6C        Redmi K70
build.fingerprint=…:17/…      Android 17
build.type=user               release，无 debuggable
build.cpu_abi=arm64-v8a
```

## 产物

```
sha256  4f0128e78ef49e2339e845ed7b1348d1a92e1733e08f396cb1c6149f0e448529
APK     20.51 MB
```

---

# 第 16 阶段：端到端自测抓出两个真 bug

## 新增能力：本机 e2e 测试（`e2e_test.py`）

不必反复装机验证。本机是 aarch64 Linux，能跑同一个内核：

```
合成 store.db（用挖出的 DDL 建库，插入一个 profile，content 是真实 mihomo 配置）
-> socketpair(非阻塞) + exec core-linux with CHANNEL   （复刻 nativeStart）
-> initialize(cacheDir, store.db, clash.socket, uuid)
-> start
-> PATCH /executor/config {activeProfile}              （触发加载 profile）
-> 遍历 20 个端点 + selector 切换 + mode 切换
```

## Bug 3：嵌入的 geoip.metadb 是坏文件（e2e 发现）

现象：`PATCH /executor/config` 卡 **45 秒**后失败：
```
rules[1] [GEOIP,CN,DIRECT] error: can't download MMDB:
Get "https://release-assets.githubusercontent.com/…"
```

根因链：
1. 我最初从 e2e 的临时目录复制了一个 112KB 的 `geoip.metadb` —— 那是 mihomo 一次**不完整**的下载
2. mihomo 的 `component/geodata/init.go` 会 `mmdb.Verify()` 校验，不合法就 `os.Remove` 然后**重新下载**
3. 于是文件被删、下载被墙/超时 → 卡 45 秒 → 返回 400

诊断方法：检查文件尾的 MaxMind 魔术字节
```
坏文件: tail = 00 4a a9 15 69 9d …        （无 MaxMind.com）
好文件: MaxMind.com 在距末尾 182 字节处   （8.4 MB，官方 release）
```

修复：从 `github.com/MetaCubeX/meta-rules-dat/releases/download/latest/geoip.metadb` 重新下载 8.4MB 完整文件，替换后 `go:embed` 嵌入。

## 修复后全链路验证

```
PATCH /executor/config   -> 200, 耗时 0.2s            （原 45s 超时）
GET /status              -> profileId=1111…, type=Loaded
GET /proxies/PROXY       -> {"now":"node-ss","proxies":["node-ss","DIRECT"],"type":"Selector"}
GET /providers/proxies   -> {"PROXY":{"name":"PROXY","proxies":["DIRECT","node-ss"]}}
PUT /proxies/PROXY       -> 200
PATCH /general           -> 200
GET /general             -> {"mode":"global"}
```

`PROXY` 组与 `node-ss` 节点是测试配置里写的，**确实被 mihomo 加载**——不是空壳。

## 代价

内核 53.6 → 61.6 MB（geoip +8.4MB），APK 20.5 → 24.5 MB。
值得：换来「真机上不会因下载 MMDB 卡死」。

## 产物

```
sha256  1505826a35855a038d9888a8024f45aa57926be16eea63c874ebec57f1b59b75
APK     24.49 MB
```

---

# 第 17 阶段：TUN 打通（核心功能）

## 之前的状态

`PUT /listeners/{TUN,VPN}/enabled` 与 `PATCH /listeners/{name}/config` 是空壳，只记录状态。**没有 TUN 就无法代理流量**——即使配置和节点都加载成功。

## 现在的链路

```
用户打开 UI 的 TUN 开关
  -> PUT /listeners/TUN/enabled {enabled:true}
  -> engine.SetListenerEnabled("TUN", true)
  -> enableTun() 调注入的 tunProvider
  -> core 的闭包 -> platform.CreateVpn -> 宿主弹 VPN 授权 -> SCM_RIGHTS 回传 tun fd
  -> 用该 fd 重启 mihomo：cfg.General.Tun.FileDescriptor = fd
     （sing-tun 在 ApplyConfig 时创建 tun listener，启动后无法热插入，故必须 Stop+Start）
```

## 解耦设计

`engine` **不 import `internal/platform`**。宿主能力通过函数注入：

```go
// engine
var tunProvider func() (int, error)
func SetTunProvider(f func() (int, error))

// core
engine.SetTunProvider(func() (int, error) {
    f, err := c.pf.CreateVpn(ctx, &platform.VpnConfigure{RouteSubnets: false})
    ...
})
```

好处：engine 可单独测试（注入假 provider），也不会被 platform 的变更波及。

## 两种时序都覆盖

| 顺序 | 行为 |
|---|---|
| 先激活订阅，再开 TUN | `enableTun` 取 fd → Stop → Start(cfg, fd) |
| 先开 TUN，再激活订阅 | `enableTun` 把 fd 存进 `pendingTunFD`；`Start` 时消费 |

## 测试（`internal/engine/tun_test.go`，5 个用例全过）

- 无 provider → 降级为 -1（纯代理模式），不报错
- provider 报错（用户拒绝授权）→ 错误冒泡，开关不被标记为 enabled
- provider 返回 fd 但无配置 → 暂存，`Start` 时消费，且只消费一次
- 关闭 TUN → 清空 pending
- provider 返回 -1 无 error → **视为失败**（否则 UI 会以为 TUN 开了）

最后一条是有意的：静默接受 -1 会让用户看到"TUN 已开启"却没有任何流量走 tun，比报错难查。

## 回归

```
ok internal/endpoint 1.197s / engine 0.905s / model 0.011s / platform 0.223s
e2e: PATCH /executor/config 0.3s, profileId 正确, /proxies/PROXY 有节点
```

---

# 第 18 阶段：真机联调 —— 三个致命 bug 的连环解锁

## 决定性突破：崩溃栈取代盲猜

真机 `logcat` 在本机为 `Off`（开发者选项里「选择日志级别」），拿到的全是 10-30 的僵尸数据。
转从 `/data/system/dropbox/data_app_crash@*.txt` 取崩溃栈，一次定位。

### Bug A：`proxies` 是对象数组，不是字符串数组

```
kotlinx.serialization.json.internal.JsonDecodingException:
Unexpected JSON token at offset 70: Expected start of the object '{', but had '[' instead
at path: $.proxies[0]
JSON input: {"history":[],"name":"GLOBAL","now":"DIRECT",
             "providers":[],"proxies":["DIRECT","REJECT"],"type":"Selector","udp":true}
```

栈里 `CollectionLikeSerializer.readElement → Proxies$Proxy$$serializer.deserialize`
是**递归**调用，证明 `proxies: List<Proxies.Proxy>`。

我此前在 dto.go 里写着「泛型擦除、无法确认，按名字列表实现」——**这个推断错了，且是致命的**。
修复：`proxiesProxy.Proxies` 改为 `[]proxiesProxy`，按名字索引递归展开，
`maxProxyDepth = 4` 防环形引用。

### Bug B：`pendingTunFD` 零值 0 被当成 tun fd

日志实证：`msg="using pending tun fd" fd=0`。
Go 零值是 0，而 fd 0 是 stdin。把 stdin 交给 sing-tun → 引擎崩塌、界面白屏。
修复：`New()` 显式置 `pendingTunFD: -1`，并在 `takePendingTun()` 里拒绝 0/1/2。

### Bug C：宿主把帧头与载荷分两次发送（本次最新）

```
msg=http method=GET path=/executor/config status=200 n=2
msg="bridge read ended" err="length 20 inconsistent with frame 20"
msg="clash-revived exited" uptime=387.254688ms
```

内核启动 387ms 后自尽，UI 只发了一个请求就没下文。

反汇编 `Transport.writeMessage` 得到调用序列：
```
113: Mutex.lock
148: allocate(payloadLen)
156: putInt(type) / putLong(callId) / putInt(method) / putInt(length)
171: flip()
178: writeFully(header)     ← 第一次
204: writeFully(payload)    ← 第二次
214: Mutex.unlock
```

`SOCK_SEQPACKET` 下两次 `writeFully` = **两个独立数据包**。
我的 `Recvmsg` 一次只收一包，先拿到 20 字节帧头、里头的 length 声称还有载荷，
于是判定「不一致」并退出。

修复：收到帧头后若载荷未到齐，继续 `recvPacket` 直到补齐。
本机用 `split_test.py`（分两包发送）复现并验证 PASS。

## 修复后的真机状态

- UI 完整渲染：VPN / 系统代理 / TUN 三卡片，字段齐全（`DNS 劫持 已禁用` 等）
- 三个进程并存：主进程 + `:core` + `libclash.so`
- 无新崩溃记录
- 访问日志升级到 Info 级别，现在能看到 UI 请求了什么、我们回了什么状态码

## 顺带修掉

- 包名解析：`/proc/self/cmdline` 在被 exec 的 `.so` 里是**可执行文件路径**，不是包名。
  旧代码拼出 `/storage/emulated/0/Android/data//data/app/.../libclash.so/files/` 这种
  荒谬路径，写盘失败且不报错 —— 这是"真机上死活看不到日志"的真因。
  改为从路径反查 + 正则校验 + 三源兜底。
- `CHANNEL` 指向无效 fd 时改为 `exit(2)`，不再静默转 standalone。
- 访问日志从 Debug 升到 Info 并记录状态码。

## 环境事实

```
Redmi 23113RKC6C / Android 17 / HyperOS
启动模式必须是 VpnService —— 原为 Shizuku，而 Shizuku 服务链路不通，
  导致内核永不被 exec、clash.socket 不建立、UI 死循环「载入中」。
  原版内核在该模式下同样卡死（已做 A/B 对照），所以不是我们的问题。
logcat 在本机为 Off，诊断走 /data/system/dropbox/
```

---

# 第 19 阶段：整条链路打通（真机验证）

## 修复 Bug C 之后

内核稳定运行，**UI 十个请求全部抵达**：

```
GET /general            200
GET /executor/config    200
GET /status             200
GET /profiles           200
GET /providers/proxies  200
GET /events             501  ← 唯一未实现（有意的空实现）
GET /proxies/GLOBAL     200
```

内核不再自尽，三个进程（主进程 / :core / libclash.so）稳定共存。

## 数据面验证通过

直接用 root 往 store.db 插入一条合成配置，再用 curl 走 unix socket 激活：

```
/profiles               -> 正确列出「测试配置」
PATCH /executor/config  -> 200
/status                 -> profileId=1111…, type=Loaded
/proxies                -> GLOBAL 组内 proxies 数组是**完整对象**（递归结构正确）
```

**代理端口实测可用**：
```
curl -x http://127.0.0.1:7890 http://www.gstatic.com/generate_204
-> http_code=204
```

mihomo 混合端口真的在转发，数据面活了。

## 仍未验证

- **UI 交互链路**：虚拟屏的触摸注入在本机不生效（`input --display` 不被支持），
  无法复刻「点新配置 → 导入订阅」的完整操作。真机点击需用户验证。
- **TUN**：代码已接 `CreateVpn` → 重启引擎，但真机没测过（需要用户点 TUN 开关并授权 VPN）。
- **订阅下载**：CA 已加载 143 张（从 `/apex/com.android.conscrypt/cacerts`），
  但还没实际下载过一个订阅。

## 环境备忘

- 虚拟屏在 `am force-stop` 后会被销毁，需重建，否则 `am start --display N` 静默失败
- `input` 命令不接受 `--display`，虚拟屏输入注入需另寻途径
- bridge 服务偶发断连（`ECONNREFUSED 127.0.0.1:18790`），重连即可

---

# 第 20 阶段：暂停开发（用户决定）

## 真机验证到的进度

**已跑通**
- 内核被 `:core` 正常 `fork+exec`（`.so` 可执行性确认）
- unix socket 通信建立（`files/clash.socket`，权限 `srwx------`）
- UI 完整渲染：VPN / 系统代理 / TUN 三张卡片，字段齐全
- 十来个端点稳定返回 200，无崩溃
- 数据面：`curl -x http://127.0.0.1:7890` 返回 204，代理端口真的在转发
- 会话 → profile 落库（手工验证：新建会话 + PUT attributes 后 profiles 表多一行）

**未跑通**
- UI 保存配置的完整交互：所有请求都 200，但 UI 仍在 `PUT attributes`/`GET /store` 之间循环 8 次后 `DELETE session`
  - 说明是**语义**不匹配（UI 在等某个条件成立），不是缺端点
  - 找到相关类：`Action$Store`、`ActionProgress$Verify`、`ActionProgress$CompleteProviders`
  - 但保存编排被 R8 内联进 Compose lambda，静态追踪断了
- TUN 未真机测试
- 远程订阅下载未实测（CA 已加载 143 张）

## 踩过的坑（给后来者）

### 环境类
1. **启动模式必须是 VpnService**。`shared_prefs/store.xml` 里 `app.launchMode=Shizuku` 且 Shizuku 服务链路不通时，
   内核永不被 exec、socket 不建立、UI 死循环「载入中」。**原版内核在该模式下同样卡死**（已 A/B 对照）。
2. **logcat 可能被整体关掉**（开发者选项「选择日志级别 = Off」）。此时诊断走
   `/data/system/dropbox/data_app_crash@*.txt`，那里有完整崩溃栈。
3. 虚拟屏在 `am force-stop` 后会被销毁，需重建，否则 `am start --display N` 静默失败。
4. `input` 命令不接受 `--display`，虚拟屏触摸注入需另寻途径。
5. `proxy.golang.org` 在本机走 IPv6 超时，必须用 `GOPROXY=https://goproxy.cn`。

### 协议类（都是真机才暴露的）
1. **宿主把帧头与载荷分两次 `writeFully` 发送**。SOCK_SEQPACKET 下这是两个独立包，
   内核必须继续收载荷，否则误判「长度不符」并在 300ms 内退出。
2. `Proxies.Proxy.proxies` 是 **`List<Proxies.Proxy>`（对象数组）**，不是字符串数组。
   返回字符串会让 kotlinx 抛 `Expected start of the object '{'`，UI 启动即崩。
3. `Proxies.Type` 只有 `Direct` / `Selector` 两个值，**不能是空串**。
4. `Proxies.Proxy.proxies` 里的占位对象必须补全 `type`，否则崩。
5. `pendingTunFD` 的 Go 零值是 0，而 **fd 0 是 stdin**。漏初始化会把 stdin 当 tun 交给 sing-tun。
6. `/profiles/sessions/{id}/store` 与 `/config/update` 这两个路径不在 dex 的字符串表里，
   是 UI 运行时拼出来的，只能靠真机日志发现。
7. ServeMux 里 `GET /profiles/{id}/store` 与 `GET /profiles/sessions/{id}` **会冲突 panic**，
   不能加通配兜底。

## 方法论教训

补 404 只是把「明显失败」变成「静默失败」。当所有请求都 200 但 UI 仍在重试时，
应该立刻去读 UI 的**轮询终止条件**，而不是继续补端点。这次绕了几轮才意识到。
