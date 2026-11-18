package proto

import (
	"encoding/json"
	"io/ioutil"
	"os"
	"path/filepath"
	"time"

	paths "github.com/rzbdz/newgate-ext/modules/config/paths"
	"github.com/rzbdz/newgate/lib/i18n"
)

// Roles. This is a machine's **local fact**, not part of the shared config: the same
// shared config is authoritative on the host and a replica everywhere else.
const (
	RoleHost   = "host"
	RoleClient = "client"
)

// SettingsKey is the top-level key configshare owns in the main state.json.
//
// It is registered in the configshare module via confighook.RegisterStateField(
// "configshare", SettingsKey) (same as classifier_naked); registration only rejects
// **cross-module key collisions** early. Reading and writing values stays with the
// module - proto only reads here, and writes go via store.SaveState (the one
// implementation that merges every module's ModuleConfig back losslessly).
const SettingsKey = "config_share"

// Default and clamp range for the poll interval. Clamping blocks a hand-written 0
// (a busy poll) and 86400 (equal to off, but leaving a goroutine around).
const (
	DefaultInterval = 30 * time.Second
	MinInterval     = 10 * time.Second
	MaxInterval     = time.Hour
)

// Settings is this machine's role and pull parameters (the config_share key of state.json).
type Settings struct {
	Role            string `json:"role,omitempty"`             // host | client
	Endpoint        string `json:"endpoint,omitempty"`         // 可拉取的 endpoint，不是"宿主的 ssh 地址"
	IntervalSeconds int    `json:"interval_seconds,omitempty"` // 0 = DefaultInterval
	AutoPull        *bool  `json:"auto_pull,omitempty"`        // nil = 开
}

// IsClient means this machine is a replica (it pulls config).
func (s Settings) IsClient() bool { return s.Role == RoleClient }

// IsHost means this machine is the authority (it distributes config).
func (s Settings) IsHost() bool { return s.Role == RoleHost }

// Configured means a role was set. Unset = this machine does not use config sharing,
// and everything related (background poll, first-bootstrap gate) takes the "unused" branch.
func (s Settings) Configured() bool { return s.IsClient() || s.IsHost() }

// Auto says whether to pull automatically (on unless explicitly disabled).
func (s Settings) Auto() bool { return s.AutoPull == nil || *s.AutoPull }

// Interval is the effective poll interval, clamped to [MinInterval, MaxInterval].
func (s Settings) Interval() time.Duration {
	d := time.Duration(s.IntervalSeconds) * time.Second
	if d <= 0 {
		return DefaultInterval
	}
	if d < MinInterval {
		return MinInterval
	}
	if d > MaxInterval {
		return MaxInterval
	}
	return d
}

// LoadSettings reads config_share from the main state.json.
//
// Why read the file directly rather than store.LoadState: proto knows nothing outside
// store, and this needs one key's raw text. Going through store would turn "read one
// field" into "assemble a full domain.State + migrate old fields", no benefit on a path
// that must be safe to call in **any** state (background polling).
func LoadSettings() (Settings, error) {
	var s Settings
	raw, err := ioutil.ReadFile(paths.StateFile())
	if err != nil {
		if os.IsNotExist(err) {
			return s, nil // 还没有 state.json = 没在用
		}
		return s, err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return s, i18n.Ef(err, "cannot parse state.json: {err}", nil)
	}
	blob, ok := fields[SettingsKey]
	if !ok {
		return s, nil
	}
	if err := json.Unmarshal(blob, &s); err != nil {
		return s, i18n.Ef(err, "cannot parse {key} in state.json: {err}", i18n.A{"key": SettingsKey})
	}
	return s, nil
}

