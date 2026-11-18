package gateway

// 本文件把**数据面自己的**两条体检交出去：出站代理会不会劫走 loopback（环境），
// 以及进程与端口那一对失败要分开说（代理）。
//
// **为什么它们住在这里**（2026-09-18）：这两条问的都是「请求到得了网关吗」，答案
// 的一半在网关自己的控制面客户端里（controlplane），另一半在网关自己的转发语义
// 里；而它们以前写在 modules/cli/diag.go，界面于是替数据面做着 HTTP 探活与端口
// 判断。谁的事谁自己说——界面只负责排版（见 cli/extension.Diagnostic）。
//
// 依赖方向：gateway → cli/extension（叶子契约），不是 → modules/cli。

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/rzbdz/newgate-ext/lib/durarg"
	"github.com/rzbdz/newgate-ext/lib/style"
	cliapi "github.com/rzbdz/newgate-ext/modules/cli/extension"
	"github.com/rzbdz/newgate-ext/modules/config/paths"
	"github.com/rzbdz/newgate-ext/modules/config/store"
	"github.com/rzbdz/newgate-ext/modules/gateway/controlplane"
	"github.com/rzbdz/newgate/lib/i18n"
)

// 体检与 status 行的位置（Rank 小的在前）。代理排在接管与配置之前：它挂了所有
// 走 newgate 的工具一起挂。
const (
	rankStatusProxy = 10
	rankCheckEnv    = 20
	rankCheckProxy  = 30

	// 诊断包里的位置（见 cliapi.DumpSection）。证据与日志排在配置之后：
	// 前面是「配置长什么样」，后面是「真发出去过什么」。
	rankDumpEvidence = 50
	rankDumpLog      = 60
)

type gatewayReporter struct{}

var (
	_ cliapi.DiagnosticProvider = gatewayReporter{}
	_ cliapi.StatusProvider     = gatewayReporter{}
	_ cliapi.Dumper             = gatewayReporter{}
)

func (gatewayReporter) Diagnostics() []cliapi.Diagnostic {
	return []cliapi.Diagnostic{checkEnv(), checkProxy()}
}

// Status 一行说清代理在不在跑。
//
// 三种状态分开表达：进程死了可以重启；**端口在听却探不通**是环境问题，重启一百
// 次也没用（常见于出站代理劫持 loopback）。这一行只给结论，展开在 doctor 里。
func (gatewayReporter) Status() []cliapi.StatusLine {
	info, doc := controlplane.State()
	var value string
	switch {
	case info == nil:
		value = style.Dim(i18n.T("Not running", nil)) + "    newgate start"
	case doc == nil:
		value = style.Yellow(i18n.T("Port not responding", nil)) + fmt.Sprintf("   pid %d · 127.0.0.1:%d", info.PID, info.Port)
	default:
		value = style.Green(i18n.T("● Running", nil)) + fmt.Sprintf("   pid %d · 127.0.0.1:%d · %s · %dreq/%derr",
			info.PID, info.Port, durarg.Format(doc.UptimeS), doc.Requests, doc.Failures)
	}
	return []cliapi.StatusLine{{Rank: rankStatusProxy, Label: i18n.T("Proxy", nil), Value: value}}
}

// checkEnv 出站代理会不会把 loopback 请求劫走。
//
// 这是最隐蔽的一类故障：newgate 日志里一条请求都没有，因为请求根本没到我们
// 这——客户端发给了 http_proxy，代理连不上 127.0.0.1 就回 502。
func checkEnv() cliapi.Diagnostic {
	d := cliapi.Diagnostic{Rank: rankCheckEnv, Label: i18n.T("Environment", nil)}
	set := proxyEnvSet()
	if len(set) == 0 {
		d.State = "ok"
		d.Line = i18n.T("No outbound proxy variables", nil)
		return d
	}
	noProxy := os.Getenv("no_proxy") + "," + os.Getenv("NO_PROXY")
	covered := 0
	for _, want := range []string{"127.0.0.1", "localhost"} {
		if strings.Contains(noProxy, want) {
			covered++
		}
	}
	if covered == 2 {
		d.State = "ok"
		d.Line = i18n.T("Outbound proxy set; NO_PROXY already allows loopback", nil)
		d.Details = append(d.Details, set...)
		return d
	}
	d.State = "bad"
	d.Line = i18n.T("Outbound proxy set; NO_PROXY does not allow 127.0.0.1 / localhost", nil)
	d.Details = append(d.Details, set...)
	d.Details = append(d.Details,
		i18n.T("Consequence: clients hand requests for 127.0.0.1:8899 to the outbound proxy, and a failed connection becomes HTTP 502;", nil),
		i18n.T("      newgate leaves no record of it in its log.", nil),
		i18n.T("Fix (add to the shell rc, then restart the client):", nil),
		`  export NO_PROXY="127.0.0.1,localhost,::1,$NO_PROXY"`,
		`  export no_proxy="$NO_PROXY"`)
	return d
}

