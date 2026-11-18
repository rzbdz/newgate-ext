package setup

// This file is the wizard's second act: **write the first upstream as a runnable global config**.
//
// It touches only three files, all through store's single write path (atomic write +
// history before write, see the kernel's config/store/history.go):
//
//	providers.json    add (or update) a provider: base_url / api_key / protocol
//	mappings/default.kv  a profile named default, all four tiers point at that one model
//	state.json       set the global default chain head to default
//
// # Why write .kv and not .json
//
// profile is what users hand-edit most later (swap models, add variants), and .kv is
// one field per line, eyeball-readable (see the kernel's store/kv.go header). When
// `.kv` and `.json` coexist `.kv` wins, so writing it means "this is the latest intent".
//
// # Why all four tiers point at the same model
//
// At this step the user gave one base, one key, one model name (exactly all "works out
// of the box" requires). Point all four tiers there and there is **one complete chain**:
// main loop (normal), classifier and compact (mid), small work (light), explicit heavy.
//
// Leaving any tier empty does not save config: that tier resolves to an empty chain -
// some requests suddenly have "no candidate", and the user cannot see it was never
// filled. Refining tiers is **later** (`newgate tui` / the web), not the bootstrap bar.
//
// vision points there too: unbound, any image request has no candidate. Whether the
// model understands images has no cheap criterion here - pointing there at least lets
// it succeed, and the closing hint says image/multimodal requests use this model too.

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/rzbdz/newgate-ext/modules/config/domain"
	"github.com/rzbdz/newgate-ext/modules/config/paths"
	"github.com/rzbdz/newgate-ext/modules/config/store"
	"github.com/rzbdz/newgate/lib/i18n"
)

// ProviderName is the key bootstrap writes into providers.json.
const ProviderName = "BootstrapProfile"

// ProfileName is the name of the profile bootstrap writes.
const ProfileName = "default"

// Bootstrap is the input for "the first upstream".
type Bootstrap struct {
	// Provider is the key in providers.json (a machine name, the name shown in the UI).
	Provider string
	// BaseURL is the upstream base (with or without /v1; see normalizeBase below).
	BaseURL string
	// APIKey is written in plaintext into providers.json (see the VaultHint note).
	APIKey string
	// Protocol is openai / anthropic (the forwarding dialect). Empty = openai.
	Protocol string
	// Model is the model name all four tiers point at.
	Model string
}

// Profile is the wizard's decision on whether to overwrite global config.
type Profile struct {
	// WriteProfile = write mappings/default.kv (false = update providers only).
	WriteProfile bool
	// SetDefault = set the global default chain head to default.
	SetDefault bool
	// Overwrite = allow overwriting an existing default.kv.
	Overwrite bool
}

// ApplyResult is one write's ledger: which files changed, what was touched.
type ApplyResult struct {
	Files []string
	// Notes: per-item lines for people ("provider added" / "profile overwritten" / "default set").
	Notes []string
	// Warnings are "written, but you should know this".
	Warnings []string
}

// Existing is the scene before the wizard acts: usable config? default already there?
type Existing struct {
	// HasProvider = providers.json already has the bootstrap name.
	HasProvider bool
	// HasProfile = mappings/default.kv (or .json) already exists.
	HasProfile bool
	// HasUsable = the global default profile resolves candidates and the provider has a key.
	//
	// When set, the wizard **leaves config alone** by default: someone already running
	// `newgate config` mostly wants to install/take over clients, not replace their config.
	HasUsable bool
	// DefaultProfile is the global default name right now (for display).
	DefaultProfile string
}

// Inspect reads the scene. Anything unreadable counts as "none" (must run on half-broken config).
func Inspect() Existing {
	e := Existing{}
	provs, err := store.LoadProviders()
	if err == nil {
		_, e.HasProvider = provs.Providers[ProviderName]
	}
	st := store.LoadState()
	e.DefaultProfile = st.DefaultProfile
	if _, err := os.Stat(filepath.Join(paths.Mappings(), ProfileName+".kv")); err == nil {
		e.HasProfile = true
	}
	if _, err := os.Stat(filepath.Join(paths.Mappings(), ProfileName+".json")); err == nil {
		e.HasProfile = true
	}
	e.HasUsable = usable(st.DefaultProfile)
	return e
}

