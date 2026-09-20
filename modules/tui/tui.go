package tui

import (
	"bufio"
	"fmt"
	"os"
	"strings"
	"syscall"
	"unsafe"

	"github.com/rzbdz/newgate/lib/i18n"
	"github.com/rzbdz/newgate/lib/style"
	"github.com/rzbdz/newgate/modules/config/domain"
	"github.com/rzbdz/newgate/modules/config/store"
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
)

type key int

const (
	kUp key = iota
	kDown
	kEnter
	kQuit
	kSave
	kLeft
	kRight
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
	case 'q', 'Q', 3, 27: // 27 单独出现也当 ESC；带序列的下面处理
		if b == 27 {
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
	case 's', 'S':
		return kSave
	}
	return kOther
}

// ---------- 主界面：选 profile ----------

func Run() error {
	old, err := enterRaw()
	if err != nil {
		// 底层错误由 Ef 放进 {err}（消息里必须写它，见 lib/i18n 的注释）；
		// `newgate --set-profile` 是命令+flag，原样留在译文里。
		return i18n.Ef(err, "this terminal does not support the TUI ({err}); use `newgate --set-profile <name>` instead", nil)
	}
	defer restore(old)
	fmt.Print(hideC)
	defer fmt.Print(showC + reset + "\n")

	in := bufio.NewReader(os.Stdin)
	names, err := store.ListProfiles()
	if err != nil || len(names) == 0 {
		return i18n.E("no profiles yet; run `newgate init` first", nil)
	}
	st := store.LoadState()

	cur := 0
	for i, n := range names {
		if n == st.DefaultProfile {
			cur = i
		}
	}

	msg := ""
	for {
		drawProfileMenu(names, cur, st.DefaultProfile, msg)
		switch readKey(in) {
		case kUp:
			if cur > 0 {
				cur--
			}
			msg = ""
		case kDown:
			if cur < len(names)-1 {
				cur++
			}
			msg = ""
		case kEnter, kRight:
			if err := store.SetActiveProfile("", names[cur]); err != nil {
				msg = style.Mark(style.Bad) + " " + err.Error()
			} else {
				// 通知守护进程「立刻」重读。**不是正确性必需**：watcher 的指纹覆盖
				// state.json（store/watch.go 的 signature），所以最迟 1 秒后一样生效；
				// 但这是用户在界面上刚敲下的一下，1 秒的延迟会被当成「没生效」。仓库里
				// 每一个写配置的地方都这么通知（config/commands.go、opencodeomo），
				// tui 原来少这一下（2026-09-18 补）。
				controlplane.Notify()
				st = store.LoadState()
				// 版式的空格留在消息外面（`颜色/缩进`都是排版），句子整个进目录；
				// profile 名上色后当占位符的值传进去，译文不必知道它带转义序列。
				msg = style.Mark(style.OK) + " " + i18n.T("switched to {name}",
					i18n.A{"name": style.Cyan(names[cur])}) +
					style.Dim("   "+i18n.T("takes effect on the next request", nil))
			}
		case kQuit:
			return nil
		}
	}
}

func drawProfileMenu(names []string, cur int, active, msg string) {
	var b strings.Builder
	b.WriteString(clear)
	b.WriteString(style.Bold(" newgate · "+i18n.T("profile selection", nil)) + "\n")
	// 按键（↑/↓ · Enter · q）留在消息里：它们夹在词中间，拆出去译文就没法重排
	// 语序了；译文里照着原样抄一遍——按键名不翻译（命令名/flag 同一条规矩）。
	b.WriteString(style.Dim(" "+i18n.T("↑/↓ or j/k to move · Enter to apply · q to quit", nil)) + "\n\n")

	provs, _ := store.LoadProviders()
	for i, n := range names {
		mark := " "
		if n == active {
			mark = style.Green("*")
		}
		line := fmt.Sprintf(" %s %s", mark, style.Pad(n, 10))
		if i == cur {
			line = rev + line + reset
		}
		b.WriteString(line)

		if pr, err := store.LoadProfile(n); err == nil {
			b.WriteString("  " + style.Dim(pr.Description))
		}
		b.WriteString("\n")

		if i == cur {
			if pr, err := store.LoadProfile(n); err == nil {
				for _, role := range domain.Roles {
					bind, ok := pr.Resolve(role)
					if !ok {
						b.WriteString("        " + style.Pad(role, 8) + " " + style.Yellow(i18n.T("not bound", nil)) + "\n")
						continue
					}
					warn := ""
					if provs != nil {
						if p, ok2 := provs.Providers[bind.Provider]; !ok2 {
							warn = style.Yellow("   " + i18n.T("provider is not defined", nil))
						} else if p.Key() == "" {
							warn = style.Yellow("   " + i18n.T("api_key missing", nil))
						}
					}
					b.WriteString("        " + style.Pad(role, 8) + " " +
						style.Cyan(bind.Provider+"/"+bind.Model) + warn + "\n")
				}
			}
		}
	}
	if msg != "" {
		b.WriteString("\n " + msg + "\n")
	}
	fmt.Print(b.String())
}
