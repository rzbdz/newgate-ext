// Package simplecli is a **minimal UI**: renders status only; other injections not shown.
//
// # What it proves
//
// In newgate the UI is a **replaceable shell**: modules inject commands and status lines
// into "the UI", a weak dependency (modules.Optional(cli)): present = mounted, absent =
// skipped. The counter-experiment: in `dist-simple-cli.json`, turn off the kernel's `cli`
// and enable `simple-cli`, **no other module changes**, and it must come up as usual:
//
//   - it still Provides `cliapi.Capability`, so every Optional(cli) still binds and
//     commands and status lines land in its ledger (a shell swap is invisible to injectors);
//   - it **renders** status and the command list only; the other six injections
//     (diagnostics, status blocks, diagnostics data, glossary, verbose marker) are
//     accepted but not shown - **one line at registration**, never swallowed silently;
//   - data plane, takeover, breaker, config parsing are untouched; they never needed the UI.
//
// # Why it does not implement modules/cli's rendering
//
// That rendering (help sections, tables, CJK width alignment) is **one shell's layout**,
// not the UI contract: modules inject in via cli/extension's seven RegisterXxx, the
// entry goes out via entry.Handler. Both here, deliberately minimal - how small a UI can be.
package simplecli

import (
	"context"
	"fmt"
	"sort"
	"sync"

	"github.com/rzbdz/newgate-ext/lib/buildinfo"
	"github.com/rzbdz/newgate-ext/lib/style"
	cliapi "github.com/rzbdz/newgate-ext/modules/cli/extension"
	modules "github.com/rzbdz/newgate/component"
	"github.com/rzbdz/newgate/component/entry"
	"github.com/rzbdz/newgate/lib/i18n"
)

// statusLabelW is the label column's width in status.
//
// It is **layout data**, so it is one constant: a Chinese label and an English one
// (`Interface`) differ in **display** width and **byte** length; padding by bytes skews
// when the language changes. The width is shared, not cataloged (docs/13-i18n.md §3).
const statusLabelW = 10

type service struct {
	self modules.Release

	mu       sync.Mutex
	commands []cliapi.Command
	statuses []cliapi.StatusProvider
	// The kinds accepted but not rendered. The count is kept and reported in status - "accepted
	// but not shown" must be visible, or a module's check item vanishing reads as its bug.
	accepted map[string]int
}

var (
	_ cliapi.CLI    = (*service)(nil)
	_ entry.Handler = (*service)(nil)
)

// New declares the minimal UI component.
//
// It Provides **the UI port itself** (cliapi.Capability), not a private port: a shell
// swap must be invisible to injectors, or every swap edits every module - what this
// injection exists to avoid. "Two shells at once" is caught by existing tests (app's
// TestExactlyOneUI asserts exactly one provider), and a spec turning one off has one.
func New() modules.Component {
	s := &service{accepted: map[string]int{}}
	return modules.Component{
		Name: "simple-cli",
		Type: "cli",
		// Deps are **empty**: the UI depends on no module (same rule as modules/cli).
		Provides: []modules.Provision{
			modules.Provide(cliapi.Capability, cliapi.CLI(s)),
		},
		Start: func(_ context.Context, ctx modules.Context) error {
			// Declares itself the **explicitly preferred** entry (entry.RankPreferred): asked
			// before the default UI. A distribution usually installs one shell; the rank makes
			// "who wins with **two shells** installed" explicit - the product's choice, not dir name luck.
			release, err := modules.MustGet(ctx, entry.Capability).Register(s, entry.RankPreferred)
			if err != nil {
				return err
			}
			s.self = release
			return nil
		},
		Stop: func(context.Context) error {
			if s.self == nil {
				return nil
			}
			return s.self()
		},
	}
}

// Name goes into logs: the composition root's `[entry] resolve: … → simple-cli`.
func (s *service) Name() string { return "simple-cli" }

// Claims always claims - this shell is the product-chosen entry, ranked below the default UI.
func (s *service) Claims(entry.Process) bool { return true }

