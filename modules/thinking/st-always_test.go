package thinking

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/rzbdz/newgate-ext/modules/config/domain"
	"github.com/rzbdz/newgate-ext/modules/gateway/quirk"
	"github.com/rzbdz/newgate-ext/modules/gateway/rewrite"
	"github.com/rzbdz/newgate-ext/modules/gateway/special"
)

const sysMarker = `"system":[{"type":"text","text":"You are a security monitor for autonomous AI coding agents."}]`

type testClaudeBackground struct{}

func (testClaudeBackground) Name() string { return "claude-bg" }
func (testClaudeBackground) Why() string  { return "test fixture" }
func (testClaudeBackground) Before() []string {
	return []string{"deepseek", "glm", "always-thinks"}
}
func (testClaudeBackground) After() []string { return nil }
func (testClaudeBackground) Match(r *special.Request) bool {
	return r != nil && r.Agent == "claude" && !r.Stream
}
func (testClaudeBackground) Route(body []byte, r *special.Request, state *domain.State) (special.RouteDecision, bool) {
	if !(testClaudeBackground{}).Match(r) ||
		!bytes.Contains(body, []byte("You are a security monitor")) {
		return special.RouteDecision{}, false
	}
	return special.RouteDecision{Tier: "light"}, true
}
func (testClaudeBackground) Apply(body []byte, r *special.Request) ([]byte, []string, error) {
	return BestEffortDisableThink(body, r)
}

func init() {
	special.Register(testClaudeBackground{})
	special.Register(alwaysThinks{})
}

// markAlwaysThinks 模拟「转发时撞过一次 1210」之后的 quirk 注册表状态。
// 注册表是全局的，用完必须 Reset，别脏了别的测试。
func markAlwaysThinks(t *testing.T, provider, model string) {
	t.Helper()
	quirk.Default.Reset()
	quirk.Default.Mark(provider, model, quirk.NoThinkingDisable)
	t.Cleanup(quirk.Default.Reset)
}

func req(model, provider, baseURL string) *special.Request {
	return &special.Request{
		Model: model, Provider: provider, BaseURL: baseURL,
		Protocol: "anthropic", Path: "/messages",
		// 网关在真实路径上会把这张表带在每次请求上（special.Request.Quirks）。
		// 测试必须照着补上——不补就等于模拟了一个「网关没给表」的世界，那正是
		// 这条依赖从「包级全局」改成「请求字段」之后暴露出来的东西。
		Quirks: quirk.Default,
	}
}

func TestAlwaysThinksMatch(t *testing.T) {
	markAlwaysThinks(t, "smt-glm", "glm-5.3")
	cases := []struct {
		name string
		r    *special.Request
		want bool
	}{
		{"quirk 学过的模型", req("glm-5.3", "smt-glm", "https://x/v1"), true},
		{"同 provider 别的模型", req("glm-4.5-air", "smt-glm", "https://x/v1"), false},
		{"同模型别的 provider", req("glm-5.3", "other", "https://x/v1"), false},
		{"nil", nil, false},
	}
	for _, c := range cases {
		if got := (alwaysThinks{}).Match(c.r); got != c.want {
			t.Errorf("%s: Match=%v want %v", c.name, got, c.want)
		}
	}
}

