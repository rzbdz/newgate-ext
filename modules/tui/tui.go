package tui

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/rzbdz/newgate-ext/lib/style"
	term "github.com/rzbdz/newgate-ext/lib/term"
	"github.com/rzbdz/newgate-ext/modules/config/domain"
	"github.com/rzbdz/newgate-ext/modules/config/store"
	agentapi "github.com/rzbdz/newgate-ext/modules/confighook"
	"github.com/rzbdz/newgate-ext/modules/gateway/controlplane"
	"github.com/rzbdz/newgate/lib/i18n"
)

// ---------- rendering ----------

// Cursor/screen-clear/reverse are **terminal control**, kept local; semantic colors always
// go through ui/style - it also handles NO_COLOR and "not a terminal", or two copies diverge.
const (
	clear = "\033[2J\033[H"
	rev   = "\033[7m"
	reset = "\033[0m"
	hideC = "\033[?25l"
	showC = "\033[?25h"

	// Left column width: agent names (longest opencode, 8) plus two cells of margin, and a
	// little more for "Global default" labels; the right column starts at a fixed column.
	leftW = 26
)

// ---------- state ----------

// scope is one row of the left column: the global default, or one concrete client.
//
// `stored` is the value **stored** in state.Active; empty = never set separately, follows
// the global default. `profile` is the one actually used after resolution. That difference
// is the whole reason for this screen: "who runs now" and "does it follow the default".
type scope struct {
	id      string // "" = global
	label   string
	profile string
	stored  string
	agent   *agentapi.Agent
}

type model struct {
	catalog agentapi.AgentCatalog
	names   []string
	scopes  []scope

	focus      int // 0 = left column (client/global), 1 = right column (profile)
	scopeCur   int
	profileCur int
	msg        string
}

// Run is the body of `newgate tui`.
//
// catalog may be nil (the skeleton assembly has confighook off): then only the global
// default shows, nothing is missing - "change the global profile" never needed client knowledge.
func Run(catalog agentapi.AgentCatalog) error {
	names, err := store.ListProfiles()
	if err != nil || len(names) == 0 {
		return i18n.E("no profiles yet; run `newgate init` first", nil)
	}
	t, err := term.Open()
	if err != nil {
		// The underlying error goes into {err} via Ef (the message must name it, see lib/i18n);
		// `newgate --set-profile` is a command+flag, kept verbatim in the translation.
		return i18n.Ef(err, "this terminal does not support the TUI ({err}); use `newgate --set-profile <name>` instead", nil)
	}
	defer t.Close()
	fmt.Print(hideC)
	defer fmt.Print(showC + reset + "\n")

	m := &model{catalog: catalog, names: names}
	m.reload("")
	for {
		m.draw()
		ev := t.ReadEvent()
		switch ev.Key {
		case term.KeyUp:
			m.move(-1)
		case term.KeyDown:
			m.move(1)
		case term.KeyLeft:
			if m.focus == 1 {
				m.focus = 0
				m.msg = ""
			}
		case term.KeyRight:
			if m.focus == 0 {
				m.focus = 1
				m.syncProfileCur()
				m.msg = ""
			}
		case term.KeyTab:
			m.focus = 1 - m.focus
			if m.focus == 1 {
				m.syncProfileCur()
			}
			m.msg = ""
		case term.KeyEnter:
			m.apply()
		case term.KeyInterrupt, term.KeyEsc, term.KeyEOF:
			return nil
		case term.KeyRune:
			// Same as the old letter keys: q quits, d clears the override, r re-reads.
			switch ev.Rune {
			case 'q', 'Q':
				return nil
			case 'd', 'D':
				m.clearOverride()
			case 'r', 'R':
				m.reload("")
			case 'j':
				m.move(1)
			case 'k':
				m.move(-1)
			}
		}
	}
}

