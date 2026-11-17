//go:build !linux && !darwin

package term

import "syscall"

// 非 Unix 平台（今天只有 Windows）没有 termios：交互界面在那里应当干净地拒绝，
// 让调用方打印「这台机器上走参数版」那句话，而不是编译不过。
//
// 构建矩阵里没有 Windows（release.yml 只编 linux/darwin），但本仓库是 Go module，
// 谁 fork 之后 `GOOS=windows go vet ./...` 不该炸在一个终端工具上。

func enterRaw(uintptr) (func(), error) { return nil, syscall.ENOSYS }

func isTTY(uintptr) bool { return false }
