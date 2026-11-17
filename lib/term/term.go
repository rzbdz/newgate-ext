// Package term 是**终端交互**这一层的唯一实现：raw mode、按键解码、单行输入。
//
// # 为什么它是一份独立的叶子，而不是各界面自己写一遍
//
// 2026-11-17 之前 raw mode 与按键解码长在 modules/tui 里。`newgate config` 那个
// 向导（modules/setup）要的多选列表、文本输入、密钥输入，用的完全是同一种按键
// 语义；抄一份过去就意味着两套按键表，而它们迟早会分叉（补一个 Ctrl-U、多认一种
// 方向键序列），分叉的症状是「这个界面里 Home 键是退格、那个界面里是跳第一行」。
//
// 与内核 lib/ 里那些同一条规矩：它不 import 任何模块，也不认识 newgate 的业务概念
// ——只有「一台终端、一个按键、一行字」。版式（颜色、表格）在 lib/style，不在这里。
package term

import (
	"bufio"
	"errors"
	"os"
	"strings"
)

// ErrNotATTY 说「stdin 不是终端」。所有交互界面都该在动手之前问一次
// （IsTTY），然后给出「这台机器上没法交互，走参数版」的那句话。
var ErrNotATTY = errors.New("stdin is not a terminal")

// Key 是一个**语义按键**。界面按这个分派，不该去认字节。
type Key int

const (
	KeyNone Key = iota
	KeyUp
	KeyDown
	KeyLeft
	KeyRight
	KeyHome
	KeyEnd
	KeyBackspace
	KeyDelete
	KeyEnter
	KeyTab
	KeySpace
	KeyEsc
	// KeyInterrupt 是 Ctrl-C：界面把它当成「放弃这次向导」，**不是**崩溃。
	KeyInterrupt
	// KeyEOF 是 stdin 被关掉（管道读完）。与 Ctrl-C 分开：那不是用户按的。
	KeyEOF
	KeyRune
	// KeyOther 是认不出来的控制键。界面通常忽略它。
	KeyOther
)

// Event 一次按键。Key == KeyRune 时 Rune 有效（按下的那个字符，已解开 UTF-8）。
type Event struct {
	Key  Key
	Rune rune
}

// Terminal 是一个进了 raw mode 的终端。
//
// 值语义上它是「这一次交互会话」：Open 进去、Close 出来。所有渲染仍然由调用方
// 负责（本包不打印任何东西），因为版式是界面的事，而按键是终端的事。
type Terminal struct {
	in       *bufio.Reader
	restore  func()
	out      *os.File
	echoless bool
}

// Open 进入 raw mode（不回显、不按行缓冲、Ctrl-C 不产生信号——由界面自己把
// KeyInterrupt 翻成「用户放弃」）。
func Open() (*Terminal, error) {
	if !IsTTY() {
		return nil, ErrNotATTY
	}
	restore, err := enterRaw(os.Stdin.Fd())
	if err != nil {
		return nil, err
	}
	return &Terminal{in: bufio.NewReader(os.Stdin), restore: restore, out: os.Stdout}, nil
}

// Close 恢复终端。幂等——调用方用 defer 关它，即使中途出错也不会把终端留在
// 一个「敲字看不见、回车不管用」的状态里（那是用户要开新终端才能修的那种损坏）。
func (t *Terminal) Close() {
	if t == nil || t.restore == nil {
		return
	}
	t.restore()
	t.restore = nil
}

// ReadEvent 读一个按键。它**阻塞**，直到有一个能表达的事件（认不出的控制键
// 会返回 KeyOther，而不是无限吞下去）。
func (t *Terminal) ReadEvent() Event {
	b, err := t.in.ReadByte()
	if err != nil {
		return Event{Key: KeyEOF}
	}
	switch b {
	case '\r', '\n':
		return Event{Key: KeyEnter}
	case '\t':
		return Event{Key: KeyTab}
	case ' ':
		return Event{Key: KeyRune, Rune: ' '}
	case 3: // Ctrl-C
		return Event{Key: KeyInterrupt}
	case 4: // Ctrl-D
		return Event{Key: KeyEOF}
	case 1: // Ctrl-A：行首
		return Event{Key: KeyHome}
	case 5: // Ctrl-E：行尾
		return Event{Key: KeyEnd}
	case 8, 127:
		return Event{Key: KeyBackspace}
	case 21: // Ctrl-U：清空这一行（ReadLine 里用）
		return Event{Key: KeyOther, Rune: 21}
	case 27:
		return t.readEscape()
	}
	if b < 0x20 {
		return Event{Key: KeyOther, Rune: rune(b)}
	}
	if b < 0x80 {
		return Event{Key: KeyRune, Rune: rune(b)}
	}
	// UTF-8 多字节：把续字节读齐，解成 rune。多读一个字节就够判断长度了。
	need := 0
	switch {
	case b&0xE0 == 0xC0:
		need = 1
	case b&0xF0 == 0xE0:
		need = 2
	case b&0xF8 == 0xF0:
		need = 3
	default:
		return Event{Key: KeyOther}
	}
	buf := []byte{b}
	for i := 0; i < need; i++ {
		c, err := t.in.ReadByte()
		if err != nil {
			return Event{Key: KeyRune, Rune: '�'}
		}
		buf = append(buf, c)
	}
	r := []rune(string(buf))
	if len(r) == 0 {
		return Event{Key: KeyOther}
	}
	return Event{Key: KeyRune, Rune: r[0]}
}

