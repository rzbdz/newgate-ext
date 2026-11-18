package special

import (
	"testing"
)

// claimer 是一个只认领不干活的插件，用来验 ClaimEgress 的挑选规则。
type claimer struct {
	name    string
	egress  Egress
	claimed bool // 记下 Egress() 有没有被问过
}

func (c *claimer) Name() string        { return c.name }
func (c *claimer) Why() string         { return "测试用" }
func (c *claimer) Match(*Request) bool { return true }
func (c *claimer) Apply(b []byte, r *Request) ([]byte, []string, error) {
	return b, nil, nil
}
func (c *claimer) Egress(*Request, []byte) Egress {
	c.claimed = true
	return c.egress
}

// noopEgress 是认领下来的改写者：一条都不改。
type noopEgress struct{}

func (noopEgress) Egress([]byte) []byte { return nil }

// 没人认领时 ClaimEgress 回 nil——调用方据此走原来那条逐字节直通的转发
// （forward 里 egressOn 那一条）。这是「没有补丁时转发就是转发」那条承诺的
// 机器可验形式。
func TestClaimEgressReturnsNilWhenNobodyClaims(t *testing.T) {
	saved := currentRegistry()
	defer SetDefault(saved)
	SetDefault(NewRegistry())

	if got := ClaimEgress(&Request{}, nil, nil); got != nil {
		t.Fatalf("an empty registry must not claim anything, got %v", got)
	}
}

// 第一个认领的赢，后面的**连问都不问**：响应只有一个出口，两家各改各的拼出来
// 的是谁都没写过的东西。
func TestClaimEgressTakesTheFirstClaimantOnly(t *testing.T) {
	saved := currentRegistry()
	defer SetDefault(saved)
	registry := NewRegistry()
	SetDefault(registry)

	second := &claimer{name: "second", egress: noopEgress{}}
	first := &claimer{name: "first", egress: noopEgress{}}
	for _, p := range []Plugin{first, second} {
		if _, err := registry.Register(p); err != nil {
			t.Fatal(err)
		}
	}
	got := ClaimEgress(&Request{}, nil, nil)
	if got == nil {
		t.Fatal("the first claimant must win")
	}
	if !first.claimed {
		t.Fatal("the first plugin was never asked")
	}
	if second.claimed {
		t.Fatal("the second plugin must not be asked at all")
	}
}

// 不认领的（回 nil）不挡住后面的人。
func TestClaimEgressSkipsNonClaimers(t *testing.T) {
	saved := currentRegistry()
	defer SetDefault(saved)
	registry := NewRegistry()
	SetDefault(registry)

	declines := &claimer{name: "declines"}
	wants := &claimer{name: "wants", egress: noopEgress{}}
	for _, p := range []Plugin{declines, wants} {
		if _, err := registry.Register(p); err != nil {
			t.Fatal(err)
		}
	}
	if got := ClaimEgress(&Request{}, nil, nil); got == nil {
		t.Fatal("the claimer after a decliner must still win")
	}
}

// 运行期关掉的插件不参与认领（`newgate plugin <path> off` 那条路）。
func TestClaimEgressHonoursTheOffSwitch(t *testing.T) {
	saved := currentRegistry()
	defer SetDefault(saved)
	registry := NewRegistry()
	SetDefault(registry)

	off := &claimer{name: "off", egress: noopEgress{}}
	on := &claimer{name: "on", egress: noopEgress{}}
	for _, p := range []Plugin{off, on} {
		if _, err := registry.Register(p); err != nil {
			t.Fatal(err)
		}
	}
	got := ClaimEgress(&Request{}, nil, func(name string) bool { return name == "off" })
	if got == nil {
		t.Fatal("the enabled claimer must win")
	}
	if off.claimed {
		t.Fatal("a switched-off plugin must not be asked")
	}
	if !on.claimed {
		t.Fatal("the enabled plugin was never asked")
	}
}

// 认领器 panic 当它没认领（fail-open，与 Apply 同一条规矩）。
type panickyClaimer struct{ claimer }

func (p *panickyClaimer) Egress(*Request, []byte) Egress { panic("boom") }

func TestClaimEgressIsFailOpen(t *testing.T) {
	saved := currentRegistry()
	defer SetDefault(saved)
	registry := NewRegistry()
	SetDefault(registry)

	bad := &panickyClaimer{claimer{name: "bad"}}
	good := &claimer{name: "good", egress: noopEgress{}}
	for _, p := range []Plugin{bad, good} {
		if _, err := registry.Register(p); err != nil {
			t.Fatal(err)
		}
	}
	if got := ClaimEgress(&Request{}, nil, nil); got == nil {
		t.Fatal("a panicking claimer must not swallow the request")
	}
	if !good.claimed {
		t.Fatal("the request must fall through to the next claimer")
	}
}

// 改写者 panic 或返回空 slice 都当没改——转发照常，字节照原样。
func TestRewriteEventIsFailOpen(t *testing.T) {
	if got := RewriteEvent(nil, []byte("data: {}")); got != nil {
		t.Fatalf("a nil rewriter must change nothing, got %q", got)
	}
	if got := RewriteEvent(noopEgress{}, []byte("data: {}")); got != nil {
		t.Fatalf("an empty result means unchanged, got %q", got)
	}
	if got := RewriteEvent(panickyEgress{}, []byte("data: {}")); got != nil {
		t.Fatalf("a panicking rewriter must change nothing, got %q", got)
	}
}

type panickyEgress struct{}

func (panickyEgress) Egress([]byte) []byte { panic("boom") }
