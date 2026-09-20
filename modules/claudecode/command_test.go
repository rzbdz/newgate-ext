package claudecode

import (
	"testing"
	"time"

	cliapi "github.com/rzbdz/newgate/modules/cli/extension"
	"github.com/rzbdz/newgate/modules/config/store"
	"github.com/rzbdz/newgate/testing/testkit"
)

// nakedHost 是 Host 的最小实现。naked 只用到 Die 和 NotifyProxy。
type nakedHost struct {
	died     int
	msg      string
	notified int
}

func (h *nakedHost) Die(code int, message string) int { h.died, h.msg = code, message; return code }

func (h *nakedHost) DaemonRunning() bool { return false }

func (h *nakedHost) NotifyProxy() { h.notified++ }

// Verb 空串：测试直接调 Run，没有分派器，也就没有「按哪个名字找到我」。
func (h *nakedHost) Verb() string { return "" }

var _ cliapi.Host = (*nakedHost)(nil)

// nakedNow 读当前裸奔配置。
func nakedNow(t *testing.T) (NakedConfig, bool) {
	t.Helper()
	return ParseNakedConfig(store.LoadState().ModuleConfig[NakedConfigKey])
}

// TestNakedRunGetsArgsWithoutTheVerb 守命令的参数下标基准。
//
// 分派器把动词剥掉才交给 Run：`newgate naked on` → Run 收到 `["on"]`。
// 2026-09-18 之前这里按 1 起下标取参（`argAt(args, 1)`），拿到的是**空串**，
// 于是落进 `case "", "off"` 分支——`newgate naked on` 实际执行的是 **off**。
//
// 这个 bug 骗过了三层：单元测试全绿（没人按分派器的形状驱动过它）、`newgate naked`
// 的输出看着正常（它打的就是「已关闭」）、连端到端里那条「裸奔终于关掉」的断言
// 也是**因为错的理由通过的**。最终是端到端里「开着时上游收到 0 个请求」那条红的
// 才把它揪出来——真实二进制跑一遍，比什么断言都值。
func TestNakedRunGetsArgsWithoutTheVerb(t *testing.T) {
	testkit.Sandbox(t)
	if _, err := store.Init(false); err != nil {
		t.Fatalf("Init: %v", err)
	}

	// on：必须真的开起来，而不是被执行成 off。
	host := &nakedHost{}
	if code := (nakedCommand{}).Run(host, []string{"on"}); code != 0 {
		t.Fatalf("naked on 失败(%d): %s", code, host.msg)
	}
	if host.notified == 0 {
		t.Fatal("开了裸奔却没通知代理——daemon 要等下一次轮询才收敛")
	}
	cfg, active := nakedNow(t)
	if !active {
		t.Fatal("naked on 没落地——这就是那个错位 bug：on 被执行成了 off")
	}
	if cfg.Mode != "on" || cfg.ExpiresAt.IsZero() {
		t.Fatalf("配置不对: %+v", cfg)
	}
	if d := time.Until(cfg.ExpiresAt); d <= 0 || d > 2*time.Minute {
		t.Fatalf("默认窗口该是 60s 量级，得到 %s", d)
	}

	// 带时长：错位时这个参数会被当成子命令（不认识的时长）。
	host = &nakedHost{}
	if code := (nakedCommand{}).Run(host, []string{"2m"}); code != 0 {
		t.Fatalf("naked 2m 失败(%d): %s", code, host.msg)
	}
	if cfg, _ := nakedNow(t); time.Until(cfg.ExpiresAt) <= 90*time.Second {
		t.Fatalf("2m 没被解析成时限，得到 %s", time.Until(cfg.ExpiresAt))
	}

	// off：清掉字段。
	host = &nakedHost{}
	if code := (nakedCommand{}).Run(host, []string{"off"}); code != 0 {
		t.Fatalf("naked off 失败(%d): %s", code, host.msg)
	}
	if _, active := nakedNow(t); active {
		t.Fatal("naked off 没清掉配置")
	}

	// 不带参数 = 看现状，不该改动任何东西。
	if code := (nakedCommand{}).Run(&nakedHost{}, nil); code != 0 {
		t.Fatal("naked 无参数该打印现状并成功退出")
	}
	if _, active := nakedNow(t); active {
		t.Fatal("看一眼现状把配置改了")
	}

	// 不认识的时长要报错，而不是静默当成 on。
	host = &nakedHost{}
	if code := (nakedCommand{}).Run(host, []string{"稍等一会"}); code != 64 {
		t.Fatalf("不认识的时长该以 64 退出，得到 %d", code)
	}
	if _, active := nakedNow(t); active {
		t.Fatal("报错路径不该写配置")
	}
}
