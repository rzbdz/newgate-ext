package forward_test

import (
	"context"
	"os"
	"testing"

	"github.com/rzbdz/newgate-ext/manifest"
	"github.com/rzbdz/newgate-ext/modules/config/store"
	"github.com/rzbdz/newgate/app"
)

// TestMain is this package's only process-level assembly point (the internal and external test
// packages share one test binary, so a package-wide TestMain can only exist once).
//
// It does two things, **in this order**:
//
//  1. confine all tests into a temporary NEWGATE_HOME and lay down a minimal config;
//  2. start a real component graph (some cases verify whether a module got registered).
//
// Step 1 was added on 2026-09-18 because CI went red three times in a row: every case in this
// package going through `newTestServer()` had no Watch, so the handler degraded to
// `store.Load()` reading the disk directly -- reading **the real ~/.config/newgate on the
// developer machine**. A dev machine always has config, so locally it stayed green; on a clean
// runner Load failed, `snap()` returned nil, and every request became a 400 "[newgate] cannot
// read config", so the whole package went down together.
//
// The lesson is not "CI needs config", it is **tests must not depend on the machine running
// them**: config existing on that machine is a coincidence; what a case really needs is "a
// config that parses", so lay one down yourself. Individual cases can still override it with
// sandboxState(t, ...).
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "newgate-forward-test-")
	if err != nil {
		panic(err)
	}
	os.Setenv("NEWGATE_HOME", dir)
	if _, err := store.Init(false); err != nil {
		_ = os.RemoveAll(dir)
		panic("seeding the test config failed: " + err.Error())
	}

	// The graph is the distribution's flagship spec: this package tests how a
	// module behaves when the product actually installs it (an upstream patch's
	// signature is registered by another module, so it has to be present).
	sel := manifest.Specs()[manifest.DefaultSpec]
	built, err := app.New(context.Background(), sel)
	if err != nil {
		_ = os.RemoveAll(dir)
		panic(err)
	}
	code := m.Run()
	if err := built.Stop(context.Background()); err != nil && code == 0 {
		code = 1
	}
	_ = os.RemoveAll(dir)
	os.Exit(code)
}
