package selfupdate

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	i18n "github.com/rzbdz/newgate/lib/i18n"
)

// This file is the source route: git mode syncs + builds in the local checkout;
// `@<ref>` with no usable Release clones + builds in a temp dir. Both go through
// `build/build.sh` - the **only build entry point** (distribution CLAUDE.md §3).
// No go build assembled here: version, ldflags, static asserts, manifest - one place.

// gitEnv keeps git from ever stopping to ask a human: without credentials
// clone/fetch must **fail**, not wait in a terminal-less child for a password.
func gitEnv() []string {
	return append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_ASKPASS=true")
}

// git runs one git command in dir and returns trimmed stdout; on failure the
// error carries stderr's first line (the cause: not a git repository / couldn't find remote ref).
func git(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	cmd.Env = gitEnv()
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		msg := firstLine(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return "", i18n.E("git {args}: {msg}", i18n.A{"args": strings.Join(args, " "), "msg": msg})
	}
	return strings.TrimSpace(stdout.String()), nil
}

// requireTools checks before doing anything that the tools a source build needs
// are on PATH. Missing one is said early - else it dies in build.sh after tens of MB.
func requireTools() error {
	var missing []string
	for _, t := range []string{"git", "go", "bash", "python3"} {
		if _, err := exec.LookPath(t); err != nil {
			missing = append(missing, t)
		}
	}
	if len(missing) > 0 {
		return i18n.E("building from source needs {tools} on PATH", i18n.A{"tools": strings.Join(missing, ", ")})
	}
	return checkGo()
}

// minGo is the oldest go command that can build this repo: since 1.21, go reads
// go.mod's `go` line and fetches that toolchain (GOTOOLCHAIN=auto); older ones error out.
//
// 2026-10-29 on snode1: PATH had go1.13.8, go.mod says 1.27 - unguarded,
// `upgrade@<ref>` clones tens of MB, then dies in `go run ./tools/distgen`.
const minGo = 21

var goVersionRe = regexp.MustCompile(`go1\.(\d+)`)

func checkGo() error {
	out, err := exec.Command("go", "version").Output()
	if err != nil {
		return i18n.Ef(err, "cannot run `go version`: {err}", nil)
	}
	m := goVersionRe.FindStringSubmatch(string(out))
	if m == nil {
		return nil // 认不出来（自编的 devel 版）就放行，让构建自己说话
	}
	if minor, _ := strconv.Atoi(m[1]); minor < minGo {
		return i18n.E("{go} is too old to build newgate (needs go1.{min} or newer, which fetches the toolchain go.mod asks for); install a newer Go, or use a release",
			i18n.A{"go": strings.TrimSpace(strings.TrimPrefix(string(out), "go version ")), "min": minGo})
	}
	return nil
}

// repoState is what a checkout looks like right now (taken before and after sync).
type repoState struct {
	branch   string // 空 = detached
	upstream string // 空 = 没有上游
	ahead    int
	behind   int
	dirty    bool
}

func readRepoState(ctx context.Context, repo string) repoState {
	var s repoState
	s.branch, _ = git(ctx, repo, "symbolic-ref", "-q", "--short", "HEAD")
	if s.branch != "" {
		s.upstream, _ = git(ctx, repo, "rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{u}")
	}
	if s.upstream != "" {
		if out, err := git(ctx, repo, "rev-list", "--left-right", "--count", "HEAD...@{u}"); err == nil {
			if f := strings.Fields(out); len(f) == 2 {
				s.ahead, _ = strconv.Atoi(f[0])
				s.behind, _ = strconv.Atoi(f[1])
			}
		}
	}
	// Untracked files do not count as dirty: they miss the build (same rule as describe --dirty).
	if out, err := git(ctx, repo, "status", "--porcelain", "--untracked-files=no"); err == nil && out != "" {
		s.dirty = true
	}
	return s
}

