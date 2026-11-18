package breaker

import (
	"github.com/rzbdz/newgate-ext/lib/view"
	"github.com/rzbdz/newgate-ext/modules/breaker/status"
)

// LatencySample gives the latency number a binding
// should display right now, and the color it should get.
//
// ok=false means **no sample** (never probed, and no real traffic went
// through this one): in that case the UI should draw nothing. Drawing a `0ms`
// says "it is so fast it has no latency", while the truth is "unknown" --
// on a page used to **pick the fast one** those two sentences differ a lot.
//
// # Why this mapping is exported rather than left in view.go
//
// Two UIs talk about this today: the `breaker.health` table (this package's
// own view.go), and the routing overview on the first screen (the distribution's
// modules/home -- it sees chains and health at once). Written twice, the two will
// drift sooner or later, and the symptom of drift is **the same binding colored
// differently on two pages**, which only makes readers suspect the data itself.
//
// The thresholds are not in this function, nor in any UI: fast / usable / slow is
// the test of `status.Grade`, implemented in one place (3000 / 12000 are written in
// modules/breaker/status, which is also the copy the CLI uses). Colors align with
// the CLI (see bindingHealthLabel in modules/config/commands.go: green / yellow /
// red) -- the same data in the same process, with two UIs saying the same thing.
//
// Coloring **all three** tiers is deliberate: latency is the only number on this page that
// is "compared by eye", and coloring only the slowest would make the user read every cell.
func LatencySample(b Status) (ms int64, tone string, ok bool) {
	if b.Checked.IsZero() {
		return 0, "", false
	}
	switch status.Grade(int(b.Latency), true) {
	case status.LatencyFast:
		return b.Latency, view.ToneOK, true
	case status.LatencyOK:
		return b.Latency, view.ToneWarn, true
	case status.LatencySlow:
		return b.Latency, view.ToneBad, true
	}
	// With sampled=true Grade cannot reach LatencyUnknown. If it does get here, it changed its
	// test, so **do not color** -- better a cell with no color than a conclusion we made up.
	return b.Latency, "", true
}

// BindingKey is the health table's row ID: `provider/model`.
//
// Why it needs a name: this key is a **cross-module machine marker** -- it is both the
// row ID in the table (healthTable in view.go) and the test the first screen uses to match
// "this hop on a chain" to "that row in the health table" (the distribution's modules/home).
// Assembling it on both sides means assembling the same string, and the symptom of
// getting it wrong is "the latency cell stays empty" -- no error, just never any data.
func BindingKey(provider, model string) string { return provider + "/" + model }
