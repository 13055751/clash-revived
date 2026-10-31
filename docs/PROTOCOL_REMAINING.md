# 剩余协议与重打包（T4 交付）

> 证据全部来自二进制：`apk/classes.dex` / `apk/classes2.dex` 的 Dalvik 字节码、
> `apk/lib/arm64-v8a/libclash.so` 的 Go pclntab 符号与机器码、以及原始 APK 的签名结构。
> 标注：**[确认]** = 指令/字节级直接读到；**[推断]** = 由命名与上下文推定；**[未确认]** = 有卡点。
> 工具：`tools/dalvik2.py`（修正版解码器）、`tools/scan_bridge.py`、`tools/one_method.py`、
> `tools/gosym.py`、`tools/go_trace.py`、`tools/apkv2.py`、`tools/resign.py`、`tools/verify_apk.py`

---

## 0. 先修工具：`work/dalvik.py` 有三处问题（已确认）

| 问题 | 后果 | 现在用的 |
|---|---|---|
| **35c 的 A/G 弄反**（规范是 `A\|G\|op`，argc 在高 4 位） | 4 参调用 argc=0（打印 `{}`）；5 参调用 argc=12，第 5 个"寄存器"其实是 argc | `tools/dalvik2.py` |
| **缺 22x**（`move-*`/from16 是 3 字） | 被当 1 字 → 其后整段错位 | 同上 |
| **packed-switch target 解释错**（实为相对 switch 指令自身的偏移） | 绝对解释只有 6~8/13 命中指令边界，相对解释 13/13 | 同上 |

Lead 已把 dalvik.py 的 35c 修好，并决定不再维护两份 —— 后续统一用 `tools/dalvik2.py`。

**工具已知残留缺陷（诚实声明）**：`0xad..0xb7`（`*-int/2addr` 族）在本 dex 里解出的助记符与
FlatBuffers 语义不符（例如本该是 `add-int/2addr` 的位置解出 `div-int/2addr`，本该 `and-int/2addr`
的位置解出 `shl-int/2addr`，恒定偏 +3）。**本报告的所有结论都不依赖这些算术助记符**，只依赖
`const/*` 立即数、`Table->__offset(N)` 的实参、`invoke-*` 的目标与实参寄存器，这些是可靠的。
字长（12x=1 字）本身不受影响，否则后续指令流不可能保持连贯。

---

## 1. method 2+ 语义

### 1.1 宿主 → 内核（Kotlin 调 `Bridge.call`）：**只有 0 / 1 / 2 三个**

两个 dex 全量穷举（`tools/scan_bridge.py`，对每个 `call`/`call$suspendImpl` 调用点做
def-use 回溯找 method 常量的定义指令）：

```
$ python3 tools/scan_bridge.py
CoreAPI.initialize                    Bridge->call  pc=95  argc=5 regs=[v9,v2,v7,v8,v12]
   arg1 v2  <= pc 53  const/4 v2, #0        → method = 0
ShizukuService$coreJob$1$1.emit       Bridge->call  pc=91  argc=5 regs=[v2,v4,v12,v3,v13]
   arg1 v4  <= pc 90  const/4 v4, #2        → method = 2
RunKt$runCore$2$1$1.invokeSuspend     Bridge->call  pc=289 argc=5 regs=[v3,v5,v4,v10,v8]
   arg1 v5  <= pc 288 const/4 v5, #1        → method = 1
（Bridge$call$1 / Platform$call$1 / UnsignedKt.call 三处是 suspend 自转发，不是协议点）
total call sites: 6
```

| method | 名称 | 载荷 | 调用点 | 证据 |
|---|---|---|---|---|
| **0** | `initialize` | FlatBuffers 根表 5 槽（4×string + 1 子表） | `CoreAPI.initialize` pc 95 | `createString`×7 + `startTable(3)`/`startTable(5)` + `addOffset` 序列 |
| **1** | `start` | **空**（`ByteBuffer.allocate(0)`） | `RunKt$runCore$2$1$1.invokeSuspend` pc 289 | pc 282 `allocate(0)`；pc 288 `const/4 v5,#1`；随后日志 `[RunCore] Started` |
| **2** | `sendEvent`（新） | FlatBuffer `startTable(1)`，**仅当 code≠0** 时写槽 0 = int32 | `ShizukuService$coreJob$1$1.emit` pc 91 | 见下 |

