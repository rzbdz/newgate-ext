package quirk

import (
	"testing"
)

// Unrecognized is never guessed at: better to show the user the raw error than to blindly
// add fields to the request.
func TestLearnNeverGuesses(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
	}{
		{"unrelated 400", 400, `{"error":{"message":"invalid request: max_tokens too large"}}`},
		{"5xx means the upstream itself is down, unrelated to the request shape", 500, `{"error":{"message":"always thinks"}}`},
		{"429", 429, `{"error":{"message":"rate limit"}}`},
		{"empty body", 400, ``},
		{"just one ordinary sentence with no signature at all", 400, `{"error":{"message":"bad request"}}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			table := NewTable()
			if _, err := table.RegisterSignature(Signature{
				Flag: NoThinkingDisable, Any: []string{"does not support disabling thinking"}, Label: "this model always thinks",
			}); err != nil {
				t.Fatal(err)
			}
			if got := table.Learn("prov", "model-y", tt.status, []byte(tt.body)); got != nil {
				t.Fatalf("it learned something it should not have: %v", got)
			}
			if table.Has("prov", "model-y", NoThinkingDisable) {
				t.Fatal("it flagged something it should not have")
			}
		})
	}
}

// Accounting is per (provider, model): what is recorded for A must not leak to B, or cross
// over into B.
//
// Mark rather than Learn: **the signature table is empty now** (criteria are registered by
// the module owning the patch, see modules/thinking/signatures.go), so what is tested here
// is this table's accounting granularity, unrelated to signatures.
func TestFlagsAreScopedToProviderAndModel(t *testing.T) {
	table := NewTable()
	table.Mark("kimi", "kimi-k2.7-code", NoThinkingDisable)
	if !table.Has("kimi", "kimi-k2.7-code", NoThinkingDisable) {
		t.Fatal("it did not record it itself")
	}
	if table.Has("kimi", "kimi-k2.7", NoThinkingDisable) || table.Has("glm", "kimi-k2.7-code", NoThinkingDisable) {
		t.Fatal("it leaked to another provider/model")
	}
}

// Snapshot must show the flags already learned (the CLI/diagnostics read it).
//
// It records via Mark directly instead of going through Learn: **the signature table is
// empty now** (criteria are registered by the module owning the patch, see
// modules/thinking/signatures.go), so Learn would learn nothing on a fresh table. This
// tests "what was recorded shows up in the snapshot", unrelated to signature content.
func TestSnapshotShowsLearnedFlags(t *testing.T) {
	table := NewTable()
	table.Mark("kimi", "kimi-k2.7-code", NoThinkingDisable)
	found := false
	for _, e := range table.Snapshot() {
		if e.Provider == "kimi" && e.Model == "kimi-k2.7-code" &&
			e.Flags&NoThinkingDisable != 0 {
			found = true
		}
	}
	if !found {
		t.Fatalf("the flag just recorded is not visible in the snapshot: %v", table.Snapshot())
	}
}
