package home

import (
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/rzbdz/newgate-ext/lib/view"
	breakerapi "github.com/rzbdz/newgate-ext/modules/breaker"
	configapi "github.com/rzbdz/newgate-ext/modules/config"
	"github.com/rzbdz/newgate-ext/modules/config/store"
	agentapi "github.com/rzbdz/newgate-ext/modules/confighook"
	gatewayapi "github.com/rzbdz/newgate-ext/modules/gateway"
	i18n "github.com/rzbdz/newgate/lib/i18n"
)

// This file is the first screen's card: **which client uses which tier**.
//
// # How it divides work with chains.go
//
// chains.go lays config's chain conclusion out as a stack of cards (pure configuration, the
// same for everybody, and `newgate tier` wants that same conclusion). Here there are two more
// things, and both **can only be combined in the backend**:
//
//  1. **Health**. Which chain exists is computed by config (a pure function); how fast each
//     binding is, is recorded by breaker (its own ledger). What the first screen wants is
//     "how this chain will go when used", which means putting the two ledgers side by side.
//     Joining them in the frontend by `provider/model` would indeed draw something, at the
//     price of **copying two things only the owner knows into the browser**: which cell on a
//     chain card counts as the head, how many milliseconds count as slow (the threshold is in
//     breaker/status, in exactly one place). Both copies get edited in their own modules, and
//     nothing goes red on the frontend's copy. So the join happens here.
//  2. **Actions**. "Use it" must write that client's chain head in state.json -- that is the
//     kernel's `confighook.AgentProfile`, and the browser should not know what that key looks
//     like.
//
// # One snapshot, one moment per screen
//
// Same as chains.go: AllChains asked once, health Snapshotted once. Asking separately
// assembles the config of two moments, and nothing on screen looks wrong. The health one is
// flattened into one map first (keyed by binding), rather than a linear scan per stop -- a
// dozen profiles × five tiers × a few stops is hundreds of lookups.
//
// # Dependency direction
//
// Every kernel import is a **public contract** (configapi / agentapi / breakerapi / gatewayapi)
// or a shared leaf (store). Not one internal implementation package. Of the four capabilities
// only config is a hard dependency (no chains, no page), the other three are Optional: no
// health table, no latency cell; no gateway, no probe button; no client catalog, no row of tabs
// above -- **missing any one of them still produces the page**, instead of the whole card
// disappearing (see the assembly in module.go).

// overviewID is the stable identity of the first screen's card.
const overviewID = "home.overview"

// overviewDeps is the **optional** ports this page can use.
//
// The zero value (all nil) is a valid state: the page in that assembly is "one card per
// profile, chains listed as usual, no latency and no buttons". The criterion is "without it,
// can this screen still tell the truth"; if it can, it is Optional.
type overviewDeps struct {
	agents  agentapi.AgentCatalog
	health  breakerapi.Breaker
	gateway gatewayapi.Gateway
}

// overviewConcept combines the chain conclusion + health + client catalog into the first
// screen's card.
//
// all is **the one the caller already took**: the two concepts in the same section (this one
// and chains) must be showing the config of the same moment. Asking AllChains again separately
// would read the disk at two moments, and nothing on screen looks wrong (see the file header of
// chains.go and concepts in view.go).
func overviewConcept(all []*configapi.Chains, d overviewDeps) view.Concept {
	health := healthByBinding(d.health)
	agents := overviewAgents(d.agents)
	ordered := orderCards(all)
	cards := make([]view.OverviewCard, 0, len(ordered))
	for _, one := range ordered {
		cards = append(cards, overviewCard(one, agents, health, d.gateway))
	}
	c := view.Concept{
		ID: overviewID, Kind: view.KindOverview,
		Title: i18n.T("Routes", nil),
		Data: view.Overview{
			Agents: agents,
			// The "default" card sorts first in the grid (it is this screen's first sentence:
			// **who everybody uses right now**). Its chain is copied from cards, so it must be
			// built after cards.
			Auto:  autoCard(agents, cards, all, d.gateway),
			Cards: cards,
		},
		// Order 0: lives in the same section as home.chains, and this card comes first (it is
		// the one the first screen is about, see view.Concept.Order); chains gives itself a bigger
		// number.
		Order: 0,
		// No Apply: every change on this screen goes through **actions** ("use it", "probe
		// it"); there is no such thing as "hand the whole card back" -- it is a computed
		// conclusion, not a document.
		//
		// No Live: it must re-read and re-parse every profile (view.Concept.Live's criterion is
		// "is one read expensive", not "does the data change"). Probe results come back with the
		// re-read after the action runs, not from polling.
	}
	// Whether fallback is off is **not said here**: the status button in the top right is that
	// sentence (red = off), and one more red statement on the card says the same thing twice
	// (the user's actual words: "now that there is a red button, isn't this sentence
	// redundant" -- yes).
	return c
}

