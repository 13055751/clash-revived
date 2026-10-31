package platform

// methods.go — core → host 的 13 项 Platform 能力。
//
// method 编号双向确认：
//   - Go 侧：libclash.so 里每个 platform.Xxx 在 bl platform.callRemote 前把 method
//     立即数写进 x2（例如 CreateVpn 是 0xd16c74 `orr x2, xzr, #0x7`）
//   - Kotlin 侧：core/Platform（被 R8 改名成 Lkotlin/UnsignedKt;）的
//     call$suspendImpl 里有两个 13 分支 packed-switch，method 0..12 一一对应
//
// StartBridge / InstallCoreAPI / Done 不在 0..12 这条分发里。
// 全二进制扫描：callRemote 的 bl 调用点只有 13 个；Transport.WriteMessage 的调用点
// 只有 3 个（callRemote 内 2 个 + handleRemoteCall 内 1 个）。
// 因此 InstallCoreAPI / Done 不可能发任何帧——它们是纯本地动作（见各自注释）。

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"time"
)

// 方向：core → host。编号已确认。
const (
	MethodGetFeatures        int32 = 0
	MethodGetTimeZone        int32 = 1
	MethodSendNotification   int32 = 2
	MethodAcquireAliveLock   int32 = 3
	MethodReleaseAliveLock   int32 = 4
	MethodReportState        int32 = 5
	MethodSetSystemProxy     int32 = 6
	MethodCreateVpn          int32 = 7 // ★ 关键路径
	MethodReleaseVpn         int32 = 8
	MethodProtectSocket      int32 = 9
	MethodQueryConnectionOwn int32 = 10
	MethodScheduleWakeup     int32 = 11
	MethodCreateAppSocket    int32 = 12
)

// MethodName 给日志用。
func MethodName(m int32) string {
	switch m {
	case MethodGetFeatures:
		return "GetFeatures"
	case MethodGetTimeZone:
		return "GetTimeZone"
	case MethodSendNotification:
		return "SendNotification"
	case MethodAcquireAliveLock:
		return "AcquireAliveLock"
	case MethodReleaseAliveLock:
		return "ReleaseAliveLock"
	case MethodReportState:
		return "ReportState"
	case MethodSetSystemProxy:
		return "SetSystemProxy"
	case MethodCreateVpn:
		return "CreateVpn"
	case MethodReleaseVpn:
		return "ReleaseVpn"
	case MethodProtectSocket:
		return "ProtectSocket"
	case MethodQueryConnectionOwn:
		return "QueryConnectionOwner"
	case MethodScheduleWakeup:
		return "ScheduleWakeup"
	case MethodCreateAppSocket:
		return "CreateAppSocket"
	}
	return fmt.Sprintf("Method(%d)", m)
}

// ---------- 报文类型 ----------

// Features 是 method 0 的响应（PlatformFeatures 表）。
//
// 已确认：Go 侧生成的 reader 有 8 个 bool 访问器
// （Vpn / SystemProxy / VpnWithSystemProxy / ScheduleWakeup / ReportState /
// QueryConnectionOwner / CreateAppSocket / AliveLock）。
// 未确认：8 个 bool 各自的 slot 序。下面按 slot 0..7 原样收，不猜名字。
type Features struct {
	Flags   [8]bool
	Present [8]bool
	Raw     []byte
}

// VpnSupported 是「宿主能建 TUN」的粗判据（任一 vpn 相关位为真）。
// 联调期它只用于日志，不用于分支。
func (f *Features) VpnSupported() bool {
	if f == nil {
		return false
	}
	for i := range f.Flags {
		if f.Present[i] && f.Flags[i] {
			return true
		}
	}
	return false
}

// TimeZone 是 method 1 的响应（TimeZone 表）。
// 已确认（reverse-dev 读 host 侧写入端）：slot0 = id(string)、slot1 = rawOffset(**long**)。
type TimeZone struct {
	ID        string
	RawOffset int64
}

// Notification 是 method 2 的请求（Notification 表）。
// 已确认（Go 写端槽位序）：slot0=int8、slot1/2/3=string。
// 推断：slot0 是状态枚举（CoreState/Stage），三个 string 是 title/content/channel。
type Notification struct {
	State   int8
	Title   string
	Content string
	Channel string
}

