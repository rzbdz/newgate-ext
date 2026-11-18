package hermes

import (
	"strings"
	"testing"
)

// The byte-surgery family tests: what should change does change correctly, everything else stays
// byte for byte, and editing twice equals editing once.
//
// sampleYAML is the shape hermes 0.13 itself dumps (an excerpt of ~/.hermes/config.yaml on this
// machine on 2026-11-04, keys replaced with placeholders): block style, two-space indent, empty
// mappings written as `{}`, lists indented under their parent key.
const sampleYAML = `model:
  provider: custom
  model: claude-opus-5
  context_length: 1000000
  default: claude-opus-5
  base_url: https://api.example.com:60000/
  api_key: sk-placeholder
  api_mode: anthropic_messages
providers: {}
fallback_providers: []
toolsets:
  - hermes-cli
agent:
  max_turns: 90
  personalities:
    helpful: You are a helpful, friendly AI assistant.
    pirate: "Arrr! Ye be talkin' to Captain Hermes: yo ho ho!"
auxiliary:
  vision:
    provider: smt-vision
    model: gpt-4o
    base_url: ""
    api_key: ""
    timeout: 120
    extra_body: {}
  compression:
    provider: auto
    model: ""
    timeout: 120
delegation:
  model: claude-sonnet-4.6
  provider: ""
  max_iterations: 50
  default_toolsets:
    - terminal
    - file
smart_model_routing:
  enabled: true
  cheap_model:
    provider: smt-codex
    model: gemini-3-flash-preview
custom_providers:
  - name: smt-claude
    base_url: https://api.example.com:60000/
    models:
      claude-opus-5:
        context_length: 1000000
`

func apply(t *testing.T, src string, edits ...edit) string {
	t.Helper()
	d := parseDoc(src)
	for _, e := range edits {
		if _, err := d.put(e); err != nil {
			t.Fatalf("put %v: %v", e.Path, err)
		}
	}
	return d.String()
}

func TestPutScalarTouchesOnlyThatLine(t *testing.T) {
	out := apply(t, sampleYAML, edit{Path: []string{"model", "default"}, Value: "normal"})
	want := strings.Replace(sampleYAML, "  default: claude-opus-5\n", "  default: normal\n", 1)
	if out != want {
		t.Errorf("only one line should change\n--- got ---\n%s", out)
	}
}

// Same-named keys in sub-mappings (auxiliary.vision.model and top-level model.model) must not
// be confused.
func TestPutNestedDoesNotConfuseSameNamedKeys(t *testing.T) {
	out := apply(t, sampleYAML, edit{Path: []string{"auxiliary", "vision", "model"}, Value: "vision"})
	if !strings.Contains(out, "  model: claude-opus-5\n") {
		t.Errorf("the top-level model.model must not be touched\n%s", out)
	}
	if !strings.Contains(out, "    model: vision\n") || strings.Contains(out, "gpt-4o") {
		t.Errorf("auxiliary.vision.model should change\n%s", out)
	}
	if !strings.Contains(out, "  compression:\n    provider: auto\n") {
		t.Errorf("sibling blocks must not be touched\n%s", out)
	}
}

// `providers: {}` is how hermes writes an empty mapping: adding keys inside first turns it into
// a block.
func TestPutIntoEmptyFlowMapping(t *testing.T) {
	out := apply(t, sampleYAML, edit{Path: []string{"providers", "newgate"},
		Value: omap{{"name", "newgate"}, {"base_url", "http://127.0.0.1:8899/a/hermes/v1"}}})
	if !strings.Contains(out, "providers:\n  newgate:\n    name: newgate\n    base_url: \"http://127.0.0.1:8899/a/hermes/v1\"\nfallback_providers: []\n") {
		t.Errorf("the provider block should be inserted under providers\n%s", out)
	}
}

// Was a block, now written as a scalar: replace the whole block, leave no orphan child lines.
func TestPutReplacesWholeBlock(t *testing.T) {
	out := apply(t, sampleYAML, edit{Path: []string{"smart_model_routing", "cheap_model"},
		Value: omap{{"provider", "newgate"}, {"model", "light"}}})
	if strings.Contains(out, "gemini-3-flash-preview") || strings.Count(out, "cheap_model:") != 1 {
		t.Errorf("the old block should be replaced entirely\n%s", out)
	}
	if !strings.Contains(out, "  cheap_model:\n    provider: newgate\n    model: light\ncustom_providers:") {
		t.Errorf("the position/indentation of the new block is wrong\n%s", out)
	}
}

func TestPutMissingParentsAreCreated(t *testing.T) {
	out := apply(t, "a: 1\n", edit{Path: []string{"x", "y", "z"}, Value: 3})
	if out != "a: 1\nx:\n  y:\n    z: 3\n" {
		t.Errorf("got\n%s", out)
	}
}

func TestPutIsIdempotent(t *testing.T) {
	edits := []edit{
		{Path: []string{"model", "default"}, Value: "normal"},
		{Path: []string{"model", "base_url"}, Value: "http://127.0.0.1:8899/a/hermes/v1"},
		{Path: []string{"providers", "newgate"}, Value: omap{{"models", omap{{"normal", omap{{"context_length", 200000}}}}}}},
		{Path: []string{"delegation", "default_toolsets"}, Value: []any{"terminal"}},
	}
	once := apply(t, sampleYAML, edits...)
	d := parseDoc(once)
	for _, e := range edits {
		changed, err := d.put(e)
		if err != nil || changed {
			t.Errorf("a second put %v should not change anything again (changed=%v err=%v)", e.Path, changed, err)
		}
	}
	if d.String() != once {
		t.Errorf("the two results differ")
	}
}

// A non-empty scalar must not be treated as a mapping to write into -- that loses someone
// else's values.
func TestPutRefusesToTurnAValueIntoAMapping(t *testing.T) {
	d := parseDoc("model: claude-opus-5\n")
	if _, err := d.put(edit{Path: []string{"model", "default"}, Value: "normal"}); err == nil {
		t.Errorf("model is a string (the old hermes way); stuffing child keys into it should be rejected")
	}
}

func TestRemove(t *testing.T) {
	out := apply(t, sampleYAML, edit{Path: []string{"model", "api_key"}})
	if strings.Contains(out, "api_key: sk-placeholder") {
		t.Errorf("not deleted\n%s", out)
	}
	if !strings.Contains(out, "  base_url: https://api.example.com:60000/\n  api_mode:") {
		t.Errorf("the neighbor should join up as is\n%s", out)
	}
}

func TestLitQuotesWhatYAMLWouldReinterpret(t *testing.T) {
	for in, want := range map[string]string{
		"normal":                     "normal",
		"newgate-ds":                 "newgate-ds",
		"smt-claude/claude-opus-5-5": "smt-claude/claude-opus-5-5",
		"http://127.0.0.1:8899/v1":   `"http://127.0.0.1:8899/v1"`,
		"yes":                        `"yes"`,
		"123":                        `"123"`,
		"":                           `""`,
		"a: b":                       `"a: b"`,
	} {
		if got := lit(in); got != want {
			t.Errorf("lit(%q) = %s, want %s", in, got, want)
		}
	}
}

func TestChildrenAndGet(t *testing.T) {
	d := parseDoc(sampleYAML)
	if got := strings.Join(d.children("auxiliary"), ","); got != "vision,compression" {
		t.Errorf("children = %s", got)
	}
	if v, _ := d.get("agent", "personalities", "pirate"); !strings.HasPrefix(v, "Arrr! Ye be talkin'") {
		t.Errorf("a quoted value should be read back without the quotes: %q", v)
	}
}
