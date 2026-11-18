package hermes

import (
	"sort"
	"strconv"
	"strings"

	"github.com/rzbdz/newgate-ext/modules/config/domain"
)

// This file is **what this takeover should write**: given the newgate config as it is now,
// compute what every place in config.yaml should be. A pure function (inputs in, []edit out),
// so it can be pinned directly by unit tests.
//
// # Which places are written (hermes 0.13, 2026-11-04: source reading + local measurement)
//
//	model.provider / default        newgate / tier name (slot model)
//	model.base_url / api_key / api_mode   the local proxy (see below: why also write one here)
//	model.context_length            the active profile's window (written only if known)
//	providers.newgate               the "follow newgate" line: /model lists tiers only
//	providers.newgate-<profile>     one line per profile: pinned to that profile (URL has /p/),
//	                                listing tiers + every concrete model it binds
//	                                (provider/model)
//	delegation.provider / model     newgate / tier (slot delegation), base_url/api_key cleared
//	auxiliary.<task>.provider/model newgate / tier, base_url/api_key cleared
//
// # A few non-obvious trade-offs
//
//   - **provider is the named `newgate`, not `custom`**: a named provider goes through
//     `_resolve_named_custom_runtime`, whose api_mode and URL follow what we wrote; /model's
//     list marks that line as "current" too (is_current compares the provider key).
//   - **also write base_url / api_key / api_mode under model**: when an auxiliary task cannot
//     get the main loop's runtime it resolves a bare `custom` from `model.base_url`
//     (auxiliary_client's fallback), and when `/model <name>` is used without `--provider`,
//     hermes only considers "the current endpoint is custom" if base_url is a local address --
//     otherwise it may switch the user to OpenRouter. Both places are covered by this one copy.
//   - **api_key is a placeholder value, not empty**: left empty, hermes goes down the list to
//     fetch `OPENAI_API_KEY` / `OPENROUTER_API_KEY` -- the user's real key would be sent to the
//     proxy (the proxy drops it, but it never should have left the house). The placeholder must
//     be >= 4 characters and not a word like `dummy`, or it is likewise treated as "not given".
//   - **do not write `discover_models: false`**: /model asks `<base>/models` and replaces the
//     model table here with what it gets back, and that list is answered by this module on the
//     proxy side (st-models.go), computed by the same function as what is written here. The
//     price of writing that key is that every normalization hermes does logs one
//     "unknown config keys ignored" into errors.log (measured: nine for one `hermes chat`).
//     The `models:` here still must be written: each model's context_length can only come from
//     here.
//   - **delegation clears base_url**: with a base_url, api_mode is hard-wired to
//     chat_completions and provider is ignored (delegate_tool.py); clearing it routes through
//     the named provider.
//   - **auxiliary clears base_url / api_key**: with both present it uses them directly as a
//     custom endpoint and the provider cell no longer counts
//     (`_resolve_task_provider_model`).

// ProviderID is the key we occupy in config.yaml's `providers:` (and its prefix: one line per
// profile, called `newgate-<profile>`). Keys under the prefix **all belong to us**: rewritten
// wholesale at takeover, extras deleted at sync (see ownedProvider).
const ProviderID = "newgate"

// placeholderKey see "api_key is a placeholder value" in the file header.
const placeholderKey = "newgate-local"

// apiMode is the dialect hermes speaks to us: OpenAI chat completions.
//
// Why not anthropic_messages (the one hermes was configured with before): newgate's forwarding
// is **pure byte passthrough**, so the client's dialect must be one the upstream understands;
// today every provider declares `protocol: openai`, while the anthropic dialect is accepted by
// only some aggregators. The other reason is hermes' own: some fallback and delegation paths
// **do not honor** a named provider's api_mode and always go chat_completions -- pick it and the
// two paths will not each speak a different dialect.
const apiMode = "chat_completions"

