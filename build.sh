#!/usr/bin/env bash
# 受限构建包装器
#
# 本机 8 核，拓扑如下（读 /sys/.../cpuinfo_max_freq 得到）：
#   cpu0-2  Cortex-A510  2.02 GHz   小核
#   cpu3-6  Cortex-A710  2.80 GHz   大核
#   cpu7    超大核        3.19 GHz
#
# 规则（用户要求）：编译最多占 2 个核心，且禁止使用超大核 cpu7。
# 默认绑到 cpu0,1（两个小核）并以 nice 19 运行，给前台交互和渲染让路。
#
# 用法：
#   ./build.sh ./...                                   # 编译检查
#   GOOS=android GOARCH=arm64 CGO_ENABLED=0 ./build.sh -o libclash.so .
#   ./build.sh test ./internal/...                     # 跑测试（同样受限）
#
# 可用 BUILD_CPUS 覆盖绑核（如 BUILD_CPUS=0,1,2），但绝不要包含 7。

set -euo pipefail

export GOROOT="${GOROOT:-/home/tsukikage/小软件制作/work/tools/go-arm64}"
export PATH="$GOROOT/bin:$PATH"
export GOPATH="${GOPATH:-/home/tsukikage/小软件制作/work/tools/gopath-arm64}"
export GOFLAGS="${GOFLAGS:--mod=mod}"

# 依赖代理：proxy.golang.org 在本机走 IPv6 会超时，用国内镜像。
# GOSUMDB=off 是因为镜像不代理 sum.golang.org；模块内容仍由 go.sum 校验。
export GOPROXY="${GOPROXY:-https://goproxy.cn,direct}"
export GOSUMDB="${GOSUMDB:-off}"

# 限制 Go 运行时的并行度（编译器自身 goroutine 数）
export GOMAXPROCS="${GOMAXPROCS:-2}"

CPUS="${BUILD_CPUS:-0,1}"

# 安全阀：拒绝把超大核纳入
case ",$CPUS," in
  *,7,*) echo "build.sh: 拒绝使用超大核 cpu7（BUILD_CPUS=$CPUS）" >&2; exit 2 ;;
esac

# -p 只能跟在 build/test/vet/install 之后；其它子命令不要加
run_go() {
  local sub="$1"; shift || true
  local args=()
  case "$sub" in
    build|test|vet|install) args=("$sub" -p 2) ;;
    *)                      args=("$sub") ;;
  esac
  if command -v taskset >/dev/null 2>&1; then
    exec taskset -c "$CPUS" nice -n 19 go "${args[@]}" "$@"
  else
    echo "build.sh: 没有 taskset，退化为仅用 GOMAXPROCS=2 与 nice" >&2
    exec nice -n 19 go "${args[@]}" "$@"
  fi
}

run_go "$@"
