package setup

// 本文件是 `newgate config` 的**主线**：铺好的那几屏按顺序串起来，以及每一步
// 「要不要做」的判断。
//
// 顺序是有理由的：
//
//	1 选客户端 —— 先问「把谁接过来」。它不需要任何配置，所以即使这台机器上
//	   什么都没有，这一屏也成立（它同时是安装清单）。
//	2 填第一个上游 —— 一个 base、一个 key、一个模型名。这是**唯一的门槛**：
//	   没有它，代理转发每一发都是 401。
//	3 落盘 —— providers.json + mappings/default.kv + state.json。
//	4 装 + 接管 —— 对第 1 步选中的每一个：没装就装、装上了就接管。
//	5 起代理 —— 让上面那些立刻能用。
//	6 一屏收尾 —— 改了什么、下一步敲什么。
//
// 第 4 步在**第 3 步之后**，因为接管时写的值（模型名、窗口声明）全来自这一刻的
// 配置：先接管再写配置，接管里那些「此刻该走哪一档」会算在一份还不存在的链上。
//
// # 已经配置好的人重跑它
//
// 他们多半只是想装/接管一个新的客户端。所以第 2、3 步默认**跳过**（已经有一份
// 能用的全局 profile 时），用 `--reconfigure` 才重来。这不是「少问一句」的省事：
// 一个能跑的人被问到「base URL」时，最可能的结果是他直接 Ctrl-C，而那时他想要的
// 那件事（装个 codex）一件都没做。

import (
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/rzbdz/newgate-ext/lib/style"
	term "github.com/rzbdz/newgate-ext/lib/term"
	"github.com/rzbdz/newgate-ext/modules/config/store"
	agentapi "github.com/rzbdz/newgate-ext/modules/confighook"
	runtimeapi "github.com/rzbdz/newgate-ext/modules/runtime"
	"github.com/rzbdz/newgate/lib/i18n"
)

// Deps 是向导用得上的全部端口。缺一个都做不完这件事，所以它们是必填的。
//
// 全是**内核的公开端口**（confighook 的目录、runtime 的接管与生命周期），没有
// 一个来自内核的内部实现包——发行版的模块对外依赖只走这条缝（见 CLAUDE.md §2）。
type Deps struct {
	Agents  agentapi.AgentCatalog
	Control runtimeapi.Control
	Out     io.Writer
}

// Options 是这次调用的开关。
type Options struct {
	// Reconfigure 重问第一份上游（默认只在「还没有可用配置」时才问）。
	Reconfigure bool
	// Yes 不问「要不要写」，直接按默认走（给脚本与 e2e 用）。
	Yes bool
	// NoStart 不代起代理（`newgate config --no-start`）。
	NoStart bool
}

// Run 是一次完整的一把梭。
func Run(d Deps, opt Options) int {
	if d.Out == nil {
		d.Out = os.Stdout
	}
	fmt.Println() // 与 shell 提示符隔开一行

	t, err := term.Open()
	if err != nil {
		return noTTY(d, opt, err)
	}
	defer t.Close()
	fmt.Print(hideC)
	defer fmt.Print(showC + reset)
	return runWithScreen(d.Out, d, opt, &screen{T: t})
}

