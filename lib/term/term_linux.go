//go:build linux || darwin

package term

import (
	"syscall"
	"unsafe"
)

// This file is the **only concrete implementation** of raw mode (Linux / Darwin share termios; the
// field layout is the same on both, only the constant names differ -- see the alias group below).
//
// Why no third-party library (golang.org/x/term): the whole repo's attitude to
// third-party dependencies is "each one must have a reason", and this feature is two
// ioctls and a struct. The distribution already has golang.org/x/crypto (ssh-tunnel)
// and golang.org/x/sys, but there is no need to add a dependency for "read one key".

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

// enterRaw switches the terminal to "one byte at a time, no echo, Ctrl-C produces no SIGINT".
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
