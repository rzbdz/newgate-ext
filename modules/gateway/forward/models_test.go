package forward

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rzbdz/newgate-ext/modules/gateway/special"
)

// listerFixture 替 `fixture` 这个客户端答 /models：钉住 profile 时多列一个具体模型。
type listerFixture struct{}

func (listerFixture) Name() string                { return "lister-fixture" }
func (listerFixture) Why() string                 { return "ModelLister 的测试夹具" }
func (listerFixture) Before() []string            { return nil }
func (listerFixture) After() []string             { return nil }
func (listerFixture) Match(*special.Request) bool { return false }
func (listerFixture) Apply(body []byte, _ *special.Request) ([]byte, []string, error) {
	return body, nil, nil
}
func (listerFixture) ListModels(agent, profile string) ([]string, bool) {
	if agent != "fixture" {
		return nil, false
	}
	if profile == "" {
		return []string{"normal"}, true
	}
	return []string{"normal", "prov/" + profile + "-model"}, true
}

func modelIDs(t *testing.T, url string) []string {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var body struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, m := range body.Data {
		out = append(out, m.ID)
	}
	return out
}

// TestModelListerAnswersForItsOwnClient 客户端模块替自己的 URL 答 /models（钉住
// profile 时列那份 profile 的具体模型）；别的客户端、裸路径仍拿缺省清单。
func TestModelListerAnswersForItsOwnClient(t *testing.T) {
	restore := special.InstallDefault(special.NewRegistry())
	defer restore()
	special.Register(listerFixture{})

	srv := newTestServer()
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/models", srv.handleModels)
	mux.HandleFunc("/", srv.handleProxy)
	front := httptest.NewServer(mux)
	defer front.Close()

	if got := strings.Join(modelIDs(t, front.URL+"/a/fixture/v1/models"), ","); got != "normal" {
		t.Errorf("跟着走的那一行 = %s", got)
	}
	if got := strings.Join(modelIDs(t, front.URL+"/a/fixture/p/ds/v1/models"), ","); got != "normal,prov/ds-model" {
		t.Errorf("钉住 ds 的那一行 = %s", got)
	}
	other := modelIDs(t, front.URL+"/a/claude/v1/models")
	bare := modelIDs(t, front.URL+"/v1/models")
	if len(other) < 5 || other[0] != "heavy" || strings.Join(other, ",") != strings.Join(bare, ",") {
		t.Errorf("别的客户端与裸路径拿缺省清单：%v / %v", other, bare)
	}
}
