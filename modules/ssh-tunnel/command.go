package sshtunnel

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/rzbdz/newgate-ext/lib/style"
	cliapi "github.com/rzbdz/newgate-ext/modules/cli/extension"
	"github.com/rzbdz/newgate-ext/modules/config/domain"
	"github.com/rzbdz/newgate-ext/modules/config/store"
	i18n "github.com/rzbdz/newgate/lib/i18n"
)

// tunnelCommand is `newgate tunnel …`.
//
// # Why it **does not report connection state**
//
// Every `newgate …` is **another process**: it has its own connection pool, always empty.
// Printing "snode1 connected" here would be a flat lie (the real connection is in the
// daemon, unreachable from here). So this command reports **config facts** and **its own
// one dial** (`tunnel test`), leaving "who is connected" explicitly to the daemon side
// (the table in the web UI).
//
// Worth writing down, because "just print the CLI's pool too" looks harmless - until
// someone uses it to debug "says disconnected though it is connected".
type tunnelCommand struct {
	m    *manager
	pool *pool
}

var (
	_ cliapi.Command    = (*tunnelCommand)(nil)
	_ cliapi.Documented = (*tunnelCommand)(nil)
)

func (*tunnelCommand) Names() []string { return []string{"tunnel", "tunnels"} }

func (*tunnelCommand) Help() cliapi.HelpLine {
	return cliapi.HelpLine{
		Section: cliapi.SectionMaintenance,
		Usage:   i18n.T("tunnel [ls|add|rm|up|down|test] [id]", nil),
		Summary: i18n.T("reach a remote machine's web interface over SSH", nil),
	}
}

func (c *tunnelCommand) Run(host cliapi.Host, args []string) int {
	verb := cliapi.Positional(args, 0)
	switch verb {
	case "", "ls", "list":
		return c.list(host)
	case "add":
		return c.add(host, args)
	case "rm", "remove":
		return c.remove(host, args)
	case "up":
		return c.setPersistent(host, args, true)
	case "down":
		return c.setPersistent(host, args, false)
	case "test", "check":
		return c.test(host, args)
	default:
		return host.Die(64, i18n.T("unknown tunnel subcommand {verb} — try ls, add, rm, up, down or test",
			i18n.A{"verb": verb}))
	}
}

// list prints the config facts and the two routes' addresses.
func (c *tunnelCommand) list(host cliapi.Host) int {
	loaded := Load()
	fmt.Println(style.Title("newgate tunnel", ""))
	fmt.Println(style.Rule(72))

	if len(loaded.Targets) == 0 {
		fmt.Println(style.Hint(i18n.T("no targets yet — add one with `newgate tunnel add <id> --host <host>`", nil)))
	} else {
		fmt.Printf("%-14s %-26s %-22s %-8s %s\n",
			i18n.T("id", nil), i18n.T("ssh", nil), i18n.T("remote", nil),
			i18n.T("mode", nil), i18n.T("how to reach it", nil))
		for _, t := range loaded.Targets {
			mode := i18n.T("lazy", nil)
			if t.Persistent {
				mode = i18n.T("always", nil)
			}
			fmt.Printf("%-14s %-26s %-22s %-8s %s\n",
				t.ID, t.User+"@"+t.DialAddr(), t.RemoteAddr(), mode, reach(t))
		}
	}
	for _, bad := range loaded.Rejected {
		fmt.Println(style.Item(style.Bad, bad.Line))
	}

	port := domain.ProxyPort
	if st := store.LoadState(); st != nil && st.Port != 0 {
		port = st.Port
	}
	fmt.Println(style.Hint(i18n.T("the route lives on the shared port: http://127.0.0.1:{port}{prefix}/<id>/",
		i18n.A{"port": port, "prefix": Prefix})))
	// Say clearly why there is no "connected" column: that is not something this command can see.
	fmt.Println(style.Hint(i18n.T("whether a target is currently connected is the daemon's business — "+
		"this command runs in its own process and cannot see it (the web interface can)", nil)))
	if !host.DaemonRunning() {
		fmt.Println(style.Hint(i18n.T("the daemon is not running: forwarding ports and background "+
			"reconnection only happen there", nil)))
	}
	return 0
}

// reach explains how to use this target (forwarded port, route, or both).
func reach(t Target) string {
	parts := []string{i18n.T("route {path}", i18n.A{"path": Prefix + "/" + t.ID + "/"})}
	if t.LocalPort != 0 {
		parts = append(parts, i18n.T("port http://127.0.0.1:{port}/ui/",
			i18n.A{"port": t.LocalPort}))
	}
	return strings.Join(parts, " · ")
}

