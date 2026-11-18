// Package view is the set of interfaces a module implements to contribute
// control-plane UI to the web interface, plus the identity of that contract.
//
// It is **two implementations of the same rule** as modules/cli/extension: the module
// keeps its knowledge, and the UI only orchestrates and renders. The reason is the same --
// once a UI (CLI or browser) reads a module's internals to display something, the two are
// welded together, and "whoever owns the state reports it" is the only way to undo that.
//
// # Why the contract is sunk into a leaf package
//
// The web UI is a heavy package (it imports gateway's metrics, config's store,
// the whole HTTP stack). If a module imported it just to get the "concept"
// type, it would drag all of that into its dependencies, so whoever imports that
// module can no longer be referenced from gateway/config tests -- a cycle. So
// the contract lives here, and the arrow is `modules/* → lib/view → component`.
//
// # Where the ledger is
//
// **The ledger (Registry) is held by the UI-providing module's own service** (the distribution's
// modules/web-dashboard), exactly as on the cli side: contributors get a Register, and the
// UI asks its own service for data. So web-dashboard knows no module, and adding a module's
// UI does not require changing it -- let alone the frontend (the frontend only knows Kind).
//
// Besides the produce function, registration also reports a **section name** (Section):
// the UI groups concepts by source, and "what this section is called" is the module's own
// business -- if the name lived in the UI, adding a module would mean changing the UI, exactly
// what the rule above avoids. The name is evaluated **at snapshot time**; see Section for why.
//
// # One concept = all the knowledge of one file
//
// A concept reports not only **data**, but also `Apply`: how to write it back to disk is the
// knowledge of **whoever owns that data**. This is what makes this thing hold up -- as soon as
// the UI writes files itself, it necessarily has to know the file format, the field semantics,
// and which fields must not be touched; that is exactly "the BFF knows config's internals".
//
// # Output happens **only when someone asks**
//
// Register only registers a produce function; it does not read disk, parse, or compute
// metrics. The produce function is called only when someone actually comes to look at the UI.
//
// This is not performance pedantry, it is a correctness matter for this thing:
// **every `newgate ...` process runs the modules' Start**, and the vast majority of them
// (typing a command, entering a shell, running a health check) have no one looking at
// the UI. Putting "read all config, parse every profile, parse which provider each tier
// binds to" at registration time means every CLI invocation pays that bill for nothing
// -- and what it buys is only "a snapshot nobody will open". The first version was
// written that way (Start read all the data straight into concepts), and it was deleted.
//
// The second thing it buys in passing: refreshing in the UI is real refreshing. The produce
// function reruns each time, so tier files the CLI just created and providers.json just changed
// all show up in the next snapshot -- a list counted once at assembly time will eventually
// be wrong (the daemon is long-lived, while files can be touched by the CLI at any time).
package view

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"

	modules "github.com/rzbdz/newgate/component"
	"github.com/rzbdz/newgate/lib/i18n"
)

// Kind is the name of the rendering style. **The frontend only recognizes
// these**: adding a UI for a new module needs no frontend change, unless it
// brings a new **shape** (then both sides change, which is the honest price).
const (
	// KindMapping: the "list within a list" binding editor for tiers/candidates:
	// each entry is an ordered set of candidates, in fallback order.
	KindMapping = "mapping-editor"
	// KindCode: source files (an editor with syntax highlighting).
	KindCode = "code"
	// KindToggles: a set of switches + selectors (each with one sentence of why).
	KindToggles = "toggles"
	// KindSeries: counters/metrics.
	KindSeries = "series"
	// KindTable: a read-only table.
	KindTable = "table"
	// KindRecords: a set of **structured records**: a few
	// fields each, with the fields declaring their own types.
	//
	// How it splits from KindToggles: toggles are "a set of homogeneous booleans/single
	// choices", one value each; here each entry is a **small form** (a provider's
	// base_url, protocol, key…), and the count also grows and shrinks. Both are
	// needed, because squeezing them into one shape leaves one of them awkward forever.
	KindRecords = "records"
	// KindLog: a log stream.
	KindLog = "log"
	// KindChains: the candidate chains laid out on one screen: one card per
	// profile, one tier per row, and in the row this chain's current state.
	//
	// How it splits from KindTable: a table is "one fact per row", read-only; here every row
	// **carries a destination** ("this tier currently takes this chain, and can also switch
	// to whom"), and that destination grows out of this row's data (see ChainRow.Actions).
	// The split from KindRecords is even clearer: each record is a **small form** (editing
	// some field's value), while what is wanted here is **live state** -- "what this tier
	// resolves to right now" is a conclusion computed by resolve, not some literal in a file.
	KindChains = "chains"
	// KindOverview is the **first screen**: a routing overview sectioned
	// by agent -- an agent tab on top, then a stack of "one per profile"
	// cards, each showing its chain as it is now **plus** each hop's health.
	//
	// How it splits from KindChains: chains answers "what does this tier resolve to" (a
	// pure config conclusion, independent of who says it or on which screen, and `newgate tier`
	// wants the same one); overview answers "which one should I have this agent use now" -- that
	// puts **two ledgers** side by side (the chain computed from config + the health recorded by
	// probes), plus a "use this one" action. Together they are the "switch a chain" operation.
	//
	// # Why health must be joined by the contributor, not by the frontend
	//
	// The chain conclusion lives in config (`configapi.AllChains`); each binding's latency and
	// probe conclusion live in breaker (its own ledger). Having the frontend join the two tables
	// by `provider/model` can indeed draw the same picture, at the cost of **two things only
	// the owner knows being copied into the UI**: which cell on a chain card is the "chain head"
	// (and what to do with an empty chain), and how much latency counts as slow (the threshold
	// is in breaker/status, one place only). Both of these will change in their own modules,
	// and nothing on the frontend's copy will go red. So the join happens **in the backend**,
	// produced by a contributor who can see both ledgers at once (see the comment in overview.go).
	KindOverview = "overview"
	// KindFingerprints is a **model fingerprint** screen: for each profile, list what each binding
	// last verified as "which model this upstream actually is", and allow verifying again on the spot.
	//
	// Why it is not Table/Records: one fingerprint verification **is an action that costs
	// tokens and takes tens of seconds to yield a result** (the upstream spews hundreds
	// of integers, and they are compared locally against a reference library). A table
	// expresses an already-computed fact; what is wanted here is the live state "this row
	// can be pressed now, and will change after pressing", and each entry's state advances
	// independently from queued to scored -- that is a **process**, not one cell of data.
	//
	// Difference from Overview: overview merges "the chain computed from config" with
	// "the health table"; here there is only one kind of material (fingerprints),
	// but one extra free-form panel of "pick an upstream, pick a model, ask for
	// a fingerprint on the spot". So it is a new shape, not a variant of one.
	KindFingerprints = "fingerprints"
)

// Table is the data shape of KindTable: some columns + some rows.
//
// Why the shape is fixed here rather than each module assembling its own map is the
// same as for the Kind constants: the frontend only recognizes this one arrangement.
// Everyone's table looks like this, so the frontend has only one renderer.
type Table struct {
	Columns []Column `json:"columns"`
	Rows    []Row    `json:"rows"`
}

// Row is one row in a table.
//
// Why cells are "a map indexed by column name" rather than an array: column order
// is a **display** matter (the frontend can reorder for narrow screens, and in the future
// let the user drag), while the correspondence between cells and columns is a **data**
// matter. Pinning the two with array indices means any change of column order shifts every
// cell -- and that error still looks normal (every cell has a value, just the wrong one).
type Row struct {
	// ID is the machine marker of **this row**: used for action callbacks and as the
	// frontend's key. Empty = this row has no actions (then nobody needs it stable).
	ID string `json:"id,omitempty"`
	// Cells is "column ID -> cell". Missing columns render as blank, not an error: a table's rows may
	// legitimately have sparse fields (say a "count only, not delisted" row has no cooldown moment).
	Cells map[string]Cell `json:"cells"`
	// Actions are the buttons on this row (shape: see Action).
	//
	// The third place with the same rule as Concept.Actions: **whoever owns the knowledge
	// reports it**. Here there is one more reason -- table rows are **grown from data** (which
	// bindings are in the health table today depends on which requests have run), so "what
	// can this row do" can only be settled when the row is constructed. The contributor builds
	// a closure per row (capturing that row), and the UI just draws the buttons it is given.
	Actions []Action `json:"actions,omitempty"`
}

// Column is one column of the header.
type Column struct {
	// ID is this column's **identity**: row cells are
	// indexed by it. A stable machine marker, not translated.
	ID string `json:"id"`
	// Label is the human-facing header (may be translated, may change).
	Label string `json:"label"`
	// Align: right-aligns when "right". Numeric columns need
	// it -- left-aligned numbers cannot be compared vertically.
	Align string `json:"align,omitempty"`
}

// Cell is one cell.
type Cell struct {
	Text string `json:"text"`
	// Tone is this cell's color semantics, taking the first three of cli's
	// ok/warn/bad/skip (no "skip": that means "this step did not run",
	// which only makes sense for a process and is useless in a table).
	//
	// Empty string = no color. **Do not** pass ANSI here: color is the render layer's
	// business, the kernel only gives semantics, otherwise the same data would need
	// the escapes written twice, once for the web page and once for the terminal.
	Tone string `json:"tone,omitempty"`
	// When Href is non-empty this cell is a **link** (the UI renders it as <a>).
	//
	// Why the address is given by the contributor: it is often **computed**, and only the
	// contributor knows the algorithm -- "that remote UI is mounted under this prefix on this
	// port" is the business of the module that owns it, and the UI knows neither the port nor
	// the prefix (see the path rewriting in modules/ssh-tunnel). Having the frontend assemble
	// it would copy that knowledge into a second place, and the two will eventually disagree.
	//
	// A new tab is **fixed**, not for convenience: the UI is a single-page app **with
	// drafts**, and the current page may hold an unsaved edit; a link replacing it would throw
	// that draft away. This and nav.ts's hash routing are two sides of the same consideration.
	Href string `json:"href,omitempty"`
}

