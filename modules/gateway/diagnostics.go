package gateway

// This file hands out **the data plane's own** two diagnostics: whether an outbound proxy
// hijacks loopback (environment), and whether the process and port failures are told apart (proxy).
//
// **Why they live here** (2026-09-18): both ask "do requests reach the gateway", half the
// answer is in the gateway's own control-plane client (controlplane) and the other half
// in the gateway's own forwarding semantics; they used to live in modules/cli/diag.go,
// so the UI did HTTP probing and port judging for the data plane. Let each owner
// speak for itself - the UI only lays it out (see cli/extension.Diagnostic).
//
// Dependency direction: gateway → cli/extension (a leaf contract), not → modules/cli.

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

// The positions of the diagnostics and the status line (smaller Rank first). Proxy comes
// before takeover and config: if it is down, every tool going through newgate is down.
const (
	rankStatusProxy = 10
	rankCheckEnv    = 20
	rankCheckProxy  = 30

	// Position inside the diagnostics bundle (see cliapi.DumpSection). Evidence and log
	// come after config: first "what the config looks like", then "what really went out".
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

// Status says in one line whether the proxy is running.
//
// Three states, expressed separately: a dead process can be restarted; **a port listening but
// unreachable** is an environment problem where a hundred restarts do not help (common with an
// outbound proxy hijacking loopback). This line gives only the conclusion; details are in doctor.
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

// checkEnv: whether an outbound proxy would hijack loopback requests.
//
// This is the most insidious class of fault: the newgate log shows no
// request at all, because the request never reached us - the client sent it
// to http_proxy, and the proxy could not reach 127.0.0.1 and answered 502.
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

// checkProxy: the two failures, process and port, are told apart:
// a dead process can be restarted; a port listening but unreachable is
// an environment problem, and restarting a hundred times does not help.
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

// ---------- raw material for the diagnostics bundle ----------

func (gatewayReporter) Dump() []cliapi.DumpSection {
	return []cliapi.DumpSection{dumpEvidence(), dumpLogTail()}
}

// dumpEvidence lists the evidence files written unconditionally when upstream errors.
//
// It is the only thing to show when diagnosing "did the proxy break
// the request" (docs/11), so its usage is written into the bundle
// too - whoever gets the bundle need not dig through the docs again.
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

// dumpLogTail is the full log. A diagnostics bundle is meant to
// be raw, so no truncation - the user decides what to do with it.
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
