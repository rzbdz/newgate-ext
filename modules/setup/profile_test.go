package setup

import (
	"encoding/json"
	"io/ioutil"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rzbdz/newgate/modules/config/domain"
	"github.com/rzbdz/newgate/modules/config/paths"
	"github.com/rzbdz/newgate/modules/config/store"
	"github.com/rzbdz/newgate/testing/testkit"
)

// 本文件的用例只管**向导自己那点知识**：写出去的三份文件长什么样、更新 provider
// 时别人的东西动没动、base URL 里的常见垃圾去没去掉、以及「有没有可用配置」那条
// 判断（它决定向导要不要重新问一遍上游）。
//
// 全部是纯函数或 store 的读写，走 testkit.Sandbox 圈在临时目录里——不碰真实的
// ~/.config/newgate。

func seedSandbox(t *testing.T) {
	t.Helper()
	testkit.Sandbox(t)
	if err := paths.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
}

// TestApplyWritesAChainThatResolves 是这一整个功能的核心承诺：**一份只给了一个
// base、一个 key、一个模型的配置，每一档都解析得出候选**。
//
// 只要有一档是空链，症状就是「某类请求突然没有可用候选」——而用户完全看不出
// 那是当初 bootstrap 没写它。
func TestApplyWritesAChainThatResolves(t *testing.T) {
	seedSandbox(t)
	in := Bootstrap{Provider: "demo", BaseURL: "https://api.example.com/v1", APIKey: "sk-x", Model: "m-1"}
	res, err := Apply(in, Profile{WriteProfile: true, SetDefault: true}, ApplyResult{})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(res.Files) == 0 {
		t.Fatal("Apply 没报改过任何文件")
	}

	pr, err := store.LoadProfile(ProfileName)
	if err != nil {
		t.Fatalf("读回 default: %v", err)
	}
	for _, tier := range domain.Roles {
		c, ok := pr.Roles[tier]
		if !ok || len(c) == 0 {
			t.Fatalf("档位 %s 没有候选（链是空的）", tier)
		}
		if c[0].Provider != "demo" || c[0].Model != "m-1" {
			t.Fatalf("档位 %s = %v，想要 demo/m-1", tier, c[0])
		}
	}
	if got := store.LoadState().DefaultProfile; got != ProfileName {
		t.Fatalf("全局默认 = %q，想要 %q", got, ProfileName)
	}
	if !Inspect().HasUsable {
		t.Fatal("刚写完的配置判成「不可用」——向导下次会再问一遍上游")
	}
}