// usable says whether a profile really works now: at least one tier resolves, and every
// provider it uses has a key.
//
// It is much stricter than "the file exists", which is what the wizard needs: a
// placeholder config laid down by `newgate init` has **all its files**, yet cannot run -
// calling that "configured" makes the wizard skip the only step that makes it work.
func usable(profile string) bool {
	if profile == "" {
		return false
	}
	pr, err := store.LoadProfile(profile)
	if err != nil || len(pr.Roles) == 0 {
		return false
	}
	provs, err := store.LoadProviders()
	if err != nil {
		return false
	}
	used := map[string]bool{}
	for _, cands := range pr.Roles {
		for _, b := range cands {
			if b.Ref != "" || b.Provider == "" || b.Model == "" {
				return false
			}
			used[b.Provider] = true
		}
	}
	if len(used) == 0 {
		return false
	}
	for name := range used {
		p, ok := provs.Providers[name]
		if !ok || p.Key() == "" {
			return false
		}
	}
	return true
}

// Apply writes this input into a runnable config.
func Apply(in Bootstrap, opt Profile, v ApplyResult) (ApplyResult, error) {
	res := v
	if strings.TrimSpace(in.Provider) == "" || strings.TrimSpace(in.BaseURL) == "" ||
		strings.TrimSpace(in.Model) == "" {
		return res, i18n.E("a provider name, a base URL and a model are all required", nil)
	}
	if err := paths.EnsureDirs(); err != nil {
		return res, err
	}

	// ---- providers.json ----
	//
	// Read the raw text, change one place: SaveProviders writes the whole table back,
	// so we **must** start from LoadProviders' result - else other providers get wiped
	// by one bootstrap. But LoadProviders resolves api_key_env into plaintext (for the
	// forwarding layer), so writing that puts keys on disk (the kernel warns about this).
	// So read the **raw form**: everything not mentioning our key is kept verbatim;
	// only the plaintext keys of providers resolved from api_key_env must not go back.
	provs, raw, err := loadProvidersRaw()
	if err != nil {
		return res, err
	}
	if _, existed := provs.Providers[in.Provider]; existed {
		res.Notes = append(res.Notes, i18n.T("updated provider {name}", i18n.A{"name": in.Provider}))
	} else {
		res.Notes = append(res.Notes, i18n.T("added provider {name}", i18n.A{"name": in.Provider}))
	}
	provs.Providers[in.Provider] = domain.Provider{
		BaseURL:  normalizeBase(in.BaseURL),
		APIKey:   strings.TrimSpace(in.APIKey),
		Protocol: protocolOr(in.Protocol),
	}
	stripResolvedKeys(provs, raw)
	if err := store.SaveProviders(provs); err != nil {
		return res, err
	}
	res.Files = append(res.Files, paths.ProvidersFile())
	if in.APIKey != "" {
		res.Warnings = append(res.Warnings, VaultHint(in.Provider))
	}

	// ---- mappings/default.kv ----
	if opt.WriteProfile {
		kv := filepath.Join(paths.Mappings(), ProfileName+".kv")
		if _, err := os.Stat(kv); err == nil && !opt.Overwrite {
			res.Warnings = append(res.Warnings, i18n.T(
				"{file} already exists and was left alone; models were not re-pointed",
				i18n.A{"file": kv}))
		} else {
			text := ProfileKV(in)
			if err := store.Write(kv, []byte(text)); err != nil {
				return res, err
			}
			// When a same-named .json coexists, **.kv wins** (see store's read order). Leaving
			// it is a trap: the user opens .json next time and reads a stale binding that does
			// nothing. Renaming to .bak is what the kernel's `profile kv --write` does.
			if old := filepath.Join(paths.Mappings(), ProfileName+".json"); fileExists(old) {
				bak := old + ".bak"
				if _, err := os.Stat(bak); err == nil {
					bak = fmt.Sprintf("%s.%d.bak", old, len(res.Files))
				}
				if err := os.Rename(old, bak); err == nil {
					res.Notes = append(res.Notes, i18n.T("moved {old} aside to {bak} (the .kv wins)",
						i18n.A{"old": old, "bak": bak}))
				}
			}
			res.Files = append(res.Files, kv)
			res.Notes = append(res.Notes, i18n.T("wrote profile {name} ({n} tiers → {model})",
				i18n.A{"name": ProfileName, "n": len(domain.Roles), "model": in.Model}))
		}
	}

	// ---- state.json ----
	if opt.SetDefault {
		if err := store.SetDefaultProfile(ProfileName, false); err != nil {
			return res, err
		}
		res.Files = append(res.Files, paths.StateFile())
		res.Notes = append(res.Notes, i18n.T("global default → {name}", i18n.A{"name": ProfileName}))
	}
	res.Files = dedupe(res.Files)
	return res, nil
}

