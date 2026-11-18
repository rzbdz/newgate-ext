package store

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestStatePreservesModuleTopLevelConfig(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("NEWGATE_HOME", dir)
	if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, "state.json")), 0o770); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "state.json")
	original := []byte(`{
  "default_profile": "default",
  "classifier_override": {"provider":"fast","model":"small"}
}`)
	if err := os.WriteFile(path, original, 0o660); err != nil {
		t.Fatal(err)
	}

	state := LoadState()
	if !bytes.Contains(state.ModuleConfig["classifier_override"], []byte(`"fast"`)) {
		t.Fatalf("module field not captured: %s", state.ModuleConfig["classifier_override"])
	}
	if _, leaked := state.ModuleConfig["default_profile"]; leaked {
		t.Fatal("core-owned state field leaked into module config")
	}
	// Change a **core's own** typed field and save again: the module section must survive as is,
	// and core fields only come out of the struct. Before 2026-09-18 what was changed here was
	// state.Debug -- after those three gateway switches moved to ModuleConfig["gateway"] they
	// are no longer core fields (see modules/gateway/gatewaystate), so Port is used instead.
	state.Port = 9999
	if err := SaveState(state); err != nil {
		t.Fatal(err)
	}
	saved, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(saved, []byte(`"classifier_override"`)) ||
		!bytes.Contains(saved, []byte(`"fast"`)) {
		t.Fatalf("module field lost after state save:\n%s", saved)
	}
	if !bytes.Contains(saved, []byte(`"port": 9999`)) {
		t.Fatalf("the core field was not written:\n%s", saved)
	}
	// After saving back, core fields should not run into the module
	// section (that is the opposite of "core recognized this key").
	if _, leaked := LoadState().ModuleConfig["port"]; leaked {
		t.Fatal("the core field leaked into the module section after a round trip")
	}
}
