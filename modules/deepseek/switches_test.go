package deepseek

import (
	"strings"
	"testing"

	"github.com/rzbdz/newgate-ext/modules/config/domain"
	"github.com/rzbdz/newgate-ext/modules/gateway/special"
	"github.com/rzbdz/newgate-ext/modules/pluginmanager"
)

// reqWithOff builds a Claude Code x DeepSeek context with the given switch points off.
func reqWithOff(t *testing.T, off ...string) *special.Request {
	t.Helper()
	cfg := pluginmanager.Config{Off: map[string]pluginmanager.Entry{}}
	for _, path := range off {
		cfg.Off[path] = pluginmanager.Entry{}
	}
	raw, err := cfg.Marshal()
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	r := claudeReq("deepseek-flash")
	r.State = &domain.State{ModuleConfig: map[string][]byte{pluginmanager.StateKey: raw}}
	return r
}

// threeHandsBody is a request that triggers all three passes at once:
//
//	pass 2: assistant has no reasoning_content, but the cache has text -> fill
//	pass 3: assistant content[] has no thinking block, thinking on, Anthropic -> fill
//	pass 4: the last user message is tool_result-only -> append a continue instruction
//
// All three ride the **same body** to verify "turning one off does not affect the
// others" - separate fixtures have their own shape issues and cannot show crosstalk.
const threeHandsBody = `{"thinking":{"type":"adaptive"},"messages":[` +
	`{"role":"user","content":[{"type":"text","text":"let's start"}]},` +
	`{"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"Bash","input":{}}]},` +
	`{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"ok"}]}]}`

const seededReasoning = "reasoning text the upstream really returned last turn"

func applyThreeHands(t *testing.T, off ...string) string {
	t.Helper()
	seedCache("t1", seededReasoning)
	out, _, err := reasoning{}.Apply([]byte(threeHandsBody), reqWithOff(t, off...))
	if err != nil {
		t.Fatalf("Apply errored: %v", err)
	}
	return string(out)
}

func TestThreeHandsFireWhenAllSwitchesOn(t *testing.T) {
	s := applyThreeHands(t)
	if !strings.Contains(s, `"reasoning_content":"`+seededReasoning+`"`) {
		t.Fatalf("pass 2 did not fill in reasoning_content:\n%s", s)
	}
	if !strings.Contains(s, `"thinking":"`+seededReasoning+`"`) {
		t.Fatalf("pass 3 did not fill in the thinking block:\n%s", s)
	}
	if !strings.Contains(s, `"text":`+mustJSON(toolLoopRebasePrompt)) {
		t.Fatalf("pass 4 did not append the continue instruction:\n%s", s)
	}
}

// TestSwitchesAreIndividuallyDisableable is why the feature exists: the three passes
// must be disableable **separately**. Before this they were one name "deepseek" - all or
// nothing - while debugging mostly needs "turn off pass 4 and see if the 400 remains".
func TestSwitchesAreIndividuallyDisableable(t *testing.T) {
	cases := []struct {
		off                              string
		reasoning, thinkingBlock, tailOK bool
	}{
		{SwitchBackfillReasoning, false, true, true},
		{SwitchBackfillThinkingBlock, true, false, true},
		{SwitchTailShape, true, true, false},
		// Pass 5 governs only the Responses dialect input[], so turning it off on the
		// three-pass fixture (messages dialect) must have no effect - this fills the new slot.
		{SwitchTailShapeResponses, true, true, true},
	}
	for _, c := range cases {
		t.Run(c.off, func(t *testing.T) {
			s := applyThreeHands(t, c.off)
			gotReasoning := strings.Contains(s, `"reasoning_content":"`+seededReasoning+`"`)
			gotThinking := strings.Contains(s, `"thinking":"`+seededReasoning+`"`)
			gotTail := strings.Contains(s, `"text":`+mustJSON(toolLoopRebasePrompt))

			if gotReasoning != c.reasoning {
				t.Errorf("pass 2 (reasoning_content) = %v, want %v\n%s", gotReasoning, c.reasoning, s)
			}
			if gotThinking != c.thinkingBlock {
				t.Errorf("pass 3 (thinking block) = %v, want %v\n%s", gotThinking, c.thinkingBlock, s)
			}
			if gotTail != c.tailOK {
				t.Errorf("pass 4 (tail instruction) = %v, want %v\n%s", gotTail, c.tailOK, s)
			}
			// The pass turned off must not drag others down: exactly one of the three
			// expectations is false, so this assertion says "crosstalk = failure".
		})
	}
}