// Tone values. The frontend colors by these words and treats everything else as not given.
const (
	// The Fields values for a section (Section): **what this section is for**. See SectionInfo.Fields.
	//
	// The three names describe three kinds of things on the UI, not module
	// categories: "which way requests go" is the viewing half, "things that can
	// be changed" is settings, and "one client's own cells" is the third kind.
	//
	// # The vocabulary must **avoid module names** (`config` especially must not be a value)
	//
	// The UI side has a ratchet: any module name appearing in `web/src` goes red (see
	// moduleLiterals in nav_test.go). The value `"config"` collided with that module name
	// -- and the ratchet was right: it cannot tell "this is a module name" from "this
	// is a vocabulary word that happens to match", and letting it through would create
	// an unclear exception for "the frontend knows modules". So it is called `settings`
	// here (changed from `config` on 2026-09-29, when it had only been added a day).
	//
	// These three words are the same kind of thing as the Kind names the frontend recognizes
	// (`overview` / `chains` / …): **the UI's own vocabulary**, not the identity of any module.
	FieldRoutes   = "routes"
	FieldSettings = "settings"
	FieldClients  = "clients"

	ToneOK   = "ok"
	ToneWarn = "warn"
	ToneBad  = "bad"
)

// Records is the data shape of KindRecords: a set of records, each with a few named fields.
//
// The **type** of each field (text / select / secret / lines) is declared by the contributor: it
// knows whether this field is a closed set (only two protocols exist), free text, or a credential
// the browser should not see. The frontend renders only by these four words and knows no concrete
// field -- "adding a module's UI needs no frontend change" is the same rule here as elsewhere.
type Records struct {
	// File is **which file** this record set writes, relative to the config
	// root (empty = it does not correspond to any editable file). The UI uses it to pair
	// the "controls" half and the "raw text" half side by side (see fileOf in nav.ts: it
	// applies to every shape carrying this field, it is not a hole opened for one Kind).
	File  string   `json:"file,omitempty"`
	Items []Record `json:"items"`
	// Base is the baseline this file had when it was loaded (a content hash,
	// see store.WriteIfUnchanged). A writable record set must have it: without
	// a baseline, saving cannot tell "is the copy in my hand the latest", and
	// that is the only defense against "the CLI just changed the same file" (see
	// the comment on Concept.Apply). A read-only record set leaves it empty.
	Base string `json:"base,omitempty"`
	// When CanAdd is true the UI gives an "add one" button, and AddLabel is its text. Why
	// not every Records can add: some record sets are decided by something else (say "which
	// modules this build has installed"), and giving it a button that does nothing is a lie.
	CanAdd   bool   `json:"can_add,omitempty"`
	AddLabel string `json:"add_label,omitempty"`
	// Blank is **what a newly added one looks like**: the field shape (ID / Label
	// / Kind / Options / Why), with values always ignored (a new one starts empty).
	//
	// Why this needs its own cell: the field shape lives on **each record**
	// (see Record.Fields), so the frontend rendering a new card can only copy an existing
	// one -- they are all the same, because the same contributor built them from the same
	// declaration. That approach fails when there is **not a single one**: `Items` empty,
	// `CanAdd` true, and pressing "add" yields an **empty card with not one input box**,
	// leaving the user no way to put anything in (hit by `modules/ssh-tunnel` on its
	// first assembly: a config with no target configured yet is exactly its normal state).
	//
	// So the shape is stated explicitly by the contributor here. Leaving it
	// empty is legal (old contributors need not change), and then the frontend
	// falls back to copying the first one -- today's behavior is unchanged.
	Blank []Field `json:"blank,omitempty"`
}

// Record is one record.
type Record struct {
	// ID is this record's identity **right now** (renaming means a different record:
	// the ID changed). Empty = newly added in the UI, which the contributor treats
	// as "add". It is also what Apply uses to match things up on the way back.
	ID     string  `json:"id"`
	Label  string  `json:"label"`
	Fields []Field `json:"fields"`
	// When Removable is true the UI gives a delete button. Deliberately **not**
	// on by default: deleting a record usually means deleting a piece of config on
	// disk, and that kind of action should be stated explicitly by the contributor
	// ("this one can be deleted") rather than inferred from "it is a record".
	Removable bool `json:"removable,omitempty"`
	// Base is the baseline of the file **this record** represents, when it was loaded; see
	// Records.Base. Some record sets have one file per record (say "these tier files"), and
	// deleting that record deletes that file, so this record's own version is needed to tell
	// "no one changed it first". The UI passes it back verbatim, without interpreting it.
	Base string `json:"base,omitempty"`
}

// Field is one field in a record.
type Field struct {
	// ID is the field name (the key on the Apply round trip). A machine marker, not translated.
	ID string `json:"id"`
	// Label is the human-facing field name (may be translated).
	Label string `json:"label"`
	// Kind decides how to render: text (plain text) / select (pick one from Options) / secret
	// (credential: **the value is not in the snapshot**, the UI shows a placeholder, and it
	// changes only when typed) / lines (multi-line, one item per line, for a list of names).
	Kind string `json:"kind"`
	// Value is the current value. **The Value of a secret
	// field is always an empty string** -- see the note below.
	Value   string   `json:"value,omitempty"`
	Options []string `json:"options,omitempty"`
	// Placeholder is the hint when empty (the UI draws it inside the input box).
	Placeholder string `json:"placeholder,omitempty"`
	// Why is one sentence on what this field affects (given on hover).
	Why string `json:"why,omitempty"`
}

// The field type values.
const (
	FieldText   = "text"
	FieldSelect = "select"
	// FieldSecret is a **credential**: the value never appears in the snapshot,
	// and the UI gets an empty string plus a placeholder hint. Sending back an
	// empty string = "leave it alone"; only a newly typed value counts as a change.
	//
	// Why not "mask it as *** and let the UI edit it": the raw text in the UI's hands
	// has already been replaced by ***, and writing it back would put *** on disk -- that
	// is data loss, far worse than not being able to change it (the same reason applies to
	// the Redacted section in modules/config/view.go). So the approach here is **the value
	// never leaves**: what the browser does not have cannot be written back verbatim.
	//
	// The test is "would leaking this hurt", not "is
	// it a password": api_key is, base_url is not.
	FieldSecret = "secret"
	// FieldLines is multi-line text, one item per line (the "this provider's model list" kind).
	FieldLines = "lines"
)

// Toggles is the data shape of KindToggles: switches / single
// choices / single-line text, each with one sentence of why.
//
// # Why these types live in the contract package, not in some user's home
//
// Before 2026-09-21 they were defined in `modules/config/view.go` (the "global settings" card).
// There was only one user then, so it looked fine; once a second module wanted to produce the same
// Kind (the "language" card in modules/locale) there were only two paths: copy these field names,
// or drag config into one's own dependencies. Both are what this design is meant to avoid -- **the
// shape is the Kind's business, and Kind is defined here**, so the shape is defined here too, the
// same sentence as "the frontend only recognizes this one arrangement" in the KindToggles comment.
//
// What the frontend reads is exactly these JSON fields
// (kinds/Toggles.svelte); renaming means changing both sides.
type Toggles struct {
	// File is **which file** this card writes, relative to the config root (empty
	// = it does not correspond to an editable file). Same meaning as Records.File:
	// the UI uses it to pair the "controls" and "raw text" halves side by side.
	File string `json:"file,omitempty"`
	// Base is the baseline this file had when it was
	// loaded (a content hash, see store.WriteIfUnchanged).
	Base  string       `json:"base,omitempty"`
	Items []ToggleItem `json:"items"`
}

// ToggleItem is one toggle cell.
type ToggleItem struct {
	// ID is this cell's machine marker (the key on the Apply round trip). Not translated.
	ID string `json:"id"`
	// Label is the human-facing name (may be translated).
	Label string `json:"label"`
	// Kind decides how to render: select (pick one from Options) / switch (On) / text (single line).
	Kind string `json:"kind"`
	// Value is the current value of a select/text cell; On is the state of a switch cell.
	//
	// **An empty string is sent too** (no omitempty): for a select the empty
	// string is a **legal value**, not "this cell is absent" -- this repo has such dropdowns
	// (the "follow system" of the language cell is the empty string, ditto the skin cell).
	// Omitting it leaves the frontend with undefined and it has to `?? ""` on its own; that
	// fallback happens to be right, but by luck: a dropdown whose option table **does not
	// contain the empty string** would then display the first item, while the real value is
	// empty -- the screen says something different from what is stored, and nothing goes red.
	//
	// A switch cell therefore carries an extra `"value": ""`. Those few bytes buy the rule "the value
	// is always in the payload" that can be relied on (the same as ready / own in OverviewAgent).
	Value string `json:"value"`
	On    bool   `json:"on,omitempty"`
	// Options are the candidates of a select cell (**machine values**, not translated).
	Options []string `json:"options,omitempty"`
	// OptionLabels gives one value a **human-facing wording** (value -> display). Unlisted
	// values display as the value itself; an unlisted empty value displays as "—".
	//
	// Why this is needed instead of writing the wording straight into Options: those values in Options
	// **are stored into the config** (tier names, profile names, `inherit`), so changing one character
	// in the display layer costs an unrecognized value in the config. And some values **cannot
	// be shown to the user directly at all** -- the empty value of the chain-head cell is exactly
	// that: it means "follow the global default", and only the backend knows who the default is.
	//
	// More importantly, it separates two things that are **completely different
	// in config**: the user picked `ds` (`active: {claude: ds}` recorded in state.json)
	// versus the user picked nothing and the global default just happens to be `ds`
	// right now (no such entry in state.json). Their **current value** in this cell is
	// the same string, and without a different wording it cannot be told apart -- while
	// their behavior differs in "will it follow when the global default changes later".
	OptionLabels map[string]string `json:"option_labels,omitempty"`
	// Why is one sentence on what this cell affects (given on hover).
	Why string `json:"why,omitempty"`
	// Placeholder is the hint in an empty cell (say "empty = loopback only").
	// When empty it deserves to be seen more than why: the user is looking at an
	// empty input box, and the first sentence has to tell them what empty means.
	Placeholder string `json:"placeholder,omitempty"`
	// Group is the heading that splits a layer **inside a card**: when the value changes, the UI
	// inserts a small heading before this cell (see kinds/Toggles.svelte). Empty = no grouping.
	//
	// Not the same as Concept.Group: that one splits **sections in the sidebar**,
	// this one splits **rows within one card**. opencode's omo slots use it to split
	// agents and categories into two blocks -- when a dozen dropdown rows are packed
	// together, those two small headings are the only thing that lets the eye rest.
	//
	// It is in the contract rather than in some one module: the shape belongs to Kind, and "how
	// it is laid out in the UI" already has one implementation (the frontend renderer); adding
	// a field only one module recognizes would make other modules' card grouping fail silently.
	Group string `json:"group,omitempty"`
}

