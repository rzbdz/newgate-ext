//go:build !linux && !darwin

package upgrade

// stdinIsTerminal：问不了终端驱动的平台一律当「不是」——宁可要求 -y，
// 也不把一次没人回答的提问当成同意。
func stdinIsTerminal() bool { return false }
