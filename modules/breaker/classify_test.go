package breaker

import (
	"testing"
	"time"
)

// shapeBody is a 400 text that testShape (see state_test.go) will claim.
//
// The real test text no longer lives in this package -- since 2026-09-17 it is registered
// by the upstream module (modules/deepseek/shape.go), and tests there use real upstream
// response snapshots. What is needed here is just a body **whose shape matches testShape**,
// to verify how the state machine handles the "shape ledger" (never delist, only count).
const shapeBody = `{"type":"error","message":"The ` + "`reasoning_content`" +
	` in the thinking mode must be passed back to the API."}`

// TestClassify is the truth table of the decision table: each
// row corresponds to a row of the table in docs/05-gateway.md.
//
// This table is the sole judge of "keep going along the chain / record or
// not / which ledger", so it must be readable exhaustively. Shape is computed
// beforehand by Report and put in (see Report); here it is given directly.
func TestClassify(t *testing.T) {
	tests := []struct {
		name string
		in   Input
		want Verdict
	}{
		// -- success --
		{"upstream success", Input{Kind: KindUpstreamSuccess}, Verdict{}},

		// -- client cancel: not the upstream's fault, and no point crashing further --
		{"client cancellation (mid-chain)", Input{Kind: KindClientCancel}, Verdict{}},
		{"client cancellation (chain tail)", Input{Kind: KindClientCancel, IsLast: true}, Verdict{}},
		{"client cancellation (bytes written)",
			Input{Kind: KindClientCancel, Written: true}, Verdict{}},

		// -- connection-class failures: switch hop + record availability --
		{"connection failure (mid-chain)",
			Input{Kind: KindConnError}, Verdict{Advance: true, Bucket: BucketAvailability}},
		{"connection failure (chain tail)",
			Input{Kind: KindConnError, IsLast: true}, Verdict{Bucket: BucketAvailability}},
		{"first-byte timeout (bytes written)",
			Input{Kind: KindConnError, Written: true}, Verdict{Bucket: BucketAvailability}},

		// -- stream broken mid-way: cannot switch hop (written bytes
		// cannot be unsent), but a real availability problem --
		{"stream broke mid-way", Input{Kind: KindStreamTruncated, Written: true},
			Verdict{Bucket: BucketAvailability}},
		{"stream broke mid-way (chain tail)",
			Input{Kind: KindStreamTruncated, Written: true, IsLast: true},
			Verdict{Bucket: BucketAvailability}},

		// -- 400 + shape hit: try further down, but never charge it to this one --
		{"shape 400 (mid-chain, fallback off)",
			Input{Kind: KindUpstreamStatus, Status: 400, Shape: true},
			Verdict{Advance: true, Bucket: BucketShape}},
		{"shape 400 (chain tail)",
			Input{Kind: KindUpstreamStatus, Status: 400, Shape: true, IsLast: true},
			Verdict{Bucket: BucketShape}},
		{"shape 400 (bytes written)",
			Input{Kind: KindUpstreamStatus, Status: 400, Shape: true, Written: true},
			Verdict{Bucket: BucketShape}},

		// -- other 400: the request itself is bad, record nothing, switch nothing --
		{"non-shape 400 (fallback off)",
			Input{Kind: KindUpstreamStatus, Status: 400}, Verdict{}},
		{"non-shape 400 (fallback on, mid-chain)",
			Input{Kind: KindUpstreamStatus, Status: 400, FallbackOn400: true}, Verdict{Advance: true}},
		{"non-shape 400 (fallback on, chain tail)",
			Input{Kind: KindUpstreamStatus, Status: 400, FallbackOn400: true, IsLast: true}, Verdict{}},

		// -- rate limiting --
		{"429 (mid-chain)",
			Input{Kind: KindUpstreamStatus, Status: 429},
			Verdict{Advance: true, Bucket: BucketRateLimit}},
		{"429 (chain tail)",
			Input{Kind: KindUpstreamStatus, Status: 429, IsLast: true},
			Verdict{Bucket: BucketRateLimit}},

		// -- config: bad key / this one has no such model / this one is out of money --
		{"401",
			Input{Kind: KindUpstreamStatus, Status: 401},
			Verdict{Advance: true, Bucket: BucketConfig}},
		{"402",
			Input{Kind: KindUpstreamStatus, Status: 402},
			Verdict{Advance: true, Bucket: BucketConfig}},
		{"402 (chain tail)",
			Input{Kind: KindUpstreamStatus, Status: 402, IsLast: true},
			Verdict{Bucket: BucketConfig}},
		{"403",
			Input{Kind: KindUpstreamStatus, Status: 403},
			Verdict{Advance: true, Bucket: BucketConfig}},
		{"404",
			Input{Kind: KindUpstreamStatus, Status: 404},
			Verdict{Advance: true, Bucket: BucketConfig}},
		{"404 (chain tail)",
			Input{Kind: KindUpstreamStatus, Status: 404, IsLast: true},
			Verdict{Bucket: BucketConfig}},

		// -- availability: upstream down / queued --
		{"408", Input{Kind: KindUpstreamStatus, Status: 408},
			Verdict{Advance: true, Bucket: BucketAvailability}},
		{"409", Input{Kind: KindUpstreamStatus, Status: 409},
			Verdict{Advance: true, Bucket: BucketAvailability}},
		{"500", Input{Kind: KindUpstreamStatus, Status: 500},
			Verdict{Advance: true, Bucket: BucketAvailability}},
		{"502", Input{Kind: KindUpstreamStatus, Status: 502},
			Verdict{Advance: true, Bucket: BucketAvailability}},
		{"503", Input{Kind: KindUpstreamStatus, Status: 503},
			Verdict{Advance: true, Bucket: BucketAvailability}},
		{"599 (>=500 always counts as availability)", Input{Kind: KindUpstreamStatus, Status: 599},
			Verdict{Advance: true, Bucket: BucketAvailability}},

		// -- other 4xx / 3xx: the request itself is bad, ramming every upstream is pointless --
		{"405", Input{Kind: KindUpstreamStatus, Status: 405}, Verdict{}},
		{"418", Input{Kind: KindUpstreamStatus, Status: 418}, Verdict{}},
		{"3xx (no failover anymore)", Input{Kind: KindUpstreamStatus, Status: 302}, Verdict{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Classify(tt.in); got != tt.want {
				t.Errorf("Classify(%+v) = %+v, want %+v", tt.in, got, tt.want)
			}
		})
	}
}

