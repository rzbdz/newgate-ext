package forward

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// 带身份前缀的 /models 必须与裸路径给出同一份列表。opencode 的 baseURL 从
// 2026-10-29 起带 `/a/opencode`（之前漏了，见发行版 opencodeomo 的
// buildNewgateProvider），它的 GET /models 就不再命中 mux 上的 "/v1/models"。
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
