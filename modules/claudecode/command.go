package claudecode

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

const nakedDefaultTTL = 60 * time.Second

// nakedCommand is `newgate naked`: short-circuit the Bash safety classifier into direct approval.
//
// **It lives here rather than in modules/cli**: naked mode is the user interface of the
// classifier-naked plugin, and the plugin's config types (NakedConfig / NakedConfigKey)
// belong to this module. Before 2026-09-18 it was written in modules/cli, so cli
// imported the whole claudecode module for one config struct -- "cli knows a client
// module", exactly the dependency everything is module exists to kill. Now this
// module injects it via cli.RegisterCommand, and cli does not know this module.
//
// Usage:
//
//	newgate naked off         turn off (default)
//	newgate naked on          open a self-limiting 60-second window
//	newgate naked 30s         custom duration (30s / 2m / 2min / 1h … supported)
//	newgate naked forever     turn on permanently (logs [naked] per request + red status warning)
type nakedCommand struct{}

var (
	_ cliapi.Command    = (*nakedCommand)(nil)
	_ cliapi.Documented = (*nakedCommand)(nil)
)

func (nakedCommand) Names() []string { return []string{"naked"} }

func (nakedCommand) Help() cliapi.HelpLine {
	return cliapi.HelpLine{
		Section: cliapi.SectionMaintenance,
		Usage:   i18n.T("naked on|forever|off|<duration>", nil),
		Summary: i18n.T("short-circuit the Bash classifier: on=60s / forever=permanent / off=disabled", nil),
	}
}

// Run dispatches the subcommand. Why not reuse `newgate st on/off`: st manages the plugin
// layer's overall switch and carries no time semantics; naked mode needs fine-grained expiry,
// and its output must be loud enough -- what the user is opening is their own safety gate.
func (nakedCommand) Run(host cliapi.Host, args []string) int {
	sub := cliapi.Positional(args, 0)
	switch sub {
	case "", "off":
		return nakedOff(host)
	case "forever":
		return nakedForever(host)
	case "on":
		return nakedOn(host, nakedDefaultTTL)
	default:
		d, err := durarg.Parse(sub)
		if err != nil {
			return host.Die(64, i18n.T(
				"naked: unknown duration {arg} (supported: on / forever / off / 30s / 2m / 2min / 1h)",
				i18n.A{"arg": strconv.Quote(sub)}))
		}
		return nakedOn(host, d)
	}
}

func nakedOff(host cliapi.Host) int {
	s := store.LoadState()
	delete(s.ModuleConfig, NakedConfigKey)
	if err := store.SaveState(s); err != nil {
		return host.Die(70, err.Error())
	}
	fmt.Println(style.Item(style.OK, i18n.T("naked is off — the classifier is working normally again", nil)))
	host.NotifyProxy()
	return 0
}

func nakedOn(host cliapi.Host, ttl time.Duration) int {
	cfg := NakedConfig{
		Mode:      "on",
		ExpiresAt: time.Now().Add(ttl),
	}
	if err := saveNakedConfig(cfg); err != nil {
		return host.Die(70, err.Error())
	}
	fmt.Println(style.Item(style.Warn, i18n.T(
		"naked is on — the classifier stays short-circuited for {ttl}, then turns itself off",
		i18n.A{"ttl": durarg.Format(int(ttl.Seconds()))})))
	fmt.Println(style.Hint(i18n.T("within this window every Bash classifier request is approved directly, with no safety check at all", nil)))
	fmt.Println(style.Hint(i18n.T("end it early: newgate naked off", nil)))
	host.NotifyProxy()
	return 0
}

func nakedForever(host cliapi.Host) int {
	cfg := NakedConfig{Mode: "forever"}
	if err := saveNakedConfig(cfg); err != nil {
		return host.Die(70, err.Error())
	}
	fmt.Println(style.Item(style.Bad, i18n.T("naked forever is on — the classifier is short-circuited completely", nil)))
	fmt.Println(style.Item(style.Bad, i18n.T("every intercepted request logs [naked], and newgate status keeps showing a red warning", nil)))
	fmt.Println(style.Hint(i18n.T("turn it off: newgate naked off", nil)))
	host.NotifyProxy()
	return 0
}

func saveNakedConfig(cfg NakedConfig) error {
	raw, err := cfg.Marshal()
	if err != nil {
		return err
	}
	s := store.LoadState()
	if s.ModuleConfig == nil {
		s.ModuleConfig = make(map[string][]byte)
	}
	s.ModuleConfig[NakedConfigKey] = raw
	return store.SaveState(s)
}
