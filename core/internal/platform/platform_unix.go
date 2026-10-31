package platform

// platform_unix.go — fd 相关的系统调用封装（android/linux）。
//
// 不用 CGO（DEV_SPEC 硬约束），直接走 syscall。
// android 构建同时满足 linux tag，所以这里的实现两个目标都能用。

import (
	"syscall"
	"time"
)

// defaultTimeout 是单次反向调用的默认上限。
// 原版没有内置超时（只看 ctx.Done()），这里给一个兜底值避免联调时永久挂住。
const defaultTimeout = 10 * time.Second

func closefd(fd int) {
	if fd >= 0 {
		_ = syscall.Close(fd)
	}
}

func dupFD(fd int) (int, error) {
	return syscall.Dup(fd)
}

// setBlocking 清掉 O_NONBLOCK。
// 宿主回传的 fd 来自 ParcelFileDescriptor，可能是非阻塞的；
// tun 设备后续读不能 EAGAIN，所以这里统一改成阻塞模式。
func setBlocking(fd int) error {
	flags, err := fcntl(fd, syscall.F_GETFL, 0)
	if err != nil {
		return err
	}
	if flags&syscall.O_NONBLOCK == 0 {
		return nil
	}
	_, err = fcntl(fd, syscall.F_SETFL, flags&^syscall.O_NONBLOCK)
	return err
}

// fcntl 直接走 SYS_FCNTL：std syscall 在 linux/arm64 上没有 FcntlInt，
// 而引 golang.org/x/sys/unix 会把 go.mod 里的 indirect 变成 direct（那是 Lead 的文件）。
func fcntl(fd, cmd, arg int) (int, error) {
	r, _, errno := syscall.Syscall(syscall.SYS_FCNTL, uintptr(fd), uintptr(cmd), uintptr(arg))
	if errno != 0 {
		return int(r), errno
	}
	return int(r), nil
}
