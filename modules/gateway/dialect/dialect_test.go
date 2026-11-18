package dialect

import "testing"

func TestMarkAndSupports(t *testing.T) {
	Reset()
	defer Reset()

	if _, known := Supports("p", "m", CapCountTokens); known {
		t.Fatal("known=true without ever probing")
	}

	if !Mark("p", "m", CapAnthropic) {
		t.Fatal("the first Mark should report newly learned")
	}
	if Mark("p", "m", CapAnthropic) {
		t.Fatal("a repeated Mark must not report newly learned")
	}
	if ok, known := Supports("p", "m", CapAnthropic); !ok || !known {
		t.Fatalf("the support bit cannot be read: ok=%v known=%v", ok, known)
	}
	// isolation: other (provider, model) pairs are unaffected
	if _, known := Supports("p", "other", CapAnthropic); known {
		t.Fatal("the key got crossed")
	}
}

// TestMarkUnsupported covers both sides: after an explicit "unsupported",
// Supports always returns (false, true) - recording only the supported bit would
// hit another 404 on every request for every upstream without that endpoint.
func TestMarkUnsupported(t *testing.T) {
	Reset()
	defer Reset()

	if !MarkUnsupported("p", "m", CapCountTokens) {
		t.Fatal("the first MarkUnsupported should report newly learned")
	}
	ok, known := Supports("p", "m", CapCountTokens)
	if ok || !known {
		t.Fatalf("after an explicit unsupported it should be (false,true), got (%v,%v)", ok, known)
	}
	if MarkUnsupported("p", "m", CapCountTokens) {
		t.Fatal("a repeated MarkUnsupported must not report newly learned")
	}

	// upstream upgraded: changing from "unsupported" to "supported" is new information too
	if !Mark("p", "m", CapCountTokens) {
		t.Fatal("changing the answer should report newly learned")
	}
	if ok, _ := Supports("p", "m", CapCountTokens); !ok {
		t.Fatal("cannot read it after changing the answer")
	}
}

func TestCapString(t *testing.T) {
	if got := (CapOpenAI | CapAnthropic).String(); got != "openai+anthropic" {
		t.Errorf("Cap.String() = %q", got)
	}
	if got := CapCountTokens.String(); got != "count_tokens" {
		t.Errorf("Cap.String() = %q", got)
	}
	if got := Cap(0).String(); got != "" {
		t.Errorf("the zero value should be an empty string, got %q", got)
	}
}

func TestSnapshotSorted(t *testing.T) {
	Reset()
	defer Reset()
	Mark("b", "z", CapOpenAI)
	Mark("b", "a", CapOpenAI)
	Mark("a", "x", CapOpenAI)

	entries := Snapshot()
	if len(entries) != 3 {
		t.Fatalf("should be 3, got %d", len(entries))
	}
	want := []string{"a/x", "b/a", "b/z"}
	for i, e := range entries {
		if got := e.Provider + "/" + e.Model; got != want[i] {
			t.Errorf("entry %d = %s, should be %s (must be stably sorted)", i, got, want[i])
		}
	}
}