// ProfileKV is the text written into default.kv.
//
// The comments are **for the next person** (often the user, or an AI they point at it) -
// so they say why the four tiers share one model and how to refine, not just
// "generated". Generated things must be readable, or step one is editing it blind.
func ProfileKV(in Bootstrap) string {
	var b strings.Builder
	b.WriteString("# newgate bootstrap profile — written by `newgate config`.\n")
	b.WriteString("# Every tier points at the same model so that the whole chain works out of the box.\n")
	b.WriteString("# Later, point tiers at different models (or providers) one line at a time:\n")
	b.WriteString("#   normal = other-provider/a-strong-model\n")
	b.WriteString("#   mid    = other-provider/a-cheap-model\n")
	b.WriteString("# `newgate tui` edits the same file interactively.\n")
	b.WriteString("desc=" + i18n.T("bootstrap chain written by newgate config", nil) + "\n")
	binding := in.Provider + "/" + in.Model
	for _, tier := range domain.Roles {
		b.WriteString(tier + "=" + binding + "\n")
	}
	return b.String()
}

// VaultHint is the reminder that "the key is already on disk".
func VaultHint(provider string) string {
	return i18n.T("the key is stored in providers.json (mode 0660, readable by your group); to keep it out of the file, put it in the environment as {env} and set api_key_env instead",
		i18n.A{"env": envName(provider)})
}

// envName is the conventional key-env name for a provider (matches the kernel init hint:
// NEWGATE_KEY_<PROVIDER uppercased, - to _>).
func envName(provider string) string {
	up := strings.ToUpper(provider)
	up = strings.NewReplacer("-", "_", ".", "_", " ", "_").Replace(up)
	return "NEWGATE_KEY_" + up
}

// SuggestedProvider derives a clean name from the base URL (the user may change it).
func SuggestedProvider(base string, taken map[string]bool) string {
	host := base
	if i := strings.Index(host, "://"); i >= 0 {
		host = host[i+3:]
	}
	if i := strings.IndexAny(host, "/?#"); i >= 0 {
		host = host[:i]
	}
	if i := strings.IndexByte(host, '@'); i >= 0 {
		host = host[i+1:]
	}
	if i := strings.IndexByte(host, ':'); i >= 0 { // port
		host = host[:i]
	}
	parts := strings.Split(host, ".")
	// All-numeric (IPv4) is kept whole: splitting `127.0.0.1` into `127` is a nonsense name.
	allNumeric := len(parts) >= 2
	for _, p := range parts {
		for _, r := range p {
			if r < '0' || r > '9' {
				allNumeric = false
				break
			}
		}
	}
	if allNumeric {
		return sanitize(strings.ReplaceAll(host, ".", "-"))
	}
	// Skip low-information labels like www / api (`api.rvcompute.com` -> `rvcompute`).
	i := 0
	for i < len(parts)-1 && (parts[i] == "www" || parts[i] == "api" || parts[i] == "gateway" || parts[i] == "llm") {
		i++
	}
	name := sanitize(parts[i])
	if name == "" {
		name = "provider"
	}
	// A name clash appends a number - silently overwriting another provider is the worst.
	cand := name
	for n := 2; taken != nil && taken[cand]; n++ {
		cand = fmt.Sprintf("%s-%d", name, n)
	}
	return cand
}

func sanitize(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '-' || r == '_' || r == '.':
			b.WriteRune('-')
		}
	}
	return strings.Trim(b.String(), "-")
}

// normalizeBase folds whatever the user pasted into the spelling we store.
//
// Users copy from all sorts of docs: some with `/v1`, some without, some with a
// trailing slash, some with the whole `…/v1/chat/completions`. forward joins "base +
// suffix", so the extra trailing slash and the trailing endpoint must go here - else
// forwarding hits `…/chat/completions/chat/completions`.
func normalizeBase(s string) string {
	s = strings.TrimSpace(s)
	for _, tail := range []string{"/chat/completions", "/messages", "/responses", "/models"} {
		s = strings.TrimSuffix(s, tail)
	}
	return strings.TrimRight(s, "/")
}

func protocolOr(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "anthropic":
		return "anthropic"
	default:
		return "openai"
	}
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

func dedupe(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

// portOf folds "the port in config" into a usable value (0/negative = default 8899).
//
// It is a function so the wizard's tests can feed a port in without writing a
// state.json first - those tests check branches, not store reads and writes.
func portOf(port int) int {
	if port > 0 {
		return port
	}
	return domain.ProxyPort
}
