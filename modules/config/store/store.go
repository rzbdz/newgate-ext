// Package store is the Config Store: the single source of truth on disk.
//
// Boundary of responsibility (docs/03-architecture.md):
//   - core has only types and pure logic, not one line of IO
//   - store handles all reads and writes, atomic
//     replacement, env var lookup for keys, schema validation
//   - the upper layer gets an **immutable snapshot**,
//     avoiding races from reading files while resolving
package store

import (
	crand "crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/rzbdz/newgate-ext/modules/config/domain"
	"github.com/rzbdz/newgate-ext/modules/config/paths"
	"github.com/rzbdz/newgate-ext/modules/config/roleprov"
	"github.com/rzbdz/newgate/lib/i18n"
)

// Snapshot a config snapshot read all at once. The resolution
// phase looks only at it, and never goes back to disk.
type Snapshot struct {
	Providers *domain.Providers
	Profiles  []*domain.Profile
	State     *domain.State
}

// Load reads a complete snapshot. A broken profile file is skipped rather than failing
// everything -- one hand-broken file should not stall every agent; doctor reports it.
func Load() (*Snapshot, error) {
	// Module-contributed dynamic role keys (omo's omo-sisyphus / cat-deep ...) are refreshed along
	// with the snapshot: they are config too -- which keys there are, and which tier each defaults to,
	// are written in the module's own files. A read failure does not block loading (fail-open): that
	// module's keys degrade to "not registered", and the user can see why in `newgate omo` / doctor.
	_ = roleprov.Refresh()

	provs, err := LoadProviders()
	if err != nil {
		return nil, err
	}
	names, err := ListProfiles()
	if err != nil {
		return nil, err
	}
	var ps []*domain.Profile
	for _, n := range names {
		if p, err := LoadProfile(n); err == nil {
			ps = append(ps, p)
		}
	}
	return &Snapshot{Providers: provs, Profiles: ps, State: LoadState()}, nil
}

// ---------- atomic writes ----------

// writeJSON writes a JSON config: serialize, then hand it to writeFileAtomic, the one write path.
//
// **Do not assemble `path + ".tmp"` here** -- that was this passage's original spelling,
// and exactly the bug writeFileAtomic was changed for: a fixed temporary name makes
// two concurrent writers collide on the same inode, one renames away, and the other
// renames a copy the first had written halfway. Two implementations coexisting only
// rot one of them slowly (this one was rotten at the time), so call it directly here.
//
// Permissions are no longer a parameter: every caller that gets here wants
// 0660 (the config directory is shared for group reading), writeFileAtomic
// lands that value anyway, and one more parameter is only one more spelling.
func writeJSON(path string, v interface{}) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	return writeFileAtomic(path, b)
}

// Write atomically overwrites a config file, copying the
// current copy into the history ring before the replacement.
//
// For **writes that do not go through CAS**: conversions initiated by a person on the spot,
// like `newgate profile kv --write`, have no "baseline at load" to compare, but must still take
// this path -- a direct os.WriteFile leaves a truncated config when the process dies halfway,
// and **that version has not even a backup** (the backup exists for exactly this). The criterion
// is asymmetric cost: one more rename in exchange for "a truncation = the config is gone".
func Write(path string, data []byte) error { return writeFileAtomic(path, data) }

func readJSON(path string, v interface{}) error {
	b, err := ioutil.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}

// ---------- providers ----------

// LoadProviders reads the provider table, and resolves the
// environment variables api_key_env points at into plaintext.
//
// This resolution is in store rather than core: reading env vars is IO, and core
// must stay pure (domain.Provider.Key() therefore returns only the APIKey field).
func LoadProviders() (*domain.Providers, error) {
	p := &domain.Providers{Providers: map[string]domain.Provider{}}
	if err := readJSON(paths.ProvidersFile(), p); err != nil {
		return nil, err
	}
	if p.Providers == nil {
		p.Providers = map[string]domain.Provider{}
	}
	for name, prov := range p.Providers {
		if prov.APIKey == "" && prov.APIKeyEnv != "" {
			if v := os.Getenv(prov.APIKeyEnv); v != "" {
				prov.APIKey = v
				p.Providers[name] = prov
			}
		}
	}
	return p, nil
}

