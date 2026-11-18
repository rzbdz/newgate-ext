// Package sshtunnel brings a remote machine's newgate webui to the local browser.
//
// # The problem it solves
//
// Intranets often have machines with only SSH open; newgate runs fine there (webui on
// 8899) but the local browser cannot reach it. This module's answer is **through SSH**:
//
//	forwarded local port (faithful)  newgate tunnel up snode1 → http://127.0.0.1:9401/ui/
//	shared-port route               http://127.0.0.1:8899/ui/remote/snode1/   (best effort)
//
// The two are **two product trades**, not "two doors to one implementation":
//
//   - a forwarded port carries a TCP channel verbatim, path untouched - the remote UI is
//     fully faithful, at the cost of one more local port (and "only 8899 open" is exactly
//     this environment's constraint, so it cannot be the only way out).
//   - a route takes no port but **must rewrite paths in responses**: the remote UI's
//     absolute prefix is baked at build time (vite's base "/ui/" in index.html src, CSS
//     url(), and JS strings from import.meta.env.BASE_URL). Unrewritten means white screen.
//     This route is **best effort**, as README and the route's index say - claiming
//     "equivalent" means the first missed address makes us look like liars.
//
// # Why a module
//
// It needs what porthub offers (a prefix on the shared port) and contributes control
// surface (`newgate tunnel` and the web section). As a module it is assembled, removed
// and listed by `newgate plugin` like any service; not in this spec, it disappears.
//
// # Credentials first
//
// Auth is **key files and ssh-agent only** - no passwords, no keyboard-interactive, no
// jump hosts. Consequence: **nothing secret in this module's config**, so it can live in
// state.json (0660, web-editable). `identity` stores a **path**, `host_key` a **policy**,
// neither a credential. A password cannot do this: it either lands in a world-readable
// file or needs a CLI-only store - which would halve "configurable from the web". This
// trade is here because it shapes every field below.
package sshtunnel

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/rzbdz/newgate-ext/modules/config/paths"
	"github.com/rzbdz/newgate-ext/modules/config/store"
)

// StateKey is this module's cell in state.json's ModuleConfig.
//
// Same as the dir name (web-dashboard / locale follow this). A mismatch is quiet: the
// config always reads empty, saving and the UI look fine, and **it is gone after restart**.
const StateKey = "ssh-tunnel"

// HostKey values (see Target.HostKey).
const (
	// HostKeyStrict: refuse if the machine is not in known_hosts (default).
	HostKeyStrict = "strict"
	// HostKeyAcceptNew: record on first sight, then behave as StrictHostKeyChecking=accept-new
	// (a change still refuses). Every real record logs a line - "trusting this machine" is a
	// user-visible event, not a silent internal state change.
	HostKeyAcceptNew = "accept-new"
)

// Defaults, in one place. Spread across normalize and views would give "shown" and
// "actually used" two truths, whose symptom is "I see 8899 but it connects elsewhere".
const (
	DefaultSSHPort    = 22
	DefaultRemoteHost = "127.0.0.1"
	DefaultRemotePort = 8899 // = domain.ProxyPort，远端 newgate 的默认端口
	DefaultIdleSecs   = 300
)

// Target is one remote webui's coordinates, plus how to get there.
//
// All fields optional (except ID/Host), filled by normalize - **normalized once**, at read
// time: views, CLI and dialing all read the normalized copy, so "shown = used" needs no
// three places each repeating the defaults.
type Target struct {
	// ID is a machine marker: identity in state.json, URL path segment (/ui/remote/<id>/),
	// and the value action results echo. **Not translated**, and no `/` (a path segment) or space.
	ID    string `json:"id"`
	Label string `json:"label,omitempty"`

	// How to get there.
	Host     string `json:"host"`
	Port     int    `json:"port,omitempty"`
	User     string `json:"user,omitempty"`
	Identity string `json:"identity,omitempty"` // 私钥**路径**；空 = 默认那几把 + agent

	// What to look at once there. From the **remote machine's** view: remote_host defaults
	// to 127.0.0.1 (the remote webui listens there), remote_port to 8899.
	RemoteHost string `json:"remote_host,omitempty"`
	RemotePort int    `json:"remote_port,omitempty"`

	// LocalPort is the local port to occupy (route one). 0 = none, route only.
	LocalPort int `json:"local_port,omitempty"`

	// Persistent true = connect when the daemon starts, back off and reconnect on loss;
	// false (default) is lazy: dial on the first request, disconnect after idle idle_seconds.
	Persistent  bool `json:"persistent,omitempty"`
	IdleSeconds int  `json:"idle_seconds,omitempty"`

	HostKey string `json:"host_key,omitempty"`
}

