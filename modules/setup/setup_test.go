package setup

import (
	"bytes"
	"errors"
	"io"
	"io/ioutil"
	"path/filepath"
	"strings"
	"testing"

	term "github.com/rzbdz/newgate-ext/lib/term"
	"github.com/rzbdz/newgate-ext/modules/config/paths"
	"github.com/rzbdz/newgate-ext/modules/config/store"
	agentapi "github.com/rzbdz/newgate-ext/modules/confighook"
	runtimeapi "github.com/rzbdz/newgate-ext/modules/runtime"
	"github.com/rzbdz/newgate-ext/testing/testkit"
)

// This file drives the whole wizard with a **fake terminal**: scripted keys in, screen to buffer.
// It pins the decisions the user never sees but that decide what the machine becomes:
//
//	already-configured second run   -> no upstream question, only install/take over selected
//	unchecked clients               -> not touched at all (that screen's words must be true)
//	install fails                   -> say so, do not count that step as success
//	no terminal                     -> write nothing, list what to type

// fakeScreen emits keys and whole lines from a script.
type fakeScreen struct {
	events []term.Event
	lines  []string
	picked []func(string, string, int) // recorded on every render (optional assertions)
	sent   *bytes.Buffer
}

func (f *fakeScreen) ReadEvent() term.Event {
	if len(f.events) == 0 {
		return term.Event{Key: term.KeyInterrupt}
	}
	ev := f.events[0]
	f.events = f.events[1:]
	return ev
}

func (f *fakeScreen) ReadLine(mode term.LineMode, render func(string, string, int)) term.LineResult {
	if len(f.lines) == 0 {
		return term.LineResult{Cancel: true}
	}
	line := f.lines[0]
	f.lines = f.lines[1:]
	if mode.Validate != nil {
		if msg := mode.Validate(line); msg != "" {
			return term.LineResult{Cancel: true}
		}
	}
	return term.LineResult{Text: line}
}

func keys(ks ...term.Key) []term.Event {
	out := make([]term.Event, len(ks))
	for i, k := range ks {
		out[i] = term.Event{Key: k}
	}
	return out
}

