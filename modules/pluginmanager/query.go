package pluginmanager

import (
	"time"

	"github.com/rzbdz/newgate-ext/modules/config/domain"
)

// rawState fetches the raw bytes of this module's section in state.json. A nil state
// (common in tests) or a missing field returns nil, which Parse reads as "never set".
func rawState(st *domain.State) []byte {
	if st == nil {
		return nil
	}
	return st.ModuleConfig[StateKey]
}

// Off reports whether this switch point was explicitly turned off. **Default false**:
// untouched = not off. Used on Default=true points (factory on, user may kill).
//
// Purity is a hard rule, not style: it runs on the forwarding hot path on every
// request, so **only the config snapshot passed in** is read - no lock, no IO, no
// registry lookup, no file.
//
// "No registry lookup" is not only speed: the registry is process-global, so a lookup
// ties the hot path to assembly order and adds a read lock per request. The pure read
// also keeps a seam: a shared layer (overlay + machine-local, local first) can govern a
// fleet by feeding the snapshot, functions unchanged - read files yourself and it is welded shut.
func Off(st *domain.State, path string) bool {
	on, _ := lookup(Parse(rawState(st)).Off, path)
	return on
}

// On reports whether this switch point was explicitly turned on. **Default false**:
// untouched = not on. Used on Default=false points (factory off, user opens it
// explicitly, e.g. gateway.passthrough).
// Same purity requirement as Off, same reasons.
func On(st *domain.State, path string) bool {
	on, _ := lookup(Parse(rawState(st)).On, path)
	return on
}

// Remaining says how long this setting still has. Zero = no limit, or never set.
//
// For CLI and status display ("auto-off in 42s"). Not on the hot path, but it still
// goes through the same parse entry - the 2026-09 lesson was "diverging expiry
// checks": a module's own check disagreed with the parser's, one config two answers.
func Remaining(st *domain.State, path string) time.Time {
	cfg := Parse(rawState(st))
	if on, until := lookup(cfg.Off, path); on {
		return until
	}
	if on, until := lookup(cfg.On, path); on {
		return until
	}
	return time.Time{}
}