// add adds a target.
func (c *tunnelCommand) add(host cliapi.Host, args []string) int {
	id := cliapi.Positional(args, 1)
	if id == "" {
		return host.Die(64, i18n.T("usage: newgate tunnel add <id> --host <host> "+
			"[--user u] [--port 22] [--remote-port 8899] [--local-port 0] [--identity path] "+
			"[--persistent] [--host-key strict|accept-new]", nil))
	}
	t := Target{
		ID:          id,
		Label:       cliapi.FlagValue(args, "--label"),
		Host:        cliapi.FlagValue(args, "--host"),
		User:        cliapi.FlagValue(args, "--user"),
		Identity:    cliapi.FlagValue(args, "--identity", "--key"),
		RemoteHost:  cliapi.FlagValue(args, "--remote-host"),
		HostKey:     cliapi.FlagValue(args, "--host-key"),
		Port:        atoiOr(cliapi.FlagValue(args, "--port"), 0),
		RemotePort:  atoiOr(cliapi.FlagValue(args, "--remote-port"), 0),
		LocalPort:   atoiOr(cliapi.FlagValue(args, "--local-port"), 0),
		IdleSeconds: atoiOr(cliapi.FlagValue(args, "--idle"), 0),
		Persistent:  cliapi.Flag(args, "--persistent"),
	}
	if t.Host == "" {
		return host.Die(64, i18n.T("--host is required (a name from ~/.ssh/config works)", nil))
	}
	t = t.normalize()
	if why := validate(t); why != "" {
		return host.Die(64, i18n.T("target {id}: {why}", i18n.A{"id": t.ID, "why": why}))
	}

	loaded := Load()
	next := make([]Target, 0, len(loaded.Targets)+1)
	replaced := false
	for _, cur := range loaded.Targets {
		if cur.ID == t.ID {
			// Same name = edit (idempotent). Report it, or "I added it" and "it is still the
			// old one" cannot be told apart.
			replaced = true
			continue
		}
		next = append(next, cur)
	}
	next = append(next, t)
	sort.Slice(next, func(i, j int) bool { return next[i].ID < next[j].ID })

	if _, err := save(next, loaded.Base); err != nil {
		return host.Die(1, i18n.T("cannot save: {err}", i18n.A{"err": err}))
	}
	if replaced {
		fmt.Println(style.Item(style.OK, i18n.T("{id}: updated", i18n.A{"id": t.ID})))
	} else {
		fmt.Println(style.Item(style.OK, i18n.T("{id}: added", i18n.A{"id": t.ID})))
	}
	fmt.Println(style.Hint(i18n.T("reach it at {url}", i18n.A{
		"url": "http://127.0.0.1:" + portText() + Prefix + "/" + t.ID + "/"})))
	if t.LocalPort != 0 {
		fmt.Println(style.Hint(i18n.T("or byte-for-byte at http://127.0.0.1:{port}/ui/ (started by the daemon)",
			i18n.A{"port": t.LocalPort})))
	}
	return 0
}

// remove deletes one target.
func (c *tunnelCommand) remove(host cliapi.Host, args []string) int {
	id := cliapi.Positional(args, 1)
	if id == "" {
		return host.Die(64, i18n.T("usage: newgate tunnel rm <id>", nil))
	}
	loaded := Load()
	next := make([]Target, 0, len(loaded.Targets))
	found := false
	for _, t := range loaded.Targets {
		if t.ID == id {
			found = true
			continue
		}
		next = append(next, t)
	}
	if !found {
		return host.Die(1, i18n.T("there is no target called {id}", i18n.A{"id": id}))
	}
	if _, err := save(next, loaded.Base); err != nil {
		return host.Die(1, i18n.T("cannot save: {err}", i18n.A{"err": err}))
	}
	fmt.Println(style.Item(style.OK, i18n.T("{id}: removed", i18n.A{"id": id})))
	if host.DaemonRunning() {
		fmt.Println(style.Hint(i18n.T("the daemon closes its connection within a couple of seconds", nil)))
	}
	return 0
}

// setPersistent is up/down: config only.
//
// "Config only" is this command's **whole semantics**, not laziness: it runs in another
// process and cannot reach the daemon's connection. So it says "the daemon catches up in
// two seconds" - far more useful than faking a connection and sending the user debugging a fake.
func (c *tunnelCommand) setPersistent(host cliapi.Host, args []string, on bool) int {
	id := cliapi.Positional(args, 1)
	if id == "" {
		return host.Die(64, i18n.T("usage: newgate tunnel {verb} <id>",
			i18n.A{"verb": map[bool]string{true: "up", false: "down"}[on]}))
	}
	loaded := Load()
	next := make([]Target, 0, len(loaded.Targets))
	found := false
	for _, t := range loaded.Targets {
		if t.ID == id {
			t.Persistent = on
			found = true
		}
		next = append(next, t)
	}
	if !found {
		return host.Die(1, i18n.T("there is no target called {id}", i18n.A{"id": id}))
	}
	if _, err := save(next, loaded.Base); err != nil {
		return host.Die(1, i18n.T("cannot save: {err}", i18n.A{"err": err}))
	}
	if on {
		fmt.Println(style.Item(style.OK, i18n.T("{id}: will stay connected", i18n.A{"id": id})))
	} else {
		fmt.Println(style.Item(style.OK, i18n.T("{id}: dials on demand only", i18n.A{"id": id})))
	}
	if host.DaemonRunning() {
		fmt.Println(style.Hint(i18n.T("the daemon picks this up within a couple of seconds", nil)))
	} else {
		fmt.Println(style.Hint(i18n.T("the daemon is not running — this takes effect when it starts", nil)))
	}
	return 0
}

