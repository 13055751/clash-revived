# Platform 反向调用协议（core → host）

> 结论来源：`libclash.so`（Go 侧，aarch64 反汇编）+ `classes.dex`（Kotlin 侧字节码）。
> 每条都标了确认程度：**已确认**=字节级证据；**推断**=形状对上但语义是猜的；**未确认**=卡住或两侧冲突。
> 复核命令见文末。

---

## 1. 通则（已确认）

| 项 | 值 |
|---|---|
| 方向 | **core → host**。内核主动发 `Call`，宿主回 `Done`/`Error`/`Cancel` |
| 帧头 | 20 字节大端：`int32 type, int64 callId, int32 method, int32 length` |
| type | `Call=0, Done=1, Error=2, Cancel=3` |
| callId | 内核用自己的序列（`transport.go` 的 `NextCallID()`，从 `1<<40` 起），避开宿主从 1 起自增的序列 |
| 载荷 | FlatBuffers（小端，root table）。空载荷 = `length=0` |
| fd | `sendmsg/recvmsg` 的 `SCM_RIGHTS` 附带，落在 `Message.FD` |
| 并发 | 同一 fd 上按 callId 配对；不同 callId 可并发。**同一 callId 绝不能有两个等待者** |
| 响应 | `Done` = 成功（载荷在 `Pair.first`，fd 在 `Pair.second`）；`Error` = 宿主抛异常 |

内核侧对应原版 `platform.callRemote`（`0xd14a50`，1488 字节）：分配 Receiver → 组包 → `WriteMessage` → 等待；ctx 取消时**补发 `Cancel`**（`callRemote` 的第二个 `WriteMessage` 调用点 `0xd14f00`）。

**Error 帧载荷**：未确认。原版把 Kotlin 异常序列化进 payload，具体编码没有逐字节还原。本实现按 UTF-8 文本尽力解析，同时保留原始字节（`RemoteError.Raw`）。

---

## 2. 13 项线上能力（method 编号已确认）

Kotlin 侧出处：`Lkotlin/UnsignedKt;->call$suspendImpl`（R8 把原 `core/Platform` 搬到了这个类名）里两个 13 分支 `packed-switch`。
Go 侧出处：每个 `platform.Xxx` 在 `bl platform.callRemote` 前把 method 号写进 `x2`。

| method | 能力 | 请求载荷 | 响应载荷 | 确认程度 |
|---|---|---|---|---|
| 0 | GetFeatures | 空 | `PlatformFeatures`（8×bool） | 表名与字段名**已确认**；slot 序**未确认** |
| 1 | GetTimeZone | 空 | `{id: string, rawOffset: long}` | **已确认** |
| 2 | SendNotification | `Notification` 表 | 空 | 槽型**已确认**；语义**推断** |
| 3 | AcquireAliveLock | `LockInfo` 表（1 个槽） | 16 字节 UUID | 槽数**已确认**；回包长度**推断** |
| 4 | ReleaseAliveLock | **裸 UTF-8 UUID 文本**（非 FlatBuffers） | 空 | **已确认**（两侧都验证过） |
| 5 | ReportState | `CoreState`：slot0=`Traffic`(4×int64)、slot2=profileName | 空 | **已确认**（读取端逐字段） |
| 6 | SetSystemProxy | `SystemProxyConfig` 根表 | 空 | **已确认**（槽位类型+语义） |
| 7 | **CreateVpn** | `VpnConfigure` 根表 | **空载荷 + fd（SCM_RIGHTS）** | **已确认**（见第 3 节） |
| 8 | ReleaseVpn | 空 | 空 | **已确认** |
| 9 | ProtectSocket | **空载荷**，fd 走 SCM_RIGHTS | 空 | **已确认** |
| 10 | QueryConnectionOwner | 形状冲突（见下） | 字符串（包名/uid） | **未确认** |
| 11 | ScheduleWakeup | 表：slot0=`int64` epoch 毫秒 | 空 | 槽型**已确认**；语义**推断** |
| 12 | CreateAppSocket | `AppSocketSpec`：3×int32 | 空载荷 + fd | 槽型**已确认**；语义**推断** |

### 2.1 各表的槽位形状

**PlatformFeatures（method 0 响应）** — 8 个 bool，名字由 Go 侧生成代码给出：
`AliveLock / CreateAppSocket / QueryConnectionOwner / ReportState / ScheduleWakeup / SystemProxy / Vpn / VpnWithSystemProxy`。
**slot 到名字的映射未确认**，本实现按 slot 0..7 原样收（`Features.Flags[8]`），不猜名字。

**Notification（method 2 请求）**
```
slot0 = int8           （推断：通知状态/类型）
slot1 = string         （推断：title）
slot2 = string         （推断：content）
slot3 = string         （推断：channel id）
```

**LockInfo（method 3 请求）**：`slot0 = int8`（唯一字段；Kotlin 侧生成的 `LockInfo` 类也只暴露一个字段）。

