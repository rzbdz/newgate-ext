package selfupdate

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"runtime"
	"strings"

	"github.com/rzbdz/newgate/lib/buildinfo"
	i18n "github.com/rzbdz/newgate/lib/i18n"
	"github.com/rzbdz/newgate/lib/style"

	"github.com/rzbdz/newgate-ext/manifest/distinfo"
	upgradeapi "github.com/rzbdz/newgate-ext/modules/upgrade"
)

// 本文件是编排：按 mode 与 `@版本` 选一条路，走到「手上有一个文件」，交给 install.go。

// updater 把所有「外面的东西」做成字段，测试换掉它们（假 GitHub、临时目录里的
// 「当前二进制」、不真的去重启 daemon）。
type updater struct {
	st       settings
	info     distinfo.Info
	current  string // 这个二进制的版本号（buildinfo.Version）
	goos     string
	goarch   string
	out      io.Writer // 进度（给人看的几行）
	buildLog io.Writer // 构建/clone 的原始输出
	client   func(repo string) *releaseClient
	target   func() (string, error)
	restart  func(target string, log io.Writer) (string, error)
	// cloneBase 是源码构建 clone 的前缀（`https://github.com/` + owner/repo + `.git`）。
	cloneBase string
}

func newUpdater() *updater {
	st, _ := ensureSettings()
	return &updater{
		st:       st,
		info:     distinfo.Current(),
		current:  buildinfo.Version(),
		goos:     runtime.GOOS,
		goarch:   runtime.GOARCH,
		out:      os.Stdout,
		buildLog: os.Stderr,
		client:   newReleaseClient,
		target:   installTarget,
		restart:  restartDaemon,

		cloneBase: "https://github.com/",
	}
}

// github 是这次用的发布仓库：自己那段的覆盖值，否则这份规格书声明的。
func (u *updater) github() string {
	if u.st.GitHub != "" {
		return u.st.GitHub
	}
	return u.info.GitHub
}

func runAction(ctx context.Context, req upgradeapi.Request) (upgradeapi.Result, error) {
	return newUpdater().run(ctx, req)
}

func describeAction() string {
	st, _ := ensureSettings()
	if st.Mode == modeGit {
		return i18n.T("rebuild from {repo} with build/build.sh and swap it in (graceful daemon handoff)",
			i18n.A{"repo": st.Repo})
	}
	repo := st.github()
	if repo == "" {
		repo = "?"
	}
	return i18n.T("download the latest GitHub release of {repo} and swap it in (graceful daemon handoff)",
		i18n.A{"repo": repo})
}

// assetName 是 build.sh 给这个发行版 × 平台起的产物名。
func (u *updater) assetName() string {
	return "newgate-" + u.info.Distribution + "-" + u.goos + "-" + u.goarch
}

func (u *updater) note(text string) {
	fmt.Fprintln(u.out, "  "+style.Dim("· "+text))
}

func (u *updater) run(ctx context.Context, req upgradeapi.Request) (upgradeapi.Result, error) {
	if u.info.Distribution == "" {
		return upgradeapi.Result{}, i18n.E("this binary does not know which distribution it is (specification {spec}); rebuild it with build/build.sh",
			i18n.A{"spec": u.info.Spec})
	}
	if req.Version != "" {
		return u.pinned(ctx, req)
	}
	if u.st.Mode == modeGit {
		return u.fromRepo(ctx, req)
	}
	return u.latestRelease(ctx, req)
}

