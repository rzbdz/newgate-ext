package hermes

import (
	"io/ioutil"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rzbdz/newgate-ext/modules/config/domain"
	"github.com/rzbdz/newgate-ext/modules/config/store"
	agentapi "github.com/rzbdz/newgate-ext/modules/confighook"
	"github.com/rzbdz/newgate-ext/testing/testkit"
)

// 接管 hermes 这一族测三件事：写进去的东西 hermes 能按我们的意思读出来（plan）、
// 别人写的东西一个都不动（takeover 的往返）、以及后台同步只碰清单不碰槽位（sync）。

func bind(provider, model string) domain.Candidates {
	return domain.Candidates{{Provider: provider, Model: model}}
}

func sampleInputs() inputs {
	return inputs{
		Port: 8899,
		Tiers: map[string]string{
			SlotModel: "normal", SlotDelegation: "mid", SlotVision: "vision",
			SlotCompression: "mid", SlotAuxiliary: "light",
		},
		Active: "ds",
		Profiles: []*domain.Profile{
			{Name: "ds", ContextWindow: 1000000, Roles: map[string]domain.Candidates{
				"heavy": bind("smt-deepseek", "deepseek-flash"),
				"mid":   bind("smt-deepseek", "deepseek-flash"),
			}},
			{Name: "complex", Roles: map[string]domain.Candidates{
				"heavy":  bind("smt-claude", "claude-opus-5-5"),
				"light":  bind("smt-codex", "gpt-5.6-luna"),
				"vision": bind("openrouter", "google/gemini-3-pro"),
			}},
			{Name: "has space", Roles: map[string]domain.Candidates{"mid": bind("p", "m")}},
		},
	}
}

func TestCatalogListsTiersAndEachProfilesModels(t *testing.T) {
	c := buildCatalog(sampleInputs())
	keys := []string{}
	for _, e := range c.Entries {
		keys = append(keys, e.K)
	}
	if strings.Join(keys, ",") != "newgate,newgate-ds,newgate-complex" {
		t.Fatalf("lines = %v", keys)
	}
	models := func(i int) []string {
		var out []string
		for _, m := range c.Entries[i].V.(omap) {
			if m.K == "models" {
				for _, x := range m.V.(omap) {
					out = append(out, x.K)
				}
			}
		}
		return out
	}
	if got := strings.Join(models(0), ","); got != strings.Join(domain.Roles, ",") {
		t.Errorf("the follow line should list tiers only: %s", got)
	}
	got := strings.Join(models(2), ",")
	if !strings.Contains(got, "smt-claude/claude-opus-5-5") || !strings.Contains(got, "smt-codex/gpt-5.6-luna") {
		t.Errorf("the pin line must list concrete models: %s", got)
	}
	// 模型名自己带 `/` 的点不中（NormalizeRole 取最后一段），不许列出来。
	if strings.Contains(got, "gemini-3-pro") {
		t.Errorf("model names containing / must not be listed: %s", got)
	}
	skipped := strings.Join(c.Skipped, "|")
	if !strings.Contains(skipped, "google/gemini-3-pro") || !strings.Contains(skipped, `"has space"`) {
		t.Errorf("skipped ones must be reported: %v", c.Skipped)
	}
}

// 钉住那一行的 URL 带 /p/<profile>：网关只从 URL 认「这一发钉在哪份上」。
func TestPinnedRowsCarryTheProfileInTheURL(t *testing.T) {
	c := buildCatalog(sampleInputs())
	for _, e := range c.Entries[1:] {
		url := ""
		for _, f := range e.V.(omap) {
			if f.K == "base_url" {
				url = f.V.(string)
			}
		}
		want := "/a/hermes/p/" + strings.TrimPrefix(e.K, "newgate-") + "/v1"
		if !strings.HasSuffix(url, want) {
			t.Errorf("base_url of %s = %s, want ...%s", e.K, url, want)
		}
	}
}

