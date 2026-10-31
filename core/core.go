package main

// core.go — Bridge 消息分发与内核生命周期
//
// 对应原版的 platform.StartBridge / handleAPICall / callRemote / handleRemoteCall。
// 原版是 Go 进程主动向 Kotlin 宿主注册能力（CreateVpn / ProtectSocket / ...），
// 方向为 core -> host 的 Call，host 以 Done/Error 回填。

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/kr328/clash-revived/internal/engine"
	"github.com/kr328/clash-revived/internal/platform"
)

type Core struct {
	tr        *Transport
	plat      *platform.Client
	pf        *platform.Platform
	mu        sync.Mutex
	started   bool
	init      InitRequest
	done      chan struct{}
	closeOnce sync.Once
	unixOnce  sync.Once
}

// platformTransport 把 main.Transport 适配成 platform.Transport。
// 两个包刻意解耦（platform 不 import main），这是唯一粘合点。
type platformTransport struct{ tr *Transport }

func (a platformTransport) ReadFrame() (*platform.Frame, error) {
	m, err := a.tr.ReadMessage()
	if err != nil {
		return nil, err
	}
	return &platform.Frame{Type: int32(m.Type), CallID: m.CallID,
		Method: m.Method, Data: m.Data, FD: m.FD}, nil
}

func (a platformTransport) WriteFrame(f *platform.Frame) error {
	return a.tr.WriteMessage(&Message{Type: MsgType(f.Type), CallID: f.CallID,
		Method: f.Method, Data: f.Data, FD: f.FD})
}

// Platform 暴露反向调用客户端，供 engine 等上层在需要宿主能力时使用。
// 未接桥（standalone）时为 nil，调用方必须判空。
func (c *Core) Platform() *platform.Client { return c.plat }

// InitRequest 是 method 0 的解码结果。
//
// 槽位语义已通过对两侧共同定案（2026-10）：
//
//	Kotlin 侧 RunKt$runCore$2$1$1 传入的 4 个 String 顺序：
//	  157: getCacheDir().getAbsolutePath()
//	  167: getDatabasePath("store.db").getAbsolutePath()
//	  181: filesDir.resolve("clash.socket").getAbsolutePath()
//	  UUID.randomUUID().toString()
//	而 CoreAPI.initialize 的外层是 FlatBufferBuilder.startTable(5)，
//	即 4 个 string + 1 个 LegacyDatabases 子表，共 5 槽。
//
//	=> slot0=cacheDir  slot1=store.db  slot2=clash.socket  slot3=token(UUID)
//	   slot4=LegacyDatabases
//
// 注意 token 在 slot3，不要因为 Go 侧 vtable 常量看起来像 off8 就取 slot2：
// 那会把 socket 路径当成 token。联调时优先看下面这行日志。
type InitRequest struct {
	CacheDir     string
	DatabasePath string
	ListenPath   string
	AppToken     string
	Legacy       LegacyDatabases
	Extra        []string // 未被已知槽位消费的字符串，便于发现版本差异
	Slots        int
}

type LegacyDatabases struct {
	FilesPath string
	V21xPath  string
	V23xPath  string
}

func NewCore(tr *Transport) *Core {
	c := &Core{tr: tr, done: make(chan struct{})}
	if tr != nil {
		// 统一 callId 分配器：用 platform 包的（起点 1<<40），
		// 避免 main 与 platform 各持一个序列撞号。
		c.plat = platform.NewClient(platformTransport{tr: tr},
			platform.WithCallIDAlloc(platform.NextCallID))
		c.pf = platform.New(c.plat)
		// 把宿主建 tun 的能力注入 engine（engine 不 import platform，保持解耦）。
		engine.SetTunProvider(func() (int, error) {
			ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
			defer cancel()
			f, err := c.pf.CreateVpn(ctx, &platform.VpnConfigure{RouteSubnets: false})
			if err != nil {
				return -1, err
			}
			if f == nil {
				return -1, nil
			}
			return int(f.Fd()), nil
		})
	}
	return c
}

// Serve 独占读循环。
//
// 只有一个读者：platform.Client.Serve。它内部先尝试把帧配对到 pending 的
// 反向调用（Handle），配不上的 Call 才回调到 onCall，交给 core.dispatch。
// 这样 host→core 的 Call 与 core→host 的响应不会互相抢帧。
func (c *Core) Serve() {
	err := c.plat.Serve(func(f *platform.Frame) {
		m := &Message{
			Type:   MsgType(f.Type),
			CallID: f.CallID,
			Method: f.Method,
			Data:   f.Data,
			FD:     f.FD,
		}
		slog.Debug("recv", "type", m.Type, "callId", m.CallID, "method", m.Method, "len", len(m.Data))
		if derr := c.dispatch(m); derr != nil {
			slog.Error("dispatch failed", "method", m.Method, "err", derr)
			_ = c.reply(&Message{Type: TypeError, CallID: m.CallID, Method: m.Method, Data: []byte(derr.Error())})
			return
		}
		// 未被消费的 fd 必须关掉，否则泄漏。
		if m.FD >= 0 {
			slog.Warn("unconsumed fd on inbound call", "method", m.Method, "fd", m.FD)
			closeFD(m.FD)
		}
	})
	if err != nil {
		slog.Info("bridge read ended", "err", err)
	}
	c.shutdown()
}

