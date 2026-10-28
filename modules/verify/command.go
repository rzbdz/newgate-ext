package verify

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	i18n "github.com/rzbdz/newgate/lib/i18n"
	"github.com/rzbdz/newgate/lib/style"
	cliapi "github.com/rzbdz/newgate/modules/cli/extension"
	"github.com/rzbdz/newgate/modules/config/targets"
)

// 本文件是 `newgate verify [profile]`：把一份（或全部）profile 的每条 binding
// 真打一轮指纹，报出「参考库说这是谁」以及与 binding 上名字的关系。
//
// 与 probe 的分工：probe 回答「这家通不通、说哪种方言」，verify 回答「通了之后
// 对面到底是谁」。两者共用 config/targets 那份 profile 解析（见那边的注释），
// 所以「哪些 profile、哪些档位、去重之后是哪几个上游」只有一份知识。
//
// 版式沿用 probe：进度走 stderr（stdout 留给结果表，`verify > f` 拿到干净的表），
// `--json` 时 stdout 只有 JSON。

type verifyCommand struct{ svc *service }

var (
	_ cliapi.Command    = (*verifyCommand)(nil)
	_ cliapi.Documented = (*verifyCommand)(nil)
	_ cliapi.Unstyled   = (*verifyCommand)(nil)
)

func (verifyCommand) Names() []string { return []string{"verify"} }

func (verifyCommand) Unstyled(args []string) bool {
	return cliapi.Flag(args, "--json") || cliapi.FlagValue(args, "--json") != ""
}

func (verifyCommand) Help() cliapi.HelpLine {
	return cliapi.HelpLine{
		Section: cliapi.SectionObserve, Rank: 41,
		Usage:   "verify [profile] [--provider <name> --model <model>] [--api auto|responses|chat_completions|messages] [--count 1-3] [--json]",
		Summary: i18n.T("fingerprint each binding and report which model the upstream really serves", nil),
	}
}

func (c verifyCommand) Run(host cliapi.Host, args []string) int {
	return runVerify(host, c.svc, args)
}

// rowOut 是一行结果：配置视图 + 它指向的那条指纹。
//
// 同一条 binding 被多个 profile 共用时，指纹只打一次、每一行引用同一份结论
// （与 probe 的 Cached 同一条：省时间也省额度）。
type rowOut struct {
	Profile  string `json:"profile,omitempty"`
	Role     string `json:"role,omitempty"`
	Provider string `json:"provider"`
	Model    string `json:"model"`
	// ConfigErr 是配置本身的问题（这一档没绑、provider 没声明）。
	ConfigErr string `json:"config_error,omitempty"`
	// RunErr 是这次请求根本没打成（没有 key、参考库加载失败）。
	RunErr string `json:"error,omitempty"`
	Entry  *Entry `json:"fingerprint,omitempty"`
}

type verifyOutcome struct {
	entry *Entry
	err   error
}