// Handle runs one invocation: knows status and (with no args) a self-description.
func (s *service) Handle(p entry.Process) int {
	if len(p.Args) == 0 {
		fmt.Println(i18n.T("newgate (simple-cli) — a minimal UI. Available: status", nil))
		return 0
	}
	if p.Args[0] == "status" {
		return s.status()
	}
	// Commands in the list still run: a module's injected Command is its own dispatcher
	// (the same interface modules/cli gets); this only forwards the call.
	if cmd, ok := s.lookup(p.Args[0]); ok {
		return cmd.Run(simpleHost{}, p.Args[1:])
	}
	// The `simple-cli: ` prefix is a **machine marker** (grep it when debugging), kept
	// outside the message - like the kernel cli's `errCLI` (see docs/13-i18n.md §7).
	//
	// Quotes live in the message rather than `%q` on the argument: placeholders only fill
	// values, quotes are part of layout (the kernel's `plugin: unknown verb "{verb}"` too).
	fmt.Printf("simple-cli: %s\n", i18n.T("unknown command \"{cmd}\" (see the list with newgate status)",
		i18n.A{"cmd": p.Args[0]}))
	return 64
}

func (s *service) lookup(name string) (cliapi.Command, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, c := range s.commands {
		for _, n := range c.Names() {
			if n == name {
				return c, true
			}
		}
	}
	return nil, false
}

func (s *service) status() int {
	fmt.Println("newgate " + buildinfo.Version() + "  simple-cli")
	// The version is a machine marker (`newgate dev  simple-cli` line), not cataloged; this
	// whole sentence is - the shell name sits mid-sentence, and splitting it out glues the
	// English (`simple-cli(renders`), while Chinese "（" separates by itself; copy it as is.
	fmt.Printf("  %s %s\n", style.Pad(i18n.T("Interface", nil), statusLabelW),
		i18n.T("simple-cli (renders status and the commands; the other injection points are accepted but not shown)", nil))

	s.mu.Lock()
	commands := append([]cliapi.Command(nil), s.commands...)
	statuses := append([]cliapi.StatusProvider(nil), s.statuses...)
	accepted := map[string]int{}
	for k, v := range s.accepted {
		accepted[k] = v
	}
	s.mu.Unlock()

	lines := []cliapi.StatusLine{}
	for _, p := range statuses {
		lines = append(lines, p.Status()...)
	}
	sort.SliceStable(lines, func(i, j int) bool { return lines[i].Rank < lines[j].Rank })
	for _, l := range lines {
		fmt.Printf("  %s %s\n", style.Pad(l.Label, statusLabelW), l.Value)
	}

	names := []string{}
	for _, c := range commands {
		names = append(names, c.Names()...)
	}
	sort.Strings(names)
	fmt.Printf("  %s\n", i18n.N("{n} command", "{n} commands", len(names), i18n.A{"n": len(names)}))
	for _, n := range names {
		fmt.Println("             · " + n)
	}
	if len(accepted) > 0 {
		kinds := []string{}
		for k := range accepted {
			kinds = append(kinds, k)
		}
		sort.Strings(kinds)
		fmt.Printf("  %s ", i18n.T("accepted but not rendered", nil))
		for i, k := range kinds {
			if i > 0 {
				// The list separator is wording too: Chinese uses "、", English ", " - it is
				// punctuation for humans, not a format string (`×`-like symbols stay outside).
				fmt.Print(i18n.T(", ", nil))
			}
			fmt.Printf("%s×%d", kindLabel(k), accepted[k])
		}
		fmt.Println()
	}
	return 0
}

// ---- cli/extension's injection points ----

// Run only satisfies the interface shape: the entry goes through Handle (above). Give it
// a defined behavior rather than panic - a direct interface call gets the entry's answer.
func (s *service) Run(p entry.Process) int { return s.Handle(p) }

// errShell prefixes this shell's assembly errors with the module name.
//
// The prefix is a **machine marker** (grep `simple-cli:` to find this shell's words), so
// it stays outside the message - same rule as the kernel cli's errCLI (docs/13-i18n.md §7).
func errShell(err error) error { return fmt.Errorf("simple-cli: %w", err) }

// RegisterCommand records and returns an undo handle (same semantics as modules/cli).
func (s *service) RegisterCommand(c cliapi.Command) (modules.Release, error) {
	if c == nil {
		return nil, errShell(i18n.E("a command cannot be nil", nil))
	}
	s.mu.Lock()
	s.commands = append(s.commands, c)
	s.mu.Unlock()
	return func() error {
		s.mu.Lock()
		defer s.mu.Unlock()
		for i, existing := range s.commands {
			if existing == c {
				s.commands = append(s.commands[:i], s.commands[i+1:]...)
				return nil
			}
		}
		return nil
	}, nil
}

