package pluginmanager

import (
	"testing"

	cliapi "github.com/rzbdz/newgate-ext/modules/cli/extension"
	"github.com/rzbdz/newgate-ext/modules/config/store"
)

// stubHost 是 Host 的最小实现。命令只用到 Die 和 NotifyProxy，其余是接口噪音。
type stubHost struct {
	died     int
	msg      string
	notified int
}

func (h *stubHost) Die(code int, message string) int { h.died, h.msg = code, message; return code }

func (h *stubHost) DaemonRunning() bool { return false }

func (h *stubHost) NotifyProxy() { h.notified++ }

// Verb 空串：测试直接调 Run，没有分派器，也就没有「按哪个名字找到我」。
func (h *stubHost) Verb() string { return "" }

var _ cliapi.Host = (*stubHost)(nil)

// TestRunGetsArgsWithoutTheVerb 守命令的参数下标基准。
//
// 分派器**把动词剥掉**才交给 Run：`newgate plugin X off 90s` → Run 收到
// `["X","off","90s"]`。这条契约 2026-09-18 之前没写在契约包上，于是照
// os.Args 的直觉按 1 起下标写，整条命令错位一格：
//
//	newgate plugin deepseek          → 打成了列表（不是展开）
//	newgate plugin deepseek.tail-shape off 90s → 报「不认识的动词 "90s"」
//
// 单元测试当时全绿——因为它们直接构造 args，绕过了分派器；是装上真二进制跑一遍
// 才发现的。所以这条测试用**分派器真实传的形状**驱动（args 里没有 "plugin"），
// 而不是自己编一个顺手的切片。
func TestRunGetsArgsWithoutTheVerb(t *testing.T) {
	manager := start(t)
	if _, err := store.Init(false); err != nil {
		t.Fatalf("Init: %v", err)
	}
	release, err := manager.RegisterSelf("deepseek", []Switch{goodSwitch("deepseek.tail-shape")})
	if err != nil {
		t.Fatalf("注册: %v", err)
	}
	defer func() { _ = release() }()

	cmd := &command{manager: manager}

	// 展开一个模块：args = ["deepseek"]。错位时它会被当成空 args 打成列表。
	if host := (&stubHost{}); cmd.Run(host, []string{"deepseek"}) != 0 {
		t.Fatalf("plugin deepseek 失败: %s", host.msg)
	}

	// 开关 + 时长：错位时 "90s" 会被当成动词。
	host := &stubHost{}
	if code := cmd.Run(host, []string{"deepseek.tail-shape", "off", "90s"}); code != 0 {
		t.Fatalf("plugin deepseek.tail-shape off 90s 失败(%d): %s", code, host.msg)
	}
	if host.notified == 0 {
		t.Fatal("改了开关却没通知代理——daemon 要等下一次轮询才收敛")
	}
	st := store.LoadState()
	if !Off(st, "deepseek.tail-shape") {
		t.Fatal("off 没落到 state.json 上")
	}
	if until := Remaining(st, "deepseek.tail-shape"); until.IsZero() {
		t.Fatal("90s 没被解析成时限——时长参数还是被吃掉了")
	}

	// 模块级开关：args = ["deepseek","on"]。
	if host := (&stubHost{}); cmd.Run(host, []string{"deepseek", "on"}) != 0 {
		t.Fatalf("plugin deepseek on 失败: %s", host.msg)
	}
	if Off(store.LoadState(), "deepseek.tail-shape") {
		t.Fatal("模块级 on 没把开关点恢复")
	}

	// 错位的第二个症状：真动词写错时该报的是那个动词。
	host = &stubHost{}
	if code := cmd.Run(host, []string{"deepseek.tail-shape", "bogus"}); code != 64 {
		t.Fatalf("不认识的动词该以 64 退出，得到 %d", code)
	}
	if host.msg == "" {
		t.Fatal("拒绝时没有给出理由")
	}
}