// LockInfo 是 method 3 的请求（LockInfo 表）。
// 已确认：Go 写端只有 slot0=int8；Kotlin 侧生成的 LockInfo 类同样只暴露一个字段。
type LockInfo struct {
	Kind int8
}

// Traffic 是 CoreState 里的内联 struct：4×int64。
// 已确认：Kotlin 侧按 bb_pos+0/8/16/24 读 4 个 long 并塞进 CoreStatus$Traffic(J,J,J,J)。
// 推断：语义依次是 upload / download / uploadTotal / downloadTotal。
type Traffic struct {
	Upload, Download, UploadTotal, DownloadTotal int64
}

// CoreState 是 method 5 的请求（CoreState 表）。
// 已确认：Go 写端 CreateString → PrependUOffsetTSlot(slot=1) → CoreStateEnd，
// 另有一个内联 struct 槽（PrependStructSlot，被内联在 disasm 里没单独出现）。
// Kotlin reader：getTraffic()（struct，slot0）+ getProfileName()（string，slot1）。
type CoreState struct {
	ProfileName string
	Traffic     Traffic
}

// SystemProxyConfig 是 method 6 的请求，也是 VpnConfigure 的 slot1 子表。
//
// 已确认（读取端逐字段字节码，见 work/PROTOCOL_REMAINING.md 1.2）：
// slot0 = bool enabled、slot1 = string host、slot2 = uint16 port、slot3 = [string] excludes。
// 宿主用它构造 ProxyInfo.buildDirectProxy(host, port, excludes)。
type SystemProxyConfig struct {
	Enabled  bool
	Host     string
	Port     uint16
	Excludes []string
}

// VpnAccessControl 是 VpnConfigure 的 slot2 子表（宿主里的 VPN 配置）。
//
// 已确认：slot0 = bool、slot1 = [string] allowedApplications；
// 列表为空时宿主只放行自身包名。
// 未确认：slot0 的确切语义（推断为「按列表放行」的开关）。
type VpnAccessControl struct {
	Enabled   bool
	AllowApps []string
}

// VpnConfigure 是 method 7 的请求根表。
//
// 已确认（读取端逐字段字节码 + Go 写端 builder 序列双向吻合）：
//
//	slot0 = bool  路由模式：false → addRoute("0.0.0.0", 0)
//	                         true  → 按宿主 R.array 子网列表逐条 addRoute，
//	                                 列表为空时退回 addRoute("172.19.0.2", 32)
//	slot1 = 子表 SystemProxyConfig
//	slot2 = 子表 VpnAccessControl
//
// 宿主硬编码（内核不用管）：172.19.0.1/30、dns 172.19.0.2、mtu 9000、establish()。
type VpnConfigure struct {
	RouteSubnets  bool
	SystemProxy   *SystemProxyConfig
	AccessControl *VpnAccessControl
}

// ConnectionInfo 是 method 10 的请求（ConnectionInfo 表）。
// 已确认（Go 写端槽位序）：slot0=int8、slot1=string、slot2=int32、slot3=string、slot4=int32。
// 推断语义：协议 / 源地址 / 源端口 / 目的地址 / 目的端口。
type ConnectionInfo struct {
	Protocol   int8
	Source     string
	SourcePort int32
	Dest       string
	DestPort   int32
}

// AppSocketSpec 是 method 12 的请求（AppSocketSpec 表）。
// 已确认：3 个 int32 槽（slot0/1/2）。
// 推断语义：domain / type / protocol（socket(2) 的三个参数）。
type AppSocketSpec struct {
	Domain   int32
	Type     int32
	Protocol int32
}

// ---------- 编码 ----------

func encodeFeatures() []byte { return nil }

func encodeTimeZone() []byte { return nil }

func encodeNotification(n *Notification) []byte {
	b := newFBB(128)
	s3 := b.createString(n.Channel)
	s2 := b.createString(n.Content)
	s1 := b.createString(n.Title)
	b.startObject(4)
	b.prependI8Slot(0, n.State, 0)
	b.prependUOffsetSlot(3, s3)
	b.prependUOffsetSlot(2, s2)
	b.prependUOffsetSlot(1, s1)
	return b.finish(b.endObject())
}

