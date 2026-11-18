package paths

import (
	"os"
	"path/filepath"
)

// pid / lock under the home directory (as required: created in ~ as soon as it starts)
func Home() string {
	h, err := os.UserHomeDir()
	if err != nil || h == "" {
		return "."
	}
	return h
}

// Root is the root directory of all runtime files: `$NEWGATE_HOME` > `$XDG_CONFIG_HOME/newgate`
// > `~/.config/newgate`. pid / lock / logs / config all live under it.
//
// It is exported (since 2026-09-20) for **distributions**: when a product wants to
// put its own things here, it should join sub-paths itself (`filepath.Join(paths.Root(),
// "configshare")`), rather than have the kernel write out every file name for it -- those
// names are the product's knowledge, and writing them in the kernel means the kernel
// knows the product. The names the kernel itself uses are still in the functions below.
func Root() string {
	if v := os.Getenv("NEWGATE_HOME"); v != "" {
		return v
	}
	if v := os.Getenv("XDG_CONFIG_HOME"); v != "" {
		return filepath.Join(v, "newgate")
	}
	return filepath.Join(Home(), ".config", "newgate")
}

func Config() string         { return Root() }
func Mappings() string       { return filepath.Join(Root(), "mappings") }
func ProvidersFile() string  { return filepath.Join(Root(), "providers.json") }
func StateFile() string      { return filepath.Join(Root(), "state.json") }
func HealthFile() string     { return filepath.Join(Root(), "health.json") }
func ProbeCacheFile() string { return filepath.Join(Root(), "probe-capabilities.json") }
func BackupDir() string      { return filepath.Join(Root(), "backups") }

// ShimDir is the PATH shim directory: `newgate on <agent>` puts a link here named
// after the client, and takeover means "make this name earlier on PATH point at us".
//
// It lives here rather than in modules/runtime/injection: **path layout belongs to this package**,
// and this layout has two consumers -- the one that places the link (injection) and **the one
// that decides "is this tool installed at all"** (confighook's OnPath: when searching PATH this
// directory must be skipped, otherwise that criterion is always true, because the same-named file
// in it is one we put there ourselves). Two consumers each spelling the path out is two pieces
// of knowledge that drift, and the symptom of drift is "takeover status starts lying again".
func ShimDir() string { return filepath.Join(Config(), "bin") }

// RelToRoot writes an absolute path as **relative to the
// config root** (falling back to the file name when it cannot).
//
// It serves the `File`/`Path` field in the UI contract: a concept is paired by "which file it is
// about" in two halves (the controls half and the source-text half, see lib/view's Toggles.File),
// so **the same file must be spelled the same way in both places** -- one absolute and one
// relative do not pair up, and in the UI it only looks like "the right column did not appear".
//
// Same as ShimDir: path layout belongs to this package. This logic
// existed in both config and locale before 2026-09-21 (the second was a
// copy), and it is exactly the "knowledge that drifts" mentioned above.
func RelToRoot(path string) string {
	rel, err := filepath.Rel(Root(), path)
	if err != nil {
		return filepath.Base(path)
	}
	return rel
}
func LogFile() string { return filepath.Join(Root(), "newgate.log") }

// ThinkCacheFile is thinkcache's on-disk cold layer: rolls at 100MB, surviving daemon restarts.
// The content is the reasoning source (= conversation content), plaintext like dump, permission
// 0660 following the directory's group-sharing model (visible within the developer group).
func ThinkCacheFile() string { return filepath.Join(Root(), "thinkcache.bin") }

// pid / lock go in the shared config directory, so several users in the same group
// can manage one daemon. NEWGATE_HOME can still isolate all runtime files in tests.
func runtimeDir() string {
	return Root()
}

func PidFile() string  { return filepath.Join(runtimeDir(), ".newgate.pid") }
func LockFile() string { return filepath.Join(runtimeDir(), ".newgate.lock") }

// LegacyPidFile / LegacyLockFile the old pid / lock locations (under $HOME). Only in
// a real deployment (NEWGATE_HOME not set) is it **read** as a fallback: after the upgrade
// to the shared directory, the old daemon still running writes its pid there -- without
// the fallback it "goes missing", can't be stopped, and start collides on the port. In a
// sandbox it must return empty: never let a test's stop follow the trail to the real daemon.
func LegacyPidFile() string {
	if os.Getenv("NEWGATE_HOME") != "" {
		return ""
	}
	return filepath.Join(Home(), ".newgate.pid")
}

func LegacyLockFile() string {
	if os.Getenv("NEWGATE_HOME") != "" {
		return ""
	}
	return filepath.Join(Home(), ".newgate.lock")
}

// EnsureDirs 2770 (group read/write + setgid): under the shared deployment model, other users
// in the group (the claude user in the developer group) must also be able to read config and
// write pid/lock to manage the same daemon. New files inheriting the group's setgid relies
// on the directory bit, and group write stripped by umask is restored with chmod. Existing
// directories are left alone -- permissions are the business of whoever installed it.
func EnsureDirs() error {
	for _, d := range []string{Config(), Mappings(), BackupDir()} {
		if _, err := os.Stat(d); err == nil {
			continue
		}
		if err := os.MkdirAll(d, 0o2770); err != nil {
			return err
		}
		if err := os.Chmod(d, 0o2770); err != nil {
			return err
		}
	}
	return nil
}
