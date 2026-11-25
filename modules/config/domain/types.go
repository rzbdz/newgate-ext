// Package domain is entities and value types. **Contains no IO**.
//
// Anyone may depend on it; it depends on no one (docs/03-architecture.md).
package domain

import (
	"strings"
	"time"
)

// Roles semantic tiers. The tool side writes only these names, never a real model name.
//
// Four ladder tiers + one orthogonal vision (expanded from three to four
// on 2026-09-16): the Claude family itself has four, aligning with it
// lets other families (with only large/medium/small) map in naturally:
//
//	heavy  <- fable   most expensive and strongest, kept for work that explicitly names it
//	normal <- opus    **main**: claude's opus slot, opencode's model
//	mid    <- sonnet  classifier / compact summaries / subagent
//	light  <- haiku   small jobs like titling
//	vision            multimodal, orthogonal to the ladder above
//
// The order is capability from high to low; do not reorder
// casually -- `newgate status`, doctor and the TUI all sort by it.
var Roles = []string{"heavy", "normal", "mid", "light", "vision"}

// IsRole tells whether a model name is a semantic tier name (rather than a concrete
// model name). The proxy uses it when receiving the model field from a client to
// tell "tier routing" from "concrete model routing" (docs/04-configuration.md).
func IsRole(s string) bool {
	for _, r := range Roles {
		if r == s {
			return true
		}
	}
	return false
}

const (
	// DefaultPriority the default when a profile does not write priority. A middle
	// value, so the user can insert before (smaller) or place after (larger).
	DefaultPriority = 50
	// ProxyPort is the local gateway listen port when not explicitly configured.
	ProxyPort = 8899
)

// Provider one upstream account.
type Provider struct {
	BaseURL   string `json:"base_url"`
	APIKey    string `json:"api_key,omitempty"`
	APIKeyEnv string `json:"api_key_env,omitempty"` // takes precedence over APIKey
	Protocol  string `json:"protocol,omitempty"`    // openai | anthropic, default openai

	// AnthropicURL this upstream's **Anthropic dialect** base. Empty = both
	// dialects on the same base (all aggregate gateways are like this).
	//
	// Why it is needed: some upstreams put the two dialects on different bases,
	// and do not forward between them. Measured on Volcengine Ark (2026-09-15):
	//
	//	OpenAI dialect    https://ark.cn-beijing.volces.com/api/coding/v3  /chat/completions
	//	Anthropic dialect https://ark.cn-beijing.volces.com/api/coding     /v1/messages
	//
	// The former hits the latter with a 404 (istio-envoy routing-level 404, empty
	// body), and the reverse too. The forwarding layer is pure byte pass-through
	// with no protocol conversion (docs/01-product.md); whatever dialect the
	// client sends is what is sent, so "which base to send to" can only be chosen
	// by **the dialect the client speaks this time** -- that is what this is for.
	//
	// Spelled the same as ANTHROPIC_BASE_URL (**without** /v1): it is the value
	// you would paste into any Anthropic-protocol client tool, and newgate itself appends
	// /v1/messages. A spelling that already has /v1 is accepted too (not appended twice).
	AnthropicURL string `json:"anthropic_url,omitempty"`

	// Models declares the concrete model names a provider can offer, for filtering
	// candidates on an explicit model request; such a request can only switch between
	// providers offering a model of that name, and cannot change the model semantics.
	Models []string `json:"models,omitempty"`
}

// IsAnthropicPath: is this forwarding suffix the
// Anthropic dialect (/messages, /messages/count_tokens).
//
// The criterion is **the path the client sent**, not the provider's protocol:
// protocol says "how to send to the upstream" (auth method, which base), and
// dialect says "what the client speaks". Measured on an aggregate gateway the
// two can disagree -- a provider marked openai still receives /v1/messages.
func IsAnthropicPath(suffix string) bool {
	return suffix == "/messages" || strings.HasPrefix(suffix, "/messages/")
}

// IsGeminiPath: is this forwarding suffix the **native Gemini API** —
// `/v1beta/models/<model>:generateContent` (also `:streamGenerateContent`,
// `:countTokens`)?
//
// Why the path and not the provider's protocol: same rule as IsAnthropicPath. The
// client decides the dialect; the protocol field only says how to authenticate.
func IsGeminiPath(suffix string) bool {
	return strings.HasPrefix(suffix, "/v1beta/")
}

// Base which upstream base this suffix should be sent to (no trailing slash), without a path.
func (p Provider) Base(suffix string) string {
	if p.AnthropicURL != "" && IsAnthropicPath(suffix) {
		return strings.TrimRight(p.AnthropicURL, "/")
	}
	return geminiBase(p.BaseURL, suffix)
}