func concat(parts ...[]term.Event) []term.Event {
	var out []term.Event
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

func runes(rs ...rune) []term.Event {
	out := make([]term.Event, len(rs))
	for i, r := range rs {
		out[i] = term.Event{Key: term.KeyRune, Rune: r}
	}
	return out
}

// fakeCatalog is the smallest proxy catalog: who is here, who can be installed.
type fakeCatalog struct {
	agents       map[string]*agentapi.Agent
	installed    map[string]bool
	installs     *[]string // records which agents had an install command run
	installFails map[string]bool
}

func (c fakeCatalog) Get(id string) (*agentapi.Agent, bool) { a, ok := c.agents[id]; return a, ok }
func (c fakeCatalog) Names() []string {
	out := make([]string, 0, len(c.agents))
	for id := range c.agents {
		out = append(out, id)
	}
	return out
}
func (c fakeCatalog) Facts(string) agentapi.AgentFacts { return nil }
func (c fakeCatalog) Installer(id string) agentapi.AgentInstaller {
	if _, ok := c.agents[id]; !ok {
		return nil
	}
	return fakeInstaller{id: id, catalog: c}
}
func (c fakeCatalog) Installed(id string) bool { return c.installed[id] }

type fakeInstaller struct {
	id      string
	catalog fakeCatalog
}

func (f fakeInstaller) Command() []string { return []string{"fake-install", f.id} }
func (f fakeInstaller) Install(out io.Writer) error {
	if f.catalog.installFails != nil && f.catalog.installFails[f.id] {
		return errors.New("fake install failed")
	}
	if f.catalog.installs != nil {
		*f.catalog.installs = append(*f.catalog.installs, f.id)
	}
	f.catalog.installed[f.id] = true
	return nil
}

// fakeControl records every takeover/start-proxy call.
type fakeControl struct {
	status      []runtimeapi.Status
	takenOver   []string
	started     int
	startOpts   runtimeapi.StartOptions
	takeoverErr map[string]error
}

func (c *fakeControl) TakeOver(agent string, port int) runtimeapi.Result {
	if err := c.takeoverErr[agent]; err != nil {
		return runtimeapi.Result{Agent: agent, Err: err}
	}
	c.takenOver = append(c.takenOver, agent)
	return runtimeapi.Result{Agent: agent, Lines: []string{"taken"}}
}
func (c *fakeControl) Release(agent string) runtimeapi.Result { return runtimeapi.Result{Agent: agent} }
func (c *fakeControl) TakeOverAll(port int) []runtimeapi.Result {
	return nil
}
func (c *fakeControl) TakeoverStatus() []runtimeapi.Status { return c.status }
func (c *fakeControl) Start(opts runtimeapi.StartOptions) (runtimeapi.StartReport, error) {
	c.started++
	c.startOpts = opts
	return runtimeapi.StartReport{Running: true, PID: 1, Port: 8899, Profile: "default"}, nil
}

// stubModels swaps "ask upstream for the list" for a fixed answer, so the test stays offline.
func stubModels(t *testing.T, models []string, why string) {
	t.Helper()
	old := probeModels
	probeModels = func(string, string, string) ([]string, string) { return models, why }
	t.Cleanup(func() { probeModels = old })
}

func sandbox(t *testing.T) {
	t.Helper()
	testkit.Sandbox(t)
	if err := paths.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
}

func catalogWith(ids ...string) fakeCatalog {
	c := fakeCatalog{agents: map[string]*agentapi.Agent{}, installed: map[string]bool{}}
	for _, id := range ids {
		// Only a Bin, enough for pick/EnsureAgents; takeover goes through fakeControl.
		c.agents[id] = &agentapi.Agent{ID: id, Bin: []string{id}}
		c.installed[id] = true
	}
	return c
}

// TestSecondRunDoesNotAskForUpstream is "an already-working user reruns the wizard":
// they mostly just want to install a new client. Asked for a base URL they most likely
// Ctrl-C - and what they wanted went undone.
func TestSecondRunDoesNotAskForUpstream(t *testing.T) {
	sandbox(t)
	if _, err := store.Init(false); err != nil {
		t.Fatal(err)
	}
	// Make it "usable": give every placeholder provider a key.
	provs, _ := store.LoadProviders()
	for name, p := range provs.Providers {
		p.APIKey, p.APIKeyEnv = "sk-real", ""
		provs.Providers[name] = p
	}
	if err := store.SaveProviders(provs); err != nil {
		t.Fatal(err)
	}
	if !Inspect().HasUsable {
		t.Fatal("precondition not met: this config should have been judged usable")
	}

	// Not one line may be asked: the script gives no input, so the upstream screen cancels.
	scr := &fakeScreen{events: keys(term.KeyEnter)}
	cat := catalogWith("claude")
	ctl := &fakeControl{}
	var out bytes.Buffer

	code := runWithScreen(&out, Deps{Agents: cat, Control: ctl, Out: &out}, Options{NoStart: true}, scr)
	if code != 0 {
		t.Fatalf("exit code %d, want 0", code)
	}
	if len(ctl.takenOver) != 1 || ctl.takenOver[0] != "claude" {
		t.Fatalf("took over %v, want [claude]", ctl.takenOver)
	}
	if got := store.LoadState().DefaultProfile; got == ProfileName {
		t.Fatal("the second run changed the global default to default (it must not touch config)")
	}
}

// TestUncheckedClientsAreNotTouched: that screen's words are "skip them and nothing changes".
// This pins it - `Start` must be called with SkipTakeover, and takeover only for checked ones.
func TestUncheckedClientsAreNotTouched(t *testing.T) {
	sandbox(t)
	// No usable config -> the upstream is asked (the script has the answers ready).
	cat := catalogWith("claude", "codex")
	// codex absent, claude taken over -> only codex is checked by default; uncheck it, check claude.
	cat.installed["codex"] = false
	ctl := &fakeControl{status: []runtimeapi.Status{{Agent: "claude", Active: true}}}
	var out bytes.Buffer

	stubModels(t, []string{"demo-model"}, "")
	scr := &fakeScreen{
		// List order claude, codex: cursor on codex (checked), uncheck it, move to claude, check.
		events: concat(keys(term.KeyUp), runes(' '), keys(term.KeyDown, term.KeyEnter, term.KeyEnter)),
		lines:  []string{"https://api.example.com/v1", "sk-test", "demo"},
	}
	code := runWithScreen(&out, Deps{Agents: cat, Control: ctl, Out: &out}, Options{NoStart: true, Yes: false}, scr)
	if code != 0 {
		t.Fatalf("exit code %d (output: %s)", code, out.String())
	}
	for _, id := range ctl.takenOver {
		if id == "codex" {
			t.Fatalf("the unchecked codex was taken over (output: %s)", out.String())
		}
	}
	if len(ctl.takenOver) != 1 || ctl.takenOver[0] != "claude" {
		t.Fatalf("took over %v, want [claude]", ctl.takenOver)
	}
	if !strings.Contains(out.String(), "Next") {
		t.Fatalf("the closing screen was not drawn: %s", out.String())
	}
}

// TestNotTakenOverYetIsSaidOutLoud: a config-type client's first takeover says "skipped
// (no config file)" - that is not success. The summary must say so, or requests go direct.
func TestNotTakenOverYetIsSaidOutLoud(t *testing.T) {
	sandbox(t)
	step := Step{Agent: "codex", Installed: true, TakenOver: false,
		Takeover: runtimeapi.Result{Agent: "codex", Lines: []string{"~/.codex/config.toml (skipped: no such file)"}}}
	var b bytes.Buffer
	Report(&b, []Step{step})
	if !strings.Contains(b.String(), "not taken over yet") {
		t.Fatalf("a skipped takeover was reported as success: %s", b.String())
	}

	var out strings.Builder
	printOutcome(&out, ApplyResult{}, []Step{step}, StartReporter{Next: nextSteps([]Step{step})})
	if !strings.Contains(out.String(), "not taken over yet") {
		t.Fatalf("the closing screen reported a skipped takeover as success: %s", out.String())
	}
	if !strings.Contains(out.String(), "then: newgate on codex") {
		t.Fatalf("the closing screen did not offer the \"run once first, then take over\" path: %s", out.String())
	}
}

// TestNoTTYWritesNothing: never silently change config in a non-interactive environment.
func TestNoTTYWritesNothing(t *testing.T) {
	sandbox(t)
	before := snapshot(t)
	var out bytes.Buffer
	code := noTTY(Deps{Out: &out}, Options{}, term.ErrNotATTY)
	if code == 0 {
		t.Fatal("reported success without a terminal")
	}
	if after := snapshot(t); after != before {
		t.Fatal("changed the config directory without a terminal")
	}
}

func snapshot(t *testing.T) string {
	t.Helper()
	var b strings.Builder
	for _, p := range []string{paths.ProvidersFile(), paths.StateFile()} {
		if raw, err := ioutil.ReadFile(p); err == nil {
			b.WriteString(p)
			b.Write(raw)
		}
	}
	ents, _ := ioutil.ReadDir(paths.Mappings())
	for _, e := range ents {
		b.WriteString(e.Name())
	}
	_ = filepath.Separator
	return b.String()
}
