package pluginmanager

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/rzbdz/newgate-ext/lib/durarg"
	"github.com/rzbdz/newgate-ext/lib/style"
	cliapi "github.com/rzbdz/newgate-ext/modules/cli/extension"
	"github.com/rzbdz/newgate-ext/modules/config/domain"
	"github.com/rzbdz/newgate-ext/modules/config/store"
	i18n "github.com/rzbdz/newgate/lib/i18n"
)

// command is `newgate plugin`.
//
// **It lives here rather than in modules/cli**: a direct corollary of "everything
// is module" - the command is this module's UI, provided by it and injected via
// cli.RegisterCommand. A real violation was fixed 2026-09-18: that version put it
// in modules/cli, arguing that "registering a command needs the cli port, and cli
// needs plugin-manager to render the list, a cycle" - but the cycle was
// **self-inflicted**, since it also made cli depend on this module. Split the other
// way: cli must not know this module (status via cli.RegisterStatus, self-reported).
//
// **Injection uses modules.Optional, not Need** (copying "register a command, so
// Need(cli)" is stopped by app/graph_test.go's TestUIStaysOutOfTheDependencyGraph):
// weak means "register if the UI is there, skip if not": no function lost, only the entry.
// Still an **ordering edge**, so Start follows the UI (see component.Optional's round-trip note).
type command struct{ manager Manager }

var (
	_ cliapi.Command        = (*command)(nil)
	_ cliapi.Documented     = (*command)(nil)
	_ cliapi.StatusProvider = (*command)(nil)
)

func (c *command) Names() []string { return []string{"plugin", "plugins"} }

func (c *command) Help() cliapi.HelpLine {
	return cliapi.HelpLine{
		Section: cliapi.SectionModules,
		Usage:   i18n.T("plugin [module[.path]] [on|off] [duration]", nil),
		Summary: i18n.T("list all modules by category; toggle a module or one of its switch points", nil),
	}
}

// Run's usage (**args has no "plugin" verb** - the dispatcher strips it, see
// the contract note on cliapi.Command):
//
//	newgate plugin                           list all modules by classification
//	newgate plugin <module>                  expand one: its switches and state
//	newgate plugin <module> on|off [dur]     turn **all** its switches on/off
//	newgate plugin <module>.<path> on|off [dur] turn a single switch on/off
//
// The list enumerates from the **component graph** (merged here), not from "who
// reported". So modules outside the switch system still show, marked "no runtime
// switch (v1)" - "no switch" and "forgot to register" must never look alike.
func (c *command) Run(host cliapi.Host, args []string) int {
	sub := cliapi.Positional(args, 0)
	switch sub {
	case "", "ls", "list":
		return c.list()
	}
	action := cliapi.Positional(args, 1)
	if action == "" {
		return c.explain(host, sub)
	}
	if action != "on" && action != "off" {
		return host.Die(64, i18n.T(
			"plugin: unknown verb \"{verb}\" (usage: newgate plugin <module>[.<path>] on|off [duration])",
			i18n.A{"verb": action}))
	}
	return c.toggle(host, sub, action == "on", cliapi.Positional(args, 2))
}

// list prints all modules grouped by classification.
func (c *command) list() int {
	modules := c.manager.Modules()
	st := store.LoadState()

	total, switchPoints := 0, 0
	nameW := 14
	for _, m := range modules {
		total++
		switchPoints += len(m.Switches)
		// Width follows the longest name: some are 19 chars (claudecode-deepseek), and
		// a fixed width would squeeze what follows.
		if w := style.VisibleWidth(m.Name); w > nameW {
			nameW = w
		}
	}
	fmt.Println(style.Title("newgate plugin",
		i18n.T("{modules} modules · {switches} runtime switch points", i18n.A{
			"modules": total, "switches": switchPoints})))

	for _, typ := range DisplayOrder() {
		var group []Module
		for _, m := range modules {
			if groupOf(m.Type) == typ {
				group = append(group, m)
			}
		}
		if len(group) == 0 {
			continue
		}
		// Sorted by name inside a group: the group order (DisplayOrder) matters, the
		// order within does not - topo order makes people re-hunt every time.
		sort.Slice(group, func(i, j int) bool { return group[i].Name < group[j].Name })
		// Section carries a leading newline and none at the end: Println adds exactly
		// that, so one blank line per group (Print would glue the heading to row one).
		fmt.Println(style.Section(string(typ)))
		for _, m := range group {
			printModuleLine(st, m, nameW)
		}
	}

	fmt.Println()
	fmt.Println(style.Hint(i18n.T("expand one module:  newgate plugin <module>", nil)))
	fmt.Println(style.Hint(i18n.T("toggle one point:   newgate plugin <module>.<path> on|off [5m|forever]", nil)))
	return 0
}