// TestClassifyShapeIsExemptFromFallbackOn400: a shape error is **not** subject
// to chain.fallback_on_400. That switch stops "ramming a bad request into
// every upstream", while a shape error is the opposite -- the same body is
// picky about only this one, and switching is the only path that can succeed.
func TestClassifyShapeIsExemptFromFallbackOn400(t *testing.T) {
	for _, fallback := range []bool{false, true} {
		got := Classify(Input{
			Kind: KindUpstreamStatus, Status: 400, Shape: true, FallbackOn400: fallback,
		})
		if !got.Advance || got.Bucket != BucketShape {
			t.Errorf("with fallback_on_400=%v a shape 400 = %+v, should continue along the chain and only count", fallback, got)
		}
	}
}

// TestClassifyNeverAdvancesAfterWritingBytesOrAtChainEnd is those two invariants: bytes
// already written cannot be unsent, and with no next hop on the chain there is nothing
// to switch to. Both must hold under any Kind, any status code, any shape decision.
func TestClassifyNeverAdvancesAfterWritingBytesOrAtChainEnd(t *testing.T) {
	kinds := []Kind{KindUpstreamSuccess, KindConnError, KindClientCancel,
		KindUpstreamStatus, KindStreamTruncated}
	statuses := []int{0, 200, 302, 400, 401, 402, 403, 404, 408, 409, 429, 500, 502, 503}
	for _, kind := range kinds {
		for _, status := range statuses {
			for _, shape := range []bool{false, true} {
				for _, fallback := range []bool{false, true} {
					for _, written := range []bool{false, true} {
						for _, last := range []bool{false, true} {
							in := Input{
								Kind: kind, Status: status, Shape: shape,
								FallbackOn400: fallback, Written: written, IsLast: last,
							}
							if got := Classify(in); got.Advance && (written || last) {
								t.Fatalf("Classify(%+v) still fails over with written=%v isLast=%v",
									in, written, last)
							}
						}
					}
				}
			}
		}
	}
}

