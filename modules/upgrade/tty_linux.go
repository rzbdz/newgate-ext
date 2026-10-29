package upgrade

import (
	"os"

	"golang.org/x/sys/unix"
)

// stdinIsTerminal 问的是终端驱动（TCGETS 成功 = 真的是一个 tty）。
//
// 不能看 os.ModeCharDevice：/dev/null 也是字符设备。2026-10-29 实测
// `newgate upgrade </dev/null` 被当成了交互，于是「问」了一句、读到 EOF——结果
// 碰巧对（EOF 当「不」），但退出码是 0，脚本以为升过了。
func stdinIsTerminal() bool {
	_, err := unix.IoctlGetTermios(int(os.Stdin.Fd()), unix.TCGETS)
	return err == nil
}