// normalize fills a config that **may lack everything** into a usable one.
//
// ID untouched (identity; empty is left for validation to reject). Returns a copy - views
// recompute on the raw struct, and in-place edits fake "one read, one change".
func (t Target) normalize() Target {
	if t.Label == "" {
		t.Label = t.ID
	}
	if t.Port == 0 {
		t.Port = DefaultSSHPort
	}
	if t.RemoteHost == "" {
		t.RemoteHost = DefaultRemoteHost
	}
	if t.RemotePort == 0 {
		t.RemotePort = DefaultRemotePort
	}
	if t.IdleSeconds == 0 {
		t.IdleSeconds = DefaultIdleSecs
	}
	if t.HostKey == "" {
		t.HostKey = HostKeyStrict
	}
	return t
}

// RemoteAddr is the dial target (the address as seen from the remote side).
func (t Target) RemoteAddr() string {
	return fmt.Sprintf("%s:%d", t.RemoteHost, t.RemotePort)
}

// DialAddr is the SSH server's address.
func (t Target) DialAddr() string {
	return fmt.Sprintf("%s:%d", t.Host, t.Port)
}

// Problem is a rejected config entry, with the reason.
//
// Why keep it, not just a log: rejection happens at **config read** (every snapshot), and
// the only places a user sees it are the UI table and `newgate tunnel ls`. Log-only makes
// "this entry is broken" visible only to log readers.
type Problem struct {
	ID   string
	Why  string
	Line string // 界面/终端上那一行原文
}

// Loaded is all of one config read's conclusions.
type Loaded struct {
	Targets  []Target  // 合法且已归一化的，按 ID 排序（顺序稳定，界面才不跳）
	Rejected []Problem // 不合法的：**跳过，但不咽下去**
	// Base is state.json's revision hash right now (store.Revision), used by writable records
	// for CAS - without a baseline there is no telling "is mine the latest", and that is the
	// only defense against "the CLI just changed the same file".
	Base string
}

// Load reads this config.
//
// fail-open: unreadable (broken file, broken JSON) gives an **empty table**, not an error -
// same as web-dashboard reading skin choice: one unreadable preference must not block the
// UI. A single broken entry goes to Rejected; the UI and CLI both report it.
func Load() Loaded {
	out := Loaded{Base: store.Revision(paths.StateFile())}
	raw := store.LoadState().ModuleConfig[StateKey]
	if len(raw) == 0 {
		return out
	}
	var st struct {
		Targets []Target `json:"targets"`
	}
	if err := json.Unmarshal(raw, &st); err != nil {
		out.Rejected = append(out.Rejected, Problem{
			Why:  "the ssh-tunnel section of state.json is not readable JSON",
			Line: err.Error(),
		})
		return out
	}
	seen := map[string]bool{}
	localPorts := map[int]string{}
	for _, t := range st.Targets {
		// **Normalize first, then validate.** The reverse treats "not written" as "wrote 0" -
		// and 0 means "use the default" everywhere here (see normalize). The wrong order's
		// symptom is all entries rejected: it looks like "config did not save" rather than
		// like "validation was too strict".
		n := t.normalize()
		if why := validate(n); why != "" {
			out.Rejected = append(out.Rejected, Problem{ID: t.ID, Why: why,
				Line: fmt.Sprintf("%s: %s", orDash(t.ID), why)})
			continue
		}
		if seen[n.ID] {
			out.Rejected = append(out.Rejected, Problem{ID: n.ID,
				Why:  "two targets share this id",
				Line: fmt.Sprintf("%s: two targets share this id", n.ID)})
			continue
		}
		if n.LocalPort != 0 {
			if other, dup := localPorts[n.LocalPort]; dup {
				out.Rejected = append(out.Rejected, Problem{ID: n.ID,
					Why:  fmt.Sprintf("local port %d is already taken by %s", n.LocalPort, other),
					Line: fmt.Sprintf("%s: local port %d is already taken by %s", n.ID, n.LocalPort, other)})
				continue
			}
			localPorts[n.LocalPort] = n.ID
		}
		seen[n.ID] = true
		out.Targets = append(out.Targets, n)
	}
	sort.Slice(out.Targets, func(i, j int) bool { return out.Targets[i].ID < out.Targets[j].ID })
	return out
}