func encodeLockInfo(l *LockInfo) []byte {
	b := newFBB(64)
	b.startObject(1)
	b.prependI8Slot(0, l.Kind, 0)
	return b.finish(b.endObject())
}

// encodeCoreState 按 method 5 的形状写：slot0 = Traffic 结构、slot2 = profileName。
// 注意 Kotlin 的 core/CoreState 类在不同 method 里被读成不同形状（见 PROTOCOL_REMAINING 1.3），
// 所以这里刻意不复用 VpnConfigure 的写法。
func encodeCoreState(s *CoreState) []byte {
	b := newFBB(128)
	name := b.createString(s.ProfileName)
	b.startObject(3)
	b.prependStructI64x4Slot(0, [4]int64{s.Traffic.Upload, s.Traffic.Download, s.Traffic.UploadTotal, s.Traffic.DownloadTotal})
	b.prependUOffsetSlot(2, name)
	return b.finish(b.endObject())
}

func encodeSystemProxy(c *SystemProxyConfig) []byte {
	b := newFBB(128)
	host := b.createString(c.Host)
	excludes := b.createStringVector(c.Excludes)
	b.startObject(4)
	b.prependBoolSlot(0, c.Enabled, false)
	b.prependUOffsetSlot(1, host)
	b.prependU16Slot(2, c.Port, 0)
	b.prependUOffsetSlot(3, excludes)
	return b.finish(b.endObject())
}

func encodeVpnConfigure(c *VpnConfigure) []byte {
	if c == nil {
		c = &VpnConfigure{}
	}
	b := newFBB(256)

	// 嵌套表必须先写（FlatBuffers 反向构建）。
	var ac, sp int
	if c.AccessControl != nil {
		apps := b.createStringVector(c.AccessControl.AllowApps)
		b.startObject(2)
		b.prependBoolSlot(0, c.AccessControl.Enabled, false)
		b.prependUOffsetSlot(1, apps)
		ac = b.endObject()
	}
	if c.SystemProxy != nil {
		host := b.createString(c.SystemProxy.Host)
		excludes := b.createStringVector(c.SystemProxy.Excludes)
		b.startObject(4)
		b.prependBoolSlot(0, c.SystemProxy.Enabled, false)
		b.prependUOffsetSlot(1, host)
		b.prependU16Slot(2, c.SystemProxy.Port, 0)
		b.prependUOffsetSlot(3, excludes)
		sp = b.endObject()
	}

	b.startObject(3)
	b.prependBoolSlot(0, c.RouteSubnets, false)
	b.prependUOffsetSlot(1, sp)
	b.prependUOffsetSlot(2, ac)
	return b.finish(b.endObject())
}

func encodeConnectionInfo(ci *ConnectionInfo) []byte {
	b := newFBB(160)
	dest := b.createString(ci.Dest)
	src := b.createString(ci.Source)
	b.startObject(5)
	b.prependI8Slot(0, ci.Protocol, 0)
	b.prependUOffsetSlot(1, src)
	b.prependI32Slot(2, ci.SourcePort, 0)
	b.prependUOffsetSlot(3, dest)
	b.prependI32Slot(4, ci.DestPort, 0)
	return b.finish(b.endObject())
}

func encodeAppSocketSpec(s *AppSocketSpec) []byte {
	b := newFBB(64)
	b.startObject(3)
	b.prependI32Slot(0, s.Domain, 0)
	b.prependI32Slot(1, s.Type, 0)
	b.prependI32Slot(2, s.Protocol, 0)
	return b.finish(b.endObject())
}

func encodeScheduleWakeup(at time.Time) []byte {
	b := newFBB(64)
	b.startObject(1)
	b.prependI64Slot(0, at.UnixMilli(), 0)
	return b.finish(b.endObject())
}

// ---------- 解码 ----------

