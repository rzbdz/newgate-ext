package cli

import (
	"github.com/rzbdz/newgate-ext/modules/gateway/controlplane"
)

// moduleCLIHost exposes rendering and daemon notification primitives to
// module-owned commands without importing concrete modules into the CLI.
//
// verb 是**分派器这次按哪个名字找到这条命令**（见 cliapi.Host.Verb）。它由分派
// 那一刻决定，所以宿主是**一次调用一份**的短命值，不是单例。
type moduleCLIHost struct{ verb string }

func (moduleCLIHost) Die(code int, message string) int { return die(code, message) }

func (h moduleCLIHost) Verb() string { return h.verb }

func (moduleCLIHost) NotifyProxy() { notifyProxy() }

// DaemonRunning 守护进程现在在跑吗。
//
// 走控制面叶子的 State()（它读 pidfile），而不是直接 import runtime/daemon：
// 界面**不认识 runtime**，而「怎么知道进程在不在」是数据面与控制面之间的事。
func (moduleCLIHost) DaemonRunning() bool {
	info, _ := controlplane.State()
	return info != nil
}
