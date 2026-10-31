package platform

// client_test.go — 用内存 socketpair 模拟宿主端，验证反向调用的线路行为。
//
// 这里刻意不引 engine 包（编 mihomo 很慢、也占 CPU），全部是纯内存测试。
// 覆盖点：请求编码、callId 分配、乱序响应配对、超时 + Cancel、Error 帧、
// SCM_RIGHTS 收 fd、fd 不泄漏、重复 CreateVpn 幂等、Serve 模式路由 host→core。

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

const (
	testHeaderSize = 20
	testMaxPayload = 8 << 20
)

// ---------- 测试用传输 ----------

type fdTransport struct {
	fd  int
	wmu sync.Mutex
}

func newFDTransport(fd int) *fdTransport { return &fdTransport{fd: fd} }

func (t *fdTransport) ReadFrame() (*Frame, error) {
	buf := make([]byte, testHeaderSize+testMaxPayload)
	oob := make([]byte, syscall.CmsgSpace(4*4))
	n, oobn, _, _, err := syscall.Recvmsg(t.fd, buf, oob, 0)
	if err != nil {
		return nil, err
	}
	if n < testHeaderSize {
		return nil, errors.New("short frame")
	}
	f := &Frame{
		Type:   int32(be32(buf[0:4])),
		CallID: int64(be64(buf[4:12])),
		Method: int32(be32(buf[12:16])),
		FD:     -1,
	}
	length := int(int32(be32(buf[16:20])))
	if length > 0 && length <= n-testHeaderSize {
		f.Data = append([]byte(nil), buf[testHeaderSize:testHeaderSize+length]...)
	}
	if oobn > 0 {
		if scms, e := syscall.ParseSocketControlMessage(oob[:oobn]); e == nil {
			for i := range scms {
				if fds, e2 := syscall.ParseUnixRights(&scms[i]); e2 == nil && len(fds) > 0 {
					f.FD = fds[0]
					break
				}
			}
		}
	}
	return f, nil
}

func (t *fdTransport) WriteFrame(f *Frame) error {
	t.wmu.Lock()
	defer t.wmu.Unlock()
	frame := make([]byte, testHeaderSize+len(f.Data))
	put32(frame[0:4], uint32(f.Type))
	put64(frame[4:12], uint64(f.CallID))
	put32(frame[12:16], uint32(f.Method))
	put32(frame[16:20], uint32(len(f.Data)))
	copy(frame[testHeaderSize:], f.Data)
	var oob []byte
	if f.FD >= 0 {
		oob = syscall.UnixRights(f.FD)
	}
	return syscall.Sendmsg(t.fd, frame, oob, nil, 0)
}

func be32(b []byte) uint32 {
	return uint32(b[0])<<24 | uint32(b[1])<<16 | uint32(b[2])<<8 | uint32(b[3])
}
func be64(b []byte) uint64 { return uint64(be32(b[0:4]))<<32 | uint64(be32(b[4:8])) }
func put32(b []byte, v uint32) {
	b[0], b[1], b[2], b[3] = byte(v>>24), byte(v>>16), byte(v>>8), byte(v)
}
func put64(b []byte, v uint64) { put32(b[0:4], uint32(v>>32)); put32(b[4:8], uint32(v)) }

// pair 建一条 SOCK_SEQPACKET socketpair。
func pair(t *testing.T) (client, host int) {
	t.Helper()
	fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_SEQPACKET, 0)
	if err != nil {
		t.Fatalf("socketpair: %v", err)
	}
	// 故意不关：测试里的读写 goroutine 在测试函数返回后仍可能再调一次 recvmsg，
	// 如果 fd 号被下一个用例复用，就会串包（实测踩过）。
	// 一个测试进程多几个常驻 fd 无所谓，进程退出时全部回收。
	return fds[0], fds[1]
}

// fakeHost 在 goroutine 里跑一个极简宿主。
type fakeHost struct {
	got   chan *Frame
	done  chan struct{}
	tr    *fdTransport
	reply func(f *Frame, tr *fdTransport) bool // 返回 false 表示停止
}

func startHost(t *testing.T, fd int, reply func(f *Frame, tr *fdTransport) bool) *fakeHost {
	t.Helper()
	h := &fakeHost{
		got:   make(chan *Frame, 32),
		done:  make(chan struct{}),
		tr:    newFDTransport(fd),
		reply: reply,
	}
	go func() {
		defer close(h.done)
		for {
			f, err := h.tr.ReadFrame()
			if err != nil {
				return
			}
			select {
			case h.got <- f:
			default:
			}
			if h.reply != nil && !h.reply(f, h.tr) {
				return
			}
		}
	}()
	return h
}

