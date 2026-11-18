package config

import (
	"encoding/json"
	"errors"
	"os"
	"sort"

	"github.com/rzbdz/newgate-ext/lib/view"
	"github.com/rzbdz/newgate-ext/modules/config/domain"
	"github.com/rzbdz/newgate-ext/modules/config/paths"
	"github.com/rzbdz/newgate-ext/modules/config/resolve"
	"github.com/rzbdz/newgate-ext/modules/config/store"
	"github.com/rzbdz/newgate/lib/i18n"
)

// chainsConceptID is the identity this family of conclusions
// would use **if the kernel drew them as a card**.
//
// Today it appears in exactly one place: the Concept field of the CAS conflict object
// (see applyProfileRoles -> writeThrough). On the row-action path the conflict reaches the
// caller as only one plain sentence (see switchHead), so this string never drifts into the
// UI -- it is just a stable statement of "which card this is about", and it cannot be empty
// (an empty string would make everyone outside `errors.As` think it means "no concept").
const chainsConceptID = "config.chains"

// Chains is the read on the Config port (see the Config comment in api.go).
//
// # Why it is a mechanism rather than some UI's private business
//
// "What this tier resolves to right now, who was skipped, why" is resolve's conclusion,
// and resolve lives inside this module. Anyone who wants to lay that out (the `/ui` aggregate
// home page, a future tui, anywhere that wants to explain "why this exchange went to that
// provider") can compute it by importing store + resolve themselves -- there are two places in
// the distribution doing exactly that today -- but that is **copying an internal shape of the
// kernel**: when the kernel is refactored it breaks in another repository, and no test there
// goes red. So the port's shape is "give me the conclusion", not "give me the raw material".
//
// # Why it does not ask the health table
//
// Breaker removal and probe latency (`controlplane.Doc`'s Available/Rank) change over
// time, and are the state of **the running process**; this method reads **what is on disk right
// now**. Mix the two and the same config gives different answers "seen in the CLI" and "seen
// in the browser", with the difference coming from "who happened to be connected to the daemon"
// -- that kind of inconsistency is far worse than a little less information. So the chain's
// Tone only expresses "is there a chain head", not health (see ChainRow.Tone in lib/view).
func (p *port) Chains(profile string, keys ...string) (*Chains, error) {
	snap, err := store.Load()
	if err != nil {
		return nil, i18n.Ef(err, "cannot read the configuration: {err}", nil)
	}

	// Empty = the one globally in effect right now. **No per-agent projection**:
	// `Active[agent]` says "which chain some client starts from", while this asks
	// "what this file resolves to"; the two are different questions (the former is
	// the multi-chain-head view of `newgate tier`, which has its own in commands.go).
	name := profile
	if name == "" {
		name = snap.State.DefaultProfile
	}
	pr := profileNamed(snap, name)
	if pr == nil {
		// An error rather than an empty chain: the shape of an empty chain is "this tier
		// has nothing configured", while the fact is **there is no such tier**. Quietly
		// and the caller draws it as an empty card, and the reader thinks the config
		// was lost. (`store.Load` skips broken profile files, so this also covers the
		// "file is broken" case -- `newgate doctor` reports which file specifically.)
		return nil, i18n.E("no such profile: {name}", i18n.A{"name": name})
	}
	return buildChains(snap, pr, keys...), nil
}

// AllChains reports the chains every profile resolves to at this moment, the effective one first.
//
// # Why it and Chains are two methods, rather than letting the caller loop
//
// A loop is **ten store.Loads**, i.e. ten moments of config pieced together on **one screen**:
// the first few cards still see the old file, the later ones already see the new one, with
// nothing visibly odd in the UI (each card right on its own). This is the same rule as "give
// all keys at once", just one level more outward -- one snapshot, one moment per screen.
//
// # Why there is no separate "which profiles exist"
//
// The list is the index here: every entry the caller gets carries Profile, and the order is
// fixed too (the effective one first, the rest by name). One more method that reports only
// names would give the caller a chance to piece together the slower and less consistent
// "use the list to ask one by one" path -- exactly the path this method replaces.
func (p *port) AllChains(keys ...string) ([]*Chains, error) {
	snap, err := store.Load()
	if err != nil {
		return nil, i18n.Ef(err, "cannot read the configuration: {err}", nil)
	}
	out := make([]*Chains, 0, len(snap.Profiles))
	for _, pr := range orderedProfiles(snap) {
		out = append(out, buildChains(snap, pr, keys...))
	}
	return out, nil
}

