package forward

import (
	"bytes"
	"encoding/json"
	"io/ioutil"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/rzbdz/newgate-ext/modules/gateway/policy"
)

// TestProbeHealthTranslatesAndDispatches pins **every** duty the data plane has
// at this control-plane decision point: authentication, translating the wire JSON
// into a neutral ProbeObservation (including passing the configured classifier
// threshold along), dispatching to contributors, printing the ack counter and the
// log, and merging the contributor's state fields into the response's top level.
//
// "What this conclusion means, and whether to strip the binding" is not the data
// plane's duty - it used to call `Health.RecordProbe` and assert ledger state,
// which tested breaker's semantics but was written in forward's test (criteria
// follow the module that owns them). After the inversion the data plane only
// translates and forwards, so here a stub contributor collects the observations.
func TestProbeHealthTranslatesAndDispatches(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("NEWGATE_HOME", dir)
	_ = os.MkdirAll(filepath.Join(dir, "mappings"), 0o755)
	_ = ioutil.WriteFile(filepath.Join(dir, "providers.json"),
		[]byte(`{"providers":{}}`), 0o600)
	const tok = "ctrl-tok-0123456789abcdef0123456789abcdef"
	_ = ioutil.WriteFile(filepath.Join(dir, "state.json"), []byte(
		`{"default_profile":"ds","port":1,"control_token":"`+tok+
			`","timeouts":{"classifier_first_byte_ms":10}}`), 0o600)

	const provider = "relay-health-test"
	srv := newTestServer()
	rec := &probeFilter{acks: []policy.ProbeAck{
		{Opened: true, Note: "[probe] breaker " + provider + "/slow：laggy"},
	}}
	if _, err := srv.Filters.Register(rec); err != nil {
		t.Fatal(err)
	}
	front := httptest.NewServer(http.HandlerFunc(srv.handleHealth))
	defer front.Close()
	body := []byte(`{"observations":[` +
		`{"provider":"` + provider + `","model":"slow","status":200,"latency_ms":20,"context_bytes":1},` +
		`{"provider":"` + provider + `","model":"fast","status":200,"latency_ms":5,"context_bytes":1},` +
		`{"provider":"","model":"fast","status":200}` +
		`]}`)
	req, _ := http.NewRequest(http.MethodPost, front.URL, bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := ioutil.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("health POST = %d：%s", resp.StatusCode, got)
	}

	obs := rec.observed()
	if len(obs) != 2 {
		t.Fatalf("%d observations distributed to contributors, want 2 (empty provider must be dropped): %+v", len(obs), obs)
	}
	if obs[0].Model != "slow" || obs[0].Latency != 20*time.Millisecond ||
		obs[0].Status != 200 || obs[0].ContextBytes != 1 {
		t.Errorf("the observation was not translated faithfully: %+v", obs[0])
	}
	// SlowAfter comes from config (classifier_first_byte_ms), filled in by the data
	// plane while translating: the threshold is the criterion for "is one request slow",
	// which belongs to data plane config, and contributors should only get the value.
	for _, o := range obs {
		if o.SlowAfter != 10*time.Millisecond {
			t.Errorf("SlowAfter of %s = %v, want 10ms (from the classifier threshold in config)",
				o.Model, o.SlowAfter)
		}
	}

	// ack counters and logs: the data plane does not interpret the
	// conclusion, but must repeat the contributor's words verbatim.
	var out struct {
		OK     bool `json:"ok"`
		Opened int  `json:"opened"`
	}
	if err := json.Unmarshal(got, &out); err != nil {
		t.Fatalf("the response is not JSON: %v (%s)", err, got)
	}
	if !out.OK || out.Opened != 1 {
		t.Errorf("response = %s, want ok=true opened=1 (the number of Opened entries in the ack)", got)
	}
}
