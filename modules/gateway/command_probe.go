package gateway

// 本文件是 `newgate probe`：对候选真打一发最小请求，拿到连通性 + 方言能力 +
// 上游毛病，并把结果回传给守护进程更新熔断表。
//
// **为什么它住在这里**（2026-09-18）：探活打的就是网关自己的转发路径，探明的
// 方言能力写进 gateway/dialect，健康结果回填给熔断表——三件事都是数据面的语义。
// 它留在界面时，界面得 import gateway/{probe,dialect}，于是「界面认识探针」这件
// 事被焊死在依赖里。搬过来之后界面既不认识 probe 也不认识 dialect。
//
// 与 metrics 同一条理由、同一个方向：gateway → cli/extension（契约叶子），注入
// 自己的命令，没有回边。

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/rzbdz/newgate-ext/lib/style"
	cliapi "github.com/rzbdz/newgate-ext/modules/cli/extension"
	"github.com/rzbdz/newgate-ext/modules/config/store"
	"github.com/rzbdz/newgate-ext/modules/gateway/controlpath"
	"github.com/rzbdz/newgate-ext/modules/gateway/controlplane"
	"github.com/rzbdz/newgate-ext/modules/gateway/dialect"
	"github.com/rzbdz/newgate-ext/modules/gateway/probe"
	i18n "github.com/rzbdz/newgate/lib/i18n"
)

type probeCommand struct{}

var (
	_ cliapi.Command    = (*probeCommand)(nil)
	_ cliapi.Documented = (*probeCommand)(nil)
	_ cliapi.Unstyled   = (*probeCommand)(nil)
)

func (probeCommand) Names() []string { return []string{"probe"} }

// Unstyled：`probe --json` 给的是机器可读的原文，不是给人看的版式。
func (probeCommand) Unstyled(args []string) bool {
	return cliapi.FlagValue(args, "--json") != "" || cliapi.Flag(args, "--json")
}

func (probeCommand) Help() cliapi.HelpLine {
	return cliapi.HelpLine{Section: cliapi.SectionObserve, Rank: rankObserve,
		Usage: "probe [profile]", Summary: i18n.T("send real requests to candidates and report their health", nil)}
}

func (probeCommand) Run(host cliapi.Host, args []string) int {
	return runProbe(host, cliapi.Positional(args, 0), cliapi.Flag(args, "--json"))
}

