package gateway

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// The data plane knows no policy - today this rule is a **test**, not a comment.
//
// In the 2026-09-18 round that inverted breaker from "gateway's library" to
// "gateway's consumer", the criterion was written as one mechanically checkable line:
//
//	command grep -rn "breaker" modules/gateway/forward/
//	modules/gateway/module.go modules/gateway/serve.go
//
// must be 0 lines. Why it is worth a test: **break it and compilation still passes, unit
// tests still go green, e2e still fully green** - the breaker → gateway dependency edge
// is still there, one import regrows it, and behavior does not change a word. That is how
// it looked before the inversion: a Health field on `forward.Server`, the hot path writing
// `breaker.KindConnError` directly and branching on `BucketShape`, so "who counts as a
// failure", "which ledger records it" and "what trace is kept" - all **policy** - grew inside
// the kernel's ifs, with no trace on the dependency graph. The only guard was "does anyone look".
//
// Two criteria, written separately because their reasons differ:
//
//  1. **The name does not appear in data plane sources** (all of forward/ + module.go +
//     serve.go). Not "does not import" - it does not even know the name: if policy changes
//     implementation or vocabulary later, the data plane does not change a word. Today
//     it speaks through policy.Registry's four decision points. 2. **modules/gateway does
//     not import the policy package itself** (`.../modules/breaker"`, the one holding the
//     logic). `.../modules/breaker/status` is a **wire leaf**: during graceful handoff
//     old and new binaries run together, and the control-plane document shape is a
//     cross-version public contract; docs/03-architecture.md §2 explicitly allows importing
//     it directly, not as a dependency edge (that is how command_metrics.go uses it).
//
// **Test files are not bound by criterion 1**: they reference the word a lot, precisely
// to explain "why we do not import it" (see that passage in forward/testutil_test.go), and
// changing that would lose the evidence. So `_test.go` is skipped here - this file included.
func TestTheDataPlaneDoesNotNameAnyStrategy(t *testing.T) {
	root := gatewayRoot(t)

	named := map[string]bool{"module.go": true, "serve.go": true}
	for _, path := range sourceFiles(t, root) {
		rel, err := filepath.Rel(root, path)
		if err != nil {
			t.Fatalf("computing the relative path failed %s: %v", path, err)
		}
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("cannot read %s: %v", path, err)
		}
		text := string(body)

		// criterion 1 covers only these three places: all of forward/, and the two root files.
		if named[rel] || strings.HasPrefix(filepath.ToSlash(rel), "forward/") {
			for i, line := range strings.Split(text, "\n") {
				if strings.Contains(line, "breaker") {
					t.Errorf("%s:%d a policy name appears in the data-plane source (criterion 1) - "+
						"this is the first step toward growing policy back into ifs in the kernel:\n    %s\n"+
						"   route decisions through the four doors of policy.Registry (see modules/gateway/policy)",
						rel, i+1, strings.TrimSpace(line))
				}
			}
		}

		// criterion 2 covers the whole subtree: importing the package
		// that holds the logic = the dependency edge grew back.
		//
		// Splitting the needle into two halves and joining them is **deliberate**: otherwise
		// this file would itself contain that literal, `command grep -rln 'modules/breaker"'
		// modules/gateway/` would report itself, and the criterion could never reach zero.
		if strings.Contains(text, `"github.com/rzbdz/newgate/modules/`+`breaker"`) {
			t.Errorf("%s imports the policy package directly (criterion 2) - "+
				"the direction should be policy Needs(gateway) and RegisterFilter, not the data plane pulling it in", rel)
		}
	}
}

// TestTheDataPlaneDoesNotKnowThePortSharingMechanism is the second use of the same
// ruler: **the data plane does not know about "several services sharing one port"**.
//
// The first implementation on 2026-09-20 put a porthub lookup into the catch-all
// (`forward.dispatch`); it compiled, forwarding worked, unit tests were green - the only
// symptom was that the data plane now knew this process's deployment shape: who else lives
// on the port, who catches which path first. That is **daemon entry** knowledge (it hands
// the data plane's handler to porthub and gets back this port's root handler, see serve.go).
//
// The criterion, like the policy one, is "not even the name appears", scoped to
// forward/ only: serve.go and module.go **must** know it (that is where composition
// happens), which is why those two files of criterion 1 are excluded here.
func TestTheDataPlaneDoesNotKnowThePortSharingMechanism(t *testing.T) {
	root := gatewayRoot(t)
	dir := filepath.Join(root, "forward")
	checked := 0
	for _, path := range sourceFiles(t, dir) {
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("cannot read %s: %v", path, err)
		}
		checked++
		for i, line := range strings.Split(string(body), "\n") {
			if strings.Contains(line, "porthub") {
				t.Errorf("%s:%d a port-sharing mechanism name appears in the data-plane source - "+
					"the data plane should receive one handler, not know where it came from or who else is on the port:\n    %s\n"+
					"   keep composition at the daemon entry point (the rootHandler part of serve.go)",
					path, i+1, strings.TrimSpace(line))
			}
		}
	}
	if checked == 0 {
		t.Fatal("not a single source file was scanned under forward/ - the criterion becomes a no-op")
	}
}

// gatewayRoot is the absolute path of modules/gateway (this file's directory).
func gatewayRoot(t *testing.T) string {
	t.Helper()
	_, this, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller cannot get the path of this file")
	}
	return filepath.Dir(this)
}

// sourceFiles lists the non-test sources in the gateway subtree.
// Test files are deliberately excluded (see the passage above).
func sourceFiles(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		name := d.Name()
		if d.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			return nil
		}
		out = append(out, path)
		return nil
	})
	if err != nil {
		t.Fatalf("walking %s failed: %v", root, err)
	}
	if len(out) == 0 {
		t.Fatalf("not a single source file was found under %s - the criterion becomes a no-op", root)
	}
	return out
}
