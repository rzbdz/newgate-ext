package proto

import (
	"encoding/json"
	"fmt"
	"os"
	"path"
	"sort"
	"strings"

	cfg "github.com/rzbdz/newgate-ext/modules/config"
	"github.com/rzbdz/newgate/lib/i18n"
)

// ValidateSnapshot checks a snapshot's shape. **Any bad file rejects the whole thing**.
//
// # Why all-or-nothing rather than skipping the bad one
//
// Because cfg.Load **skips** broken profile files (LoadProfile reads each one; a broken
// one does not affect the others). So a partial materialize **never errors**: it runs
// silently on a mix of old and new - half the tiers from the new host, half from the
// old, and both of them "look normal". That is the hardest fault class to trace.
//
// The returned warnings are "applicable, but you should know" items (dangling refs, no
// key source): syntax is fine and the daemon can load them, only semantics are doubtful.
// Semantics must not starve the fleet of config updates (fail-open).
func ValidateSnapshot(dir string, files map[string][]byte) (warnings []string, err error) {
	providers := map[string]cfg.Provider{}
	profiles := map[string]cfg.Profile{} // 档位名 → 解析后的 profile

	for _, rel := range sortedKeys(files) {
		if _, ok := ManagedPath(rel); !ok {
			return nil, i18n.E("the snapshot carries a file that is not in the managed set: {path} (the protocol never sends files outside the managed set)",
				i18n.A{"path": rel})
		}
		data := files[rel]
		switch {
		case rel == ProvidersName:
			var p cfg.Providers
			if err := json.Unmarshal(data, &p); err != nil {
				return nil, i18n.Ef(err, "cannot parse {path} (not a valid provider table): {err}", i18n.A{"path": rel})
			}
			if p.Providers == nil {
				p.Providers = map[string]cfg.Provider{}
			}
			providers = p.Providers
			// Inline key: **reject**.
			//
			// The criterion is "non-empty literal", not "has an api_key field" - `api_key_env`
			// and leaving it blank (keys come via the secrets channel) are both legitimate.
			// Why reject the whole snapshot: keys only via the secrets channel is the basis
			// of this design, and providers.json is git-managed on the host - inline once
			// and it stays in history forever. On a host that means an old version or a
			// hand edit; both must be stopped, not written in cleartext to every replica.
			var inline []string
			for name, prov := range p.Providers {
				if strings.TrimSpace(prov.APIKey) != "" {
					inline = append(inline, name)
				}
			}
			if len(inline) > 0 {
				sort.Strings(inline)
				return nil, i18n.E(
					"{providers} carries an inline api_key for {names}: a secret may only travel over the secrets channel (newgate config secrets on the host); the config channel never carries one — on the host it is managed by git, so a single commit puts it in history forever",
					i18n.A{"providers": ProvidersName, "names": strings.Join(inline, ", ")})
			}
		case rel == MetaFileName:
			var meta Meta
			if err := json.Unmarshal(data, &meta); err != nil {
				return nil, i18n.Ef(err, "cannot parse {path}: {err}", i18n.A{"path": rel})
			}
		case strings.HasSuffix(rel, ".kv"):
			// Use the daemon's own parser (cfg.ParseProfileKV), not another: a second one
			// would drift, and "validated" would then mean nothing.
			if _, err := cfg.ParseProfileKV(string(data)); err != nil {
				return nil, i18n.Ef(err, "cannot parse profile {path}: {err}", i18n.A{"path": rel})
			}
		case strings.HasSuffix(rel, ".json"):
			var pr cfg.Profile
			if err := json.Unmarshal(data, &pr); err != nil {
				return nil, i18n.Ef(err, "cannot parse profile {path}: {err}", i18n.A{"path": rel})
			}
			name := profileNameOf(rel)
			if pr.Name == "" {
				pr.Name = name
			}
			profiles[name] = pr
		}
	}

	warnings = append(warnings, semanticWarnings(dir, providers, profiles)...)
	return warnings, nil
}