// printModuleLine renders one list row: module name + its switch overview.
func printModuleLine(st *domain.State, m Module, nameW int) {
	name := style.Pad(m.Name, nameW) + " "
	// The description gets its own line, right after the module name.
	//
	// Why not append it to the name line: that line also holds the first switch,
	// and the description is a **full sentence** - squeezing both makes neither
	// readable: wrapping jumps with terminal width, and on its own line each width is free.
	if m.Desc != "" {
		fmt.Println("  " + name + style.Dim(m.Desc))
		name = style.Pad("", nameW) + " "
	}
	if len(m.Switches) == 0 {
		fmt.Println("  " + name + style.Dim(i18n.T("cannot be toggled at runtime (v1)", nil)))
		return
	}
	var parts []string
	for _, sw := range m.Switches {
		parts = append(parts, style.Dim(shortPath(sw.Path))+" "+switchState(st, sw))
	}
	// The first segment follows the module name; the rest wraps into the indent below (MaxColumns).
	fmt.Println("  " + name + parts[0])
	for _, p := range parts[1:] {
		fmt.Println("  " + style.Pad("", nameW) + " " + p)
	}
}

// explain expands one module or one switch point.
func (c *command) explain(host cliapi.Host, target string) int {
	st := store.LoadState()

	if sw, ok := c.manager.Lookup(target); ok {
		return explainSwitch(host, st, sw)
	}

	m, ok := findModule(c.manager, target)
	if !ok {
		return host.Die(65, i18n.T(
			"no module or switch point named \"{target}\" (see the list with newgate plugin)",
			i18n.A{"target": target}))
	}
	if len(m.Switches) == 0 {
		fmt.Println(style.Title("newgate plugin "+m.Name, string(m.Type)))
		fmt.Println(style.Rule(72))
		fmt.Println(style.Item(style.Skip,
			i18n.T("cannot be toggled at runtime: this module reports no switch points", nil)))
		fmt.Println(style.Hint(i18n.T("v1 only covers switches a module reports itself; "+
			"a module that reports none cannot be toggled at runtime", nil)))
		return 0
	}
	fmt.Println(style.Title("newgate plugin "+m.Name, string(m.Type)))
	fmt.Println(style.Rule(72))
	for _, sw := range m.Switches {
		fmt.Println(style.Item(style.OK, style.Bold(sw.Path)+"  "+switchState(st, sw)))
		fmt.Println("    " + style.Dim(sw.Title))
		fmt.Println("    " + style.Dim(i18n.T("what turning it off does: {why}", i18n.A{"why": sw.Why})))
		if sw.Danger != DangerSafe {
			fmt.Println("    " + dangerColor(sw.Danger)(
				i18n.T("danger level {level}", i18n.A{"level": sw.Danger})))
		}
	}
	fmt.Println()
	fmt.Println(style.Hint(i18n.T("toggle the whole module: newgate plugin {name} off",
		i18n.A{"name": m.Name})))
	return 0
}

func explainSwitch(host cliapi.Host, st *domain.State, sw Switch) int {
	fmt.Println(style.Title("newgate plugin "+sw.Path, string(sw.Danger)))
	fmt.Println(style.Rule(72))
	fmt.Println(style.Item(style.OK, sw.Title))
	fmt.Println(style.Item(style.Skip, i18n.T("now: {state}", i18n.A{"state": switchState(st, sw)})))
	fmt.Println(style.Item(style.Skip, i18n.T("what turning it off does: {why}", i18n.A{"why": sw.Why})))
	if sw.Danger == DangerFootgun {
		fmt.Println(style.Item(style.Bad,
			i18n.T("this is a footgun: turning it on/off needs a time limit, forever is not accepted", nil)))
	}
	verb := "off"
	if !sw.Default {
		verb = "on"
	}
	fmt.Println()
	fmt.Println(style.Hint(i18n.T("change it: newgate plugin {path} {verb} [duration]",
		i18n.A{"path": sw.Path, "verb": verb})))
	return 0
}

