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

// req is a request context with no agent identity (a bare /v1, nobody recognizable).
// Match/Apply tests for "claim by upstream" use it; those that need to tell the initiator
// apart (deepseek's disable-thinking, claude-bg) use claudeReq - after takeover only claude's
// base URL carries /a/claude.
func req(model, provider, baseURL string) *Request {
	return &Request{
		InModel: "heavy", Tier: "heavy",
		Model: model, Provider: provider, BaseURL: baseURL,
		Protocol: "anthropic", Path: "/messages",
	}
}

// ---- The framework itself ----

func TestApply_SkipsNonMatching(t *testing.T) {
	body := []byte(`{"model":"claude-sonnet-4","messages":[{"role":"assistant","content":"a"}]}`)
	res := Apply(body, req("claude-sonnet-4", "anth", "https://api.anthropic.com/v1"), nil)
	if res.Changed {
		t.Fatalf("must not touch an Anthropic request: %s", res.Body)
	}
	if string(res.Body) != string(body) {
		t.Fatal("the body was touched")
	}
}

func TestApply_HonorsOffSwitch(t *testing.T) {
	body := []byte(`{"model":"test","messages":[]}`)
	r := req("test", "off-test", "https://x/v1")

	if res := Apply(body, r, nil); !res.Changed {
		t.Fatal("it should take effect by default")
	}
	off := func(name string) bool { return name == "switch-test" }
	res := Apply(body, r, off)
	if res.Changed || string(res.Body) != string(body) {
		t.Fatalf("it is still modifying with the plugin off: %s", res.Body)
	}
}

// TestApply_FailOpen: when a plugin errors the request must go out as it was, and a trace must
// be left.
func TestApply_FailOpen(t *testing.T) {
	saved := currentRegistry()
	defer SetDefault(saved)
	temporary := NewRegistry()
	SetDefault(temporary)
	temporary.Register(boom{})

	body := []byte(`{"model":"x"}`)
	res := Apply(body, req("x", "y", "z"), nil)
	if res.Changed || string(res.Body) != string(body) {
		t.Fatal("the body must not change after a plugin error")
	}
	if len(res.Notes) != 1 || !strings.Contains(res.Notes[0], "boom") {
		t.Fatalf("the error was not recorded: %v", res.Notes)
	}
}

type boom struct{}

func (boom) Name() string        { return "boom" }
func (boom) Why() string         { return "for testing" }
func (boom) Match(*Request) bool { return true }
func (boom) Apply(b []byte, r *Request) ([]byte, []string, error) {
	return []byte(`{"ruined":true}`), []string{"changed"}, errFake
}

// fakePlugin is a small plugin assembled ad hoc into a registry in tests.
type fakePlugin struct {
	name  string
	match func(*Request) bool
	apply func([]byte, *Request) ([]byte, []string, error)
}

func (f fakePlugin) Name() string          { return f.name }
func (f fakePlugin) Why() string           { return "for testing" }
func (f fakePlugin) Match(r *Request) bool { return f.match(r) }
func (f fakePlugin) Apply(b []byte, r *Request) ([]byte, []string, error) {
	return f.apply(b, r)
}

var errFake = &fakeErr{}

type fakeErr struct{}

func (*fakeErr) Error() string { return "fail on purpose" }

// TestApply_SyncsContextModel pins the decorator chain's context semantics: a plugin changes
// the body's model and the framework is responsible for keeping r.Model in step - the next
// plugin (Match and Apply both take r) must see the latest rewritten value. This is no longer
// the plugin's own obligation: forgetting it means distortion (captured 2026-09-09: after the
// classifier was switched to light, its thinking was still flipped per the mid model's quirk).
// Two fake plugins verify the mechanism itself directly.
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
			return nb, []string{"switched"}, nil
		},
	}
	temporary.Register(switcher)
	temporary.Register(watcher)

	r := req("orig-model", "gw", "https://x/v1")
	res := Apply([]byte(`{"model":"orig-model","messages":[]}`), r, nil)

	if seen != "switched-model" {
		t.Fatalf("the following plugins still see the old model %q, want switched-model", seen)
	}
	if r.Model != "switched-model" {
		t.Fatalf("after Apply r.Model should be switched-model, got %s", r.Model)
	}
	if !res.Changed || !strings.Contains(string(res.Body), "switched-model") {
		t.Fatalf("the body was not switched: %s", res.Body)
	}
}

// TestPluginsHaveWhy: every plugin must be able to say clearly why it exists - deciding
// "has the upstream fixed it, is this still needed" later rests entirely on that sentence.
func TestPluginsHaveWhy(t *testing.T) {
	ps := Plugins()
	if len(ps) == 0 {
		t.Fatal("no plugin was registered")
	}
	seen := map[string]bool{}
	for _, p := range ps {
		if p.Name() == "" || len(p.Why()) < 10 {
			t.Errorf("plugin %q has an invalid Name/Why", p.Name())
		}
		if seen[p.Name()] {
			t.Errorf("duplicate plugin name: %s", p.Name())
		}
		seen[p.Name()] = true
	}
}
