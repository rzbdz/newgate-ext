package gateway

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rzbdz/newgate-ext/lib/view"
	"github.com/rzbdz/newgate-ext/modules/gateway/gatewaystate"
	"github.com/rzbdz/newgate-ext/modules/gateway/special"
	"github.com/rzbdz/newgate-ext/testing/testkit"
	i18n "github.com/rzbdz/newgate/lib/i18n"
)

// The data-gathering part of the log concept (tailLines / redactLine) is pure, so it can be
// tested in the leaf: create a real file, read its tail. Errors on this path make no noise --
// reading too much, too little, or shipping a credential all just look like "the log" in the
// UI. So it deserves a few assertions.

func writeLog(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "newgate.log")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestTailLinesReturnsTheLastOnesOldestFirst(t *testing.T) {
	p := writeLog(t, "a\nb\nc\nd\ne\n")
	// Oldest first: the newest line is at the bottom (see the note on logData.Lines).
	lines, truncated, err := tailLines(p, 10, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(lines, "|"); got != "a|b|c|d|e" {
		t.Errorf("the whole file fits, so it should be given as is: %q", got)
	}
	if truncated {
		t.Error("nothing was dropped, so it must not say truncated")
	}

	// Only the last 3 lines: the two above **really** were not sent out, so truncated must be
	// true -- not saying "you are not seeing everything" would deceive (the reader would think
	// the log starts here).
	lines, truncated, err = tailLines(p, 3, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(lines, "|"); got != "c|d|e" {
		t.Errorf("wrong tail: %q", got)
	}
	if !truncated {
		t.Error("two more lines above were not given, so it should say truncated")
	}
}

// TestTailLinesStopsAtTheByteLimit: the log rotates at 16MB×4, and the browser refreshes
// this view every few seconds. So reading must be **bounded** -- walking back line by line
// would read the whole file, a cost that grows with the log while the UI just looks "a bit
// slow".
func TestTailLinesStopsAtTheByteLimit(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 500; i++ {
		b.WriteString(strings.Repeat("x", 100))
		b.WriteString("\n")
	}
	b.WriteString("last line\n")
	p := writeLog(t, b.String())

	lines, truncated, err := tailLines(p, 400, 512)
	if err != nil {
		t.Fatal(err)
	}
	if !truncated {
		t.Error("cut by the byte limit, so it should say truncated - otherwise the user thinks the log is only this long")
	}
	if len(lines) == 0 || lines[len(lines)-1] != "last line" {
		t.Errorf("the last line of the tail should be the newest one, got %q", lines[len(lines)-1:])
	}
	// The first line is likely half a line (cut by limit) and must be dropped -- keep it and
	// the reader takes output with no beginning as evidence.
	if strings.Contains(lines[0], "x") && len(lines[0]) != 100 {
		t.Errorf("the first line is cut in half: %q", lines[0])
	}
}

func TestTailLinesOnMissingFile(t *testing.T) {
	_, _, err := tailLines(filepath.Join(t.TempDir(), "nope.log"), 10, 1024)
	if err == nil {
		t.Error("a missing file should error - the caller uses that to say 'no log yet' rather than 'the log is empty'")
	}
}

// TestRedactLine: credentials should never appear in logs, but the log is **written by
// others** (a provider URL, request headers printed by some module, the body a future plugin
// records), and this view ships the log to the browser. The catch-all criterion prefers
// over-killing.
func TestRedactLine(t *testing.T) {
	cases := map[string]string{
		"using key sk-abcdefghijklmnopqrst for demo": "using key *** for demo",
		`provider api_key=abcdefghijklmnop sent`:     `provider api_key=*** sent`,
		`Authorization: Bearer abcdefghijklmnop.qrs`: `Authorization: ***`,
		"-> deepseek/model-heavy (200)":              "-> deepseek/model-heavy (200)",
	}
	for in, want := range cases {
		if got := redactLine(in); got != want {
			t.Errorf("redactLine(%q) = %q, want %q", in, got, want)
		}
	}
}

// fakePlugin is the fake plugin used by this assertion: the real plugins live in the
// distribution (deepseek / glm / claudecode_*), and a kernel-native assembly has none -- with
// no fake, this ratchet would only hit t.Skip in CI, which is as good as unwritten.
type fakePlugin struct{ name, why string }

func (f fakePlugin) Name() string                { return f.name }
func (f fakePlugin) Why() string                 { return f.why }
func (f fakePlugin) Match(*special.Request) bool { return false }
func (f fakePlugin) Apply(b []byte, _ *special.Request) ([]byte, []string, error) {
	return b, nil, nil
}

// installFakePlugins swaps in a registry holding only these few plugins, and restores it
// afterwards.
//
// The registry is **process-level** (plugins are registered in the module's Start), so this
// uses the ownership-carrying InstallDefault -- the same hole as in modules/gateway/module.go;
// it must be handed back when done, or other tests will see these fake plugins.
func installFakePlugins(t *testing.T, ps ...special.Plugin) {
	t.Helper()
	reg := special.NewRegistry()
	for _, p := range ps {
		if _, err := reg.Register(p); err != nil {
			t.Fatalf("installing fake plugin %q: %v", p.Name(), err)
		}
	}
	t.Cleanup(special.InstallDefault(reg))
}

// TestEverythingGatewayShowsDeclaresItselfLive: the data behind all three cards changes on
// its own (counters climb, the log is written, patch switches may be flipped in a terminal),
// so all of them declare Live.
//
// Why it deserves an assertion: once Live is dropped **nothing errors** -- the UI simply stops
// asking every few seconds and the card sits quietly at the moment the page was opened. That
// is the one thing this card must never look like (a table claiming to say "which patches are
// touching your request now" but answering with ten minutes ago).
func TestEverythingGatewayShowsDeclaresItselfLive(t *testing.T) {
	cs, err := gatewayConcepts()
	if err != nil {
		t.Fatal(err)
	}
	if len(cs) == 0 {
		t.Fatal("the gateway has no cards at all - this assertion is a no-op")
	}
	for _, c := range cs {
		if !c.Live {
			t.Errorf("%s does not declare Live: it reads a snapshot in memory, and the content changes on its own", c.ID)
		}
	}
}

// specialTableOf pulls out the card's data. The assertions sit on the **data**, not on the
// rendered HTML (the same rule as on the archmap side): the data is the conclusion, rendering
// is just one way to draw it.
func specialTableOf(t *testing.T) view.Table {
	t.Helper()
	c := specialConcept()
	if c.ID != "gateway.special" || c.Kind != view.KindTable {
		t.Fatalf("the identity of this card changed: id=%q kind=%q", c.ID, c.Kind)
	}
	tb, ok := c.Data.(view.Table)
	if !ok {
		t.Fatalf("Data is not a view.Table, it is %T", c.Data)
	}
	return tb
}

// TestTheSpecialTableListsEveryPluginAndItsState is this card's ratchet.
//
// Why it exists: special_treatment **modifies the user's request**, and `newgate st` is where
// "is newgate breaking my request" is answered. In an assembly with only dashboard that
// command does not exist (dist-dashboard has no cli), so this card is the only answer -- so it
// must really list **every** plugin, and reflect both the layer switch and the per-plugin
// switch in the state.
//
// The state's wording shares a source with the CLI (one criterion, specialState); what is
// pinned here is "it really is used": miss the layer switch and the UI draws a whole layer
// turned off as "all in effect" -- worse than not showing it.
func TestTheSpecialTableListsEveryPluginAndItsState(t *testing.T) {
	testkit.Sandbox(t)
	installFakePlugins(t,
		fakePlugin{name: "shape-400", why: "the upstream rejects a tail it cannot read\n(second line, which the table must not carry)"},
		fakePlugin{name: "reasoning-backfill", why: "clients drop the reasoning content"},
	)

	ps := special.Plugins()
	if len(ps) != 2 {
		t.Fatalf("this registry should hold two plugins, got %d", len(ps))
	}
	stateOf := func(tb view.Table, name string) view.Cell {
		t.Helper()
		for _, r := range tb.Rows {
			if r.Cells["plugin"].Text == name {
				return r.Cells["state"]
			}
		}
		t.Fatalf("the table has no plugin %q", name)
		return view.Cell{}
	}

	// -- Default: layer on, nothing individually off → every row in effect --
	tb := specialTableOf(t)
	if len(tb.Rows) != len(ps) {
		t.Errorf("the table has %d rows for %d registered plugins - the missing ones are invisible in the UI",
			len(tb.Rows), len(ps))
	}
	for _, p := range ps {
		if got := stateOf(tb, p.Name()); got.Text != i18n.T("active", nil) || got.Tone != view.ToneOK {
			t.Errorf("in the default state %q should be in effect (ok), got %q/%q", p.Name(), got.Text, got.Tone)
		}
		for _, r := range tb.Rows {
			if r.Cells["plugin"].Text != p.Name() {
				continue
			}
			// Why only the first line: the full explanation is several lines; spreading it into
			// the table drowns it.
			if w := r.Cells["why"].Text; w == "" || strings.Contains(w, "\n") {
				t.Errorf("the why of %q should be a non-empty single line, got %q", p.Name(), w)
			}
		}
	}

	// -- Turn one off individually: only its row changes, and it must say **which kind** of
	// off --
	//
	// The assertion pins the **exact wording and exact color**, not "anything but active": each
	// kind of off has its own wording ("whole layer off" / "turned off individually"), and
	// swapping the two labels -- the UI telling a turned-off plugin "the whole layer is off" --
	// would still be green in a test that only asserts "it changed".
	off := ps[0].Name()
	if err := gatewaystate.SetSpecialPlugin(off, false); err != nil {
		t.Fatal(err)
	}
	tb = specialTableOf(t)
	if got := stateOf(tb, off); got.Text != i18n.T("disabled individually", nil) || got.Tone != view.ToneBad {
		t.Errorf("with %q disabled individually it should say 'disabled individually' (bad), got %q/%q", off, got.Text, got.Tone)
	}
	for _, p := range ps[1:] {
		if got := stateOf(tb, p.Name()); got.Text != i18n.T("active", nil) {
			t.Errorf("only %q was disabled, yet %q also became %q", off, p.Name(), got.Text)
		}
	}

	// -- Whole layer off: every row must say so, and say it is the **whole layer** --
	if err := gatewaystate.SetSpecialTreatment(false); err != nil {
		t.Fatal(err)
	}
	tb = specialTableOf(t)
	for _, p := range ps {
		if got := stateOf(tb, p.Name()); got.Text != i18n.T("layer off", nil) || got.Tone != view.ToneWarn {
			t.Errorf("with the whole layer off %q should say 'layer off' (warn), got %q/%q - "+
				"showing it as in effect is the lie of 'all patches are on'", p.Name(), got.Text, got.Tone)
		}
	}
}
