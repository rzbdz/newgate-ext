package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/rzbdz/newgate-ext/lib/view"
	"github.com/rzbdz/newgate-ext/modules/config/domain"
	"github.com/rzbdz/newgate-ext/modules/config/paths"
	"github.com/rzbdz/newgate-ext/modules/config/store"
	modules "github.com/rzbdz/newgate/component"
	i18n "github.com/rzbdz/newgate/lib/i18n"
)

// The three things the config module contributes to the web UI: **tier mapping**
// (one binding editor per profile), **source files** (the tab where hardcore
// users edit files directly), **global switches** (default profile and such).
//
// Why this module contributes them rather than letting the UI read itself: the moment
// the UI reads store itself, it must know the fields of a profile file, the layout
// of the mappings directory, and which files carry credentials and cannot be written
// back -- then adding one module's UI would mean changing the UI. This rule is word
// for word the same as on the cli/extension side (see the passage there about "status
// must show switches not at factory state"), only this time the server is a browser.
//
// **The knowledge of writing is here too** (each concept has an Apply):
// changing a tier means "touch only the roles section, keep other keys as is",
// and only someone who knows what a profile file looks like can do that right.

// registerView mounts this module's concepts on the UI. With no UI (web-dashboard not installed,
// or this is a CLI process) it does nothing -- config works as usual, just with no web entry point.
//
// Registration **reads not one file**: the producer function `concepts` runs
// only when someone really comes to look at the UI. The reason is the correctness
// of this setup, not performance -- every `newgate ...` command runs to here, and
// in the vast majority nobody will open the UI (see lib/view's package comment).
func registerView(v view.Service) (modules.Release, error) {
	// "Create another tier file" hangs on **the section**, not on some card (see
	// view.Section.Actions): the newly created one has no concept yet, so no
	// card can hang this button. Hanging it on the section has another benefit
	// -- it is **always within reach**, no matter which card you are looking at.
	create := view.Action{
		ID:    "new-profile",
		Label: func() string { return i18n.T("+ profile", nil) },
		Run:   newProfileFile,
	}
	// "force apply": clear **every** client's chain head to "follow the global default".
	//
	// It hangs on **the section** rather than some card, for two reasons, neither of them layout:
	//
	// - what it touches is not one cell but **all clients** (three today, more later). On one
	//   client's card the button would sit somewhere it does not govern; on the "global settings" card
	//   it would read like "one option on this cell", which it is not -- it is a batch convergence.
	// - the UI does not know the notion of a "client" (that is confighook's vocabulary).
	//   Giving the toolbar's generic save button one more "force" meaning would amount to the UI
	//   knowing "what clearing active means" -- exactly the coupling this design exists to avoid.
	//
	// The use case is one sentence: **change the global default and have every
	// client follow it**. Not following the clients set on their own is right (they were
	// explicitly designated), but "converge everything back to following, then I maintain
	// one default" is a common demand, and clicking through each is sixteen operations.
	applyAll := view.Action{
		ID: "apply-default-all",
		// It hangs on **the section**, so the object must be spelled out:
		// no card nearby tells the reader "apply what". (The pair on the card
		// is different, see profileActions -- there the object is that card.)
		Label: func() string { return i18n.T("apply the default to all clients", nil) },
		Run:   applyDefaultToAll,
	}
	return v.Register("config",
		view.Title(func() string { return i18n.T("Configuration", nil) }).
			Of(view.FieldSettings).
			Does(create).Does(applyAll), concepts)
}

// applyDefaultToAll clears all per-agent chain heads (see store.ClearAllAgentProfiles).
//
// It does not change the global default itself: what the user wants
// is "everyone follows it", not "change it to something else".
//
// When not one exists it **still counts as success** (returns nil): that is
// exactly the state the user wants, and reporting an error would only make people
// think it did not take effect. After the action the UI re-reads the snapshot,
// and each client's chain-head cell shows "follow the default" by itself.
func applyDefaultToAll() (string, error) {
	if _, err := store.ClearAllAgentProfiles(); err != nil {
		return "", i18n.Ef(err, "cannot clear the per-client profiles: {err}", nil)
	}
	return "", nil
}

// newProfileFile creates an empty tier file, with a name it picks itself (the first not taken).
//
// Why the name is picked by the **backend** (it used to be the UI): only here
// know which names are already taken, and also that "a tier file may be called .kv
// or .json" (see store.ListProfiles' dedup) -- for the UI to pick a name it would
// have to copy both pieces of knowledge, and copied knowledge necessarily drifts.
//
// Why `WriteIfUnchanged(..., "")` (requiring "it does not exist right now"):
// when two tabs press this button at the same time, the later one hits a StaleError
// rather than overwriting the one the first created -- a name clash is the only
// real concurrency scenario here, and its cost is "the one I just created is gone".
func newProfileFile() (string, error) {
	taken := map[string]bool{}
	for _, n := range profileNames() {
		taken[n] = true
	}
	for i := 1; i <= 100; i++ {
		name := "new-profile"
		if i > 1 {
			name = fmt.Sprintf("new-profile-%d", i)
		}
		if taken[name] {
			continue
		}
		file := filepath.Join(paths.Mappings(), name+".kv")
		body := []byte(store.SerializeProfileKV(&domain.Profile{Name: name}))
		_, err := store.WriteIfUnchanged(file, "", body)
		if err == nil {
			// Hand back the new card's name: the UI switches based on it
			// ("the next step after creating is always to fill it in").
			return "config.profile." + name, nil
		}
		var stale *store.StaleError
		if !errors.As(err, &stale) {
			// Cannot be written (directory permissions or such) -- report it, rather
			// than treating it as a name clash and retrying all the way to 100.
			return "", i18n.Ef(err, "cannot create {file}: {err}", i18n.A{"file": filepath.Base(file)})
		}
		// The name was just taken by someone else: try the next one.
	}
	return "", i18n.E("could not find a free name for a new profile file", nil)
}