func (h *fakeHost) next(t *testing.T, within time.Duration) *Frame {
	t.Helper()
	select {
	case f := <-h.got:
		return f
	case <-time.After(within):
		t.Fatalf("host did not receive a frame within %v", within)
		return nil
	}
}

// waitNoMore 断言在 d 之内宿主没再收到帧。
func (h *fakeHost) waitNoMore(t *testing.T, d time.Duration) {
	t.Helper()
	select {
	case f := <-h.got:
		t.Fatalf("unexpected extra frame: type=%s method=%d", TypeName(f.Type), f.Method)
	case <-time.After(d):
	}
}

func countFDs(t *testing.T) int {
	t.Helper()
	ents, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Skipf("/proc/self/fd unavailable: %v", err)
	}
	return len(ents)
}

// ---------- 测试 ----------

func TestCallEncodesRequestAndGetsFD(t *testing.T) {
	cfd, hfd := pair(t)
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	defer pw.Close()
	defer pr.Close()

	client := NewClient(newFDTransport(cfd))
	client.Start(nil)

	h := startHost(t, hfd, func(f *Frame, tr *fdTransport) bool {
		if f.Type != TypeCall || f.Method != MethodCreateVpn {
			t.Errorf("host got type=%s method=%d, want Call/CreateVpn", TypeName(f.Type), f.Method)
			return false
		}
		if f.CallID < 1<<40 {
			t.Errorf("callId = %d, want >= 1<<40 (避开宿主自增序列)", f.CallID)
		}
		// 请求载荷必须是能解出 mode 的 VpnConfigure 根表
		r := newFBR(f.Data)
		root, ok := r.root()
		if !ok {
			t.Errorf("CreateVpn payload has no root table (%d bytes)", len(f.Data))
			return false
		}
		if mode, ok := r.boolField(root, 0); !ok || !mode {
			t.Errorf("payload slot0 = %v (present=%v), want routeSubnets=true", mode, ok)
		}
		// 回 Done，并把 tun fd 通过 SCM_RIGHTS 附上
		_ = tr.WriteFrame(&Frame{Type: TypeDone, CallID: f.CallID, Method: f.Method, FD: int(pr.Fd())})
		return true
	})

	f, err := client.CreateVpn0(context.Background())
	if err != nil {
		t.Fatalf("CreateVpn: %v", err)
	}
	defer f.Close()

	if _, err := pw.Write([]byte("tun-data")); err != nil {
		t.Fatalf("write pipe: %v", err)
	}
	got := make([]byte, 8)
	if _, err := f.Read(got); err != nil {
		t.Fatalf("read from returned tun fd: %v", err)
	}
	if string(got) != "tun-data" {
		t.Errorf("read %q, want tun-data", got)
	}

	_ = h
}

// CreateVpn0 是测试专用薄封装：等价于 Platform.CreateVpn 但不带 VpnConfigure。
func (c *Client) CreateVpn0(ctx context.Context) (*os.File, error) {
	rep, err := c.Call(ctx, MethodCreateVpn, encodeVpnConfigure(&VpnConfigure{RouteSubnets: true}))
	if err != nil {
		return nil, err
	}
	if rep.FD < 0 {
		return nil, errors.New("no fd in reply")
	}
	if err := setBlocking(rep.FD); err != nil {
		closefd(rep.FD)
		return nil, err
	}
	f := os.NewFile(uintptr(rep.FD), "tun")
	if f == nil {
		closefd(rep.FD)
		return nil, errors.New("NewFile failed")
	}
	return f, nil
}

func TestOutOfOrderResponses(t *testing.T) {
	cfd, hfd := pair(t)
	client := NewClient(newFDTransport(cfd))
	client.Start(nil)

	var mu sync.Mutex
	var held []*Frame

	h := startHost(t, hfd, func(f *Frame, tr *fdTransport) bool {
		mu.Lock()
		held = append(held, f)
		n := len(held)
		mu.Unlock()
		if n == 2 {
			// 故意反序回包；载荷写成「该帧自己的 method 名」，
			// 这样断言与到达顺序无关（并发调度在负载高时不保证谁先发）。
			_ = tr.WriteFrame(&Frame{Type: TypeDone, CallID: held[1].CallID, Method: held[1].Method,
				Data: []byte(MethodName(held[1].Method))})
			_ = tr.WriteFrame(&Frame{Type: TypeDone, CallID: held[0].CallID, Method: held[0].Method,
				Data: []byte(MethodName(held[0].Method))})
		}
		return true
	})
	_ = h

	type res struct {
		method int32
		body   string
		err    error
	}
	// 两个调用并发发出，不依赖谁先到宿主
	out := make(chan res, 2)
	go func() {
		rep, err := client.Call(context.Background(), MethodGetTimeZone, nil)
		if err != nil {
			out <- res{MethodGetTimeZone, "", err}
			return
		}
		out <- res{MethodGetTimeZone, string(rep.Data), nil}
	}()
	go func() {
		rep, err := client.Call(context.Background(), MethodGetFeatures, nil)
		if err != nil {
			out <- res{MethodGetFeatures, "", err}
			return
		}
		out <- res{MethodGetFeatures, string(rep.Data), nil}
	}()

	got := map[int32]string{}
	for i := 0; i < 2; i++ {
		select {
		case r := <-out:
			if r.err != nil {
				t.Fatalf("call %s failed: %v", MethodName(r.method), r.err)
			}
			got[r.method] = r.body
		case <-time.After(5 * time.Second):
			t.Fatalf("timed out waiting for calls")
		}
	}
	if got[MethodGetTimeZone] != MethodName(MethodGetTimeZone) || got[MethodGetFeatures] != MethodName(MethodGetFeatures) {
		t.Errorf("乱序响应配错了: %v", got)
	}
}