// URL the full upstream address for this suffix.
//
// When both dialects share a base it is base_url + suffix (the old behavior
// of aggregate gateways). When split, anthropic_url is used and /v1 is appended
// per the ANTHROPIC_BASE_URL convention -- Ark's ".../api/coding" spelling
// works by copying the docs, and one that already has /v1 does not get it twice.
func (p Provider) URL(suffix string) string {
	if p.AnthropicURL != "" && IsAnthropicPath(suffix) {
		b := p.Base(suffix)
		if !strings.HasSuffix(b, "/v1") {
			b += "/v1"
		}
		return b + suffix
	}
	return geminiBase(p.BaseURL, suffix) + suffix
}

// geminiBase strips the OpenAI-era `/v1` off a base for a Gemini-dialect path.
//
// Why: one aggregate gateway serves both dialects from one host — the OpenAI one at
// `<host>/v1/chat/completions`, the native Gemini one at `<host>/v1beta/models/…`
// (probed 2026-11-25). Concatenating naively gives `<host>/v1/v1beta/…`, which 404s.
// A dedicated Gemini base (`https://generativelanguage.googleapis.com`) has no `/v1`
// and is returned unchanged.
func geminiBase(base string, suffix string) string {
	b := strings.TrimRight(base, "/")
	if !IsGeminiPath(suffix) {
		return b
	}
	return strings.TrimSuffix(b, "/v1")
}

// Providers is the root object of providers.json;
// wrapping it explicitly leaves room for format evolution.
type Providers struct {
	Providers map[string]Provider `json:"providers"`
}

// Binding one concrete (provider, model) pair, or one **reference** (Ref non-empty).
type Binding struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
	// Ref non-empty = this entry is not a concrete binding but a reference to another
	// key (written `@normal` / `{"ref":"normal"}`): when resolving, that key's
	// chain is **expanded in place** here. It lets "a slot bound to some tier chain"
	// and "the slot is itself a chain" use the same syntax. For the expansion rules
	// see docs/04-configuration.md; for the implementation see resolve.BuildChain.
	Ref string `json:"ref,omitempty"`
}

// String returns the readable binding form used uniformly by config and diagnostics.
func (b Binding) String() string {
	if b.Ref != "" {
		return "@" + b.Ref
	}
	return b.Provider + "/" + b.Model
}

// IsRef: this entry is a reference rather than a concrete binding.
func (b Binding) IsRef() bool { return b.Ref != "" }

// Profile one set of tier bindings + its position in the fallback chain. It may be **sparse** --
// define only the tiers you care about, the rest fall through to the next profile on the chain.
type Profile struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	// Priority smaller is earlier. When not written, DefaultPriority applies.
	Priority *int `json:"priority,omitempty"`
	// Pinned "when I am the chain head, the chain ends at me"
	// -- when unusable, report an error, do not quietly switch.
	Pinned bool `json:"pinned,omitempty"`
	// Excluded "others must not fall through to me automatically" -- only explicit selection.
	Excluded bool                  `json:"excluded,omitempty"`
	Roles    map[string]Candidates `json:"roles"`
	// Fallback the catch-all for every undefined tier in this profile, equivalent to roles["*"].
	Fallback *Binding `json:"fallback,omitempty"`

	// Extends lets a derived profile write only the differences and
	// inherit the rest from a base profile. The merge rules are centralized
	// in MergeFrom, so store and routing do not each interpret inheritance.
	Extends string `json:"extends,omitempty"`

	// ContextWindow the real context window (in tokens) of the main model; effective
	// only when >0. Why it is needed: the proxy injects real model names (glm-5.3) into
	// the client, and they are not in Claude Code's built-in model catalog, so the client
	// assumes a 200k window for an "unknown model" and compacts early at the slightest
	// provocation. When set, CLAUDE_CODE_MAX_CONTEXT_TOKENS is injected at start-up.
	ContextWindow int `json:"context_window,omitempty"`
	// AutoCompactWindow the target window for auto-compact; effective only when >0, and should
	// be <= ContextWindow (the client takes the min). Injects CLAUDE_CODE_AUTO_COMPACT_WINDOW
	// -- it has the highest precedence in the client's resolution, does not depend on
	// account state, and shows as "(from CLAUDE_CODE_AUTO_COMPACT_WINDOW)" in /context.
	AutoCompactWindow int `json:"auto_compact_window,omitempty"`
}