**CoreState（method 5 请求）**
```
slot0 = struct Traffic { int64×4 }   （推断：upload / download / uploadTotal / downloadTotal）
slot2 = string profileName
```
注意 `slot1` 空着不用。**坑**：Kotlin 的 `core/CoreState` 类在 method 5 与 method 7 里被读成**完全不同**的形状（FlatBuffers 不自校验，两种读法都能跑通）。所以本实现按 method 各写一个独立 builder，**没有**抽公共结构体。

**SystemProxyConfig（method 6 请求；也是 method 7 的 slot1 子表）**
```
slot0 = bool    enabled
slot1 = string  host
slot2 = uint16  port
slot3 = [string] excludes
```
宿主用 `ProxyInfo.buildDirectProxy(host, port, excludes)`。

**AppSocketSpec（method 12 请求）**：`slot0/1/2 = int32`（推断：`socket(2)` 的 domain/type/protocol）。

**ScheduleWakeupRequest（method 11 请求）**：`slot0 = int64` epoch 毫秒（Kotlin 用 `Instant.ofEpochMilli`）。

### 2.2 method 10 的证据冲突（未确认）

- Go 侧：`platform.QueryConnectionOwner`（`0xd16e60`）构造的根表是 **5 槽**：`int8, string, int32, string, int32`，收尾函数是 `ConnectionInfoEnd` → 看着像「五元组查询」。
- Kotlin 侧：method 10 分支把根表读成 **CoreState 形状**（`f0/f1` bool、`f2` int uid 之类），然后调 `queryConnectionOwner(CoreState)`，回一个 String。

两边对不上。**本实现暂时按 Go 侧 5 槽编码**（`ConnectionInfo`），并在代码里标了 `未确认`；联调时如果宿主报错，第一件事就是换形状重试（备选：按 CoreState 形状发 f0/f1/f2）。

---

## 3. CreateVpn（method 7）—— 关键路径

### 3.1 请求：`VpnConfigure` 根表（已确认，双向吻合）

```
slot0 = bool  路由模式
          false → 宿主 addRoute("0.0.0.0", 0)（全局）
          true  → 宿主按 R.array 里的子网列表逐条 addRoute，
                  列表为空时退回 addRoute("172.19.0.2", 32)
slot1 = 子表 SystemProxyConfig  （enabled/host/port/excludes，见 2.1）
slot2 = 子表 VpnAccessControl   （slot0=bool，slot1=[string] allowedApplications；
                                  allowedApplications 为空时宿主只放行自身包名）
```

宿主**硬编码**、内核不用管的 TUN 参数（写在 `VpnService.createTun` 里）：

```
addAddress("172.19.0.1", 30)
addDnsServer("172.19.0.2")
setMtu(9000)
establish() → ParcelFileDescriptor
```

也就是说：**内核不需要（也没法）指定 tun 的地址/DNS/MTU**，拿到 fd 直接读写 IP 包即可。

### 3.2 响应：空载荷 + fd

宿主回 `Done`，`length=0`，tun fd 在 `SCM_RIGHTS` 的 ancillary data 里（`Message.FD`）。
内核侧 recvmsg 得到：20 字节头 + 0 字节载荷 + 1 个 fd。

### 3.3 fd 生命周期（实现约定）

| 场景 | 处理 |
|---|---|
| 正常返回 | `setBlocking(fd)` 清掉 `O_NONBLOCK`（宿主给的是 ParcelFileDescriptor，可能非阻塞），再 `os.NewFile` 包起来 |
| 响应里没有 fd | 返回错误，不重试（重复发 = 宿主可能建两个 tun） |
| `Error` 帧却带 fd | 立刻 `close(fd)`，防止泄漏 |
| 重复调用 `CreateVpn` | **幂等**：已有 tun 就复用，不再发帧。并发重复时保留先到的，多拿到的关掉 |
| 超时/取消 | `Call` 失败即返回；迟到的响应的 fd 由 `Handle` 就地关闭（有单测覆盖） |
| `ReleaseVpn` | 先关本地 fd，再发 method 8（空载荷） |
| 进程退出 | `Platform.CloseAll()` 关 tun + 关 Client |

**所有权**：返回的 `*os.File` 归 `Platform` 所有；调用方只借不关（`Platform.TunFile()` 可取，但不转移所有权）。

---

## 4. 三个不走线上分发的能力

| 能力 | 结论 | 证据 |
|---|---|---|
| `StartBridge` | 本地：`sync.Once` 里初始化 CoreAPI、handler map、通道、起读循环（`platform.StartBridge.func1`） | 全二进制 `Transport.WriteMessage` 只有 3 个调用点（`callRemote`×2 + `handleRemoteCall`×1），`StartBridge` 一个都不沾 |
| `InstallCoreAPI` | 本地：注册 handler map，**不发帧** | 同上，没有任何写通道的路径 |
| `Done` | 本地：收尾/通知退出，**不发帧** | 同上；`platform.Done` 被内联，不在 13 个 `callRemote` 调用点里 |

