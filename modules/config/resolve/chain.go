// Package resolve is the resolution pipeline: given
// a config snapshot, compute a tier's fallback chain.
//
// All pure functions -- external facts (breaker state, disable rules, the clock) are passed in as
// arguments. This way "given this config, what chain should resolve" can be tested fully offline.
package resolve

import (
	"sort"
	"strings"

	"github.com/rzbdz/newgate-ext/modules/config/domain"
	"github.com/rzbdz/newgate/lib/i18n"
)

// Step one step on the chain: which profile's which binding.
type Step struct {
	Profile  string
	Binding  domain.Binding
	Provider domain.Provider
}

func (s Step) String() string { return s.Profile + ":" + s.Binding.String() }

// Skip why some candidate did not make the chain. For the explain view and X-Newgate-Chain.
//
// Recording skip reasons is not a flourish: the only question the user will ask is "why not
// the one I thought", and without this it can only be guessed (docs/04-configuration.md).
type Skip struct {
	Profile string
	Target  string // provider/model; empty means the whole profile was skipped
	// Reason the sentence shown to the user, already through i18n.T.
	Reason string
	// Kind this skip's **category**: a machine marker, not
	// changing with language (see the constant set below).
	//
	// Why separate from Reason: the display layer groups by category (`newgate tier`'s "N candidates
	// skipped" counts by reason, and so does the status block's tail line), and the grouping
	// criterion cannot be that human sentence -- the sentence is translated, and matching an English
	// substring under Chinese would just drop every category into "other". The category is settled
	// when the chain is built (here); the wording is looked up by category in the display layer.
	Kind string
}

// The values of Skip.Kind. They are **machine markers**: the criterion for grouping,
// counting and sorting; the human wording is looked up by category in the display layer
// (see skipLabel / skipDetail in modules/config), and not one word of it is in this layer.
//
// Why this layer can tell them apart: **which gate** rejected this candidate
// is unambiguous here (role undefined / provider undefined / no key / disabled /
// the health table says unavailable / over maxAttempts / duplicate / cycle), while
// **why** it cannot be used (the sentence the admission callback returned) belongs
// to whoever provides the criterion. The two are separated exactly for this:
// resolve recognizes only the shape, not anyone's vocabulary (see Opts.Available).
const (
	// SkipExcluded the profile is marked excluded: does not
	// join the chain automatically, only by explicit selection.
	SkipExcluded = "excluded"
	// SkipUndefined this level does not offer this role; also used for "the candidate's provider
	// is undefined" (the two were originally in the same column, only the wording differed).
	SkipUndefined = "undefined"
	// SkipNoKey the provider has no api_key.
	SkipNoKey = "no-key"
	// SkipUnavailable the admission callback (health table
	// etc.) says this candidate cannot be used right now.
	SkipUnavailable = "unavailable"
	// SkipDisabled explicitly disabled.
	SkipDisabled = "disabled"
	// SkipMaxSteps the chain exceeded maxAttempts, and the tail did not make it.
	SkipMaxSteps = "max-attempts"
	// SkipDuplicate duplicates a candidate earlier on the chain.
	SkipDuplicate = "duplicate"
	// SkipCycle references form a cycle, skipped.
	SkipCycle = "cycle"
	// SkipPinned the call is pinned to one profile, so this profile's candidates never join.
	// Distinct from SkipExcluded (a config flag) and from SkipOther (a pinned *chain head*): here
	// the pin is about this request, and the user-visible story is "you asked for claude".
	SkipPinned = "pinned"
	// SkipHidden this profile is not visible to the agent this chain is for (its
	// visible-agents/hidden-agents declaration). Unlike SkipExcluded this one also applies to
	// the chain head: "invisible to X" that an explicit /p/<name> can bypass is decoration, and
	// the failure it prevents (a Gemini body reaching a Claude upstream) is exactly what the
	// user was trying to rule out.
	SkipHidden = "hidden"
	// SkipOther the catch-all: the display layer's category table has no column
	// for it (e.g. "chain head pinned, does not fall through" -- that class
	// has no column of its own today, see skipKindOrder in modules/config).
	SkipOther = "other"
)

