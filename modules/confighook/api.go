package confighook

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/rzbdz/newgate-ext/modules/config/paths"
	modules "github.com/rzbdz/newgate/component"
	i18n "github.com/rzbdz/newgate/lib/i18n"
)

// ConfigHooks is the ownership port through which client modules
// write their config extensions. Every registration returns a Release,
// so a module can precisely undo its own contribution on stop.
type ConfigHooks interface {
	RegisterAgent(*Agent) (modules.Release, error)
	// RegisterAgentInstaller submits "how my tool is installed" (see
	// AgentInstaller). Not submitting = this client offers no self-install,
	// and `newgate <agent>` reports an error as before on a machine without it.
	RegisterAgentInstaller(agentID string, installer AgentInstaller) (modules.Release, error)
	// RegisterAgentFacts submits "how my client is doing right now" (see AgentFacts).
	//
	// It is **another** registration rather than a field on the descriptor: the descriptor
	// is settled once when submitted in Start, while the facts may differ every time they
	// are asked, and **need not be submitted** (without them the default criterion applies).
	RegisterAgentFacts(agentID string, facts AgentFacts) (modules.Release, error)
	BindTakeover(agentID string, takeover ConfigTakeover) (modules.Release, error)
	RegisterStateField(owner, name string) (modules.Release, error)
}

// AgentCatalog is the read-only client catalog for runtime and the CLI, separated from
// ConfigHooks so consumers cannot modify the registry through the query capability.
type AgentCatalog interface {
	Get(id string) (*Agent, bool)
	Names() []string
	// Facts gets the runtime facts some client submitted; nil = it submitted none.
	Facts(id string) AgentFacts
	// Installer gets some client's install method; nil = it submitted none (then no self-install).
	Installer(id string) AgentInstaller
	// Installed reports whether this client is on **this machine**.
	//
	// With facts, ask the facts (that is the client's own criterion); without them, look for
	// Bin on PATH (skipping our own shim directory, see InstalledDefault). Why the default is
	// "look on PATH" rather than "assume present": the whole use of this criterion is answering
	// "is it there at all", and assuming present returns to the bug it was meant to fix.
	Installed(id string) bool
}

// The two capabilities are the write and read faces of one registry:
// the write end goes only to extension modules, the read end to
// runtime and the CLI, so queriers cannot gain registration permission.
var (
	ConfigHooksCapability  = modules.NewCapability[ConfigHooks]("config-hooks")
	AgentCatalogCapability = modules.NewCapability[AgentCatalog]("agent-catalog")
)

// Slot describes how one model slot of a client maps to a newgate semantic tier. A slot with an
// empty EnvVar can still be used by config takeover, but takes no part in process env injection.
type Slot struct {
	Name string
	// Tier is this slot's **default** assignment. The user can change it in the
	// config (see AgentFacts.SlotTier) -- once changed, that one takes effect, not
	// this. So to read "where it goes right now" go through Facts, not this directly.
	Tier string
	// Also are the values this slot recognizes **besides
	// tiers** -- the client's own convention, not our tiers.
	//
	// Why it is needed: Claude Code's subagent slot can be written `inherit` ("follow
	// the parent session"), which is **its** semantics, and newgate only injects that string as
	// is. Without this cell, the configurable slot-mapping path would filter `inherit` out as a
	// mistyped tier name -- unsettable in the UI, and a hand edit silently ignored (looks like
	// it did not take effect, while the description plainly says it can be written that way).
	//
	// The values are declared by **the client module** (that is its knowledge); the
	// kernel and the config layer are only responsible for "these are legal too".
	Also []string
	// ModelSuffix is the suffix **unconditionally** appended to the model name when injecting
	// this slot -- equally **the client's own capability marker**, not our tier semantics.
	//
	// Why it appeared (2026-09-28): Claude Code uses the `[1m]` suffix to declare "this
	// model has 1M of context". It is a client-side switch, not part of the upstream model
	// id -- the data plane strips it before forwarding (see gateway/protocol.NormalizeRole),
	// and the reverse resolution relies on this layer: the model name left after
	// stripping is exactly the name in the config, so naming resolution hits as before.
	//
	// Why it sits on slots, rather than "one switch per whole agent": slots can
	// differ. Claude Code's subagent slot may take `inherit` (see Also), and adding
	// a suffix to that value would inject a model name it does not recognize;
	// with the switch on the slot, the client speaks for itself slot by slot.
	//
	// Why the kernel neither validates it nor knows `[1m]`: the suffix is **a string**, and what
	// it means belongs to the client module that declares it. We go around the line where the
	// kernel would import the client module, so the contract is only "append it to the value when
	// injecting" -- both ends use the same constant, and nobody hardcodes `[1m]` in the kernel.
	//
	// Empty = append nothing. It **applies only to model names**; the two window-declaration variables
	// are numbers and never touch it (see Agent.ContextWindowEnv and modules/runtime/launch).
	ModelSuffix string
	EnvVar      string
	Desc        string
}