func runVerify(host cliapi.Host, svc *service, args []string) int {
	// `--help` 必须在这里拦住。命令框架只把 `help` 当成一个动词，`verify --help`
	// 会把 `--help` 当成本命令的一个 flag 交进来——而这个命令的默认行为是
	// **把全部 profile 真打一遍**（会花 token、几十秒）。2026-10-28 实测：
	// 敲 `verify --help` 直接开始全局验证，直到被 Ctrl-C 才停。
	if cliapi.Flag(args, "--help") || cliapi.Flag(args, "-h") {
		h := verifyCommand{}.Help()
		fmt.Println("usage: newgate " + h.Usage)
		fmt.Println(h.Summary)
		return 0
	}
	asJSON := cliapi.Flag(args, "--json") || cliapi.FlagValue(args, "--json") != ""
	quiet := asJSON
	only := profileArg(args)
	provider := cliapi.FlagValue(args, "--provider")
	model := cliapi.FlagValue(args, "--model")
	api := cliapi.FlagValue(args, "--api")
	if api == "" {
		api = "auto"
	}
	count := 3
	if raw := cliapi.FlagValue(args, "--count"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > 3 {
			return host.Die(64, i18n.T("--count must be 1, 2 or 3", nil))
		}
		count = n
	}
	if (provider == "") != (model == "") {
		return host.Die(64, i18n.T("give both --provider and --model, or neither", nil))
	}
	if provider != "" && only != "" {
		return host.Die(64, i18n.T("give either a profile or --provider/--model, not both", nil))
	}

	var rows []targets.Row
	var uniq []targets.Target
	if provider != "" {
		uniq = []targets.Target{{Provider: provider, Model: model}}
	} else {
		var err error
		rows, uniq, err = targets.Enumerate(targets.Options{Only: only})
		if err != nil {
			return host.Die(65, err.Error())
		}
	}

	started := time.Now()
	head := i18n.T("all profiles", nil)
	switch {
	case provider != "":
		head = provider + "/" + model
	case only != "":
		head = "profile " + only
	}
	if !quiet {
		fmt.Fprintf(os.Stderr, "%s\n", i18n.T(
			"verifying {n} targets (concurrency {c}; each target asks for {count} challenges)",
			i18n.A{"n": len(uniq), "c": 4, "count": count}))
		if !svc.cache.dataReady() {
			fmt.Fprintf(os.Stderr, "%s\n", style.Dim(i18n.T(
				"the first run downloads about 3.6 MB of reference data and caches it under {dir}",
				i18n.A{"dir": "~/.config/newgate/fingerprints"})))
		}
		fmt.Fprintln(os.Stderr)
	}

	var mu sync.Mutex
	got := map[targets.Target]verifyOutcome{}
	sem := make(chan struct{}, 4)
	var wg sync.WaitGroup
	for _, t := range uniq {
		wg.Add(1)
		go func(t targets.Target) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			e, err := svc.Run(context.Background(), RunRequest{
				Provider: t.Provider, Model: t.Model, API: api, Count: count,
			})
			mu.Lock()
			got[t] = verifyOutcome{entry: e, err: err}
			mu.Unlock()
			if !quiet {
				fmt.Fprintln(os.Stderr, progressLine(t, e, err))
			}
		}(t)
	}
	wg.Wait()

	// 把每条配置行绑到它的指纹上。
	out := make([]rowOut, 0, len(rows)+1)
	if provider != "" {
		o := rowOut{Provider: provider, Model: model}
		o.Entry, o.RunErr = outcomeOf(got, o.Provider, o.Model)
		out = append(out, o)
	} else {
		for _, r := range rows {
			// 稀疏 profile 里「这一档没绑」是正常形态；这一屏要的是**模型**，
			// 把一堆 not bound 摊进表里只会让真正要看的行沉下去（与界面
			// 那边 groupProfiles 同一条）。
			if r.Provider == "" && r.Model == "" {
				continue
			}
			o := rowOut{
				Profile: r.Profile, Role: r.Role, Provider: r.Provider, Model: r.Model,
				ConfigErr: r.Err,
			}
			if r.Provider != "" && r.Model != "" {
				o.Entry, o.RunErr = outcomeOf(got, o.Provider, o.Model)
			}
			out = append(out, o)
		}
	}

	failed := 0
	for _, o := range got {
		if o.err != nil || o.entry == nil || o.entry.Error != "" {
			failed++
		}
	}

	if asJSON {
		b, _ := json.MarshalIndent(out, "", "  ")
		fmt.Println(string(b))
		if failed > 0 {
			return 1
		}
		return 0
	}
	if len(out) == 0 {
		fmt.Println(style.Hint(i18n.T(
			"no bound models to verify — this profile only inherits or falls through", nil)))
		return 0
	}

	printVerifyTable(head, out, uniq, got, api, count, started)
	if failed > 0 {
		return 1
	}
	return 0
}

