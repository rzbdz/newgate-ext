// Package takeover 是「接管」这件事的唯一实现。
//
// 为什么要有这一层
//
// 接管一个 agent 有两种机制：给 claude 这类读环境变量的工具装 PATH shim，
// 给 opencode 这类读配置文件的工具改写它自己的配置。这是**实现细节**，
// 用户不该被迫知道——`newgate shim install claude` 这种命令把我们的内部
// 结构泄漏成了用户接口，用户还得先搞清楚自己的工具属于哪一类才会用。
//
// 所以对外只有两个动词：接管（On）和释放（Off）。用哪种机制由这一层按
// agent catalog capability 的注册信息决定（有 BaseURLEnv 的走 shim，
// 其余走配置文件改写）。CLI / TUI / Web 三个壳都只调这里，不各自实现
// 一遍——见 docs/03-architecture.md 与 docs/07-clients-runtime.md。
//
// 期望态与现实态
//
// 「用户想接管谁」记在 state.json（domain.State.Takeover），「现在实际接管了
// 谁」看磁盘。两者必须分开，否则会出现两种对称的故障：
//
//	stop 不摘 shim → `claude` 仍命中 shim，wrapper 把代理懒启动回来，
//	                 等于没停（用户报的原始 bug）
//	start 不装回去 → 起来了却不接管，claude 静默直连，用户以为在走 newgate
//
// 期望态让 start = 插上、stop = 拔掉，两个方向都不丢用户的意图。
package takeover

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/rzbdz/newgate-ext/modules/config/store"
	agentapi "github.com/rzbdz/newgate-ext/modules/confighook"
	"github.com/rzbdz/newgate-ext/modules/runtime/agentstate"
	"github.com/rzbdz/newgate-ext/modules/runtime/injection"
	"github.com/rzbdz/newgate/lib/i18n"
)

// Mechanism 接管机制。用户不需要知道，但排查故障时要能看到。
type Mechanism string

const (
	// MechShim PATH 里放一个指向 newgate 的链接，在子进程里注入环境变量。
	// 用于 claude 这类读环境变量的工具：shell 里 export 的变量优先级高于
	// 配置文件，改配置文件会被静默盖掉。
	MechShim Mechanism = "shim"
	// MechConfig 改写工具自己的配置文件。
	MechConfig Mechanism = "config"
)

// Status 一个 agent 的接管状态。
type Status struct {
	Agent     string
	Mechanism Mechanism
	Wanted    bool // 用户意愿（state.json）
	// Active 是「磁盘上有没有我们做过的那件事」——shim 机制看链接在不在，config
	// 机制看我们改过的配置文件在不在。它是**我们做过什么**，不是**用户有没有这个
	// 工具**；后者是 Installed，两件事分开是有意的（见 Installed 的注释）。
	Active    bool
	Installed bool     // 这家客户端本身在不在这台机器上（`which`，排除我们的 shim）
	Detail    string   // 一句人话：怎么接管的，或为什么没有
	Files     []string // config 机制涉及的文件
}

// Result 一次接管/释放的结果。
type Result struct {
	Agent     string
	Mechanism Mechanism
	// Skipped 这个 agent 被有意跳过了（用户 off 过它）。不是成功也不是失败，
	// 展示时必须和「接管成功」区分开——打成 ✓ 会让用户以为接管上了。
	Skipped bool
	Lines   []string // 逐条说明，直接展示给用户
	Warn    []string
	Err     error
}

func mechanismOf(a *agentapi.Agent) Mechanism {
	if a.Config != nil {
		return MechConfig
	}
	if a.BaseURLEnv != "" {
		return MechShim
	}
	return ""
}

// Agents 已知 agent，稳定顺序。
func Agents() []string {
	ids := agentstate.Catalog().Names()
	sort.Strings(ids)
	return ids
}

// List 每个 agent 的接管状态。
func List() []Status {
	agents := agentstate.Catalog()
	st := store.LoadState()
	var out []Status
	for _, id := range Agents() {
		a, ok := agents.Get(id)
		if !ok {
			continue
		}
		s := Status{
			Agent: id, Mechanism: mechanismOf(a), Wanted: st.TakeoverWanted(id),
			// 判据是「这台机器上有没有这个工具」（扣掉我们自己的 shim，见
			// confighook.InstalledDefault），不是「我们改过的文件还在不在」。
			Installed: agents.Installed(id),
		}
		switch s.Mechanism {
		case MechShim:
			for _, n := range injection.Installed() {
				if n == id {
					s.Active = true
				}
			}
			if s.Active {
				s.Detail = fmt.Sprintf("%s/%s → newgate", injection.Dir(), id)
				if !injection.InPath() {
					s.Detail += i18n.T(" (but the shim dir is not in the current shell's PATH; reopen the shell)", nil)
				}
			} else {
				s.Detail = i18n.T("direct", nil)
			}
		case MechConfig:
			for _, t := range a.Config.Targets() {
				if _, err := os.Stat(t); err != nil {
					continue
				}
				s.Files = append(s.Files, t)
				if a.Config.IsTakenOver(t) {
					s.Active = true
				}
			}
			if s.Active {
				s.Detail = i18n.T("config file rewritten → local proxy", nil)
			} else if len(s.Files) == 0 {
				s.Detail = i18n.T("its config file was not found (not installed?)", nil)
			} else {
				s.Detail = i18n.T("direct", nil)
			}
		}
		if !s.Installed {
			// 没装的时候，「怎么接管的」那句话是在描述一件没意义的事（我们改过
			// 的文件还在，而那个工具不在了）。这句换成「我们找过什么、没找到什么」，
			// 因为判据用的是**这个进程的 PATH**——用户在另一个 shell 里装好了、
			// 而这个进程没看见，是可能的，说清楚才查得下去。
			s.Detail = i18n.T("{names} is not on PATH — nothing to take over",
				i18n.A{"names": strings.Join(a.Bin, "/")})
		}
		out = append(out, s)
	}
	return out
}

