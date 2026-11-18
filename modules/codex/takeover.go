package codex

import (
	"io/ioutil"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/rzbdz/newgate-ext/modules/config/domain"
	"github.com/rzbdz/newgate-ext/modules/config/paths"
	"github.com/rzbdz/newgate-ext/modules/config/store"
	agentapi "github.com/rzbdz/newgate-ext/modules/confighook"
	"github.com/rzbdz/newgate/lib/i18n"
)

// This file is **codex's takeover**: rewriting
// `~/.codex/config.toml` to point it at the local proxy.
//
// # Why TOML rather than environment variables
//
// The other two clients both have an env entry point (claude has ANTHROPIC_BASE_URL,
// opencode has opencode.json), codex has none: its base_url, provider and model names
// **come only from that TOML** (`-c key=value` is a command-line override, not standing
// config). So codex takes the MechConfig path -- the same mechanism as opencode, not a shim.
//
// # Why byte surgery, not a TOML round trip
//
// `~/.codex/config.toml` is **hand-written**: it has comments, sections that grew over
// time like `[projects."/root"]`, and its own bookkeeping like `[notice.model_migrations]`. A
// parse-then-serialize round trip would make all of that disappear -- and "the comments I wrote
// in my config are gone" is exactly the loss users remember for a long time while nothing turns
// red for us (the same root cause as the kernel's "the request body does no JSON round trip").
//
// So there are only three things here, and each touches only the lines it should:
//
//	top-level `model`          -> the tier name (semantic tier, see docs/01-product.md)
//	top-level `model_provider` -> newgate
//	the whole `[model_providers.newgate]` section -> point at the local proxy

// ProviderID is the provider name we occupy in config.toml.
//
// "newgate" rather than something like "openai": codex reserves **built-in**
// provider ids (measured, its error text is `model_providers contains
// reserved built-in provider IDs`), and newgate is not on the reserved list.
const ProviderID = "newgate"

// providerTable is the header of that config section. IsTakenOver and the rewrite
// both go by it -- **defined once**, so "is it taken over" and "which section to
// rewrite" cannot disagree (which would mean repeated insertion on a second takeover).
const providerTable = "[model_providers." + ProviderID + "]"

// ConfigPath is the file codex reads.
//
// `$CODEX_HOME` wins, because that is codex's own rule (internally
// it is `os.environ.get("CODEX_HOME", "~/.codex")`). Hardcoding `~/.codex` would make anyone
// who "isolates one session with CODEX_HOME" take over somewhere else: the copy they edited is
// untouched, and the copy we edited is the one they are not using -- while both look successful.
func ConfigPath() string {
	if h := strings.TrimSpace(getenv("CODEX_HOME")); h != "" {
		return filepath.Join(h, "config.toml")
	}
	return filepath.Join(paths.Home(), ".codex", "config.toml")
}

// getenv is this layer's only indirection: tests must be able to point
// CODEX_HOME at a sandbox, and **must not** touch the real user's
// ~/.codex (the landing point of "tests do not touch real config").
var getenv = os.Getenv

// Takeover is the takeover handed to confighook.
//
// **It has no fields**, deliberately: the two values takeover needs (tier, window
// declaration) are computed at the moment of Apply (see tierOf / contextWindow). The first
// version on 2026-09-21 made them two function fields, with module.go passing closures in --
// that thing was "the look of DI without the substance of DI": same package, one implementation
// each, evaluation time exactly the same as a direct call. The only thing it bought was "looks
// replaceable", at the cost of one extra hop for the reader to find where the value comes from.
//
// The real criterion for replaceable is **whether it is a capability seam**:
// someone else provides it, someone else may not (that is confighook's
// AgentFacts, which goes through a registration port, not a field).
type Takeover struct{}

var _ agentapi.ConfigTakeover = Takeover{}

func (Takeover) Targets() []string { return []string{ConfigPath()} }

// IsTakenOver's criterion is **whether that section header is present**, not "the word newgate
// appears in the file": the latter would be hit by a comment the user wrote himself, and then
// `newgate off codex` would restore a file we never changed (overwriting what he just wrote).
func (Takeover) IsTakenOver(target string) bool {
	b, err := ioutil.ReadFile(target)
	if err != nil {
		return false
	}
	return hasMarker(b)
}

func hasMarker(b []byte) bool {
	return strings.Contains(string(b), providerTable)
}