// TestClassifyBucketOnlyForKnownCauses: Bucket is "which ledger to record
// in", and can only come from the known classes of cause. This blocks
// a future classification from slipping a new bucket nobody handles.
func TestClassifyBucketOnlyForKnownCauses(t *testing.T) {
	known := map[Bucket]bool{
		BucketNone: true, BucketAvailability: true, BucketRateLimit: true,
		BucketConfig: true, BucketShape: true,
	}
	for kind := KindUpstreamSuccess; kind <= KindStreamTruncated; kind++ {
		for status := 0; status <= 600; status++ {
			for _, shape := range []bool{false, true} {
				got := Classify(Input{Kind: kind, Status: status, Shape: shape})
				if !known[got.Bucket] {
					t.Fatalf("Classify(%v, %d, shape=%v) gave an unknown ledger %d",
						kind, status, shape, got.Bucket)
				}
				if got.Bucket == BucketShape && status != 400 {
					t.Fatalf("a non-400 (%d) was judged a request-shape error", status)
				}
				// The shape ledger only appears under KindUpstreamStatus.
				if got.Bucket == BucketShape && kind != KindUpstreamStatus {
					t.Fatalf("%v was judged a request-shape error", kind)
				}
			}
		}
	}
}

// TestBucketRuleNames: the ledger has **two** names, each
// with its own use, and neither may be changed casually:
//
//   - ruleToken is persisted (health.json and the `rule` in /__newgate/status),
//     a **machine marker**, independent of language -- the known-good binary reads
//     the same file, and a restart under another language must still recognize it;
//   - ruleName is human-facing (the "ledger" column of the table,
//     the sentence in a delisting reason), and goes through i18n.
func TestBucketRuleNames(t *testing.T) {
	tokens := map[Bucket]string{
		BucketNone:         "",
		BucketAvailability: "availability",
		BucketRateLimit:    "rate limit",
		BucketConfig:       "config",
		BucketShape:        "request shape",
	}
	for b, token := range tokens {
		if got := b.ruleToken(); got != token {
			t.Errorf("Bucket(%d).ruleToken() = %q, want %q", b, got, token)
		}
		if token == "" {
			continue
		}
		if got := bucketFromName(token); got != b {
			t.Errorf("bucketFromName(%q) = %d, want %d", token, got, b)
		}
	}
	if got := bucketFromName("no such ledger"); got != BucketNone {
		t.Errorf("an unknown ledger name should fold into BucketNone, got %d", got)
	}
	// With no catalog installed i18n.T takes the identity
	// path, so what is read here is the English original.
	names := map[Bucket]string{
		BucketAvailability: "Availability",
		BucketRateLimit:    "Rate limit",
		BucketConfig:       "Config",
		BucketShape:        "Request shape",
	}
	for b, name := range names {
		if got := b.ruleName(); got != name {
			t.Errorf("Bucket(%d).ruleName() = %q, want %q", b, got, name)
		}
	}
}