**method 2 细节 [确认]**
`emit` 的 `$r8$classId` 分派是 `packed-switch`，4 个 case：key 0→pc192(`RunUpdate$UpdateAppToken`)、
key 1→pc174、key 2→pc103、**key 3→pc18 = SendEvent**；而 `RunKt$runCore$2$1$1$1` 造这个 lambda 时
`invoke-direct <init>(I,Object)` 的 classId 正是 **3**。SendEvent 分支：

```
18: check-cast v12, Number          ; 事件流元素是 Number
20: Number->intValue()
24: new RunKt$runCore$2$1$1$2      ; 仅用于日志
43: FlatBufferBuilder(16)
50: startTable(1)                  ; 1 个字段
53: if-eqz v12, ->77               ; code == 0 → 不写字段（空表）
55: prep(4,4); bb.putInt(space-4, code); vtable[0] = offset()
77: endTable() ; finish() ; dataBuffer()
90: const/4 v4, #2
91: Bridge->call(2, table, fd=null, cont)
```
语义：**内核向宿主方向不需要做任何事，只需回 `Done`**；这是宿主把平台事件码推给内核的单向通知，
但走的是 call 语义（有 callId、必须应答）。事件码的取值范围/语义 **[未确认]**：上游是
`AndroidPlatform.getEvents(): Flow`，字段类型被泛型擦除，抓到的是 `Number`；具体码值由平台侧
（网络变化、屏幕开关、VPN 撤销、时区变化等）决定，需真机联调时打日志观察。

### 1.2 内核 → 宿主（Go 调 `Platform.call`）：**method 0..12，13 个**

分发器是 `Lkotlin/UnsignedKt;->call$suspendImpl(this, method:I, data, fd, cont)`
（就是被 R8 改名搬家的 `com.github.kr328.clash.core.Platform`）。
`packed-switch` 有 13 个 case（key 0..12），逐个 case 的 handler 体与 Go 侧 FlatBuffers schema 名对得上：

| method | 能力 | 请求载荷 | 响应 | 证据 |
|---|---|---|---|---|
| 0 | `GetFeatures` | 无 | FlatBuffer 表 8×bool | pc1309：`startTable(1)` + 8 次 `addBoolean`；Go schema `PlatformFeatures{AliveLock, CreateAppSocket, QueryConnectionOwner, ReportState, ScheduleWakeup, SystemProxy, Vpn, VpnWithSystemProxy}` |
| 1 | `GetTimeZone` | 无 | `{id:string, rawOffset:long}` | pc1219：`TimeZone.getDefault().getID()` + `getRawOffset()`；Go schema `TimeZone{Id, RawOffset}` |
| 2 | `SendNotification` | 表：state + 通知类型 | 空 | pc873：`profile_result_channel` / `POST_NOTIFICATIONS` / `NotificationManagerCompat.notify` |
| 3 | `AcquireAliveLock` | `LockInfo` 子表 | 16 字节 (UUID) | pc775：`UUID.randomUUID()` → `ByteBuffer.wrap`；字符串 `No lock available` |
| 4 | `ReleaseAliveLock` | UUID（UTF-8 文本） | 空 | pc720：`Charsets.UTF_8.decode` → `UUID.fromString` |
| 5 | `ReportState` | `CoreState`：f0=Traffic 结构(4×long)、f2=profileName | 空 | pc578 |
| 6 | `SetSystemProxy` | `CoreState`（同上） | 空 | pc532 → `setSystemProxy(CoreState)` |
| 7 | **`CreateVpn`** | `CoreState` 变体（见 1.3） | **空载荷 + fd** | pc484 → `createVpn(CoreState)` → `Pair(ByteBuffer.allocate(0), Channel$FileDescriptor)` |
| 8 | `ReleaseVpn` | 无 | 空 | pc464 → `releaseVpn()` |
| 9 | `ProtectSocket` | （fd 走 SCM_RIGHTS，载荷空） | 空 | pc440 → `markSocket(fd)` |
| 10 | `QueryConnectionOwner` | `CoreState`（f0=bool?, f1=bool?, f2=int uid, f3=int?） | 空 | pc374 → `AndroidPlatform$queryConnectionOwner$2` |
| 11 | `ScheduleWakeup` | `CoreState`（含 long 时间戳） | 空 | pc237 → `AlarmManagerCompat.setExact` / `cancel`；字符串 `Scheduled wakeup` / `Alarm not supported` |
| 12 | `CreateAppSocket` | f0/f1/f2 三个 int | **空载荷 + fd** | pc136 → `createAppSocket(int,int,int)` → `Pair(allocate(0), fd)` |

这 13 个正好对应 API_CONTRACT 里 13 个能力符号（另 `StartBridge`/`InstallCoreAPI`/`Done` 不走这里）。

