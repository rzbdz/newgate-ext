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
	// 改一个 **core 自己的** typed 字段再存：模块段必须原样活下来，而 core 字段
	// 只从 struct 出。2026-09-18 之前这里改的是 state.Debug——那三个网关开关
	// 搬去 ModuleConfig["gateway"] 之后就不再是 core 字段了（见
	// modules/gateway/gatewaystate），改用 Port。
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
		t.Fatalf("core 字段没写出来:\n%s", saved)
	}
	// 存回去之后，core 字段不该跑进模块段（那是「core 认识了这个键」的反面）。
	if _, leaked := LoadState().ModuleConfig["port"]; leaked {
		t.Fatal("core 字段在往返之后漏进了模块段")
	}
}
