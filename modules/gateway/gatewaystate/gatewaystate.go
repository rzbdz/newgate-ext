// Package gatewaystate is gateway's own section in state.json.
//
// Why a separate leaf package (2026-09-18): the gateway's switches (schema repair, the special
// layer master switch, per-plugin switches) were declared as typed fields on `domain.State`.
// That wrote **the gateway's switch vocabulary into the shared config** -- every module's keys
// in one struct, so adding a gateway switch meant touching config. Now they live in
// state.json's `ModuleConfig["gateway"]`, exactly like `modules/claudecode`'s
// `classifier_naked` and `modules/pluginmanager`'s `plugin_manager`: **whoever owns the state
// parses it, and core knows no concrete key**.
//
// Why a leaf: `gateway/special` (the plugin layer) and `gateway/forward` (the forwarding hot
// path) both read it, and forward cannot import the gateway root package back. Here both
// directions are safe -- the same rule as modules/gateway/controlpath.
package gatewaystate

import (
	"encoding/json"
	"time"

	"github.com/rzbdz/newgate-ext/modules/config/domain"
	"github.com/rzbdz/newgate-ext/modules/config/store"
)

// Key is the gateway's field name in state.json's ModuleConfig.
const Key = "gateway"

// Config holds the gateway's runtime switches.
//
// Every field is "may be absent": SchemaRepair / SpecialTreatment / Fallback use pointers to
// tell "not set" from "explicitly off", and an empty SpecialOff slice equals nil. Absent =
// factory state (on).
type Config struct {
	// ParseErr is the error from parsing ModuleConfig[Key]. Non-nil means **every field below is
	// a fallback** (read from the old key, or fallen back to factory state), not the user's
	// current choice. The display layer must say so -- see status.go.
	ParseErr     error
	SchemaRepair *bool `json:"schema_repair,omitempty"`
	// SpecialTreatment is the special_treatment plugin layer's master switch (on by default).
	// Plugins apply only to the upstreams they claim (gateway/special), so on affects nobody else.
	SpecialTreatment *bool `json:"special_treatment,omitempty"`
	// SpecialOff names individually disabled plugins. When investigating "did newgate break the
	// request", turning one off is more precise than turning off the whole layer. Names are in
	// `newgate st`.
	SpecialOff []string `json:"special_treatment_off,omitempty"`

	// Fallback is the fallback chain master switch (on by default). Off = one request walks only
	// the chain head, no swapping, no timeout budget. Its meaning is on the forwarding side:
	// **it is global**, a different thing from `--profile=xx`'s "this call takes only this
	// profile", and the latter needs no switch (the URL path already pins the profile; see
	// forward.handleProxy).
	//
	// Why on by default: fallback is one of the reasons the gateway exists; shipping it off is
	// crippling the product. The pointer semantics match SchemaRepair: nil = not stated = on.
	Fallback *bool `json:"fallback,omitempty"`

	// Debug is the full request log (a per-request dump on the forwarding side). **It must be
	// time-limited**: one request body can exceed 8KB (opencode's system prompt alone is ~97KB),
	// and forgetting to turn it off fills the disk. Empty DebugUntil = explicitly permanent.
	//
	// Pointers make "never set" distinguishable from "explicitly off" -- the migration's
	// per-field fallback to legacy keys relies on exactly that distinction (only nil falls back).
	Debug *bool `json:"debug,omitempty"`
	// DebugUntil is RFC3339. Debug true with this past its time equals off (lazy expiry).
	DebugUntil string `json:"debug_until,omitempty"`
}

// legacy holds the pre-migration key names: these three switches used to be typed fields on
// domain.State, stored at state.json's **top level**. Since 2026-09-18 this module owns them.
//
// Why the legacy keys must be read: store is **lossless-preserving** for "unknown top-level
// keys" (see store.LoadState / SaveState), so legacy keys neither disappear on their own nor
// are seen by the new read path. Without reading them, a machine that explicitly ran
// `special_treatment: false` **silently goes back to all-on** after upgrading -- what the user
// turned off comes back, with no notice at all.
var legacy = struct{ SchemaRepair, SpecialTreatment, SpecialOff, Debug, DebugUntil string }{
	"schema_repair", "special_treatment", "special_treatment_off", "debug", "debug_until",
}

