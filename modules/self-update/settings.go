package selfupdate

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/rzbdz/newgate-ext/modules/config/paths"
	"github.com/rzbdz/newgate-ext/modules/config/store"
	i18n "github.com/rzbdz/newgate/lib/i18n"

	"github.com/rzbdz/newgate-ext/manifest/distinfo"
)

// stateKey is this module's section in state.json (core does not know the key, see
// domain.State.ModuleConfig).
const stateKey = "self-update"

const (
	modeRelease = "release"
	modeGit     = "git"
)

// settings is this module's own section in state.json.
type settings struct {
	// Mode is release or git.
	Mode string `json:"mode"`
	// Repo is the git-mode local checkout (repo root, absolute path).
	Repo string `json:"repo,omitempty"`
	// GitHub overrides the spec's release repo (`owner/repo`). Empty = the spec's - forkers
	// usually edit the spec; this is the hatch for "same binary, another repo".
	GitHub string `json:"github,omitempty"`
	// DecidedBy records who made this decision: first-run (inferred) or user.
	// Human-facing only - the answer to "I never set it, why git?".
	DecidedBy string `json:"decided_by,omitempty"`
}

// github is the release repo for this run: our section's override, else the spec's.
func (s settings) github() string {
	if s.GitHub != "" {
		return s.GitHub
	}
	return distinfo.Current().GitHub
}

// sourceDir is the build-time source dir (tests swap it).
var sourceDir = distinfo.Source

func loadSettings() (settings, bool) {
	raw := store.LoadState().ModuleConfig[stateKey]
	if len(raw) == 0 {
		return settings{}, false
	}
	var s settings
	if err := json.Unmarshal(raw, &s); err != nil || (s.Mode != modeRelease && s.Mode != modeGit) {
		// A corrupted section counts as absent: re-inferring beats swapping binaries on gibberish.
		return settings{}, false
	}
	return s, true
}

// saveSettings writes our section back. Take the config dir's write lock: state.json is
// shared, and another's change between read and write must not be overwritten wholesale.
func saveSettings(s settings) error {
	raw, err := json.Marshal(s)
	if err != nil {
		return err
	}
	_, err = store.WithLock(func() error {
		st := store.LoadState()
		if st.ModuleConfig == nil {
			st.ModuleConfig = map[string][]byte{}
		}
		st.ModuleConfig[stateKey] = raw
		return store.SaveState(st)
	})
	return err
}

// ensureSettings returns the effective settings, inferring (and **only writing**) when unset.
//
// The second value says whether this copy landed on disk.
func ensureSettings() (settings, bool) {
	if s, ok := loadSettings(); ok {
		return s, true
	}
	s, sure := infer(sourceDir(), distinfo.Spec())
	if !sure {
		return s, false
	}
	// state.json absent, write nothing: `newgate init` decides by "does the file exist"
	// whether to seed a default state (port, default profile); an early section is worse.
	if _, err := os.Stat(paths.StateFile()); err != nil {
		return s, false
	}
	s.DecidedBy = "first-run"
	if err := saveSettings(s); err != nil {
		return s, false
	}
	return s, true
}

// infer guesses this machine's route from the build-time source dir.
//
//   - Directory present and a git checkout with build/build.sh and this spec -> git;
//   - directory absent (a CI artifact carries the runner's path) or invisible -> release;
//   - not injected (`go run`, tests) or in a temp dir (the artifact built from an
//     `upgrade@<ref>` temp clone, whose dir still exists on the first `version`) -> no guess.
//
// "Invisible" counts as release, not "no guess": earlier reasoning was "the daemon runs
// as another user and the source is under /root, do not let it write release onto a dev
// machine". On 2026-10-29 the first deployment to snode1 hit the counterexample - that
// machine **also** has /root (0700), and a locally built artifact carried
// /root/workspace/newgate-ext, so ordinary users were forever "inferred, not written".
// But the criterion is simple: a user who cannot read the source **cannot build** it, and
// git mode is meaningless to him; the daemon's Start (in the CLI) wrote git long before.
func infer(source, spec string) (settings, bool) {
	fallback := settings{Mode: modeRelease}
	if source == "" || underTemp(source) {
		return fallback, false
	}
	if _, err := os.Stat(source); err != nil {
		return fallback, true
	}
	if checkRepo(source, spec) != nil {
		// Directory present but not this repo (something else took the path): not "self-built".
		return fallback, true
	}
	return settings{Mode: modeGit, Repo: source}, true
}

func underTemp(dir string) bool {
	tmp := filepath.Clean(os.TempDir()) + string(filepath.Separator)
	return strings.HasPrefix(filepath.Clean(dir)+string(filepath.Separator), tmp)
}

// checkRepo checks whether a dir can serve as the git-mode repo: a git root, with a build
// entry point, and this spec. Missing one, `build/build.sh` cannot build **this** distribution.
func checkRepo(dir, spec string) error {
	if !filepath.IsAbs(dir) {
		return i18n.E("{dir} is not an absolute path", i18n.A{"dir": dir})
	}
	if _, err := os.Stat(filepath.Join(dir, ".git")); err != nil {
		return i18n.E("{dir} is not the root of a git checkout", i18n.A{"dir": dir})
	}
	if _, err := os.Stat(filepath.Join(dir, "build", "build.sh")); err != nil {
		return i18n.E("{dir} has no build/build.sh (is it a newgate distribution repository?)", i18n.A{"dir": dir})
	}
	if _, err := os.Stat(filepath.Join(dir, spec)); err != nil {
		return i18n.E("{dir} has no {spec} (the specification this binary was built from)",
			i18n.A{"dir": dir, "spec": spec})
	}
	return nil
}