// runWithScreen 是不带终端的那一半：所有分支都在这里，屏幕由调用方给。
//
// 分开是为了让测试能脚本化按键（见 setup_test.go）。它同时是一道纪律：**向导
// 里没有任何一处直接读按键**——全部经 Screen，所以「用户按了什么」这件事只有
// 一个入口，测试与真终端走同一条路。
func runWithScreen(w io.Writer, d Deps, opt Options, sc Screen) int {
	fmt.Println() // 与 shell 提示符隔开一行
	existing := Inspect()

	// ---- 1. 选客户端 ----
	controlled := map[string]bool{}
	for _, st := range d.Control.TakeoverStatus() {
		if st.Active {
			controlled[st.Agent] = true
		}
	}
	pick := pickAgents(sc, d.Agents, func(id string) bool { return controlled[id] })
	if pick.Cancel {
		fmt.Print(showC + reset)
		fmt.Println(i18n.T("Nothing was changed.", nil))
		return 0
	}
	if pick.Nothing {
		fmt.Print(showC + reset)
		fmt.Println(i18n.T("No clients selected — nothing was changed.", nil))
		return 0
	}

	// ---- 2. 第一份上游 ----
	var prof ApplyResult
	wantProfile := opt.Reconfigure || !existing.HasUsable
	if wantProfile {
		boot, cancel := askUpstream(sc, opt)
		if cancel {
			fmt.Print(showC + reset)
			fmt.Println(i18n.T("Nothing was changed.", nil))
			return 0
		}
		var err error
		prof, err = Apply(boot, Profile{WriteProfile: true, SetDefault: true, Overwrite: existing.HasProfile}, prof)
		if err != nil {
			fmt.Print(showC + reset + "\n")
			fmt.Fprintln(os.Stderr, style.Mark(style.Bad)+" "+i18n.T("cannot write the configuration: {err}",
				i18n.A{"err": err.Error()}))
			return 70
		}
	}

	fmt.Print(showC + reset + "\n")

	// ---- 4. 装 + 接管 ----
	fmt.Println(style.Bold(" " + i18n.T("Clients", nil)))
	port := storePort()
	steps := EnsureAgents(d.Agents, d.Control, pick.Chosen, port, d.Out)
	Report(d.Out, steps)

	// ---- 5. 起代理 ----
	report := StartReporter{}
	if !opt.NoStart {
		// SkipTakeover：接管**已经在上面按用户选的**做完（EnsureAgents）。这里
		// 再全面接管一次会把多选那一屏没勾的那些也接过来——与那一屏写着的话
		// 正相反。
		rep, err := d.Control.Start(runtimeapi.StartOptions{Quiet: true, SkipTakeover: true})
		if err != nil {
			report.Warnings = append(report.Warnings, i18n.T("the proxy did not start: {err}",
				i18n.A{"err": err.Error()}))
			for _, p := range rep.Problems {
				report.Warnings = append(report.Warnings, p)
			}
		} else if rep.Running {
			report.ProxyLine = i18n.T("proxy running · 127.0.0.1:{port} · profile {profile}",
				i18n.A{"port": rep.Port, "profile": rep.Profile})
		}
	}

	// ---- 6. 收尾 ----
	report.Next = nextSteps(steps)
	var b strings.Builder
	printOutcome(&b, prof, steps, report)
	fmt.Fprint(w, b.String())
	return 0
}

// askUpstream 是「一个 base、一个 key、一个模型」那一页，以及随后的模型清单。
func askUpstream(s Screen, opt Options) (Bootstrap, bool) {
	var fields []field
	fields = []field{
		{
			Label:       i18n.T("Base URL", nil),
			Placeholder: "https://api.example.com/v1",
			Note:        i18n.T("the endpoint your keys belong to; OpenAI-compatible gateways (newapi, one-api, DeepSeek, GLM …) all work", nil),
			Validate: func(v string) string {
				if !strings.HasPrefix(v, "http://") && !strings.HasPrefix(v, "https://") {
					return i18n.T("a base URL starts with http:// or https://", nil)
				}
				return ""
			},
		},
		{
			Label:  i18n.T("API key", nil),
			Secret: true,
			Note:   i18n.T("stored in providers.json (0660); you can move it to an environment variable later", nil),
		},
		{
			Label:       i18n.T("Name for this provider", nil),
			Placeholder: i18n.T("derived from the host, editable", nil),
			Note:        i18n.T("how it shows up in newgate; letters, digits, - and .", nil),
			// 建议值在拿到 base 之后的那一格里算（按 Tab / 直接回车采用）。
			Suggest: func(cur string) (string, string) {
				if strings.TrimSpace(cur) != "" {
					return "", ""
				}
				return SuggestedProvider(fields[0].Value, nil), i18n.T("press enter to accept, or type another name", nil)
			},
		},
	}
	if opt.Yes {
		// --yes：能从 base 推出来的那一格不问（provider 名只是界面上的标签）。
		// 它给脚本/e2e 用，少敲一次回车；其余两格仍然是问的——base 与 key
		// 没有合理的默认值，替用户猜一个只会让他以为配好了。
		fields = fields[:2]
	}
	r := form(s, i18n.T("first setup", nil),
		i18n.T("One upstream is enough to bootstrap: every tier will use it until you split them up.", nil), fields)
	if r.Cancel {
		return Bootstrap{}, true
	}
	base, key, name := r.Values[0], r.Values[1], ""
	if len(r.Values) > 2 {
		name = r.Values[2]
	}
	if name == "" {
		name = SuggestedProvider(base, nil)
	}

	// 问一次模型清单：用户只要再挑一个，而不是把模型名背下来。
	models, why := probeModels(normalizeBase(base), key, "openai")
	model, cancel := pickModel(s, models, why)
	if cancel {
		return Bootstrap{}, true
	}
	return Bootstrap{
		Provider: name,
		BaseURL:  base,
		APIKey:   key,
		Protocol: "openai",
		Model:    model,
	}, false
}

