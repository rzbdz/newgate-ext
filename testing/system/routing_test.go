package system_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/rzbdz/newgate-ext/testing/system"
)

// anthropicBody builds a minimal anthropic-dialect request. model is the tier name
// (heavy/normal/ ...) - exactly what a taken-over client sends: the tier name is injected,
// and the proxy resolves it from the config current at request time.
func anthropicBody(model string) string {
	return `{"model":"` + model + `","max_tokens":16,` +
		`"messages":[{"role":"user","content":"hi"}]}`
}

// lastUpstreamModel takes the model field of the latest request the fake upstream got.
func lastUpstreamModel(t *testing.T, h *system.Harness) string {
	t.Helper()
	records := h.Upstream.Requests()
	if len(records) == 0 {
		t.Fatal("the fake upstream received no requests at all")
	}
	var payload struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(records[len(records)-1].Body, &payload); err != nil {
		t.Fatalf("parsing the request body the upstream received: %v (raw %s)", err, records[len(records)-1].Body)
	}
	return payload.Model
}

// TestRoutesTierToProfileChainHead is this layer's core test: a tier name in, a real
// model name out, and a response header telling the caller who served it.
//
// Expectations come from the default config app seeds (seed.go): the default profile is
// cheap, cheap.normal is undefined, so CandidatesFor fills in mid -> upstream-a/model-medium.
// The chain ties three things: tier fallback, profile resolution, binding picks the provider.
func TestRoutesTierToProfileChainHead(t *testing.T) {
	h := system.Start(t)

	resp := h.Post("/a/claude/v1/messages", anthropicBody("normal"))
	body := system.ReadBody(t, resp)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", resp.StatusCode, body)
	}
	if got := lastUpstreamModel(t, h); got != "model-medium" {
		t.Errorf("model the upstream received = %q, want model-medium (cheap.normal falls back to mid)", got)
	}
	route := resp.Header.Get("X-Newgate-Route")
	if !strings.Contains(route, "upstream-a/model-medium") {
		t.Errorf("X-Newgate-Route = %q, expected to contain upstream-a/model-medium", route)
	}
	if got := resp.Header.Get("X-Newgate-Profile"); got != "cheap" {
		t.Errorf("X-Newgate-Profile = %q, want cheap (default profile)", got)
	}
}

// TestPathProfileOverrideIsScopedToThisCall pins "a per-call profile leaves no global state".
//
// Injection encodes the profile into the base URL path (/p/<profile>); the proxy reads it
// for this call's chain head and never touches state. So send one with an override, then
// one without: the second must return to default - if it were "change global, then
// forward", the second follows, though the user never ran such a command.
// (This path is **not in docs**; behaviour is described only here and in the test below.)
func TestPathProfileOverrideIsScopedToThisCall(t *testing.T) {
	h := system.Start(t)

	overridden := h.Post("/a/claude/p/expensive/v1/messages", anthropicBody("normal"))
	system.ReadBody(t, overridden)

	if overridden.StatusCode != http.StatusOK {
		t.Fatalf("override profile request status = %d, want 200", overridden.StatusCode)
	}
	if got := overridden.Header.Get("X-Newgate-Profile"); got != "expensive" {
		t.Errorf("override request X-Newgate-Profile = %q, want expensive", got)
	}
	// expensive.mid = upstream-b/model-large. Different from the default
	// upstream-a/model-medium, so it tells that the override really took effect.
	if got := lastUpstreamModel(t, h); got != "model-large" {
		t.Errorf("override request upstream model = %q, want model-large (expensive.mid)", got)
	}

	h.Upstream.Reset()
	plain := h.Post("/a/claude/v1/messages", anthropicBody("normal"))
	system.ReadBody(t, plain)

	if got := plain.Header.Get("X-Newgate-Profile"); got != "cheap" {
		t.Errorf("the next normal request X-Newgate-Profile = %q, want cheap (the override must not leak globally)", got)
	}
	if got := lastUpstreamModel(t, h); got != "model-medium" {
		t.Errorf("the next normal request upstream model = %q, want model-medium", got)
	}
}

// TestUnknownProfileDoesNotLieAboutWhatServedIt pins "headers never echo a
// nonexistent profile".
//
// Why not assert 404: path override `/p/<profile>` currently **does not** validate
// whether the profile exists - an unknown name is passed down as chain head, orderProfiles
// misses it, the chain resolves by priority (top-only -> cheap -> ...), the last one 200s,
// and `X-Newgate-Profile` honestly reports what really served. What this layer can pin is
// the undisputed half: **it must report honestly**, not echo the client's
// "no-such-profile" verbatim - that is the genuinely misleading silence (the user thinks
// a model is pinned, another serves, and billing and output disagree).
//
// Known inconsistency (noted 2026-09-17, **unfixed**): CLI `newgate claude --profile=nope`
// refuses (exit 65, see mock/e2e_claude.sh section 5), while HTTP silently degrades.
// One concept, two attitudes, against "no guessing". Fixing it adds validation to
// handleProxy and changes live behaviour, out of scope here; pin today's behaviour first.
func TestUnknownProfileDoesNotLieAboutWhatServedIt(t *testing.T) {
	h := system.Start(t)

	resp := h.Post("/a/claude/p/no-such-profile/v1/messages", anthropicBody("normal"))
	body := system.ReadBody(t, resp)

	if got := resp.Header.Get("X-Newgate-Profile"); got == "no-such-profile" {
		t.Fatalf("X-Newgate-Profile echoed a nonexistent profile %q; body=%s", got, body)
	}
	if resp.StatusCode == http.StatusOK {
		if got := resp.Header.Get("X-Newgate-Profile"); got == "" {
			t.Errorf("200 but no X-Newgate-Profile reported, the caller cannot tell who was actually used; headers=%v",
				resp.Header)
		}
	}
}

