package locale

import (
	"encoding/json"

	"github.com/rzbdz/newgate-ext/modules/config/domain"
	"github.com/rzbdz/newgate-ext/modules/config/store"
)

// StateKey is the field name this module owns in state.json (registered with confighook by
// module.go).
//
// Why state.json: language is **this person's preference on this machine**, not fleet config --
// the same nature as plugin-manager's switch ledger (`domain.State`'s comment: machine-local
// runtime state, changed often, not in dotfiles). The config-sharing "shared layer + local layer"
// split lives in the distribution, while "which language I want to read" is local through and
// through.
const StateKey = "locale"

// persisted is the shape stored in this section. Left as a struct rather than a bare string: adding
// fields like "CLI language and log language set separately" later is adding a field here, not
// changing the format.
type persisted struct {
	Lang string `json:"lang"`
}

// loadConfigured reads the persisted language choice; empty string when never set or unreadable
// (= no opinion).
func loadConfigured() string {
	st := store.LoadState()
	if st == nil || st.ModuleConfig == nil {
		return ""
	}
	raw := st.ModuleConfig[StateKey]
	if len(raw) == 0 {
		return ""
	}
	var p persisted
	if err := json.Unmarshal(raw, &p); err != nil {
		// Undecodable counts as unset: one broken preference must not keep the CLI from starting.
		// Language is a **preference**, not part of config correctness -- the fail-open here is
		// deliberate.
		return ""
	}
	return p.Lang
}

// saveConfigured writes the language into state.json.
func saveConfigured(tag string) error {
	st := store.LoadState()
	if st == nil {
		st = &domain.State{}
	}
	if st.ModuleConfig == nil {
		st.ModuleConfig = map[string][]byte{}
	}
	raw, err := json.Marshal(persisted{Lang: tag})
	if err != nil {
		return err
	}
	st.ModuleConfig[StateKey] = raw
	return store.SaveState(st)
}
