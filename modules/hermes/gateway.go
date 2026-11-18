package hermes

import (
	"encoding/json"
	"io/ioutil"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/rzbdz/newgate-ext/modules/config/store"
	"github.com/rzbdz/newgate/lib/i18n"
)

// This file is the **messaging gateway** side of hermes: whether it is running, when it
// started, whether it needs a restart.
//
// # When a restart is needed (conclusion from reading gateway/run.py on 2026-11-04)
//
// Most of the time **not**: the gateway re-reads config.yaml by mtime on every round
// (`_load_gateway_config` → `read_raw_config`); the main loop's model/provider, the `/model`
// list, delegation and most auxiliary tasks are read live. So after a profile changes and we
// sync the model list into the file, the next message sees it -- no restart.
//
// Only one place needs it: **at startup** the gateway copies the provider/model/base_url/
// api_key of `auxiliary.{vision,web_extract,approval}` into `AUXILIARY_*` environment
// variables (run.py 489-526), and the tool side prefers the environment variable. If those
// three cells change (the first takeover always changes them), a gateway already running still
// sends image reading, web summaries and approval to the old upstream -- bypassing newgate,
// invisibly.
//
// So "needs a restart" is not guessed: every time those three cells really change, a timestamp
// is recorded; if the gateway started before it, say so once. Restart or not is the user's call
// (the user on 2026-11-04: "the user can choose not to restart") -- the cost of not restarting
// is just that those three auxiliary call types keep taking the old path for now.

// bridged is the auxiliary tasks the gateway copies into environment variables at startup.
var bridged = map[string]bool{"vision": true, "web_extract": true, "approval": true}

// stateKey is this module's bookkeeping cell in state.json's `module_config`.
const stateKey = "hermes"

// record is the shape of that cell.
type record struct {
	// RestartAfter the gateway must have started after this moment to see what we wrote
	// (unix seconds, 0 = not needed).
	RestartAfter int64 `json:"restart_after,omitempty"`
	// SyncedAt is the last time background sync wrote the model list into the file (unix
	// seconds).
	SyncedAt int64 `json:"synced_at,omitempty"`
	// SyncNote what that change was (for status / the card).
	SyncNote string `json:"sync_note,omitempty"`
	// Target is which file takeover wrote. Background sync honors it, not its own computed
	// path: the daemon's environment is not the user shell's -- the file taken over by
	// `HERMES_HOME=… newgate on hermes` is one the daemon always computes as `~/.hermes`, so it
	// would sync a file nobody uses (or one that does not exist), and the taken-over list would
	// never update again.
	Target string `json:"target,omitempty"`
}

func loadRecord() record {
	var r record
	if raw, ok := store.LoadState().ModuleConfig[stateKey]; ok {
		_ = json.Unmarshal(raw, &r)
	}
	return r
}

func saveRecord(r record) error {
	raw, err := json.Marshal(r)
	if err != nil {
		return err
	}
	s := store.LoadState()
	if s.ModuleConfig == nil {
		s.ModuleConfig = map[string][]byte{}
	}
	s.ModuleConfig[stateKey] = raw
	return store.SaveState(s)
}

// noteApply records this full takeover: which file it wrote (see record.Target), and -- if
// this time touched the cells read only at gateway startup -- "must have restarted after this
// moment".
func noteApply(r *result) {
	rec := loadRecord()
	rec.Target = r.File
	for _, p := range r.Paths {
		if len(p) >= 2 && p[0] == "auxiliary" && bridged[p[1]] {
			rec.RestartAfter = time.Now().Unix()
			break
		}
	}
	_ = saveRecord(rec)
}

// gatewayInfo is what the gateway looks like right now.
type gatewayInfo struct {
	Running bool
	PID     int
	// Started is the startup time; zero value = unknown (not Linux, or the pid file is not
	// recognized).
	Started time.Time
}