// toggle changes one switch point or a group.
func (c *command) toggle(host cliapi.Host, target string, on bool, dur string) int {
	switches, err := c.targetsOf(target)
	if err != nil {
		return host.Die(65, err.Error())
	}

	ttl, forever, derr := switchTTL(dur)
	if derr != nil {
		return host.Die(64, derr.Error())
	}

	st := store.LoadState()

	var changed []string
	for _, sw := range switches {
		// **There is only one write semantics** (SetSwitch): the factory state picks
		// the table, an action equal to the factory state undoes the setting, and a
		// footgun never takes "forever" - get these wrong and CLI and browser give
		// different answers for one config. So no rewrite: persist and notify only.
		//
		// It returns two errors: footgun "forever" (usage error, 64) and config serialize failure.
		if err := SetSwitch(st, sw, on, ttl, forever); err != nil {
			return host.Die(64, err.Error())
		}
		changed = append(changed, sw.Path)
	}

	if serr := store.SaveState(st); serr != nil {
		return host.Die(70, serr.Error())
	}
	host.NotifyProxy()

	// "turned on/off" here and switchState's set (on/on/off/off) are **two different
	// sentences**: key = source text, so two places with different Chinese need two English strings.
	mark, word := style.OK, i18n.T("switched on", nil)
	if !on {
		mark, word = style.Warn, i18n.T("switched off", nil)
	}
	fmt.Println(style.Item(mark, fmt.Sprintf("%s %s", word, strings.Join(changed, ", "))))
	after := store.LoadState()
	for _, sw := range switches {
		if until := Remaining(after, sw.Path); !until.IsZero() {
			fmt.Println(style.Hint(i18n.T("  {path} reverts automatically in {left}", i18n.A{
				"path": sw.Path,
				"left": durarg.Format(int(time.Until(until).Seconds()))})))
		}
	}
	if !on {
		fmt.Println(style.Hint(i18n.T("revert it: newgate plugin {target} on",
			i18n.A{"target": target})))
	}
	return 0
}

// targetsOf resolves user targets into switch points. Dotted = one path, undotted =
// a module name (names have no dot - components use hyphens, so no ambiguity).
func (c *command) targetsOf(target string) ([]Switch, error) {
	if strings.Contains(target, ".") {
		sw, ok := c.manager.Lookup(target)
		if !ok {
			return nil, i18n.E(
				"no switch point named \"{target}\" (see the list with newgate plugin)",
				i18n.A{"target": target})
		}
		return []Switch{sw}, nil
	}
	m, ok := findModule(c.manager, target)
	if !ok {
		return nil, i18n.E(
			"no module named \"{target}\" (see the list with newgate plugin)",
			i18n.A{"target": target})
	}
	if len(m.Switches) == 0 {
		return nil, i18n.E(
			"module \"{target}\" reports no switch points, cannot be toggled at runtime (a v1 limitation)",
			i18n.A{"target": target})
	}
	return m.Switches, nil
}

func findModule(manager Manager, name string) (Module, bool) {
	for _, m := range manager.Modules() {
		if m.Name == name {
			return m, true
		}
	}
	return Module{}, false
}

// switchTTL parses the duration. Empty = the switch's own default TTL; "forever" = no limit.
func switchTTL(s string) (time.Duration, bool, error) {
	switch s {
	case "":
		return 0, false, nil
	case "forever":
		return 0, true, nil
	}
	d, err := durarg.Parse(s)
	if err != nil {
		return 0, false, i18n.E(
			"plugin: unknown duration \"{value}\" (supported: 30s / 2m / 2min / 1h / forever)",
			i18n.A{"value": s})
	}
	return d, false, nil
}

func setEntry(table map[string]Entry, path string, until time.Time) map[string]Entry {
	if table == nil {
		table = map[string]Entry{}
	}
	table[path] = Entry{Until: until}
	return table
}

func dropEntry(table map[string]Entry, path string) map[string]Entry {
	delete(table, path)
	return table
}

// rawConfig fetches the raw bytes of the plugin_manager section in state.json.
func rawConfig(st *domain.State) []byte {
	if st == nil {
		return nil
	}
	return st.ModuleConfig[StateKey]
}

// groupOf puts unknown classifications under others. **No error**: classification
// is a product concept that grows with releases; hard refusal would let a new word
// crash the list when the module only wanted "others". Convention plus slack.
func groupOf(t Type) Type {
	for _, known := range DisplayOrder() {
		if t == known {
			return t
		}
	}
	return TypeOthers
}

// shortPath shows only the part after the module name (already in the left column).
func shortPath(path string) string {
	if i := strings.Index(path, "."); i >= 0 {
		return path[i+1:]
	}
	return path
}

// switchState renders the current state of one switch point.
func switchState(st *domain.State, sw Switch) string {
	until := Remaining(st, sw.Path)
	suffix := ""
	if !until.IsZero() {
		suffix = style.Dim(i18n.T("({left} left)",
			i18n.A{"left": durarg.Format(int(time.Until(until).Seconds()))}))
	}
	// "turned off/on" vs "on/off": factory-on ones are kill switches (Off table),
	// factory-off ones are modes (On table). English keeps that apart with "turned".
	if sw.Default {
		if Off(st, sw.Path) {
			return dangerColor(sw.Danger)(i18n.T("turned off", nil)) + suffix
		}
		return style.Dim(i18n.T("on", nil)) + suffix
	}
	if On(st, sw.Path) {
		return dangerColor(sw.Danger)(i18n.T("turned on", nil)) + suffix
	}
	return style.Dim(i18n.T("off", nil)) + suffix
}

func dangerColor(d Danger) func(string) string {
	switch d {
	case DangerFootgun:
		return style.Red
	case DangerQuirk:
		return style.Yellow
	default:
		return style.Green
	}
}
