package disttesting

import (
	"path/filepath"
	"testing"

	"github.com/rzbdz/newgate/tools/ciyaml"
)

// TestWorkflowsAreStructurallyValid is the distribution-side CI config ratchet.
//
// The criterion has one implementation (core/tools/ciyaml, same yardstick via replace),
// the same package's second entrance as the kernel's (core/app/ci_test.go). Why the
// distribution needs it: the kernel's ratchet sees only its own tree, and **the
// distribution's workflows are its own** - break any of three jobs (core-test / dist-test /
// zero-token e2e) and GitHub rejects the whole run at 0s: no job starts, nothing ran.
//
// core/ is checked too: a submodule, checked out in CI, and broken there mutes the round.
func TestWorkflowsAreStructurallyValid(t *testing.T) {
	root, err := filepath.Abs("..")
	if err != nil {
		t.Fatalf("locating the repo root: %v", err)
	}
	for _, sub := range []string{root, filepath.Join(root, "core")} {
		findings, err := ciyaml.Check(sub)
		if err != nil {
			t.Fatalf("cannot run the CI config check -- the check has degraded (did %s's workflow directory move?): %v", sub, err)
		}
		for _, f := range findings {
			t.Errorf("%s/%s", filepath.Base(sub), f)
		}
	}
}
