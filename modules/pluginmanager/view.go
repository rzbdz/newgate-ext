package pluginmanager

import (
	"encoding/json"
	"errors"
	"sort"
	"time"

	"github.com/rzbdz/newgate-ext/lib/view"
	"github.com/rzbdz/newgate-ext/modules/config/domain"
	"github.com/rzbdz/newgate-ext/modules/config/paths"
	"github.com/rzbdz/newgate-ext/modules/config/store"
	i18n "github.com/rzbdz/newgate/lib/i18n"
)

// What this module contributes to the web UI: **runtime switch points**.
//
// This is the switch ledger's most natural second entry - `newgate plugin` is the CLI
// copy, this browser one draws the same points as switches. Two entries, **one write
// semantics**: both go through SetSwitch, so the table choice, "action = factory state
// undoes it" and "no forever for footguns" cannot differ across the two UIs (see set.go).
//
// Why two concepts, not one mixed card: writability belongs to the **whole concept**
// (the front end decides save from it), and a footgun must never be flipped here at a
// whim - it needs a time limit, and a switch that fails when pressed is worse than none.
// So footguns get their own read-only card: why it is read-only, and which command to use.

type switchItem struct {
	ID     string `json:"id"`     // 开关点路径（StateKey 里的键）
	Label  string `json:"label"`  // 人话标题
	Kind   string `json:"kind"`   // 界面渲染用：全是 bool
	Value  bool   `json:"value"`  // **当前生效**的状态（出厂态 + 用户的设定）
	Why    string `json:"why"`    // 关掉会发生什么
	Group  string `json:"group"`  // 模块名（界面拿它分组）
	Danger string `json:"danger"` // safe / quirk / footgun
	Until  string `json:"until,omitempty"`
}

type switchesData struct {
	File  string       `json:"file"`
	Base  string       `json:"base"`
	Items []switchItem `json:"items"`
}

// viewConcepts produces the two switch cards as of this moment.
//
// Each call recomputes "is it on now" - a switch changed on the command line is right
// after one refresh (see lib/view's package note: production happens when someone asks).
func viewConcepts(m Manager) ([]view.Concept, error) {
	st := store.LoadState()
	file := paths.StateFile()
	base := store.Revision(file)

	var writable, footguns []switchItem
	for _, mod := range m.Modules() {
		for _, sw := range mod.Switches {
			it := switchItem{
				ID: sw.Path, Label: sw.Title, Kind: "bool",
				Value: switchOn(st, sw), Why: sw.Why,
				Group: mod.Name, Danger: string(sw.Danger),
			}
			if until := Remaining(st, sw.Path); !until.IsZero() {
				it.Until = until.Format(time.RFC3339)
			}
			if sw.Danger == DangerFootgun {
				footguns = append(footguns, it)
				continue
			}
			writable = append(writable, it)
		}
	}
	// The module list goes in **first**, and is not touched by the early return below
	// ("report nothing when there is no switch point"): it answers another question
	// (which modules make up this build), and the skeleton distribution has none at
	// all - yet needs it most ("is it installed" is the first post-upgrade check, docs/08).
	out := []view.Concept{{
		ID: "plugin-manager.modules", Kind: view.KindTable,
		Title: i18n.T("Modules in this build", nil),
		Data:  moduleTable(m),
	}}

	if len(writable) == 0 && len(footguns) == 0 {
		// Not one switch point reported (the skeleton distribution, say): the two switch
		// cards are omitted rather than emitted empty - an empty card looks like "it did not load".
		return out, nil
	}

	if len(writable) > 0 {
		out = append(out, view.Concept{
			ID: "plugin-manager.switches", Kind: view.KindToggles,
			Title: i18n.T("Runtime switches", nil),
			// Live: these switches **are also flipped from the terminal** (`newgate plugin ... on/off`,
			// and the auto-restore when the TTL runs out), and reading them is just a read of
			// state.json. Frozen at page load, the UI would state a current value already wrong.
			Live: true,
			Data: switchesData{File: file, Base: base, Items: writable},
			Apply: func(edit json.RawMessage, base string) (string, error) {
				return applySwitches(m, edit, base)
			},
		})
	}
	if len(footguns) > 0 {
		// Read-only (no Apply). Why not a "with time limit" switch: someone has to pick the
		// limit and this version has no UI for it; a switch that always fails is worse. Command in why.
		for i := range footguns {
			footguns[i].Why = i18n.T("{why} — footguns need a time limit; use: newgate plugin {path} on 5m",
				i18n.A{"why": footguns[i].Why, "path": footguns[i].ID})
		}
		out = append(out, view.Concept{
			ID: "plugin-manager.footguns", Kind: view.KindToggles,
			Title: i18n.T("Switches that need a time limit", nil),
			// Live like .switches: these values are changed from the terminal too (and they
			// carry a limit, so they **revert by themselves** even more).
			Live: true,
			Data: switchesData{File: file, Base: base, Items: footguns},
		})
	}
	return out, nil
}

