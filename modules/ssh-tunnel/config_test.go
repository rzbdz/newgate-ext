package sshtunnel

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/rzbdz/newgate-ext/modules/config/paths"
	"github.com/rzbdz/newgate-ext/modules/config/store"
	"github.com/rzbdz/newgate-ext/testing/testkit"
)

// This family checks the **read config** criteria. It is the layer most easily written
// loose: a bad read is unreported and only looks like "that target does not exist" -
// the user checks SSH, firewall, the remote, never thinking a config line was skipped.

func sandbox(t *testing.T) {
	t.Helper()
	testkit.Sandbox(t)
	if err := paths.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
}

func TestSaveAndLoadRoundTrip(t *testing.T) {
	sandbox(t)
	want := Target{
		ID: "snode1", Host: "10.0.50.11", User: "panjunzhong",
		RemotePort: 8899, LocalPort: 9401, Persistent: true, HostKey: HostKeyAcceptNew,
	}
	if err := Save([]Target{want.normalize()}); err != nil {
		t.Fatal(err)
	}
	got := Load()
	if len(got.Rejected) != 0 {
		t.Fatalf("the config just written was refused: %+v", got.Rejected)
	}
	if len(got.Targets) != 1 {
		t.Fatalf("read back %d entries, want 1", len(got.Targets))
	}
	if got.Targets[0] != want.normalize() {
		t.Errorf("read back %+v, wrote %+v", got.Targets[0], want.normalize())
	}
}

func TestDefaultsAreFilledInOnce(t *testing.T) {
	// Normalize happens once (at read), so "shown" and "used" never have two truths -
	// the symptom of a mismatch is "I see 8899 but it connects elsewhere".
	sandbox(t)
	if err := Save([]Target{{ID: "a", Host: "h"}}); err != nil {
		t.Fatal(err)
	}
	got := Load().Targets[0]
	if got.Port != DefaultSSHPort {
		t.Errorf("ssh port = %d, want %d", got.Port, DefaultSSHPort)
	}
	if got.RemoteHost != DefaultRemoteHost || got.RemotePort != DefaultRemotePort {
		t.Errorf("remote = %s:%d, want %s:%d", got.RemoteHost, got.RemotePort, DefaultRemoteHost, DefaultRemotePort)
	}
	if got.IdleSeconds != DefaultIdleSecs {
		t.Errorf("idle = %d, want %d", got.IdleSeconds, DefaultIdleSecs)
	}
	if got.HostKey != HostKeyStrict {
		t.Errorf("host_key = %q, the default must be strict", got.HostKey)
	}
	if got.Label != "a" {
		t.Errorf("label = %q, the default should use id", got.Label)
	}
	if got.Persistent {
		t.Errorf("the default must be lazy connect (persistent is an explicit choice)")
	}
}

func TestABrokenEntryIsSkippedButReported(t *testing.T) {
	// The broken one is **skipped but not swallowed**: its only exit is the UI card and
	// `newgate tunnel ls`. Log-only makes it visible only to log readers.
	sandbox(t)
	raw := `{"targets":[
		{"id":"good","host":"h"},
		{"id":"","host":"h"},
		{"id":"nohost"},
		{"id":"bad/id","host":"h"},
		{"id":"good","host":"h2"}
	]}`
	writeRaw(t, raw)

	got := Load()
	if len(got.Targets) != 1 || got.Targets[0].ID != "good" {
		t.Fatalf("only one good entry should remain, got %+v", got.Targets)
	}
	if len(got.Rejected) != 4 {
		t.Fatalf("it should report 4 bad configs, got %d: %+v", len(got.Rejected), got.Rejected)
	}
	// Each must say **why**: one "invalid config" says nothing.
	for _, bad := range got.Rejected {
		if bad.Why == "" || bad.Line == "" {
			t.Errorf("the refused one has no reason: %+v", bad)
		}
	}
	joined := ""
	for _, bad := range got.Rejected {
		joined += bad.Why + "\n"
	}
	for _, want := range []string{"id is required", "host is required", "path segment", "share this id"} {
		if !strings.Contains(joined, want) {
			t.Errorf("did not report %q, got:\n%s", want, joined)
		}
	}
}

