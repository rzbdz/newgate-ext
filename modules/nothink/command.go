package nothink

import (
	"fmt"
	"strconv"
	"time"

	"github.com/rzbdz/newgate-ext/lib/durarg"
	"github.com/rzbdz/newgate-ext/lib/style"
	cliapi "github.com/rzbdz/newgate-ext/modules/cli/extension"
	"github.com/rzbdz/newgate-ext/modules/config/store"
	i18n "github.com/rzbdz/newgate/lib/i18n"
)

// defaultTTL is the default window of `newgate nothink on`, the same number as `newgate naked on`.
//
// The purpose of the `on` subcommand is "just for now" -- one long job, one round of expensive
// batch work -- which needs a default that disappears on its own; people who want it on long term
// write `forever`, and that is a decision that **has to be said out loud**.
const defaultTTL = 60 * time.Second

// nothinkCommand is `newgate nothink`.
//
// **It lives here rather than in modules/cli**: this is the nothink plugin's UI, and the plugin's
// config types (Config / StateKey) belong to this module. The UI knows no business module;
// the module injects its own command into the UI (cli.RegisterCommand).
//
// Usage:
//
//	newgate nothink off       off (the default)
//	newgate nothink on        on, a self-limiting 60 second window
//	newgate nothink 30s       custom duration (30s / 2m / 2min / 1h …)
//	newgate nothink forever   always on (status keeps printing that line)
type nothinkCommand struct{}

var (
	_ cliapi.Command    = (*nothinkCommand)(nil)
	_ cliapi.Documented = (*nothinkCommand)(nil)
)

func (nothinkCommand) Names() []string { return []string{"nothink"} }

func (nothinkCommand) Help() cliapi.HelpLine {
	return cliapi.HelpLine{
		Section: cliapi.SectionMaintenance,
		Usage:   i18n.T("nothink on|forever|off|<duration>", nil),
		Summary: i18n.T("ask every upstream not to think: on=60s / forever=permanent / off=disabled", nil),
	}
}

// Run dispatches the subcommands. Why not `newgate st on nothink`: st manages the plugin layer's
// overall switches and carries no time semantics, while what this policy usually wants is "not this
// hour", which must disappear on its own when it expires.
func (nothinkCommand) Run(host cliapi.Host, args []string) int {
	sub := cliapi.Positional(args, 0)
	switch sub {
	case "", "off":
		return nothinkOff(host)
	case "forever":
		return nothinkForever(host)
	case "on":
		return nothinkOn(host, defaultTTL)
	default:
		d, err := durarg.Parse(sub)
		if err != nil {
			return host.Die(64, i18n.T("nothink: unknown duration {arg} (supported: on / forever / off / 30s / 2m / 2min / 1h)",
				i18n.A{"arg": strconv.Quote(sub)}))
		}
		return nothinkOn(host, d)
	}
}

func nothinkOff(host cliapi.Host) int {
	s := store.LoadState()
	delete(s.ModuleConfig, StateKey)
	if err := store.SaveState(s); err != nil {
		return host.Die(70, err.Error())
	}
	fmt.Println(style.Item(style.OK, i18n.T("nothink is off — requests go upstream with the thinking the client asked for", nil)))
	host.NotifyProxy()
	return 0
}

func nothinkOn(host cliapi.Host, ttl time.Duration) int {
	if err := save(Config{Mode: "on", ExpiresAt: time.Now().Add(ttl)}); err != nil {
		return host.Die(70, err.Error())
	}
	fmt.Println(style.Item(style.Warn, i18n.T(
		"nothink is on — for {ttl} every request goes upstream with thinking off",
		i18n.A{"ttl": durarg.Format(int(ttl.Seconds()))})))
	printBestEffort()
	fmt.Println(style.Hint(i18n.T("end it early: newgate nothink off", nil)))
	host.NotifyProxy()
	return 0
}

func nothinkForever(host cliapi.Host) int {
	if err := save(Config{Mode: "forever"}); err != nil {
		return host.Die(70, err.Error())
	}
	fmt.Println(style.Item(style.Warn, i18n.T("nothink forever is on — every request goes upstream with thinking off", nil)))
	printBestEffort()
	fmt.Println(style.Hint(i18n.T("turn it off: newgate nothink off", nil)))
	host.NotifyProxy()
	return 0
}

// printBestEffort spells out what this switch **cannot** do.
//
// The user must know where the boundary is when turning it on: some models cannot have thinking
// turned off (give them the lowest effort), OpenAI-dialect requests have no such field to write
// (not one byte touched). Unsaid, the symptom is "I turned it on, why is it still thinking" -- yet
// that is by design, not a failure.
func printBestEffort() {
	fmt.Println(style.Hint(i18n.T("best effort: a model that cannot turn thinking off gets the lowest reasoning effort, and OpenAI-dialect requests are untouched", nil)))
}

func save(c Config) error {
	raw, err := Marshal(c)
	if err != nil {
		return err
	}
	s := store.LoadState()
	if s.ModuleConfig == nil {
		s.ModuleConfig = make(map[string][]byte)
	}
	s.ModuleConfig[StateKey] = raw
	return store.SaveState(s)
}