func decodeFeatures(payload []byte) *Features {
	f := &Features{Raw: append([]byte(nil), payload...)}
	if len(payload) < 8 {
		return f
	}
	r := newFBR(payload)
	root, ok := r.root()
	if !ok {
		return f
	}
	for slot := 0; slot < 8; slot++ {
		if v, ok := r.boolField(root, slot); ok {
			f.Flags[slot], f.Present[slot] = v, true
		}
	}
	return f
}

func decodeTimeZone(payload []byte) (*TimeZone, error) {
	tz := &TimeZone{}
	if len(payload) < 8 {
		return tz, errShort("TimeZone", len(payload))
	}
	r := newFBR(payload)
	root, ok := r.root()
	if !ok {
		return tz, fmt.Errorf("platform: TimeZone payload has no root table")
	}
	if id, ok := r.stringField(root, 0); ok {
		tz.ID = id
	}
	if off, ok := r.i64Field(root, 1); ok {
		tz.RawOffset = off
	}
	return tz, nil
}

// ---------- Platform ----------

// Platform 是 16 项能力的门面（13 项走线上，3 项纯本地）。
type Platform struct {
	c   *Client
	log *slog.Logger

	mu  sync.Mutex
	vpn *os.File // 当前持有的 tun fd；由 Platform 拥有，重复 CreateVpn 复用

	coreAPI   any
	done      chan struct{}
	doneOnce  sync.Once
	closeHook func()
}

func New(c *Client, opts ...PlatformOption) *Platform {
	p := &Platform{c: c, log: slog.Default(), done: make(chan struct{})}
	for _, o := range opts {
		o(p)
	}
	return p
}

type PlatformOption func(*Platform)

// WithPlatformLogger 注入日志器。
func WithPlatformLogger(l *slog.Logger) PlatformOption { return func(p *Platform) { p.log = l } }

// GetFeatures 询问宿主支持的能力集（method 0，空载荷）。
func (p *Platform) GetFeatures(ctx context.Context) (*Features, error) {
	rep, err := p.c.Call(ctx, MethodGetFeatures, encodeFeatures())
	if err != nil {
		return nil, err
	}
	return decodeFeatures(rep.Data), nil
}

// GetTimeZone 取宿主的时区（method 1，空载荷）。
// 承载不了时调用方应退回 time.Local。
func (p *Platform) GetTimeZone(ctx context.Context) (*TimeZone, error) {
	rep, err := p.c.Call(ctx, MethodGetTimeZone, encodeTimeZone())
	if err != nil {
		return nil, err
	}
	return decodeTimeZone(rep.Data)
}

// SendNotification 请求宿主更新状态栏通知（method 2）。
func (p *Platform) SendNotification(ctx context.Context, n *Notification) error {
	_, err := p.c.Call(ctx, MethodSendNotification, encodeNotification(n))
	return err
}

// AcquireAliveLock 申请前台保活锁（method 3），返回宿主给的 token。
//
// 响应载荷：Kotlin 侧在 acquireAliveLock 后把 UUID 字符串塞回 ByteBuffer
// （Platform.call 的同一个分支里，releaseAliveLock 用同一套编解码）。
// 这里按 UTF-8 文本收；若宿主回空载荷则返回空 token（仍算成功）。
func (p *Platform) AcquireAliveLock(ctx context.Context, info *LockInfo) (string, error) {
	rep, err := p.c.Call(ctx, MethodAcquireAliveLock, encodeLockInfo(info))
	if err != nil {
		return "", err
	}
	return string(rep.Data), nil
}

// ReleaseAliveLock 释放保活锁（method 4）。
//
// 载荷已确认是「裸 UTF-8 UUID 字符串」，不是 FlatBuffers：
//   - Go 侧 uuid.UUID.String() → runtime.stringtoslicebyte → callRemote（无 builder）
//   - Kotlin 侧 Charset.decode(ByteBuffer).toString() → UUID.fromString
func (p *Platform) ReleaseAliveLock(ctx context.Context, token string) error {
	_, err := p.c.Call(ctx, MethodReleaseAliveLock, []byte(token))
	return err
}

// ReportState 上报内核状态（method 5）。
func (p *Platform) ReportState(ctx context.Context, st *CoreState) error {
	_, err := p.c.Call(ctx, MethodReportState, encodeCoreState(st))
	return err
}

