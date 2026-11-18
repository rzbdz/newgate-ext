package takeover

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/rzbdz/newgate-ext/modules/config/store"
	"github.com/rzbdz/newgate-ext/modules/runtime/injection"
)

// isolate 把 newgate 的配置目录、目标配置文件目录和 HOME 全部指到临时目录。
//
// 三个都必须隔离，缺一个就会动到用户的真实文件：
//   - NEWGATE_HOME       state.json / shim 目录
//   - NEWGATE_TARGET_DIR 被改写的 opencode 配置（**不受 NEWGATE_HOME 影响**）
//   - HOME               injection.RCFiles() 走 os.UserHomeDir()
func isolate(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	if err := os.MkdirAll(target, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("NEWGATE_HOME", dir)
	t.Setenv("NEWGATE_TARGET_DIR", target)
	t.Setenv("HOME", filepath.Join(dir, "home")) // 不创建 → 没有 rc 文件可动
	return dir
}

func installed(t *testing.T, agent string) bool {
	t.Helper()
	for _, n := range injection.Installed() {
		if n == agent {
			return true
		}
	}
	return false
}

// TestPlugUnplugCycle 是用户报的那个 bug 的回归测试。
//
// 原始症状：newgate stop 之后 claude 还在走 newgate。根因是 stop 没摘 PATH
// shim，wrapper 又会懒启动代理，等于没停。修了这一半之后立刻出现镜像故障：
// start 不把 shim 装回去，claude 静默直连。
//
// 所以这个测试必须走完整个来回，两个方向都验。
func TestPlugUnplugCycle(t *testing.T) {
	isolate(t)

	if installed(t, "claude") {
		t.Fatal("干净环境里就有 shim？隔离没生效")
	}

	// 接管
	if r := On("claude", 8899); r.Err != nil {
		t.Fatalf("On: %v", r.Err)
	}
	if !installed(t, "claude") {
		t.Fatal("On 之后 shim 不在——接管没落地")
	}

	// stop：必须摘掉 shim，否则 `claude` 仍命中 shim → wrapper 懒启动代理
	OffAll()
	if installed(t, "claude") {
		t.Fatal("stop 没摘掉 shim —— 这就是「stop 了还在走 newgate」")
	}

	// 但 stop 不该改变意愿，所以下次 start 必须原样插回去
	if st := store.LoadState(); !st.TakeoverWanted("claude") {
		t.Fatal("stop 改掉了接管意愿，start 就不会再接管了")
	}
	OnAll(8899)
	if !installed(t, "claude") {
		t.Fatal("start 没把 shim 装回去 —— claude 会静默直连")
	}
}

// TestOffPersistsIntent 显式 off 过的 agent，全面接管时必须跳过。
// 否则用户「我不要 newgate 管 claude」的决定会被下一次 start 悄悄推翻。
func TestOffPersistsIntent(t *testing.T) {
	isolate(t)

	if r := On("claude", 8899); r.Err != nil {
		t.Fatalf("On: %v", r.Err)
	}
	if r := Off("claude"); r.Err != nil {
		t.Fatalf("Off: %v", r.Err)
	}
	if installed(t, "claude") {
		t.Fatal("Off 之后 shim 还在")
	}
	if st := store.LoadState(); st.TakeoverWanted("claude") {
		t.Fatal("Off 没记住意愿")
	}

	OnAll(8899)
	if installed(t, "claude") {
		t.Fatal("全面接管无视了用户显式 off —— 用户的决定被推翻了")
	}

	// 再 on 回来，意愿要恢复
	if r := On("claude", 8899); r.Err != nil {
		t.Fatalf("重新 On: %v", r.Err)
	}
	if !installed(t, "claude") {
		t.Fatal("重新 On 没装上")
	}
	OffAll()
	OnAll(8899)
	if !installed(t, "claude") {
		t.Fatal("意愿恢复后，start 应该重新接管")
	}
}

// TestOffAllLeavesForeignEntriesAlone shim 目录里用户自己放的东西一律不动。
//
// 具体现场：用户为了绕过这个 bug，手工把 shim 重命名成 claude-bak 留着。
// 修 bug 的过程中如果 stop 把它删了，等于我们替用户销毁了他的应急手段。
func TestOffAllLeavesForeignEntriesAlone(t *testing.T) {
	isolate(t)

	if r := On("claude", 8899); r.Err != nil {
		t.Fatalf("On: %v", r.Err)
	}
	// 用户手工留的备份链接，以及一个自己写的脚本
	bak := filepath.Join(injection.Dir(), "claude-bak")
	self, _ := os.Executable()
	if err := os.Symlink(self, bak); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(injection.Dir(), "my-wrapper")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nexec claude \"$@\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	// 名字不是已知 agent → 不算我们装的
	if installed(t, "claude-bak") || installed(t, "my-wrapper") {
		t.Fatal("Installed() 把用户自己的东西算成了我们的 shim")
	}
	fg := injection.Foreign()
	if len(fg) != 2 {
		t.Fatalf("Foreign() = %v，想要 claude-bak 和 my-wrapper", fg)
	}

	OffAll()
	for _, p := range []string{bak, script} {
		if _, err := os.Lstat(p); err != nil {
			t.Fatalf("stop 删掉了用户自己的 %s：%v", filepath.Base(p), err)
		}
	}
	if err := injection.Uninstall("claude-bak"); err == nil {
		t.Fatal("Uninstall 愿意删非已知 agent 名的条目")
	}
}

// TestUnknownAgent 打错名字要给出可用列表，不能静默成功。
func TestUnknownAgent(t *testing.T) {
	isolate(t)

	r := On("claud", 8899) // 少个 e
	if r.Err == nil {
		t.Fatal("接管了一个不存在的 agent")
	}
	if r := Off("claud"); r.Err == nil {
		t.Fatal("释放了一个不存在的 agent")
	}
	// 拼错不该在 state.json 里留下垃圾条目
	if st := store.LoadState(); len(st.Takeover) != 0 {
		t.Fatalf("打错的名字被写进了 state：%v", st.Takeover)
	}
}

// TestListReportsWantedVsActual status 要能看出「想接管」和「真接管了」的差异
// ——两者不一致正是故障现场，藏起来就没法排查了。
func TestListReportsWantedVsActual(t *testing.T) {
	isolate(t)

	find := func(agent string) Status {
		t.Helper()
		for _, s := range List() {
			if s.Agent == agent {
				return s
			}
		}
		t.Fatalf("List() 里没有 %s", agent)
		return Status{}
	}

	s := find("claude")
	if s.Mechanism != MechShim {
		t.Fatalf("claude 的机制应该是 shim，得到 %q", s.Mechanism)
	}
	if !s.Wanted || s.Active {
		t.Fatalf("初始应该是 想要=true 实际=false，得到 %+v", s)
	}

	On("claude", 8899)
	if s := find("claude"); !s.Active {
		t.Fatal("装了 shim，List 却说没接管")
	}

	// stop 之后：想要仍为 true、实际为 false —— 正是「该补齐」的现场
	OffAll()
	if s := find("claude"); !s.Wanted || s.Active {
		t.Fatalf("stop 后应该是 想要=true 实际=false，得到 %+v", s)
	}
}