本实现里：`Platform.InstallCoreAPI(api)` 存引用 + `Platform.CoreAPI()` 取回；`Platform.Done()` 关 Client、唤醒等待方、执行可选 `SetCloseHook`。

---

## 5. 集成（Lead 需要做的两件事）

### 5.1 传输适配器（因为 `platform.Frame` 与 `main.Message` 是两个类型）

放在 `core-new/`（package main）任意文件里：

```go
type platformTransport struct{ tr *Transport }

func (a platformTransport) ReadFrame() (*platform.Frame, error) {
	m, err := a.tr.ReadMessage()
	if err != nil {
		return nil, err
	}
	return &platform.Frame{Type: int32(m.Type), CallID: m.CallID,
		Method: m.Method, Data: m.Data, FD: m.FD}, nil
}

func (a platformTransport) WriteFrame(f *platform.Frame) error {
	return a.tr.WriteMessage(&Message{Type: MsgType(f.Type), CallID: f.CallID,
		Method: f.Method, Data: f.Data, FD: f.FD})
}
```

### 5.2 读循环归属（必须二选一，否则会抢帧）

**方案 A（改动最小）**：`core.Serve` 继续读，把非 `Call` 帧先喂给 Client：

```go
if c.plat.Handle(&platform.Frame{Type: int32(m.Type), CallID: m.CallID,
        Method: m.Method, Data: m.Data, FD: m.FD}) {
    continue // 这是我们自己反向调用的响应
}
if m.Type != TypeCall { continue }
```

**方案 B（推荐，单读循环）**：把读循环交给 Client，host→core 的 Call 走回调：

```go
go func() {
    _ = platClient.Serve(func(f *platform.Frame) {
        _ = core.dispatch(&Message{Type: MsgType(f.Type), CallID: f.CallID,
            Method: f.Method, Data: f.Data, FD: f.FD})
    })
}()
```

`Client.Start(onCall)` 是方案 B 的非阻塞封装（幂等，只起一次）。

---

## 6. 代码与测试

```
core-new/internal/platform/
├── frame.go            Frame / Transport / type 常量 / NextCallID
├── client.go           Client：Call / CallWithFD / Handle / Serve / Start / Close
├── flatbuf.go          极简 FlatBuffers 写入器 + 读取器（不引新依赖）
├── methods.go          13 项线上能力 + 3 项本地能力 + 16 个载荷类型
├── platform_unix.go    closefd / dupFD / setBlocking（raw SYS_FCNTL，无 CGO）
├── pack_test.go        载荷 round-trip（9 个用例）
└── client_test.go      内存 socketpair 模拟宿主（8 个用例）
```

验证命令（**都用 `../build.sh`，2 核、nice 19、不用 cpu7**）：

```bash
cd /home/tsukikage/小软件制作/work/core-new

../build.sh vet ./internal/platform/...
../build.sh test ./internal/platform/ -count=1
GOOS=android GOARCH=arm64 CGO_ENABLED=0 ../build.sh build ./internal/platform/...
GOOS=android GOARCH=arm64 CGO_ENABLED=0 ../build.sh build -trimpath -ldflags="-s -w" -o /tmp/platform_check.so .
```

测试覆盖：请求编码 / callId 起点 ≥ `1<<40` / 乱序响应配对 / 超时补发 `Cancel`（同 callId）/
`Error` 帧 → `RemoteError` / `SCM_RIGHTS` 收 fd 且能读 / `dup` 语义（原 fd 不被关）/
迟到响应的 fd 被关闭 / `CreateVpn` 幂等（宿主只收到 1 个 method 7）/ `Serve` 路由 host→core。

---

## 7. 未确认清单（不要当事实用）

1. `PlatformFeatures` 8 个 bool 的 slot 序。
2. `Notification` 三个 string 的具体语义（title/content/channel 是推断）。
3. `LockInfo.slot0` 的语义；`AcquireAliveLock` 回包是否严格 16 字节 UUID。
4. `Traffic` 四个 int64 的顺序（upload/download/total 是推断）。
5. `AppSocketSpec` 三个 int32 的语义。
6. `ScheduleWakeup` 的时间基准（按 epoch 毫秒处理，与 Kotlin `Instant.ofEpochMilli` 对齐）。
7. **method 10 的请求形状（两侧证据冲突，见 2.2）**。
8. `Error` 帧载荷的编码（目前按 UTF-8 文本尽力解析）。

---

## 8. 复核命令

```bash
cd /home/tsukikage/小软件制作/work

# Go 侧 method 立即数（CreateVpn = 7）
python3 tools/go_trace.py apk/lib/arm64-v8a/libclash.so CreateVpn      # 若工具在

# Kotlin 侧 method 7 分支 + 13 分支 packed-switch
python3 tools/one_method.py apk/classes.dex 2002864 484 534
python3 tools/one_method.py apk/classes.dex 2002864                    # 全文

# 宿主硬编码 tun 参数
python3 tools/one_method.py apk/classes.dex 1555620                     # VpnService.createTun
```