// concepts is "what the config looks like right now": the fields in state.json
// that belong to this module, one binding editor per profile, one per source file.
//
// It re-reads the disk on every call, so a refresh in the UI is a real refresh -- a tier file
// the CLI just created appears in the next snapshot. One item failing to read (file deleted, JSON
// broken) does not drag down its group: that card carries a Broken note, the rest proceed as usual.
func concepts() ([]view.Concept, error) {
	var out []view.Concept
	// **The order is a product decision, not alphabetical** (see view.Concept.Order):
	// configure the upstream first, then pick tiers. Alphabetical would put
	// `config.profile.*` before `config.providers`, so the first screen of the left column
	// would be all tier files, with the upstream config beyond the first screen -- and "only
	// with an upstream can a binding be picked" is the same thing as the order of operations.
	out = append(out, stateConcept())     // Order 0: global settings
	out = append(out, providersConcept()) // Order 1: upstream
	// One file per tier, one card per file -- **change tiers, change inheritance, rename, delete,
	// create** all happen on this card (see applyProfileRoles). Before 2026-09-20 there was also
	// a "tier files" directory card here: it listed the files that correspond one to one with
	// the cards below, the same thing said twice (the user's words: "this one is redundant"),
	// and its few unique functions (create/rename/delete) are now on the card itself.
	names := profileNames()
	for _, name := range names {
		out = append(out, profileConcept(name, names))
	}
	out = append(out, fileConcepts()...)
	return out, nil
}

// profileFamily says which family a tier belongs to (for left-column grouping).
//
// One rule: cut the name by "-" from left to right, and
// when **the cut segment itself is an existing tier**, that is the family name
// (`claude-cheap` -> `claude`, `minimax-fast` -> `minimax`). If nothing can be
// cut out (no sibling like `demo-*`), use its own name -- a family of its own.
//
// Why it must exist: tiers grow **one family at a time** (`claude` first, then
// `claude-cheap` derived), and with sixteen rows laid flat in the left column, nobody
// can tell at a glance who is whose variant. The grouping criterion must be "that family
// really exists", not "there is a hyphen in the name": hyphens are common in tier names
// (`gpt-4.1-mini`), and grouping by it would invent a pile of families that do not exist.
func profileFamily(name string, all []string) string {
	exists := make(map[string]bool, len(all))
	for _, n := range all {
		exists[n] = true
	}
	// Scan the characters one by one, and on each "-" try the segment to its left.
	//
	// **Do not write it as `for i := strings.Index(name, "-"); i > 0; i = strings.Index(name[i+1:],
	// "-") + i + 1`** -- that was this passage's original spelling, and it has a bug that **hangs
	// the whole daemon**: when there is no more "-" in `name[i+1:]`, `Index` returns -1, and `-1 +
	// i + 1` happens to equal `i`, so the index stays put and the loop never exits. The trigger is
	// as ordinary as can be -- the name the UI's "+ new tier" makes is `new-profile`: there is no
	// second hyphen after `new`, and `new` is not an existing tier, so **the very first snapshot
	// refresh loops forever** (measured 2026-09-20: CPU pegged, `/ui/api/snapshot` returning not
	// one byte, and the browser layer's "switch to the new card after create" never coming true).
	//
	// The symptom is hard to recognize because what hangs is **reading the snapshot**, a
	// path everyone shares: the whole UI freezes, while the file is created just fine on
	// disk -- it looks like the front end did not refresh, not like the backend spinning.
	for i := 0; i < len(name); i++ {
		if name[i] == '-' && exists[name[:i]] {
			return name[:i]
		}
	}
	return name
}

// brokenConcept is the card saying "this thing cannot be read right
// now": the identity is still reported (so the front end knows who is
// missing), the data is absent, and it cannot be written back either.
//
// Why not just omit the concept: the user would think that tier does
// not exist, and go looking elsewhere. Why not fail the whole snapshot:
// one bad file blanking the whole UI costs far more than it saves.
func brokenConcept(id, kind, title string, err error) view.Concept {
	return view.Concept{ID: id, Kind: kind, Title: title, Broken: err.Error()}
}

// ---------- tier mapping ----------

type bindingData struct {
	Provider string `json:"provider,omitempty"`
	Model    string `json:"model,omitempty"`
	Ref      string `json:"ref,omitempty"`
}

type roleData struct {
	ID       string        `json:"id"`
	Bindings []bindingData `json:"bindings"`
}

type providerChoice struct {
	Name     string   `json:"name"`
	Protocol string   `json:"protocol,omitempty"`
	BaseURL  string   `json:"base_url,omitempty"`
	KeyEnv   string   `json:"key_env,omitempty"`
	HasKey   bool     `json:"has_key"`
	Models   []string `json:"models"`
}

// profileData is all the material for the binding editor.
//
// Carrying providers is deliberate: **a candidate is a "provider x model" combination**,
// and the UI must let people choose with a dropdown rather than typing strings from memory.
// This list comes from the config (models each provider declared), and does not ask the
// upstream -- probing is `newgate probe`'s job, and UI loading should not touch the network.
type profileData struct {
	Profile     string `json:"profile"`
	File        string `json:"file"` // relative to the config root; sent back as is on save
	Base        string `json:"base"` // baseline (content hash), see store.WriteIfUnchanged
	Description string `json:"description,omitempty"`
	Default     bool   `json:"default"`
	Pinned      bool   `json:"pinned,omitempty"`
	Excluded    bool   `json:"excluded,omitempty"`
	Extends     string `json:"extends,omitempty"`
	// ExtendsOptions are the selectable parent tiers (other tier file names).
	// **They must come from the backend**: the UI does not know the notion of a
	// "tier file", it only knows "this cell has a dropdown with these options".
	ExtendsOptions []string         `json:"extends_options"`
	Roles          []roleData       `json:"roles"`
	Providers      []providerChoice `json:"providers"`
}

func profileConcept(name string, all []string) view.Concept {
	conceptID := "config.profile." + name
	file, err := profileFile(name)
	if err != nil {
		return brokenConcept(conceptID, view.KindMapping, name, err)
	}
	// Read the **unmerged** source text: changing one field of a compact
	// derived declaration (extends) should not expand it into the whole thing --
	// that would make the file balloon tenfold when the user clicks save once.
	pr, err := store.LoadProfileRaw(name)
	if err != nil {
		return brokenConcept(conceptID, view.KindMapping, name, err)
	}
	title := name
	if pr.Description != "" {
		title = name + " — " + pr.Description
	}
	return view.Concept{
		ID: conceptID, Kind: view.KindMapping, Title: title,
		// Order 10: tier files come after "upstream / global settings" (see the concepts comment).
		//
		// Group is **two levels** (see view.Concept.Group): the top group `Tiers`, and the
		// family computed by profileFamily (`claude-cheap` tucked under `claude`). Why the top
		// group is needed: in the left column "global settings / upstream" are **the two things
		// configured right after install**, while tiers are **a pile accumulated day by day**
		// -- not separated, those two sit flat with sixteen tiers and nobody can see these are
		// two kinds of things (the user's words: "profile could really use a section here").
		//
		// The top group's name goes through i18n (it is for humans); the family
		// name is the tier name itself (a machine marker, not translated).
		Order: 10, Group: i18n.T("Profiles", nil) + "/" + profileFamily(name, all),
		Data: profileView(name, file, pr),
		Apply: func(edit json.RawMessage, base string) (string, error) {
			return applyProfileRoles(conceptID, file, edit, base)
		},
		// Preview: what this controls card should show when the draft on
		// the source-text side looks like this (see view.Concept.Preview).
		//
		// Why this card needs it: the user pastes a whole tier file into the source-text column
		// and then wants to adjust one tier with a dropdown -- without Preview, the controls column
		// still holds **the content from before the change**, and that one edit submits the whole
		// old table, so what was just pasted is gone on the spot, while the screen never showed it,
		// so he would not feel he is overwriting anything (see the lastEdit passage in App.svelte).
		Preview: func(draft []byte) (any, error) {
			p, err := parseProfileDraft(file, draft)
			if err != nil {
				return nil, err
			}
			return profileView(name, file, p), nil
		},
		Actions: profileActions(name),
		Note:    profileNote(name),
	}
}