// overviewAgents lists the clients this machine knows, plus each one's chain head.
//
// Ordered by id (a machine marker): a page whose tabs keep jumping makes the user hunt again
// every time.
//
// **The empty "all clients" tier at the end is required, even with a pile of clients in the
// catalog**: the global default (`state.default_profile`) is the chain every client without its
// own setting actually takes, and the only way to change it is `use:global:<profile>` -- that
// family of actions is, by contract, only drawn when **the current tier is global** (see
// actions() in Overview.svelte on the web side: it assembles `use:<who>:<profile>` from the
// current tier's id). With no empty tier, that id can never be assembled, so "global default"
// becomes read-only on this screen -- exactly when it is the only way to "switch the chain for
// all clients at once".
//
// At the end rather than the front: the concrete clients are this screen's body, the global
// tier is their fallback, and at the end it reads as "and there is an overall one". The
// default-selected tier is therefore chosen among the **installed** clients.
func overviewAgents(cat agentapi.AgentCatalog) []view.OverviewAgent {
	st := store.LoadState()
	var out []view.OverviewAgent
	if cat != nil {
		names := cat.Names()
		sort.Strings(names)
		for _, id := range names {
			ap := agentapi.AgentProfile{AgentID: id}
			// The icon is carried over verbatim from **the client's own declaration** (see
			// confighook.Agent.Icon). No trade-off is made here and no vendor is known: carried if
			// found, empty if not, and the UI then draws its own generic mark.
			icon := ""
			if a, ok := cat.Get(id); ok && a != nil {
				icon = a.Icon
			}
			out = append(out, view.OverviewAgent{
				Icon: icon,
				ID:   id,
				Name: id, // a **machine value** (claude / codex), already meant for humans
				// ActiveFor rather than Active[id]: without its own setting it returns the global
				// default, and that sentence is the truth of "which chain this client's requests
				// land on" (for the division of Active and Stored in confighook.AgentProfile see
				// there: the **value** in the UI uses Stored, while what is said here is who it
				// actually uses right now, so Active).
				Profile: ap.Active(),
				// Own says **whether this one set it itself**. It is a different matter from
				// Profile, and the UI must be able to separate them: "pin claude to ds" and
				// "claude follows the global, and the global happens to be ds" route the same, but
				// after changing the global one follows and the other does not. Saying only
				// Profile hides this (the criterion is confighook's IsOverride, not a second
				// one).
				Own:   ap.IsOverride(),
				Ready: cat.Installed(id),
			})
		}
	}
	// The last tier is **not a client, it is "auto"**: the profile it selects is the one "auto"
	// finally resolves to, and picking "auto" in any client tab = taking this tier's result. So
	// its Own is always true -- there is no "it follows someone" for "auto"; it is the one being
	// followed. Ready is always true: it can always be clicked.
	//
	// It is called "auto" rather than "all clients": it is **not** the "apply this to everyone in
	// one click" action (such an action really should not live in a tab bar, see cards and
	// autoCard -- changing what auto points at is done only under this tier). It is the name of
	// a thing.
	return append(out, view.OverviewAgent{
		ID: "", Name: i18n.T("Auto", nil), Ready: true, Own: true,
		Profile: st.ActiveFor(""),
	})
}