// ToggleKind values.
const (
	ToggleSelect = "select"
	ToggleSwitch = "switch"
	ToggleText   = "text"
)

// Chains is the data shape of KindChains: a stack of
// profile cards, each listing this moment's chain per tier.
//
// # Why a card is "one profile"
//
// The chain is not one global thing: one config can have several profiles such as `ark`
// and `kimi`, each with candidates per tier, and state.json decides who is the chain head
// (see docs/04-configuration.md). So the unit for "see my chains on one screen" can only be
// the profile -- as one big table, the first column would have to be the profile name, and
// that string repeated a dozen times is the last thing this page should make anyone read.
//
// # Why a row is "one tier"
//
// The question users ask is "who will this request go to", and it is asked per
// tier (`newgate tier normal`). Each row's Head is the answer to that question,
// Steps is the answer expanded, and Note explains the ones that were skipped.
type Chains struct {
	Cards []ChainCard `json:"cards"`
}

// ChainCard is one profile's chain.
type ChainCard struct {
	// Profile is this profile's name (a machine value, not translated).
	Profile string `json:"profile"`
	// File is which file it writes, relative to the config root. Same meaning as Records.File: the
	// UI uses it to pair the "controls" and "raw text" halves side by side (see fileOf in nav.ts).
	File string `json:"file,omitempty"`
	// Default true means this is the one in effect right now (the chain head). The UI draws
	// it as a mark, with the same meaning and tone as "current config" in Concept.Note.
	Default bool `json:"default,omitempty"`
	// Roles lists them by tier. **The order is given by the contributor** (who
	// comes first is a product trade-off: both `newgate status` and doctor
	// order by capability from high to low, and this follows the same rule).
	Roles []ChainRow `json:"roles"`
}

// ChainRow is the chain of **one tier** within one profile.
type ChainRow struct {
	// ID is the machine marker of **this row**: used for action callbacks and as the
	// frontend's key. Empty = this row has no actions (then nobody needs it stable).
	//
	// The fifth place with the same rule as Row.ID, and the reason is harder here: a table row is one
	// binding, while here a row is **one tier of one profile** -- `Tier` is unique within a card but
	// **not unique within the whole concept** (ten profiles mean ten `heavy`s). So "switch this tier
	// to X" cannot be located by tier name alone; the row has to carry something that can be matched.
	//
	// It is a machine marker (`demo/heavy` style), not translated,
	// and the UI should not split it apart and interpret it.
	ID string `json:"id,omitempty"`
	// Tier is the tier name (a machine value, not translated).
	Tier string `json:"tier"`
	// Label is the human-facing name (may be translated). Empty = show Tier directly.
	Label string `json:"label,omitempty"`
	// Head is this tier's first hop right now (`ark/deepseek-v3`);
	// empty = this tier has no usable candidates.
	//
	// It has its own cell rather than being "Steps[0]": the only question the user will ask is "why is
	// it not the one I expected", and that answer's focal point is the chain head. Having the UI fetch
	// the first one itself would copy the judgment "what to do with an empty chain" into the frontend.
	Head string `json:"head,omitempty"`
	// Steps is the **whole** chain, chain head included, in actual attempt order.
	Steps []ChainStep `json:"steps,omitempty"`
	// Note is the one-line tail of the chain: "who else
	// was skipped, and why". Empty = nothing was skipped.
	//
	// How it splits from Step.Note: that one is why **some step** could not rank higher,
	// this one is the summary of **the whole chain** (today it is the "skipped N candidates"
	// line at the tail of `newgate tier`). Both are needed, because skipped candidates often
	// appear in no step at all -- written only on steps, they would have nowhere to go.
	Note string `json:"note,omitempty"`
	// Tone is this row's tone (ToneOK/ToneWarn/ToneBad, empty = no color).
	//
	// Today it only expresses "is there a usable chain head": an empty chain is bad
	// (this tier has nowhere to go right now), a chain is empty-toned (no color in the normal
	// state). It **does not express health** -- that needs probing, and is a different ledger.
	Tone string `json:"tone,omitempty"`
	// Actions are the buttons on this row ("switch this tier to X" and the like); shape: see Action.
	//
	// The fourth place with the same rule as Row.Actions: actions are **grown
	// from data** (who is on this tier's chain depends on what the config looks
	// like right now), so "what this row can do" can only be settled when the
	// row is built. The UI just draws the buttons it gets and routes clicks back.
	Actions []Action `json:"actions,omitempty"`
}

// ChainStep is one hop on the chain.
type ChainStep struct {
	// Provider / Model are this hop's binding (machine values, not translated).
	Provider string `json:"provider"`
	Model    string `json:"model"`
	// Profile is which profile this hop comes from.
	//
	// Why it is needed: a chain **crosses profiles** (this profile's candidates are
	// followed by others'; see "profile is the primary order" in resolve.BuildChain).
	// Without the source written out, the user would think every hop on the chain is in
	// the file they are looking at, and go looking for a candidate that does not exist.
	Profile string `json:"profile,omitempty"`
	// Note is one sentence about this hop (today only Skip.Reason: demoted/skipped and the like).
	Note string `json:"note,omitempty"`
	// Tone is this hop's tone (ToneOK/ToneWarn/ToneBad, empty = no
	// color). The same vocabulary as Cell.Tone, for the same reason: color
	// is the render layer's business, the kernel only gives semantics.
	Tone string `json:"tone,omitempty"`
}

// Overview is the data shape of KindOverview: **a routing overview split by agent**.
//
// # Why two axes
//
// The decision the user makes is "which tier should **this** client go through".
// The former is the clients installed on a few machines (claude / codex / …, usually two
// or three per machine); the latter is the dozen or so profiles in the config. So the
// page's shape is these two axes: Agents are the tabs on top, Cards are the cards below,
// one per profile showing its chain as it is now. Cards are ordered by Profile (the
// order the contributor gives, matching `configapi.AllChains`: the one in effect first).
//
// # Why "who this agent goes through now" is on Agent, not on the card
//
// Because it is an **attribute of the agent**, not of the profile (the same
// profile can be claude's chain head while codex goes through something else).
// Written into the card, one profile would have to split into "the one for claude"
// and "the one for codex", while their data is identical except that one boolean.
type Overview struct {
	// Agents are the tabs on top, **in the order the contributor gives**
	// (today by the agent's machine marker, stable across restarts -- a
	// UI whose tabs jump makes the user hunt for the same one every time).
	Agents []OverviewAgent `json:"agents"`
	// Auto is the **"automatic"** card at the front of the grid (see OverviewCard.Auto).
	//
	// It is an **indirect reference**: it is not itself a profile, but the reference
	// "global default". Which one it points to right now is stated by Profile; which
	// ones it **can** point to is stated by the group of `use:<global>:<profile>`
	// in its own Actions (picking one = changing the global default).
	//
	// When it is empty: this process has no selectable profile (not a
	// single tier). Then it should not draw an empty card with no members.
	Auto *OverviewCard `json:"auto,omitempty"`
	// Cards is one card per profile.
	Cards []OverviewCard `json:"cards"`
}

