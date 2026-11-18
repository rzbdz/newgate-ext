//go:build !linux && !darwin

package term

import "syscall"

// Non-Unix platforms (today only Windows) have no termios: interactive
// UIs should refuse cleanly there, letting the caller print "this
// machine uses the flag version", rather than fail to compile.
//
// The build matrix has no Windows (release.yml only builds linux/darwin),
// but this repo is a Go module, and whoever forks it should not
// have `GOOS=windows go vet ./...` blow up on a terminal utility.

func enterRaw(uintptr) (func(), error) { return nil, syscall.ENOSYS }

func isTTY(uintptr) bool { return false }
