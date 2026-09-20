package claudecode

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

const nakedDefaultTTL = 60 * time.Second

// nakedCommand 是 `newgate naked`：把 Bash 安全分类器短路成直接批准。
//
// **它住在这里而不是 modules/cli**：裸奔是 classifier-naked 这个插件的用户
// 界面，而插件的配置类型（NakedConfig / NakedConfigKey）就归本模块。2026-09-18
// 之前它写在 modules/cli 里，于是 cli 为了一个配置结构体 import 了整个
// claudecode 模块——「cli 认识一个客户端模块」，正是 everything is module 要
// 消灭的那种依赖。现在由本模块经 cli.RegisterCommand 注入，cli 不认识本模块。
//
// 用法：
//
//	newgate naked off         关闭（默认）
//	newgate naked on          开 60 秒自限窗口
//	newgate naked 30s         自定义时长（支持 30s / 2m / 2min / 1h …）
//	newgate naked forever     永久开（每次请求打 [naked] 日志 + status 红色警告）
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

// Run 分派子命令。为什么不复用 `newgate st on/off`：st 管的是插件层整体开关、
// 不带时间语义；裸奔需要细粒度到期，而且输出必须足够醒目——用户打开的是自家
// 安全门。
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
