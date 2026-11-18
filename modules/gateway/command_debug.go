package gateway

import (
	"fmt"
	"strconv"
	"time"

	"github.com/rzbdz/newgate-ext/lib/durarg"
	"github.com/rzbdz/newgate-ext/lib/style"
	cliapi "github.com/rzbdz/newgate-ext/modules/cli/extension"
	"github.com/rzbdz/newgate-ext/modules/config/paths"
	"github.com/rzbdz/newgate-ext/modules/gateway/gatewaystate"
	"github.com/rzbdz/newgate/lib/i18n"
)

const debugDefaultMinutes = 30

// debugCommand 是 `newgate debug on|off [分钟] [--forever]`：全量请求日志。
//
// **它住在这里而不是 modules/cli**：被打开的是网关转发侧逐条 dump 请求与响应
// （见 forward 里的 dump 闸门），所以「有没有开」是 gateway 的语义。搬过来之后
// cli 的 switch 与 --help 都不再需要认识这个动词——理由与 specialCommand 同一条。
//
// 默认**限时**：一发请求的 body 可以到 8KB 以上（opencode 的 system prompt
// 单独就 ~97KB），忘了关会把磁盘写满。
type debugCommand struct{}

var (
	_ cliapi.Command    = (*debugCommand)(nil)
	_ cliapi.Documented = (*debugCommand)(nil)
)

func (debugCommand) Names() []string { return []string{"debug"} }

func (debugCommand) Help() cliapi.HelpLine {
	return cliapi.HelpLine{
		Section: cliapi.SectionObserve, Rank: rankObserve,
		Usage:   i18n.T("debug on|off [minutes]", nil),
		Summary: i18n.T("Full request log (turns itself off after 30 minutes by default)", nil),
	}
}

func (debugCommand) Run(host cliapi.Host, args []string) int {
	arg := cliapi.Positional(args, 0)
	// 同 schema-repair：不带参数要报用法，不能默认成 off。这条命令的代价更大
	// ——它会在你只想看一眼的时候把全量日志关掉，而「日志怎么没了」的排查
	// 成本远高于多敲一个 on/off。
	switch arg {
	case "":
		return host.Die(64, i18n.T("Usage: newgate debug on [minutes] [--forever] | off (no argument changes anything)", nil))
	case "on", "off":
	default:
		// `newgate debug 30` 是「开 30 分钟」的简写，保留。
		if _, err := durarg.Parse(arg); err != nil {
			return host.Die(64, i18n.T("debug: unrecognized argument {arg} (on / off / a number of minutes, e.g. 30)",
				i18n.A{"arg": strconv.Quote(arg)}))
		}
	}
	on := arg != "off"
	ttl := debugDefaultMinutes * time.Minute
	if on {
		for _, a := range args {
			var n int
			if _, err := fmt.Sscanf(a, "%d", &n); err == nil && n > 0 {
				ttl = time.Duration(n) * time.Minute
			}
		}
		if cliapi.Flag(args, "--forever") {
			ttl = 0
		}
	}

	until := ""
	if ttl > 0 {
		until = time.Now().Add(ttl).Format(time.RFC3339)
	}
	if err := gatewaystate.SetDebug(on, until); err != nil {
		return host.Die(70, err.Error())
	}

	if !on {
		fmt.Println(style.Item(style.Skip, "debug off"))
		return 0
	}
	if ttl > 0 {
		fmt.Println(style.Item(style.OK, i18n.T("debug on · turns itself off after {after}",
			i18n.A{"after": durarg.Format(int(ttl.Seconds()))})))
		fmt.Println(style.Hint(i18n.T("No expiry: newgate debug on --forever", nil)))
	} else {
		fmt.Println(style.Item(style.Warn, i18n.T("debug on · does not turn itself off", nil)))
		fmt.Println(style.Hint(i18n.T("Turn it off with newgate debug off", nil)))
	}
	fmt.Println(style.Hint(i18n.T("Log {path} · 16MB rotation, 4 files kept", i18n.A{"path": paths.LogFile()})))
	host.NotifyProxy()
	if host.DaemonRunning() {
		fmt.Println(style.Hint(i18n.T("Takes effect immediately, no restart needed", nil)))
	}
	return 0
}
