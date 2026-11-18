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

// markAlwaysThinks simulates the quirk registry state after "one 1210 hit while forwarding".
// The registry is global, so Reset after use - do not dirty other tests.
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
		// On the real path the gateway carries this table on every request (special.Request.Quirks).
		// The test must fill it in too - omitting it simulates "the gateway gave no table",
		// exactly what surfaced when this dependency moved from package global to request field.
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
		{"a model quirk has learned", req("glm-5.3", "smt-glm", "https://x/v1"), true},
		{"another model on the same provider", req("glm-4.5-air", "smt-glm", "https://x/v1"), false},
		{"another provider for the same model", req("glm-5.3", "other", "https://x/v1"), false},
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

	t.Run("disabled -> enabled + fill in effort", func(t *testing.T) {
		body := []byte(`{"model":"glm-5.3","thinking":{"type":"disabled"},"messages":[]}`)
		out, notes, err := (alwaysThinks{}).Apply(body, r)
		if err != nil {
			t.Fatal(err)
		}
		var m map[string]interface{}
		if err := json.Unmarshal(out, &m); err != nil {
			t.Fatalf("after the change it is not valid JSON: %v", err)
		}
		if th, _ := m["thinking"].(map[string]interface{}); th["type"] != "enabled" {
			t.Fatalf("thinking should be enabled, actual %v", m["thinking"])
		}
		if m["reasoning_effort"] != "low" {
			t.Fatalf("reasoning_effort should be filled in as low, actual %v", m["reasoning_effort"])
		}
		if len(notes) != 2 {
			t.Fatalf("both changes need a note, actual %v", notes)
		}
	})

	t.Run("leave adaptive alone (the client explicitly wants thinking)", func(t *testing.T) {
		// Regression (2026-09-09 live capture): once quirk learned, every streaming request on
		// the main loop got reasoning_effort:low stuffed in, lowering adaptive thinking depth.
		// Requests that explicitly carry thinking were already 200 and must not change a byte.
		body := []byte(`{"model":"glm-5.3","stream":true,"max_tokens":32000,` +
			`"thinking":{"type":"adaptive"},"tools":[{"name":"Bash"}],"messages":[]}`)
		out, notes, err := (alwaysThinks{}).Apply(body, r)
		if err != nil {
			t.Fatal(err)
		}
		if string(out) != string(body) || len(notes) != 0 {
			t.Fatalf("an adaptive request must not be touched: out=%s notes=%v", out, notes)
		}
	})

	t.Run("no thinking but with tools -> only fill in effort", func(t *testing.T) {
		// The original 1210 scene: tools present with no thinking, the aggregator disables
		// thinking for us, and glm-5.3 refuses outright.
		body := []byte(`{"model":"glm-5.3","tools":[{"name":"Bash"}],"messages":[]}`)
		out, notes, err := (alwaysThinks{}).Apply(body, r)
		if err != nil {
			t.Fatal(err)
		}
		var m map[string]interface{}
		_ = json.Unmarshal(out, &m)
		if m["reasoning_effort"] != "low" {
			t.Fatalf("reasoning_effort should be filled in as low, actual %v", m["reasoning_effort"])
		}
		if _, has := m["thinking"]; has {
			t.Fatalf("must not insert thinking: %s", out)
		}
		if len(notes) != 1 || !strings.Contains(notes[0], "reasoning_effort") {
			t.Fatalf("there should only be one effort fill-in, actual %v", notes)
		}
	})

	t.Run("no thinking and no tools -> leave it alone", func(t *testing.T) {
		body := []byte(`{"model":"glm-5.3","messages":[]}`)
		out, notes, err := (alwaysThinks{}).Apply(body, r)
		if err != nil {
			t.Fatal(err)
		}
		if string(out) != string(body) || len(notes) != 0 {
			t.Fatalf("this shape already gets 200 as measured, must not be touched: out=%s notes=%v", out, notes)
		}
	})

	t.Run("effort already set -> only flip thinking", func(t *testing.T) {
		body := []byte(`{"model":"glm-5.3","thinking":{"type":"disabled"},` +
			`"reasoning_effort":"high","messages":[]}`)
		out, notes, err := (alwaysThinks{}).Apply(body, r)
		if err != nil {
			t.Fatal(err)
		}
		var m map[string]interface{}
		_ = json.Unmarshal(out, &m)
		if th, _ := m["thinking"].(map[string]interface{}); th["type"] != "enabled" {
			t.Fatalf("thinking should be enabled, actual %v", m["thinking"])
		}
		if m["reasoning_effort"] != "high" {
			t.Fatalf("the client's effort must not be overwritten, actual %v", m["reasoning_effort"])
		}
		if len(notes) != 1 {
			t.Fatalf("there should only be one change, actual %v", notes)
		}
	})
}

