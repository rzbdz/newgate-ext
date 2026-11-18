package special

import (
	"testing"
)

// claimer is a plugin that claims but does nothing, used to verify ClaimEgress's selection
// rules.
type claimer struct {
	name    string
	egress  Egress
	claimed bool // records whether Egress() was ever asked
}

func (c *claimer) Name() string        { return c.name }
func (c *claimer) Why() string         { return "for testing" }
func (c *claimer) Match(*Request) bool { return true }
func (c *claimer) Apply(b []byte, r *Request) ([]byte, []string, error) {
	return b, nil, nil
}
func (c *claimer) Egress(*Request, []byte) Egress {
	c.claimed = true
	return c.egress
}

// noopEgress is a claimed rewriter: it changes nothing.
type noopEgress struct{}

func (noopEgress) Egress([]byte) []byte { return nil }

// With nobody claiming, ClaimEgress returns nil - the caller takes the original byte-for-byte
// pass-through forwarding (the egressOn path in forward). That is the machine-checkable form
// of the promise "forwarding is forwarding when there is no patch".
func TestClaimEgressReturnsNilWhenNobodyClaims(t *testing.T) {
	saved := currentRegistry()
	defer SetDefault(saved)
	SetDefault(NewRegistry())

	if got := ClaimEgress(&Request{}, nil, nil); got != nil {
		t.Fatalf("an empty registry must not claim anything, got %v", got)
	}
}

// The first claim wins and the rest are **not even asked**: a response has one outlet, and
// two parties each rewriting would splice together something neither of them wrote.
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

// One that does not claim (returns nil) does not block the ones after it.
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

// A plugin turned off at runtime does not take part in claiming (the `newgate plugin <path>
// off` path).
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

// A panicking claimer counts as not claiming (fail-open, the same rule as Apply).
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

// A rewriter that panics or returns an empty slice counts as not rewriting - forwarding goes
// on, bytes stay as they were.
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
