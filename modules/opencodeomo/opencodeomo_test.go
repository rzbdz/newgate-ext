package opencodeomo

import (
	"encoding/json"
	"io/ioutil"
	"os"
	"path/filepath"
	"testing"

	"github.com/rzbdz/newgate-ext/modules/config/domain"
	"github.com/rzbdz/newgate-ext/modules/config/resolve"
	"github.com/rzbdz/newgate-ext/modules/config/roleprov"
)

func init() {
	roleprov.Register(omoRolesProvider{})
}

// isolate 把配置目录与目标文件目录都指到临时目录：接管会写文件，
// 一个都不能落到用户的真实配置上。
func isolate(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	if err := os.MkdirAll(target, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("NEWGATE_HOME", dir)
	t.Setenv("NEWGATE_TARGET_DIR", target)
	t.Setenv("HOME", filepath.Join(dir, "home"))
	return target
}

const sampleOmo = `{
  "$schema": "x",
  "agents": {
    "sisyphus": {"model": "smt/gpt-5.6-sol", "variant": "max",
                 "fallback_models": ["smt/gpt-5.6-terra", "smt/claude-sonnet-5"]},
    "librarian": {"model": "smt/gpt-5.6-luna", "fallback_models": []}
  },
  "categories": {
    "deep": {"model": "smt/gpt-5.6-terra", "variant": "high"},
    "quick": {"model": "smt/gemini-3.1-flash-lite-preview"}
  }
}`

func writeTarget(t *testing.T, target, name, content string) string {
	t.Helper()
	p := filepath.Join(target, name)
	if err := ioutil.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func readJSONFile(t *testing.T, path string) map[string]interface{} {
	t.Helper()
	b, err := ioutil.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]interface{}
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func node(t *testing.T, root map[string]interface{}, section, name string) map[string]interface{} {
	t.Helper()
	s, _ := root[section].(map[string]interface{})
	n, _ := s[name].(map[string]interface{})
	if n == nil {
		t.Fatalf("%s.%s is not in the file", section, name)
	}
	return n
}

// TestTakeoverKeepsSlotIdentity 接管要**保留槽位身份**：以前所有槽位都被翻成
// 按体格算的档位名（sisyphus 和 librarian 从此长得一样），现在必须各自留名。
func TestTakeoverKeepsSlotIdentity(t *testing.T) {
	target := isolate(t)
	omo := writeTarget(t, target, "oh-my-openagent.json", sampleOmo)
	oc := writeTarget(t, target, "opencode.json", `{"model":"anthropic/claude-opus-5"}`)

	if _, err := ApplyAll(8899); err != nil {
		t.Fatal(err)
	}

	root := readJSONFile(t, omo)
	if got := node(t, root, "agents", "sisyphus")["model"]; got != "newgate/omo-sisyphus" {
		t.Errorf("the slot identity of sisyphus was lost: %v", got)
	}
	if got := node(t, root, "agents", "librarian")["model"]; got != "newgate/omo-librarian" {
		t.Errorf("the slot identity of librarian was lost: %v", got)
	}
	if got := node(t, root, "categories", "deep")["model"]; got != "newgate/cat-deep" {
		t.Errorf("the categories key prefix is wrong: %v", got)
	}
	// fallback_models 收敛成同一个键：链由 newgate 在服务端兜底（原列表记在注册表）
	fb, _ := node(t, root, "agents", "sisyphus")["fallback_models"].([]interface{})
	if len(fb) != 1 || fb[0] != "newgate/omo-sisyphus" {
		t.Errorf("fallback_models should collapse into one entry per key, got %v", fb)
	}

	// opencode.json 的 provider 里要把这些 id 登记出来，否则 opencode 认不出
	provs, _ := readJSONFile(t, oc)["provider"].(map[string]interface{})
	models, _ := provs["newgate"].(map[string]interface{})["models"].(map[string]interface{})
	for _, id := range []string{"omo-sisyphus", "omo-librarian", "cat-deep", "cat-quick"} {
		if _, ok := models[id]; !ok {
			t.Errorf("the newgate provider of opencode is missing model id %s", id)
		}
	}
}

// TestRegistryRecordsCurrentAndSuggested 注册表要同时记「现状」和「建议」：
// 现状保证接管不改行为，建议是给用户的一个可选项（variant 强度要算进去）。
func TestRegistryRecordsCurrentAndSuggested(t *testing.T) {
	target := isolate(t)
	writeTarget(t, target, "oh-my-openagent.json", sampleOmo)
	writeTarget(t, target, "opencode.json", `{}`)

	if _, err := ApplyAll(8899); err != nil {
		t.Fatal(err)
	}
	reg := ReadOmoSlots()
	if reg == nil {
		t.Fatal("the registry was not written")
	}

	cases := []struct{ key, was, variant, current, suggested string }{
		// opus 体格 + variant max：现状 normal（老行为），建议 heavy
		{"omo-sisyphus", "smt/gpt-5.6-sol", "max", "normal", "heavy"},
		// luna 是轻活：现状 light，没有 variant 就不建议改
		{"omo-librarian", "smt/gpt-5.6-luna", "", "light", "light"},
		{"cat-deep", "smt/gpt-5.6-terra", "high", "mid", "mid"},
		{"cat-quick", "smt/gemini-3.1-flash-lite-preview", "", "light", "light"},
	}
	for _, c := range cases {
		sl, ok := reg.SlotOf(c.key)
		if !ok {
			t.Fatalf("there is no %s in the registry", c.key)
		}
		if sl.Was != c.was || sl.Variant != c.variant {
			t.Errorf("%s recorded the wrong original form: was=%q variant=%q", c.key, sl.Was, sl.Variant)
		}
		if sl.Current != c.current {
			t.Errorf("%s current tier = %q, want %q", c.key, sl.Current, c.current)
		}
		if sl.Suggested != c.suggested {
			t.Errorf("%s suggested tier = %q, want %q", c.key, sl.Suggested, c.suggested)
		}
		if sl.Default != c.current {
			t.Errorf("%s default must equal the current setting (no behavior change), got %q", c.key, sl.Default)
		}
	}
	// 原 fallback 列表要留档（用户想恢复成显式链时用得上）
	if sl, _ := reg.SlotOf("omo-sisyphus"); len(sl.WasFallbacks) != 2 {
		t.Errorf("the original fallback list was not recorded: %v", sl.WasFallbacks)
	}
}

// TestRetakeoverIsIdempotent 反复接管不能层层加码，也不能把「原来是什么模型」
// 忘掉：第二次接管时磁盘上已经是我们写的 newgate/omo-sisyphus 了，
// 原模型名只能从备份里找回来。
func TestRetakeoverIsIdempotent(t *testing.T) {
	target := isolate(t)
	omo := writeTarget(t, target, "oh-my-openagent.json", sampleOmo)
	writeTarget(t, target, "opencode.json", `{}`)

	for i := 0; i < 3; i++ {
		if _, err := ApplyAll(8899); err != nil {
			t.Fatalf("takeover #%d: %v", i+1, err)
		}
	}
	root := readJSONFile(t, omo)
	if got := node(t, root, "agents", "sisyphus")["model"]; got != "newgate/omo-sisyphus" {
		t.Errorf("a repeated takeover corrupted the model name: %v", got)
	}
	sl, _ := ReadOmoSlots().SlotOf("omo-sisyphus")
	if sl.Was != "smt/gpt-5.6-sol" || sl.Variant != "max" {
		t.Errorf("re-takeover lost the original form: was=%q variant=%q", sl.Was, sl.Variant)
	}
	if sl.Current != "normal" || sl.Suggested != "heavy" {
		t.Errorf("tier drifted after re-takeover: current=%q suggested=%q", sl.Current, sl.Suggested)
	}
}

// TestOldTakeoverKeepsBehaviour 老版本接管过的文件（newgate/heavy）再被接管时，
// 现状必须原样保留——升级不能悄悄改变用户当前的模型选择。
//
// 原模型名这时只能从 backups/original/ 里找回来（磁盘上那份早被改写了），
// 顺带验证这条找回路径。
func TestOldTakeoverKeepsBehaviour(t *testing.T) {
	target := isolate(t)
	old := `{"agents":{"sisyphus":{"model":"newgate/heavy","variant":"max"}}}`
	omo := writeTarget(t, target, "oh-my-openagent.json", old)
	writeTarget(t, target, "opencode.json", `{}`)

	// 老版本接管留下的原始备份（真名字在里面）
	orig := filepath.Join(os.Getenv("NEWGATE_HOME"), "backups", "original")
	if err := os.MkdirAll(orig, 0o700); err != nil {
		t.Fatal(err)
	}
	pristine := `{"agents":{"sisyphus":{"model":"smt/gpt-5.6-sol","variant":"max"}}}`
	if err := ioutil.WriteFile(filepath.Join(orig, "oh-my-openagent.json"), []byte(pristine), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := ApplyAll(8899); err != nil {
		t.Fatal(err)
	}
	sl, _ := ReadOmoSlots().SlotOf("omo-sisyphus")
	if sl.Current != "heavy" || sl.Default != "heavy" {
		t.Errorf("the tier written by the old takeover should carry over unchanged, got current=%q default=%q", sl.Current, sl.Default)
	}
	if sl.Was != "smt/gpt-5.6-sol" {
		t.Errorf("the original model name should be recovered from the backup, got %q", sl.Was)
	}
	// 但现在是 omo-sisyphus 这个键在承担 heavy，用户以后可以单独改它
	if got := node(t, readJSONFile(t, omo), "agents", "sisyphus")["model"]; got != "newgate/omo-sisyphus" {
		t.Errorf("the old file should be upgraded to slot keys too: %v", got)
	}
}

// TestModeSuggestedSwitches 模式开关：suggested 才走建议，current 是默认。
func TestModeSuggestedSwitches(t *testing.T) {
	target := isolate(t)
	writeTarget(t, target, "oh-my-openagent.json", sampleOmo)
	writeTarget(t, target, "opencode.json", `{}`)
	if _, err := ApplyAll(8899); err != nil {
		t.Fatal(err)
	}

	reg := ReadOmoSlots()
	if got := reg.SlotBinding("omo-sisyphus"); got != "normal" {
		t.Fatalf("the default (current) should be normal, got %q", got)
	}
	// 覆盖优先于模式
	reg.Overrides = map[string]string{"omo-sisyphus": "@mid"}
	if got := reg.SlotBinding("omo-sisyphus"); got != "@mid" {
		t.Fatalf("the override should win, got %q", got)
	}
	delete(reg.Overrides, "omo-sisyphus")
	reg.Mode = "suggested"
	if got := reg.SlotBinding("omo-sisyphus"); got != "heavy" {
		t.Fatalf("suggested mode should give heavy, got %q", got)
	}
}

// TestRolesReachTheResolver 模块注册的角色键要真的能解析出链来——这是
// 「omo 插件把自己的 sub agent key 机制注册给框架」的端到端验收：
// 框架侧（core/domain + resolve）全程不认识 omo，只看注册表。
func TestRolesReachTheResolver(t *testing.T) {
	target := isolate(t)
	writeTarget(t, target, "oh-my-openagent.json", sampleOmo)
	writeTarget(t, target, "opencode.json", `{}`)
	if _, err := ApplyAll(8899); err != nil {
		t.Fatal(err)
	}

	if errs := roleprov.Refresh(); len(errs) > 0 {
		t.Fatalf("Refresh: %v", errs)
	}
	defer domain.SetExtraRoles(nil)

	if !domain.IsKnownRole("omo-sisyphus") || !domain.IsKnownRole("cat-deep") {
		t.Fatal("the slot key was not registered as a role key - a model sent by the client would be looked up as a concrete model name")
	}
	if !domain.IsKnownRole("heavy") || !domain.IsKnownRole("normal") {
		t.Fatal("built-in tiers should be recognized too (same role table)")
	}

	// 一个只写了 normal 的 profile：omo-sisyphus 的缺省是 normal，应当整条跟上
	prio := 10
	profiles := []*domain.Profile{{
		Name: "head", Priority: &prio,
		Roles: map[string]domain.Candidates{"normal": {{Provider: "p1", Model: "opus"}}},
	}}
	provs := &domain.Providers{Providers: map[string]domain.Provider{
		"p1": {APIKey: "k"},
	}}
	steps, _ := resolve.BuildChain("omo-sisyphus", profiles, provs,
		resolve.Opts{Active: "head"})
	if len(steps) != 1 || steps[0].Binding.Model != "opus" {
		t.Fatalf("omo-sisyphus should follow the candidates of normal, got %+v", steps)
	}
}

// TestRegistryIsReadableByOtherUsers 注册表必须**组可读**。
//
// 现场（2026-09-16）：root 的 CLI 接管成功、omo-slots.json 也写出来了，
// 跑 daemon 的 claude 用户却一个角色键都没注册到——文件按 0600 落地，
// 另一个用户读不到。症状是「文件明明在那儿，daemon 说不认识这个模型」。
func TestRegistryIsReadableByOtherUsers(t *testing.T) {
	target := isolate(t)
	writeTarget(t, target, "oh-my-openagent.json", sampleOmo)
	writeTarget(t, target, "opencode.json", `{}`)
	if _, err := ApplyAll(8899); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(filepath.Join(os.Getenv("NEWGATE_HOME"), "omo-slots.json"))
	if err != nil {
		t.Fatal(err)
	}
	if perm := fi.Mode().Perm(); perm&0o060 == 0 {
		t.Fatalf("registry permissions %o: the group cannot read it, so another user running the daemon sees no slot keys at all", perm)
	}
}

// TestSuggest 建议档位的两条规则：模型名归体格，variant 在阶梯上挪一级。
func TestSuggest(t *testing.T) {
	cases := []struct {
		model, variant string
		want           string
	}{
		{"smt/claude-opus-5", "max", "heavy"},
		{"smt/claude-opus-5", "high", "normal"},
		{"smt/claude-sonnet-5", "max", "normal"},
		{"smt/claude-sonnet-5", "", "mid"},
		{"smt/gpt-5.6-luna", "max", "mid"},
		{"smt/gpt-5.6-luna", "low", "light"},
		{"smt/gemini-3.1-pro-preview", "max", "vision"}, // vision 是正交档，不参与升降
		{"smt/whatever-unknown", "max", ""},             // 没命中规则就不建议
	}
	for _, c := range cases {
		got, why := Suggest(c.model, c.variant)
		if got != c.want {
			t.Errorf("Suggest(%q, %q) = %q (%s), want %q", c.model, c.variant, got, why, c.want)
		}
		if c.want == "" && why == "" {
			t.Errorf("even a negative recommendation must say why: %q", c.model)
		}
	}
}

// TestTakeoverCarriesAgentIdentity 锁住 2026-10-29 snode1 那次现场：接管写进
// opencode.json 的 baseURL 必须带 `/a/opencode`。网关只从路径认客户端
// （`/a/<agent>[/p/<profile>]/v1/...`，见内核 forward.parseTarget），漏了它
// 请求就被当成匿名、走全局缺省——界面上给 opencode 单独选的 profile 永远不生效。
func TestTakeoverCarriesAgentIdentity(t *testing.T) {
	target := isolate(t)
	p := writeTarget(t, target, "opencode.json", `{"$schema":"x"}`)
	if _, err := ApplyOpencode(p, 8899, nil); err != nil {
		t.Fatal(err)
	}
	prov := node(t, readJSONFile(t, p), "provider", ProviderID)
	opts, _ := prov["options"].(map[string]interface{})
	got, _ := opts["baseURL"].(string)
	if want := "http://127.0.0.1:8899/a/opencode/v1"; got != want {
		t.Fatalf("baseURL = %q, want %q (without /a/opencode the gateway routes by the global default)", got, want)
	}
}