// TestAlwaysThinksResponsesEffort pins the Codex (Responses dialect) path.
//
// Live (2026-09-22, Codex 0.155.1 → smt-glm/glm-5.3, path /a/codex/v1/responses):
// the effort lives in a **nested reasoning object**, not top-level reasoning_effort, so
// before 2026-09-22 this plugin sent it untouched and hit 1210. The symptom is **HTTP
// 200 plus a response.failed in the stream**, shown by Codex as "stream disconnected
// before completion" - status-only criteria are all green here.
//
// Every cell was measured (low/high/max → 200, medium/minimal/none → 400, effort
// absent → 200), so each is pinned: **an accepted value must not change a byte**;
// only rejected ones are replaced.
func TestAlwaysThinksResponsesEffort(t *testing.T) {
	markAlwaysThinks(t, "smt-glm", "glm-5.3")
	r := req("glm-5.3", "smt-glm", "https://x/v1")
	r.Path = "/responses"

	apply := func(t *testing.T, body string) (string, []string) {
		t.Helper()
		out, notes, err := (alwaysThinks{}).Apply([]byte(body), r)
		if err != nil {
			t.Fatalf("Apply errored: %v", err)
		}
		return string(out), notes
	}

	// 1) A value the upstream rejects → replace with low, and **say so** (not silently).
	for _, was := range []string{"medium", "minimal", "none"} {
		t.Run("the unaccepted "+was+" → low", func(t *testing.T) {
			body := `{"model":"glm-5.3","reasoning":{"effort":"` + was + `"},"input":[]}`
			out, notes := apply(t, body)
			var m map[string]interface{}
			if err := json.Unmarshal([]byte(out), &m); err != nil {
				t.Fatalf("after the change it is not valid JSON: %v\n%s", err, out)
			}
			rz, _ := m["reasoning"].(map[string]interface{})
			if rz["effort"] != "low" {
				t.Fatalf("effort should be low, actual %v (%s)", rz["effort"], out)
			}
			if len(notes) != 1 || !strings.Contains(notes[0], was) {
				t.Fatalf("if it changed there must be a note carrying the old value, actual %v", notes)
			}
		})
	}

	// 2) A value the upstream accepts → **byte for byte untouched**. Accepting one more
	//    value (high/max too) is not leniency but "never downgrade the user": low-only
	//    would turn an explicit high into low, which is silently changing behavior.
	for _, keep := range []string{"low", "high", "max"} {
		t.Run("the accepted "+keep+" not a single byte changes", func(t *testing.T) {
			body := `{"model":"glm-5.3","reasoning":{"effort":"` + keep + `"},"input":[]}`
			out, notes := apply(t, body)
			if out != body || len(notes) != 0 {
				t.Fatalf("an accepted value must not be touched: out=%s notes=%v", out, notes)
			}
		})
	}

	// 3) Unknown shape = do not guess. Absent effort is the **norm** (Codex sending only
	//    {"summary":"auto"} gets 200), so touching it has no basis.
	for _, body := range []string{
		`{"model":"glm-5.3","reasoning":{"summary":"auto"},"input":[]}`,
		`{"model":"glm-5.3","reasoning":{},"input":[]}`,
		`{"model":"glm-5.3","reasoning":{"effort":null},"input":[]}`,
		`{"model":"glm-5.3","reasoning":{"effort":""},"input":[]}`,
		`{"model":"glm-5.3","reasoning":"auto","input":[]}`,
		`{"model":"glm-5.3","input":[]}`,
	} {
		t.Run("an unrecognized shape is left alone: "+body, func(t *testing.T) {
			out, notes := apply(t, body)
			if out != body || len(notes) != 0 {
				t.Fatalf("must not be touched: out=%s notes=%v", out, notes)
			}
		})
	}

	// 4) Touch only that one value's byte range: sibling keys (Codex puts summary here)
	//    and every other byte in the body stay verbatim - "no JSON round trip on the
	//    body" lands here. A deliberately ugly layout pins it (a round trip would flatten it).
	t.Run("sibling keys and the rest of the bytes are preserved verbatim", func(t *testing.T) {
		body := `{"model":"glm-5.3","reasoning":{"summary":"auto","effort":"medium","n":1},` +
			`"input":[{"role":"user","content":"hi"}],"metadata":{"z":1,"a":2}}`
		out, notes := apply(t, body)
		want := strings.Replace(body, `"effort":"medium"`, `"effort":"low"`, 1)
		if out != want {
			t.Fatalf("only the effort segment should change\n got: %s\nwant: %s", out, want)
		}
		if len(notes) != 1 {
			t.Fatalf("there must be one note, actual %v", notes)
		}
	})

	// 5) Model guard: once the body has been switched to another model, stay out (the quirk
	//    is glm-5.3's; "glm-5.3 rejects medium" does not imply "glm-4.5-air rejects it").
	t.Run("a body for a different model is left alone", func(t *testing.T) {
		r2 := req("glm-5.3", "smt-glm", "https://x/v1")
		body := `{"model":"glm-4.5-air","reasoning":{"effort":"medium"},"input":[]}`
		out, notes, err := (alwaysThinks{}).Apply([]byte(body), r2)
		if err != nil {
			t.Fatal(err)
		}
		if string(out) != body || len(notes) != 0 {
			t.Fatalf("after the model change it must not act on glm-5.3's quirk: out=%s notes=%v", out, notes)
		}
	})
}

