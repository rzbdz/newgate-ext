package sshtunnel

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	i18n "github.com/rzbdz/newgate/lib/i18n"
	"github.com/rzbdz/newgate/lib/style"
	cliapi "github.com/rzbdz/newgate/modules/cli/extension"
	"github.com/rzbdz/newgate/modules/config/domain"
	"github.com/rzbdz/newgate/modules/config/store"
)

// tunnelCommand 是 `newgate tunnel …`。
//
// # 它为什么**不报连接状态**
//
// 每一条 `newgate …` 都是**另一个进程**：它有自己的连接池，而那个池永远是空的。
// 在这里打印「snode1 已连接」会是一句彻底的假话（真的连接在守护进程里，这个进程
// 碰不到它）。所以这个命令只报**配置里的事实**与**它自己现拨的那一次**
// （`tunnel test`），并且把「谁在连着」这件事明确地推给守护进程那一侧（web 界面
// 上那张表）。
//
// 这条取舍值得写下来，因为「顺手把 CLic 的池也打印出来」看起来完全无害——直到
// 有人照着它排查一个「明明连着却报断」的问题。
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

// list 打印配置里的事实与两条路的地址。
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
	// 说清楚为什么这里没有一列「已连接」：那不是这句命令能看到的东西。
	fmt.Println(style.Hint(i18n.T("whether a target is currently connected is the daemon's business — "+
		"this command runs in its own process and cannot see it (the web interface can)", nil)))
	if !host.DaemonRunning() {
		fmt.Println(style.Hint(i18n.T("the daemon is not running: forwarding ports and background "+
			"reconnection only happen there", nil)))
	}
	return 0
}

// reach 说清这个 target 怎么用（转发端口、route、还是两个都有）。
func reach(t Target) string {
	parts := []string{i18n.T("route {path}", i18n.A{"path": Prefix + "/" + t.ID + "/"})}
	if t.LocalPort != 0 {
		parts = append(parts, i18n.T("port http://127.0.0.1:{port}/ui/",
			i18n.A{"port": t.LocalPort}))
	}
	return strings.Join(parts, " · ")
}

// add 加一个 target。
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
			// 同名 = 改（幂等）。报出来，不然「我明明加了」与「它怎么还是老样子」
			// 之间没法分辨。
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

// remove 删一个 target。
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

// setPersistent 是 up/down：只改配置。
//
// 「只改配置」是这条命令的**全部语义**，不是偷懒：它跑在另一个进程里，够不到守护
// 进程那条连接。所以这里明说「守护进程会在两秒内跟上」——比起假装自己连上了、
// 让用户对着一个假的成功去排查，这句话有用得多。
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

// test 真的拨一次，并把每一步报出来。
//
// 它是这一族命令里唯一**不需要守护进程**的：它自己在当前进程里拨号。这也是它最
// 有用的地方——「连不上」的时候，用户需要一个能立刻跑、能指出卡在哪一步的东西，
// 而不是一个要去翻日志的 daemon。
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

// ---------- 状态与体检 ----------

// tunnelStatus 是 `newgate status` 里那一行。
//
// 只报**配置的事实**（几条 target、几条常驻）——见 tunnelCommand 的说明：这个
// provider 在 CLI 进程里跑，那里看不到守护进程的连接。
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

// tunnelDiagnostics 是 `newgate doctor` 里那几项。
//
// 与 status 同一条：报配置里的毛病（读不出来的条目、重复的本地端口），**不报
// 连接**——doctor 也在自己的进程里跑。
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
