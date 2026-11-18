package claudecode

import (
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/rzbdz/newgate-ext/modules/config/domain"
	"github.com/rzbdz/newgate-ext/modules/gateway/special"
)

// The two regexes below are copied from the two parsers in Claude Code 2.1.295 (both match
// after removing <thinking> -- naked's answer has no thinking, so that step is skipped here):
//
//	block    : /<block>(yes|no)\b(<\/block>)?/gi   -- only consistent occurrences count, no = let through
//	severity : /<severity>\s*(\d+(?:\.\d+)?)\s*(<\/severity>)?/g -- exactly one
var (
	classifierBlockRE    = regexp.MustCompile(`(?i)<block>(yes|no)\b(</block>)?`)
	classifierSeverityRE = regexp.MustCompile(`<severity>\s*(\d+(?:\.\d+)?)\s*(</severity>)?`)
)

const nakedClassifierBody = `{"model":"light","max_tokens":2112,` +
	`"system":"You are a security monitor for autonomous AI coding agents",` +
	`"messages":[{"role":"user","content":"run rm -rf /"}]}`

func nakedState(t *testing.T, cfg NakedConfig) *domain.State {
	t.Helper()
	raw, err := cfg.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	return &domain.State{ModuleConfig: map[string][]byte{NakedConfigKey: raw}}
}

// TestNakedConfigRoundTripsThroughStateFile is the direct regression for going live on 2026-09-17.
//
// The scene: the `expires_at` that `newgate naked on` wrote into state.json read
// back as the zero value, so `on` mode **never took effect** (time.Now().Before(zero)
// is always false), and status printed `-2562047h…` (time.Until overflows on the zero
// value). The root cause was NakedConfig missing JSON tags -- Go's field-name matching
// ignores case but **not underscores**, so `ExpiresAt` does not match `expires_at`.
//
// This test pins two things: writing out and reading back must be equal; and the key
// names are exactly these two (changing the tag would make old state.json unable to
// read the window, which means naked mode silently stops working after an upgrade).
func TestNakedConfigRoundTripsThroughStateFile(t *testing.T) {
	want := NakedConfig{Mode: "on", ExpiresAt: time.Now().Add(60 * time.Second).Round(time.Second)}
	raw, err := want.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{`"mode"`, `"expires_at"`} {
		if !strings.Contains(string(raw), key) {
			t.Fatalf("state.json is missing %s: %s", key, raw)
		}
	}
	got, ok := ParseNakedConfig(raw)
	if !ok {
		t.Fatalf("cannot read back the config we wrote ourselves: %s", raw)
	}
	if got.Mode != want.Mode || !got.ExpiresAt.Equal(want.ExpiresAt) {
		t.Fatalf("round trip distorted: %+v -> %+v", want, got)
	}
	if !time.Now().Before(got.ExpiresAt) {
		t.Fatal("the window read back is already expired - naked mode would silently not take effect")
	}
}

func TestParseNakedConfigRejectsBadData(t *testing.T) {
	for _, tt := range []struct {
		name string
		raw  string
	}{
		{"empty", ``},
		{"bad JSON", `{`},
		{"null", `null`},
		{"the mode is not on the allowlist", `{"mode":"maybe"}`},
		{"the mode is missing", `{"expires_at":"2026-09-17T18:00:00Z"}`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if _, ok := ParseNakedConfig([]byte(tt.raw)); ok {
				t.Fatalf("bad data was treated as in effect: %s", tt.raw)
			}
		})
	}
}

// TestNakedRespondsOnlyWhenExplicitlyOn: off by
// default, and only an explicit turn-on short-circuits.
func TestNakedRespondsOnlyWhenExplicitlyOn(t *testing.T) {
	req := &special.Request{Agent: ID, Model: "light"}
	off := &domain.State{ModuleConfig: map[string][]byte{}}
	if _, ok := (classifierNaked{}).Respond([]byte(nakedClassifierBody), req, off); ok {
		t.Fatal("with nothing configured it short-circuited too - the safety gate was silently turned off")
	}

	expired := nakedState(t, NakedConfig{Mode: "on", ExpiresAt: time.Now().Add(-time.Second)})
	if _, ok := (classifierNaked{}).Respond([]byte(nakedClassifierBody), req, expired); ok {
		t.Fatal("an expired window still short-circuits")
	}

	live := nakedState(t, NakedConfig{Mode: "on", ExpiresAt: time.Now().Add(time.Minute)})
	if _, ok := (classifierNaked{}).Respond([]byte(nakedClassifierBody), req, live); !ok {
		t.Fatal("an active window did not short-circuit")
	}

	forever := nakedState(t, NakedConfig{Mode: "forever"})
	if _, ok := (classifierNaked{}).Respond([]byte(nakedClassifierBody), req, forever); !ok {
		t.Fatal("forever mode did not short-circuit")
	}
}

