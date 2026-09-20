package claudecode

import (
	"fmt"
	"time"

	"github.com/rzbdz/newgate/lib/durarg"
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
		Usage:   "naked on|forever|off|<时长>",
		Summary: "短路 Bash 分类器：on=60s / forever=永久 / off=关",
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
			return host.Die(64, fmt.Sprintf(
				"naked: 不认识的时长 %q（支持 on / forever / off / 30s / 2m / 2min / 1h）", sub))
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
	fmt.Println(style.Item(style.OK, "裸奔已关闭 —— 分类器恢复正常工作"))
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
	fmt.Println(style.Item(style.Warn, fmt.Sprintf(
		"裸奔已开启 —— 分类器短路 %s 后自动关闭", durarg.Format(int(ttl.Seconds())))))
	fmt.Println(style.Hint("这段时间内所有 Bash 分类器请求直接被批准，不经过任何安全检查"))
	fmt.Println(style.Hint("提前关闭：newgate naked off"))
	host.NotifyProxy()
	return 0
}

func nakedForever(host cliapi.Host) int {
	cfg := NakedConfig{Mode: "forever"}
	if err := saveNakedConfig(cfg); err != nil {
		return host.Die(70, err.Error())
	}
	fmt.Println(style.Item(style.Bad, "裸奔永久模式已开启 —— 分类器被完全短路"))
	fmt.Println(style.Item(style.Bad, "每一个被拦截的请求都打 [naked] 日志，newgate status 持续显示红色警告"))
	fmt.Println(style.Hint("关闭：newgate naked off"))
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
