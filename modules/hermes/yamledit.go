package hermes

import (
	"encoding/json"
	"strings"
)

// This file is the **byte surgery** on `config.yaml`: edit a few keys by path, leave the rest
// byte for byte.
//
// # Why not a YAML round trip
//
// The same reason as codex's TOML (see modules/codex/takeover.go): one parse → serialize
// changes ordering, quoting style, blank lines, and comments the user added by hand that hermes
// never wrote -- and that loss turns no test red. The other reason is this repository's own: the
// distribution does not pull in a whole YAML library for one client (the build is offline, and
// every line in go.mod needs a reason).
//
// # Which kind of YAML it understands
//
// **The kind hermes itself writes**: block style, two-space indent, empty mappings written as
// `{}`, list items indented under their parent key (`hermes_cli/config.py` dumps with
// yaml.safe_dump; the 568-line file on this machine on 2026-11-04 has exactly this shape).
// Surgery needs only these few rules:
//
//   - a key = one line `<indent>key: value` or `<indent>key:` (followed by a deeper-indented
//     block);
//   - a key's "block" = every line below it with deeper indentation (blank lines, comments and
//     list items count as part of it);
//   - every scalar written uses a JSON string literal (which is also a valid YAML
//     double-quoted scalar), so there is no need to worry about values like `:`, `#`, `yes`/`no`
//     that YAML would interpret otherwise.
//
// Shapes it does not recognize (flow-style non-empty mappings `{a: 1}`, multi-line scalars,
// anchors) are **not force-edited**: the caller gets an error, takeover reports one actionable
// sentence, and the file stays put -- better to fail to connect than to corrupt someone else's
// config.

// doc is the file split into lines. All edits happen on lines, then everything is joined back.
type doc struct {
	lines []string
	// trailingNL whether the original ends with a newline -- the result must match (reason in
	// codex's rewrite).
	trailingNL bool
}

func parseDoc(src string) *doc {
	d := &doc{trailingNL: strings.HasSuffix(src, "\n")}
	src = strings.TrimSuffix(src, "\n")
	if src != "" {
		d.lines = strings.Split(src, "\n")
	}
	return d
}

func (d *doc) String() string {
	out := strings.Join(d.lines, "\n")
	if d.trailingNL || len(d.lines) > 0 {
		out += "\n"
	}
	return out
}

// indentOf how many spaces a line starts with; blank lines and pure comment lines return -1
// (they do not divide block boundaries).
func indentOf(line string) int {
	t := strings.TrimLeft(line, " ")
	if t == "" || strings.HasPrefix(t, "#") {
		return -1
	}
	return len(line) - len(t)
}

// keyOf extracts the key of a `key: …` / `key:` line; lines that are not mapping keys (list
// items, continuations) return false.
//
// The key must be a bare identifier (hermes' keys all are): quoted keys and `? ` complex keys
// are never recognized -- misread one line and the edit lands on someone else's value.
func keyOf(line string) (string, bool) {
	t := strings.TrimLeft(line, " ")
	if t == "" || t[0] == '#' || t[0] == '-' || t[0] == '"' || t[0] == '\'' {
		return "", false
	}
	i := strings.Index(t, ":")
	if i <= 0 {
		return "", false
	}
	if i+1 < len(t) && t[i+1] != ' ' {
		return "", false // `a:b` is a scalar, not a key
	}
	k := t[:i]
	if strings.ContainsAny(k, " \t{}[],") {
		return "", false
	}
	return k, true
}

// valueOf extracts the text after the colon of a `key: value` line (surrounding whitespace
// trimmed).
func valueOf(line string) string {
	t := strings.TrimLeft(line, " ")
	i := strings.Index(t, ":")
	if i < 0 {
		return ""
	}
	return strings.TrimSpace(t[i+1:])
}

// span is the lines a key occupies in the file: [head, end). head is the key's line, end is the
// first line after its block (or end of file). indent is the key line's indentation.
type span struct {
	head, end, indent int
}

// blockEnd from the line after head, finds the first meaningful line whose indent <= indent.
//
// Exception: list items (`- x`) whose indent **equals** indent count as inside the block -- YAML
// allows a list under `key:` to sit at the same column as the key (PyYAML writes it that way by
// default), and those items belong to this key.
func (d *doc) blockEnd(head, indent int) int {
	end := head + 1
	for end < len(d.lines) {
		ind := indentOf(d.lines[end])
		if ind < 0 {
			end++
			continue
		}
		if ind > indent {
			end++
			continue
		}
		if ind == indent && strings.HasPrefix(strings.TrimLeft(d.lines[end], " "), "- ") {
			end++
			continue
		}
		break
	}
	// Blank lines/comments at the end of the block do not belong to it: they are probably the
	// separator or note before the next key.
	for end > head+1 && indentOf(d.lines[end-1]) < 0 {
		end--
	}
	return end
}

