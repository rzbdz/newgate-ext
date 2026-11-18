// Package controlplane is the client that talks to the **running
// daemon's** control plane, plus the shape of that state document.
//
// # Why it is a shared leaf (2026-09-18)
//
// This used to live in modules/cli (proxy.go plus a bit of lifecycle.go). The consequence
// was that only the UI could read the daemon's self-reported state, so **every module wanting
// to read it was pinned**: `newgate metrics` (counter semantics belong to gateway), `newgate
// breaker` (the health table belongs to breaker), `newgate probe` (probe results must go
// back to the daemon to be recorded) - each knows its own domain, but the commands could
// only be written in the UI, because the UI was the only place holding that HTTP client.
//
// Extracted into a shared leaf, anyone can read the daemon,
// and those commands could move back to their own modules.
//
// # Why it is not a module
//
// It has no state, no lifecycle, no need to be started or stopped - just a set of
// read/write functions for the loopback control plane (plus a convenience call that
// sends SIGHUP). Same class as the pure tools in lib/, except it knows newgate's
// control-plane protocol, so it stays on the gateway side (the control-plane
// paths and documents are defined by the data plane, see controlpath).
//
// # Dependency direction
//
// controlplane → controlpath (paths), breaker/status (the wire types in the
// document), runtime/daemon (pidfile), lib/httpx. All four are leaves or tools with
// no back edge, so breaker itself, gateway itself and the UI can all import it.
package controlplane

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"syscall"
	"time"

	"github.com/rzbdz/newgate-ext/lib/httpx"
	"github.com/rzbdz/newgate-ext/modules/breaker/status"
	"github.com/rzbdz/newgate-ext/modules/gateway/controlpath"
	"github.com/rzbdz/newgate-ext/modules/runtime/daemon"
	i18n "github.com/rzbdz/newgate/lib/i18n"
)

// Think holds the reasoning cache counters. **Counters only, never the content.**
type Think struct {
	Entries   int   `json:"entries"`
	Bytes     int64 `json:"bytes"`
	MaxBytes  int64 `json:"max_bytes"`
	Hits      int64 `json:"hits"`
	Misses    int64 `json:"misses"`
	Evictions int64 `json:"evictions"`
	// Unparsable is the number of assistant messages in the request that could not be
	// parsed. It ends like Misses (both fill an empty string), but is handled differently:
	// a miss consults the cache and upstream, this looks at the JSON the client sent.
	Unparsable int64 `json:"unparsable"`
}

// Doc is the runtime state the daemon reports (`/__newgate/status`'s document).
//
// Every status page (status / metrics / st) fetches it with **one** request
// - before, each command did its own probing, and the numbers could disagree.
type Doc struct {
	OK       bool              `json:"ok"`
	Port     int               `json:"port"`
	UptimeS  int               `json:"uptime_s"`
	Requests uint64            `json:"requests"`
	Failures uint64            `json:"failures"`
	Breakers []status.Status   `json:"breakers"`
	Handoff  bool              `json:"handoff"`
	Default  string            `json:"default_profile"`
	Active   map[string]string `json:"active"`
	Think    Think             `json:"thinkcache"`
}

// Info is the daemon's process information (from the pidfile).
type Info = daemon.Info

// State gets the daemon's process information + self-reported state.
//
// pid comes from the pidfile (is the process alive), doc from HTTP (what it thinks). Each
// can fail on its own: process alive but port not answering is the classic "outbound proxy
// hijacks loopback" fault, and the two must be expressible separately (see newgate doctor).
func State() (info *Info, doc *Doc) {
	info = daemon.Running()
	if info == nil || info.Port <= 0 {
		return info, nil
	}
	var d Doc
	if !get(info.Port, controlpath.Status, &d) {
		return info, nil
	}
	return info, &d
}

// Metrics gives gateway counters and uptime. It is a separate
// endpoint from status (the counters are a map in daemon memory,
// status reports only a few numbers), so it is fetched separately.
func Metrics(port int) (map[string]uint64, int, bool) {
	var out struct {
		Metrics map[string]uint64 `json:"metrics"`
		UptimeS int               `json:"uptime_s"`
	}
	if !get(port, controlpath.Metrics, &out) {
		return nil, 0, false
	}
	return out.Metrics, out.UptimeS, true
}

