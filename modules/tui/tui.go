package tui

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"unsafe"

	"github.com/rzbdz/newgate/lib/i18n"
	"github.com/rzbdz/newgate/lib/style"
	"github.com/rzbdz/newgate/modules/config/domain"
	"github.com/rzbdz/newgate/modules/config/store"
	agentapi "github.com/rzbdz/newgate/modules/confighook"
	"github.com/rzbdz/newgate/modules/gateway/controlplane"
)

// ---------- 零依赖 raw mode ----------

type termios struct {
	Iflag, Oflag, Cflag, Lflag uint32
	Line                       uint8
	Cc                         [32]uint8
	Ispeed, Ospeed             uint32
}

const (
	tcgets = 0x5401
	tcsets = 0x5402
)

func ioctl(fd uintptr, req uintptr, t *termios) error {
	_, _, e := syscall.Syscall(syscall.SYS_IOCTL, fd, req, uintptr(unsafe.Pointer(t)))
	if e != 0 {
		return e
	}
	return nil
}

func enterRaw() (*termios, error) {
	fd := os.Stdin.Fd()
	var old termios
	if err := ioctl(fd, tcgets, &old); err != nil {
		return nil, err
	}
	raw := old
	raw.Lflag &^= syscall.ECHO | syscall.ICANON | syscall.ISIG
	raw.Iflag &^= syscall.IXON | syscall.ICRNL
	raw.Cc[syscall.VMIN] = 1
	raw.Cc[syscall.VTIME] = 0
	if err := ioctl(fd, tcsets, &raw); err != nil {
		return nil, err
	}
	return &old, nil
}

func restore(old *termios) {
	if old != nil {
		_ = ioctl(os.Stdin.Fd(), tcsets, old)
	}
}

// ---------- 渲染 ----------

// 光标/清屏/反显是**终端控制**，留在本地；语义色一律走 ui/style——那边
// 还要处理 NO_COLOR 与「输出不是终端」，两套定义迟早分叉。
const (
	clear = "\033[2J\033[H"
	rev   = "\033[7m"
	reset = "\033[0m"
	hideC = "\033[?25l"
	showC = "\033[?25h"

	// 左栏宽度：agent 名（最长 opencode，8）加两格边距，再留一点给「Global
	// default」这种标签。右栏从固定列开始，两栏才像一张菜单。
	leftW = 26
)

type key int

const (
	kUp key = iota
	kDown
	kLeft
	kRight
	kTab
	kEnter
	kQuit
	kReload
	kClear
	kOther
)

func readKey(r *bufio.Reader) key {
	b, err := r.ReadByte()
	if err != nil {
		return kQuit
	}
	switch b {
	case '\r', '\n', ' ':
		return kEnter
	case '\t':
		return kTab
	case 'q', 'Q', 3:
		return kQuit
	case 27: // ESC：单独出现是退出，后面跟 [A/B/C/D 是方向键
		if r.Buffered() >= 2 {
			b2, _ := r.ReadByte()
			b3, _ := r.ReadByte()
			if b2 == '[' {
				switch b3 {
				case 'A':
					return kUp
				case 'B':
					return kDown
				case 'C':
					return kRight
				case 'D':
					return kLeft
				}
			}
			return kOther
		}
		return kQuit
	case 'k':
		return kUp
	case 'j':
		return kDown
	case 'h':
		return kLeft
	case 'l':
		return kRight
	case 'r', 'R':
		return kReload
	case 'd', 'D', 8, 127:
		return kClear
	}
	return kOther
}

// ---------- 状态 ----------

// scope 是左边那一栏的一行：全局缺省，或一个具体的客户端。
//
// `stored` 是 state.Active 里**存下来的**那个值，空 = 没单独设过、跟随全局
// 缺省。`profile` 是解析之后实际走的那一个。两者的差别是这一屏存在的全部理由
// ——它同时解释「现在走谁」与「改全局缺省时它跟不跟着走」。
type scope struct {
	id      string // "" = 全局
	label   string
	profile string
	stored  string
	agent   *agentapi.Agent
}

type model struct {
	catalog agentapi.AgentCatalog
	names   []string
	scopes  []scope

	focus      int // 0 = 左栏（客户端/全局），1 = 右栏（profile）
	scopeCur   int
	profileCur int
	msg        string
}

// Run 是 `newgate tui` 的主体。
//
// catalog 可以为 nil（骨架装配里关掉了 confighook）：那时只显示全局缺省，
// 功能不残缺——「改全局 profile」本来就不需要知道有哪些客户端。
func Run(catalog agentapi.AgentCatalog) error {
	names, err := store.ListProfiles()
	if err != nil || len(names) == 0 {
		return i18n.E("no profiles yet; run `newgate init` first", nil)
	}
	old, err := enterRaw()
	if err != nil {
		// 底层错误由 Ef 放进 {err}（消息里必须写它，见 lib/i18n 的注释）；
		// `newgate --set-profile` 是命令+flag，原样留在译文里。
		return i18n.Ef(err, "this terminal does not support the TUI ({err}); use `newgate --set-profile <name>` instead", nil)
	}
	defer restore(old)
	fmt.Print(hideC)
	defer fmt.Print(showC + reset + "\n")

	m := &model{catalog: catalog, names: names}
	m.reload("")
	in := bufio.NewReader(os.Stdin)
	for {
		m.draw()
		switch readKey(in) {
		case kUp:
			m.move(-1)
		case kDown:
			m.move(1)
		case kLeft:
			if m.focus == 1 {
				m.focus = 0
				m.msg = ""
			}
		case kRight:
			if m.focus == 0 {
				m.focus = 1
				m.syncProfileCur()
				m.msg = ""
			}
		case kTab:
			m.focus = 1 - m.focus
			if m.focus == 1 {
				m.syncProfileCur()
			}
			m.msg = ""
		case kEnter:
			m.apply()
		case kClear:
			m.clearOverride()
		case kReload:
			m.reload("")
		case kQuit:
			return nil
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

// options 是右栏这一份 scope 能选的值。
//
// 客户端的第一个选项是空串（跟随全局缺省）——它必须是一个**可以选中的值**，
// 而不是「没得选」；用户的常见诉求就是「这家跟别人一样」。
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
		// 左栏按 Enter = 进右栏，不写盘。menuconfig 的手感：先选「改谁」，
		// 再选「改成什么」；把两步压成一步会让人在还没看清选项时就改了配置。
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
	// 立刻通知守护进程重读。不是正确性必需（watcher 最迟 1 秒也会读到），
	// 但这是用户刚敲下的一下，1 秒的延迟会被当成「没生效」。
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
	// 颜色只加在 profile 上；列宽用的是不含转义序列的 plain（见 padRight）。
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

// detail 是下半屏：当前（或光标所在）profile 落到哪些上游，以及这个客户端的
// 槽位和它自己的配置文件。它跟着光标走，所以在按 Enter 之前就能预览改动。
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
		// 全局：整条档位表都值得看。客户端的详情只列它自己的槽位——那是它
		// 真正在意的那几格（claude 的 opus/sonnet…、codex 的 model/review_model）。
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
		// 没有槽位的客户端（opencode）仍然能看主档落到谁：normal 是「主力」
		// 那一档，也是这些客户端最常写进配置的模型。
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

// padRight 把纯文本补到 width；padRightColored 在**不含转义序列**的 plain 上量
// 宽度，再把补的空格加到 colored 后面。ANSI 转义序列不占列，不能直接对 colored
// 调 Pad——那会把颜色码算进宽度，右栏就跳来跳去。
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