// overviewCard is a profile's card on this screen.
// orderCards sorts this screen's cards into a **fixed** order: by the profile's pri, then by name.
//
// # Why "which one is in effect right now" does not take part in the sort
//
// The kernel's `orderedProfiles` (chains.go) sorts that way -- it serves another question:
// `newgate profiles` and the Chains screen, where the first thing you look for is exactly "who
// is in effect right now".
//
// This screen is different: it is there to be **scanned**, and one click here changes "what
// auto points at". If the default took part in the sort, the user clicks a card and the whole
// grid jumps on the spot, so he has to find again where he was looking. The user's actual words
// (measured): "I don't like this re-sorting after a click ... it is very jarring, after clicking
// it refreshes and changes position".
//
// The two criteria differing is **intentional**, so this sorts on its own and does not go back
// to change the kernel's.
func orderCards(all []*configapi.Chains) []*configapi.Chains {
	out := make([]*configapi.Chains, 0, len(all))
	for _, one := range all {
		if one != nil {
			out = append(out, one)
		}
	}
	snap, err := store.Load()
	if err != nil {
		// Unreadable (the whole config directory cannot be read): **keep the order the caller
		// gave**, no error. This screen is still drawn as usual, only the order degrades to the
		// kernel's (default first) -- better than the whole screen disappearing, the same
		// trade-off as Concept.Broken in lib/view.
		return out
	}
	prio := make(map[string]int, len(snap.Profiles))
	for _, pr := range snap.Profiles {
		prio[pr.Name] = pr.Prio()
	}
	// SliceStable rather than Slice: two profiles with the same pri and name (same name is
	// impossible, but the same pri is common) keep the relative order the kernel gave, so two
	// renders land in the same place.
	sort.SliceStable(out, func(i, j int) bool {
		pi, pj := prio[out[i].Profile], prio[out[j].Profile]
		if pi != pj {
			return pi < pj
		}
		return out[i].Profile < out[j].Profile
	})
	return out
}

func overviewCard(one *configapi.Chains, agents []view.OverviewAgent, health map[string]breakerapi.Status, g gatewayapi.Gateway) view.OverviewCard {
	card := view.OverviewCard{
		Profile: one.Profile,
		File:    profileFile(one.Profile),
		// **No Default**: that mark now belongs to the "auto" card -- it is the "global default"
		// itself, while this card is only **the one it happens to point at right now**. With
		// "default" on both, the reader cannot tell which one to look at.
		Roles:   make([]view.OverviewRow, 0, len(one.Keys)),
		Actions: cardActions(agents, one.Profile, one.Keys, g),
	}
	for _, chain := range one.Keys {
		card.Roles = append(card.Roles, overviewRow(one.Profile, chain, health))
	}
	return card
}

// healthByBinding flattens the health table into `provider/model → Status`.
//
// The key is built by the same function as the health table's own row ID (breaker.BindingKey):
// built separately on each side, they must come out as the same string, and the symptom of a
// mismatch is "the latency cell is always empty" -- no error, just no data.
func healthByBinding(b breakerapi.Breaker) map[string]breakerapi.Status {
	if b == nil {
		return nil
	}
	rows := b.Snapshot()
	out := make(map[string]breakerapi.Status, len(rows))
	for _, r := range rows {
		out[breakerapi.BindingKey(r.Provider, r.Model)] = r
	}
	return out
}

// overviewRow is this tier's chain + each stop's health.
func overviewRow(profile string, chain configapi.Chain, health map[string]breakerapi.Status) view.OverviewRow {
	row := view.OverviewRow{
		// The row ID uses the **same construction** as on the chain card (`profile/tier`): the UI
		// keys on it, stable across snapshots (see view.OverviewRow.ID).
		ID:   profile + "/" + chain.Key,
		Tier: chain.Key,
		Note: skipNote(chain.Skips),
	}
	if len(chain.Steps) > 0 {
		row.Head = chain.Steps[0].Binding.String()
	} else {
		// An empty chain is bad: this tier has nothing to take right now (the same rule as rowOf
		// in chains.go).
		row.Tone = view.ToneBad
	}
	row.Steps = overviewSteps(chain.Steps, health)
	return row
}