// SaveProviders writes the provider table.
//
// 0660 rather than 0600: in the shared deployment model (one machine, several users
// in the developer group managing the same newgate, see docs/03) group members must be
// able to read it -- `newgate status` / the wrapper both need to resolve providers. The
// group boundary is the trust boundary; a single-user deployment that wants it tighter
// can chmod 0600 providers.json, and the code will not change it back (it only uses
// this mode when writing). Note: the env var values LoadProviders resolved must not be
// written back, otherwise the key lands on disk. The caller must pass the original form.
func SaveProviders(p *domain.Providers) error {
	return writeJSON(paths.ProvidersFile(), p)
}

// ---------- profiles ----------

// LoadProfile reads one profile, **including extends merging** (a variant writes only
// the differences, the rest is inherited from base, see domain.Profile.MergeFrom).
//
// Two file forms: `name.json` (canonical, written by programs) or `name.kv`
// (hand-writing friendly, see kv.go). When both exist **.kv wins** -- a kv
// is only ever created deliberately, and its appearance is the latest intent;
// `newgate profile kv --write` renames the old json to .bak when converting.
func LoadProfile(name string) (*domain.Profile, error) {
	return loadProfile(name, map[string]bool{})
}

func loadProfile(name string, loading map[string]bool) (*domain.Profile, error) {
	if loading[name] {
		return nil, i18n.E("extends cycle: {name} points back to itself", i18n.A{"name": name})
	}
	pr, err := readProfileFile(name)
	if err != nil {
		return nil, err
	}
	if pr.Name == "" {
		pr.Name = name
	}
	if pr.Extends == "" {
		return pr, nil
	}
	loading[name] = true
	base, err := loadProfile(pr.Extends, loading)
	delete(loading, name)
	if err != nil {
		return nil, i18n.Ef(err, "cannot resolve extends {name}: {err}", i18n.A{"name": pr.Extends})
	}
	pr.MergeFrom(base)
	pr.Extends = "" // 已合并，解析后的视图不再背 extends 声明
	return pr, nil
}

func readProfileFile(name string) (*domain.Profile, error) {
	if b, err := ioutil.ReadFile(filepath.Join(paths.Mappings(), name+".kv")); err == nil {
		return ParseProfileKV(string(b))
	}
	var pr domain.Profile
	if err := readJSON(filepath.Join(paths.Mappings(), name+".json"), &pr); err != nil {
		return nil, err
	}
	return &pr, nil
}

// LoadProfileRaw reads the **unmerged** raw profile (the extends
// declaration kept as is). For editing/conversion: changing one field of
// a compact derived declaration should not expand it into the whole thing.
func LoadProfileRaw(name string) (*domain.Profile, error) {
	pr, err := readProfileFile(name)
	if err != nil {
		return nil, err
	}
	if pr.Name == "" {
		pr.Name = name
	}
	return pr, nil
}

// SaveProfile writes a profile. Contains no key (only provider/model
// bindings), so group-shared reading exposes nothing: the wrapper at start-up
// (launch.Launch) has to resolve it anyway to inject real model names.
func SaveProfile(pr *domain.Profile) error {
	return writeJSON(filepath.Join(paths.Mappings(), pr.Name+".json"), pr)
}