// readEscape 解开 `ESC [ …` 那一族序列。
//
// 只认**已经在缓冲区里**的后续字节（与 modules/tui 原来的做法一致）：方向键、
// Home/End 在真实终端里一次 write 进来，所以它们总是一起到。单独一个 ESC（用户
// 按的是 Esc 键）后面没有字节可读，就返回 KeyEsc——这里不去等一个超时，那会让
// 每一次 Esc 都卡住几十毫秒。
func (t *Terminal) readEscape() Event {
	if t.in.Buffered() == 0 {
		return Event{Key: KeyEsc}
	}
	b, err := t.in.ReadByte()
	if err != nil {
		return Event{Key: KeyEsc}
	}
	if b != '[' && b != 'O' {
		return Event{Key: KeyEsc}
	}
	if t.in.Buffered() == 0 {
		return Event{Key: KeyOther}
	}
	c, err := t.in.ReadByte()
	if err != nil {
		return Event{Key: KeyOther}
	}
	switch c {
	case 'A':
		return Event{Key: KeyUp}
	case 'B':
		return Event{Key: KeyDown}
	case 'C':
		return Event{Key: KeyRight}
	case 'D':
		return Event{Key: KeyLeft}
	case 'H':
		return Event{Key: KeyHome}
	case 'F':
		return Event{Key: KeyEnd}
	case 'Z': // Shift-Tab：反向切换
		return Event{Key: KeyOther, Rune: 'Z'}
	case '1', '3', '4', '7', '8':
		// `ESC [ 3 ~` 删除、`1~`/`7~` Home、`4~`/`8~` End。吃掉结尾那个 `~`。
		if t.in.Buffered() > 0 {
			if tail, _ := t.in.ReadByte(); tail == '~' {
				switch c {
				case '3':
					return Event{Key: KeyDelete}
				case '1', '7':
					return Event{Key: KeyHome}
				case '4', '8':
					return Event{Key: KeyEnd}
				}
			}
		}
	}
	return Event{Key: KeyOther}
}

// LineMode 是一次 ReadLine 的行为开关。
type LineMode struct {
	// Secret=true 时只显示掩码：密钥不该在终端上、也不该留在回滚缓冲里。
	Secret bool
	// Placeholder 是输入为空时显示的那句灰字提示（由调用方给）。
	Placeholder string
	// Validate 对**这一行**做一次校验；返回非空字符串就是错误原因，界面显示它、
	// 不结束这一行。nil = 不校验。
	Validate func(string) string
}

// LineResult 是 ReadLine 的结局。
type LineResult struct {
	Text   string
	Cancel bool // 用户按了 Esc / Ctrl-C
	EOF    bool
}

// ReadLine 在 raw mode 里读一整行（回车结束），带回显、退格、Home/End 与清空。
//
// # 为什么不用「退出 raw mode + bufio.Scanner」
//
// 那会让整个向导在每一行输入时闪一下（终端回到行模式，滚动位置跳），而且掩码
// 输入还得自己关回显。更实际的是：向导里**上一步/下一步**的按键语义要与列表
// 那一屏一致（Esc = 返回上一步），混用两种读法就做不到。
//
// render 由调用方给：它收到「当前已输入的文本」，负责把整屏画出来（含提示语、
// 校验错误）。本包只管按键与文本状态——版式归界面。
func (t *Terminal) ReadLine(mode LineMode, render func(text, errMsg string, cursor int)) LineResult {
	var rs []rune
	var errMsg string
	emit := func() { render(string(rs), errMsg, len(rs)) }
	emit()
	for {
		ev := t.ReadEvent()
		switch ev.Key {
		case KeyEOF:
			return LineResult{EOF: true}
		case KeyInterrupt, KeyEsc:
			return LineResult{Cancel: true}
		case KeyEnter:
			text := strings.TrimSpace(string(rs))
			if mode.Validate != nil {
				if msg := mode.Validate(text); msg != "" {
					errMsg = msg
					emit()
					continue
				}
			}
			return LineResult{Text: text}
		case KeyBackspace:
			if len(rs) > 0 {
				rs = rs[:len(rs)-1]
				errMsg = ""
			}
		case KeyHome:
			// 光标回到行首：本实现不做行内光标移动（一行的输入很短），
			// 但 Home 要有一个**确定**的语义，而不是被静默吞掉。
			// 取「清空」是用户在这个场景里真正想要的（重输一整行地址）。
			rs = rs[:0]
			errMsg = ""
		case KeyOther:
			if ev.Rune == 21 { // Ctrl-U
				rs = rs[:0]
				errMsg = ""
			}
		case KeyRune:
			rs = append(rs, ev.Rune)
			errMsg = ""
		}
		emit()
	}
}

// IsTTY 报告 stdin 是不是一台终端。交互界面在做任何事之前问它一次。
func IsTTY() bool { return isTTY(os.Stdin.Fd()) }
