package selfupdate

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"testing"

	"github.com/rzbdz/newgate-ext/modules/config/paths"
	"github.com/rzbdz/newgate-ext/testing/testkit"

	upgradeapi "github.com/rzbdz/newgate-ext/modules/upgrade"
	webapi "github.com/rzbdz/newgate-ext/modules/web-dashboard"
)

func ext(t *testing.T, w *webState, action string) statusDoc {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"action": action})
	out, err := w.handle(webapi.ExtensionRequest{Context: context.Background(), Body: body})
	if err != nil {
		t.Fatal(err)
	}
	return out.(statusDoc)
}

// The web side: status stays offline and asks for a check the first time; the check's
// verdict is remembered and later statuses carry it; failures are recorded too, with the reason.
func TestWebStatusAndCheck(t *testing.T) {
	testkit.Sandbox(t)
	if err := paths.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	w := newWebState()
	calls := 0
	w.check = func(context.Context) (upgradeapi.Result, error) {
		calls++
		if calls == 2 {
			return upgradeapi.Result{}, errors.New("cannot reach GitHub")
		}
		return upgradeapi.Done("v0.9.0 is available"), nil
	}

	s := ext(t, w, "status")
	if calls != 0 || !s.CheckDue || s.Last != nil || s.Labels["check"] == "" {
		t.Fatalf("status: %+v (calls %d)", s, calls)
	}
	s = ext(t, w, "check")
	if s.Last == nil || !s.Last.Available || s.Last.Message != "v0.9.0 is available" || s.CheckDue {
		t.Fatalf("check: %+v", s.Last)
	}
	if s = ext(t, w, "status"); s.Last == nil || !s.Last.Available || calls != 1 {
		t.Fatalf("status after check: %+v (calls %d)", s.Last, calls)
	}
	if s = ext(t, w, "check"); s.Last == nil || s.Last.Error == "" || s.Last.Available {
		t.Fatalf("failed check: %+v", s.Last)
	}
	if _, err := w.handle(webapi.ExtensionRequest{Context: context.Background(), Body: []byte(`{"action":"install"}`)}); err == nil {
		t.Fatal("the web side must not install anything")
	}
}

func TestPluginEntryIsEmbedded(t *testing.T) {
	if _, err := fs.ReadFile(webAssets(), "plugin.js"); err != nil {
		t.Fatal(err)
	}
}

// Installed together with web-dashboard: the plugin's registration checks (entry exists,
// slot accepted) run in Start; any failure is red here, not a missing button a user finds.
func TestModuleStartsWithTheWebDashboard(t *testing.T) {
	testkit.Sandbox(t)
	g := testkit.Start(t, webapi.New(), New())
	g.Stop()
}