// Ping asks whether the control plane on the port is
// alive (HTTP first, falling back to a TCP probe).
func Ping(port int) bool {
	resp, err := httpx.LocalClient(1500 * time.Millisecond).
		Get(fmt.Sprintf("http://127.0.0.1:%d%s", port, controlpath.Status))
	if err == nil {
		defer resp.Body.Close()
		if resp.StatusCode == 200 {
			return true
		}
	}
	return httpx.TCPAlive("127.0.0.1", port, 500*time.Millisecond)
}

// Post sends a token-bearing POST to the control plane.
func Post(port int, path, token string, body, out interface{}) error {
	raw, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequest(http.MethodPost,
		fmt.Sprintf("http://127.0.0.1:%d%s", port, path), bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := httpx.LocalClient(3 * time.Second).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return i18n.E("daemon returned HTTP {status}", i18n.A{"status": resp.StatusCode})
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// Notify makes the running daemon re-read its config at once.
//
// Normally config changes rely on the watcher's 1 second poll, fast enough for
// **hand-edited files**; but a command changing config was just typed by the user
// and must take effect at once, so it sends SIGHUP explicitly to force a reload.
func Notify() {
	if i := daemon.Running(); i != nil {
		_ = syscall.Kill(i.PID, syscall.SIGHUP)
	}
}

func get(port int, path string, v interface{}) bool {
	resp, err := httpx.LocalClient(1500 * time.Millisecond).
		Get(fmt.Sprintf("http://127.0.0.1:%d%s", port, path))
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return false
	}
	return json.NewDecoder(resp.Body).Decode(v) == nil
}

// Available freezes the daemon's global breaker table into a snapshot consistent
// within one command: whether a binding can be used now (the second return value
// is the reason shown to the user). When the daemon is offline it does not judge
// blindly (d == nil ⇒ all available) - diagnosis degrades to static config alone.
//
// **It is a read-only approximation, not the admission in the daemon.** The
// daemon's Admitter has side effects (on cooldown expiry it hands out "half-open
// trial slots", one slot admitting exactly one real request); here we rebuild the
// conclusion from the snapshot's Open state, neither issuing slots nor advancing the
// state machine. The mismatch is **deliberate**: the CLI runs this on every `newgate
// tier` and must never burn a trial slot that is in flight. See docs/05-gateway.md.
func (d *Doc) Available() func(provider, model string) (bool, string) {
	blocked := map[string]string{}
	if d != nil {
		for _, b := range d.Breakers {
			if b.Open {
				reason := i18n.T("circuit open", nil)
				if b.Reason != "" {
					reason = i18n.T("circuit open ({reason})", i18n.A{"reason": b.Reason})
				}
				blocked[b.Provider+"/"+b.Model] = reason
			}
		}
	}
	return func(provider, model string) (bool, string) {
		if reason, ok := blocked[provider+"/"+model]; ok {
			return false, reason
		}
		return true, ""
	}
}

// Rank hands the sort key the daemon computed to the caller unchanged.
//
// **No recomputing thresholds here**: the 3000ms / 12000ms bucketing
// exists only in modules/breaker, and a copy would not follow when the
// daemon changes thresholds (before 2026-09-17 both sides had their own).
//
// Old daemons do not send `rank` (during graceful handoff the CLI and daemon can be
// different versions); when it is missing we fall back to a neutral value - sorting
// degrades to "config order" and versions never fight. Stripped bindings do not sink
// here: admission asks Available first, and a stripped one never becomes a candidate.
func (d *Doc) Rank() func(provider, model string) int {
	const neutral = 1_000_000
	scores := map[string]int{}
	if d != nil {
		for _, b := range d.Breakers {
			score := neutral
			if b.Rank != 0 {
				score = b.Rank
			}
			scores[b.Provider+"/"+b.Model] = score
		}
	}
	return func(provider, model string) int {
		if score, ok := scores[provider+"/"+model]; ok {
			return score
		}
		return neutral
	}
}
