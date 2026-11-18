//go:build darwin

package term

import "golang.org/x/sys/unix"

// The two ioctl numbers, and nothing else -- see term_unix.go for the implementation and for
// why the split exists at all. Darwin's termios calls are TIOCGETA/TIOCSETA; x/sys/unix has
// both here (unlike syscall, which only knows its own GOOS's spelling).
const (
	ioctlGetTermios = unix.TIOCGETA
	ioctlSetTermios = unix.TIOCSETA
)