// validate says why this config is unusable (empty = usable).
//
// Criteria sit next to their fields, not one regex: each is **one field's** constraint,
// and whoever edits the field should see it.
func validate(t Target) string {
	switch {
	case t.ID == "":
		return "id is required"
	case strings.ContainsAny(t.ID, "/?#") || strings.ContainsAny(t.ID, " \t"):
		// `/` would make it two path segments (/ui/remote/a/b/ points who-knows-where), `?`
		// and `#` get eaten by browser URL parsing, and spaces cannot be copied or pasted.
		return "id must not contain / ? # or whitespace — it is a URL path segment"
	case len(t.ID) > 64:
		return "id is longer than 64 characters"
	case t.Host == "":
		return "host is required"
	case t.Port < 1 || t.Port > 65535:
		return fmt.Sprintf("ssh port %d is out of range", t.Port)
	case t.RemotePort < 1 || t.RemotePort > 65535:
		return fmt.Sprintf("remote port %d is out of range", t.RemotePort)
	case t.LocalPort < 0 || t.LocalPort > 65535:
		return fmt.Sprintf("local port %d is out of range", t.LocalPort)
	case t.IdleSeconds < 0:
		return "idle_seconds must not be negative"
	case t.HostKey != "" && t.HostKey != HostKeyStrict && t.HostKey != HostKeyAcceptNew:
		return fmt.Sprintf("host_key %q is neither %q nor %q", t.HostKey, HostKeyStrict, HostKeyAcceptNew)
	}
	return ""
}

// save writes the whole target list back (**whole replacement**, not per-item edits).
//
// Why whole: this file is **a human's config**, not a table updated concurrently. Per-item
// edits need merging "two people changed different rows", and that cost is paid elsewhere
// (the config card's CAS). Whole write + baseline check suffices: stale baseline rereads.
func save(targets []Target, base string) (string, error) {
	// Write disk after normalize: what lands is what will really be used. Without it the
	// file keeps empty "not written" fields, while **defaults filled on read** are the truth
	// - two truths drift (changing a default changes old files' behavior, though unchanged).
	norm := make([]Target, 0, len(targets))
	for _, t := range targets {
		norm = append(norm, t.normalize())
	}
	raw, err := json.Marshal(struct {
		Targets []Target `json:"targets"`
	}{Targets: norm})
	if err != nil {
		return "", err
	}
	st := store.LoadState()
	if st.ModuleConfig == nil {
		st.ModuleConfig = map[string][]byte{}
	}
	st.ModuleConfig[StateKey] = raw

	if base == "" {
		// No baseline (the caller holds a freshly built copy, never compared to disk): write.
		return "", store.SaveState(st)
	}
	data, err := store.StateBytes(st)
	if err != nil {
		return "", err
	}
	return store.WriteIfUnchanged(paths.StateFile(), base, data)
}

// Save is the write entry for the CLI: read-modify-write, taking a baseline if none.
//
// Why the CLI also goes through CAS: the CLI and web UI may edit at once (one typing
// `tunnel add`, another dragging fields). Without a baseline check, the later write
// **silently** clobbers the earlier - and the two changes may be unrelated.
func Save(targets []Target) error {
	_, err := save(targets, store.Revision(paths.StateFile()))
	return err
}