// cmdProbe 主动探活：对每个候选真打一发最小请求。
//
// 版式：进度走 stderr（stdout 留给结果表，`probe > f` 拿到的是干净的表），
// 结果一段三块——明细表 / 方言能力 / 汇总。以前进度和结果都往 stdout 里
// 混着写，重定向之后是一堆进度行夹着表格。
func runProbe(host cliapi.Host, only string, asJSON bool) int {
	quiet := asJSON // JSON 模式不打进度，免得污染输出
	started := time.Now()
	st := store.LoadState()

	opts := probe.Options{
		Only:        only,
		Timeout:     st.Timeouts.ClassifierFirstByte(),
		SlowAfter:   st.Timeouts.ClassifierFirstByte(),
		Concurrency: 8,
		WaitTick:    5 * time.Second,
	}
	// OnNote 不受 quiet 影响：它报的不是进度（那才是 quiet 要压掉的），而是
	// 「这次探活了什么」与「缓存没能读/写」——都是只看一次就该看见的东西，
	// 而且在 --json 模式下 stderr 也不会污染 stdout 的结果表。
	opts.OnNote = func(msg string) {
		fmt.Fprintln(os.Stderr, style.Item(style.Warn, msg))
	}
	if !quiet {
		opts.OnPlan = func(ts []probe.Target) {
			fmt.Fprintf(os.Stderr, "%s\n", i18n.T(
				"probing {n} targets (concurrency {c}; each (provider, model) is probed once)",
				i18n.A{"n": len(ts), "c": opts.Concurrency}))
			for _, t := range ts {
				fmt.Fprintf(os.Stderr, "  %s %s\n", style.Mark(style.Skip), t)
			}
			fmt.Fprintln(os.Stderr)
		}
		opts.OnDone = func(t probe.Target, status int, lat, totalLat time.Duration, err error) {
			mark := style.Mark(probeMark(err == nil && status == 200))
			st := "  -"
			if status > 0 {
				st = fmt.Sprintf("%3d", status)
			}
			msg := ""
			if err != nil {
				msg = "   " + firstLine(err.Error())
			}
			line := i18n.T("{mark} {target} {status} main {main}ms total {total}ms{msg}",
				i18n.A{"mark": mark, "target": t.String(), "status": st,
					"main": lat.Milliseconds(), "total": totalLat.Milliseconds(), "msg": msg})
			fmt.Fprintln(os.Stderr, style.WrapLine(line, "    "))
		}
		opts.OnWaiting = func(inflight map[probe.Target]time.Duration) {
			var parts []string
			for t, d := range inflight {
				parts = append(parts, fmt.Sprintf("%s %ds", t, int(d.Seconds())))
			}
			sort.Strings(parts)
			for _, part := range parts {
				fmt.Fprintln(os.Stderr, style.WrapLine(
					i18n.T("        {mark} waiting: {part}",
						i18n.A{"mark": style.Mark(style.Skip), "part": part}), "          "))
			}
		}
	}

	results, err := probe.Run(opts)
	if err != nil {
		return host.Die(65, err.Error())
	}
	opened, healthErr := publishProbeHealth(results)
	if healthErr != nil && !quiet {
		fmt.Fprintln(os.Stderr, style.Item(style.Warn,
			i18n.Ef(healthErr, "global breaker table not updated: {err}", nil).Error()))
	}
	if asJSON {
		b, _ := json.MarshalIndent(results, "", "  ")
		fmt.Println(string(b))
		if probe.FailedCount(results) > 0 {
			return 1
		}
		return 0
	}

	head := i18n.T("all profiles", nil)
	if only != "" {
		head = "profile " + only
	}
	fmt.Println(style.Title("newgate probe", i18n.N(
		"{head} · {n} target · {elapsed}", "{head} · {n} targets · {elapsed}",
		probe.UniqueCount(results),
		i18n.A{"head": head, "elapsed": time.Since(started).Round(time.Millisecond)})))
	fmt.Println(style.Rule(72))

	// 明细：同一 profile 的后续行不再重复 profile 名（视觉分组，省一列宽度）。
	t := style.NewTable(i18n.T("profile", nil), i18n.T("tier", nil),
		i18n.T("upstream/model", nil), i18n.T("grade", nil),
		i18n.T("main latency", nil), i18n.T("total time", nil))
	last := ""
	var probeErrors []string
	for _, r := range results {
		p := r.Profile
		if p == last {
			p = ""
		} else {
			last = r.Profile
		}
		lat := style.Dim("-")
		if r.Latency > 0 {
			lat = fmt.Sprintf("%dms", r.Latency.Milliseconds())
		}
		if r.Err != "" {
			probeErrors = append(probeErrors, i18n.T("{provider}/{model}: HTTP {status} · {err}",
				i18n.A{"provider": r.Provider, "model": r.Model, "status": r.Status,
					"err": firstLine(r.Err)}))
		}
		totalLat := style.Dim("-")
		if r.Total > 0 {
			totalLat = fmt.Sprintf("%dms", r.Total.Milliseconds())
		}
		t.Row(p, r.Role, r.Provider+"/"+r.Model,
			probeGrade(r, opts.SlowAfter), lat, totalLat)
	}
	fmt.Print(t.String())
	for _, detail := range probeErrors {
		fmt.Println(style.Item(style.Bad, detail))
	}
	fmt.Println(style.Hint(i18n.T("the health score uses the main probe latency only; total time also includes dialect and quirk checks", nil)))

	// 方言能力：probe 顺带探明的。count_tokens ✗ 的上游，Claude Code 的
	// 水位条走本地粗估（forward 层 lazy probe 也会自己学到这一点）。
	if entries := dialect.Snapshot(); len(entries) > 0 {
		fmt.Print(style.Section(i18n.T("dialect capabilities", nil)) +
			style.Dim(i18n.T("   ✓ supported  ✗ probed and unsupported  ? not probed", nil)) + "\n")
		d := style.NewTable(i18n.T("upstream/model", nil), "openai", "anthropic", "count_tokens")
		for _, e := range entries {
			d.Row(e.Provider+"/"+e.Model,
				dialectMark(e, dialect.CapOpenAI),
				dialectMark(e, dialect.CapAnthropic),
				dialectMark(e, dialect.CapCountTokens))
		}
		fmt.Print(d.String())
	}

	sums := probe.Summarize(results)
	fmt.Print(style.Section(i18n.T("summary", nil)) + "\n")
	s := style.NewTable(i18n.T("profile", nil), i18n.T("reachable", nil),
		i18n.T("down", nil), i18n.T("average latency", nil), i18n.T("verdict", nil))
	s.AlignRight(1)
	s.AlignRight(2)
	s.AlignRight(3)
	var healthy []string
	for _, sm := range sums {
		concl := style.Green(i18n.T("usable", nil))
		switch {
		case sm.OK == 0:
			concl = style.Red(i18n.T("unavailable", nil))
		case sm.Bad > 0:
			concl = style.Yellow(i18n.T("partially usable", nil))
		case sm.Grade == "fluent":
			concl = style.Green(i18n.T("fast", nil))
		}
		avg := style.Dim("-")
		if sm.OK > 0 {
			avg = fmt.Sprintf("%dms", sm.AvgMs)
			if sm.Bad == 0 {
				healthy = append(healthy, fmt.Sprintf("%s(%dms)", sm.Profile, sm.AvgMs))
			}
		}
		s.Row(sm.Profile, fmt.Sprintf("%d", sm.OK), fmt.Sprintf("%d", sm.Bad), avg, concl)
	}
	fmt.Print(s.String())

	fmt.Println(style.Hint(i18n.T("current profile: {profile}", i18n.A{"profile": st.DefaultProfile})))
	if healthErr == nil {
		fmt.Println(style.Hint(i18n.N(
			"global breaker table updated: {n} binding among this round's targets is still open (slow threshold {threshold})",
			"global breaker table updated: {n} bindings among this round's targets are still open (slow threshold {threshold})",
			opened, i18n.A{"threshold": st.Timeouts.ClassifierFirstByte()})))
		if opened > 0 {
			fmt.Println(style.Hint(i18n.T("isolated for at least 60s; only a successful probe can restore it afterwards", nil)))
		}
	}

	// 当前 profile 有挂的就给出建议
	for _, sm := range sums {
		if sm.Profile == st.DefaultProfile && sm.Bad > 0 {
			fmt.Println()
			fmt.Println(style.Item(style.Warn, i18n.N(
				"current profile {profile} has {n} tier unavailable",
				"current profile {profile} has {n} tiers unavailable",
				sm.Bad, i18n.A{"profile": sm.Profile})))
			if len(healthy) > 0 {
				fmt.Println(style.Bullet(i18n.T("all green: {list}", i18n.A{"list": strings.Join(healthy, " · ")})))
				fmt.Println(style.Bullet(style.Cyan("newgate --set-profile " + strings.SplitN(healthy[0], "(", 2)[0])))
			} else {
				fmt.Println(style.Bullet(i18n.T("no all-green profile", nil)))
			}
		}
	}
	if probe.FailedCount(results) > 0 {
		return 1
	}
	return 0
}