func closeFD(fd int) {
	if fd >= 0 {
		_ = syscall.Close(fd)
	}
}

func (c *Core) dispatch(m *Message) error {
	switch m.Method {
	case 0:
		return c.handleInitialize(m)
	case 1:
		return c.handleStart(m)
	case 2:
		return c.handleSendEvent(m)
	default:
		slog.Warn("unhandled method", "method", m.Method, "len", len(m.Data))
		return c.reply(&Message{Type: TypeDone, CallID: m.CallID, Method: m.Method})
	}
}

func (c *Core) handleInitialize(m *Message) error {
	req := decodeInitialize(m.Data)
	c.mu.Lock()
	c.init = req
	c.mu.Unlock()
	slog.Info("initialize",
		"slots", req.Slots,
		"cacheDir", req.CacheDir,
		"databasePath", req.DatabasePath,
		"listenPath", req.ListenPath,
		"appTokenLen", len(req.AppToken),
		"appTokenIsUUID", looksLikeUUID(req.AppToken),
		"legacy", req.Legacy,
		"extra", req.Extra)
	// mihomo 需要 home dir 来定位 GeoIP / cache / 配置落盘。
	if req.Legacy.FilesPath != "" {
		engine.SetHomeDir(req.Legacy.FilesPath)
		// 预置内置 GeoIP：否则含 GEOIP 规则的配置会触发 MMDB 下载，
		// 国内环境可能卡到超时，表现为「导入配置后一直转圈」。
		ensureGeoIP(req.Legacy.FilesPath)
	}
	// DatabasePath 是 store.db 的完整文件路径（含文件名），不是目录。
	// 空串表示不可用 —— engine 会据此让 /profiles 返回 503 而不是空列表，
	// 避免"没接线"被误读成"用户没有配置"。
	if req.DatabasePath != "" {
		engine.SetDatabasePath(req.DatabasePath)
	}
	// 关键：nativeStart 会把子进程的 stdin/stdout/stderr 全部重定向到 /dev/null，
	// 所以真机上内核的日志一行都看不到。initialize 拿到 CacheDir 后立刻镜像一份到文件，
	// 否则装到手机上出问题只能盲猜。
	c.switchLogToFileFor(req)

	// UI 通过 ListenPath 上的 Unix socket 访问内核（原版行为）。
	// 时序上 start 可能先于 initialize 到达，所以这里才真正开监听。
	if req.ListenPath != "" {
		c.unixOnce.Do(func() { serveUnix(c, req.ListenPath) })
	}

	return c.reply(&Message{Type: TypeDone, CallID: m.CallID, Method: m.Method})
}

func (c *Core) handleStart(m *Message) error {
	c.mu.Lock()
	already := c.started
	c.started = true
	c.mu.Unlock()
	if already {
		slog.Warn("start called twice")
	}

	// 必须先回 Done 再启动引擎。
	// 理由：读循环是单线程的（platform.Client.Serve 独占），
	// 如果在这里同步等待 CreateVpn 的响应，那条响应永远进不来 —— 死锁。
	if err := c.reply(&Message{Type: TypeDone, CallID: m.CallID, Method: m.Method}); err != nil {
		return err
	}
	go c.bootEngine()
	return nil
}

// bootEngine 在独立 goroutine 里完成 tun 获取与引擎启动。
func (c *Core) bootEngine() {
	tunFD := -1

	// P0：默认不向宿主要 tun（--tun 打开才要）。
	// 自动要会让不响应 method 7 的对端等满超时，握手测试也会被拖慢。
	if *flagTun && c.pf != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		f, err := c.pf.CreateVpn(ctx, &platform.VpnConfigure{RouteSubnets: false})
		cancel()
		if err != nil {
			slog.Warn("CreateVpn failed; starting without tun", "err", err)
		} else if f != nil {
			tunFD = int(f.Fd())
			slog.Info("tun attached from host", "fd", tunFD)
		}
	}

	if err := engine.Default().Start(nil, tunFD); err != nil {
		slog.Error("engine start failed", "err", err)
		return
	}
	slog.Info("core started", "engine", engine.Default().State(), "tunFD", tunFD)

	go func() {
		if err := ServeAPI(c); err != nil {
			slog.Error("api server exited", "err", err)
		}
	}()
}