// gateway reads `$HERMES_HOME/gateway.pid` (JSON written by hermes itself: pid + starttime
// from /proc), confirms the process is still that one (starttime matches -- pids get reused),
// and converts to a startup time.
func gateway() gatewayInfo {
	raw, err := ioutil.ReadFile(filepath.Join(Home(), "gateway.pid"))
	if err != nil {
		return gatewayInfo{}
	}
	var pf struct {
		PID       int   `json:"pid"`
		StartTime int64 `json:"start_time"`
	}
	if json.Unmarshal(raw, &pf) != nil || pf.PID <= 0 {
		// Older versions wrote a single number.
		pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
		if err != nil || pid <= 0 {
			return gatewayInfo{}
		}
		pf.PID = pid
	}
	if syscall.Kill(pf.PID, 0) != nil {
		return gatewayInfo{}
	}
	g := gatewayInfo{Running: true, PID: pf.PID}
	ticks, ok := procStartTicks(pf.PID)
	if !ok {
		return g
	}
	if pf.StartTime != 0 && ticks != pf.StartTime {
		return gatewayInfo{} // the pid was reused by another process: that is not the gateway
	}
	if boot, ok := bootTime(); ok {
		// CLK_TCK is in practice always 100 on Linux (glibc's sysconf reports so too); Go
		// cannot get it without cgo. The risk of being off by an order of magnitude only makes
		// the "needs a restart" line inaccurate; it changes nothing.
		g.Started = boot.Add(time.Duration(ticks) * 10 * time.Millisecond)
	}
	return g
}

// procStartTicks reads column 22 of /proc/<pid>/stat (the process start time, in ticks since
// boot).
func procStartTicks(pid int) (int64, bool) {
	b, err := ioutil.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return 0, false
	}
	s := string(b)
	// Column 2 is the process name in parentheses, and may contain spaces: count from after
	// the last ')'.
	i := strings.LastIndexByte(s, ')')
	if i < 0 {
		return 0, false
	}
	f := strings.Fields(s[i+1:])
	// After ')' the first column is column 3 (state), so column 22 is f[19].
	if len(f) < 20 {
		return 0, false
	}
	n, err := strconv.ParseInt(f[19], 10, 64)
	return n, err == nil
}

func bootTime() (time.Time, bool) {
	b, err := ioutil.ReadFile("/proc/stat")
	if err != nil {
		return time.Time{}, false
	}
	for _, ln := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(ln, "btime ") {
			n, err := strconv.ParseInt(strings.TrimSpace(ln[6:]), 10, 64)
			if err == nil {
				return time.Unix(n, 0), true
			}
		}
	}
	return time.Time{}, false
}

// restartPending the gateway is running and started before the "must restart after" moment.
// When the start time is unknown, report "needed" -- better one extra prompt than letting
// people believe image-reading calls already go through newgate.
func restartPending() bool {
	rec := loadRecord()
	if rec.RestartAfter == 0 {
		return false
	}
	g := gateway()
	if !g.Running {
		return false
	}
	if g.Started.IsZero() {
		return true
	}
	return g.Started.Unix() < rec.RestartAfter
}

// restartGateway has hermes restart its own gateway: `hermes gateway restart`.
//
// Why borrow its command instead of signaling ourselves / calling systemctl: that command
// knows how the gateway is hosted (systemd user unit, launchd, or a foreground process), and it
// sends SIGUSR1 first so the running sessions **drain** before swapping the process -- killing
// it ourselves would cut a round that is mid-reply.
//
// detach = do not wait for it to finish (for the UI button: drain can take over two minutes, and
// the button must not hang that long).
func restartGateway(detach bool) error {
	bin := realHermes()
	if bin == "" {
		return i18n.E("hermes is not installed on this machine", nil)
	}
	cmd := exec.Command(bin, "gateway", "restart")
	// HERMES_HOME is passed explicitly: the daemon (the button on the web page) most likely
	// does not have it in its environment, and the gateway we must restart is the one of **the
	// taken-over file** (see Home).
	cmd.Env = append(os.Environ(), "HERMES_HOME="+Home())
	if detach {
		cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
		if err := cmd.Start(); err != nil {
			return err
		}
		go func() { _ = cmd.Wait() }()
		return clearRestart()
	}
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return i18n.Ef(err, "hermes gateway restart failed: {err}", i18n.A{"err": err})
	}
	return clearRestart()
}

func clearRestart() error {
	rec := loadRecord()
	if rec.RestartAfter == 0 {
		return nil
	}
	rec.RestartAfter = 0
	return saveRecord(rec)
}