// Opts the external inputs needed to build a chain.
// All injected as functions, keeping BuildChain pure.
type Opts struct {
	// Active the chain head. Per-agent: different agents can have different chain heads.
	Active string
	// Agent the client this chain is being built for ("" = the request names no agent, so
	// profile visibility is not filtered). It only feeds Profile.VisibleToAgent: a profile can
	// declare "only this agent sees me" / "these agents never see me", and building a chain that
	// ignores that is how a Gemini-dialect request used to fall through into a Claude-bound profile.
	Agent string
	// Available the availability judgment. When it returns false, the second
	// value says **why** it cannot be used, and that reason goes into Skip.Reason
	// verbatim for the user (`newgate tier`'s "why not the one I thought").
	//
	// Why "return the reason" rather than letting resolve write "circuit open" itself: the admission
	// criterion belongs to whoever provides it (today only the health table, but it could be quota,
	// canary, time of day), and resolve recognizes only this **shape**, not anyone's vocabulary.
	//
	// nil = all available.
	Available func(provider, model string) (bool, string)
	// Rank the health tier from the latest probe. Reorders only the fallbacks
	// after the chain head; smaller value is preferred, and equal values
	// keep the profile/list original order. nil = no dynamic reordering.
	Rank func(provider, model string) int
	// Disabled the disable judgment. nil = none disabled. target looks like "provider/model".
	Disabled func(tier, target string) (bool, string)
	// MaxSteps 0 = unlimited.
	MaxSteps int
	// Pinned the chain may not leave this profile. Empty = not pinned.
	//
	// Why this is separate from MaxSteps=1 and from the caller's `steps[:1]` truncation: both of
	// those act on an **already built** chain, so the damage is done by then. The scene
	// (2026-11-19): the chain head was claude, but a probe had delisted all four of claude's
	// bindings, so the head candidate was "unavailable" and the builder simply walked on into the
	// next profile -- the user asked for claude, `newgate fallback off`, and still got DeepSeek,
	// with every mechanism reporting success. Truncating afterwards cannot help: the head is
	// already somebody else's. Pinning is the only place the decision can be made, because it is
	// the only place that knows **which profile** each candidate came from.
	//
	// An empty pinned chain is a **legitimate, explainable** result ("every candidate of this
	// profile is delisted right now"), and the caller must report it as such rather than as
	// "unknown model" -- which is why it comes back as skips, not as nil.
	Pinned string
}

// BuildChain constructs the fallback chain for some key.
//
// The "key" may be a tier (heavy/normal/mid/light/vision) or a dynamic role contributed by
// a module (omo-sisyphus, see domain.ExtraRole); the resolution path is exactly the same.
//
// Order: **profile is the main order (chain head first, the rest by priority), list
// is the within order**. The semantics are "look for an alternative within the current task's
// key first, and only drop to the next profile when there is none" -- "I want the cheap one"
// should pick among the cheap ones first, not jump to the expensive one on the first failure.
//
// Rules:
//   - the chain head = Opts.Active; the rest sorted by (priority,
//     name), with name as tiebreak to guarantee reproducibility
//   - an excluded profile does not join the chain, unless
//     it is the chain head (explicit selection wins)
//   - when the chain head is pinned the chain is only itself
//     (including its internal list), no falling through
//   - when Opts.Pinned names a profile, **no other profile contributes a candidate** -- if that
//     profile has nothing usable the chain is empty, with the skips explaining why
//   - profiles may be sparse: without this key the whole level is skipped
//     (with an alias, the alias goes first, see domain.Profile.CandidatesFor)
//   - a **reference** in the candidates (`@normal`) is expanded
//     in place into that chain, depth first, with cycle detection
//   - (provider, model) deduped across the whole chain, tried once
//
// The expanded result is one flat fallback sequence: all profiles, all candidates (including
// expanded references) laid out by "profile priority -> list order". References introduce no
// second fallback semantics, they only let keys point at each other. See docs/04-configuration.md.
func BuildChain(key string, profiles []*domain.Profile, provs *domain.Providers, o Opts) ([]Step, []Skip) {
	ordered, skips := orderProfiles(profiles, o.Active, o.Agent)
	if o.Pinned != "" {
		ordered, skips = pinnedOnly(ordered, skips, o.Pinned)
	}
	b := &chainBuilder{profiles: ordered, provs: provs, o: o,
		seen: map[string]bool{}, visiting: map[string]bool{key: true}, skips: skips}
	b.expand(key)
	if o.Rank != nil && len(b.steps) > 2 {
		sort.SliceStable(b.steps[1:], func(i, j int) bool {
			a, z := b.steps[i+1], b.steps[j+1]
			return o.Rank(a.Binding.Provider, a.Binding.Model) <
				o.Rank(z.Binding.Provider, z.Binding.Model)
		})
	}
	b.steps, b.skips = capSteps(b.steps, b.skips, o.MaxSteps)
	return b.steps, b.skips
}