// overviewSteps is every stop on the chain, plus its cell in the health table.
func overviewSteps(list []configapi.Step, health map[string]breakerapi.Status) []view.OverviewStep {
	if len(list) == 0 {
		return nil
	}
	out := make([]view.OverviewStep, 0, len(list))
	for _, s := range list {
		step := view.OverviewStep{
			Provider: s.Binding.Provider,
			Model:    s.Binding.Model,
			Profile:  s.Profile,
		}
		if h, ok := health[breakerapi.BindingKey(s.Binding.Provider, s.Binding.Model)]; ok {
			if ms, tone, sampled := breakerapi.LatencySample(h); sampled {
				step.LatencyMs, step.LatencyTone = ms, tone
			}
			// Grade is a machine marker (fluent / laggy / …), sent out as is: it is the same
			// vocabulary as `newgate probe`'s output and that health-table column. Translate it
			// and the two no longer line up.
			if s := strings.TrimSpace(string(h.Grade)); s != "" {
				step.Probe = s
			}
		}
		out = append(out, step)
	}
	return out
}

// ---------- actions ----------

// useActionPrefix is the action-ID prefix of the "use it" family.
//
// The whole ID is `use:<agent>:<profile>`: `<agent>` is a client machine marker (`global` being
// the "all clients use it" tier), `<profile>` being which one this card is about.
//
// Both segments are necessary; the reason is written on view.OverviewCard.Actions (one
// sentence: the same agent's action exists once on each of a dozen cards, and the ledger looks
// someone up by ID taking the first match -- without the profile it hits someone else). The UI
// picks the button by it: whichever agent the current tab is, that is the prefix it takes.
const useActionPrefix = "use:"

// useGlobal is the agent segment of the "all clients use it" tier.
//
// It is not a real client: that action writes the **global default** (`default_profile` in
// `state.json`), not any row's override. The word `global` rather than an empty string, because
// `use::production` assembled from an empty string reads like a typo.
const useGlobal = "global"

// useAutoPrefix is the action-ID prefix of the "back to auto" family, the whole string being
// `auto:<agent>`.
//
// **Why a separate family instead of `use:<agent>:auto`**: the `<profile>` segment holds a tier
// name from disk, and tier names are chosen by the user -- `auto` is a perfectly legal tier
// name. Crammed into the same family, on a machine that really has an `auto.kv`, the "back to
// auto" button and a "pin to the auto tier" button would **assemble the same ID**, and the
// ledger takes the first match by ID: the user clicks "pin to auto" and what runs is release --
// silently changing the wrong thing, with nothing looking off in the UI.
//
// So the two prefixes do not share one parser either: `use:` must be followed by **two**
// segments, `auto:` by one.
const useAutoPrefix = "auto:"

// cardActions are the buttons on this card's header: "use it" + "probe the whole card".
func cardActions(agents []view.OverviewAgent, profile string, chains []configapi.Chain, g gatewayapi.Gateway) []view.Action {
	out := useActions(agents, profile)
	if g != nil {
		if targets := probeTargetsOf(chains); len(targets) > 0 {
			out = append(out, view.Action{
				ID:    "probe",
				Label: func() string { return i18n.T("Test this profile", nil) },
				Run:   func() (string, error) { return "", probeSet(g, targets) },
			})
		}
	}
	return out
}

