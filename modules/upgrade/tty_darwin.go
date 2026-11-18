package upgrade

import (
	"os"

	"golang.org/x/sys/unix"
)

// stdinIsTerminal: see tty_linux.go (on macOS the same question is asked with TIOCGETA).
func stdinIsTerminal() bool {
	_, err := unix.IoctlGetTermios(int(os.Stdin.Fd()), unix.TIOCGETA)
	return err == nil
}
