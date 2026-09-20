package claudecode

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/rzbdz/newgate/modules/config/domain"
	"github.com/rzbdz/newgate/modules/gateway/special"
	thinkingmodule "github.com/rzbdz/newgate/modules/thinking"
)

type testThinking struct{}

func (testThinking) BestEffortDisable(body []byte, request *special.Request) ([]byte, []string, error) {
	return thinkingmodule.BestEffortDisableThink(body, request)
}

func TestBackgroundIsClientLocal(t *testing.T) {
	plugin := background{}
	for _, test := range []struct {
		agent  string
		stream bool
		want   bool
	}{
		{ID, false, true},
		{ID, true, false},
		{"opencode", false, false},
		{"", false, false},
	} {
		if got := plugin.Match(&special.Request{Agent: test.agent, Stream: test.stream}); got != test.want {
			t.Fatalf("Match(%q, %v) = %v, want %v", test.agent, test.stream, got, test.want)
		}
	}
}

func TestBackgroundDisablesImplicitThinking(t *testing.T) {
	body := []byte(`{"model":"mid","messages":[{"role":"user","content":"classify"}]}`)
	out, notes, err := (background{thinking: testThinking{}}).Apply(body, &special.Request{
		Agent: ID, Model: "mid",
	})
	if err != nil {
		t.Fatal(err)
	}
	var root map[string]interface{}
	if err := json.Unmarshal(out, &root); err != nil {
		t.Fatal(err)
	}
	thinking, _ := root["thinking"].(map[string]interface{})
	if thinking["type"] != "disabled" || len(notes) == 0 {
		t.Fatalf("thinking=%v notes=%v", thinking, notes)
	}
}

func TestBackgroundOwnsRouteAndObservability(t *testing.T) {
	body := []byte(`{"system":"You are a security monitor for autonomous AI coding agents"}`)
	override := domain.Binding{Provider: "provider", Model: "fast-model"}
	raw, _ := json.Marshal(override)
	state := &domain.State{ModuleConfig: map[string][]byte{"classifier_override": raw}}
	decision, ok := (background{}).Route(body, &special.Request{Agent: ID}, state)
	if !ok || decision.Tier != "light" || decision.Head == nil || *decision.Head != override {
		t.Fatalf("decision=%+v matched=%v", decision, ok)
	}
	items := (background{}).Status(state)
	if len(items) != 1 || !strings.Contains(items[0].Value, override.String()) {
		t.Fatalf("status=%+v", items)
	}
	if len((background{}).Metrics()) == 0 || len((background{}).Bindings(state)) != 1 {
		t.Fatal("module did not publish metric/binding capabilities")
	}
}