// Apply fills the value to inject for this slot into its
// final form: appending the client's own capability marker.
//
// Both dynamic mode (value = tier name) and pinned mode (value = real model name) go
// through here, so "the marker is always there" is written once. An empty value appends
// nothing -- an empty slot variable injected tells the client "the model is called the
// empty string", which is another bug and should not be manufactured in passing here.
func (s Slot) Apply(model string) string {
	if model == "" || s.ModelSuffix == "" {
		return model
	}
	return model + s.ModelSuffix
}

// Agent is the complete descriptor of a take-overable AI CLI. It
// declares binary discovery, protocol, model slots and config takeover
// in one place, so that knowledge does not scatter across runtime.
type Agent struct {
	ID      string
	Bin     []string
	Dialect string
	// Icon is this client's icon in the UI, the SVG **path's `d` string** (`24 24` viewport).
	//
	// Why "a string of path data" rather than a file name or a URL: the
	// icon is **the client's knowledge** (the same rule as ContextWindowEnv below),
	// so it brings its own; and as path data the UI only needs an `<svg viewBox="0 0
	// 24 24">` to put it in -- no icon library to know, no disk to read, no network.
	// `currentColor` is filled on the UI side, so one set of icons follows the skin.
	//
	// Why not a front-end table looked up by ID: that table would be "the UI knows
	// the clients", while the rule of this layer is exactly that it does not -- adding a client
	// would mean changing the front end, which amounts to this cell never having been designed.
	//
	// Empty = this client gave no icon (the UI draws its own generic mark, not a blank).
	Icon  string
	Slots []Slot

	BaseURLEnv string
	AuthEnv    string
	UnsetEnv   []string
	Notes      string
	Config     ConfigTakeover

	// ContextWindowEnv / AutoCompactEnv are the **names** of the two "window
	// declaration" environment variables: the former drives the client's window
	// assumption for an "unknown model", the latter is its own compact trigger.
	//
	// The names are **the client's knowledge** (Claude Code's is
	// CLAUDE_CODE_MAX_CONTEXT_TOKENS), so they are filled in by the agent definition, and the
	// kernel is only responsible for "if it is declared in the config, inject it". Empty = this
	// client does not recognize those two variables (opencode, say), and nothing is injected.
	//
	// Before 2026-09-20 these two names were hardcoded in modules/runtime/launch -- that was the
	// kernel knowing one client's internal variable names, the wrong direction. Moving them onto the
	// definition is the continuation of the same move: client integration belongs to the distribution
	// (see the distribution's modules/claudecode), and the kernel only provides this table.
	ContextWindowEnv string
	AutoCompactEnv   string

	// ContextWindowCap / AutoCompactCap are **the client's own safety cap**: however large the
	// profile declares, the injected window and compact numbers do not exceed it. 0 = no cap.
	//
	// Why it is needed (the scene on 2026-11-12): Claude Code's `[1m]` name suffix + a
	// 1M window declaration pushes the session to the very last cell of the real window.
	// Once over, the upstream's 400 rejects /compact too -- the compaction request itself
	// carries the whole session, so **the client has no self-rescue path**, only manually
	// deleting history. Leaving headroom is the only way to recover automatically;
	// the specific numbers belong to the client module (Claude Code's is 512K now).
	//
	// ContextWindowCap also counts as a declaration when the profile **does not declare**
	// a window: the client's default assumption for an unknown model is usually smaller (Claude
	// Code's is 200k), and this client can plainly take this cap. AutoCompactCap does not backstop
	// that way -- without a declared compact, let the client compute its compaction point from
	// its own window, avoiding injecting a value larger than the window when the window is small.
	ContextWindowCap int
	AutoCompactCap   int
}

// AgentFacts are a contributor's **runtime facts** about its own client:
// whether it is installed at this moment, and which tier some slot takes.
//
// # Why a port rather than two function fields on the descriptor
//
// Agent is a **descriptor**: Bin, dialect, slot table, environment variable names --
// those are "what this client is", unchanging, printable for humans. While the answers
// to those two questions change with disk and config (uninstalled today, installed
// tomorrow; the user changed the slot mapping). Stuffing "what it is" and "how it is
// right now" into one struct removes the dependency-injection side: who provides it,
// whether it can be replaced, how a fake goes in during tests -- none of it can be
// discussed; the kernel just picks up a function pointer that happens to be assigned.
//
// The port side is complete: contributors **register** in (getting a Release),
// consumers **ask** the port, and without any registration there is a hardcoded default
// (see AgentCatalog.Installed). Same origin as the rule "a registration-type capability
// must return a Release, and the consumer releases in reverse order in Stop".
type AgentFacts interface {
	// Installed reports whether this tool is on this machine. The implementation is
	// usually just `Agent().OnPath()` -- which already skips our own shim directory.
	Installed() bool
	// SlotTier reports which tier some slot takes right
	// now; the empty string = use the descriptor's default.
	SlotTier(slot Slot) string
}

