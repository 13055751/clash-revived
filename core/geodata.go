package main

// geodata.go — 内置 GeoIP 数据库，避免真机上因下载 MMDB 而卡住启动。
//
// 背景：配置里只要有 GEOIP 规则，mihomo 在 ParseWithBytes 阶段就需要
// geoip.metadb。home 目录没有时它会去 GitHub 下载
// （默认 geox-url: https://github.com/MetaCubeX/meta-rules-dat/...）。
// 国内环境这一步可能极慢甚至失败，表现是「导入配置后一直转圈」。
//
// 本文件把 112KB 的 geoip.metadb 嵌入二进制（gzip 后仅 0.06MB），
// 在 initialize 拿到 home 目录后写出去。有则跳过，不覆盖用户已有文件。

import (
	_ "embed"
	"log/slog"
	"os"
	"path/filepath"
)

//go:embed assets/geoip.metadb
var geoipData []byte

// ensureGeoIP 确保 home 目录下有 geoip.metadb；缺失时用内置副本补齐。
func ensureGeoIP(homeDir string) {
	if homeDir == "" || len(geoipData) == 0 {
		return
	}
	p := filepath.Join(homeDir, "geoip.metadb")
	if st, err := os.Stat(p); err == nil && st.Size() > 0 {
		slog.Debug("geoip already present", "path", p, "size", st.Size())
		return
	}
	if err := os.MkdirAll(homeDir, 0o755); err != nil {
		slog.Warn("cannot create home dir for geoip", "dir", homeDir, "err", err)
		return
	}
	if err := os.WriteFile(p, geoipData, 0o644); err != nil {
		slog.Warn("cannot write embedded geoip", "path", p, "err", err)
		return
	}
	slog.Info("embedded geoip installed", "path", p, "bytes", len(geoipData))
}