// profileNote says whether this tier card is **the one in effect right now**.
//
// Yes -> a green "current config" line; no -> draw nothing.
//
// # Why "buttons not drawn" cannot express this alone (pointed out by the user on 2026-09-21)
//
// The first version was like that: the one already in effect did not carry
// that pair of buttons (for the reason see profileActions), so of sixteen
// tier cards fifteen had two buttons and one had nothing. The user's words:
//
//	what you should inject is an info-only green: the current config / active profile effect!
//
// He was right, and what he pointed at is an **information gap**: "is this one in effect" is
// what he most wants to know when opening this section, and the UI then only hinted at it with an
// **absence** -- absence is invisible, and the two cards look almost identical, the only difference
// being two fewer buttons. Not drawing buttons and not drawing anything are two different things.
//
// The wording follows what the user gave: `active profile` (Chinese "current
// config"). It is a **statement**, not a command, so it does not use the accent
// color (that is for buttons) and goes through ToneOK -- green means "all normal,
// this is the one in effect now", the same tone as the `ok` cell in the health table.
func profileNote(name string) *view.Note {
	if store.LoadState().DefaultProfile != name {
		return nil
	}
	return &view.Note{Text: i18n.T("active profile", nil), Tone: view.ToneOK}
}

// profileActions are the buttons on this tier card;
// it returns empty when it **is already the default**.
//
// Why the already-default one carries none: "set this as default" shown on itself is
// pure noise, and with more buttons on a card the genuinely useful one gets drowned out.
// There is only one criterion (is the current DefaultProfile this one), so it cannot lie.
//
// Note "no buttons" **does not equal** "say nothing": that card has a green `active profile` line
// (see profileNote). With the two separated, this function only answers "which buttons to give".
//
// Why two rather than one: for changing the default profile, what the user often wants is not
// just "make this the default" but also "**all clients** follow it". Not following the clients set
// on their own is right (they were explicitly designated), but clicking through each is sixteen
// operations -- the second button does "set default" and "converge" in one, and does nothing else.
//
// # Wording: `use` / `use globally` (changed 2026-10-26; before it was `apply` / `apply to all`)
//
// The 2026-09-21 version said `apply` / `apply to all` (Chinese "apply
// / apply to all"). On 2026-10-26 the user pointed out it is **ambiguous**:
// `apply` in this UI can also read as "save this draft down", while what these
// two buttons do is "make it the one in effect". So the verb became `use`:
//
//	use           = use it from now on (becomes the global default)
//	use globally  = also push it to all clients (clear each client's own chain head)
//
// Both are still the same verb, differing only in scope -- what "two scopes
// of one thing" should look like. The object need not be spelled out: the
// button grows on **this tier's card**, and the card header carries its name.
func profileActions(name string) []view.Action {
	if store.LoadState().DefaultProfile == name {
		return nil
	}
	return []view.Action{
		{
			ID:    "set-default",
			Label: func() string { return i18n.T("use", nil) },
			Run:   func() (string, error) { return setDefaultProfile(name, false) },
		},
		{
			ID:    "set-default-all",
			Label: func() string { return i18n.T("use globally", nil) },
			Run:   func() (string, error) { return setDefaultProfile(name, true) },
		},
	}
}

// setDefaultProfile makes a tier the global default;
// with all, also clears each client's chain head.
//
// It **does not change this tier itself**: what the user wants is "use it as the default", not
// "change it a bit". So this button does not make the card dirty, nor produce a draft to save
// -- it is a write-to-disk action, after which the UI re-reads (see lib/view's Concept.Actions).
func setDefaultProfile(name string, all bool) (string, error) {
	if err := store.SetDefaultProfile(name, all); err != nil {
		return "", err
	}
	return "", nil
}

// profileView assembles an **already resolved** tier into all the material the editor needs.
//
// The load path and the preview path share it, for one reason: the two paths must give
// **exactly the same** shape. Written separately, "just after typing the source text"
// and "after saving" would draw two tables slowly growing crooked, and that inconsistency
// turns no test red -- only users notice (one of the two tables is missing some field).
func profileView(name, file string, pr *domain.Profile) profileData {
	// An unreadable provider table does not count as this card being broken: the tier itself
	// is fine and editable, only the candidate dropdown has no material (providers.json
	// not created yet is the normal state of a fresh install). This used to be "the
	// whole profile vanishes silently"; now it is "shown as usual, candidates empty".
	snap, snapErr := store.Load()
	if snapErr != nil {
		snap = &store.Snapshot{}
	}
	// The parent tier's options = other tiers (not itself: inheriting from itself
	// is a cycle and errors when resolving). The first is the empty string = **no
	// inheritance**, and it must have a visible position (the UI draws it as "-").
	extOpts := []string{""}
	for _, n := range profileNames() {
		if n != name {
			extOpts = append(extOpts, n)
		}
	}
	data := profileData{
		Profile: name, File: paths.RelToRoot(file), Base: store.Revision(file),
		Description: pr.Description, Pinned: pr.Pinned, Excluded: pr.Excluded, Extends: pr.Extends,
		Default:        snap.State != nil && snap.State.DefaultProfile == name,
		Providers:      providerChoices(snap),
		ExtendsOptions: extOpts,
	}
	for _, id := range roleOrder(pr) {
		rd := roleData{ID: id}
		for _, b := range pr.Roles[id] {
			rd.Bindings = append(rd.Bindings, bindingData{Provider: b.Provider, Model: b.Model, Ref: b.Ref})
		}
		data.Roles = append(data.Roles, rd)
	}
	return data
}

