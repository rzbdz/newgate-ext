package nothink

import (
	"testing"
	"time"

	"github.com/rzbdz/newgate-ext/modules/config/domain"
	"github.com/rzbdz/newgate-ext/modules/gateway/special"
)

// stub 是内核那个端口的替身。
//
// 本模块**只该委托**：「没写 thinking 就补 disabled」「模型关不掉思考就翻成
// enabled + low」那两手的语义住内核 modules/thinking，它自己那几条测试管着。
// 这里验的是委托发生了、以及发生在我们改写之后的 body 上。
type stub struct{ calls int }

func (s *stub) BestEffortDisable(body []byte, _ *special.Request) ([]byte, []string, error) {
	s.calls++
	return body, nil, nil
}

func request(path, config string) *special.Request {
	r := &special.Request{
		Path:  path,
		State: &domain.State{ModuleConfig: map[string][]byte{}},
	}
	if config != "" {
		r.State.ModuleConfig[StateKey] = []byte(config)
	}
	return r
}

// TestParse 走的是**写进去再读出来**，不是手写 JSON：这个结构里最容易错的一处是
// 字段标签（`expires_at` ↔ ExpiresAt），而两边各写一份字面量的话，标签错了测试
// 照样绿——它比的是两个同样错的东西。现场那次（classifier_naked，2026-09-17）
// 的症状就是「开关永远不生效」。
func TestParse(t *testing.T) {
	marshal := func(c Config) string {
		raw, err := Marshal(c)
		if err != nil {
			t.Fatalf("Marshal: %v", err)
		}
		return string(raw)
	}
	now := time.Now()

	cases := []struct {
		name string
		raw  string
		want bool
	}{
		{"forever", marshal(Config{Mode: "forever"}), true},
		{"window still open", marshal(Config{Mode: "on", ExpiresAt: now.Add(time.Minute)}), true},
		{"window passed", marshal(Config{Mode: "on", ExpiresAt: now.Add(-time.Minute)}), false},
		{"no expiry written", `{"mode":"on"}`, false},
		{"empty", ``, false},
		{"garbage", `{`, false},
		{"unknown mode", `{"mode":"maybe"}`, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, on := Parse([]byte(c.raw)); on != c.want {
				t.Errorf("Parse(%s) decided %v, want %v", c.raw, on, c.want)
			}
		})
	}
}

// TestApply 是本模块的全部行为：开关开着的时候，客户端的思考意图被覆盖成 disabled
// （Claude Code 主循环发的是 adaptive，见包注释里的现场 dump），其余每一种情况
// 都必须**一个字节都不动**。
func TestApply(t *testing.T) {
	const (
		forever  = `{"mode":"forever"}`
		adaptive = `{"model":"m","thinking":{"type":"adaptive"}}`
		off      = `{"model":"m","thinking":{"type":"disabled"}}`
		bare     = `{"model":"m"}`
	)

	cases := []struct {
		name     string
		path     string
		config   string
		body     string
		want     string
		matched  bool
		delegate bool
	}{
		{
			name: "the main loop says adaptive, and it was overridden to disabled",
			path: "/messages", config: forever, body: adaptive,
			want: `{"model":"m","thinking":{"type":"disabled"}}`, matched: true, delegate: true,
		},
		{
			name: "already disabled: not rewritten, but still handed to the kernel half",
			path: "/messages", config: forever, body: off,
			want: off, matched: true, delegate: true,
		},
		{
			name: "no thinking written: the half that fills it lives in the kernel; here we only delegate",
			path: "/messages", config: forever, body: bare,
			want: bare, matched: true, delegate: true,
		},
		{
			name: "count_tokens speaks the same dialect",
			path: "/messages/count_tokens", config: forever, body: adaptive,
			want: `{"model":"m","thinking":{"type":"disabled"}}`, matched: true, delegate: true,
		},
		{
			name: "the switch is off (default)",
			path: "/messages", body: adaptive,
			want: adaptive, matched: false, delegate: false,
		},
		{
			name: "an expired window means off",
			path: "/messages", config: `{"mode":"on","expires_at":"2020-01-01T00:00:00Z"}`,
			body: adaptive, want: adaptive, matched: false, delegate: false,
		},
		{
			name: "the OpenAI dialect has no thinking to write",
			path: "/chat/completions", config: forever, body: bare,
			want: bare, matched: false, delegate: false,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := &stub{}
			p := nothink{thinking: s}
			r := request(c.path, c.config)

			if got := p.Match(r); got != c.matched {
				t.Errorf("Match = %v, want %v", got, c.matched)
			}
			out, notes, err := p.Apply([]byte(c.body), r)
			if err != nil {
				t.Fatalf("Apply errored: %v", err)
			}
			if string(out) != c.want {
				t.Errorf("Apply gave %s, want %s", out, c.want)
			}
			if got := s.calls > 0; got != c.delegate {
				t.Errorf("the delegated kernel half = %v, want %v", got, c.delegate)
			}
			// 不静默：改写了就必须留下一条 note（它会被写进日志）。
			if rewritten := string(out) != c.body; rewritten && len(notes) == 0 {
				t.Error("it rewrote the request but left no note - the user has no way to know what was done to their request")
			}
		})
	}
}

func TestStatusOnlyWhenOn(t *testing.T) {
	var p nothink
	if got := p.Status(request("/messages", "").State); len(got) != 0 {
		t.Errorf("while off, status should have no such line, got %v", got)
	}
	if got := p.Status(request("/messages", `{"mode":"forever"}`).State); len(got) != 1 {
		t.Errorf("with forever, status should have one line, got %v", got)
	}
}