// SetSystemProxy 设置/清除系统代理（method 6）。
func (p *Platform) SetSystemProxy(ctx context.Context, cfg *SystemProxyConfig) error {
	_, err := p.c.Call(ctx, MethodSetSystemProxy, encodeSystemProxy(cfg))
	return err
}

// CreateVpn 请宿主建立 TUN 并拿回 fd（method 7）—— 关键路径。
//
// 响应：宿主把 tun fd 通过 SCM_RIGHTS 附在 Done 帧上，落在 Reply.FD。
//
// 生命周期约定：
//   - 返回的 *os.File 归 Platform 所有，调用方只借不关（平台会在 ReleaseVpn/Close 时关）
//   - 重复调用是幂等的：已经建好就复用，不会再发一次 CreateVpn（避免 fd 泄漏）
//   - 任何错误路径都会把已经收到的 fd 关掉
func (p *Platform) CreateVpn(ctx context.Context, cfg *VpnConfigure) (*os.File, error) {
	p.mu.Lock()
	if p.vpn != nil {
		f := p.vpn
		p.mu.Unlock()
		p.log.Debug("platform: CreateVpn reused existing tun fd", "fd", f.Fd())
		return f, nil
	}
	p.mu.Unlock()

	rep, err := p.c.Call(ctx, MethodCreateVpn, encodeVpnConfigure(cfg))
	if err != nil {
		return nil, fmt.Errorf("platform: CreateVpn: %w", err)
	}
	if rep.FD < 0 {
		return nil, fmt.Errorf("platform: CreateVpn returned no fd (payload %d bytes)", len(rep.Data))
	}

	// 宿主侧给的 fd 可能带 O_NONBLOCK（ParcelFileDescriptor 常见），这里清掉，
	// 否则后续 tun 读会 EAGAIN。
	if err := setBlocking(rep.FD); err != nil {
		closefd(rep.FD)
		return nil, fmt.Errorf("platform: CreateVpn: fd %d not usable: %w", rep.FD, err)
	}
	f := os.NewFile(uintptr(rep.FD), "tun")
	if f == nil {
		closefd(rep.FD)
		return nil, fmt.Errorf("platform: CreateVpn: os.NewFile(%d) failed", rep.FD)
	}

	p.mu.Lock()
	if p.vpn != nil { // 并发重复调用：保留先到的那一个，把自己多拿到的关掉
		p.mu.Unlock()
		_ = f.Close()
		p.mu.Lock()
		f = p.vpn
	}
	if p.vpn == nil {
		p.vpn = f
	}
	p.mu.Unlock()

	p.log.Info("platform: tun attached", "fd", f.Fd())
	return f, nil
}

// TunFile 返回当前 tun 文件；没有则 nil（不转移所有权）。
func (p *Platform) TunFile() *os.File {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.vpn
}

// ReleaseVpn 释放 TUN（method 8，空载荷），并关掉本地 fd。
func (p *Platform) ReleaseVpn(ctx context.Context) error {
	p.mu.Lock()
	f := p.vpn
	p.vpn = nil
	p.mu.Unlock()

	if f != nil {
		if err := f.Close(); err != nil {
			p.log.Debug("platform: close tun fd failed", "err", err)
		}
	}
	_, err := p.c.Call(ctx, MethodReleaseVpn, nil)
	return err
}

// ProtectSocket 让宿主对新 socket 调 VpnService.protect(fd)，防路由回环（method 9）。
//
// 载荷为空；fd 通过 SCM_RIGHTS 附带。
// 原版会先 dup 一份再交给宿主（头文件里看到 unix.Dup → os.NewFile → callRemote），
// 这里做同样的事：写出去之后立刻关掉自己这份 dup。
func (p *Platform) ProtectSocket(ctx context.Context, fd int) error {
	dup, err := dupFD(fd)
	if err != nil {
		return fmt.Errorf("platform: ProtectSocket: dup(%d): %w", fd, err)
	}
	_, err = p.c.CallWithFD(ctx, MethodProtectSocket, nil, dup)
	// SCM_RIGHTS 是复制语义，发送之后本地这份必须自己关。
	closefd(dup)
	if err != nil {
		return fmt.Errorf("platform: ProtectSocket(fd=%d): %w", fd, err)
	}
	return nil
}

