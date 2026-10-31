package main

// transport.go — Bridge 线路协议
//
// 两处事实均从原版二进制直接读出，非推测：
//
// (1) 帧格式 — 来自 dex 中 Transport.readHeader / writeMessage 的字节码：
//       ByteBuffer.allocate(20)
//       getInt()   type    0=Call 1=Done 2=Error 3=Cancel
//       getLong()  callId
//       getInt()   method
//       getInt()   length
//     全大端（java.nio.ByteBuffer 默认序），随后紧跟 length 字节载荷。
//
// (2) 通道交接 — 来自 libcompat.so 的 Java_..._AndroidProcess_nativeStart：
//       access(path, R_OK|X_OK)
//       socketpair(AF_UNIX, SOCK_SEQPACKET, 0, sv)
//       fcntl(sv[i], F_GETFL) / fcntl(sv[i], F_SETFL, O_NONBLOCK)
//       fork()
//         子进程: dup2("/dev/null") -> 0,1,2
//                 遍历 /proc/self/fd 关闭除 {0,1,2,答案fd} 之外的 fd
//                 vsnprintf(buf, 32, "CHANNEL=%d", fd)  -> envp
//                 execve(path, argv, envp)
//     即：**不继承 argv 里的 fd，而是通过环境变量 CHANNEL 传 fd 号**。
//     socket 为 SOCK_SEQPACKET，故每个 sendmsg/recvmsg 恰好承载一条完整消息。

import (
	"fmt"
	"os"
	"strconv"
	"sync"
	"syscall"
	"time"
)

const headerSize = 20
const maxPayload = 8 << 20

type MsgType int32

const (
	TypeCall   MsgType = 0
	TypeDone   MsgType = 1
	TypeError  MsgType = 2
	TypeCancel MsgType = 3
)

func (t MsgType) String() string {
	switch t {
	case TypeCall:
		return "Call"
	case TypeDone:
		return "Done"
	case TypeError:
		return "Error"
	case TypeCancel:
		return "Cancel"
	}
	return fmt.Sprintf("Type(%d)", int32(t))
}

// Message mirrors com.github.kr328.clash.core.bridge.Message
type Message struct {
	Type   MsgType
	CallID int64
	Method int32
	Data   []byte
	FD     int // -1 when absent
}

// Transport carries framed messages over the inherited SOCK_SEQPACKET fd.
type Transport struct {
	fd  int
	wmu sync.Mutex
}

// NewTransportFromEnv picks up the fd handed over via CHANNEL=<fd>.
func NewTransportFromEnv() (*Transport, error) {
	v := os.Getenv("CHANNEL")
	if v == "" {
		return nil, fmt.Errorf("CHANNEL not set")
	}
	fd, err := strconv.Atoi(v)
	if err != nil {
		return nil, fmt.Errorf("CHANNEL=%q is not an fd number: %w", v, err)
	}
	// 必须确认是已打开的 socket：真机上曾遇到 CHANNEL 给了无效 fd 的情况，
	// 若不校验会「假装连上」然后 recvmsg 报 EBADF，日志里看不出真正的问题。
	var st syscall.Stat_t
	if err := syscall.Fstat(fd, &st); err != nil {
		return nil, fmt.Errorf("CHANNEL fd %d not open: %w", fd, err)
	}
	if st.Mode&syscall.S_IFMT != syscall.S_IFSOCK {
		return nil, fmt.Errorf("CHANNEL fd %d is not a socket (mode=%o)", fd, st.Mode)
	}
	return &Transport{fd: fd}, nil
}

func NewTransportFD(fd int) *Transport { return &Transport{fd: fd} }

func (t *Transport) FD() int { return t.fd }