// On 接管一个 agent，并记下「用户要接管它」。
func On(agent string, port int) Result {
	agents := agentstate.Catalog()
	a, ok := agents.Get(agent)
	if !ok {
		return Result{Agent: agent, Err: unknown(agent)}
	}
	if err := store.SetTakeoverWanted(agent, true); err != nil {
		return Result{Agent: agent, Mechanism: mechanismOf(a), Err: err}
	}
	return apply(a, port)
}

// Off 释放一个 agent，并记下「用户不要接管它」——之后 start 也不会再接管它。
func Off(agent string) Result {
	agents := agentstate.Catalog()
	a, ok := agents.Get(agent)
	if !ok {
		return Result{Agent: agent, Err: unknown(agent)}
	}
	if err := store.SetTakeoverWanted(agent, false); err != nil {
		return Result{Agent: agent, Mechanism: mechanismOf(a), Err: err}
	}
	return release(a)
}

// OnAll 全面接管：所有没被用户显式关掉的 agent。不改任何意愿。
func OnAll(port int) []Result {
	agents := agentstate.Catalog()
	st := store.LoadState()
	var out []Result
	for _, id := range Agents() {
		a, ok := agents.Get(id)
		if !ok {
			continue
		}
		if !st.TakeoverWanted(id) {
			out = append(out, Result{Agent: id, Mechanism: mechanismOf(a), Skipped: true,
				Lines: []string{i18n.T("skipped (you turned it off; newgate on {agent} restores it)",
					i18n.A{"agent": id})}})
			continue
		}
		out = append(out, apply(a, port))
	}
	return out
}

// OffAll 全面释放。**不改意愿**——这样 newgate stop 之后再 start，
// 用户原本接管的那些还会自动插回去。
func OffAll() []Result {
	agents := agentstate.Catalog()
	var out []Result
	for _, id := range Agents() {
		a, ok := agents.Get(id)
		if !ok {
			continue
		}
		r := release(a)
		if len(r.Lines) == 0 && r.Err == nil {
			continue // 本来就没接管，不用报
		}
		out = append(out, r)
	}
	return out
}

func apply(a *agentapi.Agent, port int) Result {
	res := Result{Agent: a.ID, Mechanism: mechanismOf(a)}
	switch res.Mechanism {
	case MechShim:
		link, err := injection.Install(a.ID)
		if err != nil {
			res.Err = err
			return res
		}
		res.Lines = append(res.Lines, "PATH shim "+link+" → newgate")
		for _, rc := range injection.RCFiles() {
			changed, err := injection.AddToRC(rc)
			if err != nil {
				res.Warn = append(res.Warn, rc+": "+err.Error())
				continue
			}
			if changed {
				res.Lines = append(res.Lines, i18n.T("prepended {dir} to PATH (written into {file})",
					i18n.A{"dir": injection.Dir(), "file": rc}))
			}
		}
		if real, err := a.FindReal(injection.Dir()); err == nil {
			res.Lines = append(res.Lines, i18n.T("real {agent}: {path}", i18n.A{"agent": a.ID, "path": real}))
		} else {
			res.Warn = append(res.Warn, i18n.T("cannot find the real {agent}: {err} — the shim will fail to forward, install it first",
				i18n.A{"agent": a.ID, "err": err}))
		}
		if !injection.InPath() {
			res.Warn = append(res.Warn, i18n.T("the current shell's PATH is not in effect yet; reopen the shell or `exec $SHELL -l`", nil))
		}

	case MechConfig:
		reps, err := a.Config.Apply(port)
		if err != nil {
			res.Err = err
			return res
		}
		for _, rep := range reps {
			if rep.Skipped != "" {
				res.Lines = append(res.Lines, i18n.T("{file} (skipped: {why})",
					i18n.A{"file": rep.File, "why": rep.Skipped}))
				continue
			}
			line := rep.File
			if n := len(rep.Rewrites); n > 0 {
				line += i18n.T(" ({n} rewrites)", i18n.A{"n": n})
			}
			res.Lines = append(res.Lines, line)
			if rep.Fuzzy > 0 {
				res.Warn = append(res.Warn, i18n.T(
					"{file} has {n} spots that missed the exact rules and landed in the middle tier; worth a review",
					i18n.A{"file": rep.File, "n": rep.Fuzzy}))
			}
		}
	}
	return res
}

func release(a *agentapi.Agent) Result {
	res := Result{Agent: a.ID, Mechanism: mechanismOf(a)}
	switch res.Mechanism {
	case MechShim:
		installed := false
		for _, n := range injection.Installed() {
			if n == a.ID {
				installed = true
			}
		}
		if !installed {
			return res
		}
		if err := injection.Uninstall(a.ID); err != nil {
			res.Err = err
			return res
		}
		res.Lines = append(res.Lines, i18n.T(
			"removed the PATH shim (PATH precedence goes back to the real {agent})", i18n.A{"agent": a.ID}))

	case MechConfig:
		restored, err := a.Config.Restore()
		if err != nil {
			res.Err = err
			return res
		}
		for _, f := range restored {
			res.Lines = append(res.Lines, i18n.T("restored {file}", i18n.A{"file": f}))
		}
	}
	return res
}

func unknown(agent string) error {
	return i18n.E("unknown agent {agent} (known: {list})", i18n.A{"agent": agent, "list": Agents()})
}
