package forward

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rzbdz/newgate-ext/modules/config/domain"
	"github.com/rzbdz/newgate-ext/modules/config/resolve"
	"github.com/rzbdz/newgate-ext/modules/gateway/metrics"
)

func TestCompactNotTightTimeout(t *testing.T) {
	sandboxState(t, `{"port": 0, "timeouts": {"classifier_first_byte_ms": 400, "first_byte_ms": 150000}}`)
	metrics.Default.Reset()
	t.Cleanup(func() { metrics.Default.Reset(); testChain = nil })

	var upstreamGot uint32
	hung := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.StoreUint32(&upstreamGot, 1)
		time.Sleep(3 * time.Second) // slow, but under the 150s standard ceiling
		_, _ = w.Write([]byte(`{"from":"hung-slow"}`))
	}))
	defer hung.Close()
	testChain = func(role string) []resolve.Step {
		return []resolve.Step{
			{Profile: "a", Binding: domain.Binding{Provider: "hung-prov", Model: "m1"}, Provider: testProvider(hung.URL)},
		}
	}

	srv := newTestServer()
	front := httptest.NewServer(http.HandlerFunc(srv.handleProxy))
	defer front.Close()

	// the client gives up after 1.2s (well past the tight 400ms timeout, well before the 150s cap)
	ctx, cancel := context.WithTimeout(context.Background(), 1200*time.Millisecond)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost,
		front.URL+"/a/claude/v1/messages", strings.NewReader(compactBody))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err == nil {
		resp.Body.Close()
	}

	if got := metrics.Default.Snapshot()["timeout.first_byte.non_stream"]; got != 0 {
		t.Fatalf("compact must not take the classifier's tight timeout, yet it timed out (counter=%d)", got)
	}
	if atomic.LoadUint32(&upstreamGot) == 0 {
		t.Fatal("the request did not reach the upstream")
	}
	// after the client disconnects the proxy goroutine needs a moment to handle cancellation --
	// poll until it has accounted for it
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if metrics.Default.Snapshot()["client.cancel"] == 1 {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("should be a client cancellation (counter=%d)", metrics.Default.Snapshot()["client.cancel"])
}

const compactBody = `{"model":"heavy","max_tokens":10240,` +
	`"system":[{"type":"text","text":"You are a conversation summarizer. Summarize this conversation."}],` +
	`"messages":[{"role":"user","content":"…transcript…"}]}`

func sandboxState(t *testing.T, stateJSON string) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("NEWGATE_HOME", dir)
	if err := os.MkdirAll(filepath.Join(dir, "mappings"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "providers.json"),
		[]byte(`{"providers":{}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "state.json"), []byte(stateJSON), 0o644); err != nil {
		t.Fatal(err)
	}
}

// classifierBody replicates the shape of the Bash permission classifier (observed: non-streaming
// + a system that self-reports "You are a security monitor").
const classifierBody = `{"model":"mid","max_tokens":2112,` +
	`"system":[{"type":"text","text":"You are a security monitor for autonomous AI coding agents."}],` +
	`"messages":[{"role":"user","content":"classify"}]}`

	// compactBody replicates the shape of a /compact summary request (non-streaming, **no**
	// classifier marker -- large input, does not block interaction, must not eat the tight timeout).
