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

// TestProbeHealthTranslatesAndDispatches 锁住数据面在控制面这个决策点上的
// **全部**义务：鉴权、把线上的 JSON 翻成中性的 ProbeObservation（含把配置里的
// classifier 阈值一起递过去）、分发给贡献者、把 ack 计数与日志打出来、以及把
// 贡献者的状态字段并进响应顶层。
//
// 「这条结论意味着什么、要不要摘牌」不在数据面的义务里——以前它调
// `Health.RecordProbe` 并断言账本状态，那是在验 breaker 的语义，却写在
// forward 的测试里（判据要跟着知道它的模块走）。倒置之后数据面只负责翻译与
// 转发，于是这里用一个桩贡献者收观察值。
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
		{Opened: true, Note: "[probe] 熔断 " + provider + "/slow：laggy"},
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
		t.Fatalf("分发给贡献者的观察值有 %d 条, want 2（provider 为空的要丢掉）: %+v", len(obs), obs)
	}
	if obs[0].Model != "slow" || obs[0].Latency != 20*time.Millisecond ||
		obs[0].Status != 200 || obs[0].ContextBytes != 1 {
		t.Errorf("观察值没被如实翻译: %+v", obs[0])
	}
	// SlowAfter 来自配置（classifier_first_byte_ms），由数据面在翻译时填上：
	// 阈值是「一次请求算不算慢」的判据，属于数据面的配置，贡献者只该拿到值。
	for _, o := range obs {
		if o.SlowAfter != 10*time.Millisecond {
			t.Errorf("%s 的 SlowAfter = %v, want 10ms（来自配置的 classifier 阈值）",
				o.Model, o.SlowAfter)
		}
	}

	// ack 的计数与日志：数据面不解释结论，但必须把贡献者的话原样说出来。
	var out struct {
		OK     bool `json:"ok"`
		Opened int  `json:"opened"`
	}
	if err := json.Unmarshal(got, &out); err != nil {
		t.Fatalf("响应不是 JSON: %v（%s）", err, got)
	}
	if !out.OK || out.Opened != 1 {
		t.Errorf("响应 = %s, want ok=true opened=1（ack 里 Opened 的条数）", got)
	}
}