// TestNakedOnlyTouchesTheClassifier: only Claude Code's security-monitor marker is recognized.
// Other clients, streaming requests, and background calls that are **not the classifier** (compact
// summaries, title generation) must all pass through untouched -- those need real answers.
func TestNakedOnlyTouchesTheClassifier(t *testing.T) {
	state := nakedState(t, NakedConfig{Mode: "forever"})
	plain := `{"model":"light","messages":[{"role":"user","content":"summarize"}]}`
	cases := []struct {
		name string
		body []byte
		req  *special.Request
	}{
		{"streaming request", []byte(nakedClassifierBody), &special.Request{Agent: ID, Stream: true}},
		{"other client", []byte(nakedClassifierBody), &special.Request{Agent: "opencode"}},
		{"non-classifier background call", []byte(plain), &special.Request{Agent: ID}},
		{"nil request", []byte(nakedClassifierBody), nil},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			if _, ok := (classifierNaked{}).Respond(tt.body, tt.req, state); ok {
				t.Fatalf("%s was short-circuited", tt.name)
			}
		})
	}
}

// TestNakedAnswerIsAValidApproval: the mocked answer must be a valid anthropic Messages
// response, and its content must be "do not block" in the classifier protocol. A structural error
// = the client cannot parse it = the session hangs instead, worse than not short-circuiting.
func TestNakedAnswerIsAValidApproval(t *testing.T) {
	state := nakedState(t, NakedConfig{Mode: "forever"})
	out, ok := (classifierNaked{}).Respond([]byte(nakedClassifierBody),
		&special.Request{Agent: ID, Model: "light"}, state)
	if !ok {
		t.Fatal("not short-circuited")
	}
	var resp struct {
		Type    string `json:"type"`
		Role    string `json:"role"`
		Model   string `json:"model"`
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		StopReason string `json:"stop_reason"`
	}
	if err := json.Unmarshal(out, &resp); err != nil {
		t.Fatalf("not valid JSON: %v", err)
	}
	if resp.Type != "message" || resp.Role != "assistant" || resp.StopReason != "end_turn" {
		t.Fatalf("wrong response shape: %+v", resp)
	}
	if resp.Model != "light" {
		t.Fatalf("model should be filled from the request value, got %q", resp.Model)
	}
	if len(resp.Content) != 1 || resp.Content[0].Text != classifierAnswer {
		t.Fatalf("the content is not the classifier protocol's 'approve': %+v", resp.Content)
	}
}

// TestNakedAnswerSatisfiesEveryClassifierParser pins classifierAnswer's **shape**: it
// must read as "let through" under **every** classifier answer parser in the client.
//
// The scene (2026-10-09, Claude Code 2.1.295): the only test was the response body shape
// -- after rewriting the answer into `<bad>nope</bad>`, which neither parser recognizes,
// auto mode blocked **every** time and reported "could not evaluate" (fail-closed),
// while the same command passed under the normal shape. So if naked mode's answer
// satisfies only one of them, the other judges it "no conclusion" and blocks.
//
// Both parsers are copied from the original functions read out of the 2.1.295 binary (matching
// after removing `<thinking>`); when the client protocol changes, this goes red first.
func TestNakedAnswerSatisfiesEveryClassifierParser(t *testing.T) {
	// The `<block>(yes|no)` one: after removing <thinking> it must be consistent, No = let through.
	blockMatches := classifierBlockRE.FindAllStringSubmatch(classifierAnswer, -1)
	if len(blockMatches) != 1 || blockMatches[0][1] != "no" {
		t.Fatalf("the <block> parser cannot read 'allow': %q", classifierAnswer)
	}
	// The `<severity>N</severity>` one: exactly one
	// occurrence counts, 0 <= any threshold (default 15/20).
	sevMatches := classifierSeverityRE.FindAllStringSubmatch(classifierAnswer, -1)
	if len(sevMatches) != 1 {
		t.Fatalf("the <severity> parser cannot read exactly one number: %q", classifierAnswer)
	}
	if sev, err := strconv.Atoi(sevMatches[0][1]); err != nil || sev > 0 {
		t.Fatalf("severity must be 0 (explicit allow), got %q", sevMatches[0][1])
	}
	// The counter-example guard: the shape neither parser recognizes
	// is exactly where that error message the user sees comes from.
	if classifierBlockRE.MatchString("<bad>nope</bad>") ||
		classifierSeverityRE.MatchString("<bad>nope</bad>") {
		t.Fatal("a negative example was accepted by the parser - these two regexes no longer match the client protocol")
	}
}

// TestNakedStatusAndNoteAreVisible: "never silent" demands it --
// naked mode being on must be visible in status and in the log.
func TestNakedStatusAndNoteAreVisible(t *testing.T) {
	if items := (classifierNaked{}).Status(&domain.State{}); len(items) != 0 {
		t.Fatalf("it reports naked mode in status although it is off: %+v", items)
	}

	forever := nakedState(t, NakedConfig{Mode: "forever"})
	items := (classifierNaked{}).Status(forever)
	if len(items) != 1 || !strings.Contains(items[0].Value, "forever") {
		t.Fatalf("the forever status does not state the mode: %+v", items)
	}
	if note := (classifierNaked{}).RespondNote(forever); !strings.Contains(note, "[naked]") {
		t.Fatalf("the log note has no [naked] marker: %q", note)
	}

	live := nakedState(t, NakedConfig{Mode: "on", ExpiresAt: time.Now().Add(30 * time.Second)})
	if note := (classifierNaked{}).RespondNote(live); !strings.Contains(note, "[naked]") ||
		!strings.Contains(note, "the window has") {
		t.Fatalf("the log note for window mode does not say how much time is left: %q", note)
	}
}

