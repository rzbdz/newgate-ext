package breaker

import (
	"testing"
	"time"

	"github.com/rzbdz/newgate-ext/lib/view"
)

// LatencySample is the **one place on this screen** where latency becomes
// color (both the latency column of the health table and each hop's cell on the
// first screen read it), so each tier gets one test, plus one for "no sample".
//
// Why "no sample" matters most: drawing a `0ms` says "so fast it has no
// latency", while the truth is "unknown" -- on a page used to pick the fast
// one, the difference between those sentences is picking the wrong person.
func TestLatencySampleGradesAndColours(t *testing.T) {
	for _, c := range []struct {
		name    string
		sampled bool
		ms      int64
		wantOK  bool
		tone    string
	}{
		{"without a probe it should give nothing", false, 0, false, ""},
		// Boundaries follow status.Grade's wording: <3000 fast, <=12000 usable, the rest slow.
		{"1ms is fast", true, 1, true, view.ToneOK},
		{"2999ms is still fast", true, 2999, true, view.ToneOK},
		{"from 3000ms it is usable", true, 3000, true, view.ToneWarn},
		{"12000ms is still usable", true, 12000, true, view.ToneWarn},
		{"12001ms is slow", true, 12001, true, view.ToneBad},
	} {
		t.Run(c.name, func(t *testing.T) {
			b := Status{Latency: c.ms}
			if c.sampled {
				b.Checked = time.Now()
			}
			ms, tone, ok := LatencySample(b)
			if ok != c.wantOK {
				t.Fatalf("ok should be %v, got %v", c.wantOK, ok)
			}
			if tone != c.tone {
				t.Fatalf("the color should be %q, got %q", c.tone, tone)
			}
			// The number goes out **as is**: this cell displays the sample itself, and the UI
			// does not recompute or round (the neighboring cells are what aggregate into scores).
			if ok && ms != c.ms {
				t.Fatalf("should give %dms as is, got %dms", c.ms, ms)
			}
		})
	}
}

// BindingKey is a **cross-module** machine marker: the health table's row ID and the first screen
// look up that row by it, and assembling it on both sides means assembling the same string. The
// symptom of getting it wrong is "the latency cell stays empty" -- no error, just never any data.
func TestBindingKeyJoinsProviderAndModel(t *testing.T) {
	if got := BindingKey("ark", "deepseek-v3"); got != "ark/deepseek-v3" {
		t.Fatalf("should be ark/deepseek-v3, got %q", got)
	}
}