func TestTwoTargetsCannotShareALocalPort(t *testing.T) {
	// Two targets claiming the same local port: the later `net.Listen` fails, and the symptom
	// is "this tunnel did not start" - the user never suspects **another one** took the port.
	sandbox(t)
	writeRaw(t, `{"targets":[
		{"id":"a","host":"h","local_port":9401},
		{"id":"b","host":"h","local_port":9401}
	]}`)
	got := Load()
	if len(got.Targets) != 1 || got.Targets[0].ID != "a" {
		t.Fatalf("the first declared a should be kept, got %+v", got.Targets)
	}
	if len(got.Rejected) != 1 || !strings.Contains(got.Rejected[0].Why, "9401") {
		t.Fatalf("it should report the port conflict and say which port: %+v", got.Rejected)
	}
}

func TestGarbageInTheSectionIsNotFatal(t *testing.T) {
	// fail-open: unreadable gives an empty table, not an error. One unreadable preference
	// must not block the whole UI (same as web-dashboard reading skin choice).
	sandbox(t)
	// Our cell is valid JSON but **not the shape we know** (an object, not an array).
	// Really breaking the whole file is different - store.LoadState fails open to an empty
	// state itself, and we cannot even read this cell (so nothing to report).
	writeRaw(t, `{"targets":{"nope":1}}`)
	got := Load()
	if len(got.Targets) != 0 {
		t.Errorf("bad JSON should give an empty table, got %+v", got.Targets)
	}
	if len(got.Rejected) != 1 {
		t.Errorf("bad JSON should be reported (otherwise it is silent), got %+v", got.Rejected)
	}
}

func TestTargetsComeBackInAStableOrder(t *testing.T) {
	// With stable order, UI rows do not jump between refreshes (the row about to be clicked
	// moves - the "reorder on click" class of behavior nobody dares use).
	sandbox(t)
	if err := Save([]Target{{ID: "zeta", Host: "h"}, {ID: "alpha", Host: "h"}, {ID: "mid", Host: "h"}}); err != nil {
		t.Fatal(err)
	}
	got := Load().Targets
	if len(got) != 3 || got[0].ID != "alpha" || got[1].ID != "mid" || got[2].ID != "zeta" {
		t.Errorf("wrong order: %+v", got)
	}
}

func TestMissingConfigIsSimplyEmpty(t *testing.T) {
	// Fresh install: not even the cell exists. Not an error, just zero entries.
	sandbox(t)
	got := Load()
	if len(got.Targets) != 0 || len(got.Rejected) != 0 {
		t.Errorf("a fresh install should be a clean empty table, got %+v / %+v", got.Targets, got.Rejected)
	}
}

func TestSaveRefusesToClobberSomeoneElsesWrite(t *testing.T) {
	// The CLI and web UI may edit at once (one typing `tunnel add`, one dragging fields).
	// Without a baseline check the later write **silently** clobbers the earlier - and the
	// two changes may be unrelated.
	sandbox(t)
	if err := Save([]Target{{ID: "a", Host: "h"}}); err != nil {
		t.Fatal(err)
	}
	stale := Load().Base

	// Someone else changed it once.
	if err := Save([]Target{{ID: "a", Host: "h"}, {ID: "b", Host: "h"}}); err != nil {
		t.Fatal(err)
	}

	// I write with the old baseline.
	_, err := save([]Target{{ID: "c", Host: "h"}}, stale)
	if err == nil {
		t.Fatal("writing with a stale baseline should fail")
	}
	var se *store.StaleError
	if !errorAs(err, &se) {
		t.Fatalf("it should be a StaleError, got %T: %v", err, err)
	}
	// Key: the disk copy was not overwritten.
	if got := Load(); len(got.Targets) != 2 {
		t.Errorf("on conflict the copy on disk was overwritten: %+v", got.Targets)
	}
}