// capSteps cuts the chain to maxAttempts (<=0 =
// unlimited), leaving a skip reason for each cut step.
//
// A separate function because ResolveRequest also needs to reverse-look-up a concrete model
// name **without truncation**: the name may come after maxAttempts, and truncating first would
// judge a model that plainly exists in the config as "unknown" (the scene on 2026-11-04). There
// it is "build the whole thing -> move the head -> truncate again", sharing this same finishing
// passage with "build the whole thing -> truncate" here, so the wording does not drift.
func capSteps(steps []Step, skips []Skip, max int) ([]Step, []Skip) {
	if max <= 0 || len(steps) <= max {
		return steps, skips
	}
	for _, s := range steps[max:] {
		skips = append(skips, Skip{Profile: s.Profile, Target: s.Binding.String(),
			Kind:   SkipMaxSteps,
			Reason: i18n.T("past maxAttempts={limit}", i18n.A{"limit": max})})
	}
	return steps[:max], skips
}

type chainBuilder struct {
	profiles []*domain.Profile
	provs    *domain.Providers
	o        Opts
	seen     map[string]bool
	visiting map[string]bool // keys being expanded right now (guards against reference cycles)
	steps    []Step
	skips    []Skip
}

// expand expands key's candidates into steps by profile priority, list order.
func (b *chainBuilder) expand(key string) {
	for _, p := range b.profiles {
		cands := p.CandidatesFor(key)
		if len(cands) == 0 {
			b.skips = append(b.skips, Skip{Profile: p.Name, Kind: SkipUndefined,
				Reason: i18n.T("role {role} is not defined", i18n.A{"role": key})})
			continue
		}
		for _, bd := range cands {
			if bd.IsRef() {
				if b.visiting[bd.Ref] {
					b.skips = append(b.skips, Skip{Profile: p.Name, Target: bd.String(),
						Kind: SkipCycle,
						Reason: i18n.T("reference cycle ({from} → {to}), skipped",
							i18n.A{"from": key, "to": bd.Ref})})
					continue
				}
				b.visiting[bd.Ref] = true
				b.expand(bd.Ref)
				delete(b.visiting, bd.Ref)
				continue
			}
			b.add(p, key, bd)
		}
	}
}