// MergeFrom fills in base's uncovered entries (Extends' merge rules, called when store loads).
//
// Rules:
//   - scalars (description/priority/fallback/window declarations):
//     what is not written here (zero value) is taken from base;
//   - roles: overridden by tier; tiers base has and this one does not mention are inherited as is;
//   - bare model names (bindings with an empty Provider) **borrow the provider** from base's
//     tier of the same name -- with extends=kimi, writing mid=kimi-k2.7-code-highspeed is enough;
//   - bools (pinned/excluded) are **not inherited**: that is this profile's own stance, not
//     a family attribute. To have a variant excluded too, write it again in the variant.
func (p *Profile) MergeFrom(base *Profile) {
	if p == nil || base == nil {
		return
	}
	if p.Description == "" {
		p.Description = base.Description
	}
	if p.Priority == nil {
		p.Priority = base.Priority
	}
	if p.Fallback == nil {
		p.Fallback = base.Fallback
	}
	if p.ContextWindow == 0 {
		p.ContextWindow = base.ContextWindow
	}
	if p.AutoCompactWindow == 0 {
		p.AutoCompactWindow = base.AutoCompactWindow
	}
	if len(base.Roles) > 0 {
		merged := make(map[string]Candidates, len(base.Roles)+len(p.Roles))
		for k, v := range base.Roles {
			merged[k] = v
		}
		for k, v := range p.Roles {
			// An empty tier = not written, **does not overwrite the parent**. The difference was
			// settled long ago in `CandidatesFor` -- it treats an empty list as sparse (falls
			// through / uses the default), so a `[]` has never been able to say "explicitly
			// do not want this tier"; while overwriting the parent's candidates in inheritance
			// had only one consequence: an old config that was "deleted then added back"
			// (the common UI operation of deleting a tier and adding it back empty) quietly lost
			// the parent's candidates, and that tier resolved to nothing ever after. Pick one of
			// the two semantics: it can only express "not written", so remove the shadowing in
			// inheritance too, making empty = fall to parent/default, consistently. (2026-09-20)
			if len(v) == 0 {
				continue
			}
			merged[k] = fillBareProviders(v, merged[k])
		}
		p.Roles = merged
	}
}

// fillBareProviders borrows the provider from the base candidate of the
// same tier for candidates that have only a model name and no provider. When
// base has none either, it stays empty (the validation layer reports it).
func fillBareProviders(own, base Candidates) Candidates {
	if len(base) == 0 || base[0].Provider == "" {
		return own
	}
	out := make(Candidates, len(own))
	copy(out, own)
	for i, b := range out {
		if b.Provider == "" && b.Model != "" {
			out[i].Provider = base[0].Provider
		}
	}
	return out
}

// Prio returns the explicit priority or the stable
// default, so sorting logic needs no repeated nil handling.
func (p *Profile) Prio() int {
	if p.Priority == nil {
		return DefaultPriority
	}
	return *p.Priority
}

// CandidatesFor returns the candidate list this profile offers for some key (empty = sparse).
//
// The "key" may be a tier (heavy...) or a dynamic role key contributed by a module (omo-sisyphus).
// normal->mid is an in-profile compatibility: an old profile without normal reuses its own mid. A
// module-contributed slot's default is a cross-profile reference instead, expanded by BuildChain.
//
// Note the default may be a **reference** (omo-sisyphus defaults to @normal);
// continuing to expand it is BuildChain's job (with cycle detection and dedup)
// -- here it only translates "equals which key" into the first candidate.
func (p *Profile) CandidatesFor(role string) Candidates {
	if c, ok := p.Roles[role]; ok && len(c) > 0 {
		return c
	}
	// normal is the main tier added after 2026-09-16. Backward compatibility
	// must borrow only the current profile's mid; if it returned @mid, BuildChain
	// would expand the whole cross-profile mid chain again on every profile,
	// creating many false duplicates, and would break sparse-profile semantics.
	if role == "normal" {
		if c, ok := p.Roles["mid"]; ok && len(c) > 0 {
			return c
		}
	}
	if bd, ok := DefaultBindingFor(role); ok {
		// normal's built-in default was already handled above per profile. When
		// the current profile does not even have mid, it should continue with
		// its own wildcard/fallback, rather than expanding the global mid chain.
		if role == "normal" && bd.Ref == "mid" {
			goto profileFallback
		}
		return Candidates{bd}
	}
profileFallback:
	if c, ok := p.Roles["*"]; ok && len(c) > 0 {
		return c
	}
	if p.Fallback != nil {
		return Candidates{*p.Fallback}
	}
	return nil
}

// Resolve takes the first candidate, and it is always a **concrete binding** (references
// are followed to the end). For call sites that need a single binding and will act on it (probe
// really makes a request, status/tui show the real model name) -- they cannot take a reference.
func (p *Profile) Resolve(role string) (Binding, bool) {
	return p.resolve(role, 0)
}

