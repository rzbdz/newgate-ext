package confighook

// 本文件是 `newgate agents`：列出已知客户端及其模型槽位。
//
// **为什么它住在这里**（2026-09-18）：agent 是谁、有哪些槽位、槽位走哪个环境变量，
// 全是**客户端描述符**的知识，而描述符注册表归本模块（AgentCatalog）。它以前写在
// modules/cli/profile.go 里，界面因此要认识 AgentCatalog 的每个字段。
//
// 依赖方向：config-hook → cli/extension（叶子契约），不是 → modules/cli。

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/rzbdz/newgate-ext/lib/style"
	cliapi "github.com/rzbdz/newgate-ext/modules/cli/extension"
	"github.com/rzbdz/newgate-ext/modules/config/domain"
	"github.com/rzbdz/newgate-ext/modules/config/store"
	i18n "github.com/rzbdz/newgate/lib/i18n"
)

// agentsCommand 是 `newgate agents`。
type agentsCommand struct{ agents AgentCatalog }

var (
	_ cliapi.Command    = (*agentsCommand)(nil)
	_ cliapi.Documented = (*agentsCommand)(nil)
)

func (agentsCommand) Names() []string { return []string{"agents"} }

func (agentsCommand) Help() cliapi.HelpLine {
	return cliapi.HelpLine{Section: cliapi.SectionRouting, Rank: 30,
		Usage: "agents", Summary: i18n.T("known agents: profile, model slots and their current bindings", nil)}
}

func (c agentsCommand) Run(_ cliapi.Host, _ []string) int { return runAgents(c.agents) }

// cmdAgents 已知 agent 及其模型槽位。
//
// 每个 agent 一个小节：头一行是身份（方言 + 当前 profile），槽位一张表。
// 以前是一张通铺大表，说明列又长，中文一撑就错位。
func runAgents(agents AgentCatalog) int {
	names := agents.Names()
	sort.Strings(names)

	fmt.Println(style.Title("newgate agents", i18n.T("{n} total", i18n.A{"n": len(names)})))
	fmt.Println(style.Rule(72))

	for _, n := range names {
		a, _ := agents.Get(n)
		// 这个客户端交上来的运行时事实（可能没有）。取一次、往下传：它每次问都可能
		// 读磁盘（见发行版 claudecode 的实现），而这里一个 agent 要问它好几遍。
		facts := agents.Facts(n)
		ap := AgentProfile{AgentID: a.ID}
		profile := ap.Active()
		source := i18n.T("follows the global default", nil)
		if ap.IsOverride() {
			source = i18n.T("set for this client only", nil)
		}
		fmt.Println()
		fmt.Println("  " + style.Bold(a.ID) +
			style.Dim("   "+i18n.T("{dialect} dialect", i18n.A{"dialect": a.Dialect})) +
			style.Dim("   profile ") + style.Cyan(profile) +
			style.Dim(" · "+source))
		if a.Notes != "" {
			fmt.Println(style.Hint(a.Notes))
		}
		if len(a.Slots) == 0 {
			fmt.Println(style.Hint(i18n.T("slots are discovered from the config file at startup", nil)))
		} else {
			// 链头那一份 profile 只读一次：下面每个槽位都要拿它的绑定，而读盘 + 解析
			// 是这一屏里最贵的两件事。读不出来时仍然把槽位表列出来（档位是描述符的
			// 事实），只是 Binding 一列显示「未知」——不因为一个坏文件把整张表吞掉。
			pr, profileErr := store.LoadProfile(profile)
			if profileErr != nil {
				fmt.Println(style.Item(style.Warn, i18n.T("cannot read profile {name}: {err}",
					i18n.A{"name": profile, "err": profileErr.Error()})))
			}
			provs, _ := store.LoadProviders()
			t := style.NewTable(i18n.T("Slot", nil), i18n.T("Tier", nil), i18n.T("Binding", nil))
			for _, s := range a.Slots {
				// 这一列说的是「此刻实际走哪儿」（改过就用改的，见 AgentFacts）。
				tier := TierOf(facts, s)
				t.Row(s.Name, style.Cyan(tier), slotBinding(pr, provs, tier))
			}
			fmt.Print(t.String())
			// 说明单独一行：塞进表格会把整张表撑到一百多列，反而没法对读。
			//
			// 「改过没有」也在这行而不在表格里：第一版把它拼进档位那一格，结果一行的
			// 宽度多出十来个**全角**字符，列宽是按字符数算的，整张表当场歪掉（实测）。
			// 这行本来就是「这个槽位是干什么的」，改过没有属于同一类注脚。
			for _, s := range a.Slots {
				note := ""
				if tier := TierOf(facts, s); tier != s.Tier {
					note += " · " + i18n.T("{tier} was set in the config (the default is {def})",
						i18n.A{"tier": tier, "def": s.Tier})
				}
				if s.Desc != "" || note != "" {
					fmt.Println(style.Hint(s.Name + "  " + s.Desc + note))
				}
				// 环境变量单独一行：它是**不可断的标识符**（ANTHROPIC_DEFAULT_…），
				// 拼在说明后面会被 75 列切在中间（实测 `ANT` / `HROPIC…`）。
				// 这一行是 Claude Code 那类「靠 env 注入」的客户端的真正设置。
				if s.EnvVar != "" {
					fmt.Println(style.Hint(i18n.T("env {var}", i18n.A{"var": s.EnvVar})))
				}
			}
		}
		// 它自己的配置文件与此刻有没有被接管。Claude Code 走 shim/env，没有
		// Config（所以这里什么都不打）；codex / opencode 走配置文件，这两行正是
		// 「设置落到哪儿」的答案。TUI 的详情页读的是同一份 Targets/IsTakenOver。
		if a.Config != nil {
			for _, target := range a.Config.Targets() {
				state := i18n.T("not taken over", nil)
				mark := style.Skip
				if a.Config.IsTakenOver(target) {
					state = i18n.T("taken over", nil)
					mark = style.OK
				}
				fmt.Println("  " + style.Dim(i18n.T("config", nil)) + "  " + filepath.Base(target) +
					"  " + style.Mark(mark) + " " + state)
			}
		}
	}

	fmt.Println()
	fmt.Println(style.Hint(i18n.T("switch a single agent only: newgate --set-profile <name> --agent <agent>", nil)))
	fmt.Println(style.Hint(i18n.T("or use newgate tui to do the same thing", nil)))
	return 0
}