// parseProfileDraft parses **a draft's bytes** into a profile according to that file's format.
//
// Same set of branches as store.readProfileFile (.kv goes through KV parsing, .json
// through JSON), but the input is an in-memory draft rather than a file on disk
// -- which is exactly what preview needs: the copy **not yet written to disk**.
func parseProfileDraft(file string, draft []byte) (*domain.Profile, error) {
	if strings.HasSuffix(file, ".kv") {
		return store.ParseProfileKV(string(draft))
	}
	var pr domain.Profile
	if err := json.Unmarshal(draft, &pr); err != nil {
		return nil, err
	}
	if pr.Roles == nil {
		pr.Roles = map[string]domain.Candidates{}
	}
	return &pr, nil
}

// roleOrder is the display order of tiers: the four standard tiers in ladder order (heavy ->
// light), and the rest (including "*" and client-registered dynamic keys) after them by name.
//
// The order is not layout fastidiousness: **tiers are a ladder**, and sorted
// heavy->light in the UI the user can see at a glance "this chain goes downward";
// alphabetical gives h/l/m/n, and the reader has to assemble it himself.
func roleOrder(pr *domain.Profile) []string {
	var out []string
	seen := map[string]bool{}
	for _, id := range domain.Roles {
		if _, ok := pr.Roles[id]; ok {
			out = append(out, id)
			seen[id] = true
		}
	}
	var rest []string
	for id := range pr.Roles {
		if !seen[id] {
			rest = append(rest, id)
		}
	}
	sort.Strings(rest)
	return append(out, rest...)
}

func providerChoices(snap *store.Snapshot) []providerChoice {
	if snap.Providers == nil {
		return nil
	}
	// The model list is collected from **each profile's bindings**: those
	// are "the models this provider has used", closer to reality than the models
	// declared in providers.json (and independent of whether that field exists).
	used := map[string]map[string]bool{}
	for _, p := range snap.Profiles {
		for _, cands := range p.Roles {
			for _, b := range cands {
				if b.Provider == "" || b.Model == "" {
					continue
				}
				if used[b.Provider] == nil {
					used[b.Provider] = map[string]bool{}
				}
				used[b.Provider][b.Model] = true
			}
		}
	}
	names := make([]string, 0, len(snap.Providers.Providers))
	for name := range snap.Providers.Providers {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]providerChoice, 0, len(names))
	for _, name := range names {
		p := snap.Providers.Providers[name]
		pc := providerChoice{
			Name: name, Protocol: p.Protocol, BaseURL: p.BaseURL, KeyEnv: p.APIKeyEnv,
			HasKey: p.APIKey != "" || p.APIKeyEnv != "",
		}
		for m := range used[name] {
			pc.Models = append(pc.Models, m)
		}
		sort.Strings(pc.Models)
		out = append(out, pc)
	}
	return out
}