// Apply rewrites the target file. When the file is missing it
// **does not create** one, but reports a line the user can act on.
//
// Why not "skip if absent" like opencode: several of opencode's targets are optional (the omo
// one may be missing), while codex's config.toml is its only location -- on a machine with
// codex installed it is always there (codex creates it itself). Absent = there is no codex
// on this machine at all, and that is exactly what should be said, not silently skipped.
func (t Takeover) Apply(port int) ([]*agentapi.TakeoverReport, error) {
	target := ConfigPath()
	rep := &agentapi.TakeoverReport{File: target}

	raw, err := ioutil.ReadFile(target)
	if err != nil {
		return []*agentapi.TakeoverReport{{
			File:    target,
			Skipped: i18n.T("no such file — is codex installed on this machine?", nil),
		}}, nil
	}
	if err := agentapi.BackupFile(target, hasMarker); err != nil {
		return []*agentapi.TakeoverReport{rep}, err
	}

	out, reps := rewrite(string(raw), port, wanted())
	rep.Rewrites = reps
	return []*agentapi.TakeoverReport{rep}, agentapi.WriteAtomic(target, []byte(out), 0o600)
}

// Restore restores from original/. The escape hatch: after
// restoring, delete newgate and codex works as usual.
func (Takeover) Restore() ([]string, error) {
	target := ConfigPath()
	done, err := agentapi.RestoreFile(target)
	if err != nil || !done {
		return nil, err
	}
	return []string{target}, nil
}

// ---------- TOML surgery ----------

// want is the set of values this takeover writes into config.toml.
//
// Why a struct rather than a long parameter list: codex has **more than one**
// model (see the slot table in agent.go), and there may be one more later; by
// the third positional parameter nobody remembers the order, and "the review
// tier was written into the main model" is completely invisible in the UI.
//
// Empty string / 0 = **do not write this line** (not write an empty
// value): codex's own default is always more right than a value we guessed.
type want struct {
	Model         string // which tier the main model uses
	ReviewModel   string // which tier `codex review` uses
	ContextWindow int    // context window (0 = do not declare)
	AutoCompact   int    // auto-compact threshold (0 = do not declare)
}

// values is "key -> literal to write". **Only the ones to write**.
func (w want) values() map[string]string {
	out := map[string]string{"model_provider": quote(ProviderID)}
	if w.Model != "" {
		out["model"] = quote(w.Model)
	}
	if w.ReviewModel != "" {
		out["review_model"] = quote(w.ReviewModel)
	}
	if w.ContextWindow > 0 {
		out["model_context_window"] = strconv.Itoa(w.ContextWindow)
	}
	if w.AutoCompact > 0 {
		out["model_auto_compact_token_limit"] = strconv.Itoa(w.AutoCompact)
	}
	return out
}

// order is the **fixed order** used when adding new keys to
// the file (for the why, see the add-key part in rewrite).
func (w want) order() []string {
	return []string{"model", "model_provider", "review_model",
		"model_context_window", "model_auto_compact_token_limit"}
}

// wanted computes every value to write at this moment.
//
// Every cell is computed **at this moment**: the slot mapping, the active
// profile and the takeover mode can all be changed by the user at any time.
func wanted() want {
	return want{
		Model:         slotValue("model"),
		ReviewModel:   slotValue("review_model"),
		ContextWindow: contextWindow(),
		AutoCompact:   autoCompactWindow(),
	}
}

// slotValue is the value this takeover writes for `<slot>`.
// It depends on the **takeover mode** (see models.go):
//
//	takeover (default) -> the tier name, `model = "normal"` (as before 2026-09-21).
//	rename -> codex's own model name, `model = "gpt-5.6-luna"`, from the table by tier.
//	  So its picker lists models it knows, and each name is recognized back as a tier:
//	  **switch models in codex = switch tiers**.
//
// When the reverse lookup fails (the table was narrowed, a tier has no corresponding codex
// model) it **falls back to the tier name**: takeover should not be unable to write anything
// because a table was configured too narrowly; it just means this tier has no corresponding
// name, and the behavior is the same as takeover mode -- which is its behavior anyway.
func slotValue(slot string) string {
	tier := tierFor(slot)
	if Mode() == ModeRename {
		if slug := modelFor(tier); slug != "" {
			return slug
		}
	}
	return tier
}