func ListProfiles() ([]string, error) {
	ents, err := ioutil.ReadDir(paths.Mappings())
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var out []string
	for _, e := range ents {
		if e.IsDir() {
			continue
		}
		var n string
		switch {
		case strings.HasSuffix(e.Name(), ".json"):
			n = strings.TrimSuffix(e.Name(), ".json")
		case strings.HasSuffix(e.Name(), ".kv"):
			n = strings.TrimSuffix(e.Name(), ".kv")
		default:
			continue
		}
		// A json/kv with the same name yields only one name (for who wins see LoadProfile)
		if !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	sort.Strings(out)
	return out, nil
}

// ---------- state ----------

// ValidateState answers one thing only: can state.json **be read**.
//
// Why it is needed (2026-09-18): LoadState is fail-open (gives a zero value when it cannot
// read), and that is right -- one hand-broken file should not stall every agent. But two fields
// in a zero-value State are fatal: `DefaultProfile == ""` makes the chain head a nonexistent
// profile (every request "no usable candidate"), and `Port == 0` makes the daemon listen on
// a random port (the CLI and clients look for it at the port in the config, hence "the proxy
// is running but cannot be connected"). And doctor's "files" item used to be only os.Stat --
// **existing means ok** -- so a half JSON was listed in ok and doctor printed "all passed".
//
// The user was then left with a set of mutually contradictory phenomena:
// every config file is there, doctor is all green, but no candidate
// / cannot connect. This item should be reported where he will look.
func ValidateState() error {
	b, err := ioutil.ReadFile(paths.StateFile())
	if err != nil {
		return err
	}
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(b, &probe); err != nil {
		return err
	}
	if len(probe) == 0 {
		return i18n.E("the file is empty", nil)
	}
	// Being parsable at the top level is not enough: what is really
	// needed is that it can fill domain.State (the field types match).
	var st domain.State
	return json.Unmarshal(b, &st)
}

func LoadState() *domain.State {
	s := &domain.State{}
	if b, err := ioutil.ReadFile(paths.StateFile()); err == nil {
		if json.Unmarshal(b, s) == nil {
			var fields map[string]json.RawMessage
			if json.Unmarshal(b, &fields) == nil {
				known := stateJSONFields()
				s.ModuleConfig = make(map[string][]byte)
				for name, raw := range fields {
					if !known[name] {
						s.ModuleConfig[name] = append([]byte(nil), raw...)
					}
				}
			}
		}
	}
	s.Normalize()
	return s
}

func stateJSONFields() map[string]bool {
	typ := reflect.TypeOf(domain.State{})
	fields := make(map[string]bool, typ.NumField())
	for i := 0; i < typ.NumField(); i++ {
		tag := typ.Field(i).Tag.Get("json")
		name := strings.SplitN(tag, ",", 2)[0]
		if name != "" && name != "-" {
			fields[name] = true
		}
	}
	return fields
}

func SaveState(s *domain.State) error {
	b, err := StateBytes(s)
	if err != nil {
		return err
	}
	// Temporary file names are random and in the same directory (see
	// writeFileAtomic): a fixed "<path>.tmp" is a name two concurrent writers
	// will collide on, and a collision means "you renamed someone else's bytes".
	return writeFileAtomic(paths.StateFile(), b)
}

// StateBytes is the bytes SaveState would write: known
// fields + each module's own source-text section.
//
// Exported because **more than one path writes this file** (the command line changes tiers /
// switches, the browser changes the same switch), and the safe one is "read baseline -> compare ->
// atomic write" (WriteIfUnchanged). With only a SaveState, the second writer would either bypass
// the baseline check and write directly (overwriting someone's change, and silently), or assemble
// the serialization itself -- forget ModuleConfig and someone's whole module section vanishes.
//
// The serialization rules exist in only one copy: which keys
// are known fields, which are source text deposited by others.
func StateBytes(s *domain.State) ([]byte, error) {
	typed, err := json.Marshal(s)
	if err != nil {
		return nil, err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(typed, &fields); err != nil {
		return nil, err
	}
	for name, raw := range s.ModuleConfig {
		if _, owned := fields[name]; !owned {
			fields[name] = append(json.RawMessage(nil), raw...)
		}
	}
	b, err := json.MarshalIndent(fields, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

// NewControlToken generates a control endpoint token (crypto/rand, 48 hex digits).
func NewControlToken() string {
	b := make([]byte, 24)
	if _, err := crand.Read(b); err != nil {
		// rand failing almost only happens in VMs early in boot; degrading to time
		// entropy is still better than an empty token (= the endpoint forever 403)
		return fmt.Sprintf("%x", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}

// EnsureControlToken idempotently guarantees state.json
// has a control token, returning the latest state.
//
// Must be called **before** daemon.Spawn (cmdStart / launch.Launch / Serve each catch it once):
// the token lands on disk first, so the daemon's watcher reads it on its first load, and there
// is no window of "the daemon running with an empty token". Afterwards every LoadState->SaveState
// caller (--set-profile etc.) re-reads from disk, and the token is not washed away.
// A failed write **must be reported**: when the token cannot be written, /__newgate/stop and
// /__newgate/upgrade reject every request (cross-user stop is gone), while the caller used to get
// no signal at all. It is the same scene as the umask permission pit recorded in CLAUDE.md §3.1.
func EnsureControlToken() (*domain.State, error) {
	s := LoadState()
	if s.ControlToken != "" {
		return s, nil
	}
	s.ControlToken = NewControlToken()
	if err := SaveState(s); err != nil {
		return s, i18n.Ef(err, "cannot write the control token: {err}", nil)
	}
	return s, nil
}

// SetActiveProfile sets an agent's chain head. An empty agent = set the global default.
//
// Writes only after validating existence; **never falls back silently**
// -- that is exactly where "switched it and nothing happened" comes from.
func SetActiveProfile(agent, profile string) error {
	if _, err := LoadProfile(profile); err != nil {
		avail, _ := ListProfiles()
		return i18n.E("no such profile: {name} (available: {list})",
			i18n.A{"name": profile, "list": strings.Join(avail, ", ")})
	}
	s := LoadState()
	if agent == "" {
		s.DefaultProfile = profile
	} else {
		if s.Active == nil {
			s.Active = map[string]string{}
		}
		s.Active[agent] = profile
	}
	return SaveState(s)
}

// ClearActiveProfile makes an agent fall back to the global default.
func ClearActiveProfile(agent string) error {
	s := LoadState()
	delete(s.Active, agent)
	return SaveState(s)
}

// SetDefaultProfile changes the global default; when
// clearAgents is true, also clears each agent's chain head.
//
// The two things are **written in one go** (one Load + one Save): done in two, state.json
// is half-finished for a moment -- the default already changed, the clients not yet
// converged -- and another edit landing exactly in that moment would be overwritten entirely
// by the later Save. This guards against the same class of accident as "saving is CAS".
func SetDefaultProfile(profile string, clearAgents bool) error {
	if _, err := LoadProfile(profile); err != nil {
		avail, _ := ListProfiles()
		return i18n.E("no such profile: {name} (available: {list})",
			i18n.A{"name": profile, "list": strings.Join(avail, ", ")})
	}
	s := LoadState()
	s.DefaultProfile = profile
	if clearAgents {
		s.Active = nil
	}
	return SaveState(s)
}

// ClearAllAgentProfiles makes **every** agent fall back to
// the global default, returning how many entries were cleared.
//
// It is where "force apply" lands: when changing the global default profile, the agents set on
// their own do not follow -- that is **right** (they were explicitly designated), but what the
// user often wants is "converge everything back to following, then I maintain just one default".
// Clicking through each client is sixteen operations, and this action is that thing itself.
//
// It returns the count so the caller can say "cleared N": an action
// that deletes sixteen lines of config, saying nothing on screen,
// leaves the user unable to confirm whether it did anything at all.
func ClearAllAgentProfiles() (int, error) {
	s := LoadState()
	n := len(s.Active)
	if n == 0 {
		return 0, nil
	}
	s.Active = nil
	return n, SaveState(s)
}

// SetTakeoverWanted records "does the user want this agent taken over"
// (desired state). Changes only the desired state, not touching disk
// -- how takeover is actually done is runtime/takeover's business.
func SetTakeoverWanted(agent string, want bool) error {
	s := LoadState()
	if s.Takeover == nil {
		s.Takeover = map[string]bool{}
	}
	if want {
		// The default is wanting takeover, so "wanted" is
		// expressed by deleting the entry, keeping state.json clean
		delete(s.Takeover, agent)
	} else {
		s.Takeover[agent] = false
	}
	if len(s.Takeover) == 0 {
		s.Takeover = nil
	}
	return SaveState(s)
}

// ---------- validation ----------

// Validate checks that every provider referenced by any profile exists and has a key.
func Validate() []string {
	var problems []string
	provs, err := LoadProviders()
	if err != nil {
		return []string{i18n.T("cannot read providers.json: {err}", i18n.A{"err": err.Error()})}
	}
	names, _ := ListProfiles()
	for _, n := range names {
		pr, err := LoadProfile(n)
		if err != nil {
			problems = append(problems,
				i18n.T("profile {name}: {err}", i18n.A{"name": n, "err": err.Error()}))
			continue
		}
		for tier, cands := range pr.Roles {
			for _, b := range cands {
				p, ok := provs.Providers[b.Provider]
				if !ok {
					problems = append(problems, i18n.T(
						"profile {profile} role {role}: provider {provider} is not defined",
						i18n.A{"profile": n, "role": tier, "provider": b.Provider}))
					continue
				}
				if p.Key() == "" {
					hint := i18n.T("set api_key in providers.json", nil)
					if p.APIKeyEnv != "" {
						hint = i18n.T("set the environment variable {env} or fill in api_key",
							i18n.A{"env": p.APIKeyEnv})
					}
					problems = append(problems, i18n.T("provider {provider} has no key → {hint}",
						i18n.A{"provider": b.Provider, "hint": hint}))
				}
				if b.Model == "" {
					problems = append(problems,
						i18n.T("profile {profile} role {role}: model is empty",
							i18n.A{"profile": n, "role": tier}))
				}
			}
		}
	}
	return problems
}
