package main

// ca.go — 让 Go 的 TLS 信任 Android 的系统根证书。
//
// 真机现象（Redmi K70 / Android 17）：
//   下载资源: Get "https://kaze1.aisaka-taiga.com/…":
//   tls: failed to verify certificate: x509: certificate signed by unknown authority.
//
// 已排除的：
//   - 网络：本机 openssl 直连该域名可以握手（Verify return code: 0）
//   - 证书：issuer = Google Trust Services WE1，标准公开 CA，任何系统都预置
//
// 根因：Go 的 crypto/x509 在 Android 上走 root_android.go，只读
// /system/etc/security/cacerts 一条固定路径。而 Android 14+ 系统 CA 在 APEX：
//   /apex/com.android.conscrypt/cacerts
// 用户手动装的 CA 在 /data/misc/keychain/cacerts-added。
// 这台机器上那条固定路径没有可用 PEM → 根池为空 → 任何 https 都报 unknown authority。
//
// 修复策略（双保险，生效条件不同）：
//   1) 把所有候选目录的 PEM 合并成一个 bundle，设置 SSL_CERT_FILE 指向它。
//      Go 的 SystemCertPool() 首次调用时读这个环境变量 —— 对「根池为空」最有效。
//   2) 再 x509.SetFallbackRoots(pool) 兜底。
//      它只在系统池**完全不可用**时生效，单靠它不够（上一版就栽在这）。
//
// 必须在任何 TLS 握手之前调用（根池首次使用时固定）。

import (
	"crypto/x509"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
)

var androidCADirs = []string{
	"/apex/com.android.conscrypt/cacerts", // Android 14+ 主位置
	"/system/etc/security/cacerts",        // 传统位置
	"/product/etc/security/cacerts",
	"/system/etc/ssl/certs",
	"/data/misc/keychain/cacerts-added", // 用户安装的 CA
	"/data/misc/keychain/cacerts-removed",
	// 容器/桌面环境，便于本机自测
	"/etc/ssl/certs",
	"/usr/lib/ssl/certs",
}

// collectCAs 读取所有候选目录里像 PEM 的文件，合并返回；同时统计各目录命中数。
func collectCAs() ([]byte, map[string]int) {
	var merged []byte
	perDir := map[string]int{}
	seen := map[string]bool{}

	for _, dir := range androidCADirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		n := 0
		for _, e := range entries {
			if strings.HasPrefix(e.Name(), ".") {
				continue
			}
			data, err := os.ReadFile(filepath.Join(dir, e.Name()))
			if err != nil || !strings.Contains(string(data), "BEGIN CERTIFICATE") {
				continue
			}
			// 粗粒度去重：/etc/ssl/certs 里的 hash 链接会重复
			key := string(data[:minInt(64, len(data))])
			if seen[key] {
				continue
			}
			seen[key] = true
			merged = append(merged, data...)
			if len(merged) > 0 && merged[len(merged)-1] != '\n' {
				merged = append(merged, '\n')
			}
			n++
		}
		if n > 0 {
			perDir[dir] = n
		}
	}
	return merged, perDir
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// writeCABundle 把合并后的 PEM 落到一个可读位置，返回路径。
// Android 上优先应用私有 cache 目录（无需权限）；其它平台退到临时目录。
func writeCABundle(merged []byte) string {
	var cands []string
	if pkg := packageName(); pkg != "" {
		cands = append(cands,
			filepath.Join("/data/user/0", pkg, "cache", "ca-bundle.pem"),
			filepath.Join("/data/data", pkg, "cache", "ca-bundle.pem"),
		)
	}
	cands = append(cands, filepath.Join(os.TempDir(), "clash-revived-ca-bundle.pem"))

	for _, p := range cands {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			continue
		}
		if err := os.WriteFile(p, merged, 0o644); err != nil {
			continue
		}
		return p
	}
	return ""
}

// installAndroidRoots 安装根证书，返回加载的证书数量。
func installAndroidRoots() int {
	merged, perDir := collectCAs()
	if len(merged) == 0 {
		slog.Warn("no CA certificate found anywhere; https will fail", "dirs", androidCADirs)
		return 0
	}

	// (1) SSL_CERT_FILE —— 让 SystemCertPool 直接读这个合并包
	if p := writeCABundle(merged); p != "" {
		if err := os.Setenv("SSL_CERT_FILE", p); err == nil {
			slog.Info("SSL_CERT_FILE set", "path", p)
		} else {
			slog.Warn("Setenv SSL_CERT_FILE failed", "err", err)
		}
	} else {
		slog.Warn("could not write CA bundle; relying on fallback roots only")
	}

	// (2) 兜底根池
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(merged) {
		slog.Warn("AppendCertsFromPEM failed; bundle may be malformed")
	}
	x509.SetFallbackRoots(pool)

	total := 0
	for _, v := range perDir {
		total += v
	}
	slog.Info("android CA installed", "certs", total, "perDir", perDir, "bundleBytes", len(merged))
	return total
}