// rewrite is a **pure function**: given a TOML source, it returns the changed copy
// plus an account of "what changed where". Pure so unit tests can pin it directly
// -- the most breakable things about takeover are "changed the wrong line" and
// "inserted an extra section", and both are only found by users on a real machine.
func rewrite(src string, port int, w want) (string, []string) {
	lines := strings.Split(src, "\n")
	var reps []string

	// ---- 1. top-level scalars ----
	//
	// Only keys **before the first table header** count: `[projects."/root"]` also has short keys at
	// the `model` level (`trust_level` is one), and in TOML same-named keys in different tables are
	// unrelated -- pick the wrong table and it changes a value that has nothing to do with models.
	values := w.values()
	seen := map[string]bool{}
	inTable := false
	for i, ln := range lines {
		if strings.HasPrefix(strings.TrimSpace(ln), "[") {
			inTable = true
			continue
		}
		if inTable {
			continue
		}
		k, ok := topLevelKey(ln)
		if !ok {
			continue
		}
		v, isWanted := values[k]
		if !isWanted {
			continue
		}
		seen[k] = true
		if strings.TrimSpace(ln) == k+" = "+v {
			continue // already the value we wrote: do not book a fake change
		}
		reps = append(reps, k+" -> "+v)
		lines[i] = k + " = " + v
	}

	// Missing keys go at the **very top of the file**: a top-level key written after a
	// table header becomes that table's key (TOML has no "back to top level" syntax), so the only
	// place to add one is before the first table header. At the very top rather than just before
	// the first header, because the latter means handling layout details like "is there a blank
	// line before it", while the cost of the top is just the user's comment moving down one line.
	//
	// The order follows `order` (not map iteration order): this file is for humans,
	// and we may reorder it on every takeover -- the same config taken over twice
	// coming out in two shapes would make the user think we changed something else.
	var head []string
	for _, k := range w.order() {
		v, isWanted := values[k]
		// `values()` gives only the keys **to write**, while order lists every key that
		// could be written -- when the two disagree, values wins. Without this check a
		// valueless key is written as `review_model = ` (an empty value), and in TOML that
		// is not "not configured", it is an **empty string**: codex would send it to the
		// upstream as the model name. Measured hit (the idempotency test went red first).
		if !isWanted || seen[k] {
			continue
		}
		head = append(head, k+" = "+v)
		reps = append(reps, k+" -> "+v)
	}
	if len(head) > 0 {
		lines = append(head, lines...)
	}

	// ---- 2. the provider section ----
	block := providerBlock(port)
	start, end := -1, -1
	for i, ln := range lines {
		if strings.TrimSpace(ln) == providerTable {
			start = i
			end = i + 1
			for end < len(lines) && !strings.HasPrefix(strings.TrimSpace(lines[end]), "[") {
				end++
			}
			break
		}
	}
	if start >= 0 {
		out := make([]string, 0, len(lines)+len(block))
		out = append(out, lines[:start]...)
		out = append(out, block...)
		out = append(out, lines[end:]...)
		lines = out
		reps = append(reps, i18n.T("{table} rewritten", i18n.A{"table": providerTable}))
	} else {
		// Append at the end: one blank line before the header, so
		// it does not stick to the last key of the previous table.
		if n := len(lines); n > 0 && strings.TrimSpace(lines[n-1]) != "" {
			lines = append(lines, "")
		}
		lines = append(lines, block...)
		reps = append(reps, i18n.T("{table} added", i18n.A{"table": providerTable}))
	}

	out := strings.Join(lines, "\n")
	// If the source ends with a newline, the result should too (after appending
	// that block, the empty last element ends up in the middle). Without this the
	// file gains a pointless "\ No newline at end of file" diff after one takeover
	// -- the user reviewing our change would think we touched the last line.
	if strings.HasSuffix(src, "\n") && !strings.HasSuffix(out, "\n") {
		out += "\n"
	}
	return out, reps
}

// topLevelKey pulls the key out of a line of the form `key = value`.
//
// Deliberately picky (blank lines, comments, table headers, keys with spaces
// or quotes are all rejected), because its only use is "top-level scalars"
// -- and a top-level scalar's key is always a bare identifier. Looser and
// it would also accept quoted keys like the ones in `[projects."/root"]`.
func topLevelKey(line string) (string, bool) {
	t := strings.TrimSpace(line)
	if t == "" || strings.HasPrefix(t, "#") || strings.HasPrefix(t, "[") {
		return "", false
	}
	i := strings.IndexByte(t, '=')
	if i <= 0 {
		return "", false
	}
	k := strings.TrimSpace(t[:i])
	if k == "" || strings.ContainsAny(k, " \t\"'") {
		return "", false
	}
	return k, true
}

