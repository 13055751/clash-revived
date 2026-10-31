package main

// api.go — 本地 HTTP API 的监听与挂载
//
// 端点实现全部在 internal/endpoint（owner: engine-dev），本文件只负责：
//   1) 监听地址的选定与日志
//   2) 把 mux 交给 endpoint.Register 统一注册
//   3) 访问日志与请求计数
//
// 【关键】真机实测（2026-10-31）：
//   界面通过 **Unix domain socket** 连内核，路径来自 initialize 的 ListenPath，
//   即 /data/user/0/<pkg>/files/clash.socket（原版是 srwx------ 的 socket 文件）。
//   我最初只监听 TCP 随机端口（127.0.0.1:0），UI 根本不知道连哪儿，
//   表现就是永远停在「载入中 / 载入 Core 状态中…」。
//
//   所以现在：**优先监听 ListenPath 上的 unix socket**，TCP 仅作为显式调试后门
//   （必须是 -http 明确给出非零端口才启用）。

import (
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/kr328/clash-revived/internal/endpoint"
	"github.com/kr328/clash-revived/internal/engine"
)

var (
	reqCount   atomic.Uint64
	tokens     = map[string]bool{}
	handlerMu  sync.Mutex
	sharedMux  *http.ServeMux
	sharedHTTP http.Handler
)

// buildHandler 创建（或复用）统一的路由 handler。
// unix socket 与 TCP 共用同一份，避免两套注册逻辑漂移。
func buildHandler(c *Core) http.Handler {
	handlerMu.Lock()
	defer handlerMu.Unlock()
	if sharedHTTP != nil {
		return sharedHTTP
	}
	mux := http.NewServeMux()
	endpoint.Register(mux, engine.Default())
	sharedMux = mux
	sharedHTTP = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reqCount.Add(1)
		// 用 Info 级别：真机排障时这是唯一能看到"UI 到底请求了什么"的窗口。
		// Debug 级别在默认配置下被过滤，之前正是因此看不到任何请求记录。
		sw := &statusWriter{ResponseWriter: w, code: 200}
		mux.ServeHTTP(sw, r)
		slog.Info("http", "method", r.Method, "path", r.URL.Path,
			"status", sw.code, "n", reqCount.Load())
	})
	return sharedHTTP
}

// serveUnix 在 path 上监听 unix socket。原版就是这条路。
//
// 残留文件处理：内核被宿主杀掉时 socket 文件会留下（真机上看到过
// clash.socket 存在但没人监听）。bind 前先 unlink，否则 listen 会 EADDRINUSE。
// 风险是可接受的：这个路径由 initialize 指定、且只有本内核会绑。
func serveUnix(c *Core, path string) {
	if path == "" {
		return
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		slog.Warn("cannot remove stale unix socket", "path", path, "err", err)
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		slog.Error("unix listen failed", "path", path, "err", err)
		return
	}
	// 原版是 srwx------，只允许同 uid 访问。
	if err := os.Chmod(path, 0o600); err != nil {
		slog.Warn("chmod unix socket failed", "path", path, "err", err)
	}
	slog.Info("api listening on unix socket", "path", path, "pid", os.Getpid())

	srv := &http.Server{Handler: buildHandler(c), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		if err := srv.Serve(ln); err != nil {
			slog.Error("unix api server exited", "err", err)
		}
	}()
}

// ServeAPI 保留 TCP 入口，但只在 -http 显式给出非 ":0" 地址时启用。
// 默认不再开随机端口——那不是 UI 用的通道，开着只会误导排查。
func ServeAPI(c *Core) error {
	addr := *flagHTTPAddr
	if addr == "" || addr == "127.0.0.1:0" {
		slog.Debug("tcp api disabled (ui talks over unix socket)")
		<-c.done
		return nil
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	slog.Info("api listening on tcp", "addr", ln.Addr().String(), "pid", os.Getpid())
	srv := &http.Server{Handler: buildHandler(c), ReadHeaderTimeout: 10 * time.Second}
	return srv.Serve(ln)
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// statusWriter 记录响应状态码，供访问日志使用。
type statusWriter struct {
	http.ResponseWriter
	code int
}

func (w *statusWriter) WriteHeader(code int) {
	w.code = code
	w.ResponseWriter.WriteHeader(code)
}
