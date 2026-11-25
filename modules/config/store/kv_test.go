package store

import (
	"io/ioutil"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rzbdz/newgate-ext/modules/config/domain"
)

func kvSandbox(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("NEWGATE_HOME", dir)
	if err := os.MkdirAll(filepath.Join(dir, "mappings"), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

func writeMapping(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := ioutil.WriteFile(filepath.Join(dir, "mappings", name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestParseProfileKV the syntax surface: tier abbreviations, bare models,
// candidate lists, comments, and unknown keys must error (not be silently ignored).
func TestParseProfileKV(t *testing.T) {
	p, err := ParseProfileKV(`# a comment line
extends = kimi
desc = high-speed main loop
prio = 60
excluded = true
window = 1000000
compact = 500000

mid = kimi-k2.7-code-highspeed
heavy = kimi/kimi-k3, other/big
fallback = kimi/kimi-k3
`)
	if err != nil {
		t.Fatal(err)
	}
	if p.Extends != "kimi" || p.Description != "high-speed main loop" || !p.Excluded {
		t.Fatalf("scalar parsed wrong: %+v", p)
	}
	if p.Prio() != 60 || p.ContextWindow != 1000000 || p.AutoCompactWindow != 500000 {
		t.Fatalf("number parsed wrong: prio=%d window=%d", p.Prio(), p.ContextWindow)
	}
	if mid := p.Roles["mid"]; len(mid) != 1 || mid[0].Model != "kimi-k2.7-code-highspeed" || mid[0].Provider != "" {
		t.Fatalf("bare model parsed wrong: %v", mid)
	}
	if hv := p.Roles["heavy"]; len(hv) != 2 || hv[0].String() != "kimi/kimi-k3" || hv[1].String() != "other/big" {
		t.Fatalf("candidate list parsed wrong: %v", hv)
	}
	if p.Fallback == nil || p.Fallback.String() != "kimi/kimi-k3" {
		t.Fatalf("fallback parsed wrong: %v", p.Fallback)
	}

	// An unknown key errors -- a misspelled field name being silently
	// ignored is the nastiest. Assert on English (the source language): the
	// wording is an i18n translation, the identity is the sentence itself.
	if _, err := ParseProfileKV("midle=x\n"); err == nil || !strings.Contains(err.Error(), "unknown key") {
		t.Fatalf("an unknown key should error, actual %v", err)
	}
	// A bad boolean is not guessed
	if _, err := ParseProfileKV("excluded=maybe\n"); err == nil {
		t.Fatal("a boolean other than true/false should error")
	}
	// The bare key=value shape
	if _, err := ParseProfileKV("this is not kv\n"); err == nil {
		t.Fatal("a line that is not key=value should error")
	}
}

// TestProfileKVRoundTrip serialize then parse, not one field lost (including arbitrary tier
// keys and "*" -- shapes left over from the json era must not evaporate in the conversion).
func TestProfileKVRoundTrip(t *testing.T) {
	prio := 18
	p := &domain.Profile{
		Name: "x", Description: "d", Priority: &prio, Pinned: true, Excluded: true,
		Extends: "base", ContextWindow: 1000000, AutoCompactWindow: 500000,
		Roles: map[string]domain.Candidates{
			"heavy":  {domain.Binding{Provider: "a", Model: "m1"}},
			"mid":    {domain.Binding{Provider: "a", Model: "m2"}, domain.Binding{Provider: "b", Model: "m3"}},
			"search": {domain.Binding{Provider: "a", Model: "m4"}},
			"*":      {domain.Binding{Provider: "a", Model: "m5"}},
		},
		Fallback: &domain.Binding{Provider: "f", Model: "fb"},
	}
	p2, err := ParseProfileKV(SerializeProfileKV(p))
	if err != nil {
		t.Fatal(err)
	}
	if p2.Description != "d" || p2.Prio() != 18 || !p2.Pinned || !p2.Excluded ||
		p2.Extends != "base" || p2.ContextWindow != 1000000 || p2.AutoCompactWindow != 500000 {
		t.Fatalf("scalar round trip lost a field: %+v", p2)
	}
	for _, k := range []string{"heavy", "mid", "search", "*"} {
		if got, want := len(p2.Roles[k]), len(p.Roles[k]); got != want {
			t.Fatalf("roles[%s] candidate count %d != %d", k, got, want)
		}
	}
	if p2.Roles["mid"][1].String() != "b/m3" {
		t.Fatalf("candidate list round trip wrong: %v", p2.Roles["mid"])
	}
	if p2.Fallback == nil || p2.Fallback.String() != "f/fb" {
		t.Fatalf("fallback round trip wrong: %v", p2.Fallback)
	}
}

// TestLoadProfileExtends extends merging: differences override, missing items are
// inherited, bare models borrow the provider from base's same tier, bools are not inherited.
func TestLoadProfileExtends(t *testing.T) {
	dir := kvSandbox(t)
	writeMapping(t, dir, "kimi.json", `{"name":"kimi","priority":22,"pinned":false,
		"roles":{"heavy":{"provider":"kimi","model":"kimi-k3"},
		         "mid":{"provider":"kimi","model":"k2.7"},"light":{"provider":"kimi","model":"k2.7"}}}`)
	writeMapping(t, dir, "kimi-fast.kv", `extends=kimi
excluded=true
desc=fast
mid=k2.7-highspeed
`)

	p, err := LoadProfile("kimi-fast")
	if err != nil {
		t.Fatal(err)
	}
	if p.Extends != "" {
		t.Fatal("the resolved view should not still carry the extends declaration")
	}
	if p.Excluded != true {
		t.Fatal("its own excluded must take effect")
	}
	if p.Pinned {
		t.Fatal("bool is not inherited (base did not write pinned, so it must not appear here out of thin air)")
	}
	if p.Prio() != 22 {
		t.Fatalf("priority should inherit base's 22, actual %d", p.Prio())
	}
	if m := p.Roles["mid"]; len(m) != 1 || m[0].String() != "kimi/k2.7-highspeed" {
		t.Fatalf("a bare model should borrow the provider from base: %v", m)
	}
	if m := p.Roles["heavy"]; len(m) != 1 || m[0].String() != "kimi/kimi-k3" {
		t.Fatalf("tiers not mentioned should be inherited: %v", m)
	}
}

// TestLoadProfileExtendsCycle a cycle must error, not overflow the stack.
func TestLoadProfileExtendsCycle(t *testing.T) {
	dir := kvSandbox(t)
	writeMapping(t, dir, "a.kv", "extends=b\n")
	writeMapping(t, dir, "b.kv", "extends=a\n")
	if _, err := LoadProfile("a"); err == nil || !strings.Contains(err.Error(), "extends cycle") {
		t.Fatalf("an extends cycle should error, actual %v", err)
	}
	// extends a base that does not exist
	writeMapping(t, dir, "c.kv", "extends=ghost\n")
	if _, err := LoadProfile("c"); err == nil {
		t.Fatal("extending a nonexistent base should error")
	}
}

// TestKVWinsOverJSON when a .kv and a .json of the same name coexist: kv wins
// (it is only ever created deliberately), and ListProfiles lists only one name.
func TestKVWinsOverJSON(t *testing.T) {
	dir := kvSandbox(t)
	writeMapping(t, dir, "p.json", `{"name":"p","roles":{"mid":{"provider":"j","model":"from-json"}}}`)
	writeMapping(t, dir, "p.kv", "mid=kv/model\n")

	p, err := LoadProfile("p")
	if err != nil {
		t.Fatal(err)
	}
	if m := p.Roles["mid"]; len(m) != 1 || m[0].String() != "kv/model" {
		t.Fatalf("kv should win, actual %v", m)
	}
	names, err := ListProfiles()
	if err != nil || len(names) != 1 || names[0] != "p" {
		t.Fatalf("the same name should be listed only once: %v %v", names, err)
	}
}

// TestAgentVisibilityKV: the whitelist and blacklist are two separate keys, comma separated,
// and they survive a serialize round trip (the KV form is what users hand-edit).
func TestAgentVisibilityKV(t *testing.T) {
	p, err := ParseProfileKV("visible-agents = gemini, codex\nhidden-agents=claude\ndesc = x\n")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(p.VisibleAgents, ",") != "gemini,codex" {
		t.Fatalf("visible-agents = %v", p.VisibleAgents)
	}
	if strings.Join(p.HiddenAgents, ",") != "claude" {
		t.Fatalf("hidden-agents = %v", p.HiddenAgents)
	}
	out := SerializeProfileKV(p)
	if !strings.Contains(out, "visible-agents=gemini, codex") ||
		!strings.Contains(out, "hidden-agents=claude") {
		t.Fatalf("round trip lost the visibility keys:\n%s", out)
	}
	back, err := ParseProfileKV(out)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(back.VisibleAgents, ",") != "gemini,codex" ||
		strings.Join(back.HiddenAgents, ",") != "claude" {
		t.Fatalf("second parse = %v / %v", back.VisibleAgents, back.HiddenAgents)
	}
}

func TestVisibleToAgent(t *testing.T) {
	cases := []struct {
		p     *domain.Profile
		agent string
		want  bool
	}{
		{&domain.Profile{}, "gemini", true},
		{&domain.Profile{}, "", true},
		{&domain.Profile{VisibleAgents: []string{"gemini"}}, "gemini", true},
		{&domain.Profile{VisibleAgents: []string{"gemini"}}, "claude", false},
		{&domain.Profile{HiddenAgents: []string{"gemini"}}, "gemini", false},
		{&domain.Profile{HiddenAgents: []string{"gemini"}}, "claude", true},
		// Blacklist subtracts even when the whitelist would allow.
		{&domain.Profile{VisibleAgents: []string{"gemini"}, HiddenAgents: []string{"gemini"}}, "gemini", false},
		// No agent = no identity to judge.
		{&domain.Profile{VisibleAgents: []string{"gemini"}}, "", true},
		{&domain.Profile{HiddenAgents: []string{"gemini"}}, "", true},
	}
	for _, c := range cases {
		if got := c.p.VisibleToAgent(c.agent); got != c.want {
			t.Errorf("%+v vs %q: got %v want %v", c.p, c.agent, got, c.want)
		}
	}
}
