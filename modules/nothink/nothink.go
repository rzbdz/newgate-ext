package nothink

import (
	"encoding/json"
	"time"

	"github.com/rzbdz/newgate-ext/modules/config/domain"
	"github.com/rzbdz/newgate-ext/modules/gateway/rewrite"
	"github.com/rzbdz/newgate-ext/modules/gateway/special"
	thinkingapi "github.com/rzbdz/newgate-ext/modules/thinking"
	i18n "github.com/rzbdz/newgate/lib/i18n"
)

var _ special.Plugin = nothink{}

// StateKey is the name of this cell in state.json (owned by this module, see the registration in
// module.go).
//
// **Do not change the key name**: a known-good rollback binary reads the same config file, and
// changing the key is equivalent to leaving this switch forgotten in on state -- and while it is
// on, the user's models do not reason (same as classifier_naked).
const StateKey = "nothink"

// disabled is the thinking switch written on behalf of the client: in the Anthropic dialect, "do
// not think this round".
var disabled = []byte(`{"type":"disabled"}`)

// Config describes one "globally do not think" window. Built/persisted by the CLI, consumed by the
// plugin.
//
// **The JSON tag cannot be omitted**: `expires_at` only matches `ExpiresAt` through the tag (Go's
// default field-name matching ignores case but not underscores). Without the tag, the deserialized
// ExpiresAt is the zero value, `time.Now().Before(zero)` is always false → `on` judged expired on
// the spot → the switch **never takes effect**, and the status line prints `-2562047h…`. This pit
// was hit for real once on classifier_naked (2026-09-17); a struct of the same shape must not step
// in it a second time.
type Config struct {
	Mode      string    `json:"mode"`       // "on" (self-limiting window) | "forever"
	ExpiresAt time.Time `json:"expires_at"` // when this window expires while Mode=="on"
}

// Marshal encodes the bytes to write into state.json.
func Marshal(c Config) ([]byte, error) { return json.Marshal(c) }

// Parse parses that cell in state.json, and puts **the expiry judgment here**.
//
// `on` expires lazily (the daemon holds no timer), so "does this still count as on right now" can
// only be decided at the moment of reading; and there are three places that read it (Match / Status
// / the command), and judging separately in each will drift -- that version was hit for real on
// classifier_naked: Respond judged expiry, Status did not, so after the window status printed a
// line "auto-off in -6m3s". A window that has already lapsed should look like "not on" at **any**
// read point, and that invariant holds only with a single enforcement point.
//
// Bad data (empty, bad JSON, mode not in the allowlist) is all treated as "not on" -- off is the
// safe side.
func Parse(raw []byte) (Config, bool) {
	var c Config
	if len(raw) == 0 || json.Unmarshal(raw, &c) != nil {
		return c, false
	}
	switch c.Mode {
	case "on":
		return c, time.Now().Before(c.ExpiresAt)
	case "forever":
		return c, true
	}
	return c, false
}

// nothink is that plugin: while the switch is on, turn this request into "do not think".
//
// It itself does only one thing -- **override the client's thinking intent** (see Apply); the fill
// and translate halves live in the kernel's BestEffortDisable, which this module calls through a
// capability rather than copying.
type nothink struct{ thinking thinkingapi.Service }

func (nothink) Name() string { return "nothink" }

// Before puts this plugin ahead of the fallback translator (always-thinks): the disabled we write
// must get a look from it (when the model cannot have thinking turned off, it translates disabled
// into enabled + low). Ordering it the other way round, the consequence is not ugly ordering but
// that this round goes upstream with disabled and hits a 400.
//
// Only the kernel's own plugin name is written here: the ordering edges of product plugins are
// declared by the product itself, and the kernel's `app/ordering_test.go` rejects "a kernel
// ordering edge pointing at a plugin outside the kernel" -- the direction is "this module → kernel
// plugin", this one edge.
func (nothink) Before() []string { return []string{"always-thinks"} }
func (nothink) After() []string  { return nil }

func (nothink) Why() string {
	return i18n.T("the global no-reasoning switch (newgate nothink on|forever) is on: every Anthropic-dialect "+
		"request goes upstream with thinking disabled, adaptive/enabled included\n"+
		"best effort — a model that always thinks gets enabled + reasoning_effort=low instead of a 400, "+
		"and OpenAI-dialect requests (no such field to write) are left alone\n"+
		"newgate nothink off ends it; st off nothink takes this plugin out without touching the setting", nil)
}

