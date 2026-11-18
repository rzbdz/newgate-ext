package opencodeomo

import (
	"bytes"
	"encoding/json"
	"io/ioutil"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/rzbdz/newgate-ext/modules/config/domain"
	"github.com/rzbdz/newgate-ext/modules/config/roleprov"
	i18n "github.com/rzbdz/newgate/lib/i18n"
)

// omo slots: the intra-agent key system of the opencode plugin oh-my-openagent.
//
// Why "translate each slot's model name into a tier by build" is not enough
//
// The old approach (ClassifyModel) swapped `agents.sisyphus.model` straight for `newgate/normal`.
// Two pieces of information were **lost** in that sentence:
//
//	one, slot identity. Once sisyphus and librarian both map to normal, the config file
//	   no longer tells them apart; when the user wants "sisyphus expensive, librarian cheap",
//	   there is nowhere to write it.
//	two, the user's intent. `gpt-5.6-sol` with `variant: max` and `claude-sonnet-5`
//	   bare may land on the same tier under the old approach, yet the strength they mean
//	   to express is not the same thing.
//
// New approach: at takeover, give each slot a **stable key** (omo-sisyphus / cat-deep) and write
// "who this key follows now" into omo-slots.json. So
//
//	the config file keeps the identity (newgate/omo-sisyphus), not the build;
//	the key → tier/chain mapping becomes one line of user-editable config (writing
//	  `"omo-sisyphus": ["@normal", "smt/terra"]` in the profile overrides the default);
//	core need not know omo -- it only sees a batch of dynamic role keys (domain.ExtraRole).
//
// Naming and defaults are decided by this file (the omo module); the framework only provides the
// mechanism.
const (
	// OmoAgentPrefix is the key prefix for intra-agent (agents.<name>).
	OmoAgentPrefix = "omo-"
	// OmoCatPrefix is the key prefix for task categories (categories.<name>).
	OmoCatPrefix = "cat-"
)

// Slot is one omo slot as it was before takeover.
type Slot struct {
	Kind      string // agent | category
	Name      string
	Model     string
	Variant   string
	Fallbacks []string // model names only (variant is moot once the chain is newgate's)
	// FallbackObjects: whether fallback_models' elements are objects ({"model":…,"variant":…}).
	// Write back in the original shape when rewriting; do not turn config omo recognizes into
	// something it does not.
	FallbackObjects bool
}

// Key is this slot's key name inside newgate.
func (s Slot) Key() string {
	if s.Kind == "category" {
		return OmoCatPrefix + s.Name
	}
	return OmoAgentPrefix + s.Name
}

// ---------- reading the original file ----------

// DiscoverSlots reads every slot out of oh-my-openagent.json.
//
// Pure read, no rewrite: before takeover it is used to compute key names (so the model ids can be
// registered into opencode.json's provider block ahead of time), after takeover to align with
// existing keys.
//
// A parse failure **must error**: once this returns empty silently, takeover becomes "report 0
// rewrites, config untouched" -- looking like success while nothing was done (really hit on
// 2026-09-16: fallback_models' elements were objects rather than strings, and the whole file failed
// to parse).
func DiscoverSlots(target string) ([]Slot, error) {
	if target == "" {
		return nil, nil
	}
	b, err := ioutil.ReadFile(target)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var root struct {
		Agents     map[string]omoNode `json:"agents"`
		Categories map[string]omoNode `json:"categories"`
	}
	if err := json.Unmarshal(b, &root); err != nil {
		return nil, i18n.Ef(err, "cannot parse {file}: {err}", i18n.A{"file": filepath.Base(target)})
	}
	var out []Slot
	for _, kind := range []string{"agent", "category"} {
		m := root.Agents
		if kind == "category" {
			m = root.Categories
		}
		names := make([]string, 0, len(m))
		for n := range m {
			names = append(names, n)
		}
		sort.Strings(names) // stable key order, so report and registry do not jump on every run
		for _, n := range names {
			node := m[n]
			if node.Model == "" {
				continue
			}
			sl := Slot{Kind: kind, Name: n, Model: node.Model, Variant: node.Variant}
			for i, f := range node.FallbackModels {
				if f.Model == "" {
					continue
				}
				sl.Fallbacks = append(sl.Fallbacks, f.Model)
				if i == 0 {
					sl.FallbackObjects = f.fromObject
				}
			}
			out = append(out, sl)
		}
	}
	return out, nil
}

type omoNode struct {
	Model          string   `json:"model"`
	Variant        string   `json:"variant"`
	FallbackModels []omoRef `json:"fallback_models"`
}

