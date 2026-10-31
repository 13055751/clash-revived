# CFA 3.0.3 接口契约（逆向稿）

> 证据来源：`classes.dex` 字符串表 + 指令级反汇编（`app/api/*`）。
> 标注：**[确认]** = 从字节码中直接读到；**[推断]** = 由命名/上下文推定，待 jadx 源码复核。

---

## 1. 控制面：Bridge 线路协议〔确认〕

内核（Go 独立进程）与 `:core`（Kotlin）之间的双向 RPC，走 socketpair。

### 1.1 消息头（固定 20 字节，大端）

```
offset  size  field
0       4     int32   type     // 0=Call 1=Done 2=Error 3=Cancel
4       8     int64   callId   // 客户端自增，响应必须回填
12      4     int32   method   // 方法路由号
16      4     int32   length   // 紧随其后的 payload 字节数
```

证据链：
- `Transport.readHeader()`：`ByteBuffer.allocate(20)`，随后 `getInt()`→`Message.Type.values()[i]`、`getLong()`、`getInt()`、`getInt()`。4+8+4+4=20，与分配量吻合。
- `Transport.writeMessage()`：`ByteBuffer.allocate(20)`，随后 `putInt(type.ordinal())`、`putLong(callId)`、`putInt(method)`、`putInt(length)`，再 `flip()` → `Channel.writeFully(buffer)`。
- `Bridge.call(method: Int, data: ByteBuffer, fd: FileDescriptor?)`：`dispatcher.referenceQueue.getAndAdd(1)` 生成 callId → `newReceiver(callId)` → 发送 `Message(Call, callId, method, fd, data)` → 在 `receiver.messages: BufferedChannel` 上迭代取响应。

### 1.2 载荷

`Message.data: ByteBuffer`，`length` 字段给出长度。dex 含 `com/google/flatbuffers/{FlatBufferBuilder, Table, Struct}`，Go 侧含 `flatbuffers.UOffsetT` —— 载荷为 FlatBuffers。**[推断]** schema 私有，未随包提供生成类（Kotlin 侧手写 builder 调用），需从调用序列反推。

### 1.3 fd 传递

`Message.fd: Channel.FileDescriptor?` 与 payload 并行传递，用于 `CreateVpn` 回流 tun fd 等场景。

---

## 2. 数据面：本地 HTTP API〔确认路径，方法部分待复核〕

`app/api/API` 持有 `http: Http` 与 `ws: Http`（Ktor 客户端）；所有资源类（`General`、`Profiles`、`ProxyProviders`、`RuleProviders`、`Sessions`…）都通过 `withContext(Dispatchers.IO) { http.request(path, Method.X) { ... } }` 取 JSON。

序列化为 `kotlinx.serialization`（大量 `$$serializer` 伴随类），所以**过线是 JSON**。

### 2.1 端点清单（35 组）

| 路径 | 方法 | 返回/说明 | 状态 |
|---|---|---|---|
| `/listeners` | GET | `Map<String, Listener>` | 确认 |
| `/listeners/{name}/enabled` | PUT | body `{enabled: Bool}` | 确认 |
| `/listeners/{name}/config` | PATCH | `Listener.Config`（VPN/AccessControl/SystemProxy…） | 确认 |
| `/status` | GET | `Executor.Status` | 确认 |
| `/stage` | GET | `CoreManager.Stage` | 推断 |
| `/profiles` | GET | `List<Profile>` | 确认 |
| `/profiles/{uuid}` | PUT / DELETE | 增改 / 删 | 确认 |
| `/profiles/config` | GET/PUT | `PrefetchConfig` | 推断 |
| `/profiles/sessions` | POST | 新建会话 | 确认 |
| `/profiles/sessions/{uuid}` | DELETE | 删会话 | 确认 |
| `/profiles/sessions/{uuid}/attributes` | GET | 会话属性 | 确认 |
| `/profiles/sessions/{uuid}/providers` | GET | 会话内的 provider 描述 | 确认 |
| `/providers/proxies` | GET | 全部代理 provider | 确认 |
| `/providers/proxies/{name}` | GET | 单个 | 推断 |
| `/providers/proxies/{name}/healthcheck` | GET | 健康检查 | 确认 |
| `/providers/rules` | GET | 规则 provider | 确认 |
| `/providers/rules/{name}` | GET | 单个 | 推断 |
| `/proxies/{name}` | PUT | 选择器切换（选中某节点） | 确认 |
| `/executor/config` | GET / PATCH | `Executor.Config` | 推断 |
| `/general` | GET / PATCH | `General.Config` | 推断 |
| `/network/interface` | GET / PATCH | `Network.Interface` | 推断 |
| `/network/resolver` | GET / PATCH | `Network.Resolver` | 推断 |
| `/network/fakeip` | GET / PATCH | `Network.FakeIP` | 推断 |
| `/network/hosts` | GET / PATCH | hosts 映射 | 推断 |
| `/network/outbound` | GET / PATCH | `Network.Outbound` | 推断 |
| `/connections/count` | GET | `Connections.Count{now, history}` | 确认 |
| `/traffic` | GET (WS) | 实时流量 | 确认 |
| `/bandwidth` | GET (WS) | 带宽轮询 | 确认 |
| `/events` | GET (WS) | 事件流（executor/profiles/network…） | 确认 |
| `/config` | GET | 当前配置快照 | 推断 |
| `/config/update` | POST | 触发更新 | 推断 |
| `/attributes` | GET | 元信息 | 推断 |
| `/healthcheck` | GET | 存活探测 | 推断 |
| `/store` | GET | 存储视图 | 推断 |
| `/api/tokens` | POST | 换取 API token（`RunUpdate.UpdateAppToken`） | 推断 |
| `/shizuku`、`/shizuku/request` | POST | Shizuku 提权请求 | 推断 |

