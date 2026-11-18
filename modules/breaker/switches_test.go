package breaker

import (
	"strings"
	"testing"
	"time"

	"github.com/rzbdz/newgate-ext/modules/config/domain"
	gwpolicy "github.com/rzbdz/newgate-ext/modules/gateway/policy"
)

// The pause switch (SwitchAdmission) is the cure for "one network wobble delisted everything".
//
// Three properties, and the third is the one a naive implementation gets wrong:
//
//  1. While paused, a delisted binding is admitted again (the chain is not steered);
//  2. Turning it off **clears** what is delisted, once, so `newgate breaker` does not keep
//     showing a full table while the user believes they just fixed it;
//  3. Turning it back on restores exactly the old behaviour -- the ledger keeps counting
//     during the pause, so what it shows afterwards is evidence, not a stale verdict.

// stateWith builds a config snapshot holding exactly this module's switch section.
func stateWith(t *testing.T, off ...string) *domain.State {
	t.Helper()
	var entries []string
	for _, path := range off {
		entries = append(entries, `"`+path+`":{}`)
	}
	raw := `{"off":{` + strings.Join(entries, ",") + `}}`
	return &domain.State{ModuleConfig: map[string][]byte{"plugin_manager": []byte(raw)}}
}

// pausedPlane is a plane whose BindEnv got a snapshot carrying `off` paths.
func pausedPlane(table *table, st *domain.State) *plane {
	p := &plane{t: table}
	p.BindEnv(envStub{
		logf:  func(string, ...any) {},
		probe: func(string, string) bool { return false },
		// envStub.State returns the field directly, so the plane's state hook reads it.
		state: st,
	})
	return p
}

func TestPauseSwitchAdmitsDelistedBindings(t *testing.T) {
	table, clock := clocked()
	p := pausedPlane(table, nil)

	// Trip it for real: two consecutive connection failures (DefaultPolicy: 2 / 60s).
	for i := 0; i < 2; i++ {
		p.Judge(gwpolicy.Outcome{Provider: "up", Model: "m", Kind: gwpolicy.ConnectionFailed})
		clock(time.Second)
	}
	if ok, _ := p.Admit("up", "m"); ok {
		t.Fatal("the setup did not trip the breaker -- the rest of this test would prove nothing")
	}

	p = pausedPlane(table, stateWith(t, SwitchAdmission))
	if ok, _ := p.Admit("up", "m"); !ok {
		t.Fatal("while paused every binding must be admitted, delisted or not")
	}
}

// TestPauseSwitchClearsDelistingsOnce: the transition clears the verdict, and the ledger keeps
// the evidence. Doing it on every Admit would be a disk write per request; doing it never would
// leave `newgate breaker` claiming bindings are out while they are being used.
func TestPauseSwitchClearsDelistingsOnce(t *testing.T) {
	table, clock := clocked()
	p := pausedPlane(table, nil)
	for i := 0; i < 2; i++ {
		p.Judge(gwpolicy.Outcome{Provider: "up", Model: "m", Kind: gwpolicy.ConnectionFailed})
		clock(time.Second)
	}
	if rec := table.records[bindingKey("up", "m")]; rec == nil || rec.openedAt.IsZero() {
		t.Fatal("the setup did not trip the breaker")
	}

	p = pausedPlane(table, stateWith(t, SwitchAdmission))
	p.Admit("up", "m")

	rec := table.records[bindingKey("up", "m")]
	if !rec.openedAt.IsZero() || !rec.openUntil.IsZero() {
		t.Errorf("pausing did not clear the delisting: openedAt=%v openUntil=%v", rec.openedAt, rec.openUntil)
	}
	if rec.fails == 0 {
		t.Error("the failure count was cleared too -- the table must keep counting what fails while paused, " +
			"otherwise 'newgate breaker' goes blank and there is no evidence left afterwards")
	}
}

// TestPauseSwitchRestoresNormalBehaviour: with the switch back on (or never set), admission
// follows the health table exactly as before.
func TestPauseSwitchRestoresNormalBehaviour(t *testing.T) {
	table, clock := clocked()
	p := pausedPlane(table, nil)
	for i := 0; i < 2; i++ {
		p.Judge(gwpolicy.Outcome{Provider: "up", Model: "m", Kind: gwpolicy.ConnectionFailed})
		clock(time.Second)
	}
	if ok, _ := p.Admit("up", "m"); ok {
		t.Fatal("with nothing set the breaker must delist as usual")
	}

	// Pause, then resume: the delisting was cleared, but the ledger still says "two failures".
	p = pausedPlane(table, stateWith(t, SwitchAdmission))
	if ok, _ := p.Admit("up", "m"); !ok {
		t.Fatal("paused, yet the binding was still refused")
	}

	p = pausedPlane(table, nil)
	if ok, _ := p.Admit("up", "m"); !ok {
		t.Fatal("after the pause the cleared binding must be admissible -- it has not failed since")
	}
}

// TestPauseSwitchNilSnapshotIsNotPaused: a missing snapshot (no config, a broken section) must
// fail open -- "do not delist because a file could not be read" is the wrong direction.
func TestPauseSwitchNilSnapshotIsNotPaused(t *testing.T) {
	table, clock := clocked()
	p := &plane{t: table} // no BindEnv at all: the assembly-time/test path
	for i := 0; i < 2; i++ {
		p.Judge(gwpolicy.Outcome{Provider: "up", Model: "m", Kind: gwpolicy.ConnectionFailed})
		clock(time.Second)
	}
	if ok, _ := p.Admit("up", "m"); ok {
		t.Fatal("with no snapshot at all the breaker must behave exactly as before (delist)")
	}
}

// TestSwitchIsRegisteredWithALegalShape: registration is the only way the switch reaches
// `newgate plugin`, and plugin-manager validates the shape (prefix, title, why, danger). This
// pins our side: the path hangs under this module's name, it is a kill switch (factory on) and
// it is safe (no TTL -- the user decides when the network is back).
func TestSwitchIsRegisteredWithALegalShape(t *testing.T) {
	switches := Switches()
	if len(switches) != 1 {
		t.Fatalf("expected exactly one switch point, got %d", len(switches))
	}
	sw := switches[0]
	if sw.Path != SwitchAdmission || !strings.HasPrefix(sw.Path, "breaker.") {
		t.Errorf("the path must be %q and hang under the module name: %q", SwitchAdmission, sw.Path)
	}
	if !sw.Default {
		t.Error("it must be a kill switch (on from the factory) -- an assembly that silently ships with the " +
			"health table disabled is not the product")
	}
	if sw.Title == "" || sw.Why == "" {
		t.Error("title and why are what the user reads before flipping it; both are required")
	}
}