// 完整接管之后，hermes 读到的每一格都是我们的意思（经 hermes 的解析器复核——这台
// 机器上没有 hermes 时退回本包的读回校验）。
func TestFullEditsPointEverythingAtTheProxy(t *testing.T) {
	d := parseDoc(sampleYAML)
	edits, _ := fullEdits(d, sampleInputs())
	for _, e := range edits {
		if _, err := d.put(e); err != nil {
			t.Fatal(err)
		}
	}
	out := d.String()
	if _, err := verifyEdits(sampleYAML, out, edits, ""); err != nil {
		t.Fatalf("verification failed: %v\n%s", err, out)
	}
	r := parseDoc(out)
	for path, want := range map[string]string{
		"model.provider":              "newgate",
		"model.default":               "normal",
		"model.model":                 "normal",
		"model.base_url":              "http://127.0.0.1:8899/a/hermes/v1",
		"model.api_mode":              "chat_completions",
		"model.context_length":        "1000000",
		"delegation.provider":         "newgate",
		"delegation.model":            "mid",
		"delegation.base_url":         "",
		"auxiliary.vision.model":      "vision",
		"auxiliary.vision.base_url":   "",
		"auxiliary.compression.model": "mid",
		"auxiliary.curator.provider":  "newgate",
		"auxiliary.curator.model":     "light",
	} {
		got, ok := r.get(strings.Split(path, ".")...)
		if want == "" {
			s, found := r.lookup(strings.Split(path, ".")...)
			if !found || valueOf(r.lines[s.head]) != `""` {
				t.Errorf("%s should be cleared to \"\"", path)
			}
			continue
		}
		if !ok || got != want {
			t.Errorf("%s = %q, want %q", path, got, want)
		}
	}
	// 别人的东西：一字不动。
	for _, keep := range []string{
		`    pirate: "Arrr! Ye be talkin' to Captain Hermes: yo ho ho!"`,
		"  - name: smt-claude",
		"    model: gemini-3-flash-preview", // smart_model_routing 是死配置，不碰
		"  max_iterations: 50",
	} {
		if !strings.Contains(out, keep) {
			t.Errorf("this line must not be touched: %q", keep)
		}
	}
	// 幂等：再算一次没有任何改动。
	again, _ := fullEdits(r, sampleInputs())
	for _, e := range again {
		if changed, _ := r.put(e); changed {
			t.Errorf("a second takeover must not change %v again", e.Path)
		}
	}
}

// 被引用着的过期行不删：删了 hermes 下一轮解析不出 provider，整个网关停摆。
func TestCatalogKeepsAReferencedStaleRow(t *testing.T) {
	src := "model:\n  provider: newgate-old\n  default: x\nproviders:\n  newgate-old:\n    name: newgate-old\n  newgate-gone:\n    name: newgate-gone\n  mine:\n    name: mine\n"
	d := parseDoc(src)
	edits, c := catalogEdits(d, sampleInputs())
	var removed []string
	for _, e := range edits {
		if e.Value == nil {
			removed = append(removed, e.Path[1])
		}
	}
	if strings.Join(removed, ",") != "newgate-gone" {
		t.Errorf("only the unused line should be deleted, actually deleted %v", removed)
	}
	if !strings.Contains(strings.Join(c.Skipped, "|"), "newgate-old") {
		t.Errorf("the kept line must be reported: %v", c.Skipped)
	}
}

// ---------- 读写真文件：沙箱 ----------

