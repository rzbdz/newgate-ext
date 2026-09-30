package sshtunnel

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"time"

	modules "github.com/rzbdz/newgate/component"
	i18n "github.com/rzbdz/newgate/lib/i18n"
	viewapi "github.com/rzbdz/newgate/lib/view"
)

// registerView 把这一节挂进概念账本。
//
// 两张卡，各自回答一个问题：
//
//	targets  —— 配了哪些（可写，整份替换）
//	status   —— 此刻连着没有、两条路的地址在哪（只读，跟着刷新走）
//
// 为什么不是一张：可写的那半会让整张卡带上「你有草稿没保存」的状态，而用户常常
// 只是想看一眼「连上没有」——两件事的节奏完全不同。
func registerView(v viewapi.Service, m *manager) (modules.Release, error) {
	section := viewapi.Title(func() string { return i18n.T("Remote", nil) }).
		In(func() string { return i18n.T("Routing", nil) }).
		Of(viewapi.FieldRoutes)
	return v.Register(Name, section, func() ([]viewapi.Concept, error) {
		return []viewapi.Concept{targetsConcept(m.loaded()), statusConcept(m)}, nil
	})
}

// targetsConcept 是那张可写的配置卡。
func targetsConcept(loaded Loaded) viewapi.Concept {
	items := make([]viewapi.Record, 0, len(loaded.Targets))
	for _, t := range loaded.Targets {
		items = append(items, viewapi.Record{
			ID:        t.ID,
			Label:     t.Label,
			Removable: true,
			Fields:    targetFields(t),
		})
	}
	c := viewapi.Concept{
		ID:    "ssh-tunnel.targets",
		Kind:  viewapi.KindRecords,
		Title: i18n.T("Targets", nil),
		Data: viewapi.Records{
			Items:    items,
			Base:     loaded.Base,
			CanAdd:   true,
			AddLabel: i18n.T("add a target", nil),
			// 新增一条时的形状。**必须有**：这份记录集的常态就是「一条都没有」
			// （一台还没配过远端的机器），而界面过去只能去抄一条现成的记录——
			// 没有可抄的时候，点「新增」得到的是一张一个输入框都没有的空卡片。
			Blank: targetFields(Target{}.normalize()),
		},
		Apply: applyTargets,
		Order: 10,
	}
	if len(loaded.Rejected) > 0 {
		// 读不出来的那几条**必须出现在这里**：它们唯一的出口就是这张卡。只写日志
		// 等于「这条配置坏了」这件事只对会翻日志的人可见。
		lines := make([]string, 0, len(loaded.Rejected))
		for _, bad := range loaded.Rejected {
			lines = append(lines, bad.Line)
		}
		c.Broken = i18n.T("some targets were ignored: {list}", i18n.A{"list": strings.Join(lines, "; ")})
	}
	return c
}

// targetFields 是一条 target 的字段表。
//
// 抽出来是因为它有两个用处，而两份必须**逐字相同**：一条已有记录的字段、以及
// 「新增一条」时的字段形状（Records.Blank）。抄成两份的后果是「新增的行」少几个
// 输入框——那种错看起来只像「这个字段本来就没法配」。
func targetFields(t Target) []viewapi.Field {
	return []viewapi.Field{
		{ID: "id", Label: i18n.T("id", nil), Kind: viewapi.FieldText, Value: t.ID,
			Why: i18n.T("machine name: it is the URL segment (/ui/remote/<id>/) and cannot contain / ? # or spaces", nil)},
		{ID: "label", Label: i18n.T("label", nil), Kind: viewapi.FieldText, Value: t.Label},
		{ID: "host", Label: i18n.T("ssh host", nil), Kind: viewapi.FieldText, Value: t.Host,
			Why: i18n.T("a name from ~/.ssh/config, or an address", nil)},
		{ID: "user", Label: i18n.T("user", nil), Kind: viewapi.FieldText, Value: t.User},
		{ID: "port", Label: i18n.T("ssh port", nil), Kind: viewapi.FieldText,
			Value: strconv.Itoa(t.Port), Placeholder: strconv.Itoa(DefaultSSHPort)},
		{ID: "identity", Label: i18n.T("private key", nil), Kind: viewapi.FieldText, Value: t.Identity,
			Placeholder: "~/.ssh/id_ed25519",
			Why:         i18n.T("a path, never key material. Leave empty to use the usual files and ssh-agent", nil)},
		{ID: "remote_host", Label: i18n.T("remote host", nil), Kind: viewapi.FieldText,
			Value: t.RemoteHost, Placeholder: DefaultRemoteHost,
			Why: i18n.T("as seen from the far side", nil)},
		{ID: "remote_port", Label: i18n.T("remote port", nil), Kind: viewapi.FieldText,
			Value: strconv.Itoa(t.RemotePort), Placeholder: strconv.Itoa(DefaultRemotePort)},
		{ID: "local_port", Label: i18n.T("forwarding port", nil), Kind: viewapi.FieldText,
			Value: portOrEmpty(t.LocalPort), Placeholder: i18n.T("none", nil),
			Why: i18n.T("a loopback port forwarded byte-for-byte; empty means this target is reachable "+
				"only through the rewritten route", nil)},
		{ID: "persistent", Label: i18n.T("connection", nil), Kind: viewapi.FieldSelect,
			Value: persistentValue(t.Persistent), Options: []string{"lazy", "persistent"},
			Why: i18n.T("lazy dials on the first request and lets go when idle; persistent keeps it open", nil)},
		{ID: "idle_seconds", Label: i18n.T("idle timeout", nil), Kind: viewapi.FieldText,
			Value: strconv.Itoa(t.IdleSeconds), Placeholder: strconv.Itoa(DefaultIdleSecs)},
		{ID: "host_key", Label: i18n.T("host key", nil), Kind: viewapi.FieldSelect,
			Value: t.HostKey, Options: []string{HostKeyStrict, HostKeyAcceptNew},
			Why: i18n.T("strict refuses an unknown machine; accept-new records it the first time and "+
				"still refuses if it later changes", nil)},
	}
}