### 1.3 `CreateVpn`（method 7）—— platform-dev 要看的部分 **[确认]**

**响应约定**：`Bridge/Platform` 的返回值是 `Pair(ByteBuffer, Channel$FileDescriptor)`；
method 7 与 12 都是 `Pair(ByteBuffer.allocate(0), fd)` —— 即**载荷长度 0，tun 的 fd 用
`SCM_RIGHTS` 随这条消息回传**（`Message.fd`）。宿主侧 fd 来自
`ParcelFileDescriptor` → `AndroidChannel$AndroidFileDescriptor` 包装。

**请求载荷**（宿主读法，`VpnService.createTun(CoreState)` 逐字节读出来）：

| 位置 | 类型 | 用途 / 宿主行为 |
|---|---|---|
| 根表 f0（vtable off 4） | bool | **路由模式**：`0` → `addRoute("0.0.0.0", 0)`；`1` → 从资源数组解析 Subnet 列表逐条 `addRoute`，该数组为空时退回 `addRoute("172.19.0.2", 32)` |
| 根表 f1（off 6） | 子表 | 传给 `toProxyInfo()`：f0=bool enabled、f1=string host、f2=uint16 port、f3=[string] excludes → `ProxyInfo.buildDirectProxy(host, port, excludes)` |
| 根表 f2（off 8） | 子表 | VPN 配置：f0=bool、f1=[string] **allowedApplications**（空则只放行自身包名） |

宿主侧**硬编码**（不来自内核，写死在这里）：
```
addAddress("172.19.0.1", 30)
addDnsServer("172.19.0.2")
setMtu(9000)
addAllowedApplication(<自身包名>)   // 上面 f1 列表为空时
establish() → ParcelFileDescriptor
```
> 也就是说：**tun 的地址/掩码/DNS/MTU 都不由内核指定**，内核只要拿到 fd 读写 IP 包即可。
> 内核不需要（也无法）配置 tun 参数。

**注意一个坑 [未确认]**：`core/CoreState` 这个 Kotlin 类在不同 method 里被读成**不同形状**：
method 5/6 读 f0 = `Traffic` 结构、f2 = `profileName` 字符串；
method 7 读 f0 = bool、f1/f2 = 子表。FlatBuffers 不做自校验，所以两种解释都能"跑"。
给 Go 侧的建议：**按 method 分别构造表，不要复用同一个 builder 结构体**。

### 1.4 `initialize`（method 0）字段顺序

- **Kotlin 侧 [确认]**：`startTable(5)` 后 `addOffset` 的调用顺序为 f4←LegacyDatabases、
  f3←第4个 string、f2←第3个、f1←第2个、f0←第1个；字符串实参顺序为
  `(cacheDir, getDatabasePath("store.db"), filesDir/clash.socket, UUID)`。
  LegacyDatabases 子表：f0=filesPath、f1=v21xPath、f2=v23xPath。
- **Go 侧 schema 名 [确认]**（pclntab）：`InitializeRequest{DatabasePath, ListenPath, AppToken,
  CacheDirectory, LegacyDatabases}`，`LegacyDatabasePaths{FilesPath, V21xPath, V23xPath}`。
  访问器里的 vtable 常量实测：`DatabasePath=Offset(4)`、`ListenPath=Offset(6)`、
  `AppToken=Offset(8)`、`LegacyDatabases=Offset(12)`（`CacheDirectory` 用 MOVZ 载常量，扫描未命中，
  按剩余槽位是 `Offset(10)` **[推断]**）。
- **冲突点 [未确认]**：按名字对齐后，Go 的 `DatabasePath`(f0) 收到的却是 cacheDir，
  `AppToken`(f2) 收到的却是 `clash.socket` 路径 —— 与 Kotlin 的传值顺序不自洽。
  两种可能：①我的 `addOffset(index, off)` 是"字段下标"这一假设错了（若它收的是 voffset，
  则整张表平移）；②原始 app 里字段名与用法本来就不一致。
  **判定方法**：真机联调时把内核收到的 4 个 string 打出来 —— 若出现 36 字符 UUID，
  token 就在那个槽，与 `Authorization: Bearer` 一致。**在此之前不要照字段名猜槽位。**

---

## 2. token 机制

### 2.1 先排除错误线索

任务书里提到的 `EncodeToken` / `RawToken` 是 **Go 标准库 `encoding/xml`** 的函数
（`xml.(*Encoder).EncodeToken`、`xml.(*Decoder).RawToken`），与 CFA 无关。
真正的 token 符号只有两个：