// auxTasks are the auxiliary tasks hermes 0.13's code actually calls (`auxiliary.<task>`).
//
// Tasks that appear in the file but are not in this table (`flush_memories` is one: no code
// reads it) are taken over anyway -- takeover means "every model written in the file points at
// us", not "only the ones we know count". The table's use is the reverse: also add the ones
// **missing** from the file, or they go `auto`, follow the main loop on the normal tier, and the
// light set on the slot card has no effect on them, invisibly.
var auxTasks = []string{
	"vision", "web_extract", "compression", "session_search", "skills_hub",
	"approval", "mcp", "title_generation", "triage_specifier", "curator", "goal_judge",
}

// auxSlot which slot an auxiliary task belongs to (see the slot table in agent.go).
func auxSlot(task string) string {
	switch task {
	case "vision":
		return SlotVision
	case "compression":
		return SlotCompression
	}
	return SlotAuxiliary
}

// inputs is every fact needed to compute a takeover -- all fetched live by the caller at that
// moment (see currentInputs in takeover.go); nothing here reads from disk.
type inputs struct {
	Port int
	// Tiers slot name → the tier to use right now.
	Tiers map[string]string
	// Active is the profile hermes follows right now (state.json's ActiveFor("hermes")).
	Active string
	// Profiles is every profile (sorted by name).
	Profiles []*domain.Profile
	// Unreadable profiles that exist but cannot be read right now (broken by a manual edit).
	// Their line is left alone.
	Unreadable []string
}

func (in inputs) baseURL(profile string) string {
	u := "http://127.0.0.1:" + strconv.Itoa(in.Port) + "/a/" + ID
	if profile != "" {
		u += "/p/" + profile
	}
	return u + "/v1"
}

func (in inputs) profile(name string) *domain.Profile {
	for _, p := range in.Profiles {
		if p.Name == name {
			return p
		}
	}
	return nil
}

// window the context window a profile declares; 0 if none (= do not write, same reason as
// codex's contextWindow: guessing a number is worse than not writing).
func window(p *domain.Profile) int {
	if p == nil {
		return 0
	}
	return p.ContextWindow
}

// providerKey the key of a profile's line.
func providerKey(profile string) string { return ProviderID + "-" + profile }

// ownedProvider whether this key is ours (`newgate` or `newgate-*`).
func ownedProvider(key string) bool {
	return key == ProviderID || strings.HasPrefix(key, ProviderID+"-")
}