func (m *model) reload(msg string) {
	names, err := store.ListProfiles()
	if err == nil && len(names) > 0 {
		m.names = names
	}
	st := store.LoadState()
	prev := ""
	if m.scopeCur >= 0 && m.scopeCur < len(m.scopes) {
		prev = m.scopes[m.scopeCur].id
	}
	scopes := []scope{{
		id:      "",
		label:   i18n.T("Global default", nil),
		profile: st.DefaultProfile,
	}}
	if m.catalog != nil {
		ids := m.catalog.Names()
		sort.Strings(ids)
		for _, id := range ids {
			a, ok := m.catalog.Get(id)
			if !ok {
				continue
			}
			ap := agentapi.AgentProfile{AgentID: id}
			scopes = append(scopes, scope{
				id:      id,
				label:   id,
				profile: ap.Active(),
				stored:  ap.Stored(),
				agent:   a,
			})
		}
	}
	m.scopes = scopes
	m.scopeCur = 0
	for i := range m.scopes {
		if m.scopes[i].id == prev {
			m.scopeCur = i
			break
		}
	}
	m.syncProfileCur()
	m.msg = msg
}

func (m *model) currentScope() scope {
	if m.scopeCur < 0 || m.scopeCur >= len(m.scopes) {
		return scope{}
	}
	return m.scopes[m.scopeCur]
}

// options are the values selectable for one scope in the right column.
//
// A client's first option is the empty string (follow the global default) - it must be a
// **selectable value**, not "nothing to choose"; the common request is "this one follows the rest".
func (m *model) options() []string {
	if m.currentScope().id == "" {
		return m.names
	}
	return append([]string{""}, m.names...)
}

func (m *model) syncProfileCur() {
	sc := m.currentScope()
	opts := m.options()
	idx := 0
	if sc.id == "" {
		for i, n := range opts {
			if n == sc.profile {
				idx = i
				break
			}
		}
	} else if sc.stored != "" {
		for i, n := range opts {
			if n == sc.stored {
				idx = i
				break
			}
		}
	}
	if idx >= len(opts) {
		idx = 0
	}
	m.profileCur = idx
}

func (m *model) move(delta int) {
	if m.focus == 0 {
		next := m.scopeCur + delta
		if next >= 0 && next < len(m.scopes) {
			m.scopeCur = next
			m.syncProfileCur()
		}
	} else {
		opts := m.options()
		next := m.profileCur + delta
		if next >= 0 && next < len(opts) {
			m.profileCur = next
		}
	}
	m.msg = ""
}

func (m *model) previewProfile() string {
	sc := m.currentScope()
	opts := m.options()
	if len(opts) == 0 || m.profileCur >= len(opts) {
		return sc.profile
	}
	opt := opts[m.profileCur]
	if sc.id == "" {
		return opt
	}
	if opt == "" {
		return store.LoadState().DefaultProfile
	}
	return opt
}

func (m *model) isCurrent(opt string) bool {
	sc := m.currentScope()
	if sc.id == "" {
		return opt == sc.profile
	}
	if sc.stored == "" {
		return opt == ""
	}
	return opt == sc.stored
}

func (m *model) apply() {
	if m.focus == 0 {
		// Enter on the left column = go to the right column, no write. The menuconfig feel:
		// pick "which one" first, then "to what"; one step makes people change config too early.
		m.focus = 1
		m.syncProfileCur()
		m.msg = ""
		return
	}
	opts := m.options()
	if len(opts) == 0 || m.profileCur >= len(opts) {
		return
	}
	sc := m.currentScope()
	opt := opts[m.profileCur]
	var err error
	var done string
	switch {
	case sc.id == "":
		err = store.SetActiveProfile("", opt)
		done = i18n.T("global default is now {profile}", i18n.A{"profile": opt})
	case opt == "":
		err = store.ClearActiveProfile(sc.id)
		done = i18n.T("{agent} follows the global default again", i18n.A{"agent": sc.id})
	default:
		err = store.SetActiveProfile(sc.id, opt)
		done = i18n.T("{agent} now uses {profile}", i18n.A{"agent": sc.id, "profile": opt})
	}
	if err != nil {
		m.msg = style.Mark(style.Bad) + " " + err.Error()
		return
	}
	// Tell the daemon to re-read now. Not required for correctness (the watcher gets it
	// within a second), but this is a key the user just pressed, and 1s reads as "no effect".
	controlplane.Notify()
	m.reload(style.Mark(style.OK) + " " + done +
		style.Dim("   "+i18n.T("takes effect on the next request", nil)))
}

