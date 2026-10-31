# clash-revived

Clash for Android 3.0.3.premium 的复活工程 —— **保留原版 UI 与使用逻辑**，把已停更的闭源内核替换为基于 [mihomo](https://github.com/MetaCubeX/mihomo) 的新实现。

> 状态：**未完成，开发暂停。** 内核可正常启动、数据面可用、部分端点已通；UI 保存配置的完整流程尚未走通。
> 详见 [docs/PROGRESS.md](docs/PROGRESS.md) 末尾的「第 20 阶段」。

---

## 这是什么

原版 CFA（`com.github.kr328.clash`）的内核 `libclash.so` 是一个**独立 Go 可执行进程**，
由 `:core` 进程通过 `libcompat.so` 的 `fork+exec` 拉起，与 UI 之间走 socketpair 上的自定义帧协议。

本项目的作用是**替换那个进程**，而不碰 APK 里的任何 Java/Kotlin 代码：

```
原版 dex（一字节未改）+ 原版 :core 进程 + 新内核进程
```

好处是 UI 完全保留。代价是需要精确复刻它期望的协议与 HTTP 接口。

## 已确认的核心事实

| 项 | 值 |
|---|---|
| 帧格式 | 20 字节大端：`int32 type, int64 callId, int32 method, int32 length` + 载荷 |
| 消息类型 | `Call=0 Done=1 Error=2 Cancel=3` |
| 通道交接 | 环境变量 `CHANNEL=<fd>`，`SOCK_SEQPACKET`，fd 经 `SCM_RIGHTS` 附带 |
| method 0 | `initialize`，载荷为 FlatBuffers（4 string + LegacyDatabases 子表） |
| method 1 | `start`，空载荷 |
| method 2 | `sendEvent`，单槽 int32 事件码 |
| 数据面 | JSON over 本地 HTTP，走 `files/clash.socket`（Unix domain socket） |
| store | SQLite，五张表：`profiles` / `preferences` / `providers` / `proxy_selections` / `user_infos` |

完整逆向记录见 [docs/](docs/)。

## 目录

```
core/            新内核（Go，实现原版协议）
  main.go        入口：读 CHANNEL，attach 或 standalone
  transport.go   20 字节帧协议 + SCM_RIGHTS
  flatbuf.go     FlatBuffers 读取
  core.go        method 分发
  api.go         HTTP 挂载（unix socket）
  logsetup.go    真机日志落盘（无需 root）
  ca.go          Android 根证书注入（Android 14+ APEX 路径）
  geodata.go     内置 GeoIP（避免真机下载卡死）
  internal/
    engine/      mihomo 集成、store 读写、TUN 桥接
    endpoint/    HTTP 端点实现
    platform/    core→host 反向 RPC（CreateVpn 等 16 项能力）
    model/       从 dex 生成的数据模型（勿手改）

tools/           逆向与打包工具（纯 Python，无需 java）
  dexdump.py     dex 解析
  dalvik.py      Dalvik 反汇编
  axml.py        AndroidManifest 解析
  arm64.py       AArch64 部分反汇编
  resign.py      APK 重打包 + v2 签名
  verify_apk.py  独立 v2 验证器
  keys/          自签 RSA 2048（CN=Clash Revived (unofficial build)）

docs/            逆向报告与契约
```

## 构建

需要 **Go 1.23**（arm64）。本机若为 aarch64，用原生 Go 编译，勿用模拟层。

```bash
# 1) 取内置 GeoIP（8.4MB，不入库）
bash fetch-geodata.sh

# 2) 交叉编译到 Android
cd core
GOPROXY=https://goproxy.cn,direct GOSUMDB=off \
GOOS=android GOARCH=arm64 CGO_ENABLED=0 \
go build -trimpath -ldflags="-s -w" -o libclash.so .

# 3) 重打包 + 重签名（需原版 APK 作为底包）
cd ..
python3 tools/resign.py <原版.apk> core/libclash.so out.apk
```

`build.sh` 里有受限构建的封装（绑核 + nice，避免占满 CPU），按需取用。

## 为什么不用 GitHub Actions 编译

迭代期需要「改一行 → 30 秒看结果」的循环，CI 往返太慢。
**发布**包用 CI 是合理的，开发期走本地。

## 已知问题

1. **UI 保存配置未走通**：所有请求都返回 200，但 UI 在 `PUT attributes` 与 `GET /store` 间循环 8 次后删除会话。判断是语义不匹配而非缺端点。
2. **TUN 未真机验证**：代码已接 `CreateVpn → 重启引擎`，但未实测。
3. **远程订阅下载未实测**：CA 已能加载 143 张系统根证书，但没跑过真实下载。

## 免责

本项目仅用于个人学习与技术研究。修改后的 APK 使用自签名证书，与原版不通用，安装需卸载原版。
请遵守当地法律法规与软件原作者的许可条款。