// switchLogToFileFor 把日志镜像到 cacheDir 下的文件（真机唯一诊断手段）。
//
// 为什么不用 Android log：logd 需要 JNI 或 CGO，而本项目 CGO_ENABLED=0 是硬约束。
// 为什么不在启动时就写文件：启动时还不知道应用私有目录，只能等 initialize。
func (c *Core) switchLogToFileFor(req InitRequest) {
	dir := req.CacheDir
	if dir == "" {
		dir = req.Legacy.FilesPath
	}
	if dir == "" {
		slog.Warn("no usable dir for log mirror; logs will be lost on device")
		return
	}
	path := filepath.Join(dir, "revived-core.log")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		slog.Warn("cannot open log mirror", "path", path, "err", err)
		return
	}
	lvl := slog.LevelInfo
	if *flagVerbose {
		lvl = slog.LevelDebug
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(io.MultiWriter(os.Stderr, f),
		&slog.HandlerOptions{Level: lvl})))
	slog.Info("log mirror enabled", "path", path, "version", version)
}

// handleSendEvent 处理 method 2。
//
// 证据（reverse-dev 反汇编 ShizukuService$coreJob$1$1.emit）：
//
//	emit(Number) -> intValue() -> FlatBufferBuilder.startTable(1)
//	仅当 code != 0 时写入槽 0 = int32，然后 Bridge.call(2, table, null)
//
// 即「单向事件通知 + 事件码 int32」，但走 call 语义，内核仍须回 Done。
func (c *Core) handleSendEvent(m *Message) error {
	code := int32(0)
	hasCode := false
	if len(m.Data) > 0 {
		fb := NewFB(m.Data)
		if root, err := fb.Root(); err == nil {
			if v, ok := fb.IntField(root, 0); ok {
				code = v
				hasCode = true
			}
		}
	}
	slog.Info("sendEvent", "code", code, "hasCode", hasCode)
	// TODO: 事件码语义未确认（候选：核心状态变化 / 唤醒 / 退出）。
	// 确认后在此映射到具体行为，例如上报 ReportState 或触发重载。
	return c.reply(&Message{Type: TypeDone, CallID: m.CallID, Method: m.Method})
}

// looksLikeUUID 用于联调时快速判定 token 槽：
// 宿主传的是 UUID.randomUUID().toString()，即 36 字符 8-4-4-4-12。
func looksLikeUUID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i, ch := range s {
		switch i {
		case 8, 13, 18, 23:
			if ch != '-' {
				return false
			}
		default:
			isHex := (ch >= '0' && ch <= '9') || (ch >= 'a' && ch <= 'f') || (ch >= 'A' && ch <= 'F')
			if !isHex {
				return false
			}
		}
	}
	return true
}

func (c *Core) reply(m *Message) error {
	if c.tr == nil {
		return nil
	}
	return c.tr.WriteMessage(m)
}

func (c *Core) ServeStandalone() {
	slog.Info("standalone mode: serving API without Bridge")
	if err := ServeAPI(c); err != nil {
		slog.Error("api server exited", "err", err)
	}
	c.shutdown()
}

func (c *Core) Wait() {
	<-c.done
}

// shutdown 关闭通道 fd 后再放行 Wait。
//
// 顺序很重要：Serve 阻塞在 platform.Client.Serve -> ReadFrame 上，
// 而 platform.Client 的 Done() 只关 Client 不关 Transport 的 fd。
// 如果不先关 fd，读循环会一直阻塞在 ReadFrame 等下一帧，
// 表现为「内核收到退出信号却不退」，且不报错——比 panic 难查。
func (c *Core) shutdown() {
	c.closeOnce.Do(func() {
		if c.tr != nil {
			_ = c.tr.Close()
		}
		close(c.done)
	})
}

// decodeInitialize 解 4 个 string + LegacyDatabases 子表，按已定案的槽位语义赋值。
func decodeInitialize(payload []byte) InitRequest {
	out := InitRequest{}
	if len(payload) < 8 {
		slog.Warn("initialize payload too short", "len", len(payload))
		return out
	}
	fb := NewFB(payload)
	root, err := fb.Root()
	if err != nil {
		slog.Warn("initialize: bad root", "err", err)
		return out
	}
	slots, err := fb.NumSlots(root)
	if err != nil {
		slog.Warn("initialize: bad vtable", "err", err)
		return out
	}
	out.Slots = slots

	assign := map[int]*string{
		0: &out.CacheDir,
		1: &out.DatabasePath,
		2: &out.ListenPath,
		3: &out.AppToken,
	}
	for slot := 0; slot < slots && slot < 8; slot++ {
		abs, err := fb.fieldOffset(root, slot)
		if err != nil || abs < 0 {
			continue
		}
		// 子表与字符串都存一个 uoffset，先探测是不是 table。
		if sub, err := fb.TableField(root, slot); err == nil && sub > 0 && fb.LooksLikeTable(sub) {
			out.Legacy = LegacyDatabases{
				FilesPath: fb.StringField(sub, 0),
				V21xPath:  fb.StringField(sub, 1),
				V23xPath:  fb.StringField(sub, 2),
			}
			continue
		}
		s := fb.StringField(root, slot)
		if s == "" {
			continue
		}
		if dst, ok := assign[slot]; ok {
			*dst = s
		} else {
			out.Extra = append(out.Extra, s)
		}
	}
	return out
}