// State is the replica ledger, stored in configshare/state.json.
//
// Why not the main state.json: store/watch.go's signature() watches that file (it stats
// state.json into the fingerprint), so writing a failure counter every 30s would trigger
// a full reload plus a log line every 30s. Poll bookkeeping is high-frequency and
// unrelated to config content, so it must live apart from the config dir.
type State struct {
	// HostID is the last applied authority. Different from the snapshot's HostID = the
	// authority changed, so reset the baseline (types.go explains why otherwise it stalls).
	HostID     string    `json:"host_id,omitempty"`
	Generation int       `json:"generation,omitempty"`
	Version    string    `json:"version,omitempty"`
	AppliedAt  time.Time `json:"applied_at,omitempty"`

	// Applied is "what each file we last wrote looks like": relative path -> sha256.
	// **This is the drift guard's baseline.** Why not mtime: watch.go's comment notes
	// the mtime+size blind spot (same-second rewrites can be missed), and the question
	// here is "did a person change the content" - so hash the content.
	Applied map[string]string `json:"applied,omitempty"`

	// The secrets channel's own ledger. The two generations are separate: merged, every
	// provider edit would look like a secrets change and break "idle is fully silent".
	SecretsGen     int       `json:"secrets_generation,omitempty"`
	SecretsApplied string    `json:"secrets_applied,omitempty"` // secrets.json 的 sha256
	SecretsAt      time.Time `json:"secrets_at,omitempty"`

	// Failure bookkeeping. Used by log suppression (no flooding) and config status.
	FailCount     int       `json:"fail_count,omitempty"`
	LastError     string    `json:"last_error,omitempty"`
	LastErrorAt   time.Time `json:"last_error_at,omitempty"`
	LastSuccessAt time.Time `json:"last_success_at,omitempty"`

	// Drift non-nil = the config channel refused to apply last round because of local
	// edits. It is **persistent state** (not an event): the user must resolve it before
	// the replica accepts remote config again, and status must keep showing it.
	Drift *DriftReport `json:"drift,omitempty"`

	// SecretsDrift is the secrets channel's drift, **stored apart from the config channel**.
	//
	// Why they cannot share a field: the config channel recomputes and rewrites Drift on
	// every successful round, which would wipe the drift the secrets channel recorded
	// while the user has yet to handle it. Two channels, two ledgers, two drifts.
	SecretsDrift *DriftReport `json:"secrets_drift,omitempty"`

	// Warnings are the last apply's warnings (version mismatch, no schema declared...).
	// Kept until the next successful apply so status can show them.
	Warnings []string `json:"warnings,omitempty"`
}

// DriftReport merges both channels' drift into one, for display in status and the like.
//
// Display merges (users care what is not shared from this machine), but **storage must
// stay separate** - see the SecretsDrift comment.
func (s *State) DriftReport() *DriftReport { return mergeDrift(s.Drift, s.SecretsDrift) }

// PendingDrift says whether any channel is stalled on drift (status shows it as actionable).
func (s *State) PendingDrift() bool {
	return s.Drift.Blocking() || s.SecretsDrift.Blocking()
}

// LoadState reads the replica ledger. A missing file is a normal initial state, not an error.
func LoadState() (State, error) {
	var s State
	raw, err := ioutil.ReadFile(ConfigShareStateFile())
	if err != nil {
		if os.IsNotExist(err) {
			return s, nil
		}
		return s, err
	}
	if err := json.Unmarshal(raw, &s); err != nil {
		// A broken ledger must not stop sync entirely: restart from the initial state (cost:
		// one lost drift baseline, rebuilt by the next apply). But say so - swallowed
		// silently, users would see "the drift guard suddenly stopped" with no clue.
		return State{}, i18n.Ef(err, "the configshare bookkeeping file is corrupt; starting over from the initial state: {err}", nil)
	}
	return s, nil
}

// SaveState writes the replica ledger (atomic replace).
func SaveState(s State) error {
	raw, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(ConfigShareStateFile(), append(raw, '\n'), 0o660)
}

// writeFileAtomic is the "same-dir temp file + rename" atomic replace.
//
// Both details have a history:
//   - **same dir**: a rename across filesystems degrades to a non-atomic copy.
//   - **explicit chmod, not umask**: CLAUDE.md §3.1 records this trap - a root CLI
//     writes files umask-stripped to 0640 root:developer, and a daemon running as
//     claude then cannot write them (500 on handoff, thinkcache permission denied).
//     0660 plus the dir's setgid bit is what makes "same group, several users" work.
func writeFileAtomic(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o2770); err != nil {
		return err
	}
	f, err := ioutil.TempFile(dir, "."+filepath.Base(path)+".tmp")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer func() {
		if tmp != "" {
			_ = os.Remove(tmp) // 提前返回时别留垃圾
		}
	}()
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Chmod(mode); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	tmp = "" // rename 成功，别再删
	return nil
}