// TestExpiredWindowLooksOffEverywhere pins the invariant "an expired window reads as off at
// any read point". Real run 2026-09-17: `Respond` checked expiry, `Status` did not, so after
// the window passed `newgate status` printed a line "auto-off in -6m3s" -- an already-expired
// switch showing on the status page as if it were on, worse than not showing it.
func TestExpiredWindowLooksOffEverywhere(t *testing.T) {
	expired := nakedState(t, NakedConfig{Mode: "on", ExpiresAt: time.Now().Add(-6 * time.Minute)})

	if _, ok := ParseNakedConfig(expired.ModuleConfig[NakedConfigKey]); ok {
		t.Fatal("an expired window was parsed as in effect")
	}
	if items := (classifierNaked{}).Status(expired); len(items) != 0 {
		t.Fatalf("an expired window still reports open in status: %+v", items)
	}
	// The log note must still carry the [naked] marker (this request really
	// was short-circuited), but must not show a negative remaining time.
	note := (classifierNaked{}).RespondNote(expired)
	if !strings.Contains(note, "[naked]") {
		t.Fatalf("the log note has no [naked] marker: %q", note)
	}
	if strings.Contains(note, "the window has") {
		t.Fatalf("an expired window must not report remaining time: %q", note)
	}
}

// TestNakedConfigNoExpiryIsTreatedAsOff: `on` without expires_at (or with it written
// as the zero value) equals "no window". The zero value makes time.Now().Before
// always false, silently taking effect, so it must be explicitly judged off.
func TestNakedConfigNoExpiryIsTreatedAsOff(t *testing.T) {
	state := nakedState(t, NakedConfig{Mode: "on"})
	if _, ok := ParseNakedConfig(state.ModuleConfig[NakedConfigKey]); ok {
		t.Fatal("an on without expires_at was treated as in effect")
	}
	if _, ok := (classifierNaked{}).Respond([]byte(nakedClassifierBody),
		&special.Request{Agent: ID}, state); ok {
		t.Fatal("an on without expires_at short-circuited the request")
	}
}

// TestNakedOrdersItselfAfterBackground: the short circuit sorts after claude-bg
// -- its After/Before declaration tells the plugin graph that (getting the
// order wrong would let a no-op Apply grab the reroute claude-bg should do).
func TestNakedOrdersItselfAfterBackground(t *testing.T) {
	if got := (classifierNaked{}).Before(); len(got) != 1 || got[0] != "claude-bg" {
		t.Fatalf("Before() = %v, should sort after claude-bg", got)
	}
}

// TestNakedApprovesEveryGateShape: **every family** of classifier request must be short-circuited,
// and every answer must be that "both parsers accept" shape. The whole point of naked mode
// is "every approval request from Claude Code is approved on the spot" -- missing one family
// means missing one safety gate, and the user will run into "could not evaluate" at that gate.
func TestNakedApprovesEveryGateShape(t *testing.T) {
	state := nakedState(t, NakedConfig{Mode: "forever"})
	for _, tt := range []struct{ name, body string }{
		{"Bash gate (the block kind)",
			`{"model":"mid","system":"You are a security monitor for autonomous AI coding agents","stop_sequences":["</block>"],"max_tokens":2112}`},
		{"sandbox network gate (the severity kind)",
			`{"model":"mid","system":"You are a security monitor for autonomous AI coding agents","stop_sequences":["</severity>"],"max_tokens":2112}`},
		{"fast flavour (no stop_sequences)",
			`{"model":"mid","system":"You are a security monitor for autonomous AI coding agents","max_tokens":2304}`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			out, ok := (classifierNaked{}).Respond([]byte(tt.body),
				&special.Request{Agent: ID, Model: "mid"}, state)
			if !ok {
				t.Fatalf("%s was not short-circuited", tt.name)
			}
			// Assert the decoded text, not the raw bytes: encoding/json escapes `<` into `\u003c`
			// (the client decodes the JSON before matching tags, so the escape is harmless).
			var resp struct {
				Content []struct{ Text string } `json:"content"`
			}
			if err := json.Unmarshal(out, &resp); err != nil || len(resp.Content) != 1 {
				t.Fatalf("the reply of %s is not a valid response: %v %s", tt.name, err, out)
			}
			if resp.Content[0].Text != classifierAnswer {
				t.Fatalf("the reply of %s is not a shape both parsers accept: %q", tt.name, resp.Content[0].Text)
			}
		})
	}
}