// TestApplyKeepsOtherProvidersVerbatim 是一条不能破的规矩：用户别的 provider
// **一个字节都不动**，尤其是那些用 api_key_env 的（它们的明文只活在环境里）。
func TestApplyKeepsOtherProvidersVerbatim(t *testing.T) {
	seedSandbox(t)
	first := &domain.Providers{Providers: map[string]domain.Provider{
		"work": {BaseURL: "https://work.example.com/v1", APIKeyEnv: "NEWGATE_KEY_WORK", Protocol: "openai"},
		"home": {BaseURL: "https://home.example.com/v1", APIKey: "sk-home", Protocol: "anthropic"},
	}}
	if err := store.SaveProviders(first); err != nil {
		t.Fatal(err)
	}
	// 让 api_key_env 解析得出一个明文：写回去的话它会落盘。
	t.Setenv("NEWGATE_KEY_WORK", "sk-from-env")

	if _, err := Apply(Bootstrap{Provider: "demo", BaseURL: "https://demo.example.com", APIKey: "sk-d", Model: "d-1"},
		Profile{WriteProfile: true, SetDefault: true}, ApplyResult{}); err != nil {
		t.Fatal(err)
	}

	b, err := ioutil.ReadFile(paths.ProvidersFile())
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Providers map[string]struct {
			BaseURL   string `json:"base_url"`
			APIKey    string `json:"api_key"`
			APIKeyEnv string `json:"api_key_env"`
		} `json:"providers"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatalf("写出来的不是合法 JSON: %v", err)
	}
	work := doc.Providers["work"]
	if work.BaseURL != "https://work.example.com/v1" || work.APIKeyEnv != "NEWGATE_KEY_WORK" {
		t.Fatalf("work 被改坏了: %+v", work)
	}
	if work.APIKey != "" {
		t.Fatalf("work 的明文 key 被写回磁盘了（它只该活在环境变量里）: %q", work.APIKey)
	}
	if home := doc.Providers["home"]; home.APIKey != "sk-home" || home.BaseURL != "https://home.example.com/v1" {
		t.Fatalf("home 被改坏了: %+v", home)
	}
	if demo, ok := doc.Providers["demo"]; !ok || demo.BaseURL != "https://demo.example.com" {
		t.Fatalf("demo 没写对: %+v", demo)
	}
}

// TestApplyLeavesExistingKvAlone：默认不覆盖一份已经存在的 default.kv。
func TestApplyLeavesExistingKvAlone(t *testing.T) {
	seedSandbox(t)
	kv := filepath.Join(paths.Mappings(), ProfileName+".kv")
	if err := ioutil.WriteFile(kv, []byte("normal=other/keep-me\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	res, err := Apply(Bootstrap{Provider: "demo", BaseURL: "https://d.example.com", Model: "m"},
		Profile{WriteProfile: true, SetDefault: false}, ApplyResult{})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := ioutil.ReadFile(kv)
	if string(b) != "normal=other/keep-me\n" {
		t.Fatalf("已有的 default.kv 被覆盖了: %q", b)
	}
	if len(res.Warnings) == 0 {
		t.Fatal("没覆盖却没说出来——用户会以为模型已经指过去了")
	}
}

// TestApplyKvWinsOverStaleJson：写 .kv 之后，同名的 .json 不能留在那里假装还有效。
func TestApplyKvWinsOverStaleJson(t *testing.T) {
	seedSandbox(t)
	old := filepath.Join(paths.Mappings(), ProfileName+".json")
	if err := ioutil.WriteFile(old, []byte(`{"roles":{"normal":"old/x"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Apply(Bootstrap{Provider: "demo", BaseURL: "https://d.example.com", Model: "m"},
		Profile{WriteProfile: true, SetDefault: true}, ApplyResult{}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(old); err == nil {
		t.Fatal("旧的 default.json 还在——它现在是一份不起作用的旧绑定，正是最会骗人的那种")
	}
	pr, err := store.LoadProfile(ProfileName)
	if err != nil {
		t.Fatal(err)
	}
	if got := pr.Roles["normal"][0]; got.Provider != "demo" || got.Model != "m" {
		t.Fatalf("读回来的是旧的绑定: %+v", got)
	}
}

// TestNormalizeBase：用户贴进来的地址有各种尾巴，转发层只认「base + 后缀」。
func TestNormalizeBase(t *testing.T) {
	cases := map[string]string{
		"https://api.example.com/v1/":                 "https://api.example.com/v1",
		"https://api.example.com/v1/chat/completions": "https://api.example.com/v1",
		"https://api.example.com":                     "https://api.example.com",
		" https://api.example.com/v1/messages ":       "https://api.example.com/v1",
		"https://ark.example.com/api/coding/v3/":      "https://ark.example.com/api/coding/v3",
		"https://gw.example.com/v1/models":            "https://gw.example.com/v1",
	}
	for in, want := range cases {
		if got := normalizeBase(in); got != want {
			t.Errorf("normalizeBase(%q) = %q，想要 %q", in, got, want)
		}
	}
}

// TestSuggestedProvider：从 base 推出的名字要能直接用，撞名要避开。
func TestSuggestedProvider(t *testing.T) {
	cases := map[string]string{
		"https://api.rvcompute.com:60000/v1": "rvcompute",
		"https://api.deepseek.com/v1":        "deepseek",
		"https://openrouter.ai/api/v1":       "openrouter",
		"http://127.0.0.1:9/v1":              "127-0-0-1",
		"https://www.example.co.uk":          "example",
	}
	for in, want := range cases {
		if got := SuggestedProvider(in, nil); got != want {
			t.Errorf("SuggestedProvider(%q) = %q，想要 %q", in, got, want)
		}
	}
	if got := SuggestedProvider("https://api.deepseek.com", map[string]bool{"deepseek": true}); got != "deepseek-2" {
		t.Errorf("撞名没有避开: %q", got)
	}
}

// TestParseModelNames：三种真实形状都要认（newapi 的 name、Ollama 的 models、裸数组）。
func TestParseModelNames(t *testing.T) {
	cases := map[string][]string{
		`{"data":[{"id":"gpt-5"},{"id":"deepseek-chat"}]}`: {"deepseek-chat", "gpt-5"},
		`{"data":[{"id":"1","name":"my-model"}]}`:          {"my-model"},
		`{"models":[{"name":"llama3"}]}`:                   {"llama3"},
		`["a","b","a"]`:                                    {"a", "b"},
		`{"object":"list","data":[]}`:                      nil,
		`not json`:                                         nil,
	}
	for in, want := range cases {
		got := parseModelNames([]byte(in))
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Errorf("parseModelNames(%s) = %v，想要 %v", in, got, want)
		}
	}
}

// TestModelURLs：带不带 /v1 两种写法都试，已经贴了 /models 的不重复拼。
func TestModelURLs(t *testing.T) {
	if got := modelURLs("https://api.deepseek.com"); len(got) != 2 || got[0] != "https://api.deepseek.com/v1/models" {
		t.Errorf("无 /v1 的 base: %v", got)
	}
	if got := modelURLs("https://gw.example.com/v1"); len(got) != 1 || got[0] != "https://gw.example.com/v1/models" {
		t.Errorf("带 /v1 的 base: %v", got)
	}
	if got := modelURLs("https://gw.example.com/v1/models"); len(got) != 1 || got[0] != "https://gw.example.com/v1/models" {
		t.Errorf("已经是 /models: %v", got)
	}
}

// TestUsableRejectsPlaceholders：《newgate init》铺出来的占位配置**文件都在**，
// 但它跑不起来。把它当成「已经配好了」会让向导跳过唯一能让人跑起来的那一步。
func TestUsableRejectsPlaceholders(t *testing.T) {
	seedSandbox(t)
	if _, err := store.Init(false); err != nil {
		t.Fatal(err)
	}
	if Inspect().HasUsable {
		t.Fatal("占位配置被判成可用")
	}
	// 写了真 key 之后它才可用（占位里的 provider 名与模型名都还在，只是 key 有了）。
	provs, err := store.LoadProviders()
	if err != nil {
		t.Fatal(err)
	}
	for name, p := range provs.Providers {
		p.APIKey = "sk-real"
		p.APIKeyEnv = ""
		provs.Providers[name] = p
	}
	if err := store.SaveProviders(provs); err != nil {
		t.Fatal(err)
	}
	if !Inspect().HasUsable {
		t.Fatal("有 key 的配置仍然被判成不可用——向导会白问一遍上游")
	}
}

// TestPortOf：0 / 负数落回 8899（向导要把它交给接管与 Start）。
func TestPortOf(t *testing.T) {
	if portOf(0) != domain.ProxyPort || portOf(-1) != domain.ProxyPort || portOf(9000) != 9000 {
		t.Fatal("portOf 的收敛不对")
	}
}
