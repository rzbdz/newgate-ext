package system_test

import (
	"bytes"
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/rzbdz/newgate-ext/manifest"
	breakerapi "github.com/rzbdz/newgate-ext/modules/breaker"
	"github.com/rzbdz/newgate-ext/testing/system"
	modules "github.com/rzbdz/newgate/component"
)

// This group pins **the shape-criterion mechanism**: a criterion into the health table ->
// the data plane knows it -> these 400s count only, never tri -> evidence in the log.
//
// The criterion itself is **the test's own** (shapeProbe), not any upstream dialect.
// Before 2026-09-20 this used DeepSeek's criterion (`modules/deepseek/shape.go`), so
// **the kernel's test depended on a distribution module** - once "core only tests core"
// breaks, the kernel is never clean: remove the distribution module, the kernel test reds.
//
// Now the dialect side (which sentence, which field) belongs to the distribution: it runs
// the same assertions with the real criterion in its repo. What stays is the **causal
// chain**, every link the kernel's own: the registration port (breaker.RegisterShapeDetector),
// the data plane's claim (forward reads only Result.Shape), ledger semantics (count, no tri).

// shapeProbe is the kernel-side synthetic criterion: it claims "a 400 carrying must be passed".
//
// The match text deliberately equals the fake upstream's 400s (testing/upstream/dialect.go
// replicates the upstream), but the matching itself is **deliberately loose** - it need not
// tell dialects here, only be unique enough not to hit the control group's plain 400.
type shapeProbe struct{}

func (shapeProbe) Name() string { return "test-shape" }

func (shapeProbe) Match(status int, body []byte) bool {
	return status == http.StatusBadRequest && bytes.Contains(body, []byte("must be passed"))
}

// shapeProbeComponent registers the criterion into the health table, exactly like a real
// module (Need(breaker) -> Start registers -> Stop releases in reverse).
func shapeProbeComponent() modules.Component {
	var releases []modules.Release
	return modules.Component{
		Name: "shape-probe",
		Type: "example",
		Requires: []modules.Requirement{
			modules.Need(breakerapi.Capability),
		},
		Start: func(_ context.Context, ctx modules.Context) error {
			health := modules.MustGet(ctx, breakerapi.Capability)
			release, err := health.RegisterShapeDetector(shapeProbe{})
			if err != nil {
				return err
			}
			releases = append(releases, release)
			return nil
		},
		Stop: func(context.Context) error { return modules.ReleaseAll(releases) },
	}
}

// probeGraph is the product graph plus a synthetic criterion module.
//
// It used to be "the kernel's graph plus a probe"; the kernel ships none now, so the base is
// this distribution's flagship spec -- which is the graph the probe actually runs in, and
// therefore the one worth testing the wiring against.
//
// The probe component Needs the health table, so appending it to the frozen table would put a
// consumer after its provider only by luck. Recomputing the order is the honest thing here and
// it is exactly what component.Ordered is for (a test writing its own declaration).
func probeGraph() modules.Static {
	base := append([]modules.Component(nil),
		manifest.Specs()[manifest.DefaultSpec]...)
	base = append(base, shapeProbeComponent())
	ordered, err := modules.Ordered(base)
	if err != nil {
		panic("ordering the probe graph: " + err.Error())
	}
	return modules.Static(ordered)
}

// strictReasoningBody builds a request **the fake upstream rejects as measured** - the real
// form of the on-line "reasoning_content must be passed back" 400.
//
// The criterion is **tail shape** (rewritten 2026-09-18 from measurement; the criterion
// is testing/upstream/dialect.go's strictReasoningViolation, a verbatim stand-in for
// mock/fake_upstream.py):
//
//	last role:user content[] non-empty with **all** tool_result blocks -> 400
//
// This is Claude Code's main loop: the model calls a tool, the client sends the result
// back to continue, and that content[] has not one text block. **Nothing to do with
// reasoning fields** - measured with reasoning_content as real text, omitted, empty or a
// placeholder, this cell is always 400; one space in that content[] turns it into 200.
//
// Must be stream=true: a non-streaming /a/claude/ request gets thinking:disabled added
// by claudecode's claude-bg plugin, another path (shape ignores thinking, but the main
// loop is streaming anyway). upstream-a is on the chain, no module patches this tail.
func strictReasoningBody() string {
	return `{"model":"normal","max_tokens":16,"stream":true,` +
		`"tools":[{"name":"Bash","description":"run a command",` +
		`"input_schema":{"type":"object","properties":{}}}],` +
		`"messages":[` +
		`{"role":"user","content":[{"type":"text","text":"run it"}]},` +
		`{"role":"assistant","content":[{"type":"tool_use","id":"toolu_strict_1","name":"Bash","input":{}}]},` +
		`{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_strict_1","content":"ok"}]}]}`
}