// OverviewAgent is one tab on top: which profile a client is currently pointed at.
type OverviewAgent struct {
	// Icon is this client's icon in the UI: the `d` string of an SVG path, `24 24` viewport.
	// **Empty = it gave none** -- the UI then draws its own generic mark, not a blank.
	//
	// Declared by the client itself and carried here verbatim (see
	// confighook.Agent.Icon): the UI knows no client, it just puts this string
	// into an `<svg>`, with color via `currentColor` (so it follows the skin).
	Icon string `json:"icon,omitempty"`
	// ID is the machine marker (`claude` / `codex`, the same name as in `newgate <agent>`): it
	// is also **the second half of the action ID** (see `use:<agent>` in OverviewCard.Actions),
	// so it is the test the UI uses to pick the button in this section. Not translated.
	ID string `json:"id"`
	// Name is the human-facing name (may be translated).
	Name string `json:"name"`
	// Profile is which profile it starts its chain from right now (a machine value,
	// not translated). Empty = never specified, i.e. **follow the global default** --
	// that is not "no chain", and the UI must say so clearly (see `ActiveFor` in docs).
	Profile string `json:"profile,omitempty"`
	// Own false = this agent **has no setting of its own**; the Profile above
	// is the result of following the global default, not its own choice.
	//
	// # Why this distinction must exist
	//
	// "Pin claude to ds" and "claude follows the global, and the global happens to be ds"
	// are **two different states**: the former's routing stays ds after the global changes, the
	// latter changes with it. With only Profile in the payload the two states look identical,
	// and the UI can only describe them the same way -- so - the user cannot tell "will
	// changing the global also change this one"; - and there is no way to express "set this one
	// back to automatic" (that action's premise is exactly "it currently has its own setting").
	//
	// Same as `omitempty`: both values of this sentence
	// are meaningful, and dropping one is dropping a state.
	Own bool `json:"own"`
	// Ready false means this machine **does not have this client** (the test is given
	// by whoever reports this agent, today the confighook "is it installed" question).
	// The missing tab is still drawn as usual -- hiding it would make the user think the
	// client is unsupported, while the truth is "it exists, just not on this machine".
	//
	// **No `omitempty` may be added**: both values of this sentence are meaningful;
	// dropping `false` makes "not installed" and "installed" identical on the wire (both
	// have the field absent), so the UI can draw neither the gray of that tab nor the "not
	// installed" mark -- and it will not report any error, those two just **never appear**.
	Ready bool `json:"ready"`
}

// OverviewCard is one profile's card on this home page.
type OverviewCard struct {
	// Auto true means **this card is the "automatic" card, not a profile**.
	//
	// It and the other cards are two different things, yet they **look almost the same**
	// both are "a name + a table + a set of actions"), so this marker is required: the
	// UI uses it to decide what the title says and what selecting this card means (on
	// other cards it is "pin this one", on this card it is "follow the global default").
	//
	// "Automatic" and "pinned to some profile" are **two states**, even when the profile resolved
	// right now is identical: after the global default changes, the former's routing changes with
	// it and the latter does not. Merging them into one card leaves the user no way to express
	// "I really want this one" -- which is exactly why this cell exists (in Clash the same
	// group can both pick a node directly and reference another group; it is the same thing).
	Auto bool `json:"auto,omitempty"`
	// Profile is this profile's name (a machine value, not translated).
	//
	// On the "automatic" card it is **the one it points to right now** (empty = none at all).
	Profile string `json:"profile"`
	// File is which file it writes, relative to the config
	// root (empty = unavailable). Same meaning as Records.File.
	File string `json:"file,omitempty"`
	// Default true means this is the **global default** (every
	// agent without its own setting starts its chain from it).
	Default bool `json:"default,omitempty"`
	// Roles lists them by tier. **The order is given by the contributor** (same rule as KindChains).
	Roles []OverviewRow `json:"roles"`
	// Actions are the buttons on this card. Today they are a group
	// of `use:<agent>:<profile>`: point this profile at some client (see
	// OverviewAgent.ID), with `global` being the "all clients use it" slot.
	//
	// # Why "one action per client" instead of one "use it" taking a parameter
	//
	// Actions **take no parameters** (see Action.Run): all the UI can provide is "the user clicked
	// this button", and everything else must be fixed by the implementer when the closure is built. So
	// "use for claude" and "use for codex" are necessarily **two actions**, each with its own closure.
	//
	// # Why the ID also carries the profile
	//
	// Because the same agent's same action **exists once on each of a dozen cards** (one "use it"
	// button per profile), and the ledger finds things by concept ID + action ID -- `RunConceptAction`
	// takes the **first match** (see Registry.RunConceptAction). With only `use:claude`, only the
	// card ordered first among those dozen actions could be clicked, and clicking the rest would land
	// on **someone else**: the user wants to switch to cheap, and actually changes balanced. With the
	// profile added it is unique within the whole concept, and the lookup lands on the right card.
	//
	// The UI's test for picking the button is the two machine markers in the ID (after `use:`,
	// agent first then profile), the same rule as assembling row IDs on chain cards: when both
	// sides assemble a machine marker, they must assemble the same string (see ChainRow.ID). So
	// this form is **contract**, not implementation detail; changing it means changing both sides.
	//
	// When the agent of the current section is already using this
	// profile, that action **does not appear** (pointing it at itself is
	// meaningless), so the UI just draws a statement, "currently using it".
	Actions []Action `json:"actions,omitempty"`
}

// OverviewRow is the chain of **one tier** within one
// profile, plus the health of that tier's chain head.
type OverviewRow struct {
	// ID is the machine marker of **this row** (used for action callbacks). Assembled exactly the same
	// as ChainRow.ID: `profile/tier`. A row ID is unique within a card and within the whole concept.
	ID string `json:"id,omitempty"`
	// Tier is the tier name (a machine value, not translated).
	Tier string `json:"tier"`
	// Label is the human-facing name (may be translated). Empty = show Tier directly.
	Label string `json:"label,omitempty"`
	// Head is this tier's first hop right now (`ark/deepseek-v3`); empty
	// = this tier has no usable candidates. Same meaning and reason as
	// ChainRow.Head (an empty chain is a judgment the UI should not make).
	Head string `json:"head,omitempty"`
	// Steps is the **whole** chain, chain head included, in actual attempt order.
	Steps []OverviewStep `json:"steps,omitempty"`
	// Note is the tail line "who else was skipped, and why". Empty = nothing was skipped.
	Note string `json:"note,omitempty"`
	// Tone is this row's tone (empty = no color). Same meaning as ChainRow.Tone: it
	// only expresses "is there a usable chain head", and **does not express health** --
	// health is on each hop's LatencyTone, and the two tests are completely different
	// (one is "is anyone in the config", the other is "is that one fast right now").
	Tone string `json:"tone,omitempty"`
}

// OverviewStep is one hop on the chain, plus **that hop's health right now**.
//
// That it is two shapes (rather than adding two fields to ChainStep) is deliberate: that one
// is a **config conclusion** ("who it would go to when resolved"), this one is **config + health**
// ("what it will be like when it runs"). Merging them would make the pure resolve path of `newgate
// tier` carry a "has probing recorded anything" parameter too -- a parameter it will never use.
type OverviewStep struct {
	// Provider / Model are this hop's binding (machine values, not translated).
	Provider string `json:"provider"`
	Model    string `json:"model"`
	// Profile is which profile this hop comes from (a chain crossing
	// profiles must state the source; same reason as ChainStep.Profile).
	Profile string `json:"profile,omitempty"`
	// LatencyMs is the time to first byte of the latest sample (milliseconds). 0 = **no
	// sample** (never probed, and no real traffic either); the UI draws nothing in that case --
	// drawing a `0ms` would say "it is so fast it has no latency", while the truth is "unknown".
	LatencyMs int64 `json:"latency_ms,omitempty"`
	// LatencyTone is the color that latency should get (ToneOK/ToneWarn/ToneBad, empty = no color).
	//
	// **The thresholds are not here**: fast/usable/slow is breaker's test
	// (`breaker/status.Grade`), implemented in one place only. This cell just carries
	// over the **result** of that decision, and the UI colors by it without comparing
	// numbers itself -- copying 3000/12000 into the frontend means the day the threshold
	// changes, two places need changing, and nothing goes red on the one that is missed.
	LatencyTone string `json:"latency_tone,omitempty"`
	// Probe is the conclusion of the last probe (`fluent` / `laggy` / …,
	// a machine marker, not translated). Empty = never probed. It and LatencyMs
	// are two things: latency is "how fast", the conclusion is "does it connect".
	Probe string `json:"probe,omitempty"`
	// Note is one sentence about this hop (today only Skip.Reason: demoted/skipped and the like).
	Note string `json:"note,omitempty"`
}

// Applier writes one edit of this concept to disk.
//
// The shape of `edit` is defined by **the concept itself** (each concept's applier interprets
// it), and the UI does not parse it, it just passes back the JSON the frontend produced,
// verbatim. `base` is the baseline obtained at load time (a content hash, see store.Revision):
// the implementation must compare before writing, and on mismatch return *Conflict.
//
// The new baseline it returns lets the frontend keep
// editing -- one save should not force a page reload.
type Applier func(edit json.RawMessage, base string) (newBase string, err error)

// Previewer is "my file's unflushed draft looks like this, so what should I display" -- see
// Concept.Preview. The returned shape must match Concept.Data (the frontend uses it as data).
type Previewer func(draft []byte) (any, error)

