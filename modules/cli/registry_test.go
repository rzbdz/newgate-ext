package cli

import (
	"strings"
	"testing"
)

// 账本在界面的 service 上，这三本账的不变量就守在这里。
//
// 这条测试锁的是一个**曾经真实存在**的静默 bug（2026-09-17 实测）：
// 在组件框架还允许 `Provides: Provide(别人的能力, 值)` 的年代，两个模块认领
// 同一个命令名时**先到先得，没有任何报错**——后注册的那条永远不会被分派到，
// 而它的作者以为自己装上了。现在贡献必须走 RegisterCommand，撞名当场报错。
func TestRegisterCommandRejectsDuplicateName(t *testing.T) {
	s := &service{}

	if _, err := s.RegisterCommand(fakeCommand{names: []string{"omo", "omo-alias"}}); err != nil {
		t.Fatalf("第一次注册应当成功: %v", err)
	}

	// 撞在**别名**上也算撞：Names 里的每个名字都是分派键，不能只看第一个。
	_, err := s.RegisterCommand(fakeCommand{names: []string{"other", "omo-alias"}})
	if err == nil {
		t.Fatal("第二次注册撞了命令名却没报错——这正是要锁住的静默行为")
	}
	if !strings.Contains(err.Error(), "omo-alias") {
		t.Errorf("报错应点名冲突的命令名，实际: %v", err)
	}

	// 失败的那次不能留下痕迹：账本里仍然只有第一条。
	if got := len(s.commands.All()); got != 1 {
		t.Errorf("注册失败后账本条目 = %d, want 1（校验不通过就不该插入）", got)
	}
	if _, ok := s.moduleCommand("other"); ok {
		t.Error("注册失败的命令名却能分派到——校验和插入不是原子的")
	}
}

// Release 必须精确撤销**自己那一条**，不是「清空」也不是「删最后一个」。
func TestRegisterCommandReleaseRemovesOnlyItsOwn(t *testing.T) {
	s := &service{}

	first, err := s.RegisterCommand(fakeCommand{names: []string{"a"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.RegisterCommand(fakeCommand{names: []string{"b"}}); err != nil {
		t.Fatal(err)
	}

	if err := first(); err != nil {
		t.Fatalf("撤销报错: %v", err)
	}
	if _, ok := s.moduleCommand("a"); ok {
		t.Error("撤销后 a 仍可分派")
	}
	if _, ok := s.moduleCommand("b"); !ok {
		t.Error("撤销 a 却把 b 也带走了")
	}

	// 陈旧 Release 必须是无害的 no-op：组件的 Stop 可能被调用不止一次，
	// 第二次释放时目标已经没了，不能报错。
	if err := first(); err != nil {
		t.Errorf("重复撤销应当是无害的 no-op，实际报错: %v", err)
	}
	if _, ok := s.moduleCommand("b"); !ok {
		t.Error("陈旧 Release 把别人的条目删掉了")
	}
}

func TestRegisterCommandRejectsEmptyContribution(t *testing.T) {
	s := &service{}
	if _, err := s.RegisterCommand(nil); err == nil {
		t.Error("nil 命令应当被拒绝")
	}
	if _, err := s.RegisterCommand(fakeCommand{}); err == nil {
		t.Error("没有 Names 的命令应当被拒绝（它永远分派不到）")
	}
}

// 诊断是可叠加的：没有键命名空间，所以不查重，两条都留着。
func TestRegisterDiagnosticsIsAdditive(t *testing.T) {
	s := &service{}
	for i := 0; i < 2; i++ {
		if _, err := s.RegisterDiagnostics(fakeDiagnostics{line: "x"}); err != nil {
			t.Fatalf("第 %d 次注册诊断应当成功: %v", i+1, err)
		}
	}
	if got := len(s.moduleDiagnostics()); got != 2 {
		t.Errorf("诊断条目 = %d, want 2（诊断不该查重）", got)
	}
}

type fakeCommand struct{ names []string }

func (c fakeCommand) Names() []string      { return c.names }
func (fakeCommand) Run(Host, []string) int { return 0 }

type fakeDiagnostics struct{ line string }

func (d fakeDiagnostics) Diagnostics() []Diagnostic {
	return []Diagnostic{{Label: "fake", Line: d.line}}
}
