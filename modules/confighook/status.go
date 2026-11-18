package confighook

import (
	"sort"
	"strings"

	"github.com/rzbdz/newgate-ext/lib/style"
	cliapi "github.com/rzbdz/newgate-ext/modules/cli/extension"
	i18n "github.com/rzbdz/newgate/lib/i18n"
)

// 本文件是 `newgate status` 里的**每个客户端**那一块。
//
// # 为什么由本模块报
//
// 客户端目录（有哪些 agent、每个 agent 有哪些槽位、槽位此刻走哪一档）归本模块；
// 链头（profile）归 config，但「这个客户端实际从哪条链起步」这层组合只有同时
// 看得见两边的人才说得清——而本模块就是那个位置（见 profile.go）。
//
// 上一版 status 只有 config 的一行：它说全局缺省，只在**覆盖值与缺省不同**时
// 才展开一个 agent。于是「claude 单独定过 ds、而全局缺省正好也是 ds」在终端上
// 完全看不见，用户只能去翻 state.json；TUI 更没有地方看。这里把每个客户端都
// 列出来，profile 后面老实写着它是**跟随缺省**还是**单独定过**。
const rankStatusAgents = 25

type agentStatus struct{ agents AgentCatalog }

var _ cliapi.BlockProvider = agentStatus{}

func (s agentStatus) StatusBlocks() []cliapi.StatusBlock {
	names := s.agents.Names()
	if len(names) == 0 {
		return nil
	}
	sort.Strings(names)
	var lines []string
	for _, id := range names {
		a, ok := s.agents.Get(id)
		if !ok {
			continue
		}
		ap := AgentProfile{AgentID: a.ID}
		source := i18n.T("default", nil)
		if ap.IsOverride() {
			source = i18n.T("set", nil)
		}
		// 每个客户端两行而不是一张表：槽位名是**不可断的标识符**，表格在
		// 75 列里收缩列宽时会把 `subagent` 断成 `s / ubagent`（2026-10-28 实测）。
		// 两行版把「谁」与「它那几格走哪一档」分开，换行只发生在空格上。
		lines = append(lines,
			style.Field(a.ID, style.Cyan(ap.Active())+style.Dim(" ("+source+")")),
			style.Hint(slotSummary(a.Slots, s.agents.Facts(a.ID))),
		)
	}
	return []cliapi.StatusBlock{{
		Rank:  rankStatusAgents,
		Title: i18n.T("Agents", nil),
		Lines: lines,
	}}
}

// slotSummary 是「这个客户端的槽位此刻落在哪些档」的一行摘要。
//
// 它报的是**档位**，不是 provider/model：后者的解析归 config（见
// `newgate agents` 那张带 Binding 的表）。status 是概览，把每条 binding 都铺开
// 会把下面那张「档位绑定」表淹掉；而档位是用户真正会改的东西。
func slotSummary(slots []Slot, facts AgentFacts) string {
	if len(slots) == 0 {
		return i18n.T("discovered from its config at takeover", nil)
	}
	parts := make([]string, 0, len(slots))
	for _, s := range slots {
		parts = append(parts, s.Name+"="+TierOf(facts, s))
	}
	return strings.Join(parts, "  ")
}