### 2.2 数据模型（从 `@Serializable` 类字段直接读出，节选）

```kotlin
API(bandwidth, connections, coroutineScope, events, executor, general, http,
    listeners, network, profiles, proxies, proxyProviders, ruleProviders,
    sessions, traffic, ws)

Executor.Config(activeProfile: UUID, externalController: String,
                externalControllerSecret: String)
Executor.Status(error: String?, profileId: UUID, type: Type)
General.Config(mode: Mode)

Network.Interface(autoDetect: Boolean, name: String)
Network.Outbound(ipv6: Boolean)
Network.FakeIP(enabled: Boolean, filter: List<String>, range: String)
Network.Resolver(bootstrapServers: List<String>, fallbackFilter: FallbackFilter,
                 fallbackServers: List<String>, nameServerPolicy: Map<String,Any>,
                 primaryServers: List<String>)
Network.Resolver.FallbackFilter(domains, geoip: Boolean, geoipCode: String, ipcidr)

Listeners.Listener(enabled: Boolean, status: Status)
Listeners.Listener.Config.VPN.AccessControlConfig(mode: Mode, packages: List<String>)
Listeners.Listener.Config.VPN.SystemProxyConfig(enabled: Boolean, excludes: List<String>)

Connections.Count(now: Long, history: Long)
Events.Event.Payload(event: JsonElement, source: Source)
```

规模：`app/api` 下共 **281 个类**，其中 **143 个带字段的数据类** —— 全部可从 dex 字段表机械提取，无需猜测。

---

## 3. 内核 → 宿主 的反向能力（16 项）〔确认〕

Go 侧符号表直接给出：

```
StartBridge        InstallCoreAPI     Done
CreateVpn          ReleaseVpn         ProtectSocket
CreateAppSocket    QueryConnectionOwner
GetFeatures        GetTimeZone        SetSystemProxy
ReportState        ScheduleWakeup     SendNotification
AcquireAliveLock   ReleaseAliveLock
```

Kotlin 侧实现：`core/AndroidPlatform`（含 `AndroidPlatform$Basic.createVpn`、`AndroidPlatform$Shizuku`）。
**你要的 Shizuku/root TUN 就走 `CreateVpn`：** 宿主按模式选 `VpnService.establish()` 或经 `IShizukuService` 提权建 tun，把 fd 通过 `Message.fd` 回给内核。

---

## 4. 内核内部模块（Go 符号，用于确认能力边界）

```
platform/{api,bridge,platform}.go   RPC 分发、生命周期
platform/transport/channel_unix.go  socketpair 通道
executor/   (ApplyConfig, SetMode, SetSelector, CurrentStatus, Event…)
profiles/   (List, CreateProfile, Load, update, SetConfig, StartUpdater…)
network/    (Interface, Resolver, FakeIP, Hosts, Outbound 的 get/patch)
providers/  (proxy/rule provider, healthcheck)
proxies/    rules/    logs/    memory/    traffic/    bandwidth/
listeners/  (get, getAll, patchConfig, setEnabled, ToggleStarted)
store/      (SQLite：Preference, ProfileTable, ProvidersTable, ProxySelection, UserInfo)
systemproxy/  resolve/  script/  healthcheck/  upgrade/
```

引擎侧出站仅：`Direct, Reject, Http, Socks5, ShadowSocks, ShadowSocksR, Snell, Trojan, Vmess, Wireguard` —— 再次确认缺口在 VLESS/Reality/Hy2/TUIC。
