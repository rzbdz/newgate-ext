//go:build linux || darwin

package term

import (
	"golang.org/x/sys/unix"
)

// This file is the **only concrete implementation** of raw mode on the Unixes we ship;
// term_linux.go / term_darwin.go contribute nothing but the two ioctl numbers.
//
// Why it is split that way (2026-11-18, and this is a bug we shipped): the first version was one
// file named `term_linux.go` carrying `//go:build linux || darwin`. The tag looked right and the
// compiler disagreed -- **a `_linux` filename suffix is itself a constraint**, so Darwin got the
// file's build tag and none of its contents. `GOOS=darwin go build` then failed with `undefined:
// enterRaw`, and nobody saw it: CI only runs on ubuntu, and the release matrix is the first place
// that cross-compiles to darwin. Two files, each named for the one OS it serves, cannot drift that
// way: there is no "which of the two constraints wins" question to get wrong.
//
// Why golang.org/x/sys/unix and not syscall: on Linux the numbers are TCGETS/TCSETS, on Darwin
// TIOCGETA/TIOCSETA, and `syscall` only defines the pair that belongs to its own GOOS -- so a
// single file cannot name both. x/sys/unix gives the same two calls on both platforms and is
// already a dependency (ssh-tunnel, modules/upgrade), so this costs no new dependency.
// The terminal is still just two ioctls and a struct; a third-party terminal library
// (golang.org/x/term) would still be a dependency with no reason.
//
// This mirrors modules/upgrade/tty_{linux,darwin}.go exactly -- same question, same split.

// rawMode is what "one byte at a time, no echo, Ctrl-C produces no SIGINT" means: the line
// discipline off, the input translations off, and VMIN=1/VTIME=0 so a read returns as soon as one
// byte exists instead of waiting for a newline.
func rawMode(t *unix.Termios) *unix.Termios {
	raw := *t
	raw.Lflag &^= unix.ECHO | unix.ICANON | unix.ISIG
	raw.Iflag &^= unix.IXON | unix.ICRNL
	raw.Cc[unix.VMIN] = 1
	raw.Cc[unix.VTIME] = 0
	return &raw
}

// enterRaw switches the terminal to raw mode and hands back the restore function.
//
// Restore is idempotent on purpose: every caller path (a read error, a signal, a normal exit)
// reaches for it, and running it twice must not write a stale mode back over a later one.
func enterRaw(fd uintptr) (func(), error) {
	old, err := unix.IoctlGetTermios(int(fd), ioctlGetTermios)
	if err != nil {
		return nil, err
	}
	if err := unix.IoctlSetTermios(int(fd), ioctlSetTermios, rawMode(old)); err != nil {
		return nil, err
	}
	restored := false
	return func() {
		if restored {
			return
		}
		restored = true
		_ = unix.IoctlSetTermios(int(fd), ioctlSetTermios, old)
	}, nil
}

// isTTY asks the terminal driver rather than the file mode: /dev/null is a char device too, so
// os.ModeCharDevice answers "yes" for a machine that has no terminal at all (measured 2026-10-29
// in modules/upgrade, where it made an upgrade "ask" and read EOF).
func isTTY(fd uintptr) bool {
	_, err := unix.IoctlGetTermios(int(fd), ioctlGetTermios)
	return err == nil
}