// useActions is the "use this" on **a profile's card**.
//
// # Same card, same sentence, meaning decided by **which tier you are on**
//
//   - on the "auto" tier: `use:global:<profile>` = point **auto** at this profile;
//   - on some client's tier: `use:<agent>:<profile>` = **pin this client** to it.
//
// The front end picks one of them by the current tier (see actions in Overview.svelte) -- so
// the card is itself that selector, and there is no need to put another row of same-named
// buttons on it (that just says the same thing twice).
//
// The criterion for the pin entry is "has this client **itself** set one", not "which profile
// does it resolve to right now": pinning claude to ds and "claude follows auto and auto happens
// to be ds" are **two states** -- routing is the same right now, but after auto changes one
// follows and the other does not. Comparing against the resolved result shows up as "the user
// wants to pin it, yet the UI says it is already in use and offers no button", so he can never
// take that step.
func useActions(agents []view.OverviewAgent, profile string) []view.Action {
	out := make([]view.Action, 0, len(agents)+1)
	// "Auto" points at this profile. Absent when it already points here (choosing itself
	// changes nothing).
	if st := store.LoadState(); st.ActiveFor("") != profile {
		name := profile
		out = append(out, view.Action{
			ID: useActionID(useGlobal, name),
			// It is drawn only on the "auto" tier (the front end picks by the current tier), so this
			// sentence is exactly that: point auto at this profile. The profile name is a **machine
			// value**, not translated.
			Label: func() string { return i18n.T("set as auto", nil) },
			Run:   func() (string, error) { return "", setAgentProfile("", name) },
		})
	}
	for _, a := range agents {
		if a.ID == "" || !a.Ready {
			continue
		}
		// The entry for a client already **pinned** to this profile does not appear (pointing it at
		// itself is a meaningless thing to do).
		if a.Own && a.Profile == profile {
			continue
		}
		id, name := a.ID, a.Name
		out = append(out, view.Action{
			ID: useActionID(id, profile),
			Label: func() string {
				return i18n.T("use for {client}", i18n.A{"client": name})
			},
			Run: func() (string, error) { return "", setAgentProfile(id, profile) },
		})
	}
	return out
}

// autoCard is the **"default"** card at the front of the grid -- it is not a profile, it is an
// indirect reference.
//
// # What it looks like
//
// The name is "default", followed by which profile it **points at right now** (that is what
// `{current}` is); the table holds **that profile's chain right now** (exactly the content of
// that profile's own card); the row across the card's head is the members it can point at, and
// clicking one swaps the whole card for that profile.
//
// # Why it must be separate from the profile card
//
// Right now it **looks exactly the same** as "the ds card" (when pointing at ds), yet the two
// say different things:
//
//   - clicking "use for claude" on this one = make claude **follow the global default**;
//   - clicking the same sentence on the ds card = **pin** claude to ds.
//
// The two route identically at this moment, but after the global default changes one follows and
// the other does not. Merged into one card, "I want it pinned" has nowhere to be expressed; with
// them apart, "who is this client using right now" also has a target -- whichever card is
// highlighted says whether it follows or is pinned.
//
// Not a notation we invented: in Clash a group's members can be nodes or another group
// (selecting the latter = following that group's choice), and that group has a member table of
// its own. The only difference is that Clash's is picked automatically by latency testing, while
// ours is picked by a person.
//
// `cards` are the profile cards already built: this card's chain is **copied** from one of them,
// so the two always state the same conclusion from the same moment (built separately, config
// changing between the two disk reads would make the same profile show two different chains on
// two cards).
func autoCard(agents []view.OverviewAgent, cards []view.OverviewCard, all []*configapi.Chains, g gatewayapi.Gateway) *view.OverviewCard {
	st := store.LoadState()
	cur := st.ActiveFor("")

	card := &view.OverviewCard{Auto: true, Profile: cur, Default: true}
	for _, one := range cards {
		if one.Profile != cur {
			continue
		}
		card.File = one.File
		card.Roles = one.Roles
		break
	}

	// **No member table**: which profiles it can point at is said by the profile cards below -- on
	// the "auto" tier, clicking one = pointing auto at it (see useActions). The card is itself that
	// selector; an extra row of same-named buttons just says the same thing twice.

	// "This client follows auto": shown only on clients that **did set one** -- one that never did
	// is already following, so giving it a "back to auto" is meaningless (it is already there).
	for _, a := range agents {
		if a.ID == "" || !a.Ready || !a.Own {
			continue
		}
		id, name := a.ID, a.Name
		card.Actions = append(card.Actions, view.Action{
			ID: useAutoPrefix + id,
			// **The same sentence** as on the profile card, and that is on purpose: clicking it on the
			// "default" card = give the default to this client (it then follows the global), clicking it
			// on the ds card = pin ds for this client. The same verb lands on a different subject, and
			// the subject is the card itself.
			Label: func() string {
				return i18n.T("use for {client}", i18n.A{"client": name})
			},
			Run: func() (string, error) { return "", setAgentProfile(id, "") },
		})
	}

	// Probing: this card lays out exactly that chain, so "does it connect, how fast" is answerable
	// here -- the same thing as the button on a profile card.
	//
	// The ID is `probe-auto` rather than `probe`: the ledger takes the first match by **action ID**,
	// and every profile card has a `probe` (a dozen of the same name), so this one would never be
	// hit -- the user clicks "probe the auto chain" and the first profile card's probe runs instead.
	// With a machine marker it is unique within the whole concept.
	if g != nil {
		for _, one := range all {
			if one == nil || one.Profile != cur {
				continue
			}
			if targets := probeTargetsOf(one.Keys); len(targets) > 0 {
				card.Actions = append(card.Actions, view.Action{
					ID:    "probe-auto",
					Label: func() string { return i18n.T("Test this profile", nil) },
					Run:   func() (string, error) { return "", probeSet(g, targets) },
				})
			}
			break
		}
	}

	// **As long as there is one profile, this card is drawn** -- even with not one button on it.
	//
	// It is not "something worth showing only when it has actions": it is the card that answers
	// "who is auto right now" on this screen, and that question **has an answer at all times** (no
	// answer is itself an answer, see the Profile-is-empty case). Without it the user is left with
	// the pile of "one card per profile" and has to work out "who am I following" by comparison --
	// which is exactly the mistake of merging those two into one card.
	if len(cards) == 0 {
		return nil
	}
	return card
}

