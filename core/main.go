package main

// main.go — clash-revived 内核进程入口
//
// 形态与原版 libclash.so 一致：PIE 可执行，interpreter /system/bin/linker64，
// 由 :core 进程的 AndroidProcess.nativeStart(path) fork+exec 拉起。
// 通道通过环境变量 CHANNEL=<fd> 交接（见 transport.go 注释），
// 因此本进程不读任何命令行参数即可对接原宿主。

import (
	"flag"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"syscall"
	"time"
)

const version = "3.0.3.premium-revived+1"

var (
	flagHTTPAddr   = flag.String("http", "127.0.0.1:0", "local API listen address (standalone mode)")
	flagStandalone = flag.Bool("standalone", false, "run without a Bridge peer (dev mode)")
	flagVerbose    = flag.Bool("v", false, "verbose logging")
	flagUnix       = flag.String("unix", "", "put the HTTP API on this unix socket path (dev)")
	flagTun        = flag.Bool("tun", false, "request tun fd from host on start (needs --tun for integration)")
)

func main() {
	flag.Parse()

	// 必须在任何可能失败的操作之前建立日志文件：
	// 真机上的失败往往发生在 initialize 之前，那时还没有 CacheDir 可用。
	setupEarlyLogging(*flagVerbose)

	// 在 mihomo 发起任何 https 请求之前装好 Android 根证书。
	// 晚于第一次 TLS 握手就没用了（根池是首次使用时固定的）。
	installAndroidRoots()

	slog.Info("clash-revived starting", "version", version, "pid", os.Getpid(), "arch", "arm64")

	var tr *Transport
	if !*flagStandalone {
		// 原宿主通过 CHANNEL=<fd> 交接；也允许 -channel-fd 显式指定，便于本地联调。
		if v := os.Getenv("CHANNEL"); v != "" {
			if fd, err := strconv.Atoi(v); err == nil && fd >= 0 {
				// 关键：nativeStart 对 socketpair 做了 fcntl(F_SETFL, O_NONBLOCK)。
				// 我们的读循环是阻塞式的，若不清除非阻塞标志，首次 Recvmsg 在宿主
				// 还没发消息时立刻返回 EAGAIN，会被当成致命错误 → 内核 2ms 退出
				// → 宿主随后 sendmsg 得到 EPIPE。真机实测就是这个现象。
				if err := syscall.SetNonblock(fd, false); err != nil {
					slog.Warn("clear O_NONBLOCK failed", "fd", fd, "err", err)
				}
				tr = NewTransportFD(fd)
				slog.Info("bridge attached via CHANNEL", "fd", fd, "blocking", true)
			} else {
				slog.Error("CHANNEL is not a number", "value", v)
			}
		}
		if tr == nil {
			// 宿主明确给了 CHANNEL 却用不了 —— 这是硬错误。
			// 静默转 standalone 会让内核"活着但没人连得上"，比直接退出更难排查。
			if os.Getenv("CHANNEL") != "" {
				slog.Error("CHANNEL present but unusable; exiting", "value", os.Getenv("CHANNEL"))
				os.Exit(2)
			}
			slog.Warn("CHANNEL absent; falling back to standalone")
			*flagStandalone = true
		}
	}

	core := NewCore(tr)
	if *flagUnix != "" {
		serveUnix(core, *flagUnix)
	}
	if tr != nil {
		go core.Serve()
	} else {
		go core.ServeStandalone()
	}
	core.Wait()
	slog.Info("clash-revived exited", "uptime", time.Since(startTime).String())
}

func fatal(f string, a ...any) {
	slog.Error(fmt.Sprintf(f, a...))
	os.Exit(1)
}

var startTime = time.Now()