// semanticWarnings checks "syntax fine, semantics doubtful".
//
// All warnings, never rejects: a mistyped provider name must not starve the fleet.
// Host-side publish **rejects** these (fixed on the spot, free); replicas only report.
func semanticWarnings(dir string, providers map[string]cfg.Provider, profiles map[string]cfg.Profile) []string {
	var warns []string

	// Tier names = snapshot's + locally present ones (the local layer).
	// The latter count: a profile extending a machine-private tier is legitimate, and
	// omitting them would emit a false warning every round - then nobody reads warnings.
	known := map[string]bool{}
	for name := range profiles {
		known[name] = true
	}
	if local, err := ListManagedFiles(dir); err == nil {
		for _, rel := range local {
			if strings.HasPrefix(rel, MappingsDir+"/") {
				known[profileNameOf(rel)] = true
			}
		}
	}

	missingProvider := map[string][]string{} // provider 名 → 用到它的档位
	for _, name := range sortedProfileNames(profiles) {
		pr := profiles[name]
		check := func(where string, b cfg.Binding) {
			if b.IsRef() {
				return // 引用由 resolve 展开，展开不了会有它自己的诊断
			}
			if b.Provider == "" {
				return
			}
			if _, ok := providers[b.Provider]; !ok {
				missingProvider[b.Provider] = append(missingProvider[b.Provider], name+"."+where)
			}
		}
		for role, cands := range pr.Roles {
			for i, b := range cands {
				check(fmt.Sprintf("%s[%d]", role, i), b)
			}
		}
		if pr.Fallback != nil {
			check("fallback", *pr.Fallback)
		}
		if pr.Extends != "" && !known[pr.Extends] {
			warns = append(warns, i18n.T("profile {name} extends {extends}, but this configuration has no such profile",
				i18n.A{"name": name, "extends": pr.Extends}))
		}
	}
	for _, prov := range sortedKeysOf(missingProvider) {
		warns = append(warns, i18n.T("profile {profiles} references provider {provider}, but the provider table has no such entry",
			i18n.A{"profiles": strings.Join(missingProvider[prov], ", "), "provider": prov}))
	}

	// Providers with no key source: remind only, never reject - the key may be in the
	// secrets channel, which is not visible here (the two channels are independent).
	for _, name := range sortedProviderNames(providers) {
		prov := providers[name]
		if strings.TrimSpace(prov.APIKey) == "" && strings.TrimSpace(prov.APIKeyEnv) == "" {
			warns = append(warns, i18n.T("provider {name} has no api_key_env; its key has to come from the secrets channel (config secrets on the host)",
				i18n.A{"name": name}))
		}
	}
	return warns
}

// Result is the outcome of one write-to-disk.
type Result struct {
	Written   []string          // 真的改变了内容的文件
	Unchanged []string          // 内容一致、刻意没碰的文件
	Removed   []string          // 这份快照不再包含、因而被删掉的托管文件
	Manifest  map[string]string // 新的 applied 清单（相对路径 → sha256）
}