func (s *service) RegisterStatus(p cliapi.StatusProvider) (modules.Release, error) {
	if p == nil {
		return nil, errShell(i18n.E("a status provider cannot be nil", nil))
	}
	s.mu.Lock()
	s.statuses = append(s.statuses, p)
	s.mu.Unlock()
	return func() error {
		s.mu.Lock()
		defer s.mu.Unlock()
		for i, existing := range s.statuses {
			if existing == p {
				s.statuses = append(s.statuses[:i], s.statuses[i+1:]...)
				return nil
			}
		}
		return nil
	}, nil
}

// The other five kinds: **accept, count, log one line**.
//
// Why no error: an error would fail the injector's (gateway / config / runtime...) Start
// on the spot and the graph would not assemble - and "swap the shell" must not swap the
// data plane. Nor swallow it: the registration log line + the status count make "my
// injection was not shown" checkable (easily mistaken for a module bug in practice).
func (s *service) RegisterDiagnostics(cliapi.DiagnosticProvider) (modules.Release, error) {
	return s.accept(kindDiagnostics), nil
}
func (s *service) RegisterStatusBlocks(cliapi.BlockProvider) (modules.Release, error) {
	return s.accept(kindBlocks), nil
}
func (s *service) RegisterDump(cliapi.Dumper) (modules.Release, error) {
	return s.accept(kindDump), nil
}
func (s *service) RegisterGlossary(cliapi.Glossarist) (modules.Release, error) {
	return s.accept(kindGlossary), nil
}
func (s *service) RegisterVerbose(cliapi.Verbose) (modules.Release, error) {
	return s.accept(kindVerbose), nil
}

// Injection points' **identity**: ASCII, stable, ledger keys and grep anchors.
//
// They are identity, not wording, hence no Chinese keys (CLAUDE.md "never use rendered
// text as a criterion"): a translation must not move the keys. kindLabel computes the text.
const (
	kindDiagnostics = "diagnostics"
	kindBlocks      = "status-blocks"
	kindDump        = "dump"
	kindGlossary    = "glossary"
	kindVerbose     = "verbose"
)

// kindLabel turns an identity into the sentence for humans.
//
// Computed on demand, not stored at registration: the language installs during Start, and
// module injections also happen in their own Start - order is the graph's, so storing it
// could store the pre-install copy (same thing "no i18n.T in package vars" avoids).
func kindLabel(kind string) string {
	switch kind {
	case kindDiagnostics:
		return i18n.T("diagnostics", nil)
	case kindBlocks:
		return i18n.T("status blocks", nil)
	case kindDump:
		return i18n.T("dump material", nil)
	case kindGlossary:
		return i18n.T("glossary", nil)
	case kindVerbose:
		return i18n.T("verbose mode", nil)
	}
	return kind // an unknown kind goes out as-is: a new injection point forgot to register
}

func (s *service) accept(kind string) modules.Release {
	s.mu.Lock()
	s.accepted[kind]++
	s.mu.Unlock()
	// `{kind}` is the five kinds' name: Chinese does not inflect, but the English ones
	// (diagnostics / status blocks / …) are plural, so the sentence avoids articles and
	// lets the translation order words freely.
	fmt.Printf("simple-cli: %s\n", i18n.T("{kind} injection accepted (this shell does not render it; the counts are in status)",
		i18n.A{"kind": kindLabel(kind)}))
	return func() error {
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.accepted[kind] > 0 {
			s.accepted[kind]--
		}
		return nil
	}
}

// simpleHost is the host Command.Run needs. The minimal shell offers the least: exit code, hints.
// **NotifyProxy / DaemonRunning are deliberately absent** - they need "the UI owns the
// process lifecycle", and this shell lacks that knowledge (it does not know daemons).
type simpleHost struct{}

var _ cliapi.Host = simpleHost{}

func (simpleHost) Die(code int, message string) int {
	if message != "" {
		fmt.Println(message)
	}
	return code
}
func (simpleHost) Verb() string        { return "" }
func (simpleHost) NotifyProxy()        {}
func (simpleHost) DaemonRunning() bool { return false }