// Concept is what a module contributes to the UI.
type Concept struct {
	// ID is the **stable identity**: sorting, persistence, and "who is this edit for" all use it.
	//
	// The title is not the identity: the title is for humans, it gets
	// translated and reworded, and when the identity changes the frontend's
	// local state (expanded cards, drafts in progress) all stops matching.
	ID string
	// Kind decides how the frontend renders it (see the constants above).
	Kind string
	// Title is the human-facing name (may be translated, may change).
	Title string
	// Source is who contributed it (a module name). **Filled in by the ledger**;
	// the contributor does not report it -- reporting it wrong would take the blame
	// for someone else, and the ledger already knows who registered this entry.
	Source string
	// Data is the structured data for the frontend to render; the shape is fixed by Kind.
	Data any
	// Broken non-empty means this concept **cannot be read
	// right now** (the file was deleted, the JSON is corrupt).
	//
	// The UI still renders a card with this sentence on it. Why not just drop it from
	// the snapshot, or fail the whole snapshot: the former silently loses a feature
	// (the user thinks that tier does not exist), the latter makes one bad file whiten
	// the whole UI. Both are worse than "this thing is broken, and here is why".
	Broken string
	// Live means **this side's data changes by itself**, and the UI should re-ask every few seconds.
	//
	// Declared by the contributor, not inferred from Kind (before 2026-09-20 the latter
	// was the case: the UI treated series and log as synonyms for "cheap, can be asked often").
	// That inference had two flaws: it turned **cost**, something only the contributor knows, into an
	// appendage of the rendering shape; and it left no room for other Kinds -- a table showing "how
	// long until automatic shutdown" would sit on that number forever as long as the page stays open.
	//
	// The test for declaring it is **whether one read is expensive**, not whether the data
	// changes: counters, log tails, and the current value of a few switches are all a read
	// from memory and can be asked often; the config side has to reread and reparse every
	// profile and every source file, and cannot (see the comment on Registry.Snapshot).
	//
	// **The granularity is the source, not this concept**: the UI refreshes the
	// **sources** that declared Live, and asking a source asks all its contributors.
	// So concepts in the same source must be cheap together -- one cheap declaration
	// drags the rest of that source into a poll every few seconds. Today only gateway
	// and claudecode declare it, and each of their contributors is a read from memory.
	Live bool
	// Apply nil means this concept is **read-only** (say a file with credentials:
	// its content is masked, and writing it back would put *** on disk).
	Apply Applier
	// Locked non-empty = this card **makes no sense right now**; it gives the UI a
	// reason to gray the whole card out (that sentence is the reason, shown on the card).
	//
	// Its difference from read-only (Apply nil) is "why it cannot be touched": read-only says "this
	// contributor provides no way to write back", locked says "**the thing it manages does not
	// exist right now**" -- today the only use is "this client is not installed on this machine":
	// configuring slots or classifiers for claude takes effect not one byte on this machine,
	// and if the card stayed editable the user would only find out after making useless changes.
	//
	// Why the **contributor** says it: only it knows whether its tool is there
	// (the test is on its side, today via confighook.Agent.OnPath). The UI does not
	// understand "installed or not", and should not -- it just draws the reason it gets.
	//
	// Empty string = not locked. **Do not give reasons other than "not installed"**: this cell
	// means "the whole card makes no sense", not "some cell is filled wrong" (the latter is Broken).
	Locked string
	// Preview is "what should I display when my file's **unflushed draft** looks like this".
	//
	// A file often has two halves: the structured half (controls) and the raw half (`code`).
	// They describe the same file, and the user may act on either. After the raw text is edited,
	// the controls half still holds "the on-disk content from before the edit" -- it cannot
	// see what the user just wrote, and the next control change would **overwrite all of it**
	// (a control's edit payload is the whole file, not that cell). With Preview, the UI can ask
	// once using the raw text's draft, and the two halves then always describe the same content.
	//
	// The parameter is **all the bytes** of this file's draft (the UI gets them from the other
	// half of the same file), and the returned shape is exactly the same as Data (the one
	// in the snapshot). Optional: when it returns an error the UI keeps what it had -- **the
	// line being typed is unparseable by nature**, that is normal mid-typing, not a failure.
	//
	// Why this here instead of letting the UI parse it: that file's format
	// is **the contributor's knowledge** (how the KV splits, which keys are tiers,
	// which fields must not be touched), and a UI that knows it knows that module.
	// This is exactly where the BFF rule, "it knows no module", lands here.
	Preview Previewer
	// Order is **who comes first within the same section** (smaller first, ties by ID).
	//
	// Why the contributor says it rather than alphabetical by ID: alphabetical order
	// puts "the basics to configure right after install" (upstream, global settings) after
	// "the pile added over time" -- `config.profile.*` sorts before `config.providers`, so
	// the first screen of the left column is all tier files while the upstream config is off
	// the first screen. That is not a layout preference: you have to configure the upstream
	// before you can pick tier bindings, and order and operation sequence are the same thing.
	Order int
	// Group is **which heading it belongs under in the nav**, up to two levels, separated by "/".
	//
	// The first segment is the **major group** (the `tiers` column -- it separates
	// on the page "the pile added day by day" from "the two items you configure
	// right after install"), the second is the **family** (`claude-cheap` tucked
	// under `claude`). A single segment (`claude`) means family only, no major group.
	//
	// Empty string = not in any group (topmost, no indent). It only affects arrangement, not any
	// identity -- changing the group does not change anyone's ID, and routing and drafts go by ID.
	//
	// **Whether a heading is drawn is decided by the UI on "are there >=2 cards under
	// this segment"** (see rows in TabStrip): a family of one with a heading line is pure
	// noise. So contributors can safely put a group on every card without counting first.
	Group string
	// Actions are the actions **on this card** (see Section.Actions; the shape is identical).
	//
	// The only split from section actions is: **is this thing about one particular card**.
	//
	//	"create another tier file"      section action: what is created has no concept yet,
	//	                                and no card can hold it.
	//	"make this one the default"     card action: it is about **this one** (which profile),
	//	                                and only this card knows that.
	//
	// Run still **takes no parameters**, and this time not taking them is **right**:
	// all the UI can give is "the user clicked this button", while "which card was
	// clicked" was settled when the contributor built this Action (the closure). Having
	// the UI pass back an id would be dangerous -- its snapshot may already be stale,
	// and an action that goes by name would hit a tier that **no longer exists**.
	//
	// Same as section actions on another point: it **opens no transaction
	// and touches no snapshot**; after it runs, the UI rereads by itself.
	Actions []Action
	// Note is **one sentence of state**: what this card is right now (today
	// its only use is "this one is the current config"). It is not a button
	// -- clicking it does nothing, and the UI draws it as a static mark.
	//
	// # Why it exists (rather than "show nothing")
	//
	// The first version of this **showed nothing**: `profileActions` returned empty when it saw "it
	// is already the default", on the grounds that showing "make this one the default" on itself is
	// noise. The first half was right, the second wrong -- the two cards then looked **exactly the
	// same**: one is the current config, one is not. The user could not see which was in effect,
	// and that is exactly what they most want to know when opening this section (2026-09-21,
	// the user: "you should inject an info-only green: the effect of the current config").
	//
	// The landing point of "draw no noise" should be **not drawing that button**, not
	// drawing nothing: the button is gone, and its place is taken over by a statement.
	//
	// # Why it is not Locked / Broken
	//
	// Those two both say "something is wrong with this card" -- Broken is unreadable,
	// Locked is meaningless right now. This card is neither: it is readable and editable
	// as usual, it just **happens to be the one in effect right now**. That is a fact,
	// not a fault. So it is its own field rather than borrowing some existing outlet.
	Note *Note
}

// Note is one sentence of state with a tone (see Concept.Note).
//
// Tone takes one of the three Tone constants below; empty string =
// no color. This is **information**, so the UI should not draw it
// as a warning or error -- today the only use is colored ToneOK.
type Note struct {
	Text string `json:"text"`
	Tone string `json:"tone,omitempty"`
}

// Conflict is "the copy in your hand is no longer the latest".
//
// It carries **both texts**, because only by putting them side by side can the user decide what
// to do: his change and the other's may just have happened to touch the same block, or they may be
// worlds apart. "Save failed, please retry" leaves the user to guess, and a wrong guess costs data.
type Conflict struct {
	Concept string
	Path    string
	Base    string // the copy you loaded
	Current string // the copy now on disk
	Yours   string // the copy you are about to write
	Theirs  string // the text now on disk (same content as Current, shown to the user)
}

func (c *Conflict) Error() string {
	return fmt.Sprintf("view: %s changed on disk since it was loaded (%s)", c.Path, c.Concept)
}

// Contributor is "what this module contributes right now" itself.
//
// There is only one moment it is called: someone asks the UI for data. Every
// concept in the return value should look **as of now** (read the files
// that should be read now, compute the metrics that should be computed now).
//
// Returning an error is for "the whole group cannot be produced" (say the config
// directory cannot be read at all). A single concept's own trouble should not go
// here -- that would wipe out other people's surfaces too; use Concept.Broken.
type Contributor func() ([]Concept, error)