// profileKeyOK whether this profile's name can serve as a provider key.
//
// Profile names come from file names and can in theory contain anything; and this key has to
// live at once in YAML (a bare key), hermes' slug (`custom_provider_slug` lowercases and
// replaces spaces) and a URL path (`/p/<name>`). Only this small set of characters is safe in
// all three; the unsafe ones are **skipped and reported**, not forced in.
func profileKeyOK(name string) bool {
	if name == "" {
		return false
	}
	for _, c := range name {
		ok := c == '-' || c == '_' || c == '.' ||
			(c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
		if !ok {
			return false
		}
	}
	return true
}

// concreteModels every concrete model a profile binds, written as `provider/model`.
//
// Why the provider prefix: the /model list is all the user sees -- when the same model name
// exists at two providers, the prefix is the only thing that tells them apart. The data plane
// strips the prefix (gateway/protocol.NormalizeRole), and what remains is exactly the model name
// in the config, so naming it on this line (whose URL is pinned to this profile) is always
// resolvable back.
//
// **Bindings whose model name itself contains `/` are skipped** (OpenRouter's
// `google/gemini-…`): NormalizeRole takes the part after the last `/`, which no client can
// point at -- listing it would be an entry guaranteed to 404 when picked. The skipped ones are
// handed back to the caller to report.
//
// Order: tiers in domain.Roles' capability order, the rest by name; a model is listed only at
// its first appearance.
func concreteModels(p *domain.Profile) (ids []string, skipped []string) {
	if p == nil {
		return nil, nil
	}
	var keys []string
	seenKey := map[string]bool{}
	for _, r := range domain.Roles {
		if _, ok := p.Roles[r]; ok {
			keys = append(keys, r)
			seenKey[r] = true
		}
	}
	var rest []string
	for k := range p.Roles {
		if !seenKey[k] {
			rest = append(rest, k)
		}
	}
	sort.Strings(rest)
	keys = append(keys, rest...)

	var bindings []domain.Binding
	for _, k := range keys {
		bindings = append(bindings, p.Roles[k]...)
	}
	if p.Fallback != nil {
		bindings = append(bindings, *p.Fallback)
	}
	seen := map[string]bool{}
	for _, b := range bindings {
		if b.Ref != "" || b.Model == "" || b.Provider == "" {
			continue
		}
		id := b.Provider + "/" + b.Model
		if seen[id] {
			continue
		}
		seen[id] = true
		if strings.Contains(b.Model, "/") {
			skipped = append(skipped, id)
			continue
		}
		ids = append(ids, id)
	}
	return ids, skipped
}

// providerEntry is one line under `providers:`.
func providerEntry(key, url string, ids []string, win int) omap {
	models := make(omap, 0, len(ids))
	for _, id := range ids {
		meta := omap{}
		if win > 0 {
			// Declare it **per model**, not just model.context_length: the latter is one global
			// cell, and when /model switches models hermes clears it (run_agent.py); after that the
			// new model can only rely on its own guess (256K if it cannot guess). The per-model
			// cell matches exactly on (base_url, model name), so switching to any model still hits.
			meta = omap{{"context_length", win}}
		}
		models = append(models, kv{id, meta})
	}
	return omap{
		{"name", key},
		{"base_url", url},
		{"api_key", placeholderKey},
		{"api_mode", apiMode},
		{"default_model", "normal"},
		{"models", models},
	}
}

// catalog is our lines under `providers:`, plus whatever could not be listed (to be
// reported to the user).
type catalog struct {
	Entries []kv
	// Skipped profiles whose name cannot be a key, and bindings whose model name contains `/`
	// -- each written as one sentence for the user.
	Skipped []string
}

// buildCatalog computes the "follow us" line + one line per profile.
func buildCatalog(in inputs) catalog {
	var c catalog
	c.Entries = append(c.Entries, kv{ProviderID,
		providerEntry(ProviderID, in.baseURL(""), domain.Roles, window(in.profile(in.Active)))})
	for _, p := range in.Profiles {
		if !profileKeyOK(p.Name) {
			c.Skipped = append(c.Skipped, "profile "+strconv.Quote(p.Name))
			continue
		}
		ids, skipped := concreteModels(p)
		for _, s := range skipped {
			c.Skipped = append(c.Skipped, p.Name+": "+s)
		}
		all := append(append([]string(nil), domain.Roles...), ids...)
		key := providerKey(p.Name)
		c.Entries = append(c.Entries, kv{key, providerEntry(key, in.baseURL(p.Name), all, window(p))})
	}
	return c
}

// catalogEdits is the group of changes that "only touch the model list": our lines are
// rewritten to their current shape, and the extra ones (profiles deleted) are removed.
//
// Exception: a line that is **still referenced right now** is not removed -- the user pinned
// the main model to `newgate-old` with `/model … --global` in hermes, and the old profile was
// just deleted. Remove that line and hermes cannot resolve a provider next round, stalling the
// whole messaging gateway; keep it and the worst case is a request 404-ing back from newgate
// (profile does not exist), and that error is understandable. The kept line is reported.
func catalogEdits(d *doc, in inputs) ([]edit, catalog) {
	c := buildCatalog(in)
	var edits []edit
	want := map[string]bool{}
	for _, e := range c.Entries {
		want[e.K] = true
		edits = append(edits, edit{Path: []string{"providers", e.K}, Value: e.V})
	}
	referenced := referencedProviders(d)
	for _, n := range in.Unreadable {
		want[providerKey(n)] = true // unreadable != deleted: keep it as it is
		c.Skipped = append(c.Skipped, "profile "+strconv.Quote(n)+" (unreadable, row left as is)")
	}
	for _, k := range d.mapChildren("providers") {
		if !ownedProvider(k) || want[k] {
			continue
		}
		if referenced[k] {
			c.Skipped = append(c.Skipped, k+" (still referenced, kept)")
			continue
		}
		edits = append(edits, edit{Path: []string{"providers", k}})
	}
	return edits, c
}

// referencedProviders which provider keys in the file are in use right now (main loop,
// delegation, each auxiliary task).
func referencedProviders(d *doc) map[string]bool {
	out := map[string]bool{}
	add := func(path ...string) {
		if v, ok := d.get(path...); ok && v != "" {
			out[v] = true
		}
	}
	add("model", "provider")
	add("delegation", "provider")
	for _, t := range d.mapChildren("auxiliary") {
		add("auxiliary", t, "provider")
	}
	return out
}

// fullEdits is one complete takeover: the model list + every slot pointed at us.
func fullEdits(d *doc, in inputs) ([]edit, catalog) {
	edits, c := catalogEdits(d, in)
	url := in.baseURL("")
	m := func(k string) []string { return []string{"model", k} }
	edits = append(edits,
		edit{Path: m("provider"), Value: ProviderID},
		edit{Path: m("default"), Value: in.Tiers[SlotModel]},
		edit{Path: m("base_url"), Value: url},
		edit{Path: m("api_key"), Value: placeholderKey},
		edit{Path: m("api_mode"), Value: apiMode},
	)
	// `model.model` is the old alias of `default` (read only when default is absent). If it
	// is in the file, write the same value: leaving the old model name makes the reader think
	// hermes still uses it.
	if _, ok := d.lookup("model", "model"); ok {
		edits = append(edits, edit{Path: m("model"), Value: in.Tiers[SlotModel]})
	}
	if w := window(in.profile(in.Active)); w > 0 {
		edits = append(edits, edit{Path: m("context_length"), Value: w})
	}

	dl := func(k string) []string { return []string{"delegation", k} }
	edits = append(edits,
		edit{Path: dl("provider"), Value: ProviderID},
		edit{Path: dl("model"), Value: in.Tiers[SlotDelegation]},
		edit{Path: dl("base_url"), Value: ""},
		edit{Path: dl("api_key"), Value: ""},
	)

	tasks := append([]string(nil), auxTasks...)
	known := map[string]bool{}
	for _, t := range tasks {
		known[t] = true
	}
	for _, t := range d.mapChildren("auxiliary") {
		if !known[t] {
			tasks = append(tasks, t)
		}
	}
	for _, t := range tasks {
		a := func(k string) []string { return []string{"auxiliary", t, k} }
		edits = append(edits,
			edit{Path: a("provider"), Value: ProviderID},
			edit{Path: a("model"), Value: in.Tiers[auxSlot(t)]},
			edit{Path: a("base_url"), Value: ""},
			edit{Path: a("api_key"), Value: ""},
		)
	}
	return edits, c
}

// takenOverPaths every path takeover touches -- at restore time each is put back the way it
// was in the original (see Restore).
//
// Decided from both "the file right now" and "the original": auxiliary tasks present in the
// original but absent now must be put back too; ones present now but absent from the original
// (added by us) must be removed.
func takenOverPaths(cur, orig *doc) [][]string {
	var out [][]string
	seen := map[string]bool{}
	add := func(p ...string) {
		k := strings.Join(p, "\x00")
		if !seen[k] {
			seen[k] = true
			out = append(out, p)
		}
	}
	for _, k := range []string{"provider", "default", "model", "base_url", "api_key", "api_mode", "context_length"} {
		add("model", k)
	}
	for _, k := range []string{"provider", "model", "base_url", "api_key"} {
		add("delegation", k)
	}
	tasks := map[string]bool{}
	for _, t := range auxTasks {
		tasks[t] = true
	}
	for _, d := range []*doc{cur, orig} {
		for _, t := range d.mapChildren("auxiliary") {
			tasks[t] = true
		}
	}
	var names []string
	for t := range tasks {
		names = append(names, t)
	}
	sort.Strings(names)
	for _, t := range names {
		for _, k := range []string{"provider", "model", "base_url", "api_key"} {
			add("auxiliary", t, k)
		}
	}
	for _, d := range []*doc{cur, orig} {
		for _, k := range d.mapChildren("providers") {
			if ownedProvider(k) {
				add("providers", k)
			}
		}
	}
	return out
}