// QueryConnectionOwner 查连接的归属（method 10），返回宿主给的字符串
// （包名或 uid，Android 10+ 受权限限制可能返回空）。
//
// ⚠️ 未确认：Go 侧写的根表是 5 槽（int8/string/int32/string/int32，收尾于
// ConnectionInfoEnd），但 Kotlin 侧 method 10 分支读的是 CoreState 形状
// （f0/f1 bool、f2 int uid）。两侧对不上，这里暂时按 Go 侧的 5 槽发送。
// 联调若报错，第一件事就是换形状重试，详见 work/PLATFORM_RPC.md 2.2。
func (p *Platform) QueryConnectionOwner(ctx context.Context, ci *ConnectionInfo) (string, error) {
	rep, err := p.c.Call(ctx, MethodQueryConnectionOwn, encodeConnectionInfo(ci))
	if err != nil {
		return "", err
	}
	return string(rep.Data), nil
}

// ScheduleWakeup 请求宿主定时唤醒（method 11）。
func (p *Platform) ScheduleWakeup(ctx context.Context, at time.Time) error {
	_, err := p.c.Call(ctx, MethodScheduleWakeup, encodeScheduleWakeup(at))
	return err
}

// CreateAppSocket 请宿主代建 socket（method 12），fd 通过 SCM_RIGHTS 回传。
func (p *Platform) CreateAppSocket(ctx context.Context, spec *AppSocketSpec) (*os.File, error) {
	rep, err := p.c.Call(ctx, MethodCreateAppSocket, encodeAppSocketSpec(spec))
	if err != nil {
		return nil, err
	}
	if rep.FD < 0 {
		return nil, fmt.Errorf("platform: CreateAppSocket returned no fd")
	}
	if err := setBlocking(rep.FD); err != nil {
		closefd(rep.FD)
		return nil, fmt.Errorf("platform: CreateAppSocket: fd %d not usable: %w", rep.FD, err)
	}
	f := os.NewFile(uintptr(rep.FD), "app-socket")
	if f == nil {
		closefd(rep.FD)
		return nil, fmt.Errorf("platform: CreateAppSocket: os.NewFile(%d) failed", rep.FD)
	}
	return f, nil
}

// InstallCoreAPI 向内核注册 CoreAPI 实现。
//
// 已确认：不发任何帧。全二进制里 Transport.WriteMessage 只有 3 个调用点
// （callRemote ×2 + handleRemoteCall ×1），platform.InstallCoreAPI 一个都不沾；
// 它被内联进 platform.StartBridge 的 sync.Once 闭包里，只做本地 map/chan 初始化。
func (p *Platform) InstallCoreAPI(api any) {
	p.mu.Lock()
	p.coreAPI = api
	p.mu.Unlock()
}

// CoreAPI 返回已注册的 CoreAPI 实现（可能为 nil）。
func (p *Platform) CoreAPI() any {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.coreAPI
}

// Done 通知内核退出。
//
// 已确认：不发帧（同上，Done 不在 callRemote 调用点里）。
// 这里只做本地收尾：标记关闭、唤醒等待方、可选地关掉传输。
func (p *Platform) Done() {
	p.doneOnce.Do(func() {
		close(p.done)
		p.c.Close()
		p.mu.Lock()
		hook := p.closeHook
		p.mu.Unlock()
		if hook != nil {
			hook()
		}
	})
}

// Wait 等待 Done（或 CloseAll）。
func (p *Platform) Wait() { <-p.done }

// SetCloseHook 注册 Done 时执行的收尾动作（一般是关掉 Transport fd）。
func (p *Platform) SetCloseHook(f func()) {
	p.mu.Lock()
	p.closeHook = f
	p.mu.Unlock()
}

// CloseAll 关掉 tun 与 Client（defer 用）。
func (p *Platform) CloseAll() {
	p.mu.Lock()
	f := p.vpn
	p.vpn = nil
	p.mu.Unlock()
	if f != nil {
		_ = f.Close()
	}
	p.Done()
}