// applyProfileRoles changes only the roles section, keeping the other fields as is.
//
// Why not "serialize the whole profile back": what the UI shows is **concepts** (tiers,
// candidates), while the file also holds things it does not recognize (extends, window,
// compact, fields added later). Rewriting the whole thing amounts to overwriting the file
// with the UI's knowledge, and fields the user hand-wrote vanish in places he never touched.
func applyProfileRoles(conceptID, file string, edit json.RawMessage, base string) (string, error) {
	var patch struct {
		Roles map[string][]bindingData `json:"roles"`
		// Extends / Name / Delete come up on **the same card** as roles: one kv card is
		// all of that file (tiers, inheritance, rename, delete). The two-card approach was
		// tried -- the user's words were "these should go inside each kv", and with two
		// cards the "tier files" card and each kv's card said the same thing, redundantly.
		Extends *string `json:"extends"`
		Name    *string `json:"name"`
		Delete  bool    `json:"delete"`
	}
	if err := json.Unmarshal(edit, &patch); err != nil {
		return "", i18n.Ef(err, "the tier bindings in this request are not readable: {err}", i18n.A{"err": err})
	}

	if patch.Delete {
		// What is deleted is the whole file. The CAS uses base (the version at
		// load) -- if someone just changed it, do not delete, report a conflict.
		if err := store.RemoveIfUnchanged(file, base); err != nil {
			return "", profileErr("remove", file, err)
		}
		return "", nil
	}

	// roles being nil means **tiers were not touched this time**
	// (only inheritance or the name changed): keep what the file has.
	var roles map[string]domain.Candidates
	if patch.Roles != nil {
		roles = map[string]domain.Candidates{}
		for id, list := range patch.Roles {
			// An empty list is **kept** (an empty tier is written in), not treated as "delete this
			// key": the UI deleting all candidates of a tier means "this tier has no candidates" --
			// which is a different thing from "this tier is not written" (so it inherits upward),
			// and choosing the latter for the user amounts to changing semantics he never touched.
			cands := make(domain.Candidates, 0, len(list))
			for _, b := range list {
				cands = append(cands, domain.Binding{Provider: b.Provider, Model: b.Model, Ref: b.Ref})
			}
			roles[id] = cands
		}
	}

	// Rename = create a new file + delete the old one (two independent
	// CASes). The name must be a clean **file stem**: a name with a
	// path separator or ".." could write the file outside mappings/.
	newName := ""
	if patch.Name != nil {
		newName = strings.TrimSpace(*patch.Name)
		if newName == "" {
			return "", i18n.E("a profile must have a name — there is nothing to save it as", nil)
		}
		if strings.ContainsAny(newName, `/\`) || newName == "." || newName == ".." || strings.Contains(newName, "..") {
			return "", i18n.E("a profile name must not contain path separators or \"..\": {name}", i18n.A{"name": newName})
		}
	}
	curStem := strings.TrimSuffix(filepath.Base(file), filepath.Ext(file))
	if newName == "" || newName == curStem {
		newName = "" // unchanged
	}

	if strings.HasSuffix(file, ".kv") {
		b, err := os.ReadFile(file)
		if err != nil {
			return "", err
		}
		pr, err := store.ParseProfileKV(string(b))
		if err != nil {
			return "", err
		}
		if roles != nil {
			pr.Roles = roles
		}
		if patch.Extends != nil {
			pr.Extends = strings.TrimSpace(*patch.Extends)
		}
		if newName != "" {
			// .kv has **no name field** -- the name is the file name. So rename
			// = move the content as is under the new file name (the content
			// holds no name, so there is no "path and content disagree").
			return renameProfile(conceptID, file, newName, base, []byte(store.SerializeProfileKV(pr)))
		}
		return writeThrough(conceptID, file, base, []byte(store.SerializeProfileKV(pr)))
	}

	b, err := os.ReadFile(file)
	if err != nil {
		return "", err
	}
	// map[string]json.RawMessage: replace only the keys that were touched,
	// and the others are **not even parsed**, so this save cannot change
	// their shape (how numbers are spelled, key order, unknown fields).
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(b, &doc); err != nil {
		return "", i18n.Ef(err, "{file} is not valid JSON", i18n.A{"file": filepath.Base(file)})
	}
	if doc == nil {
		doc = map[string]json.RawMessage{}
	}
	if roles != nil {
		raw, err := json.Marshal(roles)
		if err != nil {
			return "", err
		}
		doc["roles"] = raw
	}
	if patch.Extends != nil {
		ext := strings.TrimSpace(*patch.Extends)
		if ext == "" {
			delete(doc, "extends")
		} else {
			raw, err := json.Marshal(ext)
			if err != nil {
				return "", err
			}
			doc["extends"] = raw
		}
	}
	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return "", err
	}
	if newName != "" {
		return renameProfile(conceptID, file, newName, base, append(out, '\n'))
	}
	return writeThrough(conceptID, file, base, append(out, '\n'))
}

// renameProfile renames a tier file: write one under
// the new name (content as is), then delete the old one.
//
// Two independent CASes: the new file must **not exist** (base=""), and the old file
// must still be **the version at load**. Any failure in between is reported faithfully --
// a half-done rename (both present, or the old gone and the new not created) is the state
// users least want to see, so better to say clearly in the error which step it stopped at.
func renameProfile(conceptID, oldFile, newName, base string, content []byte) (string, error) {
	newFile := filepath.Join(paths.Mappings(), newName+filepath.Ext(oldFile))
	if newFile == oldFile {
		return writeThrough(conceptID, oldFile, base, content)
	}
	if err := profileNameTaken(newName); err != nil {
		return "", err
	}
	if err := writeProfileNewFile(newFile, content); err != nil {
		return "", profileErr("create", newFile, err)
	}
	if err := store.RemoveIfUnchanged(oldFile, base); err != nil {
		return "", profileErr("remove old", oldFile, err)
	}
	return "", nil
}

// writeThrough is the exit the three appliers share: write to disk, and translate "the
// baseline does not match" into **the UI's conflict shape** (both source texts present).
//
// The translation is here rather than in store: store is the file layer, and should
// only say "the copy you hold is stale"; what "stale, so now what" means is between
// the UI and the user (lay both out and let them choose). This middle layer is the one
// that owns these files -- it knows what the file is called and whose concept it is.
func writeThrough(conceptID, file, base string, data []byte) (string, error) {
	rev, err := store.WriteIfUnchanged(file, base, data)
	var stale *store.StaleError
	if errors.As(err, &stale) {
		return "", &view.Conflict{
			Concept: conceptID, Path: paths.RelToRoot(file),
			Base: stale.Base, Current: stale.Current,
			Yours: string(data), Theirs: string(stale.Disk),
		}
	}
	return rev, err
}

// ---------- global switches ----------

// The shapes of toggleItem / stateData live in the contract package
// (`lib/view`'s Toggles / ToggleItem): the shape is **Kind's business**, and
// Kind is defined there. Before 2026-09-21 they lived here, only because at the
// time this was the only card using KindToggles; once modules/locale also needs
// to produce a language card, keeping them here would only mean being copied.
type toggleItem = view.ToggleItem
type stateData = view.Toggles

// stateConcept is **the fields in state.json that belong to config**.
//
// It reports only its own fields (other modules' sections have their own owners,
// see confighook's registry table): one module handing up all of state.json for
// the user to edit amounts to letting the UI make decisions for every module.
func stateConcept() view.Concept {
	file := paths.StateFile()
	snap, _ := store.Load()
	var st *domain.State
	if snap != nil {
		st = snap.State
	}
	return view.Concept{
		ID: "config.state", Kind: view.KindToggles, Title: i18n.T("Global settings", nil),
		Data: stateView(file, st),
		Apply: func(edit json.RawMessage, base string) (string, error) {
			return applyStateDefaultProfile("config.state", file, edit, base)
		},
		// Preview: see view.Concept.Preview. This pair (`config.state` is the
		// controls half, `config.file.state.json` the source-text half) is the same
		// thing as the tier pair, so without it the same bug would grow: change
		// the source text, then flip a switch, and the whole thing is overwritten.
		Preview: func(draft []byte) (any, error) {
			return stateView(file, parseStateDraft(draft)), nil
		},
	}
}

// stateView assembles a state into the material the switch card needs. Shared by load
// and preview (for the reason see profileView: the two paths must give the same shape).
func stateView(file string, st *domain.State) stateData {
	active, host := "", ""
	if st != nil {
		active, host = st.DefaultProfile, st.Host
	}
	return stateData{
		File: paths.RelToRoot(file), Base: store.Revision(file),
		Items: []toggleItem{{
			ID: "default_profile", Kind: "select", Value: active, Options: profileNames(),
			Label: i18n.T("Default profile", nil),
			Why:   i18n.T("Which profile the chain starts from when no agent-specific one is set.", nil),
		}, {
			// Listen address: loopback only by default, because 8899 can change config and flip switches.
			// Only someone who wants to open the UI from a tailnet / LAN needs it open (when open, the
			// web UI's Host gate also recognizes Tailscale subnets, see isTailscale in web-dashboard).
			ID: "host", Kind: "text", Value: host,
			Label:       i18n.T("Bind address", nil),
			Placeholder: i18n.T("empty = loopback only (127.0.0.1)", nil),
			Why: i18n.T("An IP (e.g. a Tailscale 100.x address) or 0.0.0.0 for every interface. "+
				"Empty = loopback only. Changing it takes effect after a restart.", nil),
		}},
	}
}

// parseStateDraft parses a state.json draft into a State.
//
// The same rules as store.LoadState (zero value + Normalize when unreadable -- that
// is deliberate fail-open, one hand-broken file should not stall every agent), except
// the input is **an in-memory draft**: preview wants exactly the copy not yet on disk.
func parseStateDraft(draft []byte) *domain.State {
	s := &domain.State{}
	_ = json.Unmarshal(draft, s)
	s.Normalize()
	return s
}

func applyStateDefaultProfile(conceptID, file string, edit json.RawMessage, base string) (string, error) {
	var patch struct {
		DefaultProfile *string `json:"default_profile"`
		// Host is the listen address (empty = loopback). It can be changed in the UI too, but **a
		// restart is needed** for it to take effect: changing the listen address means binding
		// again, which is not something the hot path can do (see the comment on forward.New). So
		// after this cell is saved, the UI still shows "what it is configured as", while which
		// one is really listening right now is another matter -- that sentence goes in why.
		Host *string `json:"host"`
	}
	if err := json.Unmarshal(edit, &patch); err != nil {
		return "", i18n.Ef(err, "the settings in this request are not readable: {err}", i18n.A{"err": err})
	}
	b, err := os.ReadFile(file)
	if err != nil {
		return "", err
	}
	// RawMessage for the same reason as applyProfileRoles: state.json holds several modules'
	// own sections, and rewriting it whole would overwrite others with this place's knowledge.
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(b, &doc); err != nil {
		return "", i18n.Ef(err, "{file} is not valid JSON", i18n.A{"file": filepath.Base(file)})
	}
	if patch.DefaultProfile != nil {
		raw, err := json.Marshal(*patch.DefaultProfile)
		if err != nil {
			return "", err
		}
		doc["default_profile"] = raw
	}
	if patch.Host != nil {
		h := strings.TrimSpace(*patch.Host)
		if h == "" {
			// Empty = loopback, which is the default -- delete the key rather than
			// write an empty string, one line less noise in the file, with exactly
			// the same meaning as "never configured" (see domain.State.BindHost).
			delete(doc, "host")
		} else {
			if net.ParseIP(h) == nil {
				return "", i18n.E("the bind address must be an IP (or empty for loopback only), got {value}",
					i18n.A{"value": h})
			}
			raw, err := json.Marshal(h)
			if err != nil {
				return "", err
			}
			doc["host"] = raw
		}
	}
	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return "", err
	}
	return writeThrough(conceptID, file, base, append(out, '\n'))
}

// ---------- provider table ----------

// providerFields are the keys this concept governs. **Keys not on this list are kept
// as is** -- a name with a date, a written note, or fields core adds later should not
// vanish just because the UI does not expose them (same reason see applyProfileRoles).
var providerFields = []string{"protocol", "base_url", "anthropic_url", "api_key", "api_key_env", "models"}

// providersConcept is providers.json's **structured editor**.
//
// Why the source-text card cannot do it: that file holds api_key, which is redacted whole
// before leaving the daemon (see redact), and the source-text card's Apply writes back
// the whole text -- writing it back lands *** on disk. So `config.file.providers.json`
// has always been read-only, while "add a provider" is exactly the first thing
// to do after installing newgate, and the UI should not be absent from that.
//
// The approach is to keep the value **inside the daemon**: the api_key cell does
// not enter the snapshot (see view.FieldSecret), the UI shows a placeholder hint, and it
// changes only when typed, untouched otherwise. So the browser never gets the credential,
// while adding, renaming, deleting and switching base are all fully available.
func providersConcept() view.Concept {
	file := paths.ProvidersFile()
	// Read the **raw form**, not store.LoadProviders: that one resolves api_key_env
	// into plaintext and fills it into APIKey, and writing its result to disk lands
	// the key on disk (see the comment on store.SaveProviders). "The UI cannot
	// get credentials" is here "not even my own read path carries plaintext".
	raw, err := readProvidersRaw(file)
	if err != nil {
		return brokenConcept("config.providers", view.KindRecords, i18n.T("Providers", nil), err)
	}
	names := make([]string, 0, len(raw))
	for name := range raw {
		names = append(names, name)
	}
	sort.Strings(names)

	items := make([]view.Record, 0, len(names))
	for _, name := range names {
		items = append(items, providerRecord(name, raw[name]))
	}
	return view.Concept{
		ID: "config.providers", Kind: view.KindRecords,
		Title: i18n.T("Providers", nil),
		// Order 1: after global settings (0), before tier files (10) -- "configure the upstream first,
		// then tier bindings can be picked"; this order is the same thing as the order of operations.
		Order: 1,
		Data: view.Records{
			File:     paths.RelToRoot(file),
			Items:    items,
			Base:     store.Revision(file),
			CanAdd:   true,
			AddLabel: i18n.T("+ provider", nil),
		},
		Apply: func(edit json.RawMessage, base string) (string, error) {
			return applyProviders(file, edit, base)
		},
	}
}

// providerRecord lays one provider out as a small form.
//
// The order is the order in which **reading from top to bottom is
// enough to configure one upstream**: who it is first (name, protocol,
// address), then the credential, and finally which models it offers.
func providerRecord(name string, fields map[string]json.RawMessage) view.Record {
	str := func(k string) string {
		var s string
		if raw, ok := fields[k]; ok {
			_ = json.Unmarshal(raw, &s)
		}
		return s
	}
	var models []string
	if raw, ok := fields["models"]; ok {
		_ = json.Unmarshal(raw, &models)
	}
	sort.Strings(models)

	// The hint on the credential cell must say clearly **what state it is in
	// now**, because the value itself never comes out: "already set (leave blank =
	// do not change)" and "not set yet" are the only difference the user can see.
	keyHint := i18n.T("not set — paste the key here", nil)
	if str("api_key") != "" || str("api_key_env") != "" {
		keyHint = i18n.T("set — leave empty to keep it", nil)
	}
	return view.Record{
		ID: name, Label: name, Removable: true,
		Fields: []view.Field{
			{ID: "name", Label: i18n.T("name", nil), Kind: view.FieldText, Value: name,
				Why: i18n.T("The name the tier bindings refer to. Renaming is a new provider: bindings that used the old name keep pointing at it.", nil)},
			{ID: "protocol", Label: i18n.T("protocol", nil), Kind: view.FieldSelect,
				Value: str("protocol"), Options: []string{"", "openai", "anthropic"},
				Why: i18n.T("Which dialect this upstream speaks. Empty means openai.", nil)},
			{ID: "base_url", Label: i18n.T("base URL", nil), Kind: view.FieldText, Value: str("base_url"),
				Placeholder: i18n.T("https://…", nil),
				Why:         i18n.T("Where requests go. The path is appended by newgate.", nil)},
			{ID: "anthropic_url", Label: i18n.T("Anthropic base URL", nil), Kind: view.FieldText, Value: str("anthropic_url"),
				Placeholder: i18n.T("only if the two dialects live on different bases", nil),
				Why:         i18n.T("Some upstreams serve the Anthropic dialect on a different base and do not forward between them (ARK is the case this exists for).", nil)},
			{ID: "api_key", Label: i18n.T("API key", nil), Kind: view.FieldSecret,
				Placeholder: keyHint,
				Why:         i18n.T("Never sent to the browser: this field arrives empty, and writing a value is the only way to change it.", nil)},
			{ID: "api_key_env", Label: i18n.T("API key from env", nil), Kind: view.FieldText, Value: str("api_key_env"),
				Placeholder: i18n.T("e.g. DEEPSEEK_API_KEY", nil),
				Why:         i18n.T("Reads the key from an environment variable instead of the file. Takes precedence over the field above.", nil)},
			{ID: "models", Label: i18n.T("models", nil), Kind: view.FieldLines, Value: strings.Join(models, "\n"),
				Placeholder: i18n.T("one per line", nil),
				Why:         i18n.T("Model names this provider serves, used to pick candidates for an explicit model request.", nil)},
		},
	}
}

// applyProviders writes the provider table in the UI back to providers.json.
//
// The semantics match the switches table: what the UI sends back is **all the records it holds**,
// and what is not sent is deleted. Two exceptions, both things "the UI cannot possibly know":
//
//   - an empty api_key = **do not touch it** (the value was
//     never given to the UI, so it cannot send it back).
//   - keys not in providerFields are kept as is.
func applyProviders(file string, edit json.RawMessage, base string) (string, error) {
	var patch struct {
		Items []struct {
			ID     string            `json:"id"`
			Values map[string]string `json:"values"`
		} `json:"items"`
	}
	if err := json.Unmarshal(edit, &patch); err != nil {
		return "", i18n.Ef(err, "the providers in this request are not readable: {err}", i18n.A{"err": err})
	}

	raw, err := readProvidersRaw(file)
	if err != nil {
		return "", err
	}

	out := map[string]map[string]json.RawMessage{}
	for _, it := range patch.Items {
		name := strings.TrimSpace(it.Values["name"])
		if name == "" {
			return "", i18n.E("a provider has no name — the tier bindings would have nothing to refer to", nil)
		}
		if _, dup := out[name]; dup {
			// Two records writing the same name: quietly keeping one
			// results in the provider the user edited being gone.
			return "", i18n.E("two providers are both named \"{name}\"", i18n.A{"name": name})
		}
		// Start from **its original record** (the UI opens the one by id, and the name
		// may be changed), so unknown keys and unexposed fields are carried over as is.
		next := map[string]json.RawMessage{}
		for k, v := range raw[it.ID] {
			next[k] = v
		}
		for _, k := range providerFields {
			v, given := it.Values[k]
			if !given {
				continue // no such field on screen: leave it alone
			}
			if k == "api_key" {
				if v == "" {
					continue // empty = leave it alone, see the paragraph above
				}
			}
			if k == "models" {
				list := splitLines(v)
				if len(list) == 0 {
					delete(next, k)
					continue
				}
				b, err := json.Marshal(list)
				if err != nil {
					return "", err
				}
				next[k] = b
				continue
			}
			if strings.TrimSpace(v) == "" {
				delete(next, k)
				continue
			}
			next[k] = mustJSON(v)
		}
		out[name] = next
	}

	// Other keys of the top-level document must be kept too (sections added later), so the source
	// text is changed **only in the providers section**. When the file does not exist (a fresh install
	// has not created it) start from an empty document: that is not an error, see readProvidersRaw.
	b, err := os.ReadFile(file)
	if err != nil && !os.IsNotExist(err) {
		return "", err
	}
	doc := map[string]json.RawMessage{}
	// An empty file (missing / just cleared) starts from an empty document: `json.Unmarshal(nil, ...)`
	// reports "unexpected end of JSON input", and that is not something the user can make sense of.
	if len(strings.TrimSpace(string(b))) > 0 {
		if err := json.Unmarshal(b, &doc); err != nil {
			return "", i18n.Ef(err, "{file} is not valid JSON", i18n.A{"file": filepath.Base(file)})
		}
	}
	body, err := json.Marshal(out)
	if err != nil {
		return "", err
	}
	doc["providers"] = body

	text, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return "", err
	}
	return writeThrough("config.providers", file, base, append(text, '\n'))
}

// readProvidersRaw reads the providers section of providers.json, **keeping
// each provider's raw JSON** (key order and unknown keys untouched).
//
// A missing file counts as an empty table: a fresh install has not created it yet --
// that is not an error, and the UI should be able to add the first one against it.
func readProvidersRaw(file string) (map[string]map[string]json.RawMessage, error) {
	b, err := os.ReadFile(file)
	if os.IsNotExist(err) {
		return map[string]map[string]json.RawMessage{}, nil
	}
	if err != nil {
		return nil, err
	}
	var doc struct {
		Providers map[string]map[string]json.RawMessage `json:"providers"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		return nil, i18n.Ef(err, "{file} is not valid JSON", i18n.A{"file": filepath.Base(file)})
	}
	if doc.Providers == nil {
		doc.Providers = map[string]map[string]json.RawMessage{}
	}
	return doc.Providers, nil
}

// splitLines splits "one item per line" multi-line text into
// a list: blank lines and surrounding whitespace are dropped.
func splitLines(s string) []string {
	var out []string
	for _, line := range strings.Split(s, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			out = append(out, line)
		}
	}
	return out
}