func (m *model) clearOverride() {
	sc := m.currentScope()
	if sc.id == "" {
		m.msg = style.Hint(i18n.T("the global default has no parent to follow", nil))
		return
	}
	if sc.stored == "" {
		m.msg = style.Hint(i18n.T("{agent} already follows the global default", i18n.A{"agent": sc.id}))
		return
	}
	if err := store.ClearActiveProfile(sc.id); err != nil {
		m.msg = style.Mark(style.Bad) + " " + err.Error()
		return
	}
	controlplane.Notify()
	m.reload(style.Mark(style.OK) + " " +
		i18n.T("{agent} follows the global default again", i18n.A{"agent": sc.id}))
}

func (m *model) draw() {
	var b strings.Builder
	b.WriteString(clear)
	b.WriteString(style.Bold(" newgate · "+i18n.T("profiles and clients", nil)) + "\n")
	b.WriteString(style.Dim(" "+i18n.T(
		"↑/↓ or j/k move · ←/→ or Tab switch pane · Enter apply · d follow the default · r reload · q quit", nil)) + "\n")
	b.WriteString(style.Dim(" "+i18n.T("* on the left marks a client set for that client only; * on the right marks the profile in effect", nil)) + "\n\n")

	opts := m.options()
	rows := len(m.scopes)
	if len(opts) > rows {
		rows = len(opts)
	}
	b.WriteString(" " + style.Bold(style.Pad(i18n.T("Clients", nil), leftW-1)) + "  " +
		style.Bold(i18n.T("Profiles", nil)) + "\n")
	for i := 0; i < rows; i++ {
		b.WriteString(m.scopeRow(i))
		if i < len(opts) {
			b.WriteString(m.profileRow(i, opts[i]))
		}
		b.WriteString("\n")
	}

	b.WriteString("\n")
	b.WriteString(m.detail())
	if m.msg != "" {
		b.WriteString("\n " + m.msg + "\n")
	}
	fmt.Print(b.String())
}

func (m *model) scopeRow(i int) string {
	if i >= len(m.scopes) {
		return strings.Repeat(" ", leftW)
	}
	sc := m.scopes[i]
	selected := m.focus == 0 && i == m.scopeCur
	mark := " "
	if sc.id != "" && sc.stored != "" {
		mark = "*"
	}
	profile := sc.profile
	plain := " " + mark + " " + style.Pad(sc.label, 16) + " " + profile
	plain = padRight(plain, leftW)
	if selected {
		return rev + plain + reset
	}
	// Color only on the profile; column width uses plain without escape sequences (see padRight).
	colored := " " + mark + " " + style.Pad(sc.label, 16) + " " + style.Cyan(profile)
	return padRightColored(plain, colored, leftW)
}

func (m *model) profileRow(i int, opt string) string {
	label := opt
	if label == "" {
		label = i18n.T("follow the global default ({profile})",
			i18n.A{"profile": store.LoadState().DefaultProfile})
	}
	mark := " "
	if m.isCurrent(opt) {
		mark = style.Green("*")
	}
	if m.focus == 1 && i == m.profileCur {
		return "  " + rev + " " + strings.TrimSpace(mark) + " " + label + " " + reset
	}
	line := "  " + mark + " " + style.Cyan(label)
	if desc := profileDescription(opt); desc != "" {
		line += style.Dim("  " + desc)
	}
	return line
}

func profileDescription(name string) string {
	if name == "" {
		return i18n.T("the client uses whatever the global default is", nil)
	}
	pr, err := store.LoadProfile(name)
	if err != nil {
		return i18n.T("cannot read this profile", nil)
	}
	return pr.Description
}