// statusConcept 是那张只读的状态表。
func statusConcept(m *manager) viewapi.Concept {
	st := m.Status()
	rows := make([]viewapi.Row, 0, len(st))
	for _, s := range st {
		t := s.Target
		rows = append(rows, viewapi.Row{
			ID: t.ID,
			Cells: map[string]viewapi.Cell{
				"id":     {Text: t.Label},
				"ssh":    {Text: t.User + "@" + t.DialAddr()},
				"remote": {Text: t.RemoteAddr()},
				"state":  {Text: stateText(s), Tone: stateTone(s)},
				"port":   {Text: forwardText(s), Href: forwardHref(s)},
				"route":  {Text: Prefix + "/" + t.ID + "/", Href: Prefix + "/" + t.ID + "/"},
				"use":    {Text: useText(s)},
			},
			Actions: rowActions(m, t, s),
		})
	}
	return viewapi.Concept{
		ID:    "ssh-tunnel.status",
		Kind:  viewapi.KindTable,
		Title: i18n.T("Connections", nil),
		Data: viewapi.Table{
			Columns: []viewapi.Column{
				{ID: "id", Label: i18n.T("target", nil)},
				{ID: "ssh", Label: i18n.T("ssh", nil)},
				{ID: "remote", Label: i18n.T("remote", nil)},
				{ID: "state", Label: i18n.T("state", nil)},
				{ID: "port", Label: i18n.T("forwarding", nil)},
				{ID: "route", Label: i18n.T("route", nil)},
				{ID: "use", Label: i18n.T("traffic", nil), Align: "right"},
			},
			Rows: rows,
		},
		// Live：读的是**内存里的状态**（不拨号、不碰盘），几秒一次刷新很便宜。
		// 与 home 那一节同一条判据（见 lib/view 的 Concept.Live）。
		Live:  true,
		Order: 20,
		Note: &viewapi.Note{
			Text: i18n.T("the route rewrites the remote page's paths; the forwarding port does not — when "+
				"something looks broken over the route, the port is the faithful one", nil),
		},
	}
}

// rowActions 是每一行上那两个按钮。
//
// 「连上/断开」而不是「启用/禁用」：这个模块里**没有一个持久的开关键**——连接是
// 一个当下的东西（懒连接的默认行为就是它自己会断）。所以按下的那一下就该发生一件
// 立刻看得见的事，而不是写一个配置等下一次生效。
func rowActions(m *manager, t Target, s TargetStatus) []viewapi.Action {
	if s.Conn.State == StateUp || s.Conn.State == StateConnecting {
		return []viewapi.Action{{
			ID:    "disconnect",
			Label: func() string { return i18n.T("disconnect", nil) },
			Run: func() (string, error) {
				m.pool.drop(t.ID, i18n.T("asked from the interface", nil))
				return "", nil
			},
			Tone: func() string { return viewapi.ToneWarn },
		}}
	}
	return []viewapi.Action{{
		ID:    "connect",
		Label: func() string { return i18n.T("connect", nil) },
		Run: func() (string, error) {
			// 30 秒的预算：这个动作是用户点出来的，他就在等，而一次 SSH 握手在
			// 内网是几十毫秒。给够余量，但别让他等到浏览器自己超时——先失败的那
			// 一方能给出理由。
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			l, err := m.pool.Acquire(ctx, t)
			if err != nil {
				return "", err
			}
			l.Release()
			return "", nil
		},
	}}
}