// find looks for key `key` with exactly indent `indent` in [from, to).
func (d *doc) find(from, to, indent int, key string) (span, bool) {
	for i := from; i < to; i++ {
		if indentOf(d.lines[i]) != indent {
			continue
		}
		if k, ok := keyOf(d.lines[i]); ok && k == key {
			return span{head: i, end: d.blockEnd(i, indent), indent: indent}, true
		}
	}
	return span{}, false
}

// lookup finds a key by path; false if any level is missing.
func (d *doc) lookup(path ...string) (span, bool) {
	from, to, indent := 0, len(d.lines), 0
	var s span
	for i, k := range path {
		var ok bool
		s, ok = d.find(from, to, indent, k)
		if !ok {
			return span{}, false
		}
		if i < len(path)-1 {
			from, to = s.head+1, s.end
			indent = d.childIndent(s)
		}
	}
	return s, true
}

// childIndent how many spaces this key's children use: follow an existing child if there is
// one, else parent indent + 2 (hermes' style). Following what exists instead of hard-coding 2:
// in a file whose indent the user changed by hand, keys we add must align with their neighbors,
// or they become children of another parent key.
func (d *doc) childIndent(s span) int {
	for i := s.head + 1; i < s.end; i++ {
		if ind := indentOf(d.lines[i]); ind > s.indent {
			if _, ok := keyOf(d.lines[i]); ok {
				return ind
			}
		}
	}
	return s.indent + 2
}

// get reads a scalar (unquoted); false if absent, or if it is a block.
func (d *doc) get(path ...string) (string, bool) {
	s, ok := d.lookup(path...)
	if !ok {
		return "", false
	}
	v := valueOf(d.lines[s.head])
	if v == "" {
		return "", false
	}
	return unquote(v), true
}

// errShape is the kind of error for "I will not force-edit this shape".
type errShape struct{ path, why string }

func (e *errShape) Error() string { return e.path + ": " + e.why }

// ensure guarantees every level on the path is a **block** key (adding it at the end of the
// parent block if absent), and returns the last level.
//
// When the parent key is currently an "empty mapping" like `key: {}` / `key: null` / `key:`, it
// is turned into `key:` and things are added inside -- hermes writes an empty mapping as `{}`
// (`providers: {}` is one). When the parent key is a non-empty scalar or a non-empty flow
// mapping, **do not edit**: that is a file of the wrong shape, and overwriting it loses someone
// else's values.
func (d *doc) ensure(path ...string) (span, error) {
	from, to, indent := 0, len(d.lines), 0
	var s span
	for i, k := range path {
		found, ok := d.find(from, to, indent, k)
		if !ok {
			at := to
			line := strings.Repeat(" ", indent) + k + ":"
			d.insert(at, line)
			found = span{head: at, end: at + 1, indent: indent}
		} else if v := valueOf(d.lines[found.head]); v != "" {
			switch v {
			case "{}", "null", "~":
				d.lines[found.head] = strings.Repeat(" ", indent) + k + ":"
			default:
				return span{}, &errShape{path: strings.Join(path[:i+1], "."),
					why: "is a value, not a mapping — refusing to overwrite it"}
			}
		}
		s = found
		if i < len(path)-1 {
			from, to = s.head+1, s.end
			indent = d.childIndent(s)
		}
	}
	return s, nil
}

func (d *doc) insert(at int, lines ...string) {
	out := make([]string, 0, len(d.lines)+len(lines))
	out = append(out, d.lines[:at]...)
	out = append(out, lines...)
	out = append(out, d.lines[at:]...)
	d.lines = out
}

// remove deletes the key at path (together with its block). Returns whether it really removed it.
//
// If the parent block ends up empty, the parent key is written back as `key: {}`: a bare `key:`
// is **null** in YAML, not an empty mapping -- hermes takes a different path for
// `providers: null` than for `{}` (the verify step would catch it, but it should not be what we
// rely on).
func (d *doc) remove(path ...string) bool {
	s, ok := d.lookup(path...)
	if !ok {
		return false
	}
	d.replace(s)
	if len(path) > 1 {
		if p, ok := d.lookup(path[:len(path)-1]...); ok && p.end == p.head+1 &&
			valueOf(d.lines[p.head]) == "" {
			d.lines[p.head] += " {}"
		}
	}
	return true
}

func (d *doc) replace(s span, with ...string) {
	out := make([]string, 0, len(d.lines)-(s.end-s.head)+len(with))
	out = append(out, d.lines[:s.head]...)
	out = append(out, with...)
	out = append(out, d.lines[s.end:]...)
	d.lines = out
}

// children lists the direct child keys of a block key (in order of appearance).
func (d *doc) children(path ...string) []string {
	s, ok := d.lookup(path...)
	if !ok {
		return nil
	}
	ind := d.childIndent(s)
	var out []string
	for i := s.head + 1; i < s.end; i++ {
		if indentOf(d.lines[i]) == ind {
			if k, ok := keyOf(d.lines[i]); ok {
				out = append(out, k)
			}
		}
	}
	return out
}