// syncRepo brings a git-mode checkout to "the newest buildable": fetch, then fast-forward
// **only when it is safe**. It is the developer's tree: decide nothing for them.
//
//   - working tree changed -> leave it (built as-is; the version gets -dirty);
//   - unpushed commits or a fork from upstream -> leave it (merge/rebase is theirs);
//   - detached, no upstream -> leave it;
//   - only behind -> `merge --ff-only`, then move the submodule onto the new gitlink,
//     **provided it was on the old one**. If core/ sits elsewhere (kernel work in progress)
//     update would switch it away, so just say so.
//
// With check true, only fetch (remote-tracking refs, never the tree) and report it.
// Returns the lines for humans.
func syncRepo(ctx context.Context, repo string, check bool) []string {
	var notes []string
	fctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	_, ferr := git(fctx, repo, "fetch", "--tags", "--prune", "origin")
	cancel()
	if ferr != nil {
		// A failed fetch is not fatal: an offline dev machine still builds what it has.
		notes = append(notes, i18n.T("fetch failed, building what is checked out: {err}", i18n.A{"err": ferr.Error()}))
	}
	st := readRepoState(ctx, repo)
	switch {
	case st.branch == "":
		notes = append(notes, i18n.T("detached HEAD: not pulling", nil))
	case st.upstream == "":
		notes = append(notes, i18n.T("{branch} has no upstream: not pulling", i18n.A{"branch": st.branch}))
	case st.behind == 0:
		if st.ahead > 0 {
			notes = append(notes, i18n.N("{branch} is {n} commit ahead of {upstream}",
				"{branch} is {n} commits ahead of {upstream}", st.ahead,
				i18n.A{"branch": st.branch, "n": st.ahead, "upstream": st.upstream}))
		}
	case st.dirty:
		notes = append(notes, i18n.N("{branch} is {n} commit behind {upstream}, but the working tree has changes: not pulling",
			"{branch} is {n} commits behind {upstream}, but the working tree has changes: not pulling", st.behind,
			i18n.A{"branch": st.branch, "n": st.behind, "upstream": st.upstream}))
	case st.ahead > 0:
		notes = append(notes, i18n.T("{branch} and {upstream} have diverged ({ahead} ahead, {behind} behind): not pulling",
			i18n.A{"branch": st.branch, "upstream": st.upstream, "ahead": st.ahead, "behind": st.behind}))
	case check:
		notes = append(notes, i18n.N("{branch} is {n} commit behind {upstream}; upgrade would fast-forward it",
			"{branch} is {n} commits behind {upstream}; upgrade would fast-forward it", st.behind,
			i18n.A{"branch": st.branch, "n": st.behind, "upstream": st.upstream}))
	default:
		subsInSync := submodulesInSync(ctx, repo)
		if _, err := git(ctx, repo, "merge", "--ff-only", "@{u}"); err != nil {
			notes = append(notes, i18n.T("fast-forward failed, building what is checked out: {err}", i18n.A{"err": err.Error()}))
			break
		}
		notes = append(notes, i18n.N("fast-forwarded {branch} by {n} commit", "fast-forwarded {branch} by {n} commits",
			st.behind, i18n.A{"branch": st.branch, "n": st.behind}))
		if !subsInSync {
			notes = append(notes, i18n.T("a submodule was not at its recorded commit before the pull; left it where it is", nil))
			break
		}
		uctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
		_, err := git(uctx, repo, "submodule", "update", "--init", "--recursive")
		cancel()
		if err != nil {
			notes = append(notes, i18n.T("submodule update failed: {err}", i18n.A{"err": err.Error()}))
		}
	}
	return notes
}

// submodulesInSync: every submodule sits exactly on the commit its gitlink records.
// A leading `+` in `git submodule status` = elsewhere, `-` = not initialized, `U` = conflict.
func submodulesInSync(ctx context.Context, repo string) bool {
	out, err := git(ctx, repo, "submodule", "status", "--recursive")
	if err != nil {
		return false
	}
	for _, line := range strings.Split(out, "\n") {
		if line != "" && (line[0] == '+' || line[0] == '-' || line[0] == 'U') {
			return false
		}
	}
	return true
}

// describe is the part build.sh writes into the version (the very same command).
func describe(ctx context.Context, repo string) string {
	out, err := git(ctx, repo, "describe", "--tags", "--always", "--dirty")
	if err != nil {
		return ""
	}
	return out
}