// add lets one concrete candidate through the admission check and onto the chain.
func (b *chainBuilder) add(p *domain.Profile, key string, bd domain.Binding) {
	k := bd.String()
	if b.seen[k] {
		b.skips = append(b.skips, Skip{Profile: p.Name, Target: k, Kind: SkipDuplicate,
			Reason: i18n.T("duplicate of an earlier candidate, deduplicated", nil)})
		return
	}

	prov, ok := b.provs.Providers[bd.Provider]
	if !ok {
		b.skips = append(b.skips, Skip{Profile: p.Name, Target: k, Kind: SkipUndefined,
			Reason: i18n.T("provider is not defined", nil)})
		return
	}
	if prov.Key() == "" {
		b.skips = append(b.skips, Skip{Profile: p.Name, Target: k, Kind: SkipNoKey,
			Reason: i18n.T("provider has no api_key", nil)})
		return
	}
	if b.o.Disabled != nil {
		if yes, why := b.o.Disabled(key, k); yes {
			b.skips = append(b.skips, Skip{Profile: p.Name, Target: k, Kind: SkipDisabled,
				Reason: i18n.T("disabled: {why}", i18n.A{"why": why})})
			return
		}
	}
	if b.o.Available != nil {
		if ok, why := b.o.Available(bd.Provider, bd.Model); !ok {
			// The reason (why) is passed through as is: it belongs to whoever provides
			// the criterion, and the wording is not this layer's. The category is
			// this layer's -- "this gate rejected it" is this layer's knowledge.
			b.skips = append(b.skips, Skip{Profile: p.Name, Target: k,
				Kind: SkipUnavailable, Reason: why})
			return
		}
	}
	// **The marker goes after the admission check** (moved there 2026-09-18): it used
	// to be first, so when a rejected candidate (no key / breaker / disabled) appeared
	// again in another profile, `newgate tier` reported "duplicate of an earlier one on
	// the chain, deduped" -- while it had never been on the chain, and the real reason was
	// the first one's. The only question the user will ask is "why not the one I thought",
	// and a reason pointing at the wrong thing wastes more time than no reason. The
	// dedup semantics are unchanged: the same key still appears only once on the chain.
	b.seen[k] = true
	b.steps = append(b.steps, Step{Profile: p.Name, Binding: bd, Provider: prov})
}

// OverrideChain builds the chain "override binding as the head, the tier's chain as fallback".
//
// When the override binding's provider is undefined / has no key / is broken
// / disabled, the override does not take effect: fall back to the pure tier chain and
// return applied=false (recording a Skip saying why the override did not apply along
// the way). On success the override Step is inserted at the front, and the duplicate
// step with the same key in the tier chain is removed -- "global override first" lands
// here, and the tier's candidates only get their turn if the override model fails.
func OverrideChain(source string, override domain.Binding, tier string, profiles []*domain.Profile,
	provs *domain.Providers, o Opts) ([]Step, []Skip, bool) {

	steps, skips := BuildChain(tier, profiles, provs, o)
	if source == "" {
		source = "override"
	}
	source = "(" + source + ")"

	if override.Provider == "" || override.Model == "" {
		return steps, skips, false
	}
	prov, ok := provs.Providers[override.Provider]
	switch {
	case !ok:
		skips = append(skips, Skip{Profile: source, Target: override.String(), Kind: SkipUndefined,
			Reason: i18n.T("provider is not defined, override not applied", nil)})
		return steps, skips, false
	case prov.Key() == "":
		skips = append(skips, Skip{Profile: source, Target: override.String(), Kind: SkipNoKey,
			Reason: i18n.T("provider has no api_key, override not applied", nil)})
		return steps, skips, false
	}
	if o.Disabled != nil {
		if yes, why := o.Disabled(tier, override.String()); yes {
			skips = append(skips, Skip{Profile: source, Target: override.String(), Kind: SkipDisabled,
				Reason: i18n.T("disabled: {why}", i18n.A{"why": why})})
			return steps, skips, false
		}
	}
	// The override binding must pass admission too: a delisted binding should not get
	// onto the chain just because the user clicked it on the command line (that way
	// "the named one is broken" would bypass fallback protection and hit it head-on).
	if o.Available != nil {
		if ok, why := o.Available(override.Provider, override.Model); !ok {
			skips = append(skips, Skip{Profile: source, Target: override.String(),
				Kind:   SkipUnavailable,
				Reason: i18n.T("{why}, override not applied", i18n.A{"why": why})})
			return steps, skips, false
		}
	}

	ovStep := Step{Profile: source, Binding: override, Provider: prov}
	out := []Step{ovStep}
	for _, s := range steps {
		if s.Binding.String() == override.String() {
			continue // already at the head of the chain
		}
		out = append(out, s)
	}
	return out, skips, true
}

