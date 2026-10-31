# Clash for Android 3.0.3.premium — 架构逆向报告

> 目标：保留原 UI 与使用逻辑，适配新内核（支持 VLESS / Reality / Hysteria2 等），并延续 Shizuku/root 的 TUN 方案。
>
> 本文所有结论均标注证据来源，未验证项明确标为「推断」。

---

## 0. 身份确认

| 项 | 实测值 | 证据 |
|---|---|---|
| 包名 | `com.github.kr328.clash` | AndroidManifest.xml (AXML) |
| 版本 | `3.0.3.premium` / versionCode `300003` | AndroidManifest.xml |
| SDK | minSdk **24** / targetSdk **33** / compileSdk 33 | AndroidManifest.xml |
| 签名 | CN=`Clash for Android`，自签，2019-12-06 → 2044-11-29，**v2 方案**（无 v3） | APK Signing Block |
| 构建工具 | AGP 8.0.2 / Gradle 8.1.1 / Kotlin **1.8.20** | `kotlin-tooling-metadata.json` |
| 代码 | **未混淆**（类名/字段名完整可读），3957 个类 | dex 解析 |
| 内核 | Go **1.20.5**，`clash-multiplatform-core`（premium 分支），编译于 **2023-05-16** | libclash.so strings |
| 体积 | 52MB（4 ABI × 28MB 内核） | zip 目录 |

**premium 不是在线订阅校验。** dex 中无 Google Play Billing 库；premium 是编译期变体（单一字符串 `app.isPremium`）。但存在安装来源 + 签名校验痕迹：`apk.installer`、`getInstallSourceInfo`、`getInstallerPackageName`、`getSigningSignatures`、`[Landroid/content/pm/Signature;`。

---

## 1. 关键发现：内核是独立可执行进程，不是 JNI 库

这是整个方案的地基。

```
libclash.so   e_type = ET_DYN, PT_INTERP = /system/bin/linker64, 导出 main.main / _start
libcompat.so  e_type = ET_DYN, 无 PT_INTERP（真·共享库，被 JVM 加载）
```

`libcompat.so` 导出标准 JNI：

```
Java_com_github_kr328_clash_core_AndroidProcess_nativeStart(Ljava/lang/String;)   ← fork + exec
Java_com_github_kr328_clash_core_AndroidProcess_nativeKillProcess(I)
Java_com_github_kr328_clash_core_AndroidProcess_nativeWaitProcess(I)
Java_com_github_kr328_clash_core_AndroidChannel_nativeReadMessage(IL[BII[I)I
Java_com_github_kr328_clash_core_AndroidChannel_nativeWriteMessage(IL[BII[I)I
Java_com_github_kr328_clash_core_UnixSocket_nativeConnectUnixSocket(...)
Java_com_github_kr328_clash_core_UnixSocket_nativeGetSoTimeout / nativeSetSoTimeout
Java_com_github_kr328_shizuku_ShizukuProvider_nativeCreateSocket
JNI_OnLoad
```

而 `libclash.so` **没有任何 `Java_*` 导出符号** —— 它不通过 JNI 被调用。它是被 `fork()+execve()` 起来的一个独立 Go 进程，`extractNativeLibs=true` 保证它落在可执行的 `nativeLibraryDir`。

> **结论：换内核 = 换一个「实现了同样 socket 协议的独立可执行文件」，不需要匹配任何 JNI ABI。** 这是 B 方案可行的根本原因。

---

## 2. 三级进程架构

```
┌─ app 进程 ────────────────────────────────────────┐
│  MainActivity / Compose UI                        │
│  clash/app/api/*  (API, Profiles, Proxies,        │
│                    Connections, Events, Executor,  │
│                    Bandwidth, Network)             │
│  CoreManager → CoreAPI.initialize(...)             │
└───────────────┬───────────────────────────────────┘
                │ Binder / ClashProvider(action=API) / ForwardService
┌───────────────▼─ :core 进程（Kotlin helper）──────┐
│  core/AndroidPlatform   （实现 Platform 能力）     │
│  core/AndroidProcess    （fork+exec 内核）         │
│  core/AndroidChannel    （socketpair 收发）        │
│  core/bridge/{Bridge,Channel,Transport,Handler,   │
│               Message}                             │
│  core/Poller, CoreState, RunUpdate, LockInfo      │
└───────────────┬───────────────────────────────────┘
                │ socketpair（双向 RPC）
┌───────────────▼─ libclash.so（Go 内核进程）───────┐
│  platform.StartBridge / handleAPICall             │
│  platform.callRemote / handleRemoteCall           │
│  platform/transport.Channel.ReadMsg / WriteMsg    │
│  clash 引擎（premium：SS/SSR/VMess/Trojan/Snell/WG）│
└───────────────────────────────────────────────────┘
```