```
app_flatbuffers/com/github/kr328/clash/core.(*InitializeRequest).AppToken   ← initialize 的字段
clash-multiplatform-core/hub.InstallAppToken  + 源文件 clash-multiplatform-core/hub/token.go
```
`hub.InstallAppToken` 在 pclntab 里只有名字、**没有独立的 functab 条目**（被内联进了
`main.(*Initializer).Initialize`），所以没能拿到独立函数体反汇编 **[未确认：它究竟把 token 装到哪]**。

### 2.2 生成方式：不可逆随机串 **[确认]**

```kotlin
// RunKt$runCore$2$1$1.invokeSuspend
140: invoke-static {}, UUID->randomUUID()UUID
143: move-result-object v1
144: UUID->toString()String            ; → 36 字符，8-4-4-4-12 十六进制，版本 4
...
253: CoreAPI->initialize(cacheDir, "store.db"路径, clash.socket 路径, v1, LegacyDatabases)
```
- 长度 **36**，字符集 `[0-9a-f-]`，UUID v4（122 bit 随机）。
- 生成者 = **宿主**（Kotlin/Kotlin 侧每次 `runCore` 新生成一个），不是内核。
- 不可逆：没有任何派生关系，内核无法自行算出。

### 2.3 传递与使用 **[确认]**

```
宿主 :core 进程                      内核（Go）
  UUID.randomUUID() ── initialize 载荷 ─┐
                                        └→ 存作 API secret（hub.InstallAppToken）
  每个 HTTP 请求都带：
    Authorization: Bearer <token>
    （Client$interceptor$1.intercept：StringBuilder("Bearer ").append($token)
      → Headers.add("Authorization", ...)）
```

UI 进程怎么拿到同一个 token：`RunKt.runCore` 的 `channelFlow` 会发
`RunUpdate.UpdateAppToken(token)`（pc 338 `new-instance RunUpdate$UpdateAppToken`），
UI 侧 `Clash$runCore$2$2.emit` 把它塞进 `Clash.appTokenState: StateFlow`（pc 68-81），
API 客户端据此重建。也就是说：**token 由宿主生成 → 先给内核 → 再广播给 UI 进程**。

### 2.4 对复活内核的建议

1. `initialize` 里把 4 个 string 全打日志，先确认哪个是 UUID（见 1.4 的冲突点）。
2. **P0 阶段可以不校验** `Authorization`（Kotlin 永远会带，不校验也不影响 UI 工作）；
   要校验就只接受 `Bearer <该 token>`，别做任何派生/哈希比较之外的假设。
3. 不要试图自己生成 token 反向推给宿主 —— 宿主的 token 是它自己生成的，内核只接收。

---

## 3. 重打包 + APK Signature Scheme v2（纯 python）

### 3.1 规范里最容易写错的三点（本次踩过）

1. **v2 signer 没有 `minSdk`/`maxSdk`**（那是 **v3** 的字段）。正确结构：
   ```
   signer: len(signed_data) | signed_data | len(sigs_seq) | len(pubkey) | pubkey
     sigs_seq: [ len | uint32 alg | len(sig) | sig ]
   ```
   按任务书原写法实现时，原版 `cfa.apk` 会读成 `minSdk=268 maxSdk=264` —— 268/264/259/256
   实际是 `sigs_seq 长度 / sig_entry 长度 / alg(0x0103) / sig 长度(256)`。
2. **摘要用的 EOCD 与写进文件的 EOCD 不是同一份**：算 digest 时要用
   "central directory offset 字段 = signing block 起始偏移" 的那份；写盘时必须写真实的 CD 偏移。
3. **三段各自 1 MiB 切块、不跨段合并**；块摘要 `SHA256(0xa5 ‖ uint32le(len) ‖ block)`，
   顶层 `SHA256(0x5a ‖ uint32le(块数) ‖ 各块摘要)`。

### 3.2 交付物

```
tools/apkv2.py        v2 格式库（解析/构造/chunk digest/DER/RSA 纯 python 验签）
tools/resign.py       重打包 + 签名 + 自验（主入口）
tools/verify_apk.py   独立验证器（只读 APK 内证书，不需要私钥）
tools/keys/           自签 RSA 2048（CN=Clash Revived (unofficial build)，key 权限 600）
tools/dist/cfa-revived-3.0.3.apk   示例产物
```

用法：
```bash
python3 tools/resign.py <input.apk> <new-libclash.so> <output.apk> \
        [--abis arm64-v8a] [--store-so] [--no-verify]
python3 tools/verify_apk.py <apk>
```

