//go:build !linux && !darwin

package upgrade

// stdinIsTerminal: platforms that cannot ask the driver count as "no" - better to require -y
// than treat an unanswered prompt as consent.
func stdinIsTerminal() bool { return false }
