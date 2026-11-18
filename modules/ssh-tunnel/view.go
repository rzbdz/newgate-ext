package sshtunnel

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"time"

	viewapi "github.com/rzbdz/newgate-ext/lib/view"
	modules "github.com/rzbdz/newgate/component"
	i18n "github.com/rzbdz/newgate/lib/i18n"
)

// registerView mounts this section into the concept ledger.
//
// Two cards, each answering one question:
//
//	targets  -- what is configured (writable, whole-table replace)
//	status   -- connected now, and both paths' addresses (read-only, follows refresh)
//
// Why not one: the writable half would put "you have an unsaved draft" on the whole card,
// while users often just want a glance at "is it connected" - the two have different rhythms.
func registerView(v viewapi.Service, m *manager) (modules.Release, error) {
	section := viewapi.Title(func() string { return i18n.T("Remote", nil) }).
		In(func() string { return i18n.T("Routing", nil) }).
		Of(viewapi.FieldRoutes)
	return v.Register(Name, section, func() ([]viewapi.Concept, error) {
		return []viewapi.Concept{targetsConcept(m.loaded()), statusConcept(m)}, nil
	})
}

// targetsConcept is the writable config card.
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
			// The shape when adding one. **Required**: the normal state of this record set is
			// "not a single one" (a machine never configured for a remote), and the UI used to
			// copy an existing record - with nothing to copy, "add" gave an empty card, no inputs.
			Blank: targetFields(Target{}.normalize()),
		},
		Apply: applyTargets,
		Order: 10,
	}
	if len(loaded.Rejected) > 0 {
		// Entries that cannot be read **must appear here**: this card is their only outlet.
		// Logging only makes "this entry is broken" visible to whoever reads logs.
		lines := make([]string, 0, len(loaded.Rejected))
		for _, bad := range loaded.Rejected {
			lines = append(lines, bad.Line)
		}
		c.Broken = i18n.T("some targets were ignored: {list}", i18n.A{"list": strings.Join(lines, "; ")})
	}
	return c
}

// targetFields is one target's field table.
//
// Extracted because it has two uses, and the two must be **verbatim identical**: the
// fields of an existing record, and the field shape when adding one (Records.Blank).
// Two copies make "add" rows lose inputs - which just looks like "that field is not configurable".
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

// statusConcept is the read-only status table.
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
		// Live: it reads **in-memory state** (no dialing, no disk), so a few-second refresh is cheap.
		// Same criterion as the home section (see lib/view's Concept.Live).
		Live:  true,
		Order: 20,
		Note: &viewapi.Note{
			Text: i18n.T("the route rewrites the remote page's paths; the forwarding port does not — when "+
				"something looks broken over the route, the port is the faithful one", nil),
		},
	}
}

// rowActions are the two buttons on each row.
//
// "Connect/disconnect" rather than "enable/disable": this module has **no persistent
// switch** - a connection is a thing of the moment (lazy connect drops on its own). So a
// press should cause something immediately visible, not write config that takes effect later.
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
			// A 30s budget: the user triggered this and is waiting, and an SSH handshake on a
			// LAN takes tens of milliseconds. Give slack, but do not make them wait for the
			// browser to time out - whoever fails first can give a reason.
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

// applyTargets is the config card's write entry: **all records** come back, missing = deleted.
//
// Same contract as config's applyProviders (see core/modules/config/view.go): whole-table
// replace, not per-row edits - the data is **hand-written config**, not a concurrently updated one.
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
			// **Reject right here**, do not store and skip on the next read: the field they just
			// typed is what they are looking at, and "no slashes in id" tells them exactly where to
			// fix it; report after saving and that field is no longer in front of them.
			return "", i18n.E("target {id}: {why}", i18n.A{"id": t.ID, "why": why})
		}
		out = append(out, t.normalize())
	}
	// Duplicate names are reported here too: after save, reading skips one of them, and the
	// user cannot tell "I added two" from "only one shows".
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

// ---------- the cells in the table ----------

func stateText(s TargetStatus) string {
	switch s.Conn.State {
	case StateUp:
		return i18n.T("connected for {d}", i18n.A{"d": humanizeSince(s.Conn.Since)})
	case StateConnecting:
		return i18n.T("connecting", nil)
	case StateDown:
		// The reason **must be in the cell**: this is the only place the user can see "why not
		// connected", and the word "failed" just sends them to the logs.
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

// forwardHref is the clickable address in the forward-port cell.
//
// Empty when the port is not up, or this target never asked for one - **that means "not a link"**.
// Giving an address that fails when clicked is worse than no link: users think they misconfigured.
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