func TestAlwaysThinksApply(t *testing.T) {
	markAlwaysThinks(t, "smt-glm", "glm-5.3")
	r := req("glm-5.3", "smt-glm", "https://x/v1")

	t.Run("disabled → enabled + 补 effort", func(t *testing.T) {
		body := []byte(`{"model":"glm-5.3","thinking":{"type":"disabled"},"messages":[]}`)
		out, notes, err := (alwaysThinks{}).Apply(body, r)
		if err != nil {
			t.Fatal(err)
		}
		var m map[string]interface{}
		if err := json.Unmarshal(out, &m); err != nil {
			t.Fatalf("改完不是合法 JSON: %v", err)
		}
		if th, _ := m["thinking"].(map[string]interface{}); th["type"] != "enabled" {
			t.Fatalf("thinking 应为 enabled，实际 %v", m["thinking"])
		}
		if m["reasoning_effort"] != "low" {
			t.Fatalf("reasoning_effort 应补 low，实际 %v", m["reasoning_effort"])
		}
		if len(notes) != 2 {
			t.Fatalf("两笔改动都要有 note，实际 %v", notes)
		}
	})

	t.Run("adaptive 不动（客户端显式要思考）", func(t *testing.T) {
		// 回归（2026-09-09 实抓）：quirk 学到之后，主循环每个流式请求都被
		// 塞了 reasoning_effort:low，压低了 adaptive 思考的深度。显式带了
		// thinking 的请求本来就 200，一个字节都不该动。
		body := []byte(`{"model":"glm-5.3","stream":true,"max_tokens":32000,` +
			`"thinking":{"type":"adaptive"},"tools":[{"name":"Bash"}],"messages":[]}`)
		out, notes, err := (alwaysThinks{}).Apply(body, r)
		if err != nil {
			t.Fatal(err)
		}
		if string(out) != string(body) || len(notes) != 0 {
			t.Fatalf("adaptive 请求不该被动: out=%s notes=%v", out, notes)
		}
	})

	t.Run("没 thinking 但带 tools → 只补 effort", func(t *testing.T) {
		// 原始 1210 场景：tools 在场而没写 thinking，聚合器隐式替我们关
		// 思考，glm-5.3 直接拒。
		body := []byte(`{"model":"glm-5.3","tools":[{"name":"Bash"}],"messages":[]}`)
		out, notes, err := (alwaysThinks{}).Apply(body, r)
		if err != nil {
			t.Fatal(err)
		}
		var m map[string]interface{}
		_ = json.Unmarshal(out, &m)
		if m["reasoning_effort"] != "low" {
			t.Fatalf("reasoning_effort 应补 low，实际 %v", m["reasoning_effort"])
		}
		if _, has := m["thinking"]; has {
			t.Fatalf("不该塞 thinking: %s", out)
		}
		if len(notes) != 1 || !strings.Contains(notes[0], "reasoning_effort") {
			t.Fatalf("只该有补 effort 一笔，实际 %v", notes)
		}
	})

	t.Run("没 thinking 没 tools → 不动", func(t *testing.T) {
		body := []byte(`{"model":"glm-5.3","messages":[]}`)
		out, notes, err := (alwaysThinks{}).Apply(body, r)
		if err != nil {
			t.Fatal(err)
		}
		if string(out) != string(body) || len(notes) != 0 {
			t.Fatalf("实测这个形态本来就 200，不该动: out=%s notes=%v", out, notes)
		}
	})

	t.Run("已设 effort → 只翻 thinking", func(t *testing.T) {
		body := []byte(`{"model":"glm-5.3","thinking":{"type":"disabled"},` +
			`"reasoning_effort":"high","messages":[]}`)
		out, notes, err := (alwaysThinks{}).Apply(body, r)
		if err != nil {
			t.Fatal(err)
		}
		var m map[string]interface{}
		_ = json.Unmarshal(out, &m)
		if th, _ := m["thinking"].(map[string]interface{}); th["type"] != "enabled" {
			t.Fatalf("thinking 应为 enabled，实际 %v", m["thinking"])
		}
		if m["reasoning_effort"] != "high" {
			t.Fatalf("客户端的 effort 不该被覆盖，实际 %v", m["reasoning_effort"])
		}
		if len(notes) != 1 {
			t.Fatalf("只该有一笔，实际 %v", notes)
		}
	})
}

