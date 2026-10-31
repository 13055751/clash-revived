package platform

// client.go — core → host 反向 RPC 客户端。
//
// 对应原版 platform.callRemote（0xd14a50，1488 字节）的行为：
//   1. 分配一个 Receiver（callId）
//   2. 组 Message{Type: Call, callId, method, data, fd} 并 WriteMessage
//   3. 在 Receiver 上等响应；同时监听 ctx.Done()
//   4. ctx 取消时向 host 补发 Type=Cancel（同一 callId），然后返回 ctx.Err()
//
// 已确认（二进制）：
//   - 请求 type 固定为 Call(0)，响应 Done(1) / Error(2)
//   - 同一 fd 上按 callId 配对，不同 callId 可并发
//   - CreateVpn 的 tun fd 通过响应的 SCM_RIGHTS 回传
//
// 未确认（原版行为无法从二进制直接读出的一处）：
//   - Error 帧的载荷语义。原版是 Throwable 序列化后的产物；
//     这里按「UTF-8 错误文本」处理，同时在 RemoteError 里保留原始字节。

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"
)

// ErrClosed 表示 Client 已被关闭。
var ErrClosed = errors.New("platform: client closed")

// Reply 是一次成功调用的结果。
// FD >= 0 时由调用方负责关闭（tun fd 的生命周期归调用方）。
type Reply struct {
	Data []byte
	FD   int
}

// RemoteError 是 host 返回的 Error 帧。
// 原版把 Kotlin 侧异常序列化进载荷；这里保留原字节，Message 尽力解析成文本。
type RemoteError struct {
	Method  int32
	Message string
	Raw     []byte
}

func (e *RemoteError) Error() string {
	if e.Message == "" {
		return fmt.Sprintf("platform: host error on method %d (raw %d bytes)", e.Method, len(e.Raw))
	}
	return fmt.Sprintf("platform: host error on method %d: %s", e.Method, e.Message)
}

type Option func(*Client)

// WithTimeout 设置单次调用的默认超时（ctx 没有 deadline 时生效）。
func WithTimeout(d time.Duration) Option { return func(c *Client) { c.timeout = d } }

// WithCallIDAlloc 替换 callId 分配器。
// 集成到 main 时建议传 main.NextCallID，保证整个进程只有一个序列。
func WithCallIDAlloc(f func() int64) Option { return func(c *Client) { c.alloc = f } }

// WithLogger 注入日志器（默认 slog.Default）。
func WithLogger(l *slog.Logger) Option { return func(c *Client) { c.log = l } }

// Client 是 core→host 的 RPC 端点。
//
// 两条使用路径，二选一（不要同时跑，否则会争抢同一个 fd 上的读）：
//
//	A. Serve 模式：Client 独占读循环，把 host→core 的 Call 交给回调
//	B. Handle 模式：外部已有读循环（如 main.core.Serve），把每条非 Call 帧喂给 Handle
type Client struct {
	tr      Transport
	timeout time.Duration
	alloc   func() int64
	log     *slog.Logger

	mu      sync.Mutex
	pending map[int64]chan *Frame
	closed  bool

	startOnce sync.Once
}

func NewClient(tr Transport, opts ...Option) *Client {
	c := &Client{
		tr:      tr,
		timeout: defaultTimeout,
		alloc:   NextCallID,
		log:     slog.Default(),
		pending: make(map[int64]chan *Frame),
	}
	for _, o := range opts {
		o(c)
	}
	return c
}

// Call 发起一次反向调用并等待响应。
//
// payload 为空表示载荷为空（对应 GetFeatures / GetTimeZone / ReleaseVpn 等）。
// fd 一般只给 ProtectSocket 用；fd 的所有权仍在调用方（写出去是 SCM_RIGHTS 复制语义）。
func (c *Client) Call(ctx context.Context, method int32, payload []byte) (*Reply, error) {
	return c.call(ctx, method, payload, -1)
}

// CallWithFD 同 Call，但通过 SCM_RIGHTS 附带一个 fd。
func (c *Client) CallWithFD(ctx context.Context, method int32, payload []byte, fd int) (*Reply, error) {
	return c.call(ctx, method, payload, fd)
}

func (c *Client) call(ctx context.Context, method int32, payload []byte, fd int) (*Reply, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if c.timeout > 0 {
		if _, ok := ctx.Deadline(); !ok {
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(ctx, c.timeout)
			defer cancel()
		}
	}

	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil, ErrClosed
	}
	id := c.alloc()
	ch := make(chan *Frame, 1)
	c.pending[id] = ch
	c.mu.Unlock()

	defer func() {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
	}()

	if err := c.tr.WriteFrame(&Frame{
		Type:   TypeCall,
		CallID: id,
		Method: method,
		Data:   payload,
		FD:     fd,
	}); err != nil {
		return nil, fmt.Errorf("platform: write call (method %d, id %d): %w", method, id, err)
	}

	select {
	case f := <-ch:
		return replyFromFrame(f)
	case <-ctx.Done():
		// 原版在 ctx 取消时向宿主补发 Cancel（callRemote 的第二个 WriteMessage 调用点）。
		// 失败无所谓：宿主那边的 handler 最终会自己超时或随连接关闭而消失。
		if err := c.tr.WriteFrame(&Frame{Type: TypeCancel, CallID: id, Method: method, FD: -1}); err != nil {
			c.log.Debug("platform: send cancel failed", "method", method, "callId", id, "err", err)
		}
		return nil, fmt.Errorf("platform: call method %d interrupted: %w", method, ctx.Err())
	}
}

