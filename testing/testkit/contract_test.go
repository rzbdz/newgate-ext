package testkit_test

import (
	"testing"

	agentapi "github.com/rzbdz/newgate-ext/modules/confighook"
	"github.com/rzbdz/newgate-ext/testing/testkit"
)

// 编译期锁住：假实现必须和真接口同步。接口加了方法而这里没跟上，
// 整包测试直接编译失败，而不是某个用例静默拿到零值。
var _ agentapi.AgentCatalog = (*testkit.FakeCatalog)(nil)

func TestSandboxIsolatesState(t *testing.T) {
	env := testkit.Sandbox(t)
	if got := env.Home; got == "" {
		t.Fatal("沙箱没给 NEWGATE_HOME")
	}
	if env.Targets == env.Home {
		t.Fatal("NEWGATE_TARGET_DIR 不该和 NEWGATE_HOME 同路径")
	}
}

func TestFakeCatalogNamesAreSorted(t *testing.T) {
	catalog := testkit.NewCatalog().Add(
		&agentapi.Agent{ID: "opencode"},
		&agentapi.Agent{ID: "claude"},
	)
	names := catalog.Names()
	if len(names) != 2 || names[0] != "claude" || names[1] != "opencode" {
		t.Fatalf("Names() = %v，期望按字母序 [claude opencode]", names)
	}
	if _, ok := catalog.Get("claude"); !ok {
		t.Fatal("Get(claude) 找不到")
	}
	if _, ok := catalog.Get("nope"); ok {
		t.Fatal("Get(nope) 不该找到")
	}
}
