// Package verify uses **model fingerprints** to answer what a config UI never could:
//
//	this binding says gpt-5.6-terra - is the upstream really using it right now?
//
// # What it is
//
// Fingerprinting does not look at the model's self-reported name (it echoes what you wrote);
// it makes the upstream do **only a model can do**: emit a few hundred integers 1-355.
// Different models have stable statistical quirks in picking numbers, and comparing them
// against the reference bank ranks "who this looks like" (see https://lm.ikale.io/docs/sdk).
// This module exposes that as a newgate command (`newgate verify`) and a web screen.
//
// # Why it lives in the distribution
//
// The reference bank and identity judgment are **product choices**: the kernel's probe only
// answers "reachable? which dialect?", which is mechanism; "is this upstream who it claims"
// depends on which bank you trust and how a mismatch is reported. The kernel must not know
// lm.ikale.io, nor any vendor's model catalog service. So the kernel only gained a shared
// "profile → deduped upstreams" list (config/targets) used by both; the rest is all here.
//
// # It does not know the UI
//
// Same as modellist: with a UI, register the extension endpoint and mount this section into
// the concept ledger; without (`dist-simple-cli.json`, terminal only) the command still works.
// The arrow only goes "this module → UI".
package verify

import (
	"time"
)

// Name is this module's name in the assembly table and in `newgate plugin` (= the dir name).
const Name = "verify"

// Endpoint is the name it registers on the web BFF: `POST /ui/api/ext/fingerprint`.
//
// Deliberately **not the module name**: the front end knows "a fingerprint endpoint",
// not "a verify module" (see web-dashboard's extension endpoint note). Not translated.
const Endpoint = "fingerprint"

// Match is the relation between the model name the fingerprint gives and the one on the binding.
//
// It is a machine tag (the front end colors by it, the CLI warns by it), not translated.
// Human wording lives in each side's own vocabulary.
type Match string

const (
	// MatchOK names are identical (after removing separators).
	MatchOK Match = "match"
	// MatchDated one has a date/snapshot suffix over the other: another build of the same model.
	MatchDated Match = "dated"
	// MatchFamily another model in the same family (say gpt-5.5 written, fingerprint says gpt-5.6).
	MatchFamily Match = "family"
	// MatchMismatch names do not match, and are not the same family.
	MatchMismatch Match = "mismatch"
	// MatchUnknown no usable ranking this round (bad samples, too few samples, not model output).
	MatchUnknown Match = "unknown"
	// MatchError even the reference bank failed to load, or the provider config is unusable.
	MatchError Match = "error"
)

// Warning reports whether this conclusion is **worth a human look**.
//
// `dated` does not count: a snapshot build of the same model is normal, and warning on it
// would drown real mismatches in noise.
func (m Match) Warning() bool {
	switch m {
	case MatchFamily, MatchMismatch, MatchUnknown, MatchError:
		return true
	}
	return false
}

// Candidate is one candidate from the reference bank (score high to low).
type Candidate struct {
	Model       string   `json:"model"`
	DisplayName string   `json:"display_name,omitempty"`
	Family      string   `json:"family,omitempty"`
	FamilyName  string   `json:"family_name,omitempty"`
	Score       float64  `json:"score"`
	Probability *float64 `json:"probability,omitempty"`
}

// Entry is one binding's fingerprint conclusion. It is also **the on-disk cache** shape.
//
// The cache is not to save tokens (verify is explicit, every run really calls), but **the
// cell in the UI**: when the model dropdown opens it must show "who was verified last time",
// not blank just because the button was not pressed this time.
type Entry struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
	// API is the request format actually used (responses / chat_completions / messages).
	API string `json:"api,omitempty"`
	// Predicted is the bank's top-ranked model ID; empty = no ranking this round.
	Predicted     string `json:"predicted,omitempty"`
	PredictedName string `json:"predicted_name,omitempty"`
	Family        string `json:"family,omitempty"`
	FamilyName    string `json:"family_name,omitempty"`
	// Decision is the bank's verdict: scored / partial / unscorable / not_model.
	Decision string `json:"decision,omitempty"`
	// Probability is the confidence of the top of the closed set; null for partial (omitted in JSON).
	Probability *float64    `json:"probability,omitempty"`
	Match       Match       `json:"match"`
	UsedOutputs int         `json:"used_outputs,omitempty"`
	Results     []Candidate `json:"results,omitempty"`
	CheckedAt   time.Time   `json:"checked_at"`
	Build       string      `json:"build,omitempty"`
	Error       string      `json:"error,omitempty"`
	// CacheError is "this round verified fine, but the conclusion could not be saved" - does
	// not affect this result, but decides whether the next UI visit sees it, so it must be said.
	CacheError string `json:"cache_error,omitempty"`
}

// RoleState is one row of the config UI: which binding one profile's one tier resolved to.
type RoleState struct {
	Role     string `json:"role"`
	Provider string `json:"provider,omitempty"`
	Model    string `json:"model,omitempty"`
	Err      string `json:"error,omitempty"`
}

// ProfileState is all tiers of one profile.
type ProfileState struct {
	Name  string      `json:"name"`
	Roles []RoleState `json:"roles"`
}

// ProviderState is the material for the upstream selector. **No credentials**: the key is
// only used in the verify request, and not one byte belongs in a UI payload.
type ProviderState struct {
	Name     string   `json:"name"`
	Protocol string   `json:"protocol,omitempty"`
	BaseURL  string   `json:"base_url,omitempty"`
	HasKey   bool     `json:"has_key"`
	Models   []string `json:"models,omitempty"`
}

// State is all the material for this screen: config view + last fingerprints + bank state.
type State struct {
	Profiles  []ProfileState  `json:"profiles"`
	Providers []ProviderState `json:"providers"`
	Entries   []Entry         `json:"entries"`
	// Build is the reference bank's built_at (data version); empty = never loaded.
	Build string `json:"build,omitempty"`
	// Loaded says whether this process has read the reference bank in (the first verify
	// downloads about 3.6 MB and caches it).
	Loaded bool `json:"loaded"`
	// DataCached says whether the bank's cache file is on this machine. It explains "the
	// first verify downloads" - if the file is already there it is just parsing, no drama.
	DataCached bool `json:"data_cached"`
	// DataCheckedAt is when the bank cache was last downloaded/checked (the file's mtime).
	// The UI uses it to say "how old is the ruler"; nil = no cache.
	DataCheckedAt *time.Time `json:"data_checked_at,omitempty"`
	// Error is the overall error reading config/cache. The page still opens (free verification
	// still works), but the reason must be written on the panel.
	Error string `json:"error,omitempty"`
}

// RunRequest is the input of one single-point verification.
type RunRequest struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
	// API empty/auto = pick by the provider's protocol (anthropic → messages, others →
	// chat_completions). It can also be given explicitly.
	API string `json:"api,omitempty"`
	// Count is the number of challenges (1-3). 0 = 3 (the bank's value; 3 calibrates the probability).
	Count int `json:"count,omitempty"`
}