// moduleTable is **which modules make up this build**.
//
// Same order as `newgate plugin`, word for word: DisplayOrder for classification
// (meaningful: foundation, data plane, UI, clients, models, bridges), name in group
// (start order says nothing; sorting by it forces a re-hunt), same order on both UIs -
// one question, two entries: arch-diagram under "others" must match the terminal.
//
// "What is installed" is the first thing to confirm when troubleshooting (wrong module
// count after upgrade = wrong artifact, docs/08-operations.md): **all** modules, switched or not.
func moduleTable(m Manager) view.Table {
	t := view.Table{
		Columns: []view.Column{
			{ID: "module", Label: i18n.T("module", nil)},
			{ID: "type", Label: i18n.T("type", nil)},
			// Description: the sentence the module writes (see component.Component.Desc). Placed
			// here rather than last: it is the column read by humans, while "switch points" is a
			// count - know what this is first, care about how many switches second.
			{ID: "desc", Label: i18n.T("What it does", nil)},
			{ID: "switches", Label: i18n.T("Switch points", nil), Align: "right"},
		},
		Rows: []view.Row{},
	}
	all := m.Modules()
	for _, typ := range DisplayOrder() {
		var group []Module
		for _, mod := range all {
			if groupOf(mod.Type) == typ {
				group = append(group, mod)
			}
		}
		sort.Slice(group, func(i, j int) bool { return group[i].Name < group[j].Name })
		for _, mod := range group {
			t.Rows = append(t.Rows, view.Row{Cells: map[string]view.Cell{
				"module": {Text: mod.Name},
				// The classification word is a machine tag (infra / gateway / ...), shown verbatim:
				// it is the group name in `newgate plugin` and the Type the module declares, so
				// translating it would make two UIs use two words for one thing.
				"type": {Text: string(groupOf(mod.Type))},
				// No description = **left empty**, not padded with "(no description)": the cell's
				// information is precisely "this module did not say what it does", and empty is that
				// (the reverse of dashIfEmpty - there "there is none", here "the author did not write").
				"desc":     {Text: mod.Desc},
				"switches": switchCountCell(len(mod.Switches)),
			}})
		}
	}
	return t
}

func switchCountCell(n int) view.Cell {
	if n == 0 {
		// "None at all" and "0" are not the same: the first means "no runtime switches", the
		// second is not a possible state (registering means at least one). Use a dash, not 0.
		return view.Cell{Text: "-"}
	}
	return view.Cell{Text: i18n.T("{n}", i18n.A{"n": n})}
}

// switchOn is the **currently effective** state: factory-on (kill switch) reads the Off
// table, factory-off (mode) reads the On table. The criterion is word for word the CLI's
// switchState - if the two disagree, the user thinks one UI is lying to him.
func switchOn(st *domain.State, sw Switch) bool {
	if st == nil {
		return sw.Default
	}
	if sw.Default {
		return !Off(st, sw.Path)
	}
	return On(st, sw.Path)
}

// applySwitches writes the whole table the UI hands back into state.json.
//
// Shape of edit: `{"<switch path>": true|false}` - the UI returns **every** item it has
// (see the front end's Toggles note), so this applies them one by one, i.e. "set each
// switch to look like this". Unchanged rows are harmless idempotent writes (see SetSwitch).
func applySwitches(m Manager, edit json.RawMessage, base string) (string, error) {
	var patch map[string]bool
	if err := json.Unmarshal(edit, &patch); err != nil {
		return "", i18n.Ef(err, "the switches in this request are not readable: {err}", i18n.A{"err": err})
	}
	// Sorted only so errors are reproducible: the same bad request twice must report the same one.
	paths_ := make([]string, 0, len(patch))
	for p := range patch {
		paths_ = append(paths_, p)
	}
	sort.Strings(paths_)

	st := store.LoadState()
	for _, p := range paths_ {
		sw, ok := m.Lookup(p)
		if !ok {
			// The UI's switch point is gone (module replaced, different artifact). Saying so
			// beats a silent skip: the day it skips silently, the user thinks his flip took effect.
			return "", i18n.E("no switch point named \"{path}\" — the page is probably stale, reload it",
				i18n.A{"path": p})
		}
		if err := SetSwitch(st, sw, patch[p], 0, false); err != nil {
			return "", err
		}
	}

	b, err := store.StateBytes(st)
	if err != nil {
		return "", err
	}
	return writeThrough("plugin-manager.switches", paths.StateFile(), base, b)
}

// writeThrough translates the store's "baseline is off" into the UI contract's Conflict.
//
// # Why these six lines are copied three times (config / locale / here)
//
// This is **deliberate**, reviewed 2026-09-21: the CAS semantics (what is compared, how
// it locks, how atomic the write is) exist once in store; this only reshapes its failure.
// One copy would mean store knowing the view contract (or a package for it), both worse
// than three six-line mappings - UI knowledge in the data layer, or indirection.
//
// The **criterion** for copying: when `Conflict` gains a field, all three follow. Missing
// one shows as a column absent from the conflict dialog (the front end draws blank), no red.
func writeThrough(conceptID, file, base string, data []byte) (string, error) {
	rev, err := store.WriteIfUnchanged(file, base, data)
	var stale *store.StaleError
	if errors.As(err, &stale) {
		return "", &view.Conflict{
			Concept: conceptID,
			Path:    file,
			Base:    stale.Base,
			Current: stale.Current,
			Yours:   string(data),
			Theirs:  string(stale.Disk),
		}
	}
	return rev, err
}