func outcomeOf(got map[targets.Target]verifyOutcome, provider, model string) (*Entry, string) {
	o := got[targets.Target{Provider: provider, Model: model}]
	msg := ""
	if o.err != nil {
		msg = firstLine(o.err.Error())
	}
	return o.entry, msg
}

// profileArg 取第一个**真正的**位置参数。
//
// 不能直接用 cliapi.Positional：它只认「以 - 开头的是 flag」，不知道
// `--provider vendor-model` 里的 `vendor-model` 是那个 flag 的**值**。2026-10-28
// 实测：`verify --provider vendor-model --model some-model` 被解析成「profile
// 是 vendor-model」，于是报「档位文件和 --provider/--model 只能给一边」。
// 同一条也会让 `verify --count 1` 去验证一份叫 `1` 的 profile。
//
// 这里只跳过本命令认识的、带值的 flag；`--json` 这类布尔 flag 由「以 - 开头」
// 自然跳过。`--` 之后一律算位置参数（与 cliapi.Positional 同一条惯例）。
func profileArg(args []string) string {
	takesValue := map[string]bool{
		"--provider": true, "--model": true, "--api": true, "--count": true,
	}
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			if i+1 < len(args) {
				return args[i+1]
			}
			return ""
		}
		if takesValue[a] {
			i++ // 跳过它的值，值本身不是位置参数
			continue
		}
		if len(a) > 1 && a[0] == '-' {
			continue
		}
		return a
	}
	return ""
}

func progressLine(t targets.Target, e *Entry, err error) string {
	switch {
	case err != nil:
		return fmt.Sprintf("%s %s   %s", style.Mark(style.Bad), t, firstLine(err.Error()))
	case e == nil:
		return fmt.Sprintf("%s %s   %s", style.Mark(style.Bad), t, i18n.T("no result", nil))
	case e.Error != "":
		return fmt.Sprintf("%s %s   %s", style.Mark(style.Bad), t, firstLine(e.Error))
	default:
		return fmt.Sprintf("%s %s   %s  %s", style.Mark(style.OK), t,
			fingerprintLabel(e), matchLabel(e.Match))
	}
}