func publishProbeHealth(results []probe.Result) (int, error) {
	info, _ := controlplane.State()
	if info == nil || info.Port <= 0 {
		return 0, i18n.E("daemon is not running", nil)
	}
	st := store.LoadState()
	type observation struct {
		Provider  string `json:"provider"`
		Model     string `json:"model"`
		Status    int    `json:"status"`
		LatencyMs int64  `json:"latency_ms"`
		Context   int    `json:"context_bytes"`
		Error     string `json:"error,omitempty"`
	}
	byTarget := map[string]observation{}
	for _, r := range results {
		if r.Provider == "" || r.Model == "" {
			continue
		}
		key := r.Provider + "/" + r.Model
		byTarget[key] = observation{
			Provider: r.Provider, Model: r.Model, Status: r.Status,
			LatencyMs: r.Latency.Milliseconds(), Context: 1, Error: r.Err,
		}
	}
	var keys []string
	for key := range byTarget {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var observations []observation
	for _, key := range keys {
		observations = append(observations, byTarget[key])
	}
	var response struct {
		Opened int `json:"opened"`
	}
	err := controlplane.Post(info.Port, controlpath.Health, st.ControlToken,
		map[string]interface{}{"observations": observations}, &response)
	return response.Opened, err
}

// probeMark 探活结果的标记。跟 probe.Light() 的灯同义，但用本 CLI 统一的
// 符号集——表格里塞 emoji 会撑坏对齐，也不 geek。
func probeMark(ok bool) string {
	if ok {
		return style.OK
	}
	return style.Bad
}

func probeGrade(r probe.Result, slowAfter time.Duration) string {
	switch {
	case r.Status != http.StatusOK:
		return style.Red(i18n.T("unavailable", nil))
	case r.Latency > slowAfter:
		return style.Red(i18n.T("laggy", nil))
	case r.Latency >= 3*time.Second:
		return style.Yellow(i18n.T("usable", nil))
	default:
		return style.Green(i18n.T("fast", nil))
	}
}

// dialectMark 一格能力灯：✓ 支持 / ✗ 探过明确不支持 / ? 没探到。
func dialectMark(e dialect.Entry, c dialect.Cap) string {
	if e.Known&c == 0 {
		return "?"
	}
	if e.Supports&c != 0 {
		return "✓"
	}
	return "✗"
}

func firstLine(s string) string {
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		s = s[:i]
	}
	return s
}
