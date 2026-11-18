package pluginmanager

import (
	"encoding/json"
	"time"
)

// StateKey is this module's field name in state.json (registered with confighook by module.go).
//
// **Machine-local** is right: switches are live debugging actions, not fleet config. The
// shared config layer does not manage state.json, so machine A's off never drifts to B.
const StateKey = "plugin_manager"

// Entry is the record of one explicit setting.
type Entry struct {
	// Until is this setting's expiry time (RFC3339, zero value = no limit). **Lazy expiry**:
	// nothing scans it in the background - the read just compares the current time. So the
	// daemon holds no timer, and a restart leaves nothing behind.
	Until time.Time `json:"until,omitempty"`
}

// Config is the shape of the plugin_manager field in state.json.
//
// The two tables are **independent**, not one map plus a polarity field:
//
//   - Off is the kill switch for "factory on, user turned it off";
//   - On is the mode for "factory off, user opened it".
//
// A Path is in only one, chosen by the CLI from Switch.Default. All the payoff is on the
// hot path: Off() and On() read only their own table, **no registry lookup**, so the
// forwarding loop does one map lookup. The cost: two JSON tables, but they self-explain.
type Config struct {
	Off map[string]Entry `json:"off,omitempty"`
	On  map[string]Entry `json:"on,omitempty"`
}

// Parse parses the raw bytes in state.json.
//
// **Never fails**: bad JSON, missing fields, wrong types all mean "the user set nothing".
// This is where fail-open lands - unreadable, work as usual, never disable a feature over
// bad config. Same as modules/claudecode's ParseNakedConfig (that note records two bugs
// born from diverging expiry checks, so there is one parse entry and every read uses it).
func Parse(raw []byte) Config {
	var c Config
	if len(raw) == 0 {
		return c
	}
	if json.Unmarshal(raw, &c) != nil {
		return Config{}
	}
	return c
}

// Marshal serializes into the bytes written back to state.json.
func (c Config) Marshal() ([]byte, error) { return json.Marshal(c) }

// lookup finds one setting in the given table and does the lazy expiry check.
//
// Returns (in effect, expiry). Expired counts as never set - **and is not deleted**:
// deleting needs a disk write, and the hot path is read-only. A leftover expired entry
// costs one extra JSON line, and the CLI's next write sweeps it away (see Config.Prune).
func lookup(table map[string]Entry, path string) (bool, time.Time) {
	e, ok := table[path]
	if !ok {
		return false, time.Time{}
	}
	if !e.Until.IsZero() && !time.Now().Before(e.Until) {
		return false, time.Time{}
	}
	return true, e.Until
}

// Prune drops expired settings, for the CLI to sweep before each write (the hot path does not).
func (c Config) Prune() Config {
	out := Config{}
	keep := func(dst *map[string]Entry, src map[string]Entry) {
		for path, e := range src {
			if !e.Until.IsZero() && !time.Now().Before(e.Until) {
				continue
			}
			if *dst == nil {
				*dst = map[string]Entry{}
			}
			(*dst)[path] = e
		}
	}
	keep(&out.Off, c.Off)
	keep(&out.On, c.On)
	return out
}
