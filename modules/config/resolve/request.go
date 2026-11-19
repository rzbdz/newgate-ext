package resolve

import "github.com/rzbdz/newgate-ext/modules/config/domain"

// PrimaryBinding returns the first candidate a tier
// would **actually use** under the current chain head.
//
// Use: before the wrapper starts an agent, it fills each slot's env with the real model name
// (rather than the tier name), so Claude Code and the like show the real model in their UI. So this
// **ignores** runtime availability (breaker, disable) -- what is shown is "the first one in the
// config", while the actual fallback when forwarding is decided again by the proxy using BuildChain
// + the breaker; when the two disagree, the X-Newgate-Route response header is authoritative.
func PrimaryBinding(tier string, profiles []*domain.Profile, provs *domain.Providers, active string) (domain.Binding, bool) {
	steps, _ := BuildChain(tier, profiles, provs, Opts{Active: active})
	if len(steps) == 0 {
		return domain.Binding{}, false
	}
	return steps[0].Binding, true
}

// realNameRoleOrder the priority order for reverse-looking-up a real model name to a tier.
//
// Why not Roles' original order (heavy first): a tier-name request says its tier itself, while a
// real-name request's tier can only be looked up -- names carry no slot information on the wire
// (we inject real names exactly so the UI shows them; the opus and sonnet slots send the same
// glm-5.3), and a one-to-many lookup has no true answer, so priority follows "which slot is the
// request most likely from": the classifier and compact summaries on the sonnet slot (mid), the
// densest sender; the main loop on the opus slot (= normal after the split) second; the haiku
// slot (light) does small jobs like titling; the fable slot (heavy) is only explicit naming.
// One name in several tiers (normal and mid both bound to glm-5.3 is common) matching normal
// first would force many mid requests to claim normal heft (measured 2026-09-09: the classifier
// was then skipped by claude-bg's tier gate and ran 27 seconds a request on a mid-heft
// model). mid first aligns the lookup with where requests mostly come from; the only cost is
// that main-loop requests count as mid -- they are streaming and do not eat the tier gate.
var realNameRoleOrder = []string{"mid", "normal", "light", "heavy", "vision"}

// ResolveRequest resolves the model field the client sent into a fallback chain.
//
// Two forms (docs/04-configuration.md):
//
//   - a role name ("heavy", "newgate/heavy", "heavy[1m]" after normalization; also including
//     module-contributed dynamic roles like "omo-sisyphus") -> take that role's chain, as before;
//   - a concrete model name ("deepseek-chat") -> find which tier's
//     chain has this model, move it to the chain head (the client named
//     it, so try it first), and follow the rest by that tier's chain.
//
// The concrete-model branch is the key to "let the tool's UI show real model names":
// the wrapper writes the real model name into the tool's slot env, the tool sends it
// back as is, and we reverse-resolve it to the tier it belongs to. When one name fills
// several tiers, take the tier by realNameRoleOrder's priority (see its comment) -- the
// reverse lookup has no true answer, so do not take this return as the client's intent.
//
// A return of tier == "" means this model is neither a tier name nor
// in **any profile's config**. "In the config but not usable right now"
// does not count as unknown -- see the two reverse-lookup passages below.
func ResolveRequest(model string, active string, profiles []*domain.Profile,
	provs *domain.Providers, o Opts) ([]Step, []Skip, string) {

	// A tier name or a module-contributed dynamic role key (omo-sisyphus and such)
	// -- all "roles", taking the same chain-building path. Only when it cannot be
	// told whether it is a role does it count as a concrete model name (see below).
	if domain.IsKnownRole(model) {
		steps, skips := BuildChain(model, profiles, provs, o)
		return steps, skips, model
	}

	// The reverse lookup **cannot be done on the truncated chain**: the named
	// model may come after maxAttempts, and truncating first would judge it
	// "unknown". So build the whole chain here, move the head, then truncate.
	search := o
	search.MaxSteps = 0
	type built struct {
		role  string
		steps []Step
		skips []Skip
	}
	var tried []built
	for _, role := range realNameRoleOrder {
		steps, skips := BuildChain(role, profiles, provs, search)
		if idx := indexOfModel(steps, model); idx >= 0 {
			steps = moveToFront(steps, idx)
			steps, skips = capSteps(steps, skips, o.MaxSteps)
			return steps, skips, role
		}
		// This tier's usable chain right now (already capped by chain length);
		// when the reverse lookup fails, it may have to be handed back as is.
		steps, skips = capSteps(steps, skips, o.MaxSteps)
		tried = append(tried, built{role: role, steps: steps, skips: skips})
	}

	// Not found by here does not mean "this name is unknown": the ruler above carries runtime
	// facts (admission/breaker, disable, chain length cap), and a binding **plainly present
	// in the config** can be taken off the chain by them. The scene on 2026-11-04: the
	// global profile was not ds, ds's only binding was broken, the client sent the real model
	// name deepseek-flash that ds injected, the reverse lookup came back empty, and so it
	// 404ed saying "this model is not in any profile's bindings" -- while it was bound in ds.
	//
	// So ask again with a ruler that knows only the config: which tier has this name in
	// the config? Recognizing it, take that tier's chain **usable right now** -- the named one
	// being unavailable does not mean the whole tier chain has no usable candidate; fallback is
	// for exactly this. Only when it is truly not recognized at all does it return tier == "".
	ident := o
	ident.Available, ident.Rank, ident.Disabled, ident.MaxSteps = nil, nil, nil, 0
	// The pin comes off here too: this pass asks "does any profile of the config bind this
	// name", and if it does, what comes back is `tried[i]` -- the chain built **under** the pin,
	// i.e. empty with the "pinned to profile X" skips. That is the right story: the name exists,
	// this call may not use it.
	ident.Pinned = ""
	for i, role := range realNameRoleOrder {
		steps, _ := BuildChain(role, profiles, provs, ident)
		if indexOfModel(steps, model) >= 0 {
			return tried[i].steps, tried[i].skips, tried[i].role
		}
	}
	return nil, nil, ""
}

// moveToFront moves step idx to the chain head, keeping the rest in
// their original order (dedup is already guaranteed by BuildChain).
func moveToFront(steps []Step, idx int) []Step {
	if idx == 0 {
		return steps
	}
	out := make([]Step, 0, len(steps))
	out = append(out, steps[idx])
	out = append(out, steps[:idx]...)
	out = append(out, steps[idx+1:]...)
	return out
}

func indexOfModel(steps []Step, model string) int {
	for i, s := range steps {
		if s.Binding.Model == model {
			return i
		}
	}
	return -1
}
