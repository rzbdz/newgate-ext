package forward

import (
	"encoding/json"
	"io"
	"io/ioutil"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rzbdz/newgate-ext/modules/config/domain"
	"github.com/rzbdz/newgate-ext/modules/config/resolve"
)

func TestCompactKeepsExplicitThinking(t *testing.T) {
	isolate(t) // 不隔离就会读到线上 state.json：裸奔一开这里必红
	var sent []byte
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sent, _ = ioutil.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	defer up.Close()

	testChain = func(role string) []resolve.Step {
		return []resolve.Step{{Profile: "test",
			Binding:  domain.Binding{Provider: "glm", Model: "glm-5.3"},
			Provider: testProvider(up.URL)}}
	}
	defer func() { testChain = nil }()

	srv := newTestServer()
	front := httptest.NewServer(http.HandlerFunc(srv.handleProxy))
	defer front.Close()

	// /a/claude/ makes Agent=claude (claude-bg only accepts this agent;
	// compacts in dumps are non-streaming calls started by claude).
	compact := `{"model":"heavy","stream":false,"max_tokens":32000,` +
		`"thinking":{"type":"adaptive"},` +
		`"tools":[{"name":"Bash","description":"run a command","input_schema":{"type":"object"}}],` +
		`"messages":[{"role":"user","content":"summarize this long transcript"}]}`
	resp, err := http.Post(front.URL+"/a/claude/v1/messages", "application/json",
		strings.NewReader(compact))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		b, _ := ioutil.ReadAll(resp.Body)
		t.Fatalf("status %d: %s", resp.StatusCode, b)
	}
	if len(sent) == 0 {
		t.Fatal("the upstream received no request body")
	}
	var got struct {
		Thinking struct {
			Type string `json:"type"`
		} `json:"thinking"`
		Tools []json.RawMessage `json:"tools"`
	}
	if err := json.Unmarshal(sent, &got); err != nil {
		t.Fatalf("what was sent is not valid JSON: %v\n%s", err, sent)
	}
	if got.Thinking.Type != "adaptive" {
		t.Errorf("compact with an explicit thinking:adaptive must not be forced to disabled, got %q\n%s",
			got.Thinking.Type, sent)
	}
	if len(got.Tools) == 0 {
		t.Errorf("tools must not be stripped: %s", sent)
	}
}

// TestBackgroundCallStillDisablesThinking is the control group: a background call (classifier
// / title-generation shape) that really wrote no thinking still gets `disabled` added -
// the fix targets "forced explicit intent", not giving up the "disable thinking" intent.
