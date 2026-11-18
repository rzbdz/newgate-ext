package pluginmanager

import (
	"time"

	"github.com/rzbdz/newgate-ext/modules/config/domain"
	"github.com/rzbdz/newgate/lib/i18n"
)

// SetSwitch writes one switch point's desired state into state (**not persisted**:
// persisting, and "notify the data plane?", are the caller's call: CLI and web differ).
//
// It is the **one** semantics shared by `newgate plugin <path> on|off` and the browser UI,
// because it is full of places that are easy to get wrong a second time:
//
//   - The factory state picks the table: Default=true is a kill switch (a user "off"
//     goes into the Off table); Default=false is a mode (a user "on" goes into On);
//   - **An action equal to the factory state = undo the setting** (saying on to a
//     factory-on point means pulling it out of the Off table), not writing another "on" -
//     get it backwards and a factory-state change flips what the old action means;
//   - A footgun does not accept "forever", and its declared TTL is the fallback limit
//     when no duration is given (enforced > 0 at registration, see Switch.TTL).
//
// The criterion is "would the two UIs give different answers": get any of the three wrong
// and the user sees the CLI say off while the browser says on. So they are written once, here.
func SetSwitch(st *domain.State, sw Switch, on bool, ttl time.Duration, forever bool) error {
	if sw.Danger == DangerFootgun && forever {
		return i18n.E(
			"plugin: {path} is a footgun, forever is not accepted (give a duration, e.g. 5m)",
			i18n.A{"path": sw.Path})
	}
	cfg := Parse(rawConfig(st)).Prune()
	wantOff := sw.Default != on
	if wantOff {
		cfg.Off = setEntry(cfg.Off, sw.Path, effectiveTTL(sw, ttl, forever))
	} else {
		cfg.Off = dropEntry(cfg.Off, sw.Path)
	}
	if !wantOff && !sw.Default {
		cfg.On = setEntry(cfg.On, sw.Path, effectiveTTL(sw, ttl, forever))
	} else {
		cfg.On = dropEntry(cfg.On, sw.Path)
	}
	raw, err := cfg.Marshal()
	if err != nil {
		return err
	}
	if st.ModuleConfig == nil {
		st.ModuleConfig = map[string][]byte{}
	}
	st.ModuleConfig[StateKey] = raw
	return nil
}

// effectiveTTL is the duration to record: the user's if given, else the declared default.
func effectiveTTL(sw Switch, ttl time.Duration, forever bool) time.Time {
	if forever {
		return time.Time{}
	}
	if ttl == 0 {
		ttl = sw.TTL
	}
	if ttl == 0 {
		return time.Time{}
	}
	return time.Now().Add(ttl)
}
