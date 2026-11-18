package proto

import (
	"encoding/hex"
	"encoding/json"
	paths "github.com/rzbdz/newgate-ext/modules/config/paths"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// sandbox points NEWGATE_HOME at a temp dir and creates the subdirs.
//
// paths.Root() reads the env on every call, so after t.Setenv all path functions point
// at the sandbox at once - the same approach other tests use (testkit.Sandbox).
func sandbox(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("NEWGATE_HOME", home)
	if err := paths.EnsureDirs(); err != nil {
		t.Fatalf("creating the directory failed: %v", err)
	}
	return home
}

// writeLive writes a file into the config dir (to fake "local state").
func writeLive(t *testing.T, rel string, data []byte) {
	t.Helper()
	p := filepath.Join(paths.Config(), filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o2770); err != nil {
		t.Fatalf("creating the directory failed: %v", err)
	}
	if err := os.WriteFile(p, data, 0o660); err != nil {
		t.Fatalf("writing %s failed: %v", rel, err)
	}
}

func readLive(t *testing.T, rel string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(paths.Config(), filepath.FromSlash(rel)))
	if err != nil {
		t.Fatalf("reading %s failed: %v", rel, err)
	}
	return b
}

func existsLive(rel string) bool {
	_, err := os.Stat(filepath.Join(paths.Config(), filepath.FromSlash(rel)))
	return err == nil
}

func discardLogger() *log.Logger { return log.New(io.Discard, "", 0) }

// ---------- fake host ----------

// fakeHost is a minimal host endpoint.
//
// It only does "seal a snapshot into an envelope" - real host logic (building a snapshot
// from the config dir, maintaining generation, 404 for non-hosts) lives in commit 2's
// host.go, and these tests must not depend on it, or replica tests go red on host changes.
type fakeHost struct {
	srv    *httptest.Server
	encKey []byte
	token  []byte

	mu       sync.Mutex
	host     string
	cfgGen   int
	secGen   int
	files    map[string][]byte
	keys     map[string]string
	cfgMiss  bool // /__newgate/config returns 404 (simulates "the peer is not a host")
	authFail bool // returns 401 (simulates a different root key)
	// Serve these bytes directly without Seal. Used to build "tampered envelope" and
	// "garbage that will not open" - neither can be made by a sealing fake host.
	rawConfig  []byte
	rawSecrets []byte
	// Slow response: for "Stop interrupts an in-flight request"
	block chan struct{}
}

func newFakeHost(t *testing.T, root []byte) *fakeHost {
	t.Helper()
	encKey, token, err := DeriveKeys(root)
	if err != nil {
		t.Fatalf("deriving the key failed: %v", err)
	}
	h := &fakeHost{
		encKey: encKey,
		token:  token,
		host:   "0123456789abcdef0123456789abcdef",
		files:  map[string][]byte{},
		keys:   map[string]string{},
	}
	h.srv = httptest.NewServer(http.HandlerFunc(h.serve))
	t.Cleanup(h.srv.Close)
	return h
}

func (h *fakeHost) setConfig(gen int, files map[string][]byte) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.cfgGen, h.files = gen, files
}

func (h *fakeHost) setSecrets(gen int, keys map[string]string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.secGen, h.keys = gen, keys
}

// These setters take the lock: httptest's handler runs on **another** goroutine, so a
// bare read of h.host in a test is caught by -race (CI runs with -race).
func (h *fakeHost) setRawConfig(raw []byte) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.rawConfig = raw
}

func (h *fakeHost) setRawSecrets(raw []byte) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.rawSecrets = raw
}

func (h *fakeHost) hostID() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.host
}

func (h *fakeHost) setHostID(id string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.host = id
}

func (h *fakeHost) setNotFound(v bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.cfgMiss = v
}

func (h *fakeHost) serve(w http.ResponseWriter, r *http.Request) {
	if h.block != nil {
		<-h.block
	}
	h.mu.Lock()
	authFail, cfgMiss := h.authFail, h.cfgMiss
	wantAuth := "Bearer " + hex.EncodeToString(h.token)
	host, cfgGen, secGen := h.host, h.cfgGen, h.secGen
	files, keys := h.files, h.keys
	rawConfig, rawSecrets := h.rawConfig, h.rawSecrets
	h.mu.Unlock()

	if authFail || r.Header.Get("Authorization") != wantAuth {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	switch r.URL.Path {
	case PathConfig:
		if cfgMiss {
			http.Error(w, "not a host", http.StatusNotFound)
			return
		}
		if rawConfig != nil {
			_, _ = w.Write(rawConfig)
			return
		}
		plain, _ := json.Marshal(Snapshot{HostID: host, Generation: cfgGen, Files: files})
		env, err := Seal(h.encKey, KindConfig, host, cfgGen, plain)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeEnvelope(w, env)
	case PathSecrets:
		if cfgMiss {
			http.Error(w, "not a host", http.StatusNotFound)
			return
		}
		if rawSecrets != nil {
			_, _ = w.Write(rawSecrets)
			return
		}
		plain, _ := json.Marshal(Secrets{HostID: host, Generation: secGen, Keys: keys})
		env, err := Seal(h.encKey, KindSecrets, host, secGen, plain)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeEnvelope(w, env)
	default:
		http.Error(w, "no such path", http.StatusNotFound)
	}
}

func writeEnvelope(w http.ResponseWriter, env *Envelope) {
	raw, _ := json.Marshal(env)
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(raw)
}

// newTestClient builds a client aimed at the fake host.
//
// **Use srv.Client()**: its Transport has a nil Proxy, while http.DefaultClient honors
// HTTP_PROXY/HTTPS_PROXY - sessions running this repo's tests often ride newgate's
// proxy, so requests to the fake host would go through the real gateway and wreck the
// assertions (testing/upstream's comment records the same trap).
func newTestClient(t *testing.T, h *fakeHost, root []byte) *Client {
	t.Helper()
	c, err := New(Config{
		Endpoint: h.srv.URL,
		RootKey:  root,
		Version:  "build-17-test",
		Log:      discardLogger(),
		HTTP:     h.srv.Client(),
	})
	if err != nil {
		t.Fatalf("creating the client failed: %v", err)
	}
	return c
}

// validProviders is a providers.json that passes validation (two upstreams, keys via the channel).
func validProviders() []byte {
	return []byte(`{"providers":{
	  "smt-deepseek":{"base_url":"https://api.deepseek.com","api_key_env":"DS_KEY"},
	  "glm":{"base_url":"https://open.bigmodel.cn/api/paas/v4"}
	}}`)
}

func validProfile() []byte {
	return []byte(`{"name":"prod","roles":{"normal":[{"provider":"smt-deepseek","model":"deepseek-chat"}]}}`)
}

// metaFile builds a newgate-config.json.
func metaFile(schema int, minVersion string) []byte {
	raw, _ := json.Marshal(Meta{SchemaVersion: schema, MinNewgateVersion: minVersion})
	return raw
}

// snapshotFiles is a typical snapshot (providers + one tier + meta).
func snapshotFiles() map[string][]byte {
	return map[string][]byte{
		ProvidersName:        validProviders(),
		MetaFileName:         metaFile(SupportedSchema, ""),
		"mappings/prod.json": validProfile(),
	}
}
