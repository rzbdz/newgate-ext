// Package kernel holds the invariant tests that need **a real product graph**.
//
// They used to live in the kernel's app package, which was only possible while the
// kernel shipped the product modules. Now the kernel ships none, so "the graph
// installs, the removal matrix holds, every module has a module.go/api.go" is a
// statement about a distribution — this one. The kernel-side invariants (the
// composition root knows no module, installs nothing on its own) stay in
// core/app and need no modules at all.
package kernel

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/rzbdz/newgate-ext/manifest"
	modules "github.com/rzbdz/newgate/component"
)

// graphComponents is the component set of one spec, loaded without assembling it.
func graphComponents(t *testing.T, spec string) []modules.Component {
	t.Helper()
	sel, ok := manifest.Specs()[spec]
	if !ok {
		t.Fatalf("no spec %q (have: %v)", spec, manifest.SpecNames())
	}
	return sel
}

// kernelGraph is the flagship spec's component set.
func kernelGraph(t *testing.T) []modules.Component { return graphComponents(t, manifest.DefaultSpec) }

// repoRoot is the distribution's repository root (two levels above this package).
func repoRoot(t *testing.T) string {
	t.Helper()
	_, current, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot resolve test source path")
	}
	return filepath.Dir(filepath.Dir(filepath.Dir(current)))
}

// modulesDir is the distribution's module directory.
func modulesDir(t *testing.T) string { return filepath.Join(repoRoot(t), "modules") }

var _ = os.ReadDir

// providesPort reports whether a component provides the named port.
//
// It is the same test the kernel's app.Selection.Load makes when it refuses to let
// the composition root's own port be disabled — kept here so the matrix asserts
// the property, not a module name.
func providesPort(c modules.Component, port string) bool {
	for _, p := range c.Provides {
		if p.Name() == port {
			return true
		}
	}
	return false
}