func TestTimeoutSendsCancel(t *testing.T) {
	cfd, hfd := pair(t)
	client := NewClient(newFDTransport(cfd), WithTimeout(120*time.Millisecond))
	client.Start(nil)

	h := startHost(t, hfd, func(f *Frame, tr *fdTransport) bool {
		return true // 什么都不回，逼客户端超时
	})

	start := time.Now()
	_, err := client.Call(context.Background(), MethodGetFeatures, nil)
	if err == nil {
		t.Fatalf("expected timeout error")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want DeadlineExceeded", err)
	}
	if d := time.Since(start); d > time.Second {
		t.Errorf("timeout took %v", d)
	}

	first := h.next(t, 3*time.Second)
	if first.Method != MethodGetFeatures || first.Type != TypeCall {
		t.Fatalf("first frame = %s/%s", TypeName(first.Type), MethodName(first.Method))
	}
	// 超时后必须补发 Cancel，且 callId 相同
	cancel := h.next(t, 3*time.Second)
	if cancel.Type != TypeCancel {
		t.Fatalf("second frame type = %s, want Cancel", TypeName(cancel.Type))
	}
	if cancel.CallID != first.CallID {
		t.Errorf("cancel callId = %d, want %d", cancel.CallID, first.CallID)
	}
	if n := client.Pending(); n != 0 {
		t.Errorf("pending = %d after timeout, want 0", n)
	}
}

func TestErrorFrame(t *testing.T) {
	cfd, hfd := pair(t)
	client := NewClient(newFDTransport(cfd))
	client.Start(nil)
	startHost(t, hfd, func(f *Frame, tr *fdTransport) bool {
		_ = tr.WriteFrame(&Frame{Type: TypeError, CallID: f.CallID, Method: f.Method, Data: []byte("vpn not permitted")})
		return true
	})
	_, err := client.Call(context.Background(), MethodCreateVpn, nil)
	var re *RemoteError
	if !errors.As(err, &re) {
		t.Fatalf("err = %v, want *RemoteError", err)
	}
	if !strings.Contains(re.Error(), "vpn not permitted") {
		t.Errorf("remote error text = %q", re.Error())
	}
}

func TestProtectSocketDupSemantics(t *testing.T) {
	cfd, hfd := pair(t)
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	defer pw.Close()
	defer pr.Close()

	client := NewClient(newFDTransport(cfd))
	client.Start(nil)
	var received int32 = -1
	startHost(t, hfd, func(f *Frame, tr *fdTransport) bool {
		if f.Method != MethodProtectSocket {
			t.Errorf("method = %s, want ProtectSocket", MethodName(f.Method))
		}
		if len(f.Data) != 0 {
			t.Errorf("ProtectSocket payload len = %d, want 0", len(f.Data))
		}
		if f.FD >= 0 {
			atomic.StoreInt32(&received, int32(f.FD))
		}
		// 宿主读一下收到的 fd，证明 fd 真的传过来了
		buf := make([]byte, 4)
		n, _ := syscall.Read(f.FD, buf)
		if n > 0 {
			atomic.StoreInt32(&received, int32(n))
		}
		closefd(f.FD) // 宿主侧用完关掉自己的副本
		_ = tr.WriteFrame(&Frame{Type: TypeDone, CallID: f.CallID, Method: f.Method, FD: -1})
		return true
	})

	if _, err := pw.Write([]byte("abcd")); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := clientProtect(client, int(pr.Fd())); err != nil {
		t.Fatalf("ProtectSocket: %v", err)
	}
	if got := atomic.LoadInt32(&received); got <= 0 {
		t.Errorf("host did not get a usable fd (marker=%d)", got)
	}
	// dup 语义：原 fd 必须还活着
	var st syscall.Stat_t
	if err := syscall.Fstat(int(pr.Fd()), &st); err != nil {
		t.Errorf("original fd was closed: %v", err)
	}
}