func (p *Profile) resolve(role string, depth int) (Binding, bool) {
	c := p.CandidatesFor(role)
	if len(c) == 0 {
		return Binding{}, false
	}
	bd := c[0]
	if !bd.IsRef() {
		return bd, true
	}
	if depth >= 8 {
		return Binding{}, false // reference cycle / nested too deep
	}
	return p.resolve(bd.Ref, depth+1)
}

// ChainLimits prevents one request from walking the whole
// chain (6 providers x 40s = 4 minutes before failing).
type ChainLimits struct {
	MaxAttempts   int  `json:"max_attempts"` // cap on requests actually sent, summed across both tiers
	TotalBudgetMs int  `json:"total_budget_ms"`
	FallbackOn400 bool `json:"fallback_on_400"` // see docs/04-configuration.md: off by default
}

func (c ChainLimits) Attempts() int {
	if c.MaxAttempts <= 0 {
		return 3
	}
	return c.MaxAttempts
}

func (c ChainLimits) Budget() int {
	if c.TotalBudgetMs <= 0 {
		return 120000
	}
	return c.TotalBudgetMs
}

// Timeouts the gateway's time parameters for waiting on an upstream (milliseconds). Why
// config rather than constants: these values must be tuned to upstream behavior (say whether a
// 12s first byte for non-streaming is too tight), and "edit, rebuild, edit" is an anti-pattern
// -- write them into state.json, the watcher hot-reloads, and they take effect immediately
// (docs/04-configuration.md). All optional, with built-in defaults (see the accessors).
type Timeouts struct {
	// FirstByteMs the cap on waiting for response headers on a streaming
	// request. Default 150s: measured, some channels (anthropic-relay) have a fixed ~43s
	// overhead, and too short a setting would kill requests that would otherwise succeed.
	FirstByteMs int `json:"first_byte_ms,omitempty"`
	// ClassifierFirstByteMs a **classifier-only** tight first-byte cap. Why by identity rather
	// than a blanket "non-streaming": only the Bash permission classifier is on the interactive
	// path -- the user's terminal waits for it to allow, and hanging it = freezing the session;
	// while other non-streaming requests (/compact summaries, titling) do not block interaction
	// and often carry 500KB+ inputs, where prefill is legitimately slow, and a 12s limit would
	// only kill them in a chain reaction (measured 2026-09-09: compact timed out three times in a
	// row + breaker, and the client reported "can't help"). The classifier is identified precisely
	// by a system marker (special.Route). Default 12s; for the false-kill rate see newgate metrics.
	ClassifierFirstByteMs int `json:"classifier_first_byte_ms,omitempty"`
	// TotalMs the total timeout for non-streaming requests (streaming
	// has no total timeout -- long responses would be cut off).
	TotalMs int `json:"total_ms,omitempty"`
}

func (t Timeouts) FirstByte() time.Duration {
	if t.FirstByteMs <= 0 {
		return 150 * time.Second
	}
	return time.Duration(t.FirstByteMs) * time.Millisecond
}

// ClassifierFirstByte the first-byte cap for classifier requests (see the field comment).
func (t Timeouts) ClassifierFirstByte() time.Duration {
	if t.ClassifierFirstByteMs <= 0 {
		return 12 * time.Second
	}
	return time.Duration(t.ClassifierFirstByteMs) * time.Millisecond
}

func (t Timeouts) Total() time.Duration {
	if t.TotalMs <= 0 {
		return 15 * time.Minute
	}
	return time.Duration(t.TotalMs) * time.Millisecond
}

// Key returns this provider's key.
//
// Looking up APIKeyEnv needs reading environment variables -- that is IO, and core should
// not do it. So the store layer fills the env value into APIKey when loading, and core only
// sees the final result. This is one concrete landing of the rule "core imports no IO".
func (p Provider) Key() string { return p.APIKey }

// The key currently still resides in memory as a string; all log
// and dump exits must be redacted uniformly at the gateway layer.