// slotBinding 把一个槽位的档位解析成「落到哪家上游的哪个模型」。
//
// 这正是用户来 `newgate agents` 想看的最后一跳：档位（normal）是语义，binding
// （provider/model）才是真正会发出去的请求目标。解析不了（profile 读坏了、这一档
// 没绑）时返回一个明确的占位，而不是空白——空白看起来像「没配」和「读不出来」
// 同一件事，而修复动作完全不同。
func slotBinding(pr *domain.Profile, provs *domain.Providers, tier string) string {
	if pr == nil {
		return style.Dim(i18n.T("unknown", nil))
	}
	b, ok := pr.Resolve(tier)
	if !ok {
		return style.Dim(i18n.T("unbound", nil))
	}
	out := b.String()
	if provs == nil || b.IsRef() {
		return out
	}
	p, exists := provs.Providers[b.Provider]
	switch {
	case !exists:
		out += " " + style.Red(i18n.T("provider is not defined", nil))
	case p.Key() == "":
		out += " " + style.Yellow(i18n.T("api_key missing", nil))
	}
	return out
}

// glossary 贡献帮助屏术语表里的「agent」一行。
//
// 它是**客户端目录**的词汇（哪些 CLI 被接管），所以归本模块——界面不必认识
// AgentCatalog 才写得出来（见 cliapi.Glossarist）。
type glossary struct{ agents AgentCatalog }

var _ cliapi.Glossarist = glossary{}

func (g glossary) Glossary() []cliapi.GlossaryLine {
	names := g.agents.Names()
	list := i18n.T("(not assembled)", nil)
	switch {
	case len(names) == 0:
		list = i18n.T("(none)", nil)
	default:
		list = strings.Join(names, " / ")
	}
	return []cliapi.GlossaryLine{{Rank: 10, Term: "agent",
		Definition: i18n.T("CLIs taken over: {list}", i18n.A{"list": list})}}
}
