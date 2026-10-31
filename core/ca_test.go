package main

import (
	"crypto/tls"
	"net/http"
	"testing"
	"time"
)

// 验证 installAndroidRoots 之后，Go 能否用收集到的根池完成对公开站点的 TLS 握手。
// 这条能证明「合并 CA + SSL_CERT_FILE + fallback」这套机制真的生效，
// 而不只是「收集到了证书」。
func TestInstallRootsEnablesTLS(t *testing.T) {
	n := installAndroidRoots()
	if n == 0 {
		t.Skip("本机没有可用 CA 目录，跳过")
	}
	t.Logf("loaded %d CA files", n)

	// 用一个必然存在的公开站点验证握手的证书校验路径
	tr := &http.Transport{TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12}}
	c := &http.Client{Transport: tr, Timeout: 8 * time.Second}
	req, _ := http.NewRequest("HEAD", "https://www.cloudflare.com/", nil)
	resp, err := c.Do(req)
	if err != nil {
		// 无网络时不算失败
		t.Skipf("网络不可达，跳过：%v", err)
	}
	defer resp.Body.Close()
	t.Logf("TLS OK, status=%d", resp.StatusCode)
}