// Parse parses the gateway section. **It never fails**: no such section, or broken JSON, both
// count as "factory state" -- all on, no individually disabled plugins (fail-open: if it
// cannot be read, do not turn people's things off).
//
// During the migration it falls back to the legacy keys **per field**: read one only when the
// new section has **never set** that field.
//
// Why not "the new section exists, so no legacy reads at all": that silently drops settings in
// the window where "the new section is written, the legacy keys are not deleted yet". Measured
// and hit -- moving in two steps (schema-repair first, debug later), the first write created
// the new section, so the debug moved later could never read its old value, leaving a
// `debug: true` at state.json's top level that looks live while the gateway no longer reads it.
//
// Why per-field fallback cannot revive old values: legacy keys are **deleted on the first
// write** (see update), so the fallback window exists only for the upgrade itself; and
// already-set fields take no part in fallback, so after the user puts a switch back to factory
// state (the field explicitly written non-nil) old values cannot flip it back.
func Parse(st *domain.State) Config {
	if st == nil {
		return Config{}
	}
	var c Config
	if raw := st.ModuleConfig[Key]; len(raw) > 0 {
		// **A parse failure must be recorded, not swallowed** (2026-09-18): a broken section makes
		// every field below fall back to factory state, and an unset field's factory state is
		// "all on" -- so a patch the user turned off with `newgate st off deepseek` gets
		// **silently turned back on**, while the status line shows the same Parse, looking normal.
		//
		// Behavior is unchanged (still fail-open to factory state; a piece of bad JSON never stops
		// the gateway), but the error travels out with Config, for status / doctor to report as a warn.
		c.ParseErr = json.Unmarshal(raw, &c)
	}
	leg := legacyConfig(st)
	if c.SchemaRepair == nil {
		c.SchemaRepair = leg.SchemaRepair
	}
	if c.SpecialTreatment == nil {
		c.SpecialTreatment = leg.SpecialTreatment
	}
	if len(c.SpecialOff) == 0 {
		c.SpecialOff = leg.SpecialOff
	}
	if c.Debug == nil {
		c.Debug = leg.Debug
	}
	if c.DebugUntil == "" {
		c.DebugUntil = leg.DebugUntil
	}
	return c
}

func legacyConfig(st *domain.State) Config {
	var c Config
	if raw := st.ModuleConfig[legacy.SchemaRepair]; len(raw) > 0 {
		var v bool
		if json.Unmarshal(raw, &v) == nil {
			c.SchemaRepair = &v
		}
	}
	if raw := st.ModuleConfig[legacy.SpecialTreatment]; len(raw) > 0 {
		var v bool
		if json.Unmarshal(raw, &v) == nil {
			c.SpecialTreatment = &v
		}
	}
	if raw := st.ModuleConfig[legacy.SpecialOff]; len(raw) > 0 {
		var v []string
		if json.Unmarshal(raw, &v) == nil {
			c.SpecialOff = v
		}
	}
	if raw := st.ModuleConfig[legacy.Debug]; len(raw) > 0 {
		var v bool
		if json.Unmarshal(raw, &v) == nil {
			c.Debug = &v
		}
	}
	if raw := st.ModuleConfig[legacy.DebugUntil]; len(raw) > 0 {
		var v string
		if json.Unmarshal(raw, &v) == nil {
			c.DebugUntil = v
		}
	}
	return c
}

// Marshal encodes into the bytes to write into ModuleConfig.
func (c Config) Marshal() ([]byte, error) { return json.Marshal(c) }

// Below are the **read-side** convenience functions, taking `*domain.State` directly: call
// sites are all over the hot path, and having each Parse once is far clearer than requiring
// every caller to resolve first and pass it in (small JSON, the same order as claudecode's
// ParseNakedConfig per request).

// RepairEnabled adds "required": [] to tool schemas missing required.
// Per the JSON Schema spec that is a semantic no-op, so it is on by default.
func RepairEnabled(st *domain.State) bool {
	return Parse(st).repairOn()
}

// SpecialEnabled is whether the special_treatment layer is enabled. On by default.
func SpecialEnabled(st *domain.State) bool {
	return Parse(st).specialOn()
}

// PluginOff is whether a plugin is individually disabled.
func PluginOff(st *domain.State, name string) bool {
	return Parse(st).pluginOff(name)
}