// useActionID builds the ID of a "use this" action (see useActionPrefix).
func useActionID(agent, profile string) string {
	return useActionPrefix + agent + ":" + profile
}

// setAgentProfile changes one client's chain head; an empty agent = change the global default.
//
// Both paths land on config's **shared leaf** (store), rather than assembling state.json itself:
// that file's keys and validation rules ("this profile really exists") are config's knowledge,
// and copying them buys this UI the ability to write a mistyped chain head -- and then every
// request from that client fails to start.
func setAgentProfile(agent, profile string) error {
	if agent == "" {
		return store.SetDefaultProfile(profile, false)
	}
	return agentapi.AgentProfile{AgentID: agent}.Write(profile)
}

// probeAllAction is the "probe them all" in the top right.
//
// # Why it lives on the **section** and not on some card
//
// What it probes is the chain stations of **all** profiles on this screen -- that set belongs to
// no single card (card-level probing lives in cardActions). Section actions are always reachable
// by the user, no matter which card is being viewed.
//
// # Why it reads afresh inside Run
//
// A section action is fixed once when it is registered (see view.Section.Actions), and what is
// needed here is which chains exist **today** -- what was read at registration is long stale by
// the time of the click. So the closure captures cfg and the port, and fetches the chains inside
// Run.
// fallbackAction is the fallback-chain master switch in the top right.
//
// # What it actually turns off (spelled out, because that word is ambiguous in the UI)
//
// **The whole chain**, with no distinction between "inside this profile" and "borrowed from
// another profile". The forwarding side lands on `steps = steps[:1]`, and `steps` is **one flat
// table** expanded by resolve.BuildChain -- it expands every profile's candidates into the same
// list in profile-priority order, each step carrying its origin (the `ds` / `cheap` pills in the
// UI). The switch does not use that origin.
//
// So "disable only cross-profile, keep the fallback inside this profile" is **not possible**
// today: that first needs the chain to recognize at the cut point which segment it came from
// (changing resolve and forward), which is a separate job. The UI writes its copy for what it can
// do, not for what it wishes it could -- writing "only cross-profile" would be lying.
//
// # Why the state is read at the moment of the press
//
// `on` is the value read **when this closure is built** (the snapshot re-reads, so it is fresh
// each time). What is flipped is its opposite, not "set to some fixed value": the button says the
// action to take right now.
func fallbackAction(g gatewayapi.Gateway) (view.Action, bool) {
	if g == nil {
		return view.Action{}, false
	}
	return view.Action{
		ID: "fallback",
		// The name is **the thing's name**, not an imperative sentence (it used to be "chain head
		// only" / "enable fallback chain", both of which flipped with state). What the user wants is a
		// state button: fixed name, state told by **color** (his words: "just make auto fallback a
		// state button, default gray is disable").
		//
		// The cost, spelled out: the TUI draws only the label, so what it sees is a name with no
		// state on it. What that buys is the UI side not having to parse a translatable sentence to
		// decide "is this disabled or enabled" -- see Action.Tone.
		Label: func() string { return i18n.T("auto fallback", nil) },
		// **On = green, off = red** (the user's words: "the default should be green; clicking to turn
		// it off is gray/red").
		//
		// At first this was "on gets no color, off is marked red", on the reasoning "green is the
		// normal case, no need to say it". That was wrong: a **state** button should look like which
		// of the two states it is in, and if only the abnormal one is marked, the viewer must first
		// know "unmarked = on" -- which is exactly the question he opened this screen to ask.
		//
		// # Both fields are **closures**, not values taken on the spot
		//
		// This section's actions are registered once in Start, while the snapshot is **recomputed per
		// request** -- taking `g.FallbackOn()` / `!on` at registration time pins the color and the
		// behavior to the state at process start forever (hit for real: after `newgate fallback off`
		// the button was still green and clicking it still went "off", and that looked like the UI
		// lying). The Tone path is evaluated on every snapshot anyway, so it follows any change; the
		// captured value in Run was the wrong one.
		Tone: func() string {
			if g.FallbackOn() {
				return view.ToneOK
			}
			return view.ToneBad
		},
		Run: func() (string, error) { return "", g.SetFallback(!g.FallbackOn()) },
	}, true
}

