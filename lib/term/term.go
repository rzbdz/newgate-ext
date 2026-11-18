// Package term is the single implementation of the **terminal
// interaction** layer: raw mode, key decoding, single-line input.
//
// # Why it is a standalone leaf instead of each UI writing its own
//
// Before 2026-11-17 raw mode and key decoding lived in modules/tui. The `newgate config`
// wizard (modules/setup) needs multi-select lists, text input, and secret input, using exactly
// the same key semantics; copying it over would mean two key tables, and they will diverge
// sooner or later (a Ctrl-U added on one side, a new arrow-key sequence recognized on the
// other), with the symptom "Home is backspace in this UI, jump-to-first -line in that one".
//
// Same rule as the kernel's lib/: it imports no module and knows no newgate business concept
// -- only "one terminal, one key, one line". Layout (color, tables) is in lib/style, not here.
package term

import (
	"bufio"
	"errors"
	"os"
	"strings"
)

// ErrNotATTY says "stdin is not a terminal". Every interactive
// UI should ask once (IsTTY) before doing anything, and then give the
// sentence "this machine cannot do interactive; use the flag version".
var ErrNotATTY = errors.New("stdin is not a terminal")

// Key is a **semantic key**. UIs dispatch on this, not on bytes.
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
	// KeyInterrupt is Ctrl-C: the UI treats it as "abandon this wizard", **not** a crash.
	KeyInterrupt
	// KeyEOF is stdin being closed (pipe exhausted). Separate
	// from Ctrl-C: that one was not pressed by the user.
	KeyEOF
	KeyRune
	// KeyOther is an unrecognized control key. UIs usually ignore it.
	KeyOther
)

// Event is one key press. When Key == KeyRune, Rune is
// valid (the character pressed, already UTF-8 decoded).
type Event struct {
	Key  Key
	Rune rune
}

// Terminal is a terminal that has entered raw mode.
//
// By value semantics it is "this one interactive session": Open into it, Close out
// of it. All rendering is still the caller's job (this package prints nothing),
// because layout is the UI's business and keys are the terminal's business.
type Terminal struct {
	in       *bufio.Reader
	restore  func()
	out      *os.File
	echoless bool
}

// Open enters raw mode (no echo, no line buffering, Ctrl-C produces
// no signal -- the UI itself turns KeyInterrupt into "user gave up").
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

// Close restores the terminal. Idempotent -- the caller defers it, so even an
// error midway does not leave the terminal in a "typing is invisible, Enter does
// nothing" state (the kind of damage that forces the user to open a new terminal).
func (t *Terminal) Close() {
	if t == nil || t.restore == nil {
		return
	}
	t.restore()
	t.restore = nil
}

// ReadEvent reads one key. It **blocks** until there is an expressible event (an
// unrecognized control key returns KeyOther rather than swallowing input forever).
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
	case 1: // Ctrl-A: start of line
		return Event{Key: KeyHome}
	case 5: // Ctrl-E: end of line
		return Event{Key: KeyEnd}
	case 8, 127:
		return Event{Key: KeyBackspace}
	case 21: // Ctrl-U: clear this line (used by ReadLine)
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
	// UTF-8 multibyte: read the continuation bytes all the way and
	// decode to a rune. One extra byte read is enough to tell the length.
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

// readEscape decodes the `ESC [ …` family of sequences.
//
// It only recognizes the following bytes **already in the buffer** (same as modules/tui did):
// arrow keys, Home/End are written in one write in a real terminal, so they always arrive together.
// A lone ESC (the user pressed the Esc key) has no bytes to read after it, so it returns KeyEsc
// -- no timeout is waited for here, which would otherwise stall every Esc by tens of milliseconds.
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
	case 'Z': // Shift-Tab: cycle backwards
		return Event{Key: KeyOther, Rune: 'Z'}
	case '1', '3', '4', '7', '8':
		// `ESC [ 3 ~` delete, `1~`/`7~` Home, `4~`/`8~` End. Consume the trailing `~`.
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

// LineMode is the switch for the behavior of one ReadLine call.
type LineMode struct {
	// With Secret=true only a mask is shown: a secret should not
	// appear on the terminal, nor stay in the scrollback buffer.
	Secret bool
	// Placeholder is the gray hint shown when the input is empty (given by the caller).
	Placeholder string
	// Validate validates **this line** once; a non-empty return string is the
	// reason, the UI shows it and does not end the line. nil = no validation.
	Validate func(string) string
}

// LineResult is the outcome of ReadLine.
type LineResult struct {
	Text   string
	Cancel bool // the user pressed Esc / Ctrl-C
	EOF    bool
}

// ReadLine reads a whole line in raw mode (Enter ends
// it), with echo, backspace, Home/End, and clear.
//
// # Why not "leave raw mode + bufio.Scanner"
//
// That would make the whole wizard flash on every line of input (the terminal returns to
// line mode, the scroll position jumps), and masked input would still need echo turned off
// by hand. More practically: the **previous/next step** key semantics in the wizard must
// match the list screen (Esc = go back a step), and mixing two read methods cannot do that.
//
// render is given by the caller: it receives "the text entered so far" and is
// responsible for drawing the whole screen (including prompt and validation error).
// This package only handles keys and text state -- layout belongs to the UI.
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
			// Cursor back to the line start: this implementation does no in-line cursor
			// movement (one line of input is short), but Home must have a **definite**
			// meaning rather than being silently swallowed. Choosing "clear" is
			// what the user really wants in this scene (retyping a whole address).
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

// IsTTY reports whether stdin is a terminal. Interactive UIs ask it once before doing anything.
func IsTTY() bool { return isTTY(os.Stdin.Fd()) }
