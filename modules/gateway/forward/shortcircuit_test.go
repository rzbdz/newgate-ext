package forward

import (
	"io"
	"io/ioutil"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rzbdz/newgate-ext/modules/config/domain"
	"github.com/rzbdz/newgate-ext/modules/config/resolve"
	"github.com/rzbdz/newgate-ext/modules/gateway/special"
)

// nakedFixture 是一个**短路器**：它替上游回答，一个字节都不发出去。
//
// 为什么测试里自己写一个而不是 import 真正的裸奔模块：那个模块在发行版仓库里，
// 而这里锁的是**热路径**对短路的处置（响应头怎么回显、日志怎么写），不是裸奔
// 自己的判据。判据的真值表在它自己的测试里——与 shapeTestDetector 那条分层规矩同。
type nakedFixture struct{}

func (nakedFixture) Name() string                { return "naked-fixture" }
func (nakedFixture) Why() string                 { return "short-circuit test fixture" }
func (nakedFixture) Before() []string            { return nil }
func (nakedFixture) After() []string             { return nil }
func (nakedFixture) Match(*special.Request) bool { return false }

func (nakedFixture) Apply(body []byte, _ *special.Request) ([]byte, []string, error) {
	return body, nil, nil
}

func (nakedFixture) Respond([]byte, *special.Request, *domain.State) ([]byte, bool) {
	return []byte(`{"naked":true}`), true
}

// TestShortCircuitEchoesTheProfile 锁短路那一发的 profile 回显。
//
// 现场（2026-09-23 用户报的「裸奔功能似乎对 claude 指定 profile 版本无效」）：
// 短路发生在构链**之前**——这是设计如此，替上游回答的插件根本不需要链——但
// 响应头这里写死了一个 `X-Newgate-Profile: n/a`。于是 `/a/claude/p/ds/...`
// 被短路的那些请求回来看见 `n/a`，看着像「我点的 profile 丢了」，而真正的
// 事实是「这一发压根没解析到链」。
//
// 判据：短路口算出来的 `active`（/p/<profile> 覆盖之后的那个值）必须回显；
// 一份 profile 都没有（骨架装配）时退回 `n/a`——那才是 `n/a` 本来该表示的
// 意思，写死它等于把「没有 profile」和「短路」两个不同的事实混成一个。
func TestShortCircuitEchoesTheProfile(t *testing.T) {
	var upstreamHits int
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamHits++
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	defer up.Close()

	testChain = func(role string) []resolve.Step {
		return []resolve.Step{{Profile: "test",
			Binding:  domain.Binding{Provider: "anth", Model: "claude-sonnet-4"},
			Provider: testProvider(up.URL)}}
	}
	defer func() { testChain = nil }()

	// 短路器注册进一张**独立**注册表：包级那份是整张真图装出来的，往里面加
	// 东西会泄漏给同包其他用例（而且顺序图会跟着变）。
	restore := special.InstallDefault(special.NewRegistry())
	defer restore()
	special.Register(nakedFixture{})

	srv := newTestServer()
	front := httptest.NewServer(http.HandlerFunc(srv.handleProxy))
	defer front.Close()

	// 显式覆盖：/p/naked-profile/ ——短路该把这个值回显出来。
	resp, err := http.Post(front.URL+"/a/claude/p/naked-profile/v1/messages", "application/json",
		strings.NewReader(`{"model":"newgate/normal","stream":false,"messages":[{"role":"user","content":"hi"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	got, _ := ioutil.ReadAll(resp.Body)
	resp.Body.Close()

	if resp.StatusCode != 200 || string(got) != `{"naked":true}` {
		t.Fatalf("the short circuit should return 200 right away with the plugin body, got %d %q", resp.StatusCode, got)
	}
	if hits := resp.Header.Get("X-Newgate-Profile"); hits != "naked-profile" {
		t.Errorf("the profile echoed by the short circuit = %q, want naked-profile (hard-coding n/a makes it impossible to tell which profile handled the request)", hits)
	}
	if route := resp.Header.Get("X-Newgate-Route"); route != "naked:naked-fixture" {
		t.Errorf("X-Newgate-Route = %q, want naked:naked-fixture", route)
	}
	if upstreamHits != 0 {
		t.Errorf("the short-circuiter claimed the request yet the upstream was hit %d times", upstreamHits)
	}
}