// TestAlwaysThinksResponsesEffort 钉住 Codex（Responses 方言）那条路。
//
// 现场（2026-09-22，Codex 0.155.1 → smt-glm/glm-5.3，路径 /a/codex/v1/responses）：
// 推理强度住在**嵌套的 reasoning 对象**里，不叫顶层 reasoning_effort，于是
// 2026-09-22 之前本插件一个字节都不动地把它发出去，撞 1210。而症状是
// **HTTP 200 + 事件流里一条 response.failed**，Codex 显示成
// 「stream disconnected before completion」——只看状态码的判据在这里全绿。
//
// 每一格都是实测出来的（low/high/max → 200，medium/minimal/none → 400，
// effort 缺席 → 200），所以这里逐格钉住：**收下的值一个字节都不许动**，
// 不收的才换掉。
func TestAlwaysThinksResponsesEffort(t *testing.T) {
	markAlwaysThinks(t, "smt-glm", "glm-5.3")
	r := req("glm-5.3", "smt-glm", "https://x/v1")
	r.Path = "/responses"

	apply := func(t *testing.T, body string) (string, []string) {
		t.Helper()
		out, notes, err := (alwaysThinks{}).Apply([]byte(body), r)
		if err != nil {
			t.Fatalf("Apply 出错: %v", err)
		}
		return string(out), notes
	}

	// 1) 上游不收的值 → 换成 low，并且**说明白**（不静默）。
	for _, was := range []string{"medium", "minimal", "none"} {
		t.Run("不收的 "+was+" → low", func(t *testing.T) {
			body := `{"model":"glm-5.3","reasoning":{"effort":"` + was + `"},"input":[]}`
			out, notes := apply(t, body)
			var m map[string]interface{}
			if err := json.Unmarshal([]byte(out), &m); err != nil {
				t.Fatalf("改完不是合法 JSON: %v\n%s", err, out)
			}
			rz, _ := m["reasoning"].(map[string]interface{})
			if rz["effort"] != "low" {
				t.Fatalf("effort 应为 low，实际 %v（%s）", rz["effort"], out)
			}
			if len(notes) != 1 || !strings.Contains(notes[0], was) {
				t.Fatalf("改了就要有一笔带原值的 note，实际 %v", notes)
			}
		})
	}

	// 2) 上游收下的值 → **逐字节不动**。多认一个值（high/max 也收）不是宽容，
	//    是「不许替用户降档」：只认 low 会把用户明确设的 high 改成 low，那是
	//    静默改变行为。
	for _, keep := range []string{"low", "high", "max"} {
		t.Run("收的 "+keep+" 一个字节都不动", func(t *testing.T) {
			body := `{"model":"glm-5.3","reasoning":{"effort":"` + keep + `"},"input":[]}`
			out, notes := apply(t, body)
			if out != body || len(notes) != 0 {
				t.Fatalf("收下的值不该被动: out=%s notes=%v", out, notes)
			}
		})
	}

	// 3) 形状不认识就不猜。effort 缺席是**常态**（实测 Codex 只发
	//    {"summary":"auto"} 时上游 200），动它是无依据的。
	for _, body := range []string{
		`{"model":"glm-5.3","reasoning":{"summary":"auto"},"input":[]}`,
		`{"model":"glm-5.3","reasoning":{},"input":[]}`,
		`{"model":"glm-5.3","reasoning":{"effort":null},"input":[]}`,
		`{"model":"glm-5.3","reasoning":{"effort":""},"input":[]}`,
		`{"model":"glm-5.3","reasoning":"auto","input":[]}`,
		`{"model":"glm-5.3","input":[]}`,
	} {
		t.Run("形状不认识就不动: "+body, func(t *testing.T) {
			out, notes := apply(t, body)
			if out != body || len(notes) != 0 {
				t.Fatalf("不该动: out=%s notes=%v", out, notes)
			}
		})
	}

	// 4) 只动那一个值的字节区间：兄弟键（Codex 会把 summary 放在这儿）与
	//    body 里其余每一个字节都原样保留——「请求体不做 JSON 往返」在
	//    这一手上的落点。用一个刻意丑的排版来钉（JSON 往返会把它抹平）。
	t.Run("兄弟键与其余字节原样保留", func(t *testing.T) {
		body := `{"model":"glm-5.3","reasoning":{"summary":"auto","effort":"medium","n":1},` +
			`"input":[{"role":"user","content":"hi"}],"metadata":{"z":1,"a":2}}`
		out, notes := apply(t, body)
		want := strings.Replace(body, `"effort":"medium"`, `"effort":"low"`, 1)
		if out != want {
			t.Fatalf("只该动 effort 那一段\n got: %s\nwant: %s", out, want)
		}
		if len(notes) != 1 {
			t.Fatalf("要有一笔 note，实际 %v", notes)
		}
	})

	// 5) 模型守卫：body 已被切到别的模型时不掺和（quirk 是 glm-5.3 的，
	//    「glm-5.3 不收 medium」推不出「glm-4.5-air 不收」）。
	t.Run("body 换成别的模型就不动", func(t *testing.T) {
		r2 := req("glm-5.3", "smt-glm", "https://x/v1")
		body := `{"model":"glm-4.5-air","reasoning":{"effort":"medium"},"input":[]}`
		out, notes, err := (alwaysThinks{}).Apply([]byte(body), r2)
		if err != nil {
			t.Fatal(err)
		}
		if string(out) != body || len(notes) != 0 {
			t.Fatalf("换模型之后不该按 glm-5.3 的 quirk 动手: out=%s notes=%v", out, notes)
		}
	})
}

