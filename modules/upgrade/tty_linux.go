package upgrade

import (
	"os"

	"golang.org/x/sys/unix"
)

// stdinIsTerminal asks the terminal driver (TCGETS succeeds = a real tty).
//
// Do not check os.ModeCharDevice: /dev/null is a char device too. 2026-10-29 measured
// `newgate upgrade </dev/null` treated as interactive, so it "asked" and read EOF - the
// result happened to be right (EOF = no), but the exit code was 0, and scripts thought it upgraded.
func stdinIsTerminal() bool {
	_, err := unix.IoctlGetTermios(int(os.Stdin.Fd()), unix.TCGETS)
	return err == nil
}
