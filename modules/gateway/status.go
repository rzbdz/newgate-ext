package gateway

import (
	"strings"

	"github.com/rzbdz/newgate-ext/lib/style"
	cliapi "github.com/rzbdz/newgate-ext/modules/cli/extension"
	"github.com/rzbdz/newgate-ext/modules/config/store"
	"github.com/rzbdz/newgate-ext/modules/gateway/gatewaystate"
	"github.com/rzbdz/newgate-ext/modules/gateway/special"
	"github.com/rzbdz/newgate/lib/i18n"
)

// switchStatus 把本模块的开关（schema 修补、special 层、fallback 链、debug）
// 报给 `newgate status`。
//
// **为什么由 gateway 自己报、而不是让 cli 读 state**：这些键现在住在
// state.json 的 ModuleConfig["gateway"] 里（见 gatewaystate 的包说明），
// 是 gateway 自己的词汇；cli 若为了这一行去解析它们，就等于换了个地方
// 继续认识 gateway 的内部结构。谁的状态谁自己报——与 plugin-manager 报
// 它那份开关账本是同一个口（RegisterStatus）。
//
// 只列**离开出厂态**的：补丁、special 层、fallback 默认都开，debug 默认关，
// 一行「和出厂一样」不占版面也没信息量，与 status 那一屏「一眼看有没有被
// 关掉东西」的定位一致。
//
// **fallback=off 尤其要报**：它关掉的是「换人」这件事，症状只在**上游挂了**
// 的时候才显形（平时请求照常 200）。用户如果忘了自己拨过这个开关，会把
// 「没有 fallback」误读成「链上没人可换」——所以这一行是他唯一的线索。
// status 行的位置（Rank 小的在前）。这几行在 status 那一屏上排在接管/配置之前
// ——代理挂了所有走 newgate 的工具一起挂，所以它第一。
const rankStatusPatch = 40 // 排在代理（10）之后

type switchStatus struct{}

var (
	_ cliapi.StatusProvider = switchStatus{}
	_ cliapi.Verbose        = switchStatus{}
)

// Status 见类型说明。st 为 nil 时什么都不报（快照还没装上）。
//
// 除了本模块自己的三个开关，这里还**代转**插件层自报的状态项
// （special.StatusProvider）：那一层的注册表归本模块，插件是它的一部分，
// 所以由本模块把它们端给 cli。cli 因此不必 import gateway/special 就能显示
// 「插件说它现在是什么状态」——谁的状态谁自己报，这一层由 owner 代收。
func (switchStatus) Status() []cliapi.StatusLine {
	st := store.LoadState()
	var out []cliapi.StatusLine
	for _, item := range special.Statuses(st) {
		out = append(out, cliapi.StatusLine{Rank: rankStatusPatch, Label: item.Label, Value: item.Value})
	}

	var parts []string
	// 解析失败先说：那意味着**下面所有开关都是回退出来的**，不是用户当前的选择。
	// 不报的话，用户关掉的补丁可能正被静默打开，而这一屏看起来一切正常
	// （它读的是同一个 Parse）——见 gatewaystate.Parse 的说明。
	if cfg := gatewaystate.Parse(st); cfg.ParseErr != nil {
		parts = append(parts, style.Red(i18n.T("Failed to parse the gateway section of state.json; every switch fell back to its default (the change may not have taken effect): {err}",
			i18n.A{"err": cfg.ParseErr.Error()})))
	}
	switch {
	case gatewaystate.DebugActive(st):
		s := "debug=on"
		if until := gatewaystate.DebugUntilDisplay(st); until != "" {
			s += " " + i18n.T("(until {until})", i18n.A{"until": until})
		}
		parts = append(parts, s)
	case gatewaystate.Parse(st).Debug != nil && *gatewaystate.Parse(st).Debug:
		parts = append(parts, i18n.T("debug=expired", nil))
	}
	if gatewaystate.FallbackOff(st) {
		parts = append(parts, "fallback=off")
	}
	if !gatewaystate.RepairEnabled(st) {
		parts = append(parts, "schema-repair=off")
	}
	switch {
	case !gatewaystate.SpecialEnabled(st):
		parts = append(parts, "special_treatment=off")
	case len(gatewaystate.Parse(st).SpecialOff) > 0:
		parts = append(parts, i18n.T("special off: {list}", i18n.A{"list": strings.Join(gatewaystate.Parse(st).SpecialOff, ",")}))
	}
	if len(parts) > 0 {
		out = append(out, cliapi.StatusLine{
			Rank:  rankStatusPatch,
			Label: i18n.T("Patch switches", nil),
			Value: strings.Join(parts, "   ") +
				i18n.T("   restore: newgate st on · newgate schema-repair on · newgate fallback on · newgate debug off", nil),
		})
	}
	return out
}

// Verbose 上报「全量请求日志开着」。
//
// 界面拿它决定要不要对自己的输出做版式自检（见 cliapi.Verbose 与 modules/cli 的
// layout_audit）：debug 开着的时候用户正在盯细节，输出对齐就是细节的一部分。
//
// 以前这个判断是界面直接读 gateway 的 state 段做的——界面认识别人的配置结构。
func (switchStatus) Verbose() bool { return gatewaystate.DebugActive(store.LoadState()) }
