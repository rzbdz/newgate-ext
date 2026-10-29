package selfupdate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rzbdz/newgate/modules/config/paths"
	"github.com/rzbdz/newgate/testing/testkit"

	"github.com/rzbdz/newgate-ext/manifest/distinfo"
	upgradeapi "github.com/rzbdz/newgate-ext/modules/upgrade"
)

// fakeBinary 是一个「会回答 version 的 newgate」：一段 shell 脚本。smoke 只看
// `version` 的第一行，所以它足够当真二进制用。
func fakeBinary(ver string) []byte {
	return []byte("#!/bin/sh\necho \"newgate " + ver + "\"\n")
}

// fakeGitHub 起一个最小的 GitHub API：/releases/latest、/releases/tags/<tag> 与产物下载。
type fakeGitHub struct {
	srv      *httptest.Server
	releases map[string]map[string][]byte // tag → 产物名 → 内容
	latest   string
	badSum   bool
	// limited 让 API 一律 403（出口 IP 的额度被别人用光），只剩网页那几条地址。
	limited bool
}

func newFakeGitHub(t *testing.T) *fakeGitHub {
	f := &fakeGitHub{releases: map[string]map[string][]byte{}}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeGitHub) publish(tag string, assets map[string][]byte) {
	var sums strings.Builder
	for name, b := range assets {
		h := sha256.Sum256(b)
		sum := hex.EncodeToString(h[:])
		if f.badSum {
			sum = strings.Repeat("0", 64)
		}
		fmt.Fprintf(&sums, "%s  %s\n", sum, name)
	}
	assets[sumsAsset] = []byte(sums.String())
	f.releases[tag] = assets
	f.latest = tag
}

func (f *fakeGitHub) doc(tag string) ghRelease {
	r := ghRelease{Tag: tag}
	for name, b := range f.releases[tag] {
		r.Assets = append(r.Assets, ghAsset{Name: name, Size: int64(len(b)),
			URL: f.srv.URL + "/o/r/releases/download/" + tag + "/" + name})
	}
	return r
}

