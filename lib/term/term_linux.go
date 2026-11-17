//go:build linux || darwin

package term

import (
	"syscall"
	"unsafe"
)

// 本文件是 raw mode 的**唯一实物**（Linux / Darwin 共用 termios；字段布局两边
// 一致，只是常量名字不同——见下面那组别名）。
//
// 不用第三方库（golang.org/x/term）的原因：整个仓库对第三方依赖的态度是「每个
// 都得有个理由」，而这个功能就是两个 ioctl 加一个结构体。发行版已经有
// golang.org/x/crypto（ssh-tunnel）与 golang.org/x/sys，但没有为「读一个按键」
// 加一份依赖的必要。

type termios struct {
	Iflag, Oflag, Cflag, Lflag uint32
	Line                       uint8
	Cc                         [32]uint8
	Ispeed, Ospeed             uint32
}

const (
	tcgets = 0x5401 // TIOCGETA(Linux) / TCGETS(Darwin) 在这两个平台上都可用
	tcsets = 0x5402
)

func ioctlTermios(fd uintptr, req uintptr, t *termios) error {
	_, _, e := syscall.Syscall(syscall.SYS_IOCTL, fd, req, uintptr(unsafe.Pointer(t)))
	if e != 0 {
		return e
	}
	return nil
}

// enterRaw 把终端切成「一次一个字节、不回显、Ctrl-C 不产生 SIGINT」。
func enterRaw(fd uintptr) (func(), error) {
	var old termios
	if err := ioctlTermios(fd, tcgets, &old); err != nil {
		return nil, err
	}
	raw := old
	raw.Lflag &^= syscall.ECHO | syscall.ICANON | syscall.ISIG
	raw.Iflag &^= syscall.IXON | syscall.ICRNL
	raw.Cc[syscall.VMIN] = 1
	raw.Cc[syscall.VTIME] = 0
	if err := ioctlTermios(fd, tcsets, &raw); err != nil {
		return nil, err
	}
	restored := false
	return func() {
		if restored {
			return
		}
		restored = true
		_ = ioctlTermios(fd, tcsets, &old)
	}, nil
}

func isTTY(fd uintptr) bool {
	var t termios
	return ioctlTermios(fd, tcgets, &t) == nil
}