---

## 3. 线路协议（已还原到字段级）

从 dex 反解出的类：

```kotlin
class Message(
    val type:   Message.Type,
    val callId: Long,
    val method: Int,
    val fd:     Channel.FileDescriptor?,
    val data:   ByteBuffer
)

class Message.Header(          // 线上头部
    val type:   Message.Type,
    val callId: Long,
    val method: Int,
    val length: Int
)

enum class Message.Type { Call, Done, Error, Cancel }
```

`core/bridge/Bridge`：

```kotlin
fun call(method: Int, data: ByteBuffer, fd: Channel.FileDescriptor?): Serializable  // suspend
fun close()
// 字段：dispatcher, incomingJob, platform: Handler, transport: Transport
```

**消息模型**：`Call` 发起请求 → `Done` 正常返回 / `Error` 异常返回 / `Cancel` 取消。请求-响应配对靠 `callId: Long`，方法路由靠 `method: Int`。

**载荷编码 = FlatBuffers**：
- dex 中存在 `com/google/flatbuffers/{FlatBufferBuilder, Table, Struct, FlatBufferBuilder$HeapByteBufferFactory}`；
- Go 侧存在 `flatbuffers.UOffsetT / SOffsetT / VOffsetT` 符号；
- Go 侧 **无** Java 序列化痕迹（`0xACED0005` 出现 0 次，`java/lang/String` / `Ljava/lang` 均 0 次）——排除了 Java 序列化与 gob 跨语言的可能。

> 因此载荷是 FlatBuffers，schema 由项目私有（`.fbs`），Kotlin 侧手写 `FlatBufferBuilder` 调用，没有生成类。**schema 需从 Kotlin 字节码侧的字段访问序列反推。**

---

## 4. 内核 → 宿主 的反向能力表（Platform API）

Go 内核进程通过同一通道反向调用 Kotlin，Go 侧符号已完整列出：

| 能力 | 用途（推断依据） |
|---|---|
| `StartBridge` | 内核入口：绑定通道、启动 RPC 循环 |
| `InstallCoreAPI` | 向宿主注册 CoreAPI 实现 |
| `CreateVpn` | 请求宿主建立 TUN，返回 fd（VpnService 或 Shizuku 路径） |
| `ReleaseVpn` | 释放 TUN |
| `ProtectSocket` | 对新 socket 调 `VpnService.protect(fd)`，避免路由回环 |
| `QueryConnectionOwner` | 查 uid/包名，做 per-app 路由 |
| `CreateAppSocket` | 由宿主代建 socket（受权限约束的场景） |
| `GetFeatures` | 协商宿主支持的能力集 |
| `GetTimeZone` | 时区 |
| `SetSystemProxy` | 设置/清除系统代理 |
| `ReportState` | 上报核心状态（CoreState / ParcelStage） |
| `ScheduleWakeup` | 定时唤醒（AlarmReceiver） |
| `SendNotification` | 通知栏状态（NotifierReceiver） |
| `AcquireAliveLock` / `ReleaseAliveLock` | 前台保活锁（AliveLocks） |
| `Done` | 内核退出通知 |

Kotlin 侧对应实现：`core/AndroidPlatform`，含 `AndroidPlatform$Basic`（`createVpn`、`queryConnectionOwner`）与 `AndroidPlatform$Shizuku`（`IShizukuService`、`IShizukuCallback`、`ShizukuProvider.nativeCreateSocket`）。

> 你要的「Shizuku/root 跑 TUN」就在这条线上：`CreateVpn` → 宿主按模式选择 VpnService 或 Shizuku 提权路径 → 返回 tun fd。