// latestRelease 是 release 模式的「最新」。
func (u *updater) latestRelease(ctx context.Context, req upgradeapi.Request) (upgradeapi.Result, error) {
	repo := u.github()
	if repo == "" {
		return upgradeapi.Result{}, i18n.E("distribution {dist} declares no GitHub repository; set one with `newgate self-update release <owner/repo>` or use git mode",
			i18n.A{"dist": u.info.Distribution})
	}
	gh := u.client(repo)
	rel, err := gh.latest(ctx)
	if errors.Is(err, errNoRelease) {
		return upgradeapi.Result{}, i18n.E("{repo} has no published release yet", i18n.A{"repo": repo})
	}
	if err != nil {
		return upgradeapi.Result{}, err
	}
	if msg, skip := u.notNewer(rel.Tag, req); skip {
		return upgradeapi.Current(msg), nil
	}
	a, ok := rel.asset(u.assetName())
	if !ok {
		// riscv64 这类 Release 矩阵里没有的平台：照样能升，只是得自己编。
		u.note(i18n.T("release {tag} has no {asset}; building {tag} from source instead",
			i18n.A{"tag": rel.Tag, "asset": u.assetName()}))
		return u.fromSource(ctx, req, rel.Tag)
	}
	if req.Check {
		return upgradeapi.Done(i18n.T("{tag} is available (this binary is {current}); upgrade would install {asset}",
			i18n.A{"tag": rel.Tag, "current": u.current, "asset": a.Name})), nil
	}
	return u.installRelease(ctx, gh, rel, a, req)
}

// notNewer 回答「最新 Release 不比手上的新，该跳过」并给出那句话。
//
// 手上的版本号认不出来（只有提交号：从一个没打 tag 的提交编的，多半是
// `upgrade@<分支>` 钉上去的）时**也跳过**：没法比新旧，而把一个特意钉上去的版本
// 悄悄换成 Release 正是「替人做决定」。--force 照换。Release 的 tag 认不出来时
// 照常换——那是发布方的命名，不是这台机器的选择。
func (u *updater) notNewer(tag string, req upgradeapi.Request) (string, bool) {
	if req.Force {
		return "", false
	}
	cur, okCur := parseVersion(u.current, u.info.Distribution)
	lat, okLat := parseVersion(tag, "")
	if !okLat {
		return "", false
	}
	if !okCur {
		return i18n.T("cannot tell whether {tag} is newer than this binary ({current}); --force installs it",
			i18n.A{"tag": tag, "current": u.current}), true
	}
	switch c := compare(lat, cur); {
	case c == 0 && !cur.dirty:
		return i18n.T("already at {tag}", i18n.A{"tag": tag}), true
	case c <= 0:
		return i18n.T("this binary ({current}) is newer than the latest release {tag}; nothing to do (--force installs the release anyway)",
			i18n.A{"current": u.current, "tag": tag}), true
	}
	return "", false
}

// pinned 是 `upgrade@<ref>`：先找同名 Release，没有（或者本平台没有产物）就从源码编。
func (u *updater) pinned(ctx context.Context, req upgradeapi.Request) (upgradeapi.Result, error) {
	ref := req.Version
	if !req.Force && u.current == u.info.Distribution+"-"+ref {
		return upgradeapi.Current(i18n.T("already at {tag}", i18n.A{"tag": ref})), nil
	}
	if repo := u.github(); repo != "" {
		gh := u.client(repo)
		rel, err := gh.byTag(ctx, ref)
		switch {
		case err == nil:
			if a, ok := rel.asset(u.assetName()); ok {
				if req.Check {
					return upgradeapi.Done(i18n.T("release {tag} has {asset}; upgrade would install it",
						i18n.A{"tag": rel.Tag, "asset": a.Name})), nil
				}
				return u.installRelease(ctx, gh, rel, a, req)
			}
			u.note(i18n.T("release {tag} has no {asset}; building it from source",
				i18n.A{"tag": rel.Tag, "asset": u.assetName()}))
		case errors.Is(err, errNoRelease):
			u.note(i18n.T("{repo} has no release named {ref}; building it from source",
				i18n.A{"repo": repo, "ref": ref}))
		default:
			return upgradeapi.Result{}, err
		}
	}
	return u.fromSource(ctx, req, ref)
}