// applyTargets 是那张配置卡的写入口：界面交回来的是**全部记录**，没交的就是删了。
//
// 与 config 的 applyProviders 同一个契约（见 core/modules/config/view.go）：整份
// 替换而不是逐条改，因为两处的数据都是**人写的配置**，不是一张会被并发增量更新的表。
func applyTargets(edit json.RawMessage, base string) (string, error) {
	var patch struct {
		Items []struct {
			ID     string            `json:"id"`
			Values map[string]string `json:"values"`
		} `json:"items"`
	}
	if err := json.Unmarshal(edit, &patch); err != nil {
		return "", i18n.Ef(err, "the targets in this request are not readable: {err}", i18n.A{"err": err})
	}

	out := make([]Target, 0, len(patch.Items))
	for _, it := range patch.Items {
		v := it.Values
		t := Target{
			ID:          strings.TrimSpace(v["id"]),
			Label:       strings.TrimSpace(v["label"]),
			Host:        strings.TrimSpace(v["host"]),
			Port:        atoiOr(v["port"], 0),
			User:        strings.TrimSpace(v["user"]),
			Identity:    strings.TrimSpace(v["identity"]),
			RemoteHost:  strings.TrimSpace(v["remote_host"]),
			RemotePort:  atoiOr(v["remote_port"], 0),
			LocalPort:   atoiOr(v["local_port"], 0),
			Persistent:  v["persistent"] == "persistent",
			IdleSeconds: atoiOr(v["idle_seconds"], 0),
			HostKey:     strings.TrimSpace(v["host_key"]),
		}
		if why := validate(t.normalize()); why != "" {
			// **在这里就拒**，不要存下去等到下次读的时候再跳过：用户刚敲完的那一格
			// 就是他正看着的东西，此刻说「id 里不能有斜杠」他立刻知道改哪儿；存下去
			// 之后再报，那一格已经不在他眼前了。
			return "", i18n.E("target {id}: {why}", i18n.A{"id": t.ID, "why": why})
		}
		out = append(out, t.normalize())
	}
	// 重名的在这里就报：save 之后再读会跳过其中一条，而「我明明加了两条」与
	// 「界面上只有一条」之间的差别，用户是看不出来的。
	if dup := firstDuplicate(out); dup != "" {
		return "", i18n.E("two targets are both called \"{id}\"", i18n.A{"id": dup})
	}
	return save(out, base)
}

func firstDuplicate(ts []Target) string {
	seen := map[string]bool{}
	for _, t := range ts {
		if seen[t.ID] {
			return t.ID
		}
		seen[t.ID] = true
	}
	return ""
}

// ---------- 表格里那几格 ----------

func stateText(s TargetStatus) string {
	switch s.Conn.State {
	case StateUp:
		return i18n.T("connected for {d}", i18n.A{"d": humanizeSince(s.Conn.Since)})
	case StateConnecting:
		return i18n.T("connecting", nil)
	case StateDown:
		// 原因**必须在格子里**：这一格是用户唯一能看见「为什么连不上」的地方，
		// 而「failed」两个字会把人送去翻日志。
		return i18n.T("failed: {err}", i18n.A{"err": s.Conn.LastErr})
	default:
		return i18n.T("idle", nil)
	}
}

func stateTone(s TargetStatus) string {
	switch s.Conn.State {
	case StateUp:
		return viewapi.ToneOK
	case StateConnecting:
		return viewapi.ToneWarn
	case StateDown:
		return viewapi.ToneBad
	default:
		return ""
	}
}

func forwardText(s TargetStatus) string {
	if s.ForwardErr != "" {
		return i18n.T("not listening: {err}", i18n.A{"err": s.ForwardErr})
	}
	if s.Forward.Addr == "" {
		return i18n.T("none", nil)
	}
	return s.Forward.Addr
}

// forwardHref 是转发端口那一格的可点地址。
//
// 端口没起来、或者这个 target 压根没要端口时留空——**那正是「这不是一个链接」**。
// 给一个点开就是连不上的地址，比不给链接更糟：用户会以为自己配错了。
func forwardHref(s TargetStatus) string {
	if s.ForwardErr != "" || s.Forward.Addr == "" {
		return ""
	}
	return "http://" + s.Forward.Addr + RemoteUIPrefix
}

func useText(s TargetStatus) string {
	n := int(s.Forward.Accepted) + s.Conn.Reused
	if n == 0 {
		return ""
	}
	return i18n.T("{n} connections", i18n.A{"n": n})
}

func portOrEmpty(n int) string {
	if n == 0 {
		return ""
	}
	return strconv.Itoa(n)
}

func persistentValue(on bool) string {
	if on {
		return "persistent"
	}
	return "lazy"
}

func atoiOr(s string, def int) int {
	s = strings.TrimSpace(s)
	if s == "" {
		return def
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return def
	}
	return n
}
