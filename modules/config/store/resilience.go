package store

// Resilience: one broken profile file must never take the whole data plane down.
//
// # The failure this file exists for (2026-11-25)
//
// A hand-edited profile picked up a key this build did not know. Load() skipped the
// unparseable file **silently** — and that file was the default profile, i.e. the chain head
// of every agent. The snapshot then had no head at all, every request answered "no usable
// candidate", and the only place the real cause was visible was `newgate doctor`. From the
// user's side: "I typed one line and every service died."
//
// The old watcher rule ("a failed load does not replace the snapshot") protects against a
// broken providers.json / state.json, but the per-profile skip went around it: Load() still
// succeeded with fewer profiles, so the watcher dutifully swapped in the degraded page.
//
// # The rule now
//
//   - a profile that fails to parse is replaced by its **last usable version** (the newest
//     parseable backup in the write history, plus the previous in-memory snapshot when the
//     watcher reloads), and the problem is reported loudly;
//   - with nothing to fall back to, the profile is simply absent — and the resolver's
//     head-pinning no longer pins a chain to a profile that does not exist (see
//     forward.pinnedProfileFor), so "fallback off" cannot turn one broken file into an
//     empty chain for every agent.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/rzbdz/newgate-ext/modules/config/domain"
	"github.com/rzbdz/newgate-ext/modules/config/paths"
	"github.com/rzbdz/newgate/lib/i18n"
)

// Problem is one profile file that did not parse, and what Load did about it.
type Problem struct {
	Profile string
	Err     error
	// Used is where the usable copy came from ("" = no fallback existed, the profile is absent).
	Used string
}

// problemsError renders the whole list as one error for the watcher's log callback.
//
// One line per file, with the file's own parse error verbatim: the point of the report is
// "which line, which key", and paraphrasing that is how it becomes useless.
func problemsError(problems []Problem) error {
	var lines []string
	for _, p := range problems {
		line := i18n.T("profile {name} failed to parse: {err}",
			i18n.A{"name": p.Profile, "err": p.Err})
		if p.Used != "" {
			line += i18n.T(" — kept the last usable version ({src})", i18n.A{"src": p.Used})
		} else {
			line += i18n.T(" — this profile is out of every chain until it is fixed", nil)
		}
		lines = append(lines, line)
	}
	return i18n.E("part of the configuration is broken, everything else is serving: {details}",
		i18n.A{"details": strings.Join(lines, "; ")})
}

// loadBackupProfile finds the newest parseable copy of a broken profile in the write history.
//
// Why the history is the right source: `snapshotBeforeWrite` runs on the single write path,
// so it holds the version that was on disk before the bad edit — exactly what the user meant
// to keep. The newest backup that parses wins; a `.kv` profile's history is checked first
// because a `.kv` on disk means the user's latest intent was KV.
func loadBackupProfile(name string) (*domain.Profile, string) {
	for _, ext := range []string{".kv", ".json"} {
		target := filepath.Join(paths.Mappings(), name+ext)
		dir, err := historyDirFor(target)
		if err != nil {
			continue
		}
		ents, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		var names []string
		for _, e := range ents {
			if !e.IsDir() && strings.HasSuffix(e.Name(), ".bak") {
				names = append(names, e.Name())
			}
		}
		// UnixNano names: descending string order is newest first (equal width for years to come).
		sort.Sort(sort.Reverse(sort.StringSlice(names)))
		for _, n := range names {
			path := filepath.Join(dir, n)
			b, err := os.ReadFile(path)
			if err != nil {
				continue
			}
			p, err := parseProfileBytes(b, ext)
			if err != nil {
				continue
			}
			if p.Name == "" {
				// The file name is the profile name (same rule as LoadProfile); a KV backup
				// never carries name=.
				p.Name = name
			}
			return p, path
		}
	}
	return nil, ""
}

// parseProfileBytes parses a profile file's content by extension (the same two forms
// LoadProfile knows; duplicated here because LoadProfile is path-based).
func parseProfileBytes(b []byte, ext string) (*domain.Profile, error) {
	if ext == ".json" {
		var p domain.Profile
		if err := json.Unmarshal(b, &p); err != nil {
			return nil, err
		}
		if p.Roles == nil {
			p.Roles = map[string]domain.Candidates{}
		}
		return &p, nil
	}
	return ParseProfileKV(string(b))
}

// carryOverProfiles fills the names the new snapshot lost from the previous snapshot.
//
// The watcher already keeps the whole old page when Load fails; this is the same rule at
// **profile granularity** for the case where Load succeeds but one file went bad. A running
// session must not lose its chain head because someone saved a typo.
func carryOverProfiles(old, next *Snapshot) *Snapshot {
	if old == nil || next == nil || len(next.Problems) == 0 {
		return next
	}
	have := make(map[string]bool, len(next.Profiles))
	for _, p := range next.Profiles {
		have[p.Name] = true
	}
	for i, prob := range next.Problems {
		if have[prob.Profile] {
			continue
		}
		for _, p := range old.Profiles {
			if p.Name != prob.Profile {
				continue
			}
			next.Profiles = append(next.Profiles, p)
			have[p.Name] = true
			if next.Problems[i].Used == "" {
				next.Problems[i].Used = i18n.T("the previous in-memory snapshot", nil)
			}
			break
		}
	}
	return next
}