func clientProtect(c *Client, fd int) error {
	dup, err := syscall.Dup(fd)
	if err != nil {
		return err
	}
	_, err = c.CallWithFD(context.Background(), MethodProtectSocket, nil, dup)
	closefd(dup)
	return err
}

func TestLateResponseFDClosed(t *testing.T) {
	cfd, _ := pair(t)
	client := NewClient(newFDTransport(cfd))

	// 用裸 syscall.Pipe 而不是 os.Pipe：这个用例会由 Handle 直接关掉读端，
	// 如果包在 os.File 里，finalizer 之后可能再关一次已经被复用的 fd 号（实测会串到别的用例）。
	var pfd [2]int
	if err := syscall.Pipe(pfd[:]); err != nil {
		t.Fatalf("pipe: %v", err)
	}
	defer syscall.Close(pfd[1])
	fd := pfd[0]

	// 未知 callId 的 Done 帧：Handle 应返回 false 并把 fd 关掉
	if handled := client.Handle(&Frame{Type: TypeDone, CallID: 12345, Method: MethodCreateVpn, FD: fd}); handled {
		t.Fatalf("Handle claimed an unknown callId")
	}
	var st syscall.Stat_t
	if err := syscall.Fstat(fd, &st); err == nil {
		t.Errorf("late response fd %d was not closed", fd)
	}
}

func TestCreateVpnIdempotent(t *testing.T) {
	cfd, hfd := pair(t)
	client := NewClient(newFDTransport(cfd))
	client.Start(nil)
	p := New(client)

	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	defer pw.Close()
	defer pr.Close()

	var createCalls int32
	startHost(t, hfd, func(f *Frame, tr *fdTransport) bool {
		fd := -1
		if f.Method == MethodCreateVpn {
			atomic.AddInt32(&createCalls, 1)
			fd = int(pr.Fd()) // 每次都用同一个读端也没关系：SCM_RIGHTS 是复制语义
		}
		_ = tr.WriteFrame(&Frame{Type: TypeDone, CallID: f.CallID, Method: f.Method, FD: fd})
		return true
	})

	f1, err := p.CreateVpn(context.Background(), &VpnConfigure{})
	if err != nil {
		t.Fatalf("first CreateVpn: %v", err)
	}
	f2, err := p.CreateVpn(context.Background(), &VpnConfigure{})
	if err != nil {
		t.Fatalf("second CreateVpn: %v", err)
	}
	if f1 != f2 {
		t.Errorf("重复 CreateVpn 应复用同一个 fd，got %p vs %p", f1, f2)
	}
	if n := atomic.LoadInt32(&createCalls); n != 1 {
		t.Errorf("host saw %d CreateVpn frames, want 1", n)
	}
	if p.TunFile() != f1 {
		t.Errorf("TunFile should return the active tun")
	}

	// ReleaseVpn 应关掉 fd 并发 method 8
	if err := p.ReleaseVpn(context.Background()); err != nil {
		t.Fatalf("ReleaseVpn: %v", err)
	}
	if p.TunFile() != nil {
		t.Errorf("tun should be nil after release")
	}
}

func TestServeRoutesHostCalls(t *testing.T) {
	cfd, hfd := pair(t)
	client := NewClient(newFDTransport(cfd))

	gotCall := make(chan *Frame, 1)
	var once sync.Once
	go func() {
		once.Do(func() {})
		_ = client.Serve(func(f *Frame) {
			gotCall <- f
			_ = newFDTransport(cfd).WriteFrame(&Frame{Type: TypeDone, CallID: f.CallID, Method: f.Method})
		})
	}()

	host := newFDTransport(hfd)
	// host → core 的 Call（method 0 = initialize）
	if err := host.WriteFrame(&Frame{Type: TypeCall, CallID: 1, Method: 0}); err != nil {
		t.Fatalf("write: %v", err)
	}
	select {
	case f := <-gotCall:
		if f.Method != 0 || f.CallID != 1 {
			t.Errorf("onCall got method=%d callId=%d", f.Method, f.CallID)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("Serve did not route the host call")
	}
	// core 的 Done 回复应该能被 host 读到
	f, err := host.ReadFrame()
	if err != nil {
		t.Fatalf("host read: %v", err)
	}
	if f.Type != TypeDone || f.CallID != 1 {
		t.Errorf("host got %s callId=%d", TypeName(f.Type), f.CallID)
	}
}