// Section is the presentation face of "this column": its name.
//
// Why the name is a function rather than a string computed at registration: **registration
// happens in each module's Start, and whether the language layer is installed by then
// is not guaranteed**. Business modules do not depend on modules/locale (the only thing
// depending on it is the distribution's modules/i18n, which appends its own catalogs
// into the kernel's already-installed one), so "who Starts first" is not ours to decide;
// a title evaluated in Start would freeze in the source language, and **nothing would go
// red for it** -- a column in the Chinese UI would just be called Configuration forever.
//
// Moving evaluation to snapshot time (Registry.Sections) puts it at the same
// moment and in the same language as concept titles: concept titles have always
// done it that way (see the `Title: i18n.T(...)` lines all inside produce functions
// in modules/config/view.go), and this simply brings column names to the same rule.
type Section struct {
	Title func() string
	// Group is which heading this column belongs under in the sidebar. **Empty = no group**, topmost.
	//
	// Why it is needed: columns are reported by the modules themselves (seven today), and the
	// sidebar could previously only lay them out alphabetically by source name -- so "breaker" came
	// first, "config" sat in the middle, and that order **meant nothing** (it was just `breaker`
	// < `config`). Grouping makes "what this column is for" visible: the section configured
	// once and then rarely touched is at the top, with the categories grouped by use below.
	//
	// As with Concept.Group, **the name comes from the contributor**: the kernel knows
	// no module name, and the UI knows even less (it just draws the heading it gets). It
	// also **only affects arrangement** and takes part in no identity -- when a column
	// changes group, URLs, drafts, and routing are unaffected (they go by Source).
	Group func() string
	// Actions are the **actions** on this column (there may be none, or several).
	//
	// Why actions hang on the column rather than on some concept: **what is newly created has no
	// concept yet** -- the one produced by "add another tier file" exists only in the next snapshot,
	// so no card can hold this button. It belongs to **this section**: the owner of that batch
	// of data knows "what else this section can grow", and the UI does not (it knows no module).
	//
	// The shape is the `mkdir` kind: click once, do one thing, then reread the snapshot.
	// So it **takes no parameters** -- all the UI can give is "the user clicked this button",
	// and everything else must be decided by the implementer looking at what is on disk (what
	// to name a new tier is exactly that: only the backend knows which names are taken).
	Actions []Action
	// Default is "land here when there is no location" -- the UI's first screen lands on this column.
	//
	// Why it is needed: the first screen used to be the accident of **whose name sorted first**
	// -- `Sections` sorted by Source alphabetically (stable across restarts is required, but
	// the alphabetical order itself means nothing), so the first column with cards became the
	// default landing, and that column was `breaker` (alphabetically first). The only way to
	// let users see the "everything on one screen" section first was to start the source with
	// something like `aaa-home` to jump the queue -- that is **mistaking a coincidence for
	// a mechanism**; it breaks the moment the alphabet changes (or a module with an earlier
	// letter appears), and whoever reads the code has no idea why that name was chosen.
	//
	// So it is split into two things: **the kernel says "who may be declared the
	// landing"** (this cell, plus the deterministic rule below), **who the landing
	// is is a product trade-off** (declared by the module itself). Adding a
	// module still needs no UI change -- that is how this rule has always looked.
	//
	// Same as Group / Actions: **it only affects how the UI lays things out**, and takes part in
	// no identity (Source is the identity: URLs, drafts, and routing go by it). So "this column
	// is no longer the landing" moves nothing. Fields: see SectionInfo.Fields (declare with Of).
	Fields  string
	Default bool
}

// Action is one action on a column.
type Action struct {
	// ID is the unique machine marker within this section.
	// The frontend passes it back, **not translated**.
	ID string
	// Label is the text on the button (may be translated, may be reworded). Same rule as
	// Title: a closure, evaluated at snapshot time -- registration happens in each module's
	// Start, and whether the language layer is installed by then is not guaranteed.
	Label func() string
	// Run does the work. On success it returns **which concept to switch to** (empty string
	// = stay put); on failure it returns a sentence for humans (shown verbatim in the UI).
	//
	// Why even "where to switch to" is handed back: **only the implementer knows** what the newly
	// created thing is called in the snapshot (it picked the name). Having the UI guess it again
	// would copy the knowledge "what a new tier is named" into two places, and the copy will drift.
	// Empty string is the norm (most actions produce no particular thing, say "reload config").
	//
	// It **opens no transaction and touches no snapshot**: after
	// writing, the UI rereads by itself (same as the save path).
	Run func() (focus string, err error)
	// Tone says this button **is pressed right now** (empty =
	// normal look). The values are the same set as Note.Tone.
	//
	// It exists because a class of switches **is its own state**: the fallback button
	// does not say "go turn it off", it says "it is off right now" -- without handing over
	// the state, the UI could only guess from the label's text (whether today's sentence
	// is "disable" or "enable"), which is parsing a human-facing, translated sentence.
	//
	// # It is a **closure**, not a value taken on the spot
	//
	// Same reason as Label (see "registration happens in each module's Start" above), but harder:
	// Label is copy, once taken it is fine; Tone is **state**, while the action is registered
	// once in Start and the snapshot is recomputed on every request -- a value taken on the spot
	// would keep the color frozen at the moment the process started. Hit for real (2026-09-30):
	// after `newgate fallback off` the button was still green, which looked like the UI lying.
	Tone func() string
}

// MarshalJSON lets actions go out together with the
// **data** (the rows in a table carry their actions).
//
// Why it is needed: Action has a **function field** (Run), and the default
// encoder refuses it outright (`json: unsupported type: func()`). And in this
// thing, "a concept's data" is serialized as is (what the BFF gets is `any`;
// it knows no Kind, so it has no way to pick out what a table row should send).
//
// What goes out is the two fields the UI needs; Label is evaluated **at serialization time**
// -- which is snapshot time, the same moment and the same language as the titles of SectionInfo /
// Concept. Run does not go out; it waits in the process to be called (see Registry.RunRowAction).
func (a Action) MarshalJSON() ([]byte, error) {
	label := ""
	if a.Label != nil {
		label = a.Label()
	}
	tone := ""
	if a.Tone != nil {
		tone = a.Tone()
	}
	return json.Marshal(ActionInfo{ID: a.ID, Label: label, Tone: tone})
}

// Does attaches an action to this column (chainable).
//
//	v.Register("config", view.Title(...).Does(view.Action{ID: "new-profile", …}), concepts)
func (s Section) Does(a Action) Section {
	s.Actions = append(s.Actions, a)
	return s
}

// In gives this column its group in the sidebar (optional; omitting it means no group).
//
//	v.Register("gateway", view.Title(...).In(func() string { return i18n.T("Data plane", nil) }), concepts)
//
// Why a method chained after Title rather than one more Register parameter: there are seven call
// sites, and **the vast majority of columns need no group** -- one more required parameter would
// make all seven write an empty placeholder, and the reader would then have to judge "was this
// empty not thought through, or really none". Chaining keeps "no group" as one untouched line.
func (s Section) In(group func() string) Section {
	s.Group = group
	return s
}

// Title builds a column name. What is passed in is a **closure**, not a translated string:
//
//	v.Register("config", view.Title(func() string { return i18n.T("Configuration", nil) }), concepts)
//
// This shape has two benefits, neither pedantry:
//
//   - The error "translate the title at registration time" is unwritable at the type
//     level (passing a string does not compile), and that error is silent -- an English
//     title just looks like "not translated yet" in a Chinese UI, not like a bug;
//   - The i18n scanner still sees the msgid. It recognizes `i18n.T(...)`
//     calls in the source, and calls inside a closure body are in the same AST, so **the
//     scanner need not be taught about view.Title**. Conversely, taking the msgid as a string
//     (`view.Title("Configuration")`) would make that English line invisible to everyone: not
//     in the ledger, `check` all green, and the Chinese catalog missing one entry forever.
func Title(f func() string) Section { return Section{Title: f} }

// Landing declares this column the UI's landing (land on it when there is no location).
//
//	v.Register("home", view.Title(...).Landing(), concepts)
//
// Why a method chained after Title (same shape as In / Does): there are seven call
// sites, and **only one** needs it -- one more required parameter would make all seven
// write a zero value, and the reader would have to judge "was this false not thought
// through, or really not the landing". Chaining leaves the other six untouched.
//
// For the rule when several declare it, see Sections: **take the smallest by
// Source**. Without that rule, who gets picked depends on map iteration order,
// and that "a restart changes the first screen" error would make nothing go red.
func (s Section) Landing() Section {
	s.Default = true
	return s
}

// Of declares **what this column is for** (see SectionInfo.Fields and the Field* constants).
//
//	v.Register("home", view.Title(...).Landing().Of(view.FieldRoutes), concepts)
//
// Same shape as Landing / In / Does, and the same reason: there are seven or eight
// call sites, only some need to say this, and chaining leaves the rest as they are.
func (s Section) Of(field string) Section {
	s.Fields = field
	return s
}

// SectionInfo is one row in the column list.
//
// Source is the machine marker (the frontend matches it with concepts,
// and also puts it in the URL); Title is for humans. Same as concepts:
// the title may be translated and reworded, the identity may not.
type SectionInfo struct {
	Source string `json:"source"`
	Title  string `json:"title"`
	// Group is which heading this column belongs under (empty = none, topmost). See Section.Group.
	Group string `json:"group,omitempty"`
	// Actions are the actions on this column (see Section.Actions). Run
	// is a **function** and cannot go out, so only "which buttons there
	// are" is given here: ID for callbacks, Label for the button text.
	Actions []ActionInfo `json:"actions,omitempty"`
	// Default is "land here when there is no location" (see Section.Default).
	//
	// Why it is sent to the UI is the same as Group: **the landing is something the
	// UI uses**, and the UI knows no module (it only knows the machine marker Source).
	// Having the frontend take the first alphabetically would copy this product
	// trade-off into the frontend -- and nobody can change the frontend's copy.
	//
	// `omitempty` is deliberate: the vast majority of columns are not
	// the landing, and those few JSON lines need not carry a false.
	Default bool `json:"default,omitempty"`
	// Fields says what this column "is for" -- the UI groups by it in the catalog.
	//
	// # Why it is needed
	//
	// The sidebar used to gather columns by **the contributor's grouping** (claude / codex /
	// opencode under "Clients"). That is something the contributor knows, but it cannot answer
	// "**which category should this column go in**": when a module registers a column it means
	// to say "this is config" or "this is a client's settings", and all it has is free text
	// like `In("Clients")` -- two distributions writing `Clients` and the Chinese word for it
	// would never end up together, and the UI has nothing machine-readable to rely on either.
	//
	// Values: `view.FieldRoutes` (which way requests go), `FieldSettings` (things that can be
	// changed), `FieldClients` (client settings). Empty = not categorized yet, the UI puts it last.
	Fields string `json:"fields,omitempty"`
}

// ActionInfo is the face a section action shows the UI (see Section.Actions).
type ActionInfo struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	// Tone is "this button is pressed right now" (see Action.Tone). Empty = normal look,
	// so it could be omitted here; it is not omitted because it is **state**, and state being
	// absent from the payload even once is enough to write a screenful of guessing code (same
	// as the ready entry in OverviewAgent) -- what is omitted is the vast majority of actions.
	Tone string `json:"tone,omitempty"`
}