// orderedProfiles sorts the tiers in the snapshot as "the effective one first, the rest by name".
//
// The sort criterion can only be the **name**, never "the profile's priority": that
// number is the order inside a chain (who is the alternative first), a different thing
// from "which one the user stands on now". Sorted by priority, the effective one would
// sit in the middle, while it is exactly what the eye looks for first on this screen.
func orderedProfiles(snap *store.Snapshot) []*domain.Profile {
	out := make([]*domain.Profile, len(snap.Profiles))
	copy(out, snap.Profiles)
	sort.SliceStable(out, func(i, j int) bool {
		di, dj := out[i].Name == snap.State.DefaultProfile, out[j].Name == snap.State.DefaultProfile
		if di != dj {
			return di
		}
		return out[i].Name < out[j].Name
	})
	return out
}

func profileNamed(snap *store.Snapshot, name string) *domain.Profile {
	for _, pr := range snap.Profiles {
		if pr.Name == name {
			return pr
		}
	}
	return nil
}

// buildChains turns every tier of one profile into a chain.
//
// Empty keys = follow domain.Roles (heavy -> normal -> mid -> light, plus vision). The
// order is decided by **the caller** (pass it in and it is returned as is): who comes
// first is a product trade-off -- `newgate status` and doctor both sort most capable
// first, and the UI follows the same; the incoming order is the only place it can
// express that. The default set is only "a reasonable answer when there is no opinion".
func buildChains(snap *store.Snapshot, pr *domain.Profile, keys ...string) *Chains {
	want := keys
	if len(want) == 0 {
		want = domain.Roles
	}
	out := &Chains{Profile: pr.Name, Default: pr.Name == snap.State.DefaultProfile}

	// The source text (extends not merged) is read only for **actions**: the reorderable candidates
	// are the ones written in this file, while the merged view mixes in the parent tier's candidates
	// and references expanded from other keys -- writing an order derived from it back is wrong.
	//
	// Unreadable means no actions (this read itself still returns as usual): fail-open, a
	// file deleted at the same time should not make the whole screen of chains disappear.
	raw, file, _ := rawFor(pr.Name)

	for _, key := range want {
		// MaxSteps: 0 -- **no truncation**. maxAttempts is the execution limit of one
		// request, not the chain's membership (measured 2026-09-17: ark is the 4th stop,
		// and passing Attempts() silently trimmed it, so the screen looked like "there
		// is no ark in the normal tier at all"). The fact of truncation is stated by
		// a hint line on the command-line side, not by cutting the chain itself short.
		steps, skips := resolve.BuildChain(key, snap.Profiles, snap.Providers, resolve.Opts{
			Active:   pr.Name,
			MaxSteps: 0,
		})
		c := Chain{Key: key, Steps: steps, Skips: skips}
		if raw != nil {
			c.Actions = chainActions(pr.Name, key, file, raw.Roles[key])
		}
		out.Keys = append(out.Keys, c)
	}
	return out
}

// rawFor gets the source text of one profile and which
// file it lands in (relative to the config root).
func rawFor(name string) (*domain.Profile, string, error) {
	raw, err := store.LoadProfileRaw(name)
	if err != nil {
		return nil, "", err
	}
	file, err := profileFile(name)
	if err != nil {
		return nil, "", err
	}
	return raw, file, nil
}