---

## 5. 现状能力 vs 缺口

`strings` 计数实测（`libclash.so`）：

| 协议 | 计数 | 结论 |
|---|---|---|
| shadowsocks | 302 | 支持 |
| ssr | 312 | 支持 |
| vmess | 225 | 支持 |
| trojan | 77 | 支持 |
| snell | 130 | 支持 |
| wireguard | 594 | 支持 |
| **vless** | **0** | 不支持 |
| **reality** | **0** | 不支持 |
| **hysteria / hysteria2** | **0** | 不支持 |
| **tuic** | **0** | 不支持 |
| **shadowtls / anytls / mieru / juicity / naive** | **0** | 不支持 |

内核停在 2023-05 的 Clash Premium 水平。

另有两个兼容性硬伤：

1. **16KB 页大小**：`libclash.so` / `libcompat.so` 的所有 `PT_LOAD` 段 `p_align = 0x1000`（4KB）。Android 15 起部分设备默认 16KB 页，这类 .so 会 `dlopen` 失败。红米 K70（骁龙 8 Gen 2）大概率不受影响，但换机即中。
2. **targetSdk 33**：Android 15/16 上 `SCHEDULE_EXACT_ALARM` 默认拒绝、前台服务类型、edge-to-edge 等行为都会影响。

---

## 6. 三条改造路线

### 路线 1：二进制补丁（不换内核）
改 `p_align`、改 `targetSdk`、绕 installer 校验。
- 能解决的：新系统装得上、开得起来。
- 不能解决的：**VLESS/Reality/Hy2 依然不支持**（协议在 Go 内核里）。
- 代价：无原私钥 → 必须卸载重装；installer 校验需逐个点补。
- 工作量：小（数天）。

### 路线 2：重写内核进程（推荐，符合你的诉求）
新写一个 Go 可执行文件，实现第 3、4 节的协议，引擎层挂 mihomo 库。
- UI、`clash/app/api/*`、`:core` 的 Kotlin 层**全部不动**。
- 协议面是闭集：4 种消息类型 + 方法号表 + FlatBuffers schema + 16 个 Platform 能力。
- 需新增：TUN fd 接收、socket protect、per-app 路由回调对接。
- 代价：仍需重建 APK 并重签名（除非把它作为独立 APK 与旧版共存，但那会失去「原界面」）。
- 工作量：中到大（协议还原 + 内核实现 + 联调，以周计）。

### 路线 3：重建工程（保留界面观感，换实现）
拿反编译结构重写 Kotlin 工程，直接基于 mihomo 的 JNI/HTTP 接口。
- 界面可以照着 1:1 复刻（未混淆 + Compose，可读性高）。
- 但等于重写，`clash/app/api/*` 的语义要重新实现。
- 工作量：大。
- 只有在路线 2 的协议还原受阻时才值得走。

---

## 7. 已确认的环境约束

| 项 | 状态 |
|---|---|
| 本机 Java / apktool / jadx | 无（正在下载免安装 JDK + jadx） |
| 本机 Go 工具链 | 无（go.dev 可达，正在下载） |
| 外网 | **GitHub / proxy.golang.org 可达**；pypi 镜像超时 |
| sudo | 需要密码，不可用 → 只能装到家目录 |
| 磁盘 | 40G 可用 |
| 构建 | 用户提供 GitHub PAT，可用 Actions 构建（免费额度足够） |

---

## 8. 下一步（待办）

1. jadx 反编译 `classes.dex` → 拿到 `Bridge` / `Transport` / `Handler` / `AndroidPlatform` 的可读源码。
2. 从 `Transport` + `Handler` 的 `FlatBufferBuilder` 调用序列反推 FlatBuffers schema。
3. 从 Kotlin 侧提取 `method: Int` 常量表（CoreAPI 的方法路由）。
4. 从 Go 侧符号提取 `handleAPICall` 的分发表，双向校验方法号。
5. 测绘 `CreateVpn` / `ProtectSocket` 的完整报文与 fd 传递方式。
6. 产出 `protocol.md`（精确到字节）+ 新内核骨架。
7. 用 GitHub Actions 编译新内核 + 重打包 APK。
