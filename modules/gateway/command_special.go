package gateway

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/rzbdz/newgate-ext/lib/style"
	"github.com/rzbdz/newgate-ext/lib/view"
	cliapi "github.com/rzbdz/newgate-ext/modules/cli/extension"
	"github.com/rzbdz/newgate-ext/modules/config/domain"
	"github.com/rzbdz/newgate-ext/modules/config/store"
	"github.com/rzbdz/newgate-ext/modules/gateway/gatewaystate"
	"github.com/rzbdz/newgate/lib/i18n"

	"github.com/rzbdz/newgate-ext/modules/gateway/special"
)

// specialCommand is `newgate st`: the special_treatment plugin layer's list and switches.
//
// **Why it lives here and not in modules/cli**: this layer's plugins are part of the
// gateway data plane; plugin names, Why and switch semantics are all gateway's own
// knowledge. Before 2026-09-18 it lived in cli, so cli imported gateway/special and **read
// that package's global registry directly** (`special.Plugins()`) - bypassing a capability
// to read someone else's owner state, exactly the dependency everything is module wants
// to eliminate (the same applies to `probe` / `metrics` / `schema-repair`). After the move
// cli need not know the special package, and gateway decides what these commands look like.
//
// Usage:
//
//	newgate st list plugins: one per line, why
//	it exists newgate st <plugin> a single plugin's full
//	description newgate st on|off the whole layer's switch
//	newgate st on|off <plugin> switch one plugin on or off
//
// Why the command is worth having: this layer **changes the user's request**.
// When checking "did newgate break this upstream error", the user must see at
// a glance which patches are in effect and be able to turn them off one by one.
type specialCommand struct{}

var (
	_ cliapi.Command    = (*specialCommand)(nil)
	_ cliapi.Documented = (*specialCommand)(nil)
)

func (specialCommand) Names() []string {
	return []string{"st", "special", "special-treatment", "special_treatment"}
}

func (specialCommand) Help() cliapi.HelpLine {
	return cliapi.HelpLine{
		Section: cliapi.SectionObserve, Rank: rankObserve,
		Usage:   i18n.T("st [on|off] [plugin]", nil),
		Summary: i18n.T("special_treatment switches and descriptions", nil),
	}
}

// The args Run receives **do not include the verb "st"** (see cliapi.Command's contract).
func (specialCommand) Run(host cliapi.Host, args []string) int {
	st := store.LoadState()
	sub := cliapi.Positional(args, 0)
	name := cliapi.Positional(args, 1)

	if sub == "" || sub == "status" || sub == "ls" {
		return specialList(host, st)
	}
	// giving a plugin name directly = see its full description
	// (Why is often several lines, which would drown the list)
	if p := findPlugin(sub); p != nil {
		return specialExplain(host, st, sub)
	}

	if sub != "on" && sub != "off" {
		return host.Die(64, i18n.T("No plugin named {name} (run newgate st for the list); switch the whole layer with newgate st on|off",
			i18n.A{"name": strconv.Quote(sub)}))
	}
	on := sub == "on"

	if name == "" {
		if err := gatewaystate.SetSpecialTreatment(on); err != nil {
			return host.Die(70, err.Error())
		}
		if on {
			fmt.Println(style.Item(style.OK, i18n.T("special_treatment layer enabled", nil)))
		} else {
			fmt.Println(style.Item(style.Skip, i18n.T("special_treatment layer disabled", nil)) +
				style.Dim(i18n.T("   Requests forwarded unchanged, upstream quirks no longer patched", nil)))
		}
		host.NotifyProxy()
		return 0
	}

	if findPlugin(name) == nil {
		return host.Die(65, i18n.T("No plugin named {name} (run newgate st for the list)",
			i18n.A{"name": strconv.Quote(name)}))
	}
	if err := gatewaystate.SetSpecialPlugin(name, on); err != nil {
		return host.Die(70, err.Error())
	}
	if on {
		fmt.Println(style.Item(style.OK, i18n.T("Plugin {name} enabled", i18n.A{"name": name})))
	} else {
		fmt.Println(style.Item(style.Skip, i18n.T("Plugin {name} disabled", i18n.A{"name": name})))
	}
	if !gatewaystate.SpecialEnabled(st) {
		fmt.Println(style.Hint(i18n.T("The whole layer is still off; run newgate st on first", nil)))
	}
	host.NotifyProxy()
	return 0
}