// TestNilStateMeansEveryHandRuns: a missing snapshot counts as "not turned off".
//
// The direction must not reverse: switch points default to on, and a nil snapshot
// turning patches off is fail-closed - whose consequence is exactly the 400 they fix.
// Old callers (unit tests, code building Request directly) often have a nil State.
func TestNilStateMeansEveryHandRuns(t *testing.T) {
	seedCache("t1", seededReasoning)
	r := claudeReq("deepseek-flash") // note: State deliberately left unset
	out, _, err := reasoning{}.Apply([]byte(threeHandsBody), r)
	if err != nil {
		t.Fatalf("Apply errored: %v", err)
	}
	s := string(out)
	if !strings.Contains(s, `"reasoning_content":"`+seededReasoning+`"`) ||
		!strings.Contains(s, `"thinking":"`+seededReasoning+`"`) ||
		!strings.Contains(s, `"text":`+mustJSON(toolLoopRebasePrompt)) {
		t.Fatalf("with State nil all three passes should still run (fail-open):\n%s", s)
	}
}

// TestSwitchesOfOtherModulesDoNotLeak: same-named switch points under other prefixes must not bite.
func TestSwitchesOfOtherModulesDoNotLeak(t *testing.T) {
	s := applyThreeHands(t, "claudecode-deepseek.inject-thinking")
	if !strings.Contains(s, `"reasoning_content":"`+seededReasoning+`"`) ||
		!strings.Contains(s, `"text":`+mustJSON(toolLoopRebasePrompt)) {
		t.Fatalf("turning off another module's switch points must not affect this module's three passes:\n%s", s)
	}
}

// TestResponsesTailShapeSwitchDisablesHand5: pass 5 can be turned off **alone**.
//
// It and pass 4 target the same false report in two dialects, but touch two arrays
// (messages content[] vs input[]), so each has a switch - debugging often wants "turn
// off only the Responses pass and see if the 400 remains".
func TestResponsesTailShapeSwitchDisablesHand5(t *testing.T) {
	body := []byte(`{"model":"deepseek-flash","input":[` +
		`{"type":"function_call","call_id":"call_faedac561213487ebeea7731","name":"bash","arguments":"{}"},` +
		`{"type":"function_call_output","call_id":"call_faedac561213487ebeea7731","output":"ok"}]}`)

	on, notes, err := reasoning{}.Apply(body, reqWithOff(t))
	if err != nil {
		t.Fatalf("Apply errored: %v", err)
	}
	if !strings.Contains(string(on), mustJSON(toolLoopRebasePrompt)) {
		t.Fatalf("pass 5 (switch on) did not append the continue instruction:\n%s", on)
	}
	if !containsNote(notes, "Responses input[] ends on a function_call_output") {
		t.Fatalf("changed something but did not report notes: %v", notes)
	}

	off, notesOff, err := reasoning{}.Apply(body, reqWithOff(t, SwitchTailShapeResponses))
	if err != nil {
		t.Fatalf("Apply errored: %v", err)
	}
	if string(off) != string(body) {
		t.Fatalf("the switch is off yet the request was changed:\n%s", off)
	}
	if containsNote(notesOff, "Responses input[] ends on a function_call_output") {
		t.Fatalf("the switch is off yet it reported a 5th-pass note: %v", notesOff)
	}
}
