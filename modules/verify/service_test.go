package verify

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	lmfpd "github.com/Ikaleio/lm-detector/sdk/go"
	"github.com/rzbdz/newgate/modules/config/domain"
	"github.com/rzbdz/newgate/modules/config/paths"
	"github.com/rzbdz/newgate/modules/config/store"
	"github.com/rzbdz/newgate/testing/testkit"
)

type fakeDetector struct {
	res       *lmfpd.DetectResult
	build     string
	api       string
	err       error
	calls     int
	refreshed int
}

func (f *fakeDetector) Detect(context.Context, domain.Provider, string, string, int) (*lmfpd.DetectResult, string, string, error) {
	f.calls++
	return f.res, f.build, f.api, f.err
}

func (f *fakeDetector) Loaded() (string, bool) { return f.build, f.res != nil }

func (f *fakeDetector) Refresh(context.Context) (string, error) {
	f.refreshed++
	return "fingerprint reference data is now test", nil
}

func seedConfig(t *testing.T) {
	t.Helper()
	testkit.Sandbox(t)
	if err := paths.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveProviders(&domain.Providers{Providers: map[string]domain.Provider{
		"relay": {BaseURL: "https://relay.example/v1", APIKey: "sk-top-secret", Protocol: "openai"},
	}}); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveProfile(&domain.Profile{Name: "main", Roles: map[string]domain.Candidates{
		"normal": {{Provider: "relay", Model: "model-x"}},
		"light":  {{Provider: "relay", Model: "model-x"}},
	}}); err != nil {
		t.Fatal(err)
	}
}

func scored() *lmfpd.DetectResult {
	return &lmfpd.DetectResult{Analysis: &lmfpd.Analysis{
		Decision: "scored", Prediction: "model-x", PredictionName: "Model X",
		FamilyPrediction: "relay", FamilyPredictionName: "Relay",
		Probability: ptr(0.93), UsedOutputs: 3,
		Results: []lmfpd.AnalysisResult{
			{Model: "model-x", DisplayName: "Model X", Family: "relay", Score: 9.1, Probability: ptr(0.93)},
			{Model: "model-y", DisplayName: "Model Y", Family: "relay", Score: 1.2, Probability: ptr(0.07)},
		},
	}}
}

func TestRunBuildsEntryAndCachesIt(t *testing.T) {
	seedConfig(t)
	svc := newService()
	svc.det = &fakeDetector{res: scored(), build: "built-1", api: "chat_completions"}

	e, err := svc.Run(context.Background(), RunRequest{Provider: "relay", Model: "model-x"})
	if err != nil {
		t.Fatal(err)
	}
	if e.Match != MatchOK || e.Predicted != "model-x" || e.API != "chat_completions" {
		t.Fatalf("entry = %+v", e)
	}
	if e.Build != "built-1" || len(e.Results) != 2 {
		t.Fatalf("entry = %+v", e)
	}
	if e.Probability == nil || *e.Probability != 0.93 {
		t.Fatalf("probability = %v", e.Probability)
	}
	// 落盘之后另一个实例读得到（界面与 CLI 是两个进程）。
	got, ok, err := newCacheStore().get("relay", "model-x")
	if err != nil || !ok {
		t.Fatalf("cache get: ok=%v err=%v", ok, err)
	}
	if got.Predicted != "model-x" {
		t.Fatalf("cached = %+v", got)
	}
}

func TestRunReportsMismatchWithoutFailing(t *testing.T) {
	seedConfig(t)
	svc := newService()
	res := scored()
	res.Analysis.Prediction = "gpt-6-luna"
	res.Analysis.FamilyPrediction = "gpt"
	svc.det = &fakeDetector{res: res}

	e, err := svc.Run(context.Background(), RunRequest{Provider: "relay", Model: "model-x"})
	if err != nil {
		t.Fatal(err)
	}
	if e.Match != MatchMismatch || !e.Match.Warning() {
		t.Fatalf("match = %q, want mismatch", e.Match)
	}
	if e.Error != "" {
		t.Fatalf("a mismatch is a finding, not a request failure: %q", e.Error)
	}
}