func findPlugin(name string) special.Plugin {
	for _, p := range special.Plugins() {
		if p.Name() == name {
			return p
		}
	}
	return nil
}

// specialState is one plugin's state right now: the layer switch + the plugin's switch.
//
// Both UIs share it: the CLI prints that mark, the web card turns the same mark into
// a color (specialTone). The criterion lives in this one place - "what counts as
// active" should not be written twice in two UIs, or they will eventually disagree.
func specialState(st *domain.State, name string) (mark, word string) {
	switch {
	case !gatewaystate.SpecialEnabled(st):
		return style.Skip, i18n.T("layer off", nil)
	case gatewaystate.PluginOff(st, name):
		return style.Bad, i18n.T("disabled individually", nil)
	}
	return style.OK, i18n.T("active", nil)
}

// specialTone turns the CLI's mark into the web card's color.
//
// **One-to-one, no re-judging of severity**: active → ok, whole layer off → warn, one plugin
// off → bad, saying the same thing as the mark in the terminal. This is not aesthetics:
// `runtime/view.go` says "two UIs giving different answers about the same on-disk state
// is worse than one of them not showing it" - painting the CLI's ✗ yellow on the web page
// is that "different answer", and the **worst kind**: the words are the same, only the
// severity differs, so the reader trusts the color first and notices the text too late.
//
// tone has only three words (ok/warn/bad, see lib/view) while mark has four: style.Skip
// (the `·` of a whole layer off) has no tone, so it lands on warn - "none of these patches
// is on now" should be seen, but it is not bad: that slot is taken by "individually off".
func specialTone(mark string) string {
	switch mark {
	case style.OK:
		return view.ToneOK
	case style.Bad:
		return view.ToneBad
	default:
		return view.ToneWarn
	}
}

func specialList(host cliapi.Host, st *domain.State) int {
	ps := special.Plugins()
	layer := style.Green(i18n.T("on", nil))
	if !gatewaystate.SpecialEnabled(st) {
		layer = style.Yellow(i18n.T("off (whole layer)", nil))
	}
	fmt.Println(style.Title("newgate st", i18n.N("special_treatment {layer} · {n} plugin",
		"special_treatment {layer} · {n} plugins", len(ps), i18n.A{"layer": layer})))
	fmt.Println(style.Rule(72))
	if len(ps) == 0 {
		fmt.Println(style.Hint(i18n.T("No plugins registered", nil)))
		return 0
	}
	t := style.NewTable(i18n.T("State", nil), i18n.T("Plugin", nil), i18n.T("Why it exists", nil))
	for _, p := range ps {
		mark, _ := specialState(st, p.Name())
		why := strings.SplitN(p.Why(), "\n", 2)[0]
		t.Row(style.Mark(mark), p.Name(), style.Dim(why))
	}
	fmt.Print(t.String())
	fmt.Println(style.Hint(i18n.T("Upstream quirk patches: they apply only to the upstream that claimed the request, and every change is logged", nil)))
	fmt.Println(style.Hint(i18n.T("Full description: newgate st <plugin> · disable one: newgate st off <plugin> · disable the layer: newgate st off", nil)))
	// Reasoning cache counters are also reported by this module (see thinkcache_status.go):
	// they used to go back to the UI for rendering through a cliapi.Host port, and that
	// port's name (PrintThinkCache) was itself evidence that "the UI knows a module concept".
	printThinkCache()
	return 0
}

func specialExplain(host cliapi.Host, st *domain.State, name string) int {
	p := findPlugin(name)
	mark, word := specialState(st, name)
	fmt.Println(style.Title("newgate st "+name, word))
	fmt.Println(style.Rule(72))
	fmt.Println(style.Item(mark, p.Why()))
	fmt.Println()
	if gatewaystate.PluginOff(st, name) {
		fmt.Println(style.Hint(i18n.T("Enable: newgate st on {name}", i18n.A{"name": name})))
	} else {
		fmt.Println(style.Hint(i18n.T("Disable: newgate st off {name}", i18n.A{"name": name})))
	}
	return 0
}