// omoRef is one element of fallback_models. omo uses both shapes:
//
//	"smt-codex/gpt-5.6-terra"                        a bare model name
//	{"model": "smt-codex/gpt-5.6-terra", "variant": "medium"}   with strength
//
// Both must be accepted, so it is decoded by hand -- typed as []string it would fail the whole
// file's parse, and a parse failure looks like "the takeover report says 0 rewrites", the hardest
// kind of silence to track down.
type omoRef struct {
	Model      string `json:"model"`
	Variant    string `json:"variant,omitempty"`
	fromObject bool
}

func (r *omoRef) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	if len(b) > 0 && b[0] == '"' {
		return json.Unmarshal(b, &r.Model)
	}
	type alias omoRef // guards against recursion
	var a alias
	if err := json.Unmarshal(b, &a); err != nil {
		return err
	}
	*r = omoRef(a)
	r.fromObject = true
	return nil
}

// OriginalSlots reads slots from the original file in backups/original/.
//
// Used to recover `was` on **re-takeover**: on the user's second `newgate on opencode`, the file on
// disk was rewritten to newgate/xxx long ago, and only the backup still remembers what model and
// what variant it originally was. Without that memory, the suggested value cannot be computed.
func OriginalSlots() []Slot {
	slots, _ := DiscoverSlots(originalPath(omoTargetPath()))
	return slots
}

func omoTargetPath() string {
	for _, t := range TargetFiles() {
		if strings.Contains(filepath.Base(t), "openagent") {
			return t
		}
	}
	return ""
}

// ---------- registry ----------

// OmoSlots is the slot registry (~/.config/newgate/omo-slots.json).
type OmoSlots struct {
	Version   int               `json:"version"`
	Source    string            `json:"source"`
	Updated   string            `json:"updated,omitempty"`
	Mode      string            `json:"mode,omitempty"`      // current (as-is, default) | suggested
	Overrides map[string]string `json:"overrides,omitempty"` // key -> tier name/@ref, highest priority
	Slots     []OmoSlot         `json:"slots"`
	Note      string            `json:"note,omitempty"`
}

// OmoSlot is one slot's registry entry.
type OmoSlot struct {
	Key          string   `json:"key"`
	Kind         string   `json:"kind"`
	Name         string   `json:"name"`
	Was          string   `json:"was,omitempty"` // the real model name before takeover
	WasFallbacks []string `json:"was_fallbacks,omitempty"`
	Variant      string   `json:"variant,omitempty"`
	Current      string   `json:"current"`             // current: its build at takeover time
	Suggested    string   `json:"suggested,omitempty"` // suggested: build + variant strength
	Default      string   `json:"default"`             // what actually takes effect (overrides > mode)
	Why          string   `json:"why,omitempty"`
}

// ReadOmoSlots reads the registry. Missing (omo never taken over) returns nil, not an error.
func ReadOmoSlots() *OmoSlots {
	var s OmoSlots
	b, err := ioutil.ReadFile(SlotsFile())
	if err != nil || len(b) == 0 {
		return nil
	}
	if err := json.Unmarshal(b, &s); err != nil {
		return nil
	}
	return &s
}

// SlotBinding is a slot's **actually effective** default binding (overrides > mode > current).
// The return value goes straight into domain.ExtraRole.Default, supporting "@reference" /
// "provider/model" / tier names.
func (s *OmoSlots) SlotBinding(key string) string {
	if s == nil {
		return ""
	}
	if v, ok := s.Overrides[key]; ok && v != "" {
		return v
	}
	for _, sl := range s.Slots {
		if sl.Key != key {
			continue
		}
		if s.Mode == "suggested" && sl.Suggested != "" {
			return sl.Suggested
		}
		return sl.Current
	}
	return ""
}

// SlotOf looks up a registry entry by key.
func (s *OmoSlots) SlotOf(key string) (OmoSlot, bool) {
	if s == nil {
		return OmoSlot{}, false
	}
	for _, sl := range s.Slots {
		if sl.Key == key {
			return sl, true
		}
	}
	return OmoSlot{}, false
}

// WriteOmoSlots writes the registry atomically.
func WriteOmoSlots(s *OmoSlots) error {
	if s == nil {
		return nil
	}
	s.Version = 1
	s.Source = "omo"
	s.Updated = time.Now().Format(time.RFC3339)
	s.Note = i18n.T("Generated automatically when oh-my-openagent is taken over. "+
		"default is the binding this key falls back to when the profile does not spell it out; "+
		"to move a slot to another tier, edit overrides (it accepts \"@other-key\" / \"tier name\" / \"provider/model\"), "+
		"or write this key directly in the profile.", nil)
	b, err := marshal(s)
	if err != nil {
		return err
	}
	// 0660: the registry must be readable by **the user running the daemon** (often not the one who
	// wrote it)
	return writeAtomicMode(SlotsFile(), b, 0o660)
}