### 3.3 验证证据（可复跑）

**(a) 产物自验**
```
$ python3 tools/resign.py cfa.apk core-new/libclash.so tools/dist/cfa-revived-3.0.3.apk
      条目 154（删 6：armeabi-v7a, x86, x86_64 等）
      替换 lib/arm64-v8a/libclash.so <- core-new/libclash.so (5767489 字节)
      signing block 1657 字节，CD 最终偏移 0x40fcbf（摘要用 CD 偏移 0x40f646）
      chunks=7 digest=9a4016ee...b2c3b0e8
      signed_data=1031 字节，signature=256 字节
      PASS signing block 存在              start=0x40f646 size=1657 pairs=1
      PASS v2 pair (0x7109871a)            value 1613 字节
      PASS v2 结构解析                     signers=1
      PASS signer 字段                     digests=1 certs=1 sigs=1 pubkey=294B
      PASS 内容摘要 (SHA-256)              computed == declared
      PASS public key 解析                 RSA 2048 bit, e=65537
      PASS 公钥 == 证书公钥                 证书 2048 bit
      PASS 签名 (RSA PKCS#1 SHA-256, 纯 python)  ok
      PASS 签名 (openssl 交叉验证)          Verified OK
      PASS zipfile 回读 + CRC              条目=154 testzip=ok
      PASS 条目数据对齐                     全部满足
      PASS 内容逐条比对                     154 条一致，libclash.so 已替换
RESULT: PASS
```

**(b) 第三方交叉验证（关键）** —— 本机没有 apksigner / zipalign / jarsigner（只有 unzip/zipinfo）。
所以拿**原版 `cfa.apk`（Google apksigner 产出）当标准答案**，用它声明的摘要与签名反验我的实现：
```
$ python3 tools/verify_apk.py cfa.apk
  PASS 内容摘要 (SHA-256)   chunks=52 computed=5c66fe4b47...e26dc0 declared=5c66fe4b47...e26dc0
  PASS 签名 (RSA PKCS#1 SHA-256, 纯 python)      ok
  PASS 签名 (openssl 交叉验证)                     Verified OK
RESULT: PASS
```
即：**chunk digest 算法与 RSA/PKCS#1 处理都被 apksigner 的真实产物独立证实**，不是自证自洽。

**(c) 非 python 解析器验证**：`unzip -t` → `No errors detected in compressed data`。

### 3.4 缺口（不要当成绿灯）

1. **没有真机安装验证**，也没有 apksigner 的 `verify` 跑过。目前只有结构证据 + apksigner 反验。
2. **只保留 v2**，丢弃了 `0x504b4453`（SOURCE_STAMP，Play 源戳）与 `0x42726577`（VERITY_PADDING，
   APK Verity 分块）。两者都是可选、侧载不需要。**原版没有 v3**（v3 的 id 是 0xf05368c0，不存在）。
3. `lib/*.so` 保持 **deflate**（原版就是压缩的，manifest `extractNativeLibs=true`，安装时解压到
   `/data/app/.../lib/arm64/`；新内核要被 `execve`，必须落在可执行目录，所以这条必须保持）。
   `--store-so`（STORED + 16 KiB 对齐）这条路**未实测**。
4. `resources.arsc` 保持原版 STORED + 4 字节对齐（API 30+ 硬要求），实测 `data_off=0x3ae13c, mod4=0`。
5. 签名证书是**新的自签证书**，与原版不同 ⇒ 必须卸载原版再装。
6. `zipfile` 里 STORED 条目按 zipalign 规则对齐到 4 字节；deflate 条目也对齐了（无害）。

---

## 4. 待确认清单（交给联调）

| # | 事项 | 怎么确认 |
|---|---|---|
| 1 | `initialize` 4 个 string 的真实顺序 / token 在哪个槽 | 真机跑一次，把 4 个 string 打日志；出现 36 字符 UUID 即为 token |
| 2 | method 2 事件码的取值范围与语义 | 内核打印收到的 int32，对照真机操作（切网络、锁屏、撤 VPN、改时区） |
| 3 | method 7 根表 f0 bool 的真实含义（路由模式命名） | 看宿主是否在 flag=1 时走 `R.array` 私有网段路由；命名待考 |
| 4 | `hub.InstallAppToken` 把 token 装到哪（路由中间件？全局变量？） | 需 Go 侧反汇编（本机无 `go tool objdump` 可用，符号被 strip 掉 symtab；pclntab 只剩名字） |
| 5 | APK 真机可安装性 | 装机 + `logcat` 看 PackageManager 报错 |
