package nothink

import (
	"fmt"
	"strconv"
	"time"

	"github.com/rzbdz/newgate/lib/durarg"
	i18n "github.com/rzbdz/newgate/lib/i18n"
	"github.com/rzbdz/newgate/lib/style"
	cliapi "github.com/rzbdz/newgate/modules/cli/extension"
	"github.com/rzbdz/newgate/modules/config/store"
)

// defaultTTL 是 `newgate nothink on` 的默认窗口，与 `newgate naked on` 同一个数。
//
// `on` 这个子命令的用途是「就这会儿」——调一次长活、跑一轮贵的批处理——那需要一个
// 会自己消失的缺省；长期开着的人写 `forever`，那是一个**要说出口**的决定。
const defaultTTL = 60 * time.Second

// nothinkCommand 是 `newgate nothink`。
//
// **它住在这里而不是 modules/cli**：这是 nothink 这个插件的用户界面，而插件的配置
// 类型（Config / StateKey）就归本模块。界面不认识任何业务模块，是模块把自己的命令
// 注入给界面（cli.RegisterCommand）。
//
// 用法：
//
//	newgate nothink off       关（默认）
//	newgate nothink on        开 60 秒自限窗口
//	newgate nothink 30s       自定义时长（30s / 2m / 2min / 1h …）
//	newgate nothink forever   一直开（status 会一直打这一行）
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

// Run 分派子命令。为什么不做成 `newgate st on nothink`：st 管的是插件层整体开关、
// 不带时间语义，而这条策略常用的是「这一小时别想」，到期必须自己消失。
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

// printBestEffort 说清这条开关**做不到**什么。
//
// 用户打开它的时候必须知道边界在哪：有的模型关不掉思考（给它最小强度），
// OpenAI 方言的请求没有这个字段可写（一个字节都不动）。不说的话，症状是
// 「我开了啊，怎么还在想」——而那是设计，不是故障。
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