// ---------- suggestions ----------

// Suggest computes the suggested tier from "what model it was before takeover, how hard it was
// pushed".
//
// Two steps: first give the build from the model name (ClassifyModel), then move one step on the
// ladder by variant -- max/xhigh means this slot aims at the strongest (opus+max suggests
// heavy, sonnet+max suggests normal), low/minimal means deliberately sparing, high/medium do not
// move.
//
// When the model name hits no rule (fuzzy classification), nothing is suggested: that was a guess
// to begin with, and stacking a variant on top only makes the guess sound more like a conclusion.
// The reason goes into why for the user to read.
func Suggest(model, variant string) (tier, why string) {
	if model == "" {
		return "", i18n.T("there was no concrete model name before takeover", nil)
	}
	t, exact := ClassifyModel(model)
	if !exact {
		return "", i18n.T("model name {model} matched no rule; the build itself is a guess",
			i18n.A{"model": model})
	}
	n := variantShift(variant)
	to := domain.ShiftTier(t, n)
	if n == 0 || to == t {
		// A reason is given even without a tier move. Otherwise a difference like "current heavy /
		// suggested normal" would be a blank on the UI, and nowhere to look when the user asks "why is
		// it not what I thought" (docs/04-configuration.md). The classification itself hit a rule, and
		// this sentence is that answer.
		if variant == "" {
			return t, i18n.T("decided by the model build; no variant was recorded before takeover", nil)
		}
		return t, i18n.T("decided by the model build; variant={variant} does not change the tier",
			i18n.A{"variant": variant})
	}
	// One message for up and one for down: making dir a separate message and embedding it in another
	// leaves translators facing "a word inside a translated sentence is also a table lookup", which
	// stops composing once the word order changes (see lib/i18n).
	if n < 0 {
		return to, i18n.T("variant={variant} shifts down one step ({from} → {to})",
			i18n.A{"variant": variant, "from": t, "to": to})
	}
	return to, i18n.T("variant={variant} shifts up one step ({from} → {to})",
		i18n.A{"variant": variant, "from": t, "to": to})
}

// ClearOmoSlots clears the slots when releasing a takeover, **keeping overrides**: the keys are
// gone, but the user's hand-written "which chain this slot should follow" must not be lost along
// with them -- the next takeover uses them again directly.
//
// A write failure must be reported: if the file is not cleared, those keys **stay**, and the next
// takeover treats them as "slots the user already chose" and uses them as is (see takeover's
// current reuse logic) -- so `newgate off opencode` followed by `on` again produces an assignment
// different from what the user expects, with no sign of why.
func ClearOmoSlots() error {
	prev := ReadOmoSlots()
	if prev == nil {
		return nil
	}
	if len(prev.Overrides) == 0 && len(prev.Slots) == 0 {
		return nil
	}
	return WriteOmoSlots(&OmoSlots{Mode: prev.Mode, Overrides: prev.Overrides})
}

func variantShift(v string) int {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "max", "xhigh":
		return 1
	case "low", "minimal":
		return -1
	default:
		return 0
	}
}

// ---------- role key source ----------

// omoRolesProvider reports the registry's slots to the framework.
//
// This is the step "a module registers its own sub agent key mechanism": the framework (roleprov +
// domain.ExtraRole + resolve's reference expansion) knows nothing about omo, it only gets a batch
// of "key → default binding". Should the omo plugin add a new agent some day, a re-takeover
// automatically adds a key, and core does not change one line.
type omoRolesProvider struct{}

var (
	_ roleprov.RoleProvider      = (*omoRolesProvider)(nil)
	_ roleprov.RoleWatchProvider = (*omoRolesProvider)(nil)
)

func (omoRolesProvider) Source() string { return "omo" }

func (omoRolesProvider) WatchFiles() []string { return []string{SlotsFile()} }

func (omoRolesProvider) Roles() ([]domain.ExtraRole, error) {
	s := ReadOmoSlots()
	if s == nil {
		return nil, nil // omo never taken over: contributes no keys
	}
	out := make([]domain.ExtraRole, 0, len(s.Slots))
	for _, sl := range s.Slots {
		if sl.Key == "" {
			continue
		}
		out = append(out, domain.ExtraRole{
			Key:     sl.Key,
			Source:  "omo",
			Default: s.SlotBinding(sl.Key),
			Meta: map[string]string{
				"kind":      sl.Kind,
				"name":      sl.Name,
				"was":       sl.Was,
				"variant":   sl.Variant,
				"current":   sl.Current,
				"suggested": sl.Suggested,
				"default":   s.SlotBinding(sl.Key),
			},
		})
	}
	return out, nil
}
