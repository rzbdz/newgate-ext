package selfupdate

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"runtime"
	"strings"

	"github.com/rzbdz/newgate-ext/lib/buildinfo"
	"github.com/rzbdz/newgate-ext/lib/style"
	i18n "github.com/rzbdz/newgate/lib/i18n"

	"github.com/rzbdz/newgate-ext/manifest/distinfo"
	upgradeapi "github.com/rzbdz/newgate-ext/modules/upgrade"
)

// Orchestration: mode and `@version` pick a route to "a file in hand", then install.go.

// updater turns every "outside thing" into a field the tests swap (fake GitHub, a
// temp-dir "current binary", never really restarting the daemon).
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
	// cloneBase is the source-build clone prefix (`https://github.com/` + owner/repo + `.git`).
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

// github is the release repo for this run: the self-section override, else the spec's.
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

// assetName is the artifact name build.sh gives this distribution x platform.
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

// latestRelease is "latest" in release mode.
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
		// A platform missing from the Release matrix (riscv64 etc.): still upgradable, just build it.
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

// notNewer answers "the latest Release is no newer, skip" and gives that sentence.
//
// If the version in hand is unrecognizable (only a commit id: built from an untagged
// commit, usually pinned by `upgrade@<branch>`) it **also skips**: no comparison is
// possible, and silently replacing a deliberately pinned version with a Release is
// "deciding for them". --force still swaps; an unknown Release tag swaps - not our choice.
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

// pinned is `upgrade@<ref>`: look for a same-named Release first, else build from source.
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
		// The web fallback cannot get the size (see release.go's file header).
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

// fromRepo is git mode's "latest": sync the local checkout, build, swap.
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

// fromSource is "clone ref into a temp dir and build": `@ref` with no usable Release,
// and when this platform is not in the Release matrix. The dev checkout is left alone (their tree).
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

// cloneURL is where source builds clone from: a declared GitHub repo gives its https
// address (public, no credentials); otherwise fall back to the git-mode checkout's origin.
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

// finish: dry-run the candidate -> swap it in -> hand the daemon over.
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

// rollBack is the way back when failure comes **after** the swap: daemon handover
// fails, the new process will not start or answer. Put .prev back, pull the daemon up
// with the **old** one (restart = start if stopped); new stays .prev for `rollback`.
//
// The way back can itself fail (the old one will not start either - usually not the
// binary, but the port/config); then report both errors and say which one is in hand.
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