func (u *updater) installRelease(ctx context.Context, gh *releaseClient, rel *ghRelease, a ghAsset, req upgradeapi.Request) (upgradeapi.Result, error) {
	sums, err := gh.sums(ctx, rel)
	if err != nil {
		return upgradeapi.Result{}, err
	}
	want, ok := sums[a.Name]
	if !ok {
		return upgradeapi.Result{}, i18n.E("{file} of {tag} does not list {asset}; refusing to install an unverified binary",
			i18n.A{"file": sumsAsset, "tag": rel.Tag, "asset": a.Name})
	}
	target, err := u.target()
	if err != nil {
		return upgradeapi.Result{}, err
	}
	unlock, err := lockUpgrade()
	if err != nil {
		return upgradeapi.Result{}, err
	}
	defer unlock()
	if a.Size > 0 {
		u.note(i18n.T("downloading {asset} from {tag} ({mb} MB)",
			i18n.A{"asset": a.Name, "tag": rel.Tag, "mb": fmt.Sprintf("%.1f", float64(a.Size)/(1<<20))}))
	} else {
		// 网页那条退路拿不到大小（见 release.go 的文件头）。
		u.note(i18n.T("downloading {asset} from {tag}", i18n.A{"asset": a.Name, "tag": rel.Tag}))
	}
	f, err := stage(target)
	if err != nil {
		return upgradeapi.Result{}, err
	}
	got, err := gh.download(ctx, a, f)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = os.Remove(f.Name())
		return upgradeapi.Result{}, err
	}
	if got != want {
		_ = os.Remove(f.Name())
		return upgradeapi.Result{}, i18n.E("checksum mismatch for {asset}: got {got}, {file} says {want}",
			i18n.A{"asset": a.Name, "got": got[:12], "file": sumsAsset, "want": want[:min(12, len(want))]})
	}
	u.note(i18n.T("checksum verified against {file}", i18n.A{"file": sumsAsset}))
	return u.finish(ctx, f.Name(), target, req)
}

// fromRepo 是 git 模式的「最新」：同步本地 checkout，编，换。
func (u *updater) fromRepo(ctx context.Context, req upgradeapi.Request) (upgradeapi.Result, error) {
	repo := u.st.Repo
	if err := checkRepo(repo, u.info.Spec); err != nil {
		return upgradeapi.Result{}, i18n.Ef(err, "git mode: {err}; point it elsewhere with `newgate self-update git <dir>`", nil)
	}
	if err := requireTools(); err != nil {
		return upgradeapi.Result{}, err
	}
	for _, n := range syncRepo(ctx, repo, req.Check) {
		u.note(n)
	}
	rev := describe(ctx, repo)
	next := u.info.Distribution + "-" + rev
	if rev != "" && !req.Force && next == u.current && !strings.HasSuffix(rev, "-dirty") {
		return upgradeapi.Current(i18n.T("already at {version} (built from {repo})", i18n.A{"version": u.current, "repo": repo})), nil
	}
	if req.Check {
		return upgradeapi.Done(i18n.T("upgrade would build {version} from {repo} (this binary is {current})",
			i18n.A{"version": next, "repo": repo, "current": u.current})), nil
	}
	return u.buildAndInstall(ctx, repo, req)
}

// fromSource 是「临时 clone 一份 ref 再编」：没有可用 Release 的 `@ref`、以及本平台
// 不在 Release 矩阵里的时候。开发者的本地 checkout 不被切换（那是他正在改的树）。
func (u *updater) fromSource(ctx context.Context, req upgradeapi.Request, ref string) (upgradeapi.Result, error) {
	url, err := u.cloneURL(ctx)
	if err != nil {
		return upgradeapi.Result{}, err
	}
	if err := requireTools(); err != nil {
		return upgradeapi.Result{}, err
	}
	if err := refExists(ctx, url, ref); err != nil {
		return upgradeapi.Result{}, err
	}
	if req.Check {
		return upgradeapi.Done(i18n.T("upgrade would clone {url} at {ref} and build it", i18n.A{"url": url, "ref": ref})), nil
	}
	src, cleanup, err := cloneRef(ctx, url, ref, u.buildLog)
	if err != nil {
		return upgradeapi.Result{}, err
	}
	defer cleanup()
	if err := checkRepo(src, u.info.Spec); err != nil {
		return upgradeapi.Result{}, i18n.Ef(err, "{ref} cannot build this distribution: {err}", i18n.A{"ref": ref})
	}
	return u.buildAndInstall(ctx, src, req)
}

// cloneURL 是源码构建去哪儿 clone：声明了 GitHub 仓库就用它的 https 地址（公开、
// 不要凭据）；没有就退回 git 模式那份 checkout 的 origin。
func (u *updater) cloneURL(ctx context.Context) (string, error) {
	if repo := u.github(); repo != "" {
		return u.cloneBase + repo + ".git", nil
	}
	if u.st.Mode == modeGit && u.st.Repo != "" {
		if url, err := git(ctx, u.st.Repo, "remote", "get-url", "origin"); err == nil && url != "" {
			return url, nil
		}
	}
	return "", i18n.E("no repository to build from: distribution {dist} declares no GitHub repository", i18n.A{"dist": u.info.Distribution})
}

