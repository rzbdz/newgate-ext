// Package launch 是「启动一个被接管的 agent」这件事的唯一实现。
//
// 它同时服务两个入口：
//
//   - argv0 分发：`claude`（PATH shim 指向 newgate 二进制）→ main 检测到
//     basename 是 agent，转到这里；
//   - CLI 包装启动：`newgate claude --profile=ds` → cli 解析出 agent 与
//     profile，也转到这里。
//
// 它做的事（docs/07-clients-runtime.md）：解析真实可执行文件、必要时懒起代理、往子进程
// 注入 env（含真实模型名）、exec。除此之外不碰任何文件、不发任何网络请求。
//
// 槽位命名是这一层的核心决策（2026-09-09 定稿，见 buildInject 注释）：
// 默认注入**档位名**（heavy/mid/…）——会话发档位名回来，代理按请求时的
// 当前配置解析，`newgate --set-profile` 对跑着的会话立刻生效（动态）；
// 显式 --profile 钉死本次调用时才注入**真实模型名**（glm-5.3），显示的
// 就是这次真用的（钉死才配真名）。窗口声明（CLAUDE_CODE_*）两种模式
// 都注入，与命名无关。
package launch

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"syscall"
	"time"

	"github.com/rzbdz/newgate-ext/lib/httpx"
	"github.com/rzbdz/newgate-ext/modules/config/domain"
	"github.com/rzbdz/newgate-ext/modules/config/resolve"
	"github.com/rzbdz/newgate-ext/modules/config/store"
	agentapi "github.com/rzbdz/newgate-ext/modules/confighook"
	"github.com/rzbdz/newgate-ext/modules/runtime/daemon"
	"github.com/rzbdz/newgate-ext/modules/runtime/injection"
	"github.com/rzbdz/newgate/lib/i18n"
)

// Options 一次启动的覆盖项。零值 = 用 agent 的默认 profile。
type Options struct {
	// Profile 本次调用的 profile 覆盖（不写全局状态）。空 = 走 agent 默认。
	Profile string
}

// Launch 注入 env 并 exec 真实 agent。成功时进程被替换、不再返回；
// 返回的 int 是进程退出码（仅在失败路径返回）。
func Launch(a *agentapi.Agent, facts agentapi.AgentFacts, args []string, o Options) int {
	depth, _ := strconv.Atoi(os.Getenv("NEWGATE_DEPTH"))
	if depth >= 2 || os.Getenv("NEWGATE_DISABLE") == "1" {
		return execReal(a, args, nil) // 完全透传，不注入
	}

	st := store.LoadState()
	if _, err := a.FindReal(injection.Dir()); err != nil {
		fmt.Fprintf(os.Stderr, "newgate: %v\n", err)
		return 69
	}

	// 代理懒启动；否则 agent 一进来就 connection refused。
	// （EnsureControlToken：别的用户停这个懒起的 daemon 也得有令牌可用）
	if daemon.Running() == nil {
		// 写不出去只警告不拦：用户此刻要的是「把客户端拉起来」，为一行令牌
		// 拒绝启动更糟。但后果要说清楚——那个 daemon 没法被别人停。
		if _, err := store.EnsureControlToken(); err != nil {
			fmt.Fprintf(os.Stderr, "newgate: %s\n", i18n.T(
				"Cannot write the control token (a cross-user newgate stop will not work): {err}",
				i18n.A{"err": err.Error()}))
		}
		fmt.Fprintf(os.Stderr, "newgate: proxy not running, starting…\n")
		if _, err := daemon.Spawn(st.Port); err != nil {
			fmt.Fprintf(os.Stderr, "newgate: failed to start proxy: %v\n"+
				"  (NEWGATE_DISABLE=1 bypasses newgate entirely)\n", err)
			return 69
		}
		waitProxy(st.Port)
	}

	// 解析本次的链头：命令行 --profile > NEWGATE_PROFILE/PRESET > agent 默认。
	explicit := o.Profile
	if explicit == "" {
		explicit = firstNonEmpty(os.Getenv("NEWGATE_PROFILE"), os.Getenv("NEWGATE_PRESET"))
	}
	active := explicit
	if active == "" {
		active = st.ActiveFor(a.ID)
	}

	inject, warns := buildInject(a, facts, st, active, explicit)
	for _, w := range warns {
		fmt.Fprintf(os.Stderr, "newgate: %s\n", w)
	}
	inject["NEWGATE_DEPTH"] = strconv.Itoa(depth + 1)
	return execReal(a, args, inject)
}