// Materialize writes a **validated** snapshot to the config dir. All or nothing.
//
// # Why "write + delete", not only write
//
// Write-only leaves `prod` on replicas **forever** after the host renames it to
// `production`: refs like `@prod` keep resolving, and users think it is retired.
// The managed set means "the host decides this group of files", so files we wrote
// that the new snapshot no longer contains must go.
//
// The boundary is explicit: **delete only what we wrote** (the applied list). Local
// layer files (like mappings/local.json) are never touched by this function.
//
// # Two-phase commit
//
// Write every changing file as `.tmp` first, then rename one by one. Each rename is
// atomic and the window between two is microseconds - the watcher polls once a second,
// so collisions are negligible. One file at a time would leave a half set on failure -
// exactly the state ValidateSnapshot's comment says cfg.Load accepts quietly.
//
// Writes go through os.Root throughout: the payload is from the network and names
// must not escape the config dir (`../` was stopped in ManagedPath; this is the
// second gate, and it also covers symlinks, which os.Root does not follow outward).
func Materialize(dir string, files map[string][]byte, applied map[string]string) (Result, error) {
	res := Result{Manifest: map[string]string{}}

	root, err := os.OpenRoot(dir)
	if err != nil {
		return res, i18n.Ef(err, "cannot open the configuration directory {dir}: {err}", i18n.A{"dir": dir})
	}
	defer root.Close()

	// mappings/ may not exist yet (a fresh machine, or one that never wrote a tier).
	//
	// Pass 0o770, not 0o2770 as elsewhere in the repo: **os.Root.MkdirAll validates
	// mode and rejects 0o2770** ("unsupported file mode"). It checks FileMode bits,
	// and setgid is os.ModeSetgid (1<<25) in Go, not 0o2000 - elsewhere
	// os.MkdirAll(d, 0o2770) only takes the low 9 bits and drops setgid silently.
	// Permission bits only here, so no reader thinks setgid took effect.
	if err := root.MkdirAll(MappingsDir, 0o770); err != nil {
		return res, err
	}

	type pending struct{ rel, tmp string }
	var pend []pending

	// ---- phase 1: prepare. Work out what really changed, write their .tmp files ----
	for _, rel := range sortedKeys(files) {
		data := files[rel]
		sum := hashBytes(data)
		res.Manifest[rel] = sum

		if cur, err := root.ReadFile(rel); err == nil && hashBytes(cur) == sum {
			// Identical content is not written. Not an optimization but **semantics**:
			// untouched mtime = same watcher fingerprint = no reload or log line for nothing.
			res.Unchanged = append(res.Unchanged, rel)
			continue
		}
		tmp := tmpNameOf(rel)
		if err := root.WriteFile(tmp, data, 0o660); err != nil {
			return res, i18n.Ef(err, "cannot write {path}: {err}", i18n.A{"path": rel})
		}
		pend = append(pend, pending{rel: rel, tmp: tmp})
	}

	// ---- phase 2: commit. Rename one by one ----
	for _, p := range pend {
		if err := root.Rename(p.tmp, p.rel); err != nil {
			// Renamed ones are on the new version, the rest on the old. Say so, so the
			// caller does not think this was a clean success.
			return res, i18n.Ef(err, "cannot replace {path} (the configuration may be half old and half new; the next sync will retry): {err}",
				i18n.A{"path": p.rel})
		}
		// chmod after rename: umask strips the group write bit, a trap recorded in
		// cfg.writeJSON's comment and CLAUDE.md §3.1 (root-written files daemons can't edit).
		if err := root.Chmod(p.rel, 0o660); err != nil {
			return res, i18n.Ef(err, "cannot fix the permissions of {path}: {err}", i18n.A{"path": p.rel})
		}
		res.Written = append(res.Written, p.rel)
	}

	// ---- phase 3: delete. Files we wrote that this snapshot no longer contains ----
	for _, rel := range sortedApplied(applied) {
		if _, keep := files[rel]; keep {
			continue
		}
		if _, ok := ManagedPath(rel); !ok {
			continue // 已不在托管集合里的老记录，不去碰
		}
		if err := root.Remove(rel); err != nil && !os.IsNotExist(err) {
			return res, i18n.Ef(err, "cannot remove the retired {path}: {err}", i18n.A{"path": rel})
		}
		res.Removed = append(res.Removed, rel)
	}

	return res, nil
}

// ---------- small helpers ----------

// profileNameOf extracts the tier name x from `mappings/x.json` / `mappings/x.kv`.
func profileNameOf(rel string) string {
	base := path.Base(rel)
	return strings.TrimSuffix(strings.TrimSuffix(base, ".json"), ".kv")
}

// tmpNameOf is the temp name in the same dir. It must be the same dir: a rename across
// filesystems degrades to a non-atomic copy (the same reason for cfg.writeJSON's .tmp).
func tmpNameOf(rel string) string {
	dir := path.Dir(rel)
	base := path.Base(rel)
	if dir == "." {
		return "." + base + ".tmp"
	}
	return dir + "/." + base + ".tmp"
}

func sortedKeys(m map[string][]byte) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func sortedApplied(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func sortedProfileNames(m map[string]cfg.Profile) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func sortedProviderNames(m map[string]cfg.Provider) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func sortedKeysOf(m map[string][]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