// mustJSON encodes a string into a JSON value. A string cannot fail to encode, so this
// does not return an error -- one fewer if that can never be reached at the call site.
func mustJSON(s string) json.RawMessage {
	b, err := json.Marshal(s)
	if err != nil {
		return json.RawMessage(`""`)
	}
	return b
}

// writeProfileNewFile writes a new file "that does not exist right now": requiring base="" makes
// WriteIfUnchanged (which also returns the new baseline) reject a concurrent create outright.
func writeProfileNewFile(path string, data []byte) error {
	_, err := store.WriteIfUnchanged(path, "", data)
	return err
}

// profileErr wraps the three classes of failure into one readable error located by file
// -- this is the "do not be silent" rule (an error carries context), and also "locate by
// file" (this path touches several files at once, and the user must know which one broke).
func profileErr(verb, file string, err error) error {
	name := filepath.Base(file)
	if sc, ok := err.(*store.StaleError); ok {
		return i18n.E("{verb} {file} failed: {err} (someone else changed it since you opened this page — reload and try again)",
			i18n.A{"verb": verb, "file": name, "err": sc.Error()})
	}
	return i18n.Ef(err, "{verb} {file} failed: {err}",
		i18n.A{"verb": verb, "file": name})
}

// ---------- source files ----------

type fileData struct {
	Path     string `json:"path"`
	Language string `json:"language"`
	Text     string `json:"text"`
	// Base is this file's baseline at load (a content hash, see store.WriteIfUnchanged).
	//
	// **Without it, the source-text column cannot be saved at all**: the UI hands this baseline
	// back when saving, and without a baseline it becomes "I thought it did not exist", while on
	// disk it does exist -- the CAS judges it stale on the spot and reports "this file was changed
	// by someone else after the page loaded". What the user sees is "editing the source text
	// and pressing save does nothing, while editing controls works fine" (measured 2026-09-20).
	Base string `json:"base"`
	// Redacted: this content has been **redacted** (credentials replaced
	// with ***), so it is read-only -- writing it back lands *** on disk,
	// which is data loss, far more serious than being unable to edit.
	Redacted bool `json:"redacted,omitempty"`
}

