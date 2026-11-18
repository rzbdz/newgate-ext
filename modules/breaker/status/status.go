// Package status is the health table's **wire types**:
// the shape of the JSON between daemon and CLI.
//
// Why a separate leaf (2026-09-18): this type is a **cross-process contract**
// (the `breakers` field of `/__newgate/status`), and several places need it -- the data
// plane serializes it, the CLI renders it, the control-plane client deserializes it, and
// breaker's own command reads it too. If it stayed in the modules/breaker root package,
// "whoever wants it must import the whole breaker module", while breaker's command in turn
// needs the control-plane client -- a cycle. Sunk into a leaf, both sides can import it.
//
// It is also the **only** thing allowed to be read across versions (during
// graceful handover the CLI and daemon can be from different versions),
// so the "only add fields, never change them" rule below is hard.
package status

import (
	"time"

	i18n "github.com/rzbdz/newgate/lib/i18n"
)

// ProbeGrade normalizes probe results into a few stable
// tiers, interpreted jointly by routing ordering and the UI.
type ProbeGrade string

const (
	ProbeFluent      ProbeGrade = "fluent"
	ProbeUsable      ProbeGrade = "usable"
	ProbeLaggy       ProbeGrade = "laggy"
	ProbeUnavailable ProbeGrade = "unavailable"
)

// Status is one row of the binding health table, and the daemon->CLI
// wire contract (the `breakers` field of `/__newgate/status`).
//
// **Only add fields, never change them.** During graceful handover the CLI and daemon can be
// from different versions: a new CLI reading an old daemon's snapshot, and an old CLI reading a
// new daemon's snapshot, must both survive. Likewise the `health.json` file name and these JSON
// keys cannot change -- the known-good binary used for downgrade rollback reads exactly them.
//
// So `Open` keeps its original meaning ("delisted right now"), and new states
// like half-open go into the added `State`; an old CLI seeing `Open=true` treats
// it as unusable, it just does not know it is about to probe its way back.
type Status struct {
	Provider string        `json:"provider"`
	Model    string        `json:"model"`
	Fails    int           `json:"fails"`
	Open     bool          `json:"open"`
	OpenFor  time.Duration `json:"open_for_ns"`
	Reason   string        `json:"reason,omitempty"`
	Grade    ProbeGrade    `json:"grade,omitempty"`
	Latency  int64         `json:"latency_ms,omitempty"`
	Checked  time.Time     `json:"checked_at,omitempty"`
	ScoreMs  int           `json:"score_ms"`
	Samples  int           `json:"samples"`
	Scores   [4]int        `json:"scores_ms,omitempty"`
	Buckets  [4]int        `json:"samples_by_bucket,omitempty"`
	OpenedAt time.Time     `json:"opened_at,omitempty"`

	// -- Added in the 2026-09-17 half-open refactor (add only, no changes) --
	//
	// State is closed / open / half-open. Open cannot express half-open: when half-open the binding
	// can already enter the chain, and the state machine is waiting for this request's result.
	State string `json:"state,omitempty"`
	// Rule is the name of the current ledger (availability / rate limit / config
	// / request shape), so one glance tells which class of problem delisted it.
	Rule string `json:"rule,omitempty"`
	// OpenUntil is the absolute moment the cooldown expires. The CLI computes
	// age from OpenedAt itself, no longer relying on the snapshot-time
	// OpenFor (that value is stale as soon as it crosses the wire).
	OpenUntil time.Time `json:"open_until,omitempty"`
	// Trial: is there a half-open trial in flight right now.
	Trial bool `json:"trial,omitempty"`
	// ShapeSkips is the cumulative count of request-shape errors. Such
	// errors never delist, but the number must be visible -- otherwise "why
	// is it always 400" can only be answered by digging through metrics.
	ShapeSkips int `json:"shape_skips,omitempty"`
	// CooldownMs is how long the next trip will use (the value after backoff).
	CooldownMs int64 `json:"cooldown_ms,omitempty"`
	// Rank is the sort key computed by the daemon (by the <=4K tier). The CLI uses it directly
	// and no longer recomputes the 3000/12000 thresholds -- there is one source for policy.
	Rank int `json:"rank,omitempty"`
	// Spared: the diagnostic probe before tripping pulled this binding back (almost delisted,
	// and the probe proved it still works). The number is cumulative and survives restarts.
	Spared int `json:"spared,omitempty"`
}

// Latency is the tiering of latency samples.
// **Thresholds are defined here only once** (2026-09-18).
//
// Ordering (who goes on the chain first) and display (fluent / usable / laggy)
// read the same numbers. There used to be three separate copies of 3000 / 12000: the
// sort key in modules/breaker, the metrics rendering in gateway, the tier rendering
// in config. Changing one did not change the other two -- this repo has paid for that
// once already (the comment in modules/cli/proxy.go records the 2026-09-17 "a copy on
// each side"), so now there is one source only: thresholds here, tier names here too.
type Latency int

const (
	LatencyFast    Latency = iota // < FastMs
	LatencyOK                     // <= SlowMs
	LatencySlow                   // > SlowMs
	LatencyUnknown                // 没有样本
)

const (
	// FastMs is the upper bound of "fluent".
	FastMs = 3000
	// SlowMs is the upper bound of "usable"; beyond it counts as laggy.
	SlowMs = 12000
)

// Grade puts a latency sample into a tier. sampled=false (never probed) gives LatencyUnknown.
func Grade(scoreMs int, sampled bool) Latency {
	switch {
	case !sampled:
		return LatencyUnknown
	case scoreMs < FastMs:
		return LatencyFast
	case scoreMs <= SlowMs:
		return LatencyOK
	default:
		return LatencySlow
	}
}

// String is the human wording of these tiers. The tiers themselves (the
// LatencyFast constants) are the test; they are not persisted and do not go
// over the wire, so what is translated here is the **wording**, not the test.
func (l Latency) String() string {
	switch l {
	case LatencyFast:
		return i18n.T("fast", nil)
	case LatencyOK:
		return i18n.T("usable", nil)
	case LatencySlow:
		return i18n.T("laggy", nil)
	default:
		return i18n.T("unrated", nil)
	}
}