// chainActions reports the set of buttons "swap this tier's chain head to X".
//
// The candidates are **only the ones this profile itself writes under this tier** (own is that line
// in the source text): other stops on the chain are either fallbacks supplied by another profile,
// or references expanded out (written literally under **a different key**). Reordering this
// tier does not reach them, and a button that does nothing when pressed is worse than no button.
//
// **The first candidate gets no button**: it is the chain head right now,
// and clicking it changes nothing -- the same trade-off as codex's pair of mode
// buttons and the tier card's pair of applies (drawing the one already in effect as
// a button only stands next to and drowns out the one that really does something).
//
// The label is the binding itself (`ark/deepseek-v3`), not "switch to..."
// -- the whole context of this row is "switch to whom", and a binding
// reads shorter than an action description, and needs no translation.
func chainActions(profile, key, file string, own domain.Candidates) []view.Action {
	if len(own) < 2 {
		return nil
	}
	out := make([]view.Action, 0, len(own)-1)
	for _, bd := range own[1:] {
		target := bd.String()
		out = append(out, view.Action{
			ID:    "head:" + target,
			Label: func() string { return target },
			Run:   func() (string, error) { return switchHead(profile, key, file, target) },
		})
	}
	return out
}

// switchHead reorders this tier's candidates to "target
// first, the rest in their original order" and writes to disk.
//
// # Why it takes no parameters, and where the baseline comes from
//
// By design, row actions **take no parameters** (see Action.Run in lib/view): all the UI can offer
// is "the user clicked this button", while "which profile's which tier, and switch to whom" was
// settled when the closure was built. So the baseline cannot come from the UI's copy either -- it
// can only **read once, CAS once** here: the window in between is covered by the CAS (someone wrote
// in between -> StaleError, we write not one byte, and report a sentence asking them to retry).
//
// # Why it does not assemble the serialization itself
//
// It takes **the same write path** as the tier card (applyProfileRoles): whether that
// file is kv or json, which fields must not be touched, how unknown keys are preserved,
// has only one answer. The cost is one property that path already has, copied along:
// what is submitted is **the whole roles** (that path wants the complete table), so a
// single candidate written as an object in .json gets normalized into an array -- not
// newly introduced here; changing one tier on the tier card gives the same result.
func switchHead(profile, key, file, target string) (string, error) {
	raw, err := store.LoadProfileRaw(profile)
	if err != nil {
		return "", err
	}
	own := raw.Roles[key]
	moved := -1
	for i, bd := range own {
		if bd.String() == target {
			moved = i
			break
		}
	}
	if moved < 0 {
		return "", i18n.E("{tier} in {profile} has no candidate called {target} any more — reload the page and try again",
			i18n.A{"tier": key, "profile": profile, "target": target})
	}
	if moved == 0 {
		return "", nil // 已经是链头。界面上那个按钮本该已经被这一条挡住（chainActions 跳过了第一个）
	}

	next := make(domain.Candidates, 0, len(own))
	next = append(next, own[moved])
	next = append(next, own[:moved]...)
	next = append(next, own[moved+1:]...)

	roles := make(map[string][]bindingData, len(raw.Roles))
	for k, c := range raw.Roles {
		roles[k] = bindingDataOf(c)
	}
	roles[key] = bindingDataOf(next)
	edit, err := json.Marshal(struct {
		Roles map[string][]bindingData `json:"roles"`
	}{Roles: roles})
	if err != nil {
		return "", err
	}

	disk, err := os.ReadFile(file)
	if err != nil {
		return "", err
	}
	if _, err := applyProfileRoles(chainsConceptID, file, edit, store.RevisionOf(disk)); err != nil {
		// Here a conflict is **one sentence** rather than that two-column dialog: the
		// apply path can pop a dialog because the request carries the draft the UI holds
		// and the baseline (both source texts are there). A row action has neither -- the
		// user clicked a button, not a draft, so all that can be offered is "try again".
		var cf *view.Conflict
		if errors.As(err, &cf) {
			return "", i18n.E("{file} changed on disk after this page read it — reload and try again",
				i18n.A{"file": paths.RelToRoot(file)})
		}
		return "", err
	}
	return "", nil
}

func bindingDataOf(c domain.Candidates) []bindingData {
	out := make([]bindingData, 0, len(c))
	for _, b := range c {
		out = append(out, bindingData{Provider: b.Provider, Model: b.Model, Ref: b.Ref})
	}
	return out
}