// TestAlwaysThinksMatchFallsBackToProbeCache pins Match's **second** source.
//
// Why it is necessary: the quirk table lives in memory and empties on restart; and the
// codex path (HTTP 200 + response.failed in the stream) never reaches learnQuirks (it
// only looks at status>=400). Together that means the first Codex→GLM request after
// every upgrade/restart always 400s, and the user sees "stream disconnected before
// completion". 2026-09-22 hit exactly this shape.
//
// So Match, besides the table in hand, also asks the probe cache on disk (probe-capabilities.json).
// Here the disk path is faked - real reads depend on what this dev machine happened to
// probe, and such a test would go red or green on someone else's config.
func TestAlwaysThinksMatchFallsBackToProbeCache(t *testing.T) {
	quirk.Default.Reset()
	t.Cleanup(quirk.Default.Reset)

	orig := cachedNoThinkingDisable
	t.Cleanup(func() { cachedNoThinkingDisable = orig })

	// In-memory table empty, cache says yes → Match must be green.
	cachedNoThinkingDisable = func(provider, model string) bool {
		return provider == "smt-glm" && model == "glm-5.3"
	}
	r := req("glm-5.3", "smt-glm", "https://x/v1")
	if !(alwaysThinks{}).Match(r) {
		t.Fatal("empty in-memory table, cache has it -> must match (otherwise the first request after a restart is a guaranteed 400)")
	}
	if (alwaysThinks{}).Match(req("glm-4.5-air", "smt-glm", "https://x/v1")) {
		t.Fatal("a model not in the cache must not match")
	}

	// Unreadable cache (bad file/permissions) counts as "unknown": skip one fix (the
	// upstream errors, the user sees the text) over rewriting a body from an unreadable file.
	cachedNoThinkingDisable = func(string, string) bool { return false }
	quirk.Default.Mark("smt-glm", "glm-5.3", quirk.NoThinkingDisable)
	if !(alwaysThinks{}).Match(r) {
		t.Fatal("when the cache cannot be read, what the in-memory table learned must still take effect")
	}
}

// TestAlwaysThinksSkipsRoutedClassifier regression (2026-09-09 live capture #12, routed):
// once quirk learned glm-5.3, a classifier request must reroute **the whole way to the
// light chain** - Route decides before the chain is built, forward's chain loop replaces
// the body model with the light head (glm-4.5-air), and only then does special see it. So
// glm-5.3's quirk (NoThinkingDisable) never gets a say: thinking stays disabled and no
// reasoning_effort is added. Run the full registry in real order for an end-to-end result.
func TestAlwaysThinksSkipsRoutedClassifier(t *testing.T) {
	markAlwaysThinks(t, "smt-glm", "glm-5.3")
	body := []byte(`{"model":"glm-5.3","max_tokens":2112,` + sysMarker +
		`,"messages":[{"role":"user","content":"classify"}]}`)

	// 1) Routing layer: the classifier reroutes to light (forward asks before parsing the chain)
	if decision, ok := special.Route(body, &special.Request{Agent: "claude"}, &domain.State{}); !ok || decision.Tier != "light" {
		t.Fatalf("the classifier should reroute to the light chain, actual %+v", decision)
	}
	// 2) Chain loop: the body model becomes the light chain head
	body, err := rewrite.ReplaceTopLevelString(body, "model", "glm-4.5-air")
	if err != nil {
		t.Fatal(err)
	}
	// 3) special layer: r.Model is air from the start
	r := req("glm-4.5-air", "smt-glm", "https://x/v1")
	r.Agent, r.Stream, r.Tier = "claude", false, "light"

	res := special.Apply(body, r, nil)
	var m map[string]interface{}
	if err := json.Unmarshal(res.Body, &m); err != nil {
		t.Fatalf("after the change it is not valid JSON: %v\n%s", err, res.Body)
	}
	if m["model"] != "glm-4.5-air" {
		t.Fatalf("the model should stay the head of the light chain, actual %v", m["model"])
	}
	if th, _ := m["thinking"].(map[string]interface{}); th["type"] != "disabled" {
		t.Fatalf("air accepts disabled, thinking must not be flipped back by glm-5.3's quirk: %v", m["thinking"])
	}
	if _, has := m["reasoning_effort"]; has {
		t.Fatalf("must not have reasoning_effort: %s", res.Body)
	}
	for _, n := range res.Notes {
		if strings.Contains(n, "always-thinks") {
			t.Fatalf("always-thinks must not meddle with a rerouted request: %v", res.Notes)
		}
	}
}

