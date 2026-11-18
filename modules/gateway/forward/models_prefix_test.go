package forward

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// The identity-prefixed /models must give the same list as the bare path. opencode's baseURL
// carries `/a/opencode` since 2026-10-29 (it was missing before; see buildNewgateProvider in
// the distribution's opencodeomo), so its GET /models no longer matches "/v1/models" on the mux.
func TestModelsUnderAgentPrefix(t *testing.T) {
	isolate(t)
	s := newTestServer()
	for _, path := range []string{"/v1/models", "/a/opencode/v1/models", "/a/opencode/p/ds/v1/models"} {
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: status %d, body %q", path, rec.Code, rec.Body.String())
		}
		var doc struct {
			Object string `json:"object"`
			Data   []struct {
				ID string `json:"id"`
			} `json:"data"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &doc); err != nil || doc.Object != "list" || len(doc.Data) == 0 {
			t.Fatalf("%s: not a model list: %q (%v)", path, rec.Body.String(), err)
		}
	}
}