// pinnedOnly narrows an ordered profile list down to the pinned one, recording a skip for
// every profile that is left out -- "I asked for claude and got DeepSeek" has to be answerable
// from the skip list alone, otherwise the only evidence is the absence of something.
//
// A pinned profile that is not in the config at all yields an empty list *and* no skip (there is
// no profile to point at); `pinnedProfiles` in the forwarding path handles the same case by
// returning nil, and both end up as "no usable candidate".
func pinnedOnly(ordered []*domain.Profile, skips []Skip, pinned string) ([]*domain.Profile, []Skip) {
	for _, p := range ordered {
		if p.Name != pinned {
			continue
		}
		for _, other := range ordered {
			if other.Name == pinned {
				continue
			}
			skips = append(skips, Skip{Profile: other.Name, Kind: SkipPinned,
				Reason: i18n.T("the chain is pinned to profile {profile}, this one does not join",
					i18n.A{"profile": pinned})})
		}
		return []*domain.Profile{p}, skips
	}
	return nil, skips
}

// orderProfiles the chain head + the rest by (priority, name).
func orderProfiles(profiles []*domain.Profile, active, agent string) ([]*domain.Profile, []Skip) {
	var head *domain.Profile
	var rest []*domain.Profile
	var skips []Skip

	for _, p := range profiles {
		if p.Name == active && p.VisibleToAgent(agent) {
			head = p
			continue
		}
		rest = append(rest, p)
	}
	sort.SliceStable(rest, func(i, j int) bool {
		if pi, pj := rest[i].Prio(), rest[j].Prio(); pi != pj {
			return pi < pj
		}
		return rest[i].Name < rest[j].Name
	})

	var out []*domain.Profile
	// Visibility is checked **before** the head is used: a head hidden from this agent must not
	// become the head, or an agent with no per-agent profile would silently run on the global
	// default -- the exact cross-dialect failure this feature exists to stop. The skip is
	// reported, so "why is my chain different from the config" has an answer.
	if active != "" && head == nil {
		for _, p := range profiles {
			if p.Name != active {
				continue
			}
			skips = append(skips, Skip{Profile: p.Name, Kind: SkipHidden,
				Reason: hiddenReason(p, agent)})
			break
		}
	}
	if head != nil {
		out = append(out, head) // an explicit pick wins; excluded cannot block the chain head
		if head.Pinned {
			for _, p := range rest {
				// The category is "other": the display layer's category table gives pinned no
				// column of its own (`newgate tier`'s skip summary groups by that table), and no
				// column is invented here -- adding one is a product decision, not a translation.
				skips = append(skips, Skip{Profile: p.Name, Kind: SkipOther,
					Reason: i18n.T("chain head {name} is pinned, the chain stops there",
						i18n.A{"name": head.Name})})
			}
			return out, skips
		}
	}
	for _, p := range rest {
		if !p.VisibleToAgent(agent) {
			skips = append(skips, Skip{Profile: p.Name, Kind: SkipHidden,
				Reason: hiddenReason(p, agent)})
			continue
		}
		if p.Excluded {
			skips = append(skips, Skip{Profile: p.Name, Kind: SkipExcluded,
				Reason: i18n.T("excluded: explicit selection only", nil)})
			continue
		}
		out = append(out, p)
	}
	return out, skips
}

// hiddenReason says which declaration kept this profile out, so the user can tell a whitelist
// from a blacklist without opening the file.
func hiddenReason(p *domain.Profile, agent string) string {
	if len(p.VisibleAgents) > 0 {
		return i18n.T("only visible to {agents} (this request is for agent {agent})",
			i18n.A{"agents": strings.Join(p.VisibleAgents, ", "), "agent": agent})
	}
	return i18n.T("hidden from agent {agent}", i18n.A{"agent": agent})
}
