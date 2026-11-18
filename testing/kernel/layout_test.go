package kernel

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// TestEveryConcreteModuleHasStandardEntryFile 锁住两条目录规则：
//
//  1. modules/ 下每个目录都是一个具体组件，必须暴露根 module.go——组合根
//     （曾经的 modules/builtin，现在的 app）搬出 modules/ 之后，这条规则
//     没有例外了。
//  2. 公开 capability 和接口直接写在模块根目录的 api.go 里，不建
//     <module>/api 子包（2026-09-17 起）。子包一旦出现，说明有人把契约
//     和实现重新拆开了——这正是本条规则要拦住的回退。
func TestEveryConcreteModuleHasStandardEntryFile(t *testing.T) {
	_, current, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve test source path")
	}
	_ = current
	dir := modulesDir(t)
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		module := entry.Name()
		modulePath := filepath.Join(dir, module)

		if _, err := os.Stat(filepath.Join(modulePath, "module.go")); err != nil {
			t.Errorf("%s: concrete module must expose root module.go: %v", module, err)
		}
		if info, err := os.Stat(filepath.Join(modulePath, "api")); err == nil && info.IsDir() {
			t.Errorf("%s: capability contracts must live in root api.go, not an api/ subpackage", module)
		}
	}
}