// TestAlwaysThinksMatchFallsBackToProbeCache 钉住 Match 的**第二条**来源。
//
// 为什么它是必须的：quirk 表活内存里，进程重启就空了；而 codex 那条路
// （HTTP 200 + 事件流里的 response.failed）根本走不到 learnQuirks（它只看
// status>=400）。两条加起来 = 每次换版/重启后的第一发 Codex→GLM 必然 400，
// 而用户看到的是一句「stream disconnected before completion」。2026-09-22
// 实测到的正是这个形态。
//
// 所以 Match 除了手里那张表，还要问探活落盘的那份缓存（probe-capabilities.json）。
// 这里把磁盘那一路换成假的——真读盘会依赖开发机上恰好探过什么，那种测试
// 会因为别人的配置变红或变绿。
func TestAlwaysThinksMatchFallsBackToProbeCache(t *testing.T) {
	quirk.Default.Reset()
	t.Cleanup(quirk.Default.Reset)

	orig := cachedNoThinkingDisable
	t.Cleanup(func() { cachedNoThinkingDisable = orig })

	// 内存表空、缓存说有 → Match 必须绿。
	cachedNoThinkingDisable = func(provider, model string) bool {
		return provider == "smt-glm" && model == "glm-5.3"
	}
	r := req("glm-5.3", "smt-glm", "https://x/v1")
	if !(alwaysThinks{}).Match(r) {
		t.Fatal("内存表空、缓存有 → 必须匹配（否则重启后第一发必 400）")
	}
	if (alwaysThinks{}).Match(req("glm-4.5-air", "smt-glm", "https://x/v1")) {
		t.Fatal("缓存里没有的模型不该匹配")
	}

	// 缓存读不出来（文件坏/权限不对）按「不知道」处理：少修一发（上游报错，
	// 用户看得见原文）好过凭一份读不出来的文件去改请求体。
	cachedNoThinkingDisable = func(string, string) bool { return false }
	quirk.Default.Mark("smt-glm", "glm-5.3", quirk.NoThinkingDisable)
	if !(alwaysThinks{}).Match(r) {
		t.Fatal("缓存读不出来时，内存表学到的那些仍然要生效")
	}
}

// TestAlwaysThinksSkipsRoutedClassifier 回归（2026-09-09 实抓 #12 的路由版）：
// quirk 学到 glm-5.3 之后，分类器请求必须**整个改道 light 链**——Route
// 在建链之前决定，forward 的链循环把 body 的 model 换成 light 链头
// （glm-4.5-air）之后 special 才看到它。于是 glm-5.3 的 quirk
// （NoThinkingDisable）从头到尾没机会掺和：thinking 保持 disabled、
// 不补 reasoning_effort。走全注册表按真实顺序验证端到端结果。
func TestAlwaysThinksSkipsRoutedClassifier(t *testing.T) {
	markAlwaysThinks(t, "smt-glm", "glm-5.3")
	body := []byte(`{"model":"glm-5.3","max_tokens":2112,` + sysMarker +
		`,"messages":[{"role":"user","content":"classify"}]}`)

	// 1) 路由层：分类器改道 light（forward 在解析链之前问这里）
	if decision, ok := special.Route(body, &special.Request{Agent: "claude"}, &domain.State{}); !ok || decision.Tier != "light" {
		t.Fatalf("分类器应改道 light 链，实际 %+v", decision)
	}
	// 2) 链循环：body 的 model 换成 light 链头
	body, err := rewrite.ReplaceTopLevelString(body, "model", "glm-4.5-air")
	if err != nil {
		t.Fatal(err)
	}
	// 3) special 层：r.Model 从一开始就是 air
	r := req("glm-4.5-air", "smt-glm", "https://x/v1")
	r.Agent, r.Stream, r.Tier = "claude", false, "light"

	res := special.Apply(body, r, nil)
	var m map[string]interface{}
	if err := json.Unmarshal(res.Body, &m); err != nil {
		t.Fatalf("改完不是合法 JSON: %v\n%s", err, res.Body)
	}
	if m["model"] != "glm-4.5-air" {
		t.Fatalf("模型应保持 light 链头，实际 %v", m["model"])
	}
	if th, _ := m["thinking"].(map[string]interface{}); th["type"] != "disabled" {
		t.Fatalf("air 接受 disabled，thinking 不该被 glm-5.3 的 quirk 翻回: %v", m["thinking"])
	}
	if _, has := m["reasoning_effort"]; has {
		t.Fatalf("不该有 reasoning_effort: %s", res.Body)
	}
	for _, n := range res.Notes {
		if strings.Contains(n, "always-thinks") {
			t.Fatalf("always-thinks 不该掺和改道后的请求: %v", res.Notes)
		}
	}
}