// TestShapeDetectorIsWiredThroughTheRealGraph is the **integration-level** regression for
// the 2026-09-17 incident: full real graph + real forward + fake upstream, and a
// criterion registered in Start must really take effect.
//
// Why the whole graph, not another forward unit test: what broke was **wiring** - the
// criterion lived in a module, registered into the health table, injected into forward
// (forward.New's fourth parameter). Any wiring error (no Need breaker, handle not
// released in Stop, module missing from the manifest, harness calling newTable()), and
// unit tests stay green while production still drops that upstream. So assert the chain:
//
//	fake upstream strict 400 -> criterion claims -> count only, no tri -> log explains
//
// Live scene (~/.config/newgate/health.json + logs): the claimed provider was tripped
// after two consecutive 400s, while `newgate probe` stayed fluent - liveness sends a
// minimal request that never triggers "thinking must echo verbatim". Asymmetric cost:
// the user is silently moved to another model, then waits out a 60s+ cooldown.
func TestShapeDetectorIsWiredThroughTheRealGraph(t *testing.T) {
	h := system.StartWith(t, probeGraph())

	// Every candidate returns the same strict 400. Shape errors **do** continue down the
	// chain (another, laxer provider might accept the same body), so one client request hits
	// the upstream twice - cheap.mid's chain is upstream-a/model-medium -> upstream-b/model-medium.
	const hitsPerRequest = 2

	for i := 0; i < 3; i++ {
		resp := h.Post("/a/claude/v1/messages", strictReasoningBody())
		body := system.ReadBody(t, resp)
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("request %d status = %d, want 400 (the upstream body must pass through verbatim); body=%s",
				i+1, resp.StatusCode, body)
		}
		if !strings.Contains(body, "must be passed back") {
			t.Fatalf("request %d did not pass the upstream body to the client (never silent is a hard requirement); body=%s", i+1, body)
		}
	}

	if got := h.Upstream.Count(); got != 3*hitsPerRequest {
		t.Fatalf("the fake upstream received %d requests, want %d (each of the two candidates in the chain is tried once)",
			got, 3*hitsPerRequest)
	}

	// The **evidence** that the criterion claimed is in the log: the forward path knows no
	// upstream string; it reads Result.Shape's name, so the name must be printed, else nothing.
	if !h.WaitForLog(t, "[shape-400]") {
		t.Fatalf("no [shape-400] in the log -- the check did not claim it, or the forwarding path does not read Shape;\n%s",
			h.Logs())
	}
	// The assertion is **the criterion's name** (`detector test-shape`): names are machine
	// tags; a localized wording changes with language, this line's proof does not.
	// The product graph also ships the DeepSeek detector, registered earlier, and the
	// registry answers first-match — so the claim is legitimately `deepseek`. What
	// this test guards is that the wired detector's *name* reaches the log at all,
	// not that the synthetic one wins.
	if !strings.Contains(h.Logs(), "detector deepseek") && !strings.Contains(h.Logs(), "detector test-shape") {
		t.Fatalf("the log does not name the detector that claimed the 400 (with several "+
			"upstreams returning 400 this is the only thing that tells them apart);\n%s",
			h.Logs())
	}

	// Core assertion: the ledger is untouched. Open=true means the user's next call went elsewhere.
	skips := 0
	for _, s := range h.Breaker().Snapshot() {
		skips += s.ShapeSkips
		if s.ShapeSkips == 0 {
			continue
		}
		if s.ShapeSkips != 3 {
			t.Errorf("shape_skips for %s = %d, want 3 (one per client request)",
				s.Provider+"/"+s.Model, s.ShapeSkips)
		}
		if s.Fails != 0 || s.Open || s.State != "closed" {
			t.Errorf("%s was removed by a request-shape error: fails=%d open=%v state=%s reason=%q --"+
				"this kind of 400 is the same with any provider, so it must not count against any of them",
				s.Provider+"/"+s.Model, s.Fails, s.Open, s.State, s.Reason)
		}
	}
	if skips != 3*hitsPerRequest {
		t.Errorf("total shape_skips across all bindings = %d, want %d -- it is 0 when the check is not in effect,"+
			"which means this kind of 400 falls into the availability ledger again (threshold 2, it should be removed on the first request)",
			skips, 3*hitsPerRequest)
	}
}

// TestOtherClientErrorIsNotClaimedAsShape is the control group: the criterion must **choose**.
//
// A criterion claiming every 400 also greens the test above, but lets "the upstream is
// really rejecting us" through. A shape criterion exists for that distinction: only
// "the same body fails on another provider" is a request-shape problem; other 400s mean
// "your request is wrong".
//
// FailNext, not a different body: one request shape, only the upstream differs - one
// variable. A non-shape 400 neither counts nor moves, so the chain stops at the first one.
func TestOtherClientErrorIsNotClaimedAsShape(t *testing.T) {
	h := system.StartWith(t, probeGraph())

	h.Upstream.FailNext(http.StatusBadRequest)
	resp := h.Post("/a/claude/v1/messages", strictReasoningBody())
	body := system.ReadBody(t, resp)

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", resp.StatusCode, body)
	}
	if got := h.Upstream.Count(); got != 1 {
		t.Errorf("the fake upstream was hit %d times, want 1 -- a non-shape 400 must not fail over across every upstream in the chain", got)
	}
	if strings.Contains(h.Logs(), "[shape-400]") {
		t.Fatalf("a 400 unrelated to reasoning was judged a request-shape error: the check is too broad and will let real"+
			"availability failures through as well;\n%s", h.Logs())
	}
	for _, s := range h.Breaker().Snapshot() {
		if s.ShapeSkips != 0 || s.Fails != 0 || s.Open {
			t.Errorf("%s entered the ledger because of a 400 unrelated to it: %+v", s.Provider+"/"+s.Model, s)
		}
	}
}
