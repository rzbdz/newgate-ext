package selfupdate

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rzbdz/newgate/modules/config/paths"
	"github.com/rzbdz/newgate/testing/testkit"

	"github.com/rzbdz/newgate-ext/manifest/distinfo"
	upgradeapi "github.com/rzbdz/newgate-ext/modules/upgrade"
)

// git 模式的一整圈，全离线：一个裸仓库当 origin、一份开发者 checkout、一个假的
// build/build.sh（照真脚本的约定：产物名 newgate-<发行版>-<os>-<arch>，版本号
// <发行版>-<git describe>）。

const fakeBuild = `#!/usr/bin/env bash
set -e
out=$1
here=$(cd "$(dirname "$0")/.." && pwd)
rev=$(git -C "$here" describe --tags --always --dirty)
mkdir -p "$out"
art="$out/newgate-default-${NEWGATE_PLATFORMS%/*}-${NEWGATE_PLATFORMS#*/}"
printf '#!/bin/sh\necho "newgate default-%s"\n' "$rev" > "$art"
chmod +x "$art"
`

func run(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@example.com"}, args...)...)
	cmd.Env = gitEnv()
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
}

func TestGitModeFastForwardsBuildsAndInstalls(t *testing.T) {
	if err := requireTools(); err != nil {
		t.Skip(err)
	}
	testkit.Sandbox(t)
	if err := paths.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	origin := filepath.Join(root, "origin.git")
	seed := filepath.Join(root, "seed")
	dev := filepath.Join(root, "dev")
	run(t, root, "init", "-q", "--bare", "-b", "main", origin)
	run(t, root, "clone", "-q", origin, seed)
	write(t, filepath.Join(seed, "build", "build.sh"), fakeBuild)
	write(t, filepath.Join(seed, "dist.json"), "{}\n")
	write(t, filepath.Join(seed, "README"), "one\n")
	run(t, seed, "add", ".")
	run(t, seed, "commit", "-q", "-m", "one")
	run(t, seed, "tag", "v0.1.0")
	run(t, seed, "push", "-q", "origin", "HEAD:main", "--tags")
	run(t, root, "clone", "-q", origin, dev)

	// 上游又前进一个提交：开发者那份落后 1。
	write(t, filepath.Join(seed, "README"), "two\n")
	run(t, seed, "commit", "-q", "-am", "two")
	run(t, seed, "push", "-q", "origin", "HEAD:main")

	target := filepath.Join(t.TempDir(), "newgate")
	write(t, target, string(fakeBinary("default-v0.1.0")))
	var out strings.Builder
	u := &updater{
		st:       settings{Mode: modeGit, Repo: dev},
		info:     distinfo.Info{Spec: "dist.json", Distribution: "default"},
		current:  "default-v0.1.0",
		goos:     "linux",
		goarch:   "amd64",
		out:      &out,
		buildLog: io.Discard,
		client:   newReleaseClient,
		target:   func() (string, error) { return target, nil },
		restart:  func(string, io.Writer) (string, error) { return "ok", nil },
	}

	if _, err := u.run(context.Background(), upgradeapi.Request{}); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	// v0.1.0 之后 1 个提交——也就是拉下来的那个。
	got, err := smoke(context.Background(), target)
	if err != nil || !strings.HasPrefix(got, "default-v0.1.0-1-g") {
		t.Fatalf("installed %q (%v), want default-v0.1.0-1-g…\n%s", got, err, out.String())
	}
	if !strings.Contains(out.String(), "fast-forwarded") {
		t.Fatalf("the pull was not reported:\n%s", out.String())
	}

	// 同一个提交再来一次：不编。
	u.current = got
	msg, err := u.runMsg(context.Background(), upgradeapi.Request{})
	if err != nil || !strings.Contains(msg, "already at") {
		t.Fatalf("second run: %q %v", msg, err)
	}

	// 开发者改着东西、上游又前进了：不拉、照编手上这份（-dirty）。
	write(t, filepath.Join(dev, "README"), "local edit\n")
	write(t, filepath.Join(seed, "README"), "three\n")
	run(t, seed, "commit", "-q", "-am", "three")
	run(t, seed, "push", "-q", "origin", "HEAD:main")
	out.Reset()
	if _, err := u.run(context.Background(), upgradeapi.Request{}); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "not pulling") {
		t.Fatalf("a dirty tree must not be pulled:\n%s", out.String())
	}
	if got, _ := smoke(context.Background(), target); !strings.HasSuffix(got, "-dirty") {
		t.Fatalf("installed %q, want the dirty local build", got)
	}
	if b, _ := os.ReadFile(filepath.Join(dev, "README")); string(b) != "local edit\n" {
		t.Fatal("the developer's working tree was touched")
	}
}

// `upgrade@<ref>` 的临时 clone 必须带着 tag 与通往它的历史：版本号来自 describe，
// 浅 clone 只剩一个提交号，之后的 upgrade 就没法再比新旧。分支、tag、提交号三种
// ref 都要 clone 得出来。
func TestCloneRefKeepsDescribableHistory(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip(err)
	}
	root := t.TempDir()
	origin := filepath.Join(root, "origin.git")
	seed := filepath.Join(root, "seed")
	run(t, root, "init", "-q", "--bare", "-b", "main", origin)
	run(t, root, "clone", "-q", origin, seed)
	write(t, filepath.Join(seed, "README"), "one\n")
	run(t, seed, "add", ".")
	run(t, seed, "commit", "-q", "-m", "one")
	run(t, seed, "tag", "v0.1.0")
	write(t, filepath.Join(seed, "README"), "two\n")
	run(t, seed, "commit", "-q", "-am", "two")
	head := run(t, seed, "rev-parse", "HEAD")
	run(t, seed, "push", "-q", "origin", "HEAD:main", "--tags")

	url := "file://" + origin
	for ref, want := range map[string]string{
		"main":   "v0.1.0-1-g",
		"v0.1.0": "v0.1.0",
		head:     "v0.1.0-1-g",
	} {
		src, cleanup, err := cloneRef(context.Background(), url, ref, io.Discard)
		if err != nil {
			t.Fatalf("%s: %v", ref, err)
		}
		if got := describe(context.Background(), src); !strings.HasPrefix(got, want) {
			t.Errorf("%s: describe = %q, want prefix %q", ref, got, want)
		}
		cleanup()
	}
	if _, _, err := cloneRef(context.Background(), url, "no-such-ref", io.Discard); err == nil {
		t.Fatal("an unknown ref must fail")
	}
}