func TestRunDetectionFailureIsAnEntry(t *testing.T) {
	seedConfig(t)
	svc := newService()
	msg := "No valid samples. This round cannot be scored."
	svc.det = &fakeDetector{res: &lmfpd.DetectResult{Error: &msg}}

	e, err := svc.Run(context.Background(), RunRequest{Provider: "relay", Model: "model-x"})
	if err != nil {
		t.Fatalf("a failed round should be reported on the entry, not as a handler error: %v", err)
	}
	if e.Match != MatchUnknown || e.Error == "" {
		t.Fatalf("entry = %+v", e)
	}
}

func TestRunRejectsUnknownProviderAndBadCount(t *testing.T) {
	seedConfig(t)
	svc := newService()
	svc.det = &fakeDetector{res: scored()}
	if _, err := svc.Run(context.Background(), RunRequest{Provider: "ghost", Model: "m"}); err == nil {
		t.Fatal("unknown provider should error")
	}
	if _, err := svc.Run(context.Background(), RunRequest{Provider: "relay", Model: "m", Count: 4}); err == nil {
		t.Fatal("count 4 should error")
	}
}

func TestStateNeverLeaksKeys(t *testing.T) {
	seedConfig(t)
	svc := newService()
	svc.det = &fakeDetector{res: scored(), build: "built-1"}
	st := svc.State()
	if st.Error != "" {
		t.Fatalf("state error: %s", st.Error)
	}
	if len(st.Profiles) != 1 || st.Profiles[0].Name != "main" {
		t.Fatalf("profiles = %+v", st.Profiles)
	}
	if len(st.Profiles[0].Roles) != 2 {
		t.Fatalf("roles = %+v, want only the two bound tiers (unbound ones are normal, not noise)",
			st.Profiles[0].Roles)
	}
	if len(st.Providers) != 1 || !st.Providers[0].HasKey {
		t.Fatalf("providers = %+v", st.Providers)
	}
	raw, err := json.Marshal(st)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "sk-top-secret") {
		t.Fatal("the provider key leaked into the interface payload")
	}
}

func TestUpgradeRefreshesReferenceData(t *testing.T) {
	seedConfig(t)
	svc := newService()
	fake := &fakeDetector{res: scored()}
	svc.det = fake
	msg, err := svc.Upgrade(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if fake.refreshed != 1 || msg == "" {
		t.Fatalf("refresh calls = %d, message = %q", fake.refreshed, msg)
	}
}

func TestEndpointFor(t *testing.T) {
	base := domain.Provider{BaseURL: "https://relay.example/v1", APIKey: "k"}
	got, api, name, err := endpointFor(base, "auto")
	if err != nil || got != "https://relay.example/v1" || api != lmfpd.APIChatCompletions || name != "chat_completions" {
		t.Fatalf("openai auto = %q %q %q %v", got, api, name, err)
	}
	got, api, name, err = endpointFor(base, "responses")
	if err != nil || got != "https://relay.example/v1" || api != lmfpd.APIResponses || name != "responses" {
		t.Fatalf("responses = %q %q %q %v", got, api, name, err)
	}

	// Anthropic 方言分 base：newgate 的惯例是 anthropic_url 不含 /v1，URL()
	// 补上 /v1 再拼 /messages；交给 SDK 时要把它已经补好的那一段留下。
	anth := domain.Provider{
		BaseURL: "https://relay.example/api/coding/v3", APIKey: "k",
		Protocol: "anthropic", AnthropicURL: "https://relay.example/api/coding",
	}
	got, api, name, err = endpointFor(anth, "auto")
	if err != nil || got != "https://relay.example/api/coding/v1" || api != lmfpd.APIMessages || name != "messages" {
		t.Fatalf("anthropic auto = %q %q %q %v", got, api, name, err)
	}

	if _, _, _, err := endpointFor(domain.Provider{BaseURL: "https://x/v1"}, "auto"); err == nil {
		t.Fatal("a provider without a key should error")
	}
	if _, _, _, err := endpointFor(base, "nope"); err == nil {
		t.Fatal("an unknown api should error")
	}
}
