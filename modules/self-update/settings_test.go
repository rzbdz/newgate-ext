package selfupdate

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/rzbdz/newgate/modules/config/domain"
	"github.com/rzbdz/newgate/modules/config/paths"
	"github.com/rzbdz/newgate/modules/config/store"
	"github.com/rzbdz/newgate/testing/testkit"
)

// fakeRepo 造一个「看起来是发行版仓库」的目录：checkRepo 只看这三样。
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
	// 测试的临时目录本来就在 TMPDIR 下，而 infer 不信临时目录里的源码（那是
	// `upgrade@<ref>` 的临时 clone）。把 TMPDIR 挪开，repo 才像一份正常的 checkout。
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

// 第一次运行时写下决定，但**只在 state.json 已经存在时**：抢在 `newgate init`
// 之前写一份，会让 init 以为状态已经初始化过了，于是不种端口与缺省 profile。
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

	// 别人的字段不能被这一段的写入盖掉。
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