// TestDefaultPolicyIsComplete: the policy table must cover every ledger that will
// be used, or rule() returns a zero-value Rule (threshold 0 = never delist) --
// one typo and an incident like "500 does not trip the breaker" ships silently.
func TestDefaultPolicyIsComplete(t *testing.T) {
	p := DefaultPolicy()
	for _, b := range []Bucket{BucketAvailability, BucketRateLimit, BucketConfig, BucketShape} {
		if _, ok := p.Rules[b]; !ok {
			t.Errorf("the default policy is missing the %s ledger", b.ruleName())
		}
	}
	for b, r := range p.Rules {
		if b == BucketShape {
			if r.Threshold != 0 {
				t.Errorf("the request-shape ledger must never drop a binding, threshold = %d", r.Threshold)
			}
			continue
		}
		if r.Threshold < 1 {
			t.Errorf("the threshold of the %s ledger = %d would mean it never drops", b.ruleName(), r.Threshold)
		}
		if r.Cooldown <= 0 {
			t.Errorf("the cooldown of the %s ledger = %v", b.ruleName(), r.Cooldown)
		}
		if r.Backoff < 1 {
			t.Errorf("the backoff of the %s ledger = %v (<1 would make each cooldown shorter)", b.ruleName(), r.Backoff)
		}
		if r.MaxCooldown > 0 && r.MaxCooldown < r.Cooldown {
			t.Errorf("the cap %v of the %s ledger is below the first cooldown %v", b.ruleName(), r.MaxCooldown, r.Cooldown)
		}
	}
	if p.TrialTTL <= 0 {
		t.Errorf("trial TTL = %v: without it, a trial that never reports back would leave the binding half-open forever", p.TrialTTL)
	}
}

// TestPolicyNormalizationFillsDefaults: SetPolicy writes only the
// few items to change, the rest are filled with defaults -- otherwise a
// caller omitting one item sets that class of failure to "never delist".
func TestPolicyNormalizationFillsDefaults(t *testing.T) {
	got := Policy{Rules: map[Bucket]Rule{
		BucketRateLimit: {Threshold: 7, Cooldown: time.Second},
	}}.normalized()

	if got.Rules[BucketRateLimit].Threshold != 7 || got.Rules[BucketRateLimit].Cooldown != time.Second {
		t.Fatalf("an explicitly given entry was overridden: %+v", got.Rules[BucketRateLimit])
	}
	def := DefaultPolicy()
	if got.Rules[BucketAvailability] != def.Rules[BucketAvailability] {
		t.Fatalf("a ledger that was not given did not get defaults: %+v", got.Rules[BucketAvailability])
	}
	if got.TrialTTL != def.TrialTTL {
		t.Fatalf("TrialTTL that was not given did not get a default: %v", got.TrialTTL)
	}
	// Backoff 1 is the **explicit** way to write "no backoff" and must not be dropped as a zero value.
	p := Policy{Rules: map[Bucket]Rule{
		BucketAvailability: {Threshold: 1, Cooldown: time.Second, Backoff: 1},
	}}.normalized()
	if p.Rules[BucketAvailability].Backoff != 1 {
		t.Fatalf("an explicit Backoff=1 was overridden: %+v", p.Rules[BucketAvailability])
	}
}

func TestSplitBindingKey(t *testing.T) {
	provider, model := splitBindingKey(bindingKey("relay", "deepseek-chat"))
	if provider != "relay" || model != "deepseek-chat" {
		t.Fatalf("splitBindingKey round trip failed: %q / %q", provider, model)
	}
	// A \x00 in a model name is impossible, but parsing the
	// concatenated key must not eat a wrong split because of it.
	if p, m := splitBindingKey("nokey"); p != "nokey" || m != "" {
		t.Fatalf("it must not guess when there is no separator: %q / %q", p, m)
	}
}

func BenchmarkClassify(b *testing.B) {
	in := Input{Kind: KindUpstreamStatus, Status: 500, Body: []byte(shapeBody)}
	for i := 0; i < b.N; i++ {
		if v := Classify(in); !v.Advance {
			b.Fatalf("Classify = %+v", v)
		}
	}
}