func TestTheIDIsAURLSegment(t *testing.T) {
	// id goes straight into the URL. With `/`, /ui/remote/a/b/ points who-knows-where; with
	// `?#` the browser's URL parsing eats it.
	for _, id := range []string{"a/b", "a?b", "a#b", "a b", "a\tb", ""} {
		if why := validate(Target{ID: id, Host: "h"}.normalize()); why == "" {
			t.Errorf("id %q should be rejected", id)
		}
	}
	for _, id := range []string{"snode1", "a-b", "a_b", "a.b", "work-2"} {
		if why := validate(Target{ID: id, Host: "h"}.normalize()); why != "" {
			t.Errorf("id %q should be accepted, got: %s", id, why)
		}
	}
}

// ---------- what the UI writes back ----------

func TestApplyFromTheInterfaceReplacesTheWholeSet(t *testing.T) {
	// Same contract as config's applyProviders: the UI hands back **all records**; what
	// is missing was deleted. So "delete one" needs no second interface.
	sandbox(t)
	if err := Save([]Target{{ID: "old", Host: "h"}}); err != nil {
		t.Fatal(err)
	}
	base := Load().Base

	edit := `{"items":[
		{"id":"old","values":{"id":"old","label":"renamed","host":"10.0.0.1","user":"u",
		 "port":"2222","remote_port":"8899","local_port":"9401","persistent":"persistent",
		 "idle_seconds":"60","host_key":"accept-new"}},
		{"id":"","values":{"id":"new","host":"10.0.0.2"}}
	],"removed":[{"id":"gone"}]}`

	if _, err := applyTargets(json.RawMessage(edit), base); err != nil {
		t.Fatal(err)
	}
	got := Load()
	if len(got.Targets) != 2 {
		t.Fatalf("there should be two, got %+v (rejected: %+v)", got.Targets, got.Rejected)
	}
	byID := map[string]Target{}
	for _, t := range got.Targets {
		byID[t.ID] = t
	}
	old := byID["old"]
	if old.Label != "renamed" || old.Port != 2222 || !old.Persistent || old.HostKey != HostKeyAcceptNew {
		t.Errorf("the modified one did not land correctly: %+v", old)
	}
	if old.User != "u" || old.LocalPort != 9401 || old.IdleSeconds != 60 {
		t.Errorf("the modified one lost a few fields: %+v", old)
	}
	fresh := byID["new"]
	if fresh.Host != "10.0.0.2" || fresh.IdleSeconds != DefaultIdleSecs {
		t.Errorf("the newly added one did not land correctly: %+v", fresh)
	}
}

func TestApplyRefusesABadRowWhileTheUserIsLookingAtIt(t *testing.T) {
	// Refuse **at this moment**, do not store it and skip on the next read: the cell the
	// user just typed is what they are looking at; saying "no slash in id" now tells them where.
	sandbox(t)
	base := Load().Base
	edit := `{"items":[{"id":"","values":{"id":"a/b","host":"h"}}]}`
	if _, err := applyTargets(json.RawMessage(edit), base); err == nil {
		t.Fatal("a bad id should be rejected")
	} else if !strings.Contains(err.Error(), "path segment") {
		t.Errorf("the error should say it is an id problem: %v", err)
	}
	if got := Load(); len(got.Targets) != 0 {
		t.Errorf("after refusal nothing should be written: %+v", got.Targets)
	}
}

func TestApplyRefusesDuplicateIDsUpFront(t *testing.T) {
	sandbox(t)
	base := Load().Base
	edit := `{"items":[
		{"id":"","values":{"id":"dup","host":"h"}},
		{"id":"","values":{"id":"dup","host":"h2"}}
	]}`
	if _, err := applyTargets(json.RawMessage(edit), base); err == nil {
		t.Fatal("a duplicate name should be rejected")
	}
}

// ---------- tools ----------

func writeRaw(t *testing.T, body string) {
	t.Helper()
	st := store.LoadState()
	if st.ModuleConfig == nil {
		st.ModuleConfig = map[string][]byte{}
	}
	st.ModuleConfig[StateKey] = []byte(body)
	if err := store.SaveState(st); err != nil {
		t.Fatal(err)
	}
}

// errorAs is a thin wrap over errors.As, only to make the sites above read shorter.
func errorAs(err error, target **store.StaleError) bool {
	for err != nil {
		if se, ok := err.(*store.StaleError); ok {
			*target = se
			return true
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}
