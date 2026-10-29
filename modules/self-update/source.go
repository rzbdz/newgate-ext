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

// 本文件是源码那条路：git 模式在本地 checkout 里同步 + 编，`@<ref>` 没有可用
// Release 时在临时目录里 clone + 编。两者编的都是 `build/build.sh`——**唯一的构建
// 入口**（发行版 CLAUDE.md §3），这里不自己拼 go build：版本号、ldflags、静态断言、
// 生成清单，全都只在那一处。

// gitEnv 让 git 永远不停下来问人：没有凭据时 clone/fetch 应该**失败**，而不是在
// 一个看不见终端的子进程里等一个永远不会来的密码。
func gitEnv() []string {
	return append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_ASKPASS=true")
}

// git 在 dir 里跑一条 git，返回去掉首尾空白的 stdout；失败时错误带上 stderr 的
// 第一行（那通常就是原因：not a git repository / couldn't find remote ref）。
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

// requireTools 在动手之前核对源码构建要的工具都在 PATH 上。缺一个就早早说清楚
// ——否则会在 clone 了几十 MB 之后才死在 build.sh 的某一行。
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
	return nil
}

// repoState 是一份 checkout 此刻的样子（同步前后各取一次）。
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
	// 未跟踪的文件不算脏：它们不进构建（build.sh 的 describe --dirty 同一条判据）。
	if out, err := git(ctx, repo, "status", "--porcelain", "--untracked-files=no"); err == nil && out != "" {
		s.dirty = true
	}
	return s
}

// syncRepo 把 git 模式的 checkout 带到「能编的最新」：fetch，然后**只在安全时**快进。
//
// 这是开发者正在用的树，所以规矩是「绝不替人做决定」：
//
//   - 工作区有改动 → 不动（编的就是他手上这份，版本号会带 -dirty）；
//   - 本地有没推的提交、和上游分叉 → 不动（快进做不到，合并/变基是他的事）；
//   - detached、没有上游 → 不动；
//   - 只落后 → `merge --ff-only`，再把 submodule 带到新的 gitlink 上——**前提是它
//     本来就在旧 gitlink 上**。core/ 停在别处（正在改内核）时 update 会把它切走，
//     那就只说一声。
//
// check 为真时只 fetch（只动远端跟踪分支，不碰工作区）并报告。返回给人看的几行。
func syncRepo(ctx context.Context, repo string, check bool) []string {
	var notes []string
	fctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	_, ferr := git(fctx, repo, "fetch", "--tags", "--prune", "origin")
	cancel()
	if ferr != nil {
		// fetch 失败不致命：离线的开发机照样能编手上那份。
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

// submodulesInSync：每个 submodule 都正好停在 gitlink 记的那个提交上。
// `git submodule status` 的行首 `+` = 停在别处，`-` = 没初始化，`U` = 冲突。
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

// describe 是 build.sh 会写进版本号的那一段（与它同一条命令）。
func describe(ctx context.Context, repo string) string {
	out, err := git(ctx, repo, "describe", "--tags", "--always", "--dirty")
	if err != nil {
		return ""
	}
	return out
}

// buildRepo 在 repo 里跑 build/build.sh，只编这份规格书 × 本平台，返回产物路径
// 与清理函数。构建输出原样流到 log（几十秒到几分钟，人要看得见它在动）。
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

// buildEnv 把「编哪份、编哪个平台」钉死：环境里残留的 NEWGATE_ALL / NEWGATE_DISTS
// 在 build.sh 里**优先级更高**，不剥掉的话会编出一堆别的规格书，而产物数对不上。
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

// commitish 是「看起来像一个提交号」：ls-remote 只列 ref，查不到提交。
var commitish = regexp.MustCompile(`^[0-9a-f]{7,40}$`)

// refExists 在 clone 之前问一句远端有没有这个 tag/分支（ls-remote 只传 ref 表，
// 几 KB）。敲错一个版本号不该先花一分钟 clone 再报错——--check 尤其要答对。
// 提交号问不出来（ls-remote 不列提交），放行给 clone 去判。
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

// cloneRef 把 url 的 ref（tag、分支或提交）clone 进一个临时目录，带上 submodule。
//
// 先试浅 clone（tag/分支一步到位）；ref 是个提交号时 `--branch` 认不得，退回完整
// clone + checkout。submodule 同样先浅后深：gitlink 指的提交不一定是对方某个分支
// 的头，有的服务器不让按提交号浅取。
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

	if _, err := git(cctx, tmp, "clone", "--quiet", "--depth", "1", "--branch", ref, url, src); err != nil {
		_ = os.RemoveAll(src)
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