// test really dials once and reports every step.
//
// It is the only command in this family that **needs no daemon**: it dials in-process.
// That is its value - on "cannot connect" the user needs something to run now that says
// where it stopped, not a daemon whose logs must be dug through.
func (c *tunnelCommand) test(host cliapi.Host, args []string) int {
	id := cliapi.Positional(args, 1)
	loaded := Load()
	targets := loaded.Targets
	if id != "" {
		t, ok := find(loaded, id)
		if !ok {
			return host.Die(1, i18n.T("there is no target called {id}", i18n.A{"id": id}))
		}
		targets = []Target{t}
	} else if len(targets) == 0 {
		return host.Die(1, i18n.T("no targets are configured", nil))
	}

	fmt.Println(style.Title("newgate tunnel test", ""))
	fmt.Println(style.Rule(72))
	bad := 0
	for _, t := range targets {
		fmt.Println(style.Section(t.Label))
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		for _, step := range Probe(ctx, t, c.pool.dialer) {
			fmt.Println(style.Item(tone(step.State), style.Pad(step.Name, 10)+" "+step.Line))
			if step.State == "bad" {
				bad++
			}
		}
		cancel()
	}
	if bad > 0 {
		return 1
	}
	return 0
}

func tone(state string) string {
	switch state {
	case "ok":
		return style.OK
	case "warn":
		return style.Warn
	default:
		return style.Bad
	}
}

func portText() string {
	if st := store.LoadState(); st != nil && st.Port != 0 {
		return fmt.Sprintf("%d", st.Port)
	}
	return fmt.Sprintf("%d", domain.ProxyPort)
}

// ---------- status and checks ----------

// tunnelStatus is the line in `newgate status`.
//
// Reports **config facts** only (how many targets, how many persistent) - see the
// tunnelCommand note: this provider runs in the CLI process, which cannot see the daemon.
type tunnelStatus struct{ m *manager }

var _ cliapi.StatusProvider = tunnelStatus{}

func (s tunnelStatus) Status() []cliapi.StatusLine {
	loaded := Load()
	if len(loaded.Targets) == 0 && len(loaded.Rejected) == 0 {
		return nil
	}
	always := 0
	withPort := 0
	for _, t := range loaded.Targets {
		if t.Persistent {
			always++
		}
		if t.LocalPort != 0 {
			withPort++
		}
	}
	value := i18n.T("{n} targets", i18n.A{"n": len(loaded.Targets)})
	if always > 0 {
		value += ", " + i18n.T("{n} dialling on start", i18n.A{"n": always})
	}
	if withPort > 0 {
		value += ", " + i18n.T("{n} with a forwarding port", i18n.A{"n": withPort})
	}
	if len(loaded.Rejected) > 0 {
		value += ", " + i18n.T("{n} unreadable", i18n.A{"n": len(loaded.Rejected)})
	}
	return []cliapi.StatusLine{{Rank: 60, Label: i18n.T("ssh tunnels", nil), Value: value}}
}

// tunnelDiagnostics is the items in `newgate doctor`.
//
// Same as status: report config faults (unreadable entries, duplicate local ports),
// **not connections** - doctor also runs in its own process.
type tunnelDiagnostics struct{ m *manager }

var _ cliapi.DiagnosticProvider = tunnelDiagnostics{}

func (d tunnelDiagnostics) Diagnostics() []cliapi.Diagnostic {
	loaded := Load()
	if len(loaded.Targets) == 0 && len(loaded.Rejected) == 0 {
		return nil
	}
	out := []cliapi.Diagnostic{{
		Rank:  60,
		Label: i18n.T("ssh tunnels", nil),
		State: i18n.T("{n} configured", i18n.A{"n": len(loaded.Targets)}),
		Line:  i18n.T("remote interfaces reached over SSH", nil),
	}}
	for _, bad := range loaded.Rejected {
		out = append(out, cliapi.Diagnostic{
			Rank:  61,
			Label: i18n.T("ssh tunnel {id}", i18n.A{"id": orDash(bad.ID)}),
			State: i18n.T("unusable", nil),
			Line:  bad.Line,
		})
	}
	return out
}

func orDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}
