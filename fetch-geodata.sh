#!/usr/bin/env bash
# 下载内置 GeoIP 数据库（8.4MB，不入 git）。
#
# 为什么需要：配置里只要有 GEOIP 规则，mihomo 在解析阶段就要 geoip.metadb。
# 真机上若 home 目录没有它，会去 GitHub 下载 —— 国内环境可能卡到超时，
# 表现为「导入配置后一直转圈」。所以编译期就把这份文件 embed 进二进制。
set -euo pipefail
cd "$(dirname "$0")"

URL="https://github.com/MetaCubeX/meta-rules-dat/releases/download/latest/geoip.metadb"
OUT="core/assets/geoip.metadb"

mkdir -p core/assets
if [ -f "$OUT" ]; then
  echo "已存在: $OUT ($(stat -c%s "$OUT") bytes)"
  exit 0
fi

echo "下载 $URL"
curl -fSL --retry 3 -o "$OUT.tmp" "$URL"
mv "$OUT.tmp" "$OUT"

# 校验：合法的 MMDB 文件末尾附近应有 MaxMind.com 标记。
# 曾经误用一份 112KB 的残缺下载，mihomo 校验失败后删掉重下，真机表现为卡死。
python3 - "$OUT" <<'PY'
import sys
d = open(sys.argv[1], 'rb').read()
hit = d.rfind(b'MaxMind.com')
if hit < 0:
    sys.exit("FAIL: 不是有效的 MMDB（找不到 MaxMind.com 标记）")
print("OK: %d bytes, MaxMind.com 在距末尾 %d 字节处" % (len(d), len(d) - hit))
PY
