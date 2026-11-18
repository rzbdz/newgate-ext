package selfupdate

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/rzbdz/newgate-ext/modules/config/domain"
	"github.com/rzbdz/newgate-ext/modules/config/paths"
	"github.com/rzbdz/newgate-ext/modules/config/store"
	"github.com/rzbdz/newgate-ext/testing/testkit"
)

// fakeRepo makes a dir that "looks like a distribution repo": checkRepo looks at these three.
func fakeRepo(t *testing.T, dir string) {
	t.Helper()
	for _, p := range []string{".git", "build"} {
		if err := os.MkdirAll(filepath.Join(dir, p), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, f := range []string{"build/build.sh", "dist.json"} {
		if err := os.WriteFile(filepath.Join(dir, f), []byte("x"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
}

func TestInfer(t *testing.T) {
	repo := t.TempDir()
	fakeRepo(t, repo)
	// The test temp dir is under TMPDIR, and infer distrusts sources in temp dirs (those
	// are `upgrade@<ref>` temp clones). Move TMPDIR away, and the repo looks like a checkout.
	t.Setenv("TMPDIR", filepath.Join(t.TempDir(), "elsewhere"))

	if s, sure := infer(repo, "dist.json"); !sure || s.Mode != modeGit || s.Repo != repo {
		t.Fatalf("own checkout: %+v sure=%v, want git %s", s, sure, repo)
	}
	if s, sure := infer("/nonexistent/runner/work/newgate-ext", "dist.json"); !sure || s.Mode != modeRelease {
		t.Fatalf("CI path: %+v sure=%v, want release", s, sure)
	}
	if _, sure := infer("", "dist.json"); sure {
		t.Fatal("no injected source (go run / tests) must not decide anything")
	}
	// snode1's live case: /root exists but is 0700, and an ordinary user stat gets EACCES.
	locked := filepath.Join(repo, "locked")
	if err := os.MkdirAll(filepath.Join(locked, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(locked, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })
	if os.Geteuid() != 0 { // root 不受权限位约束，测不出这一条
		if s, sure := infer(filepath.Join(locked, "src"), "dist.json"); !sure || s.Mode != modeRelease {
			t.Fatalf("unreadable source: %+v sure=%v, want a recorded release decision", s, sure)
		}
	}
	if s, sure := infer(repo, "dist-missing.json"); !sure || s.Mode != modeRelease {
		t.Fatalf("a checkout without this specification is not ours: %+v sure=%v", s, sure)
	}
}

func TestInferDistrustsTempDirs(t *testing.T) {
	repo := t.TempDir() // 在 TMPDIR 下
	fakeRepo(t, repo)
	if _, sure := infer(repo, "dist.json"); sure {
		t.Fatal("a checkout under TMPDIR is an upgrade@<ref> clone; it must not be recorded")
	}
}

// First run writes down its decision, but **only if state.json already exists**:
// writing one ahead of `newgate init` makes init think state is set up and seed nothing.
func TestSettingsPersistOnlyIntoAnExistingState(t *testing.T) {
	testkit.Sandbox(t)
	if err := paths.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	if err := saveSettings(settings{Mode: modeGit, Repo: "/src", DecidedBy: "user"}); err != nil {
		t.Fatal(err)
	}
	got, ok := loadSettings()
	if !ok || got.Mode != modeGit || got.Repo != "/src" {
		t.Fatalf("round trip = %+v, %v", got, ok)
	}

	// Other people's fields must not be clobbered by this section's write.
	st := store.LoadState()
	st.DefaultProfile = "work"
	if err := store.SaveState(st); err != nil {
		t.Fatal(err)
	}
	if err := saveSettings(settings{Mode: modeRelease}); err != nil {
		t.Fatal(err)
	}
	if store.LoadState().DefaultProfile != "work" {
		t.Fatal("saving the self-update section dropped another field")
	}
}

func TestEnsureSettingsDoesNotCreateState(t *testing.T) {
	testkit.Sandbox(t)
	if err := paths.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	old := sourceDir
	sourceDir = func() string { return "/nonexistent/ci/checkout" } // 有把握的推断：release
	t.Cleanup(func() { sourceDir = old })

	if _, saved := ensureSettings(); saved {
		t.Fatal("ensureSettings wrote a state.json that did not exist")
	}
	if _, err := os.Stat(paths.StateFile()); err == nil {
		t.Fatal("state.json appeared before newgate init")
	}

	if err := store.SaveState(&domain.State{DefaultProfile: "ds", Port: 8899}); err != nil {
		t.Fatal(err)
	}
	s, saved := ensureSettings()
	if !saved || s.Mode != modeRelease || s.DecidedBy != "first-run" {
		t.Fatalf("after init: %+v saved=%v, want a recorded first-run release decision", s, saved)
	}
}
