package upgrade

import (
	"os"

	"golang.org/x/sys/unix"
)

// stdinIsTerminal：见 tty_linux.go（macOS 上同一个问题用 TIOCGETA 问）。
func stdinIsTerminal() bool {
	_, err := unix.IoctlGetTermios(int(os.Stdin.Fd()), unix.TIOCGETA)
	return err == nil
}