// TestBestEffortDisableThinkDumbLightConfig mismatch scene (2026-09-09 user scenario):
// the user put a "cannot disable thinking" model into the light tier. This is what best
// effort means - the intent still lands, a model that does not understand it gets the
// least thinking it does; the request lives (slow, but theirs), never fails for it.
func TestBestEffortDisableThinkDumbLightConfig(t *testing.T) {
	markAlwaysThinks(t, "smt-glm", "glm-5.3")
	body := []byte(`{"model":"glm-5.3","max_tokens":2112,` + sysMarker +
		`,"messages":[{"role":"user","content":"classify"}]}`)

	// Routing still goes to light (under the mismatch the light head is glm-5.3 itself)
	if decision, ok := special.Route(body, &special.Request{Agent: "claude"}, &domain.State{}); !ok || decision.Tier != "light" {
		t.Fatalf("a mismatch does not change the routing decision, actual %+v", decision)
	}
	// Light chain head: r.Model = glm-5.3; BestEffortDisableThink does intent + translate
	// in one operation
	r := req("glm-5.3", "smt-glm", "https://x/v1")
	r.Agent, r.Stream, r.Tier = "claude", false, "light"

	res := special.Apply(body, r, nil) // 走全注册表：claude-bg → always-thinks
	var m map[string]interface{}
	if err := json.Unmarshal(res.Body, &m); err != nil {
		t.Fatalf("after the change it is not valid JSON: %v\n%s", err, res.Body)
	}
	if th, _ := m["thinking"].(map[string]interface{}); th["type"] != "enabled" {
		t.Fatalf("a model that does not support disabling thinking should be translated to enabled, actual %v", m["thinking"])
	}
	if m["reasoning_effort"] != "low" {
		t.Fatalf("should fill in reasoning_effort=low, actual %v", m["reasoning_effort"])
	}
}

// TestBestEffortDisableThinkModelGuard translation follows the (provider, r.Model) quirk:
// stay out once the body is switched (the intent write is model-agnostic, unaffected).
func TestBestEffortDisableThinkModelGuard(t *testing.T) {
	markAlwaysThinks(t, "smt-glm", "glm-5.3")
	// r.Model is still glm-5.3 (the quirk is on it), but the body model is already air
	r := req("glm-5.3", "smt-glm", "https://x/v1")
	body := []byte(`{"model":"glm-4.5-air","messages":[]}`)

	out, notes, err := BestEffortDisableThink(body, r)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]interface{}
	_ = json.Unmarshal(out, &m)
	// The intent landed (model-agnostic)...
	if th, _ := m["thinking"].(map[string]interface{}); th["type"] != "disabled" {
		t.Fatalf("a write intent is not affected by the model guard, actual %v", m["thinking"])
	}
	// ...but glm-5.3's quirk did not translate it (air does not take that blame)
	if m["reasoning_effort"] != nil {
		t.Fatalf("must not fill in effort for air based on glm-5.3's quirk: %s", out)
	}
	for _, n := range notes {
		if strings.Contains(n, "always-thinks") || strings.Contains(n, "does not support disabling thinking") {
			t.Fatalf("the translation should not have happened: %v", notes)
		}
	}
}

// TestRegistrationOrder: composite semantics rest on explicit precedence, not file or init order.
func TestRegistrationOrder(t *testing.T) {
	var names []string
	for _, p := range special.Plugins() {
		names = append(names, p.Name())
	}
	want := []string{"claude-bg", "always-thinks"}
	if len(names) != len(want) {
		t.Fatalf("the registry changed (%v), update want to the new fact first and then think about whether it was intentional", names)
	}
	for i := range want {
		if names[i] != want[i] {
			t.Fatalf("registration order is wrong: %v\nwant: %v", names, want)
		}
	}
}
