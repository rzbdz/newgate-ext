package special

import (
	"strings"
	"testing"

	"github.com/rzbdz/newgate-ext/modules/gateway/rewrite"
)

type switchTestPlugin struct{}

func (switchTestPlugin) Name() string { return "switch-test" }
func (switchTestPlugin) Why() string  { return "tests the generic off switch" }
func (switchTestPlugin) Match(request *Request) bool {
	return request != nil && request.Provider == "off-test"
}
func (switchTestPlugin) Apply(body []byte, _ *Request) ([]byte, []string, error) {
	out, err := rewrite.InsertTopLevelRaw(body, "test_marker", []byte("true"))
	return out, []string{"marked"}, err
}

func init() { Register(switchTestPlugin{}) }

// req 一条不带 agent 身份的请求上下文（裸 /v1，认不出是谁）。
// 测「按上游认领」的 Match/Apply 用它；要区分发起方的（deepseek 的关思考、
// claude-bg）用 claudeReq——接管时只有 claude 的 base URL 带 /a/claude。
func req(model, provider, baseURL string) *Request {
	return &Request{
		InModel: "heavy", Tier: "heavy",
		Model: model, Provider: provider, BaseURL: baseURL,
		Protocol: "anthropic", Path: "/messages",
	}
}

// ---- 框架本身 ----

func TestApply_SkipsNonMatching(t *testing.T) {
	body := []byte(`{"model":"claude-sonnet-4","messages":[{"role":"assistant","content":"a"}]}`)
	res := Apply(body, req("claude-sonnet-4", "anth", "https://api.anthropic.com/v1"), nil)
	if res.Changed {
		t.Fatalf("不该改 Anthropic 的请求: %s", res.Body)
	}
	if string(res.Body) != string(body) {
		t.Fatal("body 被动了")
	}
}

func TestApply_HonorsOffSwitch(t *testing.T) {
	body := []byte(`{"model":"test","messages":[]}`)
	r := req("test", "off-test", "https://x/v1")

	if res := Apply(body, r, nil); !res.Changed {
		t.Fatal("默认应该生效")
	}
	off := func(name string) bool { return name == "switch-test" }
	res := Apply(body, r, off)
	if res.Changed || string(res.Body) != string(body) {
		t.Fatalf("关掉了还在改: %s", res.Body)
	}
}

// TestApply_FailOpen 插件报错时请求必须原样发出去，而且要留下痕迹。
func TestApply_FailOpen(t *testing.T) {
	saved := currentRegistry()
	defer SetDefault(saved)
	temporary := NewRegistry()
	SetDefault(temporary)
	temporary.Register(boom{})

	body := []byte(`{"model":"x"}`)
	res := Apply(body, req("x", "y", "z"), nil)
	if res.Changed || string(res.Body) != string(body) {
		t.Fatal("插件报错后 body 不该变")
	}
	if len(res.Notes) != 1 || !strings.Contains(res.Notes[0], "boom") {
		t.Fatalf("报错没被记录: %v", res.Notes)
	}
}

type boom struct{}

func (boom) Name() string        { return "boom" }
func (boom) Why() string         { return "测试用" }
func (boom) Match(*Request) bool { return true }
func (boom) Apply(b []byte, r *Request) ([]byte, []string, error) {
	return []byte(`{"毁了":true}`), []string{"改了"}, errFake
}

// fakePlugin 测试里临时拼注册表用的小插件。
type fakePlugin struct {
	name  string
	match func(*Request) bool
	apply func([]byte, *Request) ([]byte, []string, error)
}

func (f fakePlugin) Name() string          { return f.name }
func (f fakePlugin) Why() string           { return "测试用" }
func (f fakePlugin) Match(r *Request) bool { return f.match(r) }
func (f fakePlugin) Apply(b []byte, r *Request) ([]byte, []string, error) {
	return f.apply(b, r)
}

var errFake = &fakeErr{}

type fakeErr struct{}

func (*fakeErr) Error() string { return "故意失败" }

// TestApply_SyncsContextModel 锁死装饰器链的上下文语义：插件改了 body 的
// model，框架负责让 r.Model 跟上——下一个插件（Match 和 Apply 都拿 r）
// 看到的必须是改过的最新值。这不再是插件自己的义务：忘了维护就失真
// （2026-09-09 实抓：分类器被切到 light 后，还是按 mid 模型的 quirk 被翻
// 了 thinking）。用两个假插件直接验证机制本身。
func TestApply_SyncsContextModel(t *testing.T) {
	saved := currentRegistry()
	defer SetDefault(saved)
	temporary := NewRegistry()
	SetDefault(temporary)

	seen := ""
	watcher := fakePlugin{
		name:  "watcher",
		match: func(*Request) bool { return true },
		apply: func(b []byte, r *Request) ([]byte, []string, error) {
			seen = r.Model
			return b, nil, nil
		},
	}
	switcher := fakePlugin{
		name:  "switcher",
		match: func(*Request) bool { return true },
		apply: func(b []byte, r *Request) ([]byte, []string, error) {
			nb, err := rewrite.ReplaceTopLevelString(b, "model", "switched-model")
			if err != nil {
				return b, nil, err
			}
			return nb, []string{"切了"}, nil
		},
	}
	temporary.Register(switcher)
	temporary.Register(watcher)

	r := req("orig-model", "gw", "https://x/v1")
	res := Apply([]byte(`{"model":"orig-model","messages":[]}`), r, nil)

	if seen != "switched-model" {
		t.Fatalf("后面的插件看到的还是旧模型 %q，想要 switched-model", seen)
	}
	if r.Model != "switched-model" {
		t.Fatalf("Apply 之后 r.Model 应为 switched-model，实际 %s", r.Model)
	}
	if !res.Changed || !strings.Contains(string(res.Body), "switched-model") {
		t.Fatalf("body 没被切过去: %s", res.Body)
	}
}

// TestPluginsHaveWhy 每个插件都必须能说清自己为什么存在——将来判断
// 「上游修好了没、这段还要不要」全靠这句话。
func TestPluginsHaveWhy(t *testing.T) {
	ps := Plugins()
	if len(ps) == 0 {
		t.Fatal("一个插件都没注册")
	}
	seen := map[string]bool{}
	for _, p := range ps {
		if p.Name() == "" || len(p.Why()) < 10 {
			t.Errorf("插件 %q 的 Name/Why 不合格", p.Name())
		}
		if seen[p.Name()] {
			t.Errorf("插件名重复: %s", p.Name())
		}
		seen[p.Name()] = true
	}
}