func (f *fakeGitHub) serve(w http.ResponseWriter, r *http.Request) {
	p := r.URL.Path
	if f.limited && strings.HasPrefix(p, "/repos/") {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"message":"API rate limit exceeded for 10.0.0.1. (But here's the good news: ...)"}`))
		return
	}
	switch {
	case p == "/o/r/releases/latest":
		if f.latest == "" {
			http.Redirect(w, r, "/o/r/releases", http.StatusFound)
			return
		}
		http.Redirect(w, r, "/o/r/releases/tag/"+f.latest, http.StatusFound)
	case strings.HasPrefix(p, "/o/r/releases/tag/"):
		if _, ok := f.releases[strings.TrimPrefix(p, "/o/r/releases/tag/")]; !ok {
			http.NotFound(w, r)
		}
	case p == "/repos/o/r/releases/latest" && f.latest != "":
		_ = json.NewEncoder(w).Encode(f.doc(f.latest))
	case strings.HasPrefix(p, "/repos/o/r/releases/tags/"):
		tag := strings.TrimPrefix(p, "/repos/o/r/releases/tags/")
		if _, ok := f.releases[tag]; !ok {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(f.doc(tag))
	case strings.HasPrefix(p, "/o/r/releases/download/"):
		parts := strings.SplitN(strings.TrimPrefix(p, "/o/r/releases/download/"), "/", 2)
		b, ok := f.releases[parts[0]][parts[1]]
		if !ok {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(b)
	default:
		http.NotFound(w, r)
	}
}

// harness 是一个装在临时目录里的「当前二进制」+ 指向假 GitHub 的 updater。
type harness struct {
	u        *updater
	target   string
	restarts int
	out      strings.Builder
}

func newHarness(t *testing.T, gh *fakeGitHub, current string) *harness {
	t.Helper()
	testkit.Sandbox(t)
	if err := paths.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	h := &harness{target: filepath.Join(t.TempDir(), "newgate")}
	if err := os.WriteFile(h.target, fakeBinary(current), 0o755); err != nil {
		t.Fatal(err)
	}
	h.u = &updater{
		st:       settings{Mode: modeRelease, GitHub: "o/r"},
		info:     distinfo.Info{Spec: "dist.json", Distribution: "default", GitHub: "o/r"},
		current:  current,
		goos:     "linux",
		goarch:   "amd64",
		out:      &h.out,
		buildLog: io.Discard,
		client: func(repo string) *releaseClient {
			c := newReleaseClient(repo)
			c.api, c.web = gh.srv.URL, gh.srv.URL
			return c
		},
		target: func() (string, error) { return h.target, nil },
		restart: func(string, io.Writer) (string, error) {
			h.restarts++
			return "daemon restarted", nil
		},
	}
	return h
}

func (h *harness) installedVersion(t *testing.T) string {
	t.Helper()
	v, err := smoke(context.Background(), h.target)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

const asset = "newgate-default-linux-amd64"

func TestReleaseUpgradeInstallsAndKeepsPrevious(t *testing.T) {
	gh := newFakeGitHub(t)
	gh.publish("v0.9.0", map[string][]byte{asset: fakeBinary("default-v0.9.0")})
	h := newHarness(t, gh, "default-v0.8.0")

	msg, err := h.u.runMsg(context.Background(), upgradeapi.Request{})
	if err != nil {
		t.Fatal(err)
	}
	if got := h.installedVersion(t); got != "default-v0.9.0" {
		t.Fatalf("installed %s, want default-v0.9.0 (msg %q)", got, msg)
	}
	prev, err := smoke(context.Background(), h.target+".prev")
	if err != nil || prev != "default-v0.8.0" {
		t.Fatalf(".prev = %q, %v; want the old binary", prev, err)
	}
	if h.restarts != 1 {
		t.Fatalf("restarts = %d, want 1", h.restarts)
	}

	// rollback 是一个开关：换回去，再换回来。
	if v, err := rollback(context.Background(), h.target); err != nil || v != "default-v0.8.0" {
		t.Fatalf("rollback = %q, %v", v, err)
	}
	if got := h.installedVersion(t); got != "default-v0.8.0" {
		t.Fatalf("after rollback %s", got)
	}
	if v, _ := rollback(context.Background(), h.target); v != "default-v0.9.0" {
		t.Fatalf("second rollback = %q, want back to v0.9.0", v)
	}
}

func TestReleaseChecksumMismatchReplacesNothing(t *testing.T) {
	gh := newFakeGitHub(t)
	gh.badSum = true
	gh.publish("v0.9.0", map[string][]byte{asset: fakeBinary("default-v0.9.0")})
	h := newHarness(t, gh, "default-v0.8.0")

	if _, err := h.u.run(context.Background(), upgradeapi.Request{}); err == nil {
		t.Fatal("a checksum mismatch must fail")
	}
	if got := h.installedVersion(t); got != "default-v0.8.0" {
		t.Fatalf("binary was replaced despite the mismatch: %s", got)
	}
	leftovers, _ := filepath.Glob(filepath.Join(filepath.Dir(h.target), ".newgate.new-*"))
	if len(leftovers) != 0 {
		t.Fatalf("candidate left behind: %v", leftovers)
	}
}

// 下回来的东西跑不起来 / 是别的发行版：不换。
func TestBrokenOrForeignBinaryIsNotInstalled(t *testing.T) {
	for name, content := range map[string][]byte{
		"does not run": []byte("not an executable"),
		"other distro": fakeBinary("hello-v0.9.0"),
	} {
		t.Run(name, func(t *testing.T) {
			gh := newFakeGitHub(t)
			gh.publish("v0.9.0", map[string][]byte{asset: content})
			h := newHarness(t, gh, "default-v0.8.0")
			if _, err := h.u.run(context.Background(), upgradeapi.Request{}); err == nil {
				t.Fatal("want an error")
			}
			if got := h.installedVersion(t); got != "default-v0.8.0" {
				t.Fatalf("replaced with %s", got)
			}
			if h.restarts != 0 {
				t.Fatal("restarted the daemon although nothing was installed")
			}
		})
	}
}

func TestReleaseNotNewerIsSkippedWithoutForce(t *testing.T) {
	gh := newFakeGitHub(t)
	gh.publish("v0.8.0", map[string][]byte{asset: fakeBinary("default-v0.8.0")})

	for _, cur := range []string{"default-v0.8.0", "default-v0.8.0-40-gabcdef0"} {
		h := newHarness(t, gh, cur)
		msg, err := h.u.runMsg(context.Background(), upgradeapi.Request{})
		if err != nil || h.installedVersion(t) != cur {
			t.Fatalf("%s: msg %q err %v; must stay", cur, msg, err)
		}
	}

	// 手上是一个没打 tag 的提交编的（upgrade@<分支> 钉上去的）：没法比，不替人换。
	h0 := newHarness(t, gh, "default-d5899f6")
	if msg, err := h0.u.runMsg(context.Background(), upgradeapi.Request{}); err != nil || h0.installedVersion(t) != "default-d5899f6" {
		t.Fatalf("untagged build: msg %q err %v; must stay", msg, err)
	}

	// --force 照样装（开发机想回到正式版）。
	h := newHarness(t, gh, "default-v0.8.0-40-gabcdef0")
	if _, err := h.u.run(context.Background(), upgradeapi.Request{Force: true}); err != nil {
		t.Fatal(err)
	}
	if got := h.installedVersion(t); got != "default-v0.8.0" {
		t.Fatalf("--force installed %s", got)
	}
}

func TestCheckChangesNothing(t *testing.T) {
	gh := newFakeGitHub(t)
	gh.publish("v0.9.0", map[string][]byte{asset: fakeBinary("default-v0.9.0")})
	h := newHarness(t, gh, "default-v0.8.0")
	msg, err := h.u.runMsg(context.Background(), upgradeapi.Request{Check: true})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(msg, "v0.9.0") {
		t.Fatalf("check message %q does not name the available version", msg)
	}
	if h.installedVersion(t) != "default-v0.8.0" || h.restarts != 0 {
		t.Fatal("--check changed something")
	}
}

// `upgrade@<tag>`：有 Release 就用 Release（哪怕比手上旧——点名就是要它）。
func TestPinnedReleaseMayGoBack(t *testing.T) {
	gh := newFakeGitHub(t)
	gh.publish("v0.7.0", map[string][]byte{asset: fakeBinary("default-v0.7.0")})
	gh.publish("v0.9.0", map[string][]byte{asset: fakeBinary("default-v0.9.0")})
	h := newHarness(t, gh, "default-v0.8.0")
	if _, err := h.u.run(context.Background(), upgradeapi.Request{Version: "v0.7.0"}); err != nil {
		t.Fatal(err)
	}
	if got := h.installedVersion(t); got != "default-v0.7.0" {
		t.Fatalf("installed %s, want the pinned v0.7.0", got)
	}
}

// 没有这个 tag 的 Release、或者本平台没有产物：改走源码（这里只用 --check 看它
// 选了哪条路——真的 clone 要出网）。
func TestPinnedWithoutReleaseFallsBackToSource(t *testing.T) {
	if err := requireTools(); err != nil {
		t.Skip(err)
	}
	gh := newFakeGitHub(t)
	gh.publish("v0.9.0", map[string][]byte{"newgate-default-darwin-arm64": fakeBinary("default-v0.9.0")})
	h := newHarness(t, gh, "default-v0.8.0")

	// clone 的前缀换成本地目录：<base>/o/r.git 是一个裸仓库，ls-remote 真的问得到
	// ref，而测试不出网。
	base := t.TempDir() + "/"
	origin := filepath.Join(base, "o", "r.git")
	seed := filepath.Join(t.TempDir(), "seed")
	run(t, base, "init", "-q", "--bare", "-b", "main", origin)
	run(t, filepath.Dir(seed), "clone", "-q", origin, seed)
	write(t, filepath.Join(seed, "README"), "x\n")
	run(t, seed, "add", ".")
	run(t, seed, "commit", "-q", "-m", "x")
	run(t, seed, "tag", "v1.0.0-unreleased")
	run(t, seed, "tag", "v0.9.0")
	run(t, seed, "push", "-q", "origin", "HEAD:main", "--tags")
	h.u.cloneBase = base

	msg, err := h.u.runMsg(context.Background(), upgradeapi.Request{Version: "v1.0.0-unreleased", Check: true})
	if err != nil || !strings.Contains(msg, origin) || !strings.Contains(msg, "v1.0.0-unreleased") {
		t.Fatalf("no release: msg %q err %v; want a source build of the ref", msg, err)
	}
	if _, err := h.u.run(context.Background(), upgradeapi.Request{Version: "v0.0.0-typo", Check: true}); err == nil {
		t.Fatal("a ref the remote does not have must fail before any clone")
	}

	// 本平台没有产物：同样改走源码。
	msg, err = h.u.runMsg(context.Background(), upgradeapi.Request{Check: true})
	if err != nil || !strings.Contains(msg, "v0.9.0") || !strings.Contains(msg, "clone") {
		t.Fatalf("no asset for this platform: msg %q err %v; want a source build of v0.9.0", msg, err)
	}
}

// API 额度被同一出口的别人用光时，退回 github.com 的网页地址：latest 跟重定向、
// tag 看 200/404、产物清单读 SHA256SUMS。拿到的东西要和 API 那条一样能装、能校验。
func TestRateLimitedAPIFallsBackToTheWebEndpoints(t *testing.T) {
	gh := newFakeGitHub(t)
	gh.limited = true
	gh.publish("v0.8.5", map[string][]byte{asset: fakeBinary("default-v0.8.5")})
	gh.publish("v0.9.0", map[string][]byte{asset: fakeBinary("default-v0.9.0")})
	h := newHarness(t, gh, "default-v0.8.0")
	if _, err := h.u.run(context.Background(), upgradeapi.Request{}); err != nil {
		t.Fatal(err)
	}
	if got := h.installedVersion(t); got != "default-v0.9.0" {
		t.Fatalf("latest via web: installed %s", got)
	}
	if _, err := h.u.run(context.Background(), upgradeapi.Request{Version: "v0.8.5"}); err != nil {
		t.Fatal(err)
	}
	if got := h.installedVersion(t); got != "default-v0.8.5" {
		t.Fatalf("pinned via web: installed %s", got)
	}
	c := h.u.client("o/r")
	if _, err := c.byTag(context.Background(), "v9.9.9"); !errors.Is(err, errNoRelease) {
		t.Fatalf("missing tag via web = %v, want errNoRelease", err)
	}

	// 校验照样生效。
	gh2 := newFakeGitHub(t)
	gh2.limited, gh2.badSum = true, true
	gh2.publish("v0.9.0", map[string][]byte{asset: fakeBinary("default-v0.9.0")})
	h2 := newHarness(t, gh2, "default-v0.8.0")
	if _, err := h2.u.run(context.Background(), upgradeapi.Request{}); err == nil {
		t.Fatal("checksum mismatch via the web fallback must fail")
	}
}

// runMsg 是测试里的便捷形：只要那句话。
func (u *updater) runMsg(ctx context.Context, req upgradeapi.Request) (string, error) {
	r, err := u.run(ctx, req)
	return r.Message, err
}

// 「没什么可做」要以 UpToDate 报出来：upgrade 据此不再追问 y/N。
func TestNothingToDoIsReportedAsUpToDate(t *testing.T) {
	gh := newFakeGitHub(t)
	gh.publish("v0.8.0", map[string][]byte{asset: fakeBinary("default-v0.8.0")})
	for _, cur := range []string{"default-v0.8.0", "default-v0.8.0-3-gabcdef0", "default-d5899f6"} {
		h := newHarness(t, gh, cur)
		res, err := h.u.run(context.Background(), upgradeapi.Request{Check: true})
		if err != nil || !res.UpToDate {
			t.Errorf("%s: %+v %v, want UpToDate", cur, res, err)
		}
	}
	h := newHarness(t, gh, "default-v0.7.0")
	if res, err := h.u.run(context.Background(), upgradeapi.Request{Check: true}); err != nil || res.UpToDate {
		t.Errorf("older binary: %+v %v, want something to do", res, err)
	}
}

func TestParseSums(t *testing.T) {
	got := parseSums("ABC  newgate-a\nbad line here x\ndef *newgate-b\n")
	if got["newgate-a"] != "abc" || got["newgate-b"] != "def" || len(got) != 2 {
		t.Fatalf("parseSums = %v", got)
	}
}

// 装进图里：往 upgrade 登记一个认识版本的 `binary` 动作，Stop 时撤销。
func TestModuleRegistersVersionedBinaryAction(t *testing.T) {
	testkit.Sandbox(t)
	g := testkit.Start(t, upgradeapi.New(), New())
	svc := testkit.Get(g, upgradeapi.Capability)
	var found *upgradeapi.Action
	for _, a := range svc.Actions() {
		if a.ID == actionID {
			a := a
			found = &a
		}
	}
	if found == nil || !found.Versioned {
		t.Fatalf("binary action = %+v, want a versioned registration", found)
	}
	g.Stop()
	for _, a := range svc.Actions() {
		if a.ID == actionID {
			t.Fatal("the action outlived Stop")
		}
	}
}

// 换上去之后才失败（daemon 交接不上 / 新进程起不来 / 不答话）：自动换回旧版，
// 再用旧版把 daemon 拉回来。这是「任何一步失败都要能退回去」里最要紧的一条——
// 失败发生在二进制已经换掉之后。
func TestHandoffFailureRollsBack(t *testing.T) {
	gh := newFakeGitHub(t)
	gh.publish("v0.9.0", map[string][]byte{asset: fakeBinary("default-v0.9.0")})
	h := newHarness(t, gh, "default-v0.8.0")
	var ranWith []string
	h.u.restart = func(target string, _ io.Writer) (string, error) {
		v, _ := smoke(context.Background(), target)
		ranWith = append(ranWith, v)
		if len(ranWith) == 1 {
			return "", errors.New("the new daemon does not answer")
		}
		return "back", nil
	}
	_, err := h.u.run(context.Background(), upgradeapi.Request{})
	if err == nil || !strings.Contains(err.Error(), "rolled back to default-v0.8.0") {
		t.Fatalf("err = %v, want a rollback report", err)
	}
	if got := h.installedVersion(t); got != "default-v0.8.0" {
		t.Fatalf("after a failed handoff the binary is %s, want the old one back", got)
	}
	if strings.Join(ranWith, ",") != "default-v0.9.0,default-v0.8.0" {
		t.Fatalf("restarted with %v, want the new one, then the old one", ranWith)
	}
	// 坏掉的那份留成 .prev（想再试可以 rollback），不是被删掉。
	if v, _ := smoke(context.Background(), h.target+".prev"); v != "default-v0.9.0" {
		t.Fatalf(".prev = %s, want the failed new version", v)
	}
}

// 退路上 daemon 也拉不回来：二进制照样换回旧版，并且明说要手动 start。
func TestHandoffFailureWhenTheOldDaemonDoesNotComeBackEither(t *testing.T) {
	gh := newFakeGitHub(t)
	gh.publish("v0.9.0", map[string][]byte{asset: fakeBinary("default-v0.9.0")})
	h := newHarness(t, gh, "default-v0.8.0")
	h.u.restart = func(string, io.Writer) (string, error) { return "", errors.New("port in use") }
	_, err := h.u.run(context.Background(), upgradeapi.Request{})
	if err == nil || !strings.Contains(err.Error(), "newgate start") {
		t.Fatalf("err = %v, want it to say how to bring the daemon back", err)
	}
	if got := h.installedVersion(t); got != "default-v0.8.0" {
		t.Fatalf("binary = %s, want the old one", got)
	}
}

// 下载/构建被 Ctrl-C 打断会在 bin 目录里留下候选；下一次升级（持锁）清掉它们。
func TestStaleCandidatesAreCleanedUp(t *testing.T) {
	gh := newFakeGitHub(t)
	gh.publish("v0.9.0", map[string][]byte{asset: fakeBinary("default-v0.9.0")})
	h := newHarness(t, gh, "default-v0.8.0")
	stale := filepath.Join(filepath.Dir(h.target), ".newgate.new-123")
	if err := os.WriteFile(stale, []byte("half a download"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := h.u.run(context.Background(), upgradeapi.Request{}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(stale); err == nil {
		t.Fatal("the interrupted candidate is still there")
	}
}
