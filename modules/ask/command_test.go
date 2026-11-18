package ask

import (
	"strings"
	"testing"

	cliapi "github.com/rzbdz/newgate-ext/modules/cli/extension"
)

// This pins a pit where **sending it asks the wrong question**: `cliapi.Positional` only skips
// arguments starting with `-`, and the **value** of a value-taking option has no `-`. So in
// `newgate ask --tier light what is the weather today`, `light` is taken as the first positional
// -- the user asked about the weather, and what goes out is "light what is the weather today".
//
// It does not error, does not go red, it just **answers
// something nonsensical**, so it must be pinned.
func TestLeftoverDropsFlagValues(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"no options", []string{"today", "weather"}, "today weather"},
		{"a space-separated option value does not count as part of the question",
			[]string{"--tier", "light", "today", "weather"}, "today weather"},
		{"equals-form option", []string{"--tier=light", "today"}, "today"},
		{"short option", []string{"-t", "light", "today"}, "today"},
		{"several options mixed together",
			[]string{"--tier", "light", "--profile", "ds", "--system", "reply in Chinese only", "today"},
			"today"},
		{"after -- everything is the question itself (even if it looks like an option)",
			[]string{"--tier", "light", "--", "--help"}, "--help"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := strings.Join(leftover(c.args), " "); got != c.want {
				t.Errorf("leftover(%q) = %q, want %q", c.args, got, c.want)
			}
		})
	}
}

func TestParsePositiveInt(t *testing.T) {
	for _, ok := range []string{"1", "4096", "100000"} {
		n, err := parsePositiveInt(ok)
		if err != nil || n <= 0 {
			t.Errorf("parsePositiveInt(%q) = %d, %v; should accept it", ok, n, err)
		}
	}
	// 0, negatives, and garbage are all refused on the spot -- sending
	// them only gets an incomprehensible 400 from the upstream.
	for _, bad := range []string{"0", "-1", "a lot", "12x", ""} {
		if _, err := parsePositiveInt(bad); err == nil {
			t.Errorf("parsePositiveInt(%q) should error", bad)
		}
	}
}

// This pins **the contract for scripts**: not one byte of the body may go
// to stderr, and not one byte of the reasoning chain may go to stdout. In one
// answer from deepseek / glm the reasoning chain is often several times longer
// than the body; mixed in, what `answer=$(newgate ask …)` gets is unusable.
//
// It also pins "unknown events must not break the whole run": the upstream adding fields or
// changing event types is routine, and "stopped halfway" is the failure mode we least want.
func TestStreamRoutesTextAndThinkingApart(t *testing.T) {
	sse := strings.Join([]string{
		"event: message_start",
		`data: {"type":"message_start","message":{"id":"x"}}`,
		"",
		`data: {"type":"content_block_delta","delta":{"type":"thinking_delta","thinking":"think first"}}`,
		"",
		// An unseen type: skip it, but do not break.
		`data: {"type":"something_new","payload":{"a":1}}`,
		"",
		`data: {"type":"content_block_delta","delta":{"type":"thinking_delta","thinking":"think again"}}`,
		"",
		`data: {"type":"content_block_delta","delta":{"type":"text_delta","text":"hello"}}`,
		"",
		`data: {"type":"content_block_delta","delta":{"type":"text_delta","text":", world"}}`,
		"",
		// A line that is not JSON: also must not break.
		"data: this is not JSON",
		"",
		`data: {"type":"message_stop"}`,
		"",
	}, "\n")

	var out, thinking strings.Builder
	if err := streamAnswer(strings.NewReader(sse), &out, &thinking); err != nil {
		t.Fatalf("streamAnswer errored: %v", err)
	}
	if got := out.String(); got != "hello, world" {
		t.Errorf("stdout should contain only the body text, actual %q", got)
	}
	if got := thinking.String(); got != "think firstthink again" {
		t.Errorf("stderr should contain only the reasoning chain, actual %q", got)
	}
}

// An upstream error is an **event**, not "the stream broke": it must become
// a visible error return, not quietly print the last little bit and exit 0.
func TestStreamSurfacesAnErrorEvent(t *testing.T) {
	sse := `data: {"type":"error","error":{"message":"upstream said no"}}` + "\n"
	var out, thinking strings.Builder
	err := streamAnswer(strings.NewReader(sse), &out, &thinking)
	if err == nil {
		t.Fatal("an error event should become an error return")
	}
	if !strings.Contains(err.Error(), "upstream said no") {
		t.Errorf("the error should carry that upstream sentence, actual %v", err)
	}
}

// Unstyled must be there: the model's body text is not newgate's layout, and without declaring it
// the UI's layout audit would treat every sentence from the upstream as "non-compliant formatting".
func TestAskIsUnstyled(t *testing.T) {
	var c cliapi.Command = askCommand{}
	if _, ok := c.(cliapi.Unstyled); !ok {
		t.Fatal("askCommand must implement cliapi.Unstyled")
	}
	if _, ok := c.(cliapi.Documented); !ok {
		t.Fatal("askCommand must implement cliapi.Documented (otherwise it will not appear in --help)")
	}
}
