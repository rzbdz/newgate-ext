package main

import (
	"fmt"
	"html/template"
	"strings"
)

// themeMeta is the two header lines (`<meta name="theme-color">`).
//
// # Why two lines
//
// `theme-color`'s content **does not accept** `(prefers-color-scheme)` - the two lines
// are two declarations of one attribute, and the browser picks the last matching one
// (today only Chromium reads the media attribute; others take the first, dark, the default).
//
// # Order (the line before media)
//
// Emit **dark** first, then light. On load the URL bar is tinted the first color, then
// the media one applies - reversed, a light-theme user sees a light flash then dark (or
// the reverse). Putting the default first keeps that flash shortest.
//
// Neither line present returns an empty string: the slot becomes an empty line, no effect.
func themeMeta(th theme) template.HTML {
	if th.Dark == "" || th.Light == "" {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "<meta name=\"theme-color\" content=%q>\n", th.Dark)
	fmt.Fprintf(&b, "<meta name=\"theme-color\" content=%q media=\"(prefers-color-scheme: light)\">\n", th.Light)
	return template.HTML(b.String())
}

// theme is what the browser's **own UI** (phone address bar, status bar) is tinted.
//
// It has exactly one source: the two `--bg` values in the site stylesheet.
type theme struct {
	Dark  string
	Light string
}

// themeColors reads the base color for both themes out of the site stylesheet.
//
// # Why **read** the CSS instead of writing it again in site.json
//
// All `theme-color` does is "make the browser's own UI look like the page" - so it must
// **equal** `--bg`. A second copy in config would silently keep the old value on the day
// a redesign changes the base, visible **only on phones** as a wrong-colored address bar
// - desktop browsers never read it, so a dev machine never shows it.
//
// # Unreadable means error, not guess
//
// None, exactly one, more than two: all three error. A guessed color is worse than no
// theme-color (it looks right). Same attitude as md.go toward unknown markdown: a
// silent wrong drawing is the only unacceptable outcome.
func themeColors(css string) (theme, error) {
	var th theme
	vals, err := cssVar(stripComments(css), "--bg")
	if err != nil {
		return th, err
	}
	switch len(vals) {
	case 2:
		// Order is meaning: the first is the default (dark), the second is inside
		// `@media (prefers-color-scheme: light)` (light). This mirrors site.css's
		// structure - that is how it is written there, and this check forces that shape.
	default:
		return th, fmt.Errorf("site.css has %d `--bg` declarations -- there should be exactly two (the default one is dark,"+
			"and the one inside `@media (prefers-color-scheme: light)` is light). theme-color cannot be taken"+
			"from a single value without getting it wrong, so nothing is guessed here", len(vals))
	}
	for i, v := range vals {
		if !strings.HasPrefix(v, "#") {
			// theme-color takes any CSS color, but **a phone's address bar** only handles
			// solid ones. If the value does not start with `#` (gradients, var(...),
			// keywords), say so rather than ship a string the browser discards.
			return th, fmt.Errorf("`--bg` number %d in site.css has the value %q -- theme-color needs"+
				"a solid color starting with `#` (the address bar cannot take a gradient)", i+1, v)
		}
	}
	th.Dark, th.Light = vals[0], vals[1]
	return th, nil
}

// cssVar extracts all values of a custom property from a CSS file (in order).
//
// It is **not** a CSS parser; it only knows "`--name` up to the next `;`". Enough, and a
// misread costs **nothing** - `themeColors` errors at once. Names are compared by the
// char right after `--bg`, so `--bg-soft` is not mistaken for `--bg`.
func cssVar(css, name string) ([]string, error) {
	var out []string
	for {
		i := strings.Index(css, name)
		if i < 0 {
			return out, nil
		}
		rest := css[i+len(name):]
		if rest == "" || (rest[0] != ':' && rest[0] != ' ') {
			// `--bg-soft:` / `--bgfoo:` are name fragments, not it.
			css = rest
			continue
		}
		colon := strings.IndexByte(rest, ':')
		if colon < 0 {
			return nil, fmt.Errorf("`%s` is not followed by a colon", name)
		}
		// Only whitespace may sit between name and colon (`--bg : red` is valid CSS).
		if strings.TrimSpace(rest[:colon]) != "" {
			css = rest
			continue
		}
		rest = rest[colon+1:]
		semi := strings.IndexByte(rest, ';')
		if semi < 0 {
			// Unclosed declaration: **treat it as absent** rather than error - a truncated
			// CSS is mid-edit, and this judges "how many --bg there are", not validity.
			return out, nil
		}
		if v := strings.TrimSpace(rest[:semi]); v != "" {
			out = append(out, v)
		}
		css = rest[semi+1:]
		if css == "" {
			return out, nil
		}
	}
}

// stripComments removes CSS comments.
//
// Without it, a `--bg` inside a comment is read by cssVar as a declaration - and this
// stylesheet comments heavily (every criterion carries a "why"), so mentioning `--bg` is normal.
func stripComments(css string) string {
	var b strings.Builder
	for {
		i := strings.Index(css, "/*")
		if i < 0 {
			b.WriteString(css)
			return b.String()
		}
		b.WriteString(css[:i])
		j := strings.Index(css[i:], "*/")
		if j < 0 {
			// Unclosed comment: the rest is a comment (browsers read it that way too).
			return b.String()
		}
		css = css[i+j+2:]
	}
}