// providerBlock is the config section pointing at the local proxy.
//
// Two things are worth explaining:
//
//   - `wire_api = "responses"`: codex 0.155 (measured 2026-09-21) **no longer supports** `wire_api =
//     "chat"`; its error text is `wire_api = "chat" is no longer supported`. It means the Responses
//     API, and our forwarding is pure byte pass-through -- the upstream relay was measured to accept
//     `/v1/responses` (400 missing model, not 404), so this line needs no protocol conversion.
//   - `requires_openai_auth = false`: the local proxy never checks
//     this token anyway (the data plane drops the client's Authorization and replaces
//     it with the upstream's real key), so there is no reason to make the user manage
//     an environment variable. The cost, plainly: **do not treat this TOML as something
//     that protects the proxy**; it points at a local port listening on 127.0.0.1 only.
//
// **Do not go back to `env_key`** (measured 2026-09-21): when `env_key` points at an
// environment variable that is not set, codex does not error, it **hangs** -- `codex exec`
// ran the full 90-second timeout (exit 124), with output stopped at `Reading additional
// input from stdin...`. That is exactly the common case of "the user bypasses the shim
// and calls codex directly", and the symptom is a CLI that hangs with no explanation,
// extremely hard to attribute. Writing both does not help either (giving `env_key` and
// `requires_openai_auth = false` together, with the env var unset, still times out).
func providerBlock(port int) []string {
	return []string{
		providerTable,
		`name = "newgate (local proxy)"`,
		`base_url = "http://127.0.0.1:` + strconv.Itoa(port) + `/a/` + ID + `/v1"`,
		`wire_api = "responses"`,
		`requires_openai_auth = false`,
	}
}

// quote writes the value as a TOML basic string.
func quote(s string) string { return `"` + s + `"` }

// contextWindow gets the window this client should declare at this moment.
//
// The source is the **active profile's ContextWindow** (`docs/04-configuration.md`) -- the
// very same number the claude side uses for CLAUDE_CODE_MAX_CONTEXT_TOKENS. When it cannot
// be obtained (not configured, profile unreadable) it returns 0, i.e. **do not write this
// line**: codex will use its fallback metadata and print a warning, and that warning is
// honest -- better than us guessing a number and making it compact by the wrong window.
//
// The cost, plainly: when the profile changes (a tier bound to another model) this number
// does not follow by itself; `newgate on codex` has to be run again. This is the same
// thing as docs/03 §3.3's "changed injected content needs another takeover", not a new pit.
func contextWindow() int { return profileValue(func(p *domain.Profile) int { return p.ContextWindow }) }

// autoCompactWindow gets the auto-compact threshold this client
// should declare at this moment; 0 when it cannot be obtained.
//
// It is the counterpart of claude's `CLAUDE_CODE_AUTO_COMPACT_WINDOW` (see agent.go):
// codex estimates by itself when to compact based on the window, and the number it
// estimates comes from **what it thinks** the model metadata is -- and that metadata
// does not exist for tier names (takeover prints `Model metadata for 'X' not found`).
// Only newgate knows which real model a tier maps to, so only we can give this number.
func autoCompactWindow() int {
	return profileValue(func(p *domain.Profile) int { return p.AutoCompactWindow })
}

// profileValue takes a number from the **active profile**; 0 when
// it cannot be obtained (not configured, profile unreadable).
//
// 0 means "do not declare", not "declare 0" -- codex's
// own default is more right than a number we guessed.
//
// The cost, plainly: when the profile changes (a tier bound to another model) this
// number does not follow by itself; `newgate on codex` has to be run again. This
// is the same thing as the docs' "changed injected content needs another takeover".
func profileValue(get func(*domain.Profile) int) int {
	snap, err := store.Load()
	if err != nil || snap == nil {
		return 0
	}
	active := snap.State.ActiveFor(ID)
	for _, p := range snap.Profiles {
		if p.Name == active {
			return get(p)
		}
	}
	return 0
}

// tierFor gets the tier a slot should use at this moment; the empty
// string when there is no such slot (= do not write that line).
//
// It goes through **the slot machinery** (the same knowledge, the same card
// as claude's tier mapping): none of codex's slots have an EnvVar (their
// values are written into the TOML, not injected as env), and confighook's
// Slot allowing an empty EnvVar is exactly the opening left for this case.
//
// Find by **name** rather than by index: the slot table grows (`review_model`
// in addition to `model`), and a shift in indices would write the review
// tier into the main model -- an error completely invisible in the UI.
func tierFor(name string) string {
	for _, s := range Agent().Slots {
		if s.Name == name {
			return agentapi.TierOf(facts{}, s)
		}
	}
	return ""
}