func replyFromFrame(f *Frame) (*Reply, error) {
	switch f.Type {
	case TypeDone:
		return &Reply{Data: f.Data, FD: f.FD}, nil
	case TypeError:
		// fd 出现在 Error 帧上是异常路径，必须就地关闭，否则泄漏。
		if f.FD >= 0 {
			closefd(f.FD)
		}
		return nil, &RemoteError{Method: f.Method, Message: errorText(f.Data), Raw: f.Data}
	case TypeCancel:
		if f.FD >= 0 {
			closefd(f.FD)
		}
		return nil, fmt.Errorf("platform: host cancelled method %d", f.Method)
	}
	if f.FD >= 0 {
		closefd(f.FD)
	}
	return nil, fmt.Errorf("platform: unexpected frame type %s for method %d", TypeName(f.Type), f.Method)
}

// errorText 尽力把 Error 载荷转成可读文本：优先取 UTF-8，非文本则给十六进制摘要。
func errorText(b []byte) string {
	if len(b) == 0 {
		return ""
	}
	if isMostlyText(b) {
		return string(b)
	}
	return fmt.Sprintf("% x", b[:min(len(b), 64)])
}

func isMostlyText(b []byte) bool {
	bad := 0
	for _, r := range b {
		if r < 0x20 && r != '\n' && r != '\r' && r != '\t' {
			bad++
		}
	}
	return bad*10 <= len(b)
}

// Handle 处理一条从通道读到的帧。
//
// 返回 true 表示这条帧是本 Client 的响应/取消（外部读循环应该 continue）；
// 返回 false 表示不是（多半是 host→core 的 Call，应交给原有分发逻辑）。
func (c *Client) Handle(f *Frame) bool {
	if f == nil || f.Type == TypeCall {
		return false
	}
	c.mu.Lock()
	ch, ok := c.pending[f.CallID]
	c.mu.Unlock()
	if !ok {
		// 未知 callId 的响应：可能是超时之后宿主的迟到回包。
		if f.FD >= 0 {
			closefd(f.FD)
		}
		c.log.Debug("platform: late response dropped", "type", TypeName(f.Type), "callId", f.CallID)
		return false
	}
	// ch 有 1 个缓冲，不会阻塞；重复响应也只可能投一次。
	select {
	case ch <- f:
	default:
		if f.FD >= 0 {
			closefd(f.FD)
		}
	}
	return true
}

// Start 起后台读循环（推荐用法）。幂等。
//
// onCall 为 nil 时，host→core 的 Call 会被丢弃并记一条 debug 日志。
// 需要把 host→core 交给 main 的分发逻辑时，传一个回调进来即可；
// 另一条路是不用 Start，自己在读循环里把每条帧喂给 Handle（见文档「集成」）。
func (c *Client) Start(onCall OnCall) {
	c.startOnce.Do(func() {
		go func() {
			if err := c.Serve(onCall); err != nil {
				c.mu.Lock()
				closed := c.closed
				c.mu.Unlock()
				if !closed {
					c.log.Debug("platform: read loop ended", "err", err)
				}
			}
		}()
	})
}

// OnCall 是 Serve 模式下的 host→core 回调。
// 实现方必须在返回前把 fd 处理掉（关掉或接管）。
type OnCall func(f *Frame)

// Serve 独占读循环。
//
// 帧的处理顺序：先给 Handle（配对响应），否则当作 host→core 的 Call 交给 onCall。
// 返回 nil 表示通道正常关闭；其他错误原样返回。
func (c *Client) Serve(onCall OnCall) error {
	for {
		f, err := c.tr.ReadFrame()
		if err != nil {
			c.mu.Lock()
			closed := c.closed
			c.mu.Unlock()
			if closed {
				return nil
			}
			return err
		}
		if c.Handle(f) {
			continue
		}
		if f.Type == TypeCall && onCall != nil {
			onCall(f)
			continue
		}
		if f.FD >= 0 {
			closefd(f.FD)
		}
		c.log.Debug("platform: unhandled frame", "type", TypeName(f.Type), "method", f.Method, "callId", f.CallID)
	}
}

// Pending 返回当前在等的调用数（测试与排障用）。
func (c *Client) Pending() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.pending)
}

// Close 关闭 Client：在等的调用立刻收到 ErrClosed，之后不再发新调用。
// 不关闭底层 Transport（所有权在调用方）。
func (c *Client) Close() {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	c.closed = true
	pending := c.pending
	c.pending = make(map[int64]chan *Frame)
	c.mu.Unlock()

	for _, ch := range pending {
		select {
		case ch <- &Frame{Type: TypeCancel, FD: -1}:
		default:
		}
	}
}