func sandbox(t *testing.T) string {
	t.Helper()
	testkit.Sandbox(t)
	dir := t.TempDir()
	t.Setenv("HERMES_HOME", dir)
	if err := ioutil.WriteFile(filepath.Join(dir, "config.yaml"), []byte(sampleYAML), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveProviders(&domain.Providers{Providers: map[string]domain.Provider{
		"smt-deepseek": {BaseURL: "https://example.invalid/v1"},
	}}); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveProfile(&domain.Profile{Name: "ds", ContextWindow: 500000,
		Roles: map[string]domain.Candidates{"mid": bind("smt-deepseek", "deepseek-flash")}}); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(dir, "config.yaml")
}

// 接管 → hermes 自己改了一格 → off：接管写过的几格回到原样，hermes 改的那格保留。
func TestTakeoverRoundTripKeepsWhatHermesWroteMeanwhile(t *testing.T) {
	target := sandbox(t)
	if _, err := (Takeover{}).Apply(8899); err != nil {
		t.Fatal(err)
	}
	if !(Takeover{}).IsTakenOver(target) {
		t.Fatal("after takeover it should be recognizable")
	}
	b, _ := ioutil.ReadFile(target)
	if !strings.Contains(string(b), "newgate-ds:") {
		t.Fatalf("the profile line should be there\n%s", b)
	}
	// 用户在接管期间改了一个与我们无关的键（hermes config set 那种）。
	b = []byte(strings.Replace(string(b), "  max_turns: 90", "  max_turns: 120", 1))
	if err := ioutil.WriteFile(target, b, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := (Takeover{}).Restore(); err != nil {
		t.Fatal(err)
	}
	after, _ := ioutil.ReadFile(target)
	want := strings.Replace(sampleYAML, "  max_turns: 90", "  max_turns: 120", 1)
	// 接管补进去、原件里没有的辅助任务会留下一个空映射（`goal_judge: {}`）——
	// hermes 读它与读「没有」一样（缺省值深合并），把它们去掉再比。
	got := string(after)
	for _, t := range auxTasks {
		got = strings.Replace(got, "  "+t+": {}\n", "", 1)
	}
	if got != want {
		t.Errorf("after restore it should be original + that one user change\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
	if (Takeover{}).IsTakenOver(target) {
		t.Error("after restore it must no longer be recognized as taken over")
	}
	if _, err := os.Stat(agentapi.OriginalPath(target)); err != nil {
		t.Errorf("the original must be kept (escape hatch): %v", err)
	}
}

// 后台同步只碰清单：用户在 hermes 里 `/model … --global` 选过的主模型不会被改回去。
func TestSyncLeavesTheSlotsAlone(t *testing.T) {
	target := sandbox(t)
	if _, err := (Takeover{}).Apply(8899); err != nil {
		t.Fatal(err)
	}
	b, _ := ioutil.ReadFile(target)
	b = []byte(strings.Replace(string(b), "  default: normal", "  default: smt-deepseek/deepseek-flash", 1))
	b = []byte(strings.Replace(string(b), "model:\n  provider: newgate\n", "model:\n  provider: newgate-ds\n", 1))
	if err := ioutil.WriteFile(target, b, 0o600); err != nil {
		t.Fatal(err)
	}
	// newgate 这边加了一份 profile。
	if err := store.SaveProfile(&domain.Profile{Name: "kimi",
		Roles: map[string]domain.Candidates{"mid": bind("kimi", "kimi-k3")}}); err != nil {
		t.Fatal(err)
	}
	if stale, err := staleCatalog(); err != nil || !stale {
		t.Fatalf("after adding a profile the list should be stale (stale=%v err=%v)", stale, err)
	}
	syncOnce()
	after, _ := ioutil.ReadFile(target)
	s := string(after)
	if !strings.Contains(s, "newgate-kimi:") || !strings.Contains(s, "kimi/kimi-k3") {
		t.Errorf("the new profile should be synced in\n%s", s)
	}
	if !strings.Contains(s, "  default: smt-deepseek/deepseek-flash") || !strings.Contains(s, "model:\n  provider: newgate-ds\n") {
		t.Errorf("the main model the user chose must not be changed back\n%s", s)
	}
	if stale, _ := staleCatalog(); stale {
		t.Error("after syncing it should no longer be stale")
	}
}

// 文件不在：不创建，报一句能行动的话。
func TestApplyWithoutAConfigDoesNotCreateOne(t *testing.T) {
	testkit.Sandbox(t)
	dir := t.TempDir()
	t.Setenv("HERMES_HOME", dir)
	reps, err := (Takeover{}).Apply(8899)
	if err != nil || len(reps) != 1 || reps[0].Skipped == "" {
		t.Fatalf("it should skip and state the reason: %+v %v", reps, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "config.yaml")); err == nil {
		t.Error("it must not create a config for hermes")
	}
}

// 写之前文件被别人改了：不写。
func TestWriteIfUnchangedRefusesARacingWriter(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "c.yaml")
	_ = ioutil.WriteFile(p, []byte("a: 2\n"), 0o600)
	if err := writeIfUnchanged(p, []byte("a: 1\n"), []byte("a: 3\n")); err == nil {
		t.Error("a stale baseline should be refused")
	}
	b, _ := ioutil.ReadFile(p)
	if string(b) != "a: 2\n" {
		t.Errorf("the file must not be touched: %s", b)
	}
}

// 真实文件里 auxiliary 下面混着标量（2026-11-04 本机：display 的十几个键缩进错进了
// auxiliary）。它们不是任务：不许被当成任务去改，也不许挡住整个接管。
func TestAuxiliaryWithStrayScalarsIsStillTakenOver(t *testing.T) {
	src := strings.Replace(sampleYAML, "  compression:\n", "  compact: false\n  skin: default\n  compression:\n", 1)
	d := parseDoc(src)
	edits, _ := fullEdits(d, sampleInputs())
	for _, e := range edits {
		if _, err := d.put(e); err != nil {
			t.Fatalf("a stray scalar must not block takeover: %v", err)
		}
	}
	out := d.String()
	if !strings.Contains(out, "  compact: false\n  skin: default\n") {
		t.Errorf("those two scalars must be kept as is\n%s", out)
	}
	if _, err := verifyEdits(src, out, edits, ""); err != nil {
		t.Fatal(err)
	}
}

// 代理那一侧答的 /models 与写进文件的模型表是同一份（同一个函数算的）。
func TestModelsListerMatchesTheCatalog(t *testing.T) {
	sandbox(t)
	ids, ok := modelsLister{}.ListModels(ID, "ds")
	if !ok || strings.Join(ids, ",") != strings.Join(domain.Roles, ",")+",smt-deepseek/deepseek-flash" {
		t.Errorf("pinning ds = %v %v", ids, ok)
	}
	if ids, ok := (modelsLister{}).ListModels(ID, ""); !ok || strings.Join(ids, ",") != strings.Join(domain.Roles, ",") {
		t.Errorf("the follow line should list tiers only: %v", ids)
	}
	if _, ok := (modelsLister{}).ListModels("claude", ""); ok {
		t.Error("other clients are not its business")
	}
	if _, ok := (modelsLister{}).ListModels(ID, "nope"); ok {
		t.Error("a nonexistent profile gets the default list")
	}
}

// 同一台机器上两个守护进程（不同端口）：只有文件指着的那一个同步，另一个不碰。
// 否则两边每十秒各把 base_url 改成自己的端口（2026-11-04 实测的来回跳）。
func TestSyncOnlyByTheDaemonTheFilePointsAt(t *testing.T) {
	target := sandbox(t)
	if _, err := (Takeover{}).Apply(8899); err != nil {
		t.Fatal(err)
	}
	before, _ := ioutil.ReadFile(target)
	st := store.LoadState()
	st.Port = 18950 // 这个进程是「另一个」守护进程
	if err := store.SaveState(st); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveProfile(&domain.Profile{Name: "kimi",
		Roles: map[string]domain.Candidates{"mid": bind("kimi", "kimi-k3")}}); err != nil {
		t.Fatal(err)
	}
	syncOnce()
	after, _ := ioutil.ReadFile(target)
	if string(after) != string(before) {
		t.Errorf("the file points at 8899; the daemon on 18950 must not touch it")
	}
}

// TestHermesPythonRejectsANonPythonLauncher 是 2026-11-17 那次「后台同步卡住」的
// 回归守卫。
//
// 现场：hermes 的官方安装器在 `~/.local/bin/hermes` 放的是
// `#!/bin/sh` + `exec …` 的**壳**。`hermesPython` 那时直接信任 shebang，于是
// 拿 `/bin/sh` 当解释器去跑一段 python（`sh -I -c '<python>' a b c d`）——sh 从
// **继承来的终端**读 stdin，那条命令永远不返回。接管与同步两处都会卡，
// 而超时分支是 fail-open 的，所以外面看起来只是「同步没发生」。
//
// 判据是：拿不到一个**名字像 python** 的解释器就返回空串（调用方降级成读回
// 校验），而不是把一个别的东西当解释器去跑。
func TestHermesPythonRejectsANonPythonLauncher(t *testing.T) {
	testkit.Sandbox(t)
	dir := t.TempDir()

	writeLauncher := func(body string) string {
		p := filepath.Join(dir, "hermes")
		if err := ioutil.WriteFile(p, []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
		return p
	}

	// 官方安装器那种壳：shebang 是 `#!/bin/sh`，后面 exec 真的入口。
	// 把它当解释器拿去跑一段 python，就是 2026-11-17 那次卡住的现场。
	sh := writeLauncher("#!/bin/sh\nexec \"$HOME/.hermes/hermes-agent/.hermes/bin/hermes\" \"$@\"\n")
	if got := pythonFor(sh); got != "" {
		t.Fatalf("it treated %q as the hermes python interpreter (that would hang verify)", got)
	}

	// 真的 python 入口：认它（两种 shebang 写法都要认——venv 用绝对路径，
	// 用户装的常用 `env python3`）。
	for _, body := range []string{
		"#!/usr/bin/env python3\n# real launcher\n",
		"#!/usr/bin/python3\n# real launcher\n",
	} {
		p := writeLauncher(body)
		got := pythonFor(p)
		if got == "" {
			t.Fatalf("cannot recognize that kind of shebang: %q", strings.TrimSpace(body))
		}
		if base := filepath.Base(got); !strings.HasPrefix(base, "python") {
			t.Fatalf("it did not return python: %q", got)
		}
	}
}
