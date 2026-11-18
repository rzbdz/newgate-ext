package setup

// This file is `newgate config`'s **mainline**: the screens in order, plus each step's
// "do we do it" decision.
//
// The order has reasons:
//
//	1 pick clients - ask "whose traffic do we take" first. Needs no config, so it
//	   holds even on a bare machine (it doubles as the install checklist).
//	2 fill the first upstream - one base, one key, one model name. The **only gate**:
//	   without it every proxied request is a 401.
//	3 write disk - providers.json + mappings/default.kv + state.json.
//	4 install + takeover - each chosen in step 1: install if absent, take over if present.
//	5 start the proxy - so all of the above works at once.
//	6 one closing screen - what changed, what to type next.
//
// Step 4 comes **after step 3** because takeover writes values (model name, window
// declaration) from config now: takeover first computes tiers on a chain not yet written.
//
// # Already-configured people rerunning it
//
// They mostly just want to install/take over a new client. So steps 2 and 3 are
// **skipped** by default (with a usable global profile present); `--reconfigure` redoes
// them. Not lazy: asked for a "base URL", a working user most likely Ctrl-Cs, and then
// what they wanted (install a codex) went undone.

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

// Deps is every port the wizard uses. Missing one makes the job impossible, so required.
//
// All are **kernel public ports** (confighook's catalog, runtime's takeover and
// lifecycle), none a kernel internal package - deps cross only that seam (CLAUDE.md §2).
type Deps struct {
	Agents  agentapi.AgentCatalog
	Control runtimeapi.Control
	Out     io.Writer
}

// Options are this invocation's switches.
type Options struct {
	// Reconfigure re-asks the first upstream (default: only when "no usable config").
	Reconfigure bool
	// Yes does not ask "shall I write", follows defaults (for scripts and e2e).
	Yes bool
	// NoStart does not start the proxy (`newgate config --no-start`).
	NoStart bool
}

// Run is one complete go.
func Run(d Deps, opt Options) int {
	if d.Out == nil {
		d.Out = os.Stdout
	}
	fmt.Println() // leave a blank line between the shell prompt and the wizard

	t, err := term.Open()
	if err != nil {
		return noTTY(d, opt, err)
	}
	defer t.Close()
	fmt.Print(hideC)
	defer fmt.Print(showC + reset)
	return runWithScreen(d.Out, d, opt, &screen{T: t})
}

// runWithScreen is the half without a terminal: all branches here, screen from caller.
//
// Split so tests can script keys (see setup_test.go). It is also a discipline: **no
// place in the wizard reads keys directly** - all via Screen, so "what the user pressed"
// has one entry, and tests and the real terminal take the same path.
func runWithScreen(w io.Writer, d Deps, opt Options, sc Screen) int {
	fmt.Println() // leave a blank line between the shell prompt and the wizard
	existing := Inspect()

	// ---- 1. pick clients ----
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

	// ---- 2. first upstream ----
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

	// ---- 4. install + takeover ----
	fmt.Println(style.Bold(" " + i18n.T("Clients", nil)))
	port := storePort()
	steps := EnsureAgents(d.Agents, d.Control, pick.Chosen, port, d.Out)
	Report(d.Out, steps)

	// ---- 5. start the proxy ----
	report := StartReporter{}
	if !opt.NoStart {
		// SkipTakeover: takeover **already** happened above as the user chose (EnsureAgents).
		// A full takeover here would take over unchecked clients too - the opposite of what
		// that screen says.
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

	// ---- 6. closing ----
	report.Next = nextSteps(steps)
	var b strings.Builder
	printOutcome(&b, prof, steps, report)
	fmt.Fprint(w, b.String())
	return 0
}

// askUpstream is the "one base, one key, one model" page, plus the model list after it.
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
			// The suggestion is computed in the cell after base (Tab / Enter accepts it).
			Suggest: func(cur string) (string, string) {
				if strings.TrimSpace(cur) != "" {
					return "", ""
				}
				return SuggestedProvider(fields[0].Value, nil), i18n.T("press enter to accept, or type another name", nil)
			},
		},
	}
	if opt.Yes {
		// --yes: the cell derived from base is not asked (provider name is just a UI label).
		// It is for scripts/e2e, one Enter less; the other two still ask - base and key have
		// no sensible default, and guessing one only makes the user think it is set up.
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

	// Ask for the model list once: one more pick instead of memorizing a model name.
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

// nextSteps is the closing "what now" lines, computed from what this run actually did.
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
	// Clients installed this run whose config file has not appeared yet: run once, take
	// over again. This is the most important closing line - else requests in codex go
	// direct while the user thinks they just configured it.
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

// noTTY is the path without a terminal: no pretending to interact, list what to type.
//
// It must **really write nothing**: a non-interactive `newgate config` is likely from a
// script/CI, and silently changing config is the worst outcome there.
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

// screen wraps *term.Terminal into the two things the wizard uses.
type screen struct{ T *term.Terminal }

func (s *screen) ReadEvent() term.Event { return s.T.ReadEvent() }
func (s *screen) ReadLine(m term.LineMode, r func(string, string, int)) term.LineResult {
	return s.T.ReadLine(m, r)
}

// storePort reads the port from config right now (`newgate init` writes 8899; follow edits).
func storePort() int {
	return portOf(store.LoadState().Port)
}