// ---------- probe fan-out ----------

// probeTarget is one probe's target. key is the human-facing spelling (`provider/model`).
type probeTarget struct {
	provider, model, key string
}

// probeSet is the shared implementation of "probe a batch of bindings", returning a sentence for
// the user (possibly empty).
//
// # Why it stops at the **batch** level, with no per-item probe action
//
// There is only one entry point for a single probe today: the button on each row of the health
// table (`breaker.health`) -- that is where "does this binding connect right now" should be asked.
// The probing on this first screen is **per card** (how is this profile's chain doing right now)
// and **per screen** (refresh them all), both of which are this batch route. Hanging a
// per-station button on every station would need two levels of identity (which card, which tier),
// and what it buys overlaps entirely with that row in the health table -- what is gained is a row
// of buttons, and the most used one on this screen ("just use it") would drown in them.
//
// # Why concurrency is capped, and total time too
//
// A single probe worst case waits for one first-byte timeout (12s by default, see store's
// Timeouts). Probing a dozen one at a time is minutes -- and this is **one HTTP request**;
// neither the browser nor a reverse proxy waits that long. So: probe concurrently, and wait at
// most probeBudget in total, saying honestly "a few are still in flight" when it runs out.
//
// The timed-out ones **are not wasted**: they finish in the background and their verdicts reach
// the policy layer as usual (ProbeBinding feeds itself), and the user sees them on the next
// refresh. That is also why this screen has no Live and yet does not feel wrong.
//
// The returned error is **the sentence on the banner**. The reason for taking the error path
// (rather than a silent success): something in this round really did not connect, or did not
// arrive in time, and that is exactly the answer the person pressing the button wants. Swallowed,
// the UI would only show "the latency cell is still the old one" -- which looks like the button
// did nothing.
// probeSet is the entry point of "probe a batch of bindings": take one gateway, probe this batch.
func probeSet(g gatewayapi.Gateway, targets []probeTarget) error {
	if g == nil || len(targets) == 0 {
		return nil
	}
	return probeBatch(g.ProbeBinding, targets)
}

