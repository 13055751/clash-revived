// Package platform 实现 core → host 方向的 Platform 反向调用。
//
// 线路事实（来自 libclash.so 与 classes.dex 双向取证，见 work/PLATFORM_RPC.md）：
//   - 帧头 20 字节大端：int32 type, int64 callId, int32 method, int32 length
//   - type: Call=0 Done=1 Error=2 Cancel=3
//   - 通道 SOCK_SEQPACKET，fd 通过 SCM_RIGHTS 附带
//
// 本包刻意不 import main 包：main.Transport 与这里的 Frame 是两套类型，
// 集成时需要一个 12 行的适配器（见 PLATFORM_RPC.md 的「集成」一节）。
package platform

import (
	"fmt"
	"sync/atomic"
)

// Frame 与 com.github.kr328.clash.core.bridge.Message 一一对应。
type Frame struct {
	Type   int32
	CallID int64
	Method int32
	Data   []byte
	FD     int // -1 表示没有附带 fd
}

// 与 main 包 transport.go 的 MsgType 取值保持一致。
const (
	TypeCall   int32 = 0
	TypeDone   int32 = 1
	TypeError  int32 = 2
	TypeCancel int32 = 3
)

// TypeName 便于日志。
func TypeName(t int32) string {
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
	return fmt.Sprintf("Type(%d)", t)
}

// Transport 是 Client 依赖的最小传输接口。
// 实现者负责 SCM_RIGHTS 的收发（main.Transport 已经支持）。
type Transport interface {
	// ReadFrame 读恰好一帧；返回的 Frame.FD 在没有 fd 时必须是 -1。
	ReadFrame() (*Frame, error)
	// WriteFrame 写恰好一帧；Frame.FD >= 0 时通过 SCM_RIGHTS 附带。
	WriteFrame(*Frame) error
}

// allocBase 与 main 包 transport.go 的起点相同（1<<40），
// 用来避开宿主侧从 1 开始自增的 callId 序列。
var allocBase int64 = 1 << 40

// NextCallID 分配一个 core→host 方向独有的 callId。
// 与 main.NextCallID 同语义；两边若同时使用请只保留一个分配器（见文档）。
func NextCallID() int64 { return atomic.AddInt64(&allocBase, 1) }
