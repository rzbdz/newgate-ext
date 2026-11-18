package proto

import (
	"io/ioutil"
	"path"
	"sort"
	"strings"
)

// The managed file set - **this is the single contract**.
//
// Only the files listed here move around. Everything else under `~/.config/newgate/` is
// **machine-local**: state.json, health.json, thinkcache.bin, dump/, backups/,
// probe-capabilities.json, omo-slots.json, bin/, .newgate.pid, .newgate.lock,
// newgate.log, and secrets.json (its own channel, see secrets.go).
//
// # Why an explicit allowlist, never a directory scan
//
// Two verified pitfalls, both of which break the lazy "scan the directory" approach:
//
//  1. **`newgate profile kv --write` leaves `name.json.bak`** (it renames the old
//     json during conversion). store.readProfileFile only knows `.kv` and `.json`, so
//     that `.bak` is dead - a scan would ship it to every machine, as junk that is
//     never read and counts as a "local change" for the next drift guard.
//  2. **`omo-slots.json` must stay local**. roleprov.WatchFiles() watches it, and
//     `newgate on opencode` generates it per **machine** (which key each intra-agent
//     stably gets). Sharing it would fight local takeover: A's slots copied to B
//     would reference keys B lacks - the symptom is unknown-role, not a crash, so
//     it is very hard to debug.
const (
	// ProvidersName is the provider table in the managed set.
	ProvidersName = "providers.json"
	// MappingsDir is the tier directory in the managed set.
	MappingsDir = "mappings"
)

// ManagedPath decides whether a **slash path relative to the config dir** is in the
// managed set and is safe (cannot escape the config dir). Returns the cleaned path.
//
// This function is the only criterion: host snapshot generation, replica validation
// and drift enumeration all use it. One shared rule, so nothing "sends but won't take".
func ManagedPath(rel string) (string, bool) {
	clean, ok := cleanRel(rel)
	if !ok {
		return "", false
	}
	if clean == ProvidersName || clean == MetaFileName {
		return clean, true
	}
	// mappings/<name>.<json|kv>
	if !strings.HasPrefix(clean, MappingsDir+"/") {
		return "", false
	}
	name := strings.TrimPrefix(clean, MappingsDir+"/")
	if name == "" || strings.Contains(name, "/") {
		return "", false // mappings 下不再有子目录
	}
	// Leading dots are temp/hidden files (our atomic writes make `.<name>.tmp`).
	if strings.HasPrefix(name, ".") {
		return "", false
	}
	switch {
	case strings.HasSuffix(name, ".json"):
		// Note `.json.bak` does not end in `.json`, so it is excluded - exactly pitfall 1.
		return clean, true
	case strings.HasSuffix(name, ".kv"):
		return clean, true
	}
	return "", false
}

// cleanRel normalizes a relative path and rejects shapes that could escape the config dir.
//
// os.Root is the final gate on write (materialize.go); this is the first: bad paths
// must be stopped at **validation** (nothing written yet), not at write time.
func cleanRel(rel string) (string, bool) {
	if rel == "" || strings.ContainsRune(rel, 0) {
		return "", false
	}
	if strings.Contains(rel, `\`) {
		return "", false // 反斜杠在 Linux 上不是分隔符，出现就是有人在试探
	}
	if path.IsAbs(rel) {
		return "", false
	}
	clean := path.Clean(rel)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", false
	}
	// After path.Clean, a path still containing // or a trailing / is rejected (else
	// "mappings//x.json" and "mappings/x.json" would become two ledger entries).
	if clean != rel {
		return "", false
	}
	return clean, true
}

// ListManagedFiles lists the managed files **present locally** (relative, sorted).
//
// For drift detection: it must see "extra local files in the managed set" (local layer).
func ListManagedFiles(dir string) ([]string, error) {
	var out []string
	if _, ok := ManagedPath(ProvidersName); ok {
		if fileExists(dir + "/" + ProvidersName) {
			out = append(out, ProvidersName)
		}
	}
	if _, ok := ManagedPath(MetaFileName); ok {
		if fileExists(dir + "/" + MetaFileName) {
			out = append(out, MetaFileName)
		}
	}
	ents, err := ioutil.ReadDir(dir + "/" + MappingsDir)
	if err != nil {
		if isNotExist(err) {
			sort.Strings(out)
			return out, nil
		}
		return nil, err
	}
	for _, e := range ents {
		if e.IsDir() {
			continue
		}
		if rel, ok := ManagedPath(MappingsDir + "/" + e.Name()); ok {
			out = append(out, rel)
		}
	}
	sort.Strings(out)
	return out, nil
}

// shadowOf returns the relative path of a local file that **shadows** a managed file.
//
// One shape only: store.readProfileFile checks `name.kv`, then `name.json` - so a
// non-managed `mappings/x.kv` makes the managed `mappings/x.json` **take no effect**.
// The nastiest drift: the remote synced, local behavior unchanged, and no error.
func shadowOf(rel string) string {
	if !strings.HasPrefix(rel, MappingsDir+"/") || !strings.HasSuffix(rel, ".json") {
		return ""
	}
	return strings.TrimSuffix(rel, ".json") + ".kv"
}
