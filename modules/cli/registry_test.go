package cli

import (
	"strings"
	"testing"
)

// The ledger is on the UI's service, and these three ledgers' invariants are guarded here.
//
// This test pins a silent bug that **really existed once** (measured 2026-09-17): back
// when the component framework still allowed `Provides: Provide(someone's capability, value)`,
// two modules claiming the same command name went **first come first served with no error at
// all** -- the later registration could never be dispatched to, and its author thought it was
// installed. Now contributions must go through RegisterCommand, and a clash errors on the spot.
func TestRegisterCommandRejectsDuplicateName(t *testing.T) {
	s := &service{}

	if _, err := s.RegisterCommand(fakeCommand{names: []string{"omo", "omo-alias"}}); err != nil {
		t.Fatalf("the first registration should succeed: %v", err)
	}

	// A clash on an **alias** is a clash too: every name in Names
	// is a dispatch key, so the first one alone is not enough.
	_, err := s.RegisterCommand(fakeCommand{names: []string{"other", "omo-alias"}})
	if err == nil {
		t.Fatal("registering a command name twice did not error -- that is exactly the silent behavior to pin down")
	}
	if !strings.Contains(err.Error(), "omo-alias") {
		t.Errorf("the error should name the conflicting command name, actual: %v", err)
	}

	// The failed attempt must leave no trace: the ledger still has only the first one.
	if got := len(s.commands.All()); got != 1 {
		t.Errorf("ledger entries after a failed registration = %d, want 1 (a failed validation must not insert)", got)
	}
	if _, ok := s.moduleCommand("other"); ok {
		t.Error("a command name whose registration failed can still be dispatched -- validation and insertion are not atomic")
	}
}

// Release must precisely undo **its own entry**, not "clear" and not "delete the last one".
func TestRegisterCommandReleaseRemovesOnlyItsOwn(t *testing.T) {
	s := &service{}

	first, err := s.RegisterCommand(fakeCommand{names: []string{"a"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.RegisterCommand(fakeCommand{names: []string{"b"}}); err != nil {
		t.Fatal(err)
	}

	if err := first(); err != nil {
		t.Fatalf("revoke errored: %v", err)
	}
	if _, ok := s.moduleCommand("a"); ok {
		t.Error("a is still dispatchable after revoke")
	}
	if _, ok := s.moduleCommand("b"); !ok {
		t.Error("revoking a also took b away")
	}

	// A stale Release must be a harmless no-op: a component's Stop may be called more
	// than once, and on the second release the target is already gone; it must not error.
	if err := first(); err != nil {
		t.Errorf("a duplicate revoke should be a harmless no-op, actually errored: %v", err)
	}
	if _, ok := s.moduleCommand("b"); !ok {
		t.Error("a stale Release deleted someone else's entry")
	}
}

func TestRegisterCommandRejectsEmptyContribution(t *testing.T) {
	s := &service{}
	if _, err := s.RegisterCommand(nil); err == nil {
		t.Error("a nil command should be rejected")
	}
	if _, err := s.RegisterCommand(fakeCommand{}); err == nil {
		t.Error("a command without Names should be rejected (it can never be dispatched)")
	}
}

// Diagnostics are stackable: no key namespace, so no duplicate check; both are kept.
func TestRegisterDiagnosticsIsAdditive(t *testing.T) {
	s := &service{}
	for i := 0; i < 2; i++ {
		if _, err := s.RegisterDiagnostics(fakeDiagnostics{line: "x"}); err != nil {
			t.Fatalf("registration %d for diagnostics should succeed: %v", i+1, err)
		}
	}
	if got := len(s.moduleDiagnostics()); got != 2 {
		t.Errorf("diagnostic entries = %d, want 2 (diagnostics should not dedupe)", got)
	}
}

type fakeCommand struct{ names []string }

func (c fakeCommand) Names() []string      { return c.names }
func (fakeCommand) Run(Host, []string) int { return 0 }

type fakeDiagnostics struct{ line string }

func (d fakeDiagnostics) Diagnostics() []Diagnostic {
	return []Diagnostic{{Label: "fake", Line: d.line}}
}