// nextSteps 是收尾时「接下来做什么」的那几条，按这次实际做了什么算。
func nextSteps(steps []Step) []string {
	var out []string
	var ready []string
	for _, s := range steps {
		if s.Err == nil && s.Takeover.Err == nil && s.Installed {
			ready = append(ready, s.Agent)
		}
	}
	sort.Strings(ready)
	if len(ready) > 0 {
		out = append(out, i18n.T("start an agent: {cmd}", i18n.A{"cmd": "newgate " + ready[0]}))
		out = append(out, i18n.T("or just `{bin}` — the PATH shim sends it through newgate", i18n.A{"bin": ready[0]}))
	}
	// 装在这一次、但配置文件还没出现的客户端：先把它们跑一次，再接管一次。
	// 这句话是收尾里最要紧的一条——不然用户在 codex 里发请求是直连，而他以为
	// 自己刚刚配好了。
	var pending []string
	for _, s := range steps {
		if s.Err == nil && s.Takeover.Err == nil && s.Installed && !s.TakenOver {
			pending = append(pending, s.Agent)
		}
	}
	sort.Strings(pending)
	for _, id := range pending {
		out = append(out, i18n.T("run {bin} once so it writes its config, then: newgate on {agent}",
			i18n.A{"bin": id, "agent": id}))
	}
	out = append(out, i18n.T("first request a client sends will be routed by tier; watch it with `newgate logs -f`", nil))
	out = append(out, i18n.T("have the agent do the rest: it can read {file} and {dir}",
		i18n.A{"file": "~/.config/newgate/providers.json", "dir": "~/.config/newgate/mappings"}))
	return out
}

// noTTY 是没有终端时的那条路：不装作能交互，而是逐条告诉人要敲什么。
//
// 它必须**真的不写任何东西**：一个非交互环境里的 `newgate config` 大概率是
// 某个脚本/CI 敲的，那时静默改配置是最糟的结果。
func noTTY(d Deps, opt Options, err error) int {
	fmt.Fprintln(os.Stderr, style.Mark(style.Bad)+" "+i18n.T(
		"newgate config needs an interactive terminal ({err})", i18n.A{"err": err.Error()}))
	fmt.Fprintln(os.Stderr)
	fmt.Fprintln(os.Stderr, i18n.T("Without one, do the same steps by hand:", nil))
	fmt.Fprintln(os.Stderr, "  1. "+i18n.T("fill in {file} (base_url, api_key, models)", i18n.A{"file": "~/.config/newgate/providers.json"}))
	fmt.Fprintln(os.Stderr, "  2. "+i18n.T("point tiers at it in {dir}/*.kv", i18n.A{"dir": "~/.config/newgate/mappings"}))
	fmt.Fprintln(os.Stderr, "  3. "+i18n.T("take clients over: {cmd}", i18n.A{"cmd": "newgate on <agent>"}))
	fmt.Fprintln(os.Stderr, "  4. "+i18n.T("start the proxy: {cmd}", i18n.A{"cmd": "newgate start"}))
	return 64
}

// screen 把 *term.Terminal 包成向导用的那两件事。
type screen struct{ T *term.Terminal }

func (s *screen) ReadEvent() term.Event { return s.T.ReadEvent() }
func (s *screen) ReadLine(m term.LineMode, r func(string, string, int)) term.LineResult {
	return s.T.ReadLine(m, r)
}

// storePort 读此刻配置里的端口（`newgate init` 写的是 8899；有人改过就跟着走）。
func storePort() int {
	return portOf(store.LoadState().Port)
}