// Service is the face the contributor sees: register a produce function.
//
// The query half is not given out -- it belongs to the UI itself (whoever
// renders reads the ledger). Same as porthub's Service only offering
// Mount: the smaller the exposed area, the less coupling can grow.
type Service interface {
	// Register registers source (the module name, used for errors and to name people
	// in grouping) and its column name, plus the produce function. Returns a release.
	Register(source string, section Section, read Contributor) (modules.Release, error)
}

// Capability is "this UI is in this process" itself.
//
// The provider is the **UI module** (the distribution's modules/web-dashboard); the
// contributors are the business modules (config reports tiers and files, gateway
// reports counters, …), which depend on it optionally: register if the UI is there,
// skip if not -- module features lose nothing, there is just no web entry point.
var Capability = modules.NewCapability[Service]("view")

// ---------- Ledger ----------

// Registry is the contributors' ledger. The zero value is unusable; use NewRegistry.
//
// It lives in the leaf package (rather than inside the UI module) for one reason only: **it
// can be unit tested**. The ledger's behavior (duplicates, release, concurrency) is unrelated
// to rendering, and testing the rendering half would drag in HTTP and embedded assets.
type Registry struct {
	mu      sync.RWMutex
	next    int
	sources []source
}

type source struct {
	id     int
	name   string
	title  func() string // Register rejects nil, so this is never empty
	group  func() string // may be nil: unset means no grouping
	def    bool          // see Section.Default: where the UI lands it
	fields string        // see Section.Fields: what this column is for
	acts   []Action
	read   Contributor
}

func NewRegistry() *Registry {
	return &Registry{}
}

// Register takes a contributor and returns a release.
//
// This step **does not call the produce function**: during assembly
// the ledger only knows "who will report", not "what will be reported".
func (r *Registry) Register(name string, section Section, read Contributor) (modules.Release, error) {
	if name == "" {
		return nil, i18n.E("view: a contributor registered with no source name", nil)
	}
	if section.Title == nil {
		// Error out rather than fall back to some name: a missing column name means **a
		// column with no title in the UI**, and that moment is one HTTP request and one
		// render away from here, so nobody will trace it back. Going red here is cheapest.
		return nil, i18n.E("view: {source} registered with no section title — the sidebar would have "+
			"nothing to label its column with (use view.Title)", i18n.A{"source": name})
	}
	if read == nil {
		return nil, i18n.E("view: {source} registered a nil contributor — the interface would show nothing for it",
			i18n.A{"source": name})
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	s := source{id: r.next, name: name, title: section.Title, group: section.Group,
		def: section.Default, fields: section.Fields, acts: section.Actions, read: read}
	r.next++
	r.sources = append(r.sources, s)
	return func() error {
		r.mu.Lock()
		defer r.mu.Unlock()
		// A release removes only its own entry (identified by registration sequence
		// number, not by name): the same module name may be registered twice, and "a
		// later one with the same name" should not be removed by the former's release.
		for i, cur := range r.sources {
			if cur.id == s.id {
				r.sources = append(r.sources[:i], r.sources[i+1:]...)
				break
			}
		}
		return nil
	}, nil
}

// Sections lists **every registered column** (not just
// those with concepts right now), sorted by Source.
//
// Why not reuse "take sources from concepts": a source may produce not one concept right now
// -- the config directory is entirely unreadable, omo has never been taken over, not one plugin
// switch point has reported. Those columns should still have a place in the sidebar: if it
// disappears from the list, the user will think that module is missing and go looking elsewhere.
//
// It **calls no produce function** (it only takes the registered column names), so asking any time
// is cheap and the UI can carry it along in every snapshot. Column names are evaluated here -- in
// the language of the snapshot moment, not the registration moment (see the comment on Section).
//
// The order is stable across restarts (by Source, a machine marker, not varying
// with language): a UI order that keeps changing makes the user hunt again every
// time. **The landing is not expressed by this order** -- see Section.Default.
func (r *Registry) Sections() []SectionInfo {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]SectionInfo, 0, len(r.sources))
	for _, s := range r.sources {
		title := ""
		if s.title != nil {
			title = s.title()
		}
		if strings.TrimSpace(title) == "" {
			// Fall back rather than error: a column name is a piece of display copy, and failing the whole
			// snapshot for it costs a blank UI -- far worse than one column's title showing the source name.
			title = s.name
		}
		// The group name is evaluated at the same moment as the column name (the
		// language of the snapshot moment): it is also human-facing text, and evaluating
		// it in Start would freeze it in the source language (see the comment on Title).
		group := ""
		if s.group != nil {
			group = s.group()
		}
		var acts []ActionInfo
		for _, a := range s.acts {
			label := ""
			if a.Label != nil {
				label = a.Label()
			}
			tone := ""
			if a.Tone != nil {
				tone = a.Tone()
			}
			acts = append(acts, ActionInfo{ID: a.ID, Label: label, Tone: tone})
		}
		out = append(out, SectionInfo{Source: s.name, Title: title, Group: group, Actions: acts, Default: s.def, Fields: s.fields})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Source < out[j].Source })
	// The landing is **at most one**. When several declare it, take the smallest by
	// Source: without this rule, who gets picked depends on registration order (i.e. the
	// order the modules Start), and that "change how it is installed and the first screen
	// moves" error would make nothing go red. Taking the smallest uses the same ruler
	// as the sort above, so "who comes first" and "who is the landing" look consistent.
	if n := countDefaults(out); n > 1 {
		for i := range out {
			if out[i].Default && !isFirstDefault(out, i) {
				out[i].Default = false
			}
		}
	}
	return out
}

func countDefaults(s []SectionInfo) int {
	n := 0
	for _, x := range s {
		if x.Default {
			n++
		}
	}
	return n
}

// isFirstDefault says whether out[i] is the smallest Default (after sorting = the first one).
func isFirstDefault(out []SectionInfo, i int) bool {
	for j := 0; j < i; j++ {
		if out[j].Default {
			return false
		}
	}
	return true
}

// RunAction runs a section action (see Section.Actions).
//
// When the column is not found, or that column has no such action,
// it returns ErrUnknownAction -- the snapshot in the UI's hands may be stale
// (the module was turned off), and saying so plainly beats pretending it ran.
//
// **It opens no transaction and touches no snapshot**: after it runs, the caller (the
// BFF) tells the UI to reread. Actions and "save" take two different paths, because
// their shapes were different from the start: save is "write this change of this
// concept to disk", an action is "do one thing, and only that one knows what exactly".
func (r *Registry) RunAction(source, id string) (string, error) {
	r.mu.RLock()
	var found *Action
	for i := range r.sources {
		if r.sources[i].name != source {
			continue
		}
		for j := range r.sources[i].acts {
			if r.sources[i].acts[j].ID == id {
				found = &r.sources[i].acts[j]
				break
			}
		}
		if found != nil {
			break
		}
	}
	r.mu.RUnlock()
	if found == nil {
		return "", i18n.E("{source} has no action called {action} — the page is probably stale, reload it",
			i18n.A{"source": source, "action": id})
	}
	if found.Run == nil {
		return "", i18n.E("{source}.{action} has nothing to run", i18n.A{"source": source, "action": id})
	}
	return found.Run()
}

// RunConceptAction runs one action on some concept (see Concept.Actions).
//
// It differs from RunAction in one place: **it has to ask the contributor first**. Section
// actions lie in the ledger from registration, while concept actions are built fresh by the
// produce function each time (see the package comment of lib/view: concepts are computed
// "when someone asks") -- so the ledger itself does not know "which card this ID is".
//
// The cost is that this click runs the produce function (the config one rereads all
// tiers). **Acceptable**: it is one user click, not the hot path; and what it buys
// is a property of this thing -- the target of an action is **the card the user
// sees on screen right now**, not some possibly stale registration in the ledger.
//
// When not found, error out, do not pretend it ran: the snapshot in
// the UI's hands may already be stale (that tier was just deleted), and
// an action that goes by name would hit something that does not exist.
func (r *Registry) RunConceptAction(conceptID, actionID string) (string, error) {
	r.mu.RLock()
	reads := make([]Contributor, 0, len(r.sources))
	for _, s := range r.sources {
		reads = append(reads, s.read)
	}
	r.mu.RUnlock()

	for _, read := range reads {
		cs, err := read()
		if err != nil {
			continue // this contributor yielded nothing: it could never own that card
		}
		for _, c := range cs {
			if c.ID != conceptID {
				continue
			}
			acts, err := conceptActions(c)
			if err != nil {
				return "", err
			}
			for _, a := range acts {
				if a.ID != actionID {
					continue
				}
				if a.Run == nil {
					return "", i18n.E("{concept}.{action} has nothing to run",
						i18n.A{"concept": conceptID, "action": actionID})
				}
				return a.Run()
			}
			return "", i18n.E("{concept} has no action called {action} — the page is probably stale, reload it",
				i18n.A{"concept": conceptID, "action": actionID})
		}
	}
	return "", i18n.E("no view contributed a concept called {concept} — the page is probably stale, reload it",
		i18n.A{"concept": conceptID})
}

