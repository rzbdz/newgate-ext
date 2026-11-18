//go:build linux

package term

import "golang.org/x/sys/unix"

// The two ioctl numbers, and nothing else -- see term_unix.go for the implementation and for
// why the split exists at all. On Linux "get the termios" is TCGETS; Darwin spells the same
// question TIOCGETA (term_darwin.go).
const (
	ioctlGetTermios = unix.TCGETS
	ioctlSetTermios = unix.TCSETS
)