func printVerifyTable(head string, out []rowOut, uniq []targets.Target,
	got map[targets.Target]verifyOutcome, api string, count int, started time.Time) {
	fmt.Println(style.Title("newgate verify", i18n.N(
		"{head} · {n} target · {elapsed}", "{head} · {n} targets · {elapsed}",
		len(uniq), i18n.A{"head": head, "elapsed": time.Since(started).Round(time.Millisecond),
			"n": len(uniq)})))
	fmt.Println(style.Rule(72))

	t := style.NewTable(i18n.T("profile", nil), i18n.T("tier", nil),
		i18n.T("upstream/model", nil), i18n.T("fingerprint", nil),
		i18n.T("name match", nil), i18n.T("probability", nil))
	last := ""
	for _, r := range out {
		p := r.Profile
		if p == last {
			p = ""
		} else {
			last = r.Profile
		}
		fp, match, prob := "—", "—", "—"
		if r.Entry != nil {
			fp = fingerprintLabel(r.Entry)
			match = matchLabel(r.Entry.Match)
			if r.Entry.Probability != nil {
				prob = fmt.Sprintf("%.0f%%", *r.Entry.Probability*100)
			}
		}
		if r.ConfigErr != "" {
			fp, match = style.Dim(r.ConfigErr), style.Dim("—")
		}
		t.Row(p, r.Role, r.Provider+"/"+r.Model, fp, match, prob)
	}
	fmt.Print(t.String())

	// 警告：失配/同族/没有排名/请求失败——**每一条只说一次**（同一条 binding
	// 可能挂在好几个 profile 下，重复四遍只会让人跳过它们）。
	var warnings []string
	seen := map[targets.Target]bool{}
	for _, r := range out {
		if r.Provider == "" || r.Model == "" {
			continue
		}
		tg := targets.Target{Provider: r.Provider, Model: r.Model}
		if seen[tg] {
			continue
		}
		seen[tg] = true
		e := got[tg].entry
		err := got[tg].err
		switch {
		case err != nil:
			warnings = append(warnings, style.Item(style.Bad, i18n.T("{target}: {err}",
				i18n.A{"target": tg, "err": firstLine(err.Error())})))
		case e == nil:
			warnings = append(warnings, style.Item(style.Bad, i18n.T("{target}: no result",
				i18n.A{"target": tg})))
		case e.Error != "" && e.Predicted == "":
			warnings = append(warnings, style.Item(style.Bad, i18n.T("{target}: {err}",
				i18n.A{"target": tg, "err": firstLine(e.Error)})))
		case e.Match.Warning():
			warnings = append(warnings, style.Item(style.Warn, i18n.T(
				"{target}: fingerprint says {prediction}, but the binding asks for {model} ({match})",
				i18n.A{"target": tg, "prediction": fingerprintLabel(e),
					"model": e.Model, "match": matchLabel(e.Match)})))
		}
		if e != nil && e.CacheError != "" {
			warnings = append(warnings, style.Item(style.Warn, i18n.T(
				"{target}: verified, but the result could not be cached: {err}",
				i18n.A{"target": tg, "err": firstLine(e.CacheError)})))
		}
	}
	if len(warnings) > 0 {
		fmt.Println()
		for _, w := range warnings {
			fmt.Println(w)
		}
	}

	// 汇总。
	var matched, dated, family, mismatch, unknown, bad int
	for _, o := range got {
		switch {
		case o.err != nil || o.entry == nil || o.entry.Error != "":
			bad++
		default:
			switch o.entry.Match {
			case MatchOK:
				matched++
			case MatchDated:
				dated++
			case MatchFamily:
				family++
			case MatchMismatch:
				mismatch++
			default:
				unknown++
			}
		}
	}
	fmt.Println()
	fmt.Println(style.Section(i18n.T("summary", nil)))
	fmt.Println(style.Bullet(i18n.T(
		"{n} matched · {dated} dated snapshot · {family} same family · {mismatch} different model · {unknown} no ranking · {bad} failed",
		i18n.A{"n": matched, "dated": dated, "family": family,
			"mismatch": mismatch, "unknown": unknown, "bad": bad})))
	fmt.Println(style.Hint(i18n.T(
		"fingerprints are evidence, not proof: fix the request parameters and repeat when it matters", nil)))
	if api != "auto" {
		fmt.Println(style.Hint(i18n.T("api: {api} · challenges per target: {count}",
			i18n.A{"api": api, "count": count})))
	} else {
		fmt.Println(style.Hint(i18n.T(
			"api: chosen from each provider's protocol · challenges per target: {count}",
			i18n.A{"count": count})))
	}
}

func fingerprintLabel(e *Entry) string {
	if e == nil {
		return "—"
	}
	switch {
	case e.Predicted != "" && e.PredictedName != "" && e.PredictedName != e.Predicted:
		return e.PredictedName + " (" + e.Predicted + ")"
	case e.Predicted != "":
		return e.Predicted
	case e.Error != "":
		return style.Dim(i18n.T("no ranking", nil))
	}
	return "—"
}

func matchLabel(m Match) string {
	switch m {
	case MatchOK:
		return style.Green(i18n.T("match", nil))
	case MatchDated:
		return style.Green(i18n.T("dated snapshot", nil))
	case MatchFamily:
		return style.Yellow(i18n.T("same family", nil))
	case MatchMismatch:
		return style.Yellow(i18n.T("different model", nil))
	case MatchUnknown:
		return style.Red(i18n.T("no ranking", nil))
	case MatchError:
		return style.Red(i18n.T("error", nil))
	}
	return string(m)
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