// conceptActions is "the buttons that can be clicked on this concept".
//
// For the vast majority of Kinds it is Concept.Actions itself (the one the contributor built
// when producing that card). There is one exception: **Kinds whose data is itself a stack
// of sub-objects** -- those buttons live on the sub-objects (one "use it" per profile),
// because they are about "this one", and the concept-level column cannot hold that identity.
//
// The test is the same as rowActions: dispatch by Kind, do not probe with "can this be
// converted to some type" -- the correspondence between shape and Kind is contract, and
// the probing style silently picks wrong when two shapes happen to convert successfully.
//
// Why these buttons still take the **concept action** path (rather than a row action): what
// they change is **outside this screen** (the client's chain head in state.json), the card
// itself changes not one character, and rereading after the run is enough -- that is exactly
// the semantics of a concept action (see the split between Concept.Actions and RunRowAction).
func conceptActions(c Concept) ([]Action, error) {
	if c.Kind != KindOverview {
		return c.Actions, nil
	}
	ov, ok := c.Data.(Overview)
	if !ok {
		return nil, i18n.E("{concept} is an overview but its data is not an Overview", i18n.A{"concept": c.ID})
	}
	out := make([]Action, 0, len(c.Actions)+len(ov.Cards)+1)
	out = append(out, c.Actions...)
	// The actions on the "automatic" card **must also be in this pile**: its members
	// (`use:global:<profile>`) and "follow the global" (`auto:<agent>`) are only on
	// it, and missing them means every button on that card does nothing when clicked
	// -- and `RunConceptAction` reports "no such action" when it cannot find one,
	// which in the UI looks like "clicking pops up a sentence nobody understands".
	if ov.Auto != nil {
		out = append(out, ov.Auto.Actions...)
	}
	for _, card := range ov.Cards {
		out = append(out, card.Actions...)
	}
	return out, nil
}

// RunRowAction runs one action on **a row** (see Row.Actions and ChainRow.Actions).
//
// Same set as RunConceptAction: first ask the contributor to rebuild that card (rows are
// **grown from data**; the ledger does not have them), then find that action in that row.
//
// Two-step location -- concept ID then row ID -- is not verbosity: a row is
// an independent binding (in a table), or one tier of one profile (in a chain
// card). Without the row layer, "switch to X" would not know who to change.
//
// # Why it no longer hardcodes KindTable (chains added 2026-09-22)
//
// The first version only knew Table, so "an action on a row" was tied to a **shape**:
// chain card rows and table rows say the same thing ("where this row goes"), yet because
// they render differently, there was nowhere to hang it. After dispatching by Kind, adding
// a Kind with rows needs no change to this function -- it just adds one case, and a missed
// one reports a sensible sentence **at the moment of the call** (the default below).
func (r *Registry) RunRowAction(conceptID, rowID, actionID string) (string, error) {
	r.mu.RLock()
	reads := make([]Contributor, 0, len(r.sources))
	for _, s := range r.sources {
		reads = append(reads, s.read)
	}
	r.mu.RUnlock()

	for _, read := range reads {
		cs, err := read()
		if err != nil {
			continue
		}
		for _, c := range cs {
			if c.ID != conceptID {
				continue
			}
			acts, err := rowActions(c, rowID)
			if err != nil {
				return "", err
			}
			for _, a := range acts {
				if a.ID != actionID {
					continue
				}
				if a.Run == nil {
					return "", i18n.E("{row}.{action} has nothing to run",
						i18n.A{"row": rowID, "action": actionID})
				}
				return a.Run()
			}
			return "", i18n.E("row {row} of {concept} has no action called {action} — "+
				"the page is probably stale, reload it",
				i18n.A{"row": rowID, "concept": conceptID, "action": actionID})
		}
	}
	return "", i18n.E("no view contributed a concept called {concept} — the page is probably stale, reload it",
		i18n.A{"concept": conceptID})
}

// rowActions pulls out the actions hanging on that row from a card, by row ID.
//
// Dispatch by Kind, not by probing "can this be converted to some type": the correspondence
// between shape and Kind is **contract** (the frontend also picks its renderer by Kind),
// and the probing style silently picks wrong when two shapes happen to convert successfully.
func rowActions(c Concept, rowID string) ([]Action, error) {
	switch c.Kind {
	case KindTable:
		tbl, ok := c.Data.(Table)
		if !ok {
			return nil, i18n.E("{concept} is a table but its data is not a Table", i18n.A{"concept": c.ID})
		}
		for _, row := range tbl.Rows {
			if row.ID == rowID {
				return row.Actions, nil
			}
		}
		return nil, i18n.E("{concept} has no row called {row} — the page is probably stale, reload it",
			i18n.A{"concept": c.ID, "row": rowID})
	case KindChains:
		ch, ok := c.Data.(Chains)
		if !ok {
			return nil, i18n.E("{concept} is a chain card but its data is not a Chains", i18n.A{"concept": c.ID})
		}
		// A row ID is **unique across the whole concept** within a chain card (`demo/heavy`): a row
		// is "one tier of one profile", and a tier name alone appears ten times across ten profiles
		// (see ChainRow.ID). So here we scan the whole card and need not pick the profile first.
		for _, card := range ch.Cards {
			for _, role := range card.Roles {
				if role.ID == rowID {
					return role.Actions, nil
				}
			}
		}
		return nil, i18n.E("{concept} has no row called {row} — the page is probably stale, reload it",
			i18n.A{"concept": c.ID, "row": rowID})
	// KindOverview **deliberately has no branch**: rows on the overview card carry no actions.
	// Probing on this screen is card-level and section-level (see OverviewCard.Actions); a
	// row level would need another layer of identity (which card's which tier), while what
	// really needs probing on a chain is the chain head -- the card-level button already
	// covers it. Falling through to the sentence below, which truthfully says "rows of this
	// Kind carry no actions", beats an empty branch that quietly returns an empty list.
	default:
		return nil, i18n.E("{concept} is a {kind}, whose rows carry no actions "+
			"(row actions live on tables and chain cards)",
			i18n.A{"concept": c.ID, "kind": c.Kind})
	}
}

// Snapshot asks the contributors and returns this moment's concepts (sorted by Source, ID).
//
// No names = ask all. With names = **ask only those** -- this is not performance
// pedantry, it is part of the refresh semantics: what the UI refreshes every few
// seconds is counters and logs (those are cheap to produce), while the config one has to
// reread and reparse every profile and every source file. Making "refresh the counters"
// incidentally reread the whole config directory spends the cost where nobody is looking.
//
// An unknown name is **not an error** (that one is not installed, or was just turned
// off): refreshing is background behavior, and failing the whole refresh for a
// contributor that no longer exists would leave the UI stuck on the previous frame.
//
// The sort is for **diagnostic output**: two runs of the same assembly must show cards in
// the same order, or the user will think something changed. Page order is decided by the
// frontend, and today it **groups by Source** (one small heading per source, by ID within
// the group) -- Kind only decides how one card is drawn, not where it sits. (Corrected
// 2026-09-20: this used to say "grouped by Kind"; the frontend never did that.)
func (r *Registry) Snapshot(only ...string) ([]Concept, error) {
	r.mu.RLock()
	sources := append([]source(nil), r.sources...)
	r.mu.RUnlock()
	if len(only) > 0 {
		want := make(map[string]bool, len(only))
		for _, name := range only {
			want[name] = true
		}
		kept := sources[:0:0]
		for _, s := range sources {
			if want[s.name] {
				kept = append(kept, s)
			}
		}
		sources = kept
	}

	var out []Concept
	owner := map[string]string{}
	for _, s := range sources {
		concepts, err := s.read()
		if err != nil {
			return nil, i18n.Ef(err, "interface: {source} could not produce its view: {err}",
				i18n.A{"source": s.name, "err": err})
		}
		for _, c := range concepts {
			c.Source = s.name
			// A duplicate errors out on the spot, no "the later one wins": when two
			// modules each think they are reporting the same ID, the UI shows only one of
			// them, and the other's edit point can never be clicked -- that is silently
			// losing a feature, the same class of pit as a prefix conflict (porthub).
			//
			// This used to happen at registration time; now it can only happen here -- because
			// "which concepts there are" is itself decided by the produce function. The cost is
			// that it moved from assembly time to the first time someone looks at the UI; what
			// it buys is that assembly reads not one byte from disk (see the package comment).
			if prev, dup := owner[c.ID]; dup {
				return nil, i18n.E("view: concept {id} is contributed by both {a} and {b} — "+
					"two concepts on one id means only one of them is ever reachable from the interface",
					i18n.A{"id": c.ID, "a": prev, "b": s.name})
			}
			if c.ID == "" {
				return nil, i18n.E("view: {source} contributed a concept with no id", i18n.A{"source": s.name})
			}
			if c.Kind == "" {
				return nil, i18n.E("view: concept {id} ({source}) has no kind — "+
					"the interface would not know how to render it",
					i18n.A{"id": c.ID, "source": s.name})
			}
			owner[c.ID] = s.name
			out = append(out, c)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Source != out[j].Source {
			return out[i].Source < out[j].Source
		}
		// Order first, ID as fallback: contributors with no Order declared (most today) behave exactly
		// as before (all 0, so sorted by ID), and those who declare it can put themselves in front.
		if out[i].Order != out[j].Order {
			return out[i].Order < out[j].Order
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}

// ErrUnknownConcept means "nobody reports this ID right now".
//
// The UI uses it to tell 404 (the frontend's snapshot is
// stale, just reload) from 500 (something is really broken).
var ErrUnknownConcept = errors.New("view: no such concept")

// Get fetches one concept (the UI finds "who this edit is for" by ID).
//
// It goes through Snapshot: the produce function **runs again**. That is deliberate --
// the Apply closure is built fresh by the produce function (it has to know the file's current
// path and baseline), and caching an old one would mean writing to disk with stale knowledge.
func (r *Registry) Get(id string) (Concept, error) {
	all, err := r.Snapshot()
	if err != nil {
		return Concept{}, err
	}
	for _, c := range all {
		if c.ID == id {
			return c, nil
		}
	}
	return Concept{}, fmt.Errorf("%w: %s", ErrUnknownConcept, id)
}