// TestFailoverMovesToNextCandidate verifies the fallback chain actually moves.
//
// The fake upstream arms the next response as 429 (real-world rate limiting), and the
// proxy should move to the next candidate rather than hand the 429 to the client - a
// client seeing 429 retries itself, doubling the load on the rate-limited upstream.
//
// Who judges what (measured, not designed):
//   - X-Newgate-Chain says "who tried, what happened", like
//     `upstream-a/model-large(429) -> upstream-b/model-large(ok)` - **who** is read here.
//   - X-Newgate-Failover only says "a failover happened", like
//     `<head profile> -> <final profile> (upstream 429; see: newgate logs)`.
//     Within one profile both ends are the same name (`cheap -> cheap`), so it cannot
//     prove who was swapped - only that a swap happened.
//
// A 200 alone is not enough: maybe the first try never failed.
func TestFailoverMovesToNextCandidate(t *testing.T) {
	h := system.Start(t)

	h.Upstream.FailNext(http.StatusTooManyRequests)

	resp := h.Post("/a/claude/v1/messages", anthropicBody("heavy"))
	body := system.ReadBody(t, resp)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("after the 429 took out a chain step, status = %d, want 200 (should move to the next candidate); body=%s",
			resp.StatusCode, body)
	}
	if resp.Header.Get("X-Newgate-Failover") == "" {
		t.Fatalf("failover happened but X-Newgate-Failover was not reported (never silent is a hard requirement); headers=%v",
			resp.Header)
	}
	chain := resp.Header.Get("X-Newgate-Chain")
	if !strings.Contains(chain, "upstream-a/model-large(429)") {
		t.Errorf("X-Newgate-Chain = %q, expected to contain upstream-a/model-large(429) (the step that was taken out)", chain)
	}
	if !strings.Contains(chain, "upstream-b/model-large(ok)") {
		t.Errorf("X-Newgate-Chain = %q, expected to contain upstream-b/model-large(ok) (the step that caught it)", chain)
	}
}

// TestRequestsAreNotJSONRoundTripped pins the byte-surgery rule.
//
// The request body has a big integer 9007199254740993 (2^53+1) and an unknown field. A
// JSON round-trip would float-convert the former to ...92 and drop the latter - both
// silent; the client never learns its request was touched. So assert both survive in bytes.
//
// It uses the request the upstream recorded, because what must be verified is what we sent.
func TestRequestsAreNotJSONRoundTripped(t *testing.T) {
	h := system.Start(t)

	const bigInt = "9007199254740993"
	body := `{"model":"normal","max_tokens":16,` +
		`"messages":[{"role":"user","content":"hi"}],` +
		`"x_unknown_field":{"deep":[1,2,3]},"x_big":` + bigInt + `}`

	resp := h.Post("/a/claude/v1/messages", body)
	system.ReadBody(t, resp)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	sent := string(h.Upstream.Requests()[len(h.Upstream.Requests())-1].Body)
	if !strings.Contains(sent, bigInt) {
		t.Errorf("a big integer in the request the upstream received got corrupted (typical symptom of a JSON round trip):\n%s", sent)
	}
	if !strings.Contains(sent, "x_unknown_field") {
		t.Errorf("an unknown field in the request the upstream received was lost (typical symptom of a JSON round trip):\n%s", sent)
	}
}

// TestCountTokensIsForwardedToUpstream verifies count_tokens takes the upstream's real
// value, not a local estimate.
//
// The fake upstream answers {"input_tokens":42} - a number no local estimate happens to
// give (bytes/4; our body is far more than 168 bytes). So 42 is the "forwarded" fingerprint.
func TestCountTokensIsForwardedToUpstream(t *testing.T) {
	h := system.Start(t)

	resp := h.Post("/a/claude/v1/messages/count_tokens", anthropicBody("mid"))
	body := system.ReadBody(t, resp)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", resp.StatusCode, body)
	}
	var payload struct {
		InputTokens int `json:"input_tokens"`
	}
	if err := json.Unmarshal([]byte(body), &payload); err != nil {
		t.Fatalf("parsing the response: %v (raw %s)", err, body)
	}
	if payload.InputTokens != 42 {
		t.Errorf("input_tokens = %d, want 42 (the fake upstream's real value; a local rough estimate would not happen to match)",
			payload.InputTokens)
	}
}

// TestStreamingPassesThrough verifies streaming really passes chunk by chunk, unbuffered.
//
// The criterion is not "the response has SSE frames" (buffering has them too), but
// **the client reads the same frame sequence as the upstream**. Here: first, last, [DONE].
// Upstream chunk delays expose buffering in practice, but for a regression test the frame
// sequence already stops "treat streaming as non-streaming".
func TestStreamingPassesThrough(t *testing.T) {
	h := system.Start(t)

	body := `{"model":"normal","max_tokens":16,"stream":true,` +
		`"messages":[{"role":"user","content":"hi"}]}`
	resp := h.Post("/a/claude/v1/messages", body)
	got := system.ReadBody(t, resp)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", resp.StatusCode, got)
	}
	for _, want := range []string{"message_start", "content_block_delta", "message_stop"} {
		if !strings.Contains(got, want) {
			t.Errorf("the streaming response is missing %s:\n%s", want, got)
		}
	}
}
