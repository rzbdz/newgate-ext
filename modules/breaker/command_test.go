package breaker

import (
	"testing"
	"time"
)

// A graded-unavailable probe that delisted nothing still belongs on the `newgate breaker`
// screen. The 2026-11-19 change made a failed probe stop delisting (one sweep had wiped out
// four smt-claude bindings), and the note it emits says "recorded as evidence only" -- if the
// evidence is not visible here, that note is a lie and the user is back to reading state.json.
func TestProbeEvidenceWithoutATripIsStillShown(t *testing.T) {
	only := Status{Provider: "smt-claude", Model: "claude-opus-5-5",
		Grade: ProbeUnavailable, Checked: time.Date(2026, 11, 19, 10, 0, 0, 0, time.UTC)}
	if !isCountedOnly(only) {
		t.Error("a probe that graded a binding unavailable was not shown anywhere - the 2026-11-19 evidence blind spot")
	}
	if !isCountedOnly(Status{Fails: 1}) {
		t.Error("failures below the threshold must stay visible")
	}
	if !isCountedOnly(Status{ShapeSkips: 1}) {
		t.Error("shape errors must stay visible (they never delist)")
	}
	if isCountedOnly(Status{}) {
		t.Error("a binding with nothing to report must not be listed")
	}
	if isCountedOnly(Status{Grade: ProbeFluent}) {
		t.Error("a healthy probe is not evidence of trouble")
	}
}
