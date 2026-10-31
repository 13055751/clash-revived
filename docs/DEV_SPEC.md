# clash-revived 开发规范（并行协作基线）

> 所有成员必须遵守本文件。协议与数据结构的事实来源是 `API_CONTRACT.md` 与 `PROGRESS.md`，
> 本文件只规定**工程约定**，避免并行冲突。

---

## 1. 仓库布局与写入边界

```
core-new/
├── go.mod / go.sum
├── main.go             Lead 独占
├── transport.go        Lead 独占（帧协议，已定稿）
├── flatbuf.go          Lead 独占（读取器，已定稿）
├── core.go             Lead 独占（method 分发）
├── api.go              Lead 独占（路由注册表，所有人只在此登记，不直接改）
├── internal/
│   ├── engine/         teammate-A 独占：mihomo 集成
│   ├── endpoint/       teammate-A 独占：35 组端点实现
│   ├── platform/       teammate-B 独占：Platform 反向调用 + TUN
│   └── model/          Lead 独占：CFA 的 JSON 数据模型（唯一真相）
└── cmd/                仅 Lead 建新命令
```

**规则**
- 任何人不得修改其他人独占的文件。需要改动时发消息给 owner，或让 Lead 改。
- 新增文件必须落在自己的目录内。
- `api.go` 的路由注册表由 Lead 统一维护；teammate-A 提供 `endpoint.Register(mux)` 一个入口。

## 2. 已定稿、不可再议的事实

以下内容已由二进制证据确认，**不要再花时间验证或改动**：

| 项 | 值 |
|---|---|
| 帧格式 | 20 字节大端：`int32 type, int64 callId, int32 method, int32 length` + 载荷 |
| type | `Call=0, Done=1, Error=2, Cancel=3` |
| method 0 | `initialize`，载荷 = FlatBuffers（根表 5 槽：4×string + 1 子表；子表 3 槽：`filesPath/v21xPath/v23xPath`） |
| method 1 | `start`，载荷为空 |
| 通道 | 环境变量 `CHANNEL=<fd>`，`SOCK_SEQPACKET`，全大端 |
| 内核入口 | 无参数；被 `fork+exec` 拉起，stdin/stdout/stderr 已重定向到 `/dev/null` |
| 数据面 | JSON over 本地 HTTP（Ktor 客户端消费） |

## 3. 数据模型是唯一真相

`internal/model/` 下的结构体必须与 `api_dataclasses.json` 中提取的字段一一对应。
**字段名与类型不允许自由发挥**——它们直接决定 Kotlin 侧能否反序列化。

JSON 字段名默认取字段名本身（Kotlin `@SerialName` 若有改名，以 `api_dataclasses.json` 的 name 为准）。

## 4. 构建（有资源硬约束，必须遵守）

### 4.0 CPU 限制 —— 这是一条硬规则

本机 8 核拓扑（`/sys/devices/system/cpu/cpu*/cpufreq/cpuinfo_max_freq`）：

| 核心 | 类型 | 频率 |
|---|---|---|
| cpu0–2 | Cortex-A510 小核 | 2.02 GHz |
| cpu3–6 | Cortex-A710 大核 | 2.80 GHz |
| cpu7 | 超大核 | 3.19 GHz |

**用户要求：编译最多占 2 个核心，且禁止使用超大核 cpu7。**

**所有构建/测试必须走 `work/build.sh`**，它已经内置 `taskset -c 0,1` + `nice -n 19` + `GOMAXPROCS=2` + `go build -p 2`：

```bash
cd /home/tsukikage/小软件制作/work/core-new

../build.sh build ./...                                  # 编译检查
../build.sh test ./internal/...                          # 跑测试
GOOS=android GOARCH=arm64 CGO_ENABLED=0 ../build.sh build -trimpath -ldflags="-s -w" -o libclash.so .
GOOS=linux   GOARCH=arm64 CGO_ENABLED=0 ../build.sh build -trimpath -o core-linux .
../build.sh vet ./...
```

脚本里有安全阀：`BUILD_CPUS` 含 7 会直接拒绝执行。

**不要**直接敲 `go build`——那会占满 8 核，把用户正在跑的渲染挤死（实测 load 曾到 34）。

### 4.1 工具链

本机是 aarch64，**不要用 `tools/go`（amd64，跑在 qemu 里，慢 15 倍）**。`build.sh` 已导出正确的 `GOROOT`/`GOPATH`。

**产物必须满足**（每次提交前自查）：
- `e_type = 3`（ET_DYN / PIE）
- `PT_INTERP = /system/bin/linker64`
- 所有 `PT_LOAD` 的 `p_align >= 0x4000`（16KB 页安全）

自查脚本：`python3 ../check_elf.py core-new/libclash.so`

## 5. 测试

- 任何协议相关改动，必须跑 `python3 bridge_test.py ./core-linux` 并保持 PASS。
- 端点实现必须带 `_test.go`，用 `httptest` 校验 JSON 形状。
- 禁止把真实订阅/节点写进测试。

## 6. 语言与风格

- 日志用 `log/slog`，不要 `fmt.Println`。
- 错误必须包裹上下文（`fmt.Errorf("xxx: %w", err)`）。
- 不用 CGO（`CGO_ENABLED=0` 是硬约束，避免 NDK 依赖）。
- 不做「以后可能用到」的抽象。

## 7. 沟通协议

- 完成一个模块 → 在共享任务上 `complete`，并给 Lead 发一条含**验证命令与结果**的消息。
- 发现规范与事实冲突 → 先发消息问 Lead，不要自行改规范。
- 阻塞超过 30 分钟 → 立刻报告，不要闷头试。

---

## 8. 任务划分

| 任务 | owner | 写入范围 | 依赖 |
|---|---|---|---|
| T1 数据模型 | Lead | `internal/model/` | 无 |
| T2 端点骨架 + mihomo 集成 | A | `internal/engine/`, `internal/endpoint/` | T1 |
| T3 Platform 反向调用 + TUN | B | `internal/platform/` | T1 |
| T4 method 2+/token/重打包 | C | `tools/` 下脚本 | 无 |
| T5 联调与验收 | Lead | — | T2/T3/T4 |