// probeBatch is the fan-out half, and the half that **can be unit tested**: all it wants is a
// "probe one" function, not the whole gateway port.
//
// Why this seam: the probe fan-out has pitfalls of its own (concurrency, dedupe, the ones not
// finished), while the gateway port has a dozen methods -- faking a whole gateway for one test
// costs more than the thing under test, and a stand-in that does not behave like the real thing
// makes the test verify the stand-in's behavior. One function is enough: the only questions are
// "is the concurrency right, are failures said out loud".
func probeBatch(probe func(provider, model string) (gatewayapi.ProbeOutcome, error), targets []probeTarget) error {
	type result struct {
		target probeTarget
		err    error
	}
	ch := make(chan result, len(targets))
	sem := make(chan struct{}, probeFanout)
	var wg sync.WaitGroup
	for _, t := range targets {
		wg.Add(1)
		go func(t probeTarget) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			_, err := probe(t.provider, t.model)
			ch <- result{target: t, err: err}
		}(t)
	}
	go func() { wg.Wait(); close(ch) }()

	deadline := time.After(probeBudget)
	var failed []string
	done := 0
	for done < len(targets) {
		select {
		case r := <-ch:
			done++
			if r.err != nil {
				failed = append(failed, r.target.key)
			}
		case <-deadline:
			// Out of time: count the ones that have not come back as "in flight" and wait no longer.
			return probeReport(len(targets), failed, len(targets)-done)
		}
	}
	return probeReport(len(targets), failed, 0)
}

// probeReport is the result sentence for this probe batch; nil when all connected (no banner).
func probeReport(total int, failed []string, inFlight int) error {
	if len(failed) == 0 && inFlight == 0 {
		return nil
	}
	parts := []string{i18n.N("probed {n} binding", "probed {n} bindings", total,
		i18n.A{"n": total})}
	if len(failed) > 0 {
		sort.Strings(failed)
		parts = append(parts, i18n.N("{n} not answering ({list})",
			"{n} not answering ({list})", len(failed),
			i18n.A{"n": len(failed), "list": joinLimit(failed, 6)}))
	}
	if inFlight > 0 {
		parts = append(parts, i18n.N("{n} still in flight — refresh in a moment",
			"{n} still in flight — refresh in a moment", inFlight, i18n.A{"n": inFlight}))
	}
	return i18n.E("{report}", i18n.A{"report": strings.Join(parts, " · ")})
}

// joinLimit lists the first n, rolling the rest up as "and N more".
//
// With a dozen not connecting, laying them all out turns the banner into a wall, while the first
// sentence the user wants is "which ones do not connect".
func joinLimit(items []string, n int) string {
	if len(items) <= n {
		return strings.Join(items, ", ")
	}
	rest := len(items) - n
	return strings.Join(items[:n], ", ") + " · " +
		i18n.N("and {n} more", "and {n} more", rest, i18n.A{"n": rest})
}

// probeTargetsOf is the deduplicated targets across these chains (one probe must not hit the same
// binding twice -- the same provider/model repeating within a chain is normal, and cross-profile
// chains all the more).
func probeTargetsOf(chains []configapi.Chain) []probeTarget {
	var out []probeTarget
	seen := map[string]bool{}
	for _, c := range chains {
		for _, s := range c.Steps {
			key := breakerapi.BindingKey(s.Binding.Provider, s.Binding.Model)
			if seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, probeTarget{provider: s.Binding.Provider, model: s.Binding.Model, key: key})
		}
	}
	return out
}

// probeFanout is how many are probed at once at most. 8 is the same number as the `newgate probe`
// command: the same upstream hit twice by us at the same moment -- there is no reason to be
// harsher here than the command line is.
const probeFanout = 8

// probeBudget is how long one probe action waits at most.
//
// 15s is "a few stations plus some margin": a single probe is 12s in the worst case (first-byte
// timeout), and with 8 in flight a dozen usually come back in one round. The cap exists so that
// one click does not become a minute of spinning -- the ones that time out still finish in the
// background, their verdicts still going into the ledger.
const probeBudget = 15 * time.Second
