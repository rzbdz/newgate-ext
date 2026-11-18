package selfupdate

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rzbdz/newgate-ext/modules/config/paths"
	"github.com/rzbdz/newgate-ext/testing/testkit"

	"github.com/rzbdz/newgate-ext/manifest/distinfo"
	upgradeapi "github.com/rzbdz/newgate-ext/modules/upgrade"
)

// A full git-mode round, all offline: a bare repo as origin, a developer checkout, and a
// fake build/build.sh (following the real script's contract: artifact name
// newgate-<distribution>-<os>-<arch>, version <distribution>-<git describe>).

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

	// Upstream moves one commit ahead: the developer's copy is 1 behind.
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
	// One commit after v0.1.0 - the one just pulled.
	got, err := smoke(context.Background(), target)
	if err != nil || !strings.HasPrefix(got, "default-v0.1.0-1-g") {
		t.Fatalf("installed %q (%v), want default-v0.1.0-1-g…\n%s", got, err, out.String())
	}
	if !strings.Contains(out.String(), "fast-forwarded") {
		t.Fatalf("the pull was not reported:\n%s", out.String())
	}

	// Same commit again: no build.
	u.current = got
	msg, err := u.runMsg(context.Background(), upgradeapi.Request{})
	if err != nil || !strings.Contains(msg, "already at") {
		t.Fatalf("second run: %q %v", msg, err)
	}

	// The developer has changes, upstream moved: no pull, build what is at hand (-dirty).
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

// A temporary clone of `upgrade@<ref>` must carry the tag and the history leading to it:
// the version comes from describe, and a shallow clone has only a commit id, so a later
// upgrade cannot compare old and new. Branch, tag and commit refs must all clone.
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