func proxyEnvSet() []string {
	var out []string
	for _, n := range []string{"http_proxy", "HTTP_PROXY", "https_proxy", "HTTPS_PROXY",
		"all_proxy", "ALL_PROXY"} {
		if v := os.Getenv(n); v != "" {
			out = append(out, n+"="+v)
		}
	}
	return out
}

// checkProxy 代理进程与端口的两种失败要分开说：进程死了可以重启，
// 端口在听却探不通是环境问题，重启一百次也没用。
func checkProxy() cliapi.Diagnostic {
	d := cliapi.Diagnostic{Rank: rankCheckProxy, Label: i18n.T("Proxy", nil)}
	st := store.LoadState()
	info, doc := controlplane.State()
	switch {
	case info == nil:
		d.State = "skip"
		d.Line = i18n.T("Not running (port {port})", i18n.A{"port": st.Port})
		d.Details = append(d.Details, "newgate start")
	case doc != nil:
		d.State = "ok"
		d.Line = fmt.Sprintf("pid %d · 127.0.0.1:%d · %s", info.PID, info.Port, durarg.Format(doc.UptimeS))
	default:
		d.State = "bad"
		d.Line = i18n.T("pid {pid} is alive, HTTP probe to 127.0.0.1:{port} failed",
			i18n.A{"pid": info.PID, "port": info.Port})
		d.Details = append(d.Details,
			i18n.T("Process is healthy but requests cannot reach it; usually an outbound proxy hijacking loopback (see the Environment item).", nil))
	}
	return d
}

// ---------- 诊断包的原始素材 ----------

func (gatewayReporter) Dump() []cliapi.DumpSection {
	return []cliapi.DumpSection{dumpEvidence(), dumpLogTail()}
}

// dumpEvidence 列出上游报错时无条件落盘的证据文件。
//
// 它是排查「是不是代理改坏了请求」唯一能拿出手的东西（docs/11），所以连用法
// 一起写进诊断包——拿到包的人不必再去翻文档。
func dumpEvidence() cliapi.DumpSection {
	s := cliapi.DumpSection{Rank: rankDumpEvidence, Title: i18n.T("Error evidence files", nil)}
	dir := filepath.Join(paths.Config(), "dump")
	ents, err := os.ReadDir(dir)
	if err != nil || len(ents) == 0 {
		s.Lines = append(s.Lines, i18n.T("  (none; written automatically when the upstream returns 4xx/5xx)", nil))
		return s
	}
	for _, e := range ents {
		info, ierr := e.Info()
		size := int64(0)
		if ierr == nil {
			size = info.Size()
		}
		s.Lines = append(s.Lines, i18n.T("  {path}  {bytes} bytes",
			i18n.A{"path": filepath.Join(dir, e.Name()), "bytes": size}))
	}
	s.Lines = append(s.Lines,
		"",
		i18n.T("  Diff what we sent against what the client sent:", nil),
		fmt.Sprintf("    diff <(jq -S . %s/err-*.client-sent.json) \\", dir),
		fmt.Sprintf("         <(jq -S . %s/err-*.we-sent.json)", dir))
	return s
}

// dumpLogTail 日志全文。诊断包就是要原文，所以不做截断——它由用户自己决定怎么用。
func dumpLogTail() cliapi.DumpSection {
	s := cliapi.DumpSection{Rank: rankDumpLog, Title: i18n.T("Full log", nil)}
	b, err := os.ReadFile(paths.LogFile())
	if err != nil {
		s.Lines = append(s.Lines, i18n.T("  Cannot read: {err}", i18n.A{"err": err.Error()}))
		return s
	}
	s.Lines = append(s.Lines, strings.Split(strings.TrimRight(string(b), "\n"), "\n")...)
	return s
}