// State machine-local runtime state. Changes often, does not go into dotfiles.
type State struct {
	// Active per-agent chain heads: each agent picks its own profile
	// independently. claude uses expensive, opencode uses cheap,
	// each switching its own -- this is M1's "per-agent switching".
	Active map[string]string `json:"active,omitempty"`
	// DefaultProfile used by agents not listed in Active.
	DefaultProfile string `json:"default_profile"`

	// ActiveProfile / FallbackProfile are v0's old fields, kept only for migration. Old
	// state.json used a single active_profile for the chain head and fallback_profile
	// for the standby. In the new model the chain head is default_profile, and fallback
	// is expressed by the profiles' priority chain. Normalize() migrates the old fields.
	ActiveProfile   string `json:"active_profile,omitempty"`
	FallbackProfile string `json:"fallback_profile,omitempty"`

	Port int `json:"port"`
	// Host is **which address the gateway listens on** (empty = loopback only).
	//
	// Why it must be configurable: binding only 127.0.0.1 by default is the safe tier
	// -- any program on this machine can reach 8899, and 8899 can change config and flip
	// runtime switches. Only someone who wants to open the UI from elsewhere (another
	// container on the same machine, a Tailscale subnet, a LAN) needs to open it up,
	// and that is an **explicit decision**, not one a default should make for the user.
	//
	// The value may be an IP (`100.64.0.1`) or `0.0.0.0` (all interfaces). **When changed to
	// a non-loopback address, the web UI's Host gate must be opened too** (see web-dashboard's
	// allowedHost) -- that gate blocks DNS rebinding, a different thing from the listen address.
	Host      string `json:"host,omitempty"`
	TakenOver bool   `json:"taken_over"`

	// ControlToken the Bearer token for the control endpoint (/__newgate/stop). Why it
	// is needed: in a shared deployment, users in the same group can read this state (0660),
	// but have no kill() permission on a daemon someone else started -- stopping can only go
	// through the proxy's own HTTP endpoint, and the endpoint must verify intent. The token
	// is as secret as providers' keys: a group member who can read it is already trusted to
	// the extent of "can take the upstream key", so stop permission adds no new exposure.
	ControlToken string `json:"control_token,omitempty"`

	// Takeover per-agent takeover intent (desired state). No entry = wants takeover,
	// so `newgate start` takes over everything by default; an explicit false = the user
	// went past it with `newgate off <agent>`, and start should not touch it again.
	//
	// Why it must be persisted: taking over claude uses a PATH shim, and stop must remove
	// it -- otherwise `claude` still hits the shim, the wrapper lazily starts the proxy
	// back up, and it is as if it never stopped. But after removing it, we must remember
	// "the user did want claude taken over", otherwise the next start comes up without taking
	// over, claude connects directly in silence -- the same asymmetry, only reversed. Desired
	// state makes start = plug in, stop = unplug, and neither loses the user's intent.
	Takeover map[string]bool `json:"takeover,omitempty"`

	// Chain the chain's cost upper bound.
	Chain ChainLimits `json:"chain"`

	// Timeouts the gateway's time parameters for waiting on
	// an upstream (hot-reloaded, see the Timeouts comment).
	Timeouts Timeouts `json:"timeouts,omitempty"`

	// ModuleConfig keeps the source text of top-level config core does not
	// recognize. store handles lossless read/write, and each module decodes by the
	// fields it declares; core does not thereby know concepts like Claude or OMO.
	//
	// 2026-09-18: the three switches debug / schema_repair / special_treatment moved from this
	// struct's typed fields into each module's own section here (see modules/gateway/gatewaystate).
	// The criterion is "whose vocabulary this key is" -- they are all the gateway's, and
	// putting them in the shared config would mean changing config to add a gateway switch.
	ModuleConfig map[string][]byte `json:"-"`
}

// BindHost is the address to **actually** bind: loopback when not configured.
//
// Note the nil receiver must be askable too (the short
// stretch of daemon start-up before state is read).
func (s *State) BindHost() string {
	if s == nil || s.Host == "" {
		return "127.0.0.1"
	}
	return s.Host
}

// Normalize fills in defaults. Called after reading from disk.
func (s *State) Normalize() {
	if s.Port == 0 {
		s.Port = ProxyPort
	}
	// Migration: v0's active_profile -> default_profile.
	if s.DefaultProfile == "" && s.ActiveProfile != "" {
		s.DefaultProfile = s.ActiveProfile
	}
	if s.DefaultProfile == "" {
		s.DefaultProfile = "cheap"
	}
	if s.Active == nil {
		s.Active = map[string]string{}
	}
}

// ActiveFor an agent's chain head. The global default when not set on its own.
func (s *State) ActiveFor(agent string) string {
	if p, ok := s.Active[agent]; ok && p != "" {
		return p
	}
	return s.DefaultProfile
}

// TakeoverWanted whether the user wants this agent taken over. No opinion counts as wanting --
// `newgate start` means "take over everything", and should not require registering each one first.
func (s *State) TakeoverWanted(agent string) bool {
	if v, ok := s.Takeover[agent]; ok {
		return v
	}
	return true
}