// detail is the lower half: which upstreams the current (or cursor) profile lands on, plus
// this client's slots and its own config file. It follows the cursor, so you preview first.
func (m *model) detail() string {
	sc := m.currentScope()
	prof := m.previewProfile()
	var b strings.Builder
	b.WriteString(" " + style.Bold(sc.label) + style.Dim(" · profile ") + style.Cyan(prof))
	if sc.id == "" {
		b.WriteString(style.Dim(" · " + i18n.T("the global default", nil)))
	} else if sc.stored != "" {
		b.WriteString(style.Dim(" · " + i18n.T("set for this client only", nil)))
	} else {
		b.WriteString(style.Dim(" · " + i18n.T("follows the global default", nil)))
	}
	b.WriteString("\n")

	if sc.id != "" && sc.agent == nil {
		b.WriteString("   " + style.Hint(i18n.T("this client is no longer in the catalog", nil)) + "\n")
		return b.String()
	}
	pr, err := store.LoadProfile(prof)
	if err != nil {
		b.WriteString("   " + style.Mark(style.Warn) + " " +
			i18n.T("cannot read profile {name}: {err}", i18n.A{"name": prof, "err": err.Error()}) + "\n")
		return b.String()
	}
	provs, _ := store.LoadProviders()

	switch {
	case sc.id == "":
		// Global: the whole tier table is worth seeing. A client's detail lists only its own
		// slots - the cells it really cares about (claude's opus/sonnet..., codex's model/review_model).
		for _, role := range domain.Roles {
			bind, ok := pr.Resolve(role)
			value := style.Dim(i18n.T("unbound", nil))
			note := ""
			if ok {
				value = style.Cyan(bind.String())
				note = bindingNote(provs, bind)
			}
			b.WriteString("   " + style.Pad(role, 9) + " " + value + note + "\n")
		}
	case len(sc.agent.Slots) == 0:
		b.WriteString("   " + style.Hint(i18n.T("slots are discovered from the config file at takeover", nil)) + "\n")
		// A client with no slots (opencode) can still see where the primary tier lands: normal
		// is the "primary" tier, and the one these clients most often put into their config.
		if bind, ok := pr.Resolve("normal"); ok {
			b.WriteString("   " + style.Pad("normal", 9) + " " + style.Cyan(bind.String()) +
				bindingNote(provs, bind) + "\n")
		}
	default:
		facts := m.catalog.Facts(sc.agent.ID)
		for _, slot := range sc.agent.Slots {
			tier := agentapi.TierOf(facts, slot)
			bind, ok := pr.Resolve(tier)
			value := style.Dim(i18n.T("unbound", nil))
			note := ""
			if ok {
				value = style.Cyan(bind.String())
				note = bindingNote(provs, bind)
			}
			b.WriteString("   " + style.Pad(slot.Name, 12) + " " +
				style.Cyan(style.Pad(tier, 8)) + " " + value + note + "\n")
		}
	}

	if sc.id != "" && sc.agent != nil && sc.agent.Config != nil {
		for _, target := range sc.agent.Config.Targets() {
			state := i18n.T("not taken over", nil)
			mark := style.Mark(style.Skip)
			if sc.agent.Config.IsTakenOver(target) {
				state = i18n.T("taken over", nil)
				mark = style.Mark(style.OK)
			}
			b.WriteString("   " + style.Dim("config") + " " + filepath.Base(target) + " " +
				mark + " " + state + "\n")
		}
	}
	b.WriteString("   " + style.Hint(i18n.T("Applies to the next request; takeover rewrites the client's own config with newgate on <agent>.", nil)) + "\n")
	return b.String()
}

func bindingNote(provs *domain.Providers, b domain.Binding) string {
	if provs == nil || b.IsRef() {
		return ""
	}
	p, ok := provs.Providers[b.Provider]
	switch {
	case !ok:
		return "  " + style.Red(i18n.T("provider is not defined", nil))
	case p.Key() == "":
		return "  " + style.Yellow(i18n.T("api_key missing", nil))
	}
	return ""
}

// padRight pads plain text to width; padRightColored measures width on plain **without
// escape sequences**, then appends the padding to colored. ANSI escapes take no columns, so
// calling Pad on colored directly counts color codes as width and the right column jumps.
func padRight(plain string, width int) string {
	if n := style.VisibleWidth(plain); n < width {
		return plain + strings.Repeat(" ", width-n)
	}
	return plain
}

func padRightColored(plain, colored string, width int) string {
	if n := style.VisibleWidth(plain); n < width {
		return colored + strings.Repeat(" ", width-n)
	}
	return colored
}