// indentBlock prepends n spaces to every non-empty line of body.
func indentBlock(body []string, n int) []string {
	pad := strings.Repeat(" ", n)
	out := make([]string, len(body))
	for i, l := range body {
		if l == "" {
			out[i] = l
			continue
		}
		out[i] = pad + l
	}
	return out
}

// lit writes a string as a YAML scalar literal.
//
// Bare when possible (matching hermes' own dump style, keeping diffs small), otherwise a JSON
// string -- a JSON string is a valid YAML double-quoted scalar, so "which characters must be
// escaped" has exactly one answer. The criterion for "bare is possible" is deliberately
// conservative: only [A-Za-z0-9._/-], not starting with a digit or `-`, and not one of the words
// YAML would interpret otherwise. `normal` is bare, `newgate-ds` is bare,
// `smt-claude/claude-opus-5-5` is bare, `http://…` gets quotes.
func lit(s string) string {
	if plain(s) {
		return s
	}
	b, _ := json.Marshal(s)
	return string(b)
}

func plain(s string) bool {
	if s == "" {
		return false
	}
	switch strings.ToLower(s) {
	case "true", "false", "yes", "no", "on", "off", "null", "y", "n", "~":
		return false
	}
	if c := s[0]; c == '-' || c == '.' || (c >= '0' && c <= '9') {
		return false
	}
	for _, c := range s {
		ok := c == '.' || c == '_' || c == '/' || c == '-' ||
			(c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
		if !ok {
			return false
		}
	}
	return true
}

// unquote strips the quotes on both sides of a YAML scalar (double quotes parsed as JSON, single
// quotes with YAML's `”` doubling).
func unquote(v string) string {
	switch {
	case len(v) >= 2 && v[0] == '"' && v[len(v)-1] == '"':
		var s string
		if json.Unmarshal([]byte(v), &s) == nil {
			return s
		}
		return v[1 : len(v)-1]
	case len(v) >= 2 && v[0] == '\'' && v[len(v)-1] == '\'':
		return strings.ReplaceAll(v[1:len(v)-1], "''", "'")
	}
	return v
}

// sameScalar whether two literals are the same string value (`normal` and `"normal"` are the
// same).
func sameScalar(a, b string) bool { return unquote(a) == unquote(b) }

// raw extracts the verbatim lines of the key at path (together with its block), plus the key
// line's indentation.
func (d *doc) raw(path ...string) ([]string, int, bool) {
	s, ok := d.lookup(path...)
	if !ok {
		return nil, 0, false
	}
	return append([]string(nil), d.lines[s.head:s.end]...), s.indent, true
}

// putRaw puts the verbatim lines taken from another file (see raw) back at path: replace the
// whole block if present, else append at the end of the parent block. Indentation is shifted by
// the difference -- at restore time the original's and the current parent's indentation may
// differ (hermes rewrote the file), and after the shift it is still a child of that parent key.
func (d *doc) putRaw(lines []string, fromIndent int, path ...string) error {
	parent, key := path[:len(path)-1], path[len(path)-1]
	from, to, indent := 0, len(d.lines), 0
	if len(parent) > 0 {
		p, err := d.ensure(parent...)
		if err != nil {
			return err
		}
		from, to, indent = p.head+1, p.end, d.childIndent(p)
	}
	shifted := make([]string, len(lines))
	for i, l := range lines {
		switch {
		case indentOf(l) < 0 && strings.TrimSpace(l) == "":
			shifted[i] = l
		case indent >= fromIndent:
			shifted[i] = strings.Repeat(" ", indent-fromIndent) + l
		default:
			cut := fromIndent - indent
			if len(l) >= cut && strings.TrimLeft(l[:cut], " ") == "" {
				shifted[i] = l[cut:]
			} else {
				shifted[i] = strings.TrimLeft(l, " ")
			}
		}
	}
	if s, ok := d.find(from, to, indent, key); ok {
		d.replace(s, shifted...)
		return nil
	}
	d.insert(to, shifted...)
	return nil
}

// mapChildren is like children but only for those whose **value is a mapping** (followed by a
// block, or written as `{}`).
//
// Why the distinction: in a real file a mapping does not necessarily contain only one kind of
// thing. On this machine on 2026-11-04, `auxiliary:` in config.yaml had a dozen `display`
// scalars mixed in (`compact: false`, `skin: default`, … -- some migration must have written
// the wrong indent). Editing them as auxiliary tasks means stuffing child keys into
// `compact: false`: that is exactly where the first version was rejected by ensure (which did
// the right thing). The criterion for "which are tasks" is therefore shape: only those whose
// value is a mapping.
func (d *doc) mapChildren(path ...string) []string {
	s, ok := d.lookup(path...)
	if !ok {
		return nil
	}
	ind := d.childIndent(s)
	var out []string
	for i := s.head + 1; i < s.end; i++ {
		if indentOf(d.lines[i]) != ind {
			continue
		}
		k, ok := keyOf(d.lines[i])
		if !ok {
			continue
		}
		if v := valueOf(d.lines[i]); v == "" || v == "{}" {
			out = append(out, k)
		}
	}
	return out
}
