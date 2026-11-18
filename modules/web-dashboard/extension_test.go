package webdashboard

import (
	"encoding/json"
	"net/http"
	"testing"
)

// Extension endpoints are the BFF's **second door**: the concept ledger owns "things you
// can draw", this one "a click does something". Knows no module, only registered names.
//
// These pin that sentence: the BFF does shape only, not business.

func registerExtension(t *testing.T, h *Handler, name string, run ExtensionHandler) func() error {
	t.Helper()
	rel, err := h.extensions.Register(name, run)
	if err != nil {
		t.Fatalf("registering %s: %v", name, err)
	}
	return rel
}

func TestExtensionForwardsTheRequestOpaque(t *testing.T) {
	h := newHandler()
	var got ExtensionRequest
	registerExtension(t, h, "echo", func(r ExtensionRequest) (any, error) {
		got = r
		return map[string]any{"ok": true, "body": string(r.Body)}, nil
	})

	rec := post(t, h, "/api/ext/echo?x=1", `{"provider":"p","refresh":true}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("the extension should answer 200, actual %d: %s", rec.Code, rec.Body)
	}
	if got.Method != http.MethodPost {
		t.Errorf("the extension should see the original request method, actual %q", got.Method)
	}
	if got.Query.Get("x") != "1" {
		t.Errorf("the query string should be passed down as is, actual %v", got.Query)
	}
	if string(got.Body) != `{"provider":"p","refresh":true}` {
		t.Errorf("the request body should be passed down **verbatim** (the BFF does not parse it), actual %s", got.Body)
	}
	var doc map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
		t.Fatalf("the response is not JSON: %v", err)
	}
	if doc["ok"] != true {
		t.Errorf("the response should serve the extension's return value as is, actual %v", doc)
	}
}

func TestExtensionErrorsAndGates(t *testing.T) {
	h := newHandler()
	registerExtension(t, h, "boom", func(ExtensionRequest) (any, error) {
		return nil, errString("the provider is unreachable")
	})

	cases := []struct {
		name string
		path string
		body string
		want int
	}{
		{"unregistered name", "/api/ext/nope", `{}`, http.StatusNotFound},
		{"empty name", "/api/ext/", `{}`, http.StatusNotFound},
		{"name containing a slash", "/api/ext/a/b", `{}`, http.StatusNotFound},
		{"the extension errors itself", "/api/ext/boom", `{}`, http.StatusBadRequest},
		{"bad JSON", "/api/ext/boom", `{`, http.StatusBadRequest},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := post(t, h, c.path, c.body)
			if rec.Code != c.want {
				t.Errorf("%s should be %d, actual %d: %s", c.path, c.want, rec.Code, rec.Body)
			}
		})
	}

	// GET is not this door's shape: extensions touch upstreams and change runtime state;
	// nothing justifies it being looser than saving.
	rec := get(t, h, "/api/ext/boom")
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET should be 405, actual %d", rec.Code)
	}
	if allow := rec.Header().Get("Allow"); allow != http.MethodPost {
		t.Errorf("405 should carry Allow: POST, actual %q", allow)
	}

	// Same gate as apply: a cross-site form can send text/plain, not application/json.
	rec = raw(t, h, http.MethodPost, "/api/ext/boom", `{}`, loopback, "text/plain")
	if rec.Code != http.StatusUnsupportedMediaType {
		t.Errorf("without declaring application/json it should be 415, actual %d", rec.Code)
	}
}

func TestExtensionRegistrationRejectsCollisions(t *testing.T) {
	h := newHandler()
	rel := registerExtension(t, h, "one", func(ExtensionRequest) (any, error) { return nil, nil })

	if _, err := h.extensions.Register("one", func(ExtensionRequest) (any, error) { return nil, nil }); err == nil {
		t.Fatal("a duplicate name should error on the spot -- otherwise only the one registered first receives requests and the other silently fails")
	}
	if _, err := h.extensions.Register("Bad Name", func(ExtensionRequest) (any, error) { return nil, nil }); err == nil {
		t.Fatal("spaces/uppercase in a name should be rejected (it goes into a URL path segment)")
	}
	if _, err := h.extensions.Register("nil", nil); err == nil {
		t.Fatal("a nil handler must be rejected")
	}

	// After undo the name must really vanish: the UI may still hold a snapshot, so its next
	// click gets 404, not a stopped module still hanging there.
	if err := rel(); err != nil {
		t.Fatal(err)
	}
	if rec := post(t, h, "/api/ext/one", `{}`); rec.Code != http.StatusNotFound {
		t.Errorf("after revoke it should be 404, actual %d", rec.Code)
	}
}

// errString is an error stand-in with only Error(): the test cares not about the type,
// only that the sentence reaches the UI verbatim.
type errString string

func (e errString) Error() string { return string(e) }