func (u *updater) buildAndInstall(ctx context.Context, repo string, req upgradeapi.Request) (upgradeapi.Result, error) {
	target, err := u.target()
	if err != nil {
		return upgradeapi.Result{}, err
	}
	unlock, err := lockUpgrade()
	if err != nil {
		return upgradeapi.Result{}, err
	}
	defer unlock()
	u.note(i18n.T("building {spec} for {platform} in {repo}",
		i18n.A{"spec": u.info.Spec, "platform": u.goos + "/" + u.goarch, "repo": repo}))
	artifact, cleanup, err := buildRepo(ctx, repo, u.info.Spec, u.goos, u.goarch, u.buildLog)
	if err != nil {
		return upgradeapi.Result{}, err
	}
	defer cleanup()
	candidate, err := stageFrom(artifact, target)
	if err != nil {
		return upgradeapi.Result{}, err
	}
	return u.finish(ctx, candidate, target, req)
}

// finish：试跑候选 → 换上 → 交接 daemon。
func (u *updater) finish(ctx context.Context, candidate, target string, req upgradeapi.Request) (upgradeapi.Result, error) {
	got, err := smoke(ctx, candidate)
	if err != nil {
		_ = os.Remove(candidate)
		return upgradeapi.Result{}, i18n.Ef(err, "the new binary does not run here, nothing was replaced: {err}", nil)
	}
	if !req.Force && !strings.HasPrefix(got, u.info.Distribution+"-") {
		_ = os.Remove(candidate)
		return upgradeapi.Result{}, i18n.E("the new binary reports {version}, which is not a {dist} build; nothing was replaced (--force to install it anyway)",
			i18n.A{"version": got, "dist": u.info.Distribution})
	}
	prev, err := install(candidate, target)
	if err != nil {
		return upgradeapi.Result{}, err
	}
	u.note(i18n.T("installed {version} at {target}; the previous binary is kept as {prev}",
		i18n.A{"version": got, "target": target, "prev": prev}))
	msg, err := u.restart(target, u.out)
	if err != nil {
		return upgradeapi.Result{}, u.rollBack(ctx, target, got, err)
	}
	return upgradeapi.Done(i18n.T("{old} → {new}; {daemon}", i18n.A{"old": u.current, "new": got, "daemon": msg})), nil
}

// rollBack 是「换上去之后才失败」的退路：daemon 交接不上、新进程起不来或者不答话。
// 把 .prev 换回来，再用**旧的那份**把 daemon 拉回来（它没在跑时 restart 等于
// start）。新版本留成 .prev，想再试可以 `newgate self-update rollback`。
//
// 退路本身也会失败（旧版也起不来——那多半不是二进制的问题，是端口/配置），那时
// 两个错都报出来，并且说清楚手上现在是哪一份。
func (u *updater) rollBack(ctx context.Context, target, bad string, cause error) error {
	u.note(i18n.T("{version} did not take over the daemon ({err}); rolling back", i18n.A{"version": bad, "err": cause.Error()}))
	old, err := rollback(ctx, target)
	if err != nil {
		return i18n.E("{version} did not take over the daemon ({cause}), and rolling back failed too: {err}; the new binary is still at {target}, the old one at {prev}",
			i18n.A{"version": bad, "cause": cause.Error(), "err": err.Error(), "target": target, "prev": target + ".prev"})
	}
	if _, err := u.restart(target, u.out); err != nil {
		return i18n.E("{version} did not take over the daemon ({cause}); the binary is back at {old}, but the daemon did not come back either: {err} — run newgate start",
			i18n.A{"version": bad, "cause": cause.Error(), "old": old, "err": err.Error()})
	}
	return i18n.E("{version} did not take over the daemon ({cause}); rolled back to {old}, and the daemon runs it again",
		i18n.A{"version": bad, "cause": cause.Error(), "old": old})
}
