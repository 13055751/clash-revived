package main

// logsetup.go — 让真机上的日志无需 root/adb 也能看到。
//
// 背景：nativeStart 把子进程的 stdin/stdout/stderr 全部重定向到 /dev/null，
// 所以内核在真机上完全静默。而 release 包没有 android:debuggable。
//
// 【2026-10-31 真机踩坑记录】
// 最初从 /proc/self/cmdline 取包名，但那在「被 exec 的 .so」里拿到的是
// **可执行文件自身的路径**（execve 的 argv[0]），不是 `com.pkg:core` 进程名。
// 结果拼出 "/storage/emulated/0/Android/data//data/app/.../libclash.so/files/…"
// 这种荒谬路径，写盘失败且不报错 —— 真机上一直「没有日志」就是这个原因，
// 让我误判了好几轮。
//
// 现在改为多来源提取，并对结果做严格形状校验（必须是 com.xxx.yyy 形式）。

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// pkgRe 匹配 Android 包名：至少两段、全小写字母数字下划线点。
var pkgRe = regexp.MustCompile(`^[a-z][a-z0-9_]*(\.[a-z0-9_]+)+$`)

// sanitizePkg 校验并返回包名，不合格返回空串。
func sanitizePkg(s string) string {
	s = strings.TrimSpace(s)
	if s == "" || len(s) > 128 {
		return ""
	}
	// 去掉进程后缀
	if i := strings.IndexByte(s, ':'); i > 0 {
		s = s[:i]
	}
	if !pkgRe.MatchString(s) {
		return ""
	}
	return s
}

// pkgFromPath 从 APK/so 路径反查包名。
//
// 真机形态：
//
//	/data/app/~~hash==/com.github.kr328.clash-hash==/lib/arm64/libclash.so
//	/data/user/0/com.github.kr328.clash/...
//
// 取路径里第一个「像包名的目录名」，去掉 -hash 后缀。
func pkgFromPath(p string) string {
	for _, seg := range strings.Split(p, "/") {
		if seg == "" {
			continue
		}
		// 去掉 `com.pkg-xxxx==` 里的哈希后缀
		if i := strings.IndexByte(seg, '-'); i > 0 {
			if cand := sanitizePkg(seg[:i]); cand != "" {
				return cand
			}
		}
		if cand := sanitizePkg(seg); cand != "" && strings.Contains(seg, ".") {
			// 排除简单文件/目录名误判
			if strings.Count(cand, ".") >= 2 || !strings.HasSuffix(cand, ".so") {
				return cand
			}
		}
	}
	return ""
}

// packageName 按可靠性依次尝试：cmdline（普通进程）、/proc/self/exe（被 exec 的 so）。
func packageName() string {
	// 1) cmdline：在 app 主进程/：core 进程里是包名；在被 exec 的 .so 里是路径
	if b, err := os.ReadFile("/proc/self/cmdline"); err == nil && len(b) > 0 {
		name := string(b)
		if i := strings.IndexByte(name, 0); i >= 0 {
			name = name[:i]
		}
		if cand := sanitizePkg(name); cand != "" && !strings.Contains(name, "/") {
			return cand
		}
		// 是路径就走反查
		if strings.Contains(name, "/") {
			if cand := pkgFromPath(name); cand != "" {
				return cand
			}
		}
	}

	// 2) /proc/self/exe -> /data/app/.../com.pkg-hash==/lib/arm64/libclash.so
	if exe, err := os.Readlink("/proc/self/exe"); err == nil {
		if cand := pkgFromPath(exe); cand != "" {
			return cand
		}
	}

	// 3) 环境变量兜底（宿主若设置）
	for _, k := range []string{"ANDROID_PACKAGE_NAME", "CLASH_PKG"} {
		if v := os.Getenv(k); v != "" {
			if cand := sanitizePkg(v); cand != "" {
				return cand
			}
		}
	}
	return ""
}

// candidateLogPaths 按优先级给出日志落盘位置。
func candidateLogPaths() []string {
	var out []string
	if pkg := packageName(); pkg != "" {
		out = append(out,
			// 应用私有 cache（需要 root 读，但一定可写）
			filepath.Join("/data/user/0", pkg, "cache", "revived-core.log"),
			// 外部专属目录（文件管理器可见，无需权限）
			filepath.Join("/storage/emulated/0/Android/data", pkg, "files", "revived-core.log"),
		)
	}
	// 最后兜底：/data/local/tmp 在调试机上一般可写
	out = append(out, "/data/local/tmp/revived-core.log")
	out = append(out, filepath.Join(os.TempDir(), "revived-core.log"))
	return out
}

// setupEarlyLogging 立刻把日志接到能看见的地方，返回实际使用的文件列表。
func setupEarlyLogging(verbose bool) []*os.File {
	lvl := slog.LevelInfo
	if verbose {
		lvl = slog.LevelDebug
	}
	writers := []io.Writer{os.Stderr}
	var opened []*os.File
	for _, p := range candidateLogPaths() {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			continue
		}
		f, err := os.OpenFile(p, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			continue
		}
		writers = append(writers, f)
		opened = append(opened, f)
		break
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(io.MultiWriter(writers...),
		&slog.HandlerOptions{Level: lvl})))
	if len(opened) > 0 {
		slog.Info("log file ready", "path", opened[0].Name(), "pkg", packageName())
	} else {
		slog.Warn("no writable log path; logs will be lost on device")
	}
	return opened
}