// Match recognizes "this switch is on right now", **plus** this request speaking the Anthropic
// dialect.
//
// The dialect criterion sits in Match rather than Apply: it is not the "nothing to change this
// round" kind of exception, it is "this has nothing to do with me" -- an OpenAI-dialect request
// should never be counted by this policy at all (see the opencode field report in the package
// comment).
func (nothink) Match(r *special.Request) bool {
	return r != nil && domain.IsAnthropicPath(r.Path) && active(r)
}

// active: is this switch on right now.
//
// It reads **the config snapshot this request saw** (r.State), not the disk afresh: within one
// request every plugin sees the same world. A missing State (unit tests, old call surfaces) counts
// as "not on" -- off is this switch's default state.
func active(r *special.Request) bool {
	if r == nil || r.State == nil {
		return false
	}
	_, on := Parse(r.State.ModuleConfig[StateKey])
	return on
}

// Apply lands "globally do not think" on this one request body.
//
// Two steps, each with its boundary spelled out:
//
//  1. **If the client wrote thinking, override it** (adaptive / enabled / any other shape).
//     That is the entire point of this switch: the main loop's request carries exactly
//     adaptive, and without overriding it this switch is equivalent to nonexistent on the
//     path that actually thinks. The rewrite touches only thinking's byte range (a rewrite
//     primitive); every other byte in the body is kept as is.
//  2. **The rest goes to the kernel's best-effort**: add disabled where no thinking was
//     written; where the model cannot have thinking turned off (the always-thinks quirk
//     table), translate it in place into enabled + reasoning_effort=low. Untranslatable
//     goes out as is -- best effort's boundary is "never fail the request because of this
//     intent", not "must turn it off".
//
// For a request where the client explicitly set reasoning_effort, step 2 is skipped entirely (the
// kernel's criterion: setting an effort means wanting to think); but step 1 still changes thinking
// to disabled -- the two fields are **two different things**, and this does not guess on the user's
// behalf which one rules; it only makes sure "globally do not think" lands.
func (n nothink) Apply(body []byte, r *special.Request) ([]byte, []string, error) {
	// Confirm Match once more. The framework's chain only calls Apply on matching plugins anyway, so
	// this line is **redundant** -- but what it guards is worth it: this plugin changes the intent
	// the client **wrote**, and the invariant "switch off means not one byte touched" sits right next
	// to the change, so another call surface (tests, another chain in the future) cannot break it.
	if !n.Match(r) {
		return body, nil, nil
	}

	out, notes := body, []string(nil)
	if raw, has := rewrite.TopLevelRaw(out, "thinking"); has {
		if kind, _ := rewrite.TopLevelString(raw, "type"); kind != "disabled" {
			rewritten, err := rewrite.ReplaceTopLevelRaw(out, "thinking", disabled)
			if err != nil {
				// Unchangeable goes out as is: better that this round thinks per the client's intent than
				// that a switch breaks the request (fail-open, and it is also a note).
				return body, nil, i18n.Ef(err, "thinking left as the client wrote it ({err})", nil)
			}
			out = rewritten
			notes = append(notes, i18n.T("thinking:{was} → disabled (nothink is on)",
				i18n.A{"was": kind}))
		}
	}

	translated, more, err := n.thinking.BestEffortDisable(out, r)
	if err != nil {
		// Unreachable: the kernel move folds its failures into notes (it is fail-open itself). If it
		// really happens, treat it as not having run -- the move above already landed, and sending per
		// that is closer to the user's intent.
		return out, notes, nil
	}
	return translated, append(notes, more...), nil
}

// Status makes `newgate status` keep printing this while the switch is on.
//
// Why this belongs on the status screen more than other status lines: it changes **the model's
// behavior**, and the symptoms (no more reasoning, dumber, faster) will never say anywhere who did
// it. The moment you forget you left it on, that becomes a question answerable only by digging
// through the config.
func (nothink) Status(state *domain.State) []special.StatusItem {
	if state == nil {
		return nil
	}
	cfg, on := Parse(state.ModuleConfig[StateKey])
	if !on {
		return nil
	}
	if cfg.Mode == "forever" {
		return []special.StatusItem{{
			Label: i18n.T("Nothink", nil),
			Value: i18n.T("on (forever) — every request goes upstream with thinking off, best effort; newgate nothink off ends it", nil),
		}}
	}
	return []special.StatusItem{{
		Label: i18n.T("Nothink", nil),
		Value: i18n.T("on — it turns itself off in {left} (newgate nothink off ends it early)",
			i18n.A{"left": time.Until(cfg.ExpiresAt).Round(time.Second)}),
	}}
}