// buildRepo runs build/build.sh in repo, building only this spec x this platform,
// returning the artifact path and cleanup; output streams to log (one must see it move).
func buildRepo(ctx context.Context, repo, spec, goos, goarch string, log io.Writer) (string, func(), error) {
	out, err := os.MkdirTemp("", "newgate-build-")
	if err != nil {
		return "", func() {}, err
	}
	cleanup := func() { _ = os.RemoveAll(out) }
	bctx, cancel := context.WithTimeout(ctx, 30*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(bctx, "bash", filepath.Join(repo, "build", "build.sh"), out)
	cmd.Dir = repo
	cmd.Stdout, cmd.Stderr = log, log
	cmd.Env = buildEnv(spec, goos+"/"+goarch)
	if err := cmd.Run(); err != nil {
		cleanup()
		return "", func() {}, i18n.Ef(err, "build/build.sh failed: {err}", nil)
	}
	matches, _ := filepath.Glob(filepath.Join(out, "newgate-*-"+goos+"-"+goarch))
	if len(matches) != 1 {
		cleanup()
		return "", func() {}, i18n.E("build/build.sh produced {n} binaries for {platform}, expected one",
			i18n.A{"n": len(matches), "platform": goos + "/" + goarch})
	}
	return matches[0], cleanup, nil
}

// buildEnv pins down "which spec, which platform": stray NEWGATE_ALL / NEWGATE_DISTS
// in the environment **win** inside build.sh - unpeeled, they build other specs; count is wrong.
func buildEnv(spec, platform string) []string {
	var env []string
	for _, kv := range gitEnv() {
		switch {
		case strings.HasPrefix(kv, "NEWGATE_ALL="),
			strings.HasPrefix(kv, "NEWGATE_DISTS="),
			strings.HasPrefix(kv, "NEWGATE_DIST="),
			strings.HasPrefix(kv, "NEWGATE_PLATFORMS="):
			continue
		}
		env = append(env, kv)
	}
	return append(env, "NEWGATE_DIST="+spec, "NEWGATE_PLATFORMS="+platform)
}

// commitish is "looks like a commit id": ls-remote only lists refs, never commits.
var commitish = regexp.MustCompile(`^[0-9a-f]{7,40}$`)

// refExists asks the remote before cloning whether this tag/branch exists
// (ls-remote sends only the ref table, a few KB). A typo in a version must not cost
// a minute-long clone first - --check must answer right; a commit id goes to clone.
func refExists(ctx context.Context, url, ref string) error {
	if commitish.MatchString(ref) {
		return nil
	}
	lctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	out, err := git(lctx, os.TempDir(), "ls-remote", "--tags", "--heads", url, ref)
	if err != nil {
		return err
	}
	if out == "" {
		return i18n.E("{ref} is not a tag or branch of {url}", i18n.A{"ref": ref, "url": url})
	}
	return nil
}

// cloneRef clones url's ref (tag, branch or commit) into a temp dir, with submodules.
//
// No shallow clone: build.sh's version comes from `git describe --tags`; a shallow
// clone misses the tag and the history to it, leaving only a commit id
// (`default-d5899f6`), and later `upgrade`s cannot compare old and new. So use
// `--filter=blob:none` (all history, contents on demand) - describe works, download
// ~shallow; fall back to a full clone. `--branch` takes no commit: clone default, checkout.
//
// Submodules shallow first: the kernel version is not in the artifact name, so shallow
// suffices; a gitlink commit may be no branch head, so some servers refuse - go full.
func cloneRef(ctx context.Context, url, ref string, log io.Writer) (string, func(), error) {
	tmp, err := os.MkdirTemp("", "newgate-src-")
	if err != nil {
		return "", func() {}, err
	}
	cleanup := func() { _ = os.RemoveAll(tmp) }
	src := filepath.Join(tmp, "src")
	cctx, cancel := context.WithTimeout(ctx, 15*time.Minute)
	defer cancel()
	fmt.Fprintln(log, i18n.T("cloning {url} at {ref} into {dir}", i18n.A{"url": url, "ref": ref, "dir": src}))

	cloned := false
	for _, args := range [][]string{
		{"clone", "--quiet", "--filter=blob:none", "--branch", ref, url, src},
		{"clone", "--quiet", "--branch", ref, url, src},
	} {
		if _, err := git(cctx, tmp, args...); err == nil {
			cloned = true
			break
		}
		_ = os.RemoveAll(src)
	}
	if !cloned {
		if _, err := git(cctx, tmp, "clone", "--quiet", url, src); err != nil {
			cleanup()
			return "", func() {}, err
		}
		if _, err := git(cctx, src, "checkout", "--quiet", "--detach", ref); err != nil {
			cleanup()
			return "", func() {}, i18n.E("{ref} is not a tag, branch or commit of {url}",
				i18n.A{"ref": ref, "url": url})
		}
	}
	if _, err := git(cctx, src, "submodule", "update", "--init", "--recursive", "--depth", "1"); err != nil {
		if _, err := git(cctx, src, "submodule", "update", "--init", "--recursive"); err != nil {
			cleanup()
			return "", func() {}, err
		}
	}
	return src, cleanup, nil
}