func fileConcepts() []view.Concept {
	var out []view.Concept
	add := func(path, language string) {
		rel := paths.RelToRoot(path)
		b, err := os.ReadFile(path)
		if err != nil {
			// When it cannot be read, report a broken card (rather than not reporting
			// it): a missing file name on the source-files tab makes the user think
			// the file does not exist, while it may just have wrong permissions.
			out = append(out, brokenConcept("config.file."+rel, view.KindCode, rel, err))
			return
		}
		text, redacted := redact(string(b), language)
		c := view.Concept{
			ID: "config.file." + rel, Kind: view.KindCode, Title: rel,
			// Order 20: the source-text half comes after all controls cards. It does
			// not take a slot in the left column anyway (it is folded away when the controls
			// half exists, see App's navUnits); this Order is for files that "have no controls
			// half and show their face on their own" (corner files other than providers.json).
			Order: 20,
			Data: fileData{
				Path: rel, Language: language, Text: text, Redacted: redacted,
				// The baseline is taken from **the bytes on disk** (not from the text
				// above -- text may have been rewritten by redaction, and a hash computed
				// from it would not match the disk; saving would still be judged stale).
				Base: store.Revision(path),
			},
		}
		if !redacted {
			c.Apply = func(edit json.RawMessage, base string) (string, error) {
				var patch struct {
					Text string `json:"text"`
				}
				if err := json.Unmarshal(edit, &patch); err != nil {
					return "", i18n.Ef(err, "the file content in this request is not readable: {err}", i18n.A{"err": err})
				}
				return writeThrough(c.ID, path, base, []byte(patch.Text))
			}
		}
		out = append(out, c)
	}
	add(paths.ProvidersFile(), "json")
	add(paths.StateFile(), "json")
	if ents, err := os.ReadDir(paths.Mappings()); err == nil {
		for _, e := range ents {
			if e.IsDir() {
				continue
			}
			name := e.Name()
			switch {
			case strings.HasSuffix(name, ".kv"):
				add(filepath.Join(paths.Mappings(), name), "kv")
			case strings.HasSuffix(name, ".json"):
				add(filepath.Join(paths.Mappings(), name), "json")
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// secretKeys are the field names that are **never sent out**.
//
// Listed explicitly rather than "just pick the fields I recognize": the source-files tab sends
// the whole file to the browser, and providers.json's api_key and state.json's control_token
// are credentials. The UI listens on loopback, but the loopback boundary means **any process
// on this machine** can read -- the credentials should not be protected only by this.
var secretKeys = map[string]bool{
	"api_key": true, "control_token": true, "root_key": true, "token": true,
}

func redact(text, language string) (string, bool) {
	if language == "json" {
		var doc map[string]any
		if json.Unmarshal([]byte(text), &doc) != nil {
			return text, false // unparsable: hand it over as is (read-only view, never written back)
		}
		if !scrub(doc) {
			return text, false
		}
		b, err := json.MarshalIndent(doc, "", "  ")
		if err != nil {
			return text, true
		}
		return string(b) + "\n", true
	}
	found := false
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		key, _, ok := strings.Cut(line, "=")
		if ok && secretKeys[strings.TrimSpace(key)] {
			lines[i] = key + "=***"
			found = true
		}
	}
	return strings.Join(lines, "\n"), found
}

func scrub(v any) bool {
	touched := false
	switch node := v.(type) {
	case map[string]any:
		for k, val := range node {
			if secretKeys[k] {
				node[k] = "***"
				touched = true
				continue
			}
			if scrub(val) {
				touched = true
			}
		}
	case []any:
		for _, item := range node {
			if scrub(item) {
				touched = true
			}
		}
	}
	return touched
}

// ---------- small helpers ----------

func profileNames() []string {
	names, err := store.ListProfiles()
	if err != nil {
		return nil
	}
	return names
}

// profileNameTaken says whether this **name** already has a tier file (either `.kv` or `.json`).
//
// Why judge by name rather than by "the path I am about to write": when the
// two suffixes coexist `.kv` wins (see store.LoadProfile), so writing a `demo.kv`
// while `demo.json` is still there amounts to **silently suppressing** that json --
// it is fine in the filesystem, but nobody will ever read it again (the user only
// sees "why is the tier I created empty"). Judging by name plugs that hole too.
//
// Why this must come **before** the CAS: the CAS can block the write, but its
// error is "(loaded (absent), on disk sha256:...) (changed by someone else after the
// page opened -- reload and retry)" -- while here nobody changed anything at all, and
// reloading and retrying ten thousand times gives the same result (measured 2026-09-21:
// renaming aaa to an existing bbb reported exactly this, and the user follows it,
// reloads, and gets stuck). Existence is **a settled fact**; say it as the fact.
func profileNameTaken(name string) error {
	if slices.Contains(profileNames(), name) {
		return i18n.E("a profile named {name} already exists — pick another name, or delete that one first",
			i18n.A{"name": name})
	}
	return nil
}

// profileFile is this profile's real body on disk (.kv first, the same read order as store).
func profileFile(name string) (string, error) {
	kv := filepath.Join(paths.Mappings(), name+".kv")
	if _, err := os.Stat(kv); err == nil {
		return kv, nil
	}
	js := filepath.Join(paths.Mappings(), name+".json")
	if _, err := os.Stat(js); err == nil {
		return js, nil
	}
	return "", fmt.Errorf("profile %s has no file", name)
}