// ReadMessage receives exactly one message (SEQPACKET keeps boundaries).
func (t *Transport) ReadMessage() (*Message, error) {
	buf := make([]byte, headerSize+maxPayload)
	oob := make([]byte, syscall.CmsgSpace(4*4))
	// EAGAIN/EINTR 不是错误：前者说明 fd 仍是非阻塞（nativeStart 会设），
	// 后者是信号打断。当成致命错误会导致内核在启动瞬间退出，
	// 表现为宿主 sendmsg 收到 EPIPE。这里退避重试。
	var n, oobn int
	var err error
	for attempt := 0; ; attempt++ {
		n, oobn, _, _, err = syscall.Recvmsg(t.fd, buf, oob, 0)
		if err == nil {
			break
		}
		if err == syscall.EAGAIN || err == syscall.EINTR {
			if attempt < 1000 {
				time.Sleep(10 * time.Millisecond)
				continue
			}
			return nil, fmt.Errorf("recvmsg: still %v after retries", err)
		}
		return nil, fmt.Errorf("recvmsg: %w", err)
	}
	if n < headerSize {
		return nil, fmt.Errorf("short frame: %d bytes", n)
	}
	m := &Message{
		Type:   MsgType(int32(be32(buf[0:4]))),
		CallID: int64(be64(buf[4:12])),
		Method: int32(be32(buf[12:16])),
		FD:     -1,
	}
	length := int(int32(be32(buf[16:20])))
	if length < 0 || length > maxPayload {
		return nil, fmt.Errorf("implausible payload length %d (frame %d)", length, n)
	}
	if length > 0 {
		have := n - headerSize
		if have > length {
			have = length
		}
		m.Data = append([]byte(nil), buf[headerSize:headerSize+have]...)
		// 宿主把帧头与载荷**分两次 writeFully 发送**（见 Transport.writeMessage，
		// 178 与 204 两次调用）。SOCK_SEQPACKET 下这是两个独立数据包，
		// 因此这里收到的第一包可能只有 20 字节帧头，载荷在下一包。
		// 必须继续收，否则会误判成「长度不符」并让内核退出——
		// 真机上表现为启动后 300ms 内核自尽、UI 卡在载入中。
		for len(m.Data) < length {
			want := length - len(m.Data)
			tmp := make([]byte, want)
			got, err := t.recvPacket(tmp, nil)
			if err != nil {
				return nil, fmt.Errorf("recv payload: %w", err)
			}
			if got == 0 {
				return nil, fmt.Errorf("payload truncated: want %d more, got 0", want)
			}
			m.Data = append(m.Data, tmp[:got]...)
		}
	}
	if oobn > 0 {
		if scms, e := syscall.ParseSocketControlMessage(oob[:oobn]); e == nil {
			for i := range scms {
				if fds, e2 := syscall.ParseUnixRights(&scms[i]); e2 == nil && len(fds) > 0 {
					m.FD = fds[0]
					break
				}
			}
		}
	}
	return m, nil
}

// WriteMessage sends one message; carries m.FD via SCM_RIGHTS when >= 0.
func (t *Transport) WriteMessage(m *Message) error {
	t.wmu.Lock()
	defer t.wmu.Unlock()

	payload := m.Data
	frame := make([]byte, headerSize+len(payload))
	put32(frame[0:4], uint32(m.Type))
	put64(frame[4:12], uint64(m.CallID))
	put32(frame[12:16], uint32(m.Method))
	put32(frame[16:20], uint32(len(payload)))
	copy(frame[headerSize:], payload)

	var oob []byte
	if m.FD >= 0 {
		oob = syscall.UnixRights(m.FD)
	}
	if err := syscall.Sendmsg(t.fd, frame, oob, nil, 0); err != nil {
		return fmt.Errorf("sendmsg: %w", err)
	}
	return nil
}

func (t *Transport) Close() error { return syscall.Close(t.fd) }

func be32(b []byte) uint32 {
	return uint32(b[0])<<24 | uint32(b[1])<<16 | uint32(b[2])<<8 | uint32(b[3])
}
func be64(b []byte) uint64 {
	return uint64(be32(b[0:4]))<<32 | uint64(be32(b[4:8]))
}
func put32(b []byte, v uint32) {
	b[0], b[1], b[2], b[3] = byte(v>>24), byte(v>>16), byte(v>>8), byte(v)
}
func put64(b []byte, v uint64) { put32(b[0:4], uint32(v>>32)); put32(b[4:8], uint32(v)) }

// 反向调用的 callId 分配器统一由 internal/platform 提供（platform.NextCallID，
// 起点 1<<40），避免两个包各持一个序列导致撞号。此处不再保留本地分配器。

// recvPacket 收一个数据包，对 EAGAIN/EINTR 退避重试。
// 返回实际收到的字节数（可能小于 buf）。
func (t *Transport) recvPacket(buf, oob []byte) (int, error) {
	if oob == nil {
		oob = make([]byte, syscall.CmsgSpace(4*4))
	}
	for attempt := 0; ; attempt++ {
		n, _, _, _, err := syscall.Recvmsg(t.fd, buf, oob, 0)
		if err == nil {
			return n, nil
		}
		if err == syscall.EAGAIN || err == syscall.EINTR {
			if attempt < 1000 {
				time.Sleep(10 * time.Millisecond)
				continue
			}
			return 0, fmt.Errorf("recvmsg: still %v after retries", err)
		}
		return 0, fmt.Errorf("recvmsg: %w", err)
	}
}