// buildInject 算出要叠加给子进程的整组 env（NEWGATE_DEPTH 除外）。
//
// facts 是这个客户端交上来的**运行时事实**（见 agentapi.AgentFacts），由调用方从
// 目录端口取来注入——本函数不认识它是怎么算出来的。nil = 槽位全走描述符里的缺省。
// 单独成函数是为了可测：只依赖读盘快照和入参，不 exec、不碰 daemon。
//
// 槽位命名有两种模式（2026-09-09 定稿）：
//
//   - 动态（默认）：槽位保持**档位名**（heavy/mid/…）。会话把档位名发
//     回来，代理按**请求时**的当前配置解析——`newgate --set-profile`
//     对跑着的会话立刻生效。界面显示的是语义层名字，实际模型看
//     X-Newgate-Route / `newgate tier`，两边各自为真、互不冒充。
//   - 钉死（显式 --profile / NEWGATE_PROFILE / argv0 分发）：注入**真实
//     模型名**。本次调用已被用户钉在这个 profile 上，显示的就是这次
//     真用的——钉死才配得上真名。
//
// 窗口声明两条模式都注入：窗口 env 走客户端的「未知模型」分支（档位名和真实名
// 对它都是未知，效果相同），compact env 无条件优先级最高——窗口修复不依赖命名模式。
// **两个变量的名字来自 agent 定义**（`ContextWindowEnv` / `AutoCompactEnv`）：
// 那是客户端的知识，内核不认识任何一家的变量名（2026-09-20 之前这里是写死的）。
// 返回的第二项是「本该注入但没注入成」的话，由调用方打到 stderr。读配置失败
// 不影响启动（fail-open：代理仍能按档位名路由），但它会让这次调用少注入真实
// 模型名与窗口声明——后者不给的话 Claude Code 会按 200k 假设提前 compact，是
// 用户能直接感觉到的行为变化。所以不能一声不响（同文件上面那条控制令牌的写法）。
func buildInject(a *agentapi.Agent, facts agentapi.AgentFacts, st *domain.State, active, explicit string) (map[string]string, []string) {
	// 槽位默认 = 档位名（动态模式）。facts 是这个客户端交上来的运行时事实
	// （用户改过的槽位映射住在它那儿），nil = 全用描述符里的缺省。
	inject := a.BuildEnv(st.Port, "newgate-local", facts)
	var warns []string

	if snap, err := store.Load(); err != nil {
		warns = append(warns, i18n.T(
			"cannot read the config, so this run injects no real model names and no window declaration (the proxy still routes by tier name): {err}",
			i18n.A{"err": err}))
	} else {
		// 钉死模式：槽位换成该 profile 链头的真实模型名。解析不出就保留
		// 档位名（fail-open，docs/05-gateway.md）：代理仍能按档位路由。
		// 两者都过 Slot.Apply——客户端的能力标记（`[1m]` 那类）必须跟着走，
		// 否则钉死模式恰好把它丢了，而这正是用户点名要的那条路（--profile=xx）。
		if explicit != "" {
			for _, s := range a.EnvSlots() {
				if b, ok := resolve.PrimaryBinding(agentapi.TierOf(facts, s), snap.Profiles, snap.Providers, active); ok {
					inject[s.EnvVar] = s.Apply(b.Model)
				}
			}
		}

		// 主力模型的真实窗口（profile 里声明）。不给 Claude Code 声明的
		// 话，它对不认识的模型名（档位名或真实名都一样）按 200k 窗口假设
		// 提前 compact（2026-09 实测）。两个 env 都只在得到 >0 的数时注入；
		// 客户端可以在描述符里封顶（ContextWindowCap / AutoCompactCap），
		// 那时窗口即便 profile 没声明也用上限值——缺省的 200k 假设更小，
		// 而这台客户端明确接受更大的窗口。其他 agent（如 opencode）不认识
		// 这些变量，忽略之，无害。
		//
		// 这两个是**数字**，所以永远不过 Slot.Apply：把客户端的模型名标记
		// 拼到窗口上会写进去一个解析不了的值，而失败的样子是「窗口声明静默
		// 失效、又回到 200k 假设」——看起来像这条注入根本不存在。
		for _, p := range snap.Profiles {
			if p.Name != active {
				continue
			}
			// 客户端的封顶（见 agentapi.Agent.ContextWindowCap）。profile
			// 声明得更大时**说一声**——把用户要的 1M 悄悄换成 512K，正是
			// 「配置看着对、行为不一样」那一类最难归因的故障。
			window := p.ContextWindow
			if a.ContextWindowCap > 0 && (window == 0 || window > a.ContextWindowCap) {
				if window > a.ContextWindowCap {
					warns = append(warns, i18n.T(
						"profile {name} declares a {window}-token context window; {client} is capped at {cap} so a compaction request still fits under the upstream limit",
						i18n.A{"name": p.Name, "window": window, "client": a.ID, "cap": a.ContextWindowCap}))
				}
				window = a.ContextWindowCap
			}
			if window > 0 && a.ContextWindowEnv != "" {
				inject[a.ContextWindowEnv] = strconv.Itoa(window)
			}
			compact := p.AutoCompactWindow
			if a.AutoCompactCap > 0 && compact > a.AutoCompactCap {
				compact = a.AutoCompactCap
			}
			if compact > 0 && a.AutoCompactEnv != "" {
				inject[a.AutoCompactEnv] = strconv.Itoa(compact)
			}
			break
		}
	}

	// per-invocation 覆盖：把 profile 编码进 base URL 路径（/p/<profile>）。
	// 代理从路径读出覆盖，完全不改全局状态（docs/07-clients-runtime.md）。
	if explicit != "" && a.BaseURLEnv != "" {
		inject[a.BaseURLEnv] = fmt.Sprintf("http://127.0.0.1:%d/a/%s/p/%s", st.Port, a.ID, active)
	}
	return inject, warns
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// execReal 找到真实可执行文件并用注入后的 env 覆盖进程。
func execReal(a *agentapi.Agent, args []string, inject map[string]string) int {
	real, err := a.FindReal(injection.Dir())
	if err != nil {
		fmt.Fprintf(os.Stderr, "newgate: %v\n", err)
		return 69
	}

	// 受控的 env 白名单：继承 → 丢掉干扰项 → 叠加我们的。
	drop := map[string]bool{}
	for _, k := range a.UnsetEnv {
		drop[k] = true
	}
	var env []string
	for _, kv := range os.Environ() {
		k := kv
		if i := indexByte(kv, '='); i >= 0 {
			k = kv[:i]
		}
		if drop[k] {
			continue
		}
		if _, overridden := inject[k]; overridden {
			continue
		}
		env = append(env, kv)
	}
	for k, v := range inject {
		env = append(env, k+"="+v)
	}

	execArgs := append([]string{real}, args...)
	if err := syscall.Exec(real, execArgs, env); err != nil {
		// 没有 shebang 的脚本（例如 npm 的 claude.exe 兜底脚本、或 CRLF
		// 行尾损坏的 shebang）会让内核返回 ENOEXEC（exec format error）。
		// 交给 /bin/sh 执行，让它打印脚本自己那清晰明了的错误信息，
		// 而不是给用户一个神秘的 "exec format error"。
		if errors.Is(err, syscall.ENOEXEC) {
			shArgs := append([]string{"/bin/sh", real}, args...)
			if err2 := syscall.Exec("/bin/sh", shArgs, env); err2 != nil {
				fmt.Fprintf(os.Stderr, "newgate: %s\n", i18n.T(
					"running {file} through /bin/sh failed too: {err}",
					i18n.A{"file": real, "err": err2}))
				return 70
			}
			return 0 // 不会走到这里：Exec 成功则不返回
		}
		fmt.Fprintf(os.Stderr, "newgate: exec %s failed: %v\n", real, err)
		return 70
	}
	return 0 // 不会走到这里：Exec 成功则不返回
}

func indexByte(s string, b byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == b {
			return i
		}
	}
	return -1
}

// waitProxy 轮询代理直到它响应，或约 3s 后放弃。
func waitProxy(port int) {
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if tcpAlive(port) {
			return
		}
		time.Sleep(40 * time.Millisecond)
	}
}

func tcpAlive(port int) bool { return httpx.TCPAlive("127.0.0.1", port, 500*time.Millisecond) }