// TierOf is the tier a slot actually takes right now: with facts from
// the contributor, ask them, otherwise use the descriptor's default.
//
// It is a **pure function** (feed the facts in), so "use the default
// when no facts" has only one implementation, the same for every caller.
func TierOf(facts AgentFacts, slot Slot) string {
	if facts != nil {
		if t := facts.SlotTier(slot); t != "" {
			return t
		}
	}
	return slot.Tier
}

// InstalledDefault is the **single** criterion for "is this client on this
// machine": if the client submitted facts ask the facts (it has its knowledge), and
// only without them look for its Bin on PATH -- and **exclude skipDirs** (our own
// shim directory, given by the caller: where the shim goes is runtime's knowledge).
//
// Why a free function rather than each place writing it: more than one place
// implements AgentCatalog (the real registry, the fake catalog in tests),
// and "installed or not" having two answers is the worst way this kind of
// criterion fails -- one says installed, one says not, and both look right.
func InstalledDefault(a *Agent, facts AgentFacts) bool {
	if facts != nil {
		return facts.Installed()
	}
	if a == nil {
		return false
	}
	return a.OnPath()
}

// TakeoverReport records what one config takeover
// actually changed; takeover must not succeed silently.
type TakeoverReport struct {
	File     string
	Rewrites []string
	Fuzzy    int
	Skipped  string
}

// ConfigTakeover defines a reversible persistent config takeover. Apply and Restore come
// in pairs, while Targets/IsTakenOver let the CLI explain the current state before acting.
type ConfigTakeover interface {
	Targets() []string
	IsTakenOver(target string) bool
	Apply(port int) ([]*TakeoverReport, error)
	Restore() ([]string, error)
}

// EnvSlots returns the slots that need environment variables
// injected, keeping the original Slots for config-type clients.
func (a *Agent) EnvSlots() []Slot {
	var out []Slot
	for _, slot := range a.Slots {
		if slot.EnvVar != "" {
			out = append(out, slot)
		}
	}
	return out
}

// BuildEnv produces the minimal environment override needed to start
// the client, without copying the current process's whole environment.
//
// facts is "how this client is right now" (see AgentFacts), fetched by the
// caller from the catalog port and handed in -- this function does not know
// how it was computed, and should not. nil = use the descriptor's default.
func (a *Agent) BuildEnv(port int, authToken string, facts AgentFacts) map[string]string {
	env := map[string]string{}
	if a.BaseURLEnv != "" {
		env[a.BaseURLEnv] = fmt.Sprintf("http://127.0.0.1:%d/a/%s", port, a.ID)
	}
	if a.AuthEnv != "" {
		env[a.AuthEnv] = authToken
	}
	for _, slot := range a.EnvSlots() {
		// Apply: the client's **capability marker** (`[1m]` and such) is appended here, and
		// both modes share the same append -- the marker is written once, see Slot.ModelSuffix.
		env[slot.EnvVar] = slot.Apply(TierOf(facts, slot))
	}
	return env
}

// FindReal skips newgate's own shim to find the real
// client, preventing a recursive self-launch after takeover.
func (a *Agent) FindReal(skipDir string) (string, error) {
	if p := a.realBin(); p != "" {
		return p, nil
	}
	return "", i18n.E("cannot find {names} in PATH (skipped the shim directory {dir})",
		i18n.A{"names": strings.Join(a.Bin, "/"), "dir": skipDir})
}

// OnPath reports whether this client's **real**
// executable is on PATH -- **excluding our own shim**.
//
// "Excluding" is the whole point of this criterion: takeover puts a same-named
// link in a directory earlier on PATH, so a naive PATH lookup **always** finds it, and
// that file is the one we put there. Without exclusion the criterion would answer "we
// did a takeover" rather than "this machine has this tool" -- and the two are separate
// in the "installed, then uninstalled" case (measured 2026-09-21: `which opencode` finds
// nothing, while status reports a check mark, because opencode goes through the config
// mechanism, and "taken over" is judged by whether the config file we changed is there).
//
// The directory to exclude is **one hardcoded entry** (paths.ShimDir), not
// a parameter passed by callers: there is always the same directory to skip, and
// making each caller pass it only adds the fault of "passed it wrong / forgot to
// pass it", and the symptom of that fault is exactly this criterion lying again.
func (a *Agent) OnPath() bool { return a.realBin() != "" }

// realBin is the one lookup the two above share.
func (a *Agent) realBin() string {
	skipDir := filepath.Clean(paths.ShimDir())
	skipped := func(dir string) bool { return filepath.Clean(dir) == skipDir }
	for _, name := range a.Bin {
		for _, dir := range filepath.SplitList(os.Getenv("PATH")) {
			if dir == "" || skipped(filepath.Clean(dir)) {
				continue
			}
			path := filepath.Join(dir, name)
			if !isExec(path) {
				continue
			}
			// One that still points at newgate after resolving the link
			// does not count either (people copy the shim elsewhere).
			if real, err := filepath.EvalSymlinks(path); err == nil {
				if skipped(filepath.Dir(real)) ||
					strings.HasSuffix(filepath.Base(real), "newgate") {
					continue
				}
			}
			return path
		}
	}
	return ""
}

func isExec(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir() && info.Mode()&0o111 != 0
}