// TestBestEffortDisableThinkDumbLightConfig 错配现场（2026-09-09 用户提的
// 场景）：用户把「不支持关思考」的模型配进了 light 档。best effort 的含义
// 就在这里——意图照样落地，模型听不懂就翻成它听得懂的最小思考，请求
// 活着（慢就慢，由他去了），绝不因为关不掉而失败。
func TestBestEffortDisableThinkDumbLightConfig(t *testing.T) {
	markAlwaysThinks(t, "smt-glm", "glm-5.3")
	body := []byte(`{"model":"glm-5.3","max_tokens":2112,` + sysMarker +
		`,"messages":[{"role":"user","content":"classify"}]}`)

	// 路由照走 light（错配下 light 链头就是 glm-5.3 本尊）
	if decision, ok := special.Route(body, &special.Request{Agent: "claude"}, &domain.State{}); !ok || decision.Tier != "light" {
		t.Fatalf("错配不改路由决策，实际 %+v", decision)
	}
	// light 链头：r.Model = glm-5.3；BestEffortDisableThink 一个操作完成
	// 意图落地 + 翻译
	r := req("glm-5.3", "smt-glm", "https://x/v1")
	r.Agent, r.Stream, r.Tier = "claude", false, "light"

	res := special.Apply(body, r, nil) // 走全注册表：claude-bg → always-thinks
	var m map[string]interface{}
	if err := json.Unmarshal(res.Body, &m); err != nil {
		t.Fatalf("改完不是合法 JSON: %v\n%s", err, res.Body)
	}
	if th, _ := m["thinking"].(map[string]interface{}); th["type"] != "enabled" {
		t.Fatalf("不支持关思考的模型应被翻译成 enabled，实际 %v", m["thinking"])
	}
	if m["reasoning_effort"] != "low" {
		t.Fatalf("应补 reasoning_effort=low，实际 %v", m["reasoning_effort"])
	}
}

// TestBestEffortDisableThinkModelGuard 翻译按 (provider, r.Model) 的 quirk
// 决定：body 已被切到别的模型时不掺和（写意图不受影响——那是模型无关的）。
func TestBestEffortDisableThinkModelGuard(t *testing.T) {
	markAlwaysThinks(t, "smt-glm", "glm-5.3")
	// r.Model 还是 glm-5.3（quirk 在它身上），但 body 的 model 已是 air
	r := req("glm-5.3", "smt-glm", "https://x/v1")
	body := []byte(`{"model":"glm-4.5-air","messages":[]}`)

	out, notes, err := BestEffortDisableThink(body, r)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]interface{}
	_ = json.Unmarshal(out, &m)
	// 意图落地了（模型无关）……
	if th, _ := m["thinking"].(map[string]interface{}); th["type"] != "disabled" {
		t.Fatalf("写意图不受模型守卫影响，实际 %v", m["thinking"])
	}
	// ……但 glm-5.3 的 quirk 没有翻它（air 不背这口锅）
	if m["reasoning_effort"] != nil {
		t.Fatalf("不该按 glm-5.3 的 quirk 给 air 补 effort: %s", out)
	}
	for _, n := range notes {
		if strings.Contains(n, "always-thinks") || strings.Contains(n, "不支持关闭思考") {
			t.Fatalf("翻译不该发生: %v", notes)
		}
	}
}

// TestRegistrationOrder 组合语义靠显式优先级，而不是文件名或 init 顺序。
func TestRegistrationOrder(t *testing.T) {
	var names []string
	for _, p := range special.Plugins() {
		names = append(names, p.Name())
	}
	want := []string{"claude-bg", "always-thinks"}
	if len(names) != len(want) {
		t.Fatalf("注册表变了（%v），先把 want 更新成新事实再想是不是有意的", names)
	}
	for i := range want {
		if names[i] != want[i] {
			t.Fatalf("注册顺序错了: %v\n想要: %v", names, want)
		}
	}
}
