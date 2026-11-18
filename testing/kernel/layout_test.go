package kernel

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// TestEveryConcreteModuleHasStandardEntryFile pins two directory rules:
//
//  1. Every directory under modules/ is a concrete component and must expose a root
//     module.go - after the composition root (once modules/builtin, now app) moved out
//     of modules/, this rule has no exceptions.
//  2. Public capabilities and interfaces live directly in the module root's api.go, no
//     <module>/api subpackage (since 2026-09-17). Such a subpackage means someone
//     split contract from implementation again - the regression this rule stops.
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