// FallbackOff is whether fallback chains are globally off (`newgate fallback off`). False by
// default.
//
// The name says "off" and not "on": the forwarding side's only use is
// `if tgt.Profile != "" || FallbackOff(st) { truncate the chain }`,
// and an affirmative `FallbackEnabled` would make every call site read
// `!FallbackEnabled(st)` -- scattered negation is exactly where this kind of switch gets read
// backwards.
func FallbackOff(st *domain.State) bool {
	return Parse(st).fallbackOff()
}

// DebugActive is whether the full request log is on now. **Lazy expiry**: Debug true but past
// its time counts as off, with nobody needing to turn it off -- every check on the forwarding
// path naturally gets false.
//
// An unparsable timestamp counts as "on": better to record one extra stretch of log than to
// silently switch off something the user deliberately turned on because of a hand-broken
// timestamp.
func DebugActive(st *domain.State) bool {
	return Parse(st).debugActive()
}

func (c Config) debugActive() bool {
	if c.Debug == nil || !*c.Debug {
		return false
	}
	if c.DebugUntil == "" {
		return true // explicitly turned on forever
	}
	t, err := time.Parse(time.RFC3339, c.DebugUntil)
	if err != nil {
		return true
	}
	return time.Now().Before(t)
}

// DebugUntilDisplay is the raw expiry text, for status to show (empty = no time limit).
func DebugUntilDisplay(st *domain.State) string { return Parse(st).DebugUntil }

func (c Config) repairOn() bool  { return c.SchemaRepair == nil || *c.SchemaRepair }
func (c Config) specialOn() bool { return c.SpecialTreatment == nil || *c.SpecialTreatment }

func (c Config) fallbackOff() bool { return c.Fallback != nil && !*c.Fallback }

func (c Config) pluginOff(name string) bool {
	for _, n := range c.SpecialOff {
		if n == name {
			return true
		}
	}
	return false
}

// ---------- write side ----------

// SetSchemaRepair switches schema repair on or off.
func SetSchemaRepair(on bool) error {
	return update(func(c *Config) { c.SchemaRepair = &on })
}

// SetSpecialTreatment switches the whole special_treatment layer on or off.
func SetSpecialTreatment(on bool) error {
	return update(func(c *Config) { c.SpecialTreatment = &on })
}

// SetFallback switches fallback chains. Off = every gateway walks only the chain head.
func SetFallback(on bool) error {
	return update(func(c *Config) { c.Fallback = &on })
}

// SetDebug switches the full request log. Empty untilRFC3339 = no time limit.
func SetDebug(on bool, untilRFC3339 string) error {
	return update(func(c *Config) {
		c.Debug = &on
		c.DebugUntil = ""
		if on {
			c.DebugUntil = untilRFC3339
		}
	})
}

// SetSpecialPlugin turns one plugin on or off. Off = add to SpecialOff, on = remove from it.
func SetSpecialPlugin(name string, on bool) error {
	return update(func(c *Config) {
		out := make([]string, 0, len(c.SpecialOff))
		for _, n := range c.SpecialOff {
			if n != name {
				out = append(out, n)
			}
		}
		if !on {
			out = append(out, name)
		}
		if len(out) == 0 {
			out = nil
		}
		c.SpecialOff = out
	})
}

// update reads one state, changes the gateway section, writes it back. Read-modify-write as one
// unit, so two setters cannot overwrite each other (the same shape it had inside store).
func update(mutate func(*Config)) error {
	st := store.LoadState()
	c := Parse(st)
	mutate(&c)
	raw, err := c.Marshal()
	if err != nil {
		return err
	}
	if st.ModuleConfig == nil {
		st.ModuleConfig = map[string][]byte{}
	}
	st.ModuleConfig[Key] = raw
	// Delete the legacy keys explicitly. store's lossless preservation means they sit in
	// state.json forever unless deleted, and the next reader believes they still apply
	// (measured: after the migration the top level still held schema_repair: true). The new
	// value already landed in the Key section, so deleting loses no information.
	delete(st.ModuleConfig, legacy.SchemaRepair)
	delete(st.ModuleConfig, legacy.SpecialTreatment)
	delete(st.ModuleConfig, legacy.SpecialOff)
	delete(st.ModuleConfig, legacy.Debug)
	delete(st.ModuleConfig, legacy.DebugUntil)
	return store.SaveState(st)
}
