package hermes

import (
	"bytes"
	"encoding/json"
	"strconv"
	"strings"
)

// This file is the **values** written into config.yaml: ordered mappings, lists, scalars, and
// laying them out as YAML lines.
//
// Why values need types (rather than string lines assembled directly): the same value goes to
// two readers --
//
//  1. emit lays it out as YAML lines and writes it to the file;
//  2. the verify step (verify.go) serializes it to JSON and hands it to **hermes' own YAML
//     parser** for comparison: what "the original file + these edits" parses to must be
//     **value-for-value equal** to what "the written file" parses to.
//
// Only with structured values does the second step have a comparable "expected" -- assembled
// string lines have none.

// kv is one entry of an ordered mapping.
type kv struct {
	K string
	V any
}

// omap is an **ordered** mapping: the order written to the file is the order here (a
// human-facing file, laid out the same way on every takeover, reason in codex's rewrite).
type omap []kv

// MarshalJSON writes a JSON object in order (handed to Python at verify time; it cares only
// about values).
func (m omap) MarshalJSON() ([]byte, error) {
	var b bytes.Buffer
	b.WriteByte('{')
	for i, e := range m {
		if i > 0 {
			b.WriteByte(',')
		}
		k, _ := json.Marshal(e.K)
		b.Write(k)
		b.WriteByte(':')
		v, err := json.Marshal(e.V)
		if err != nil {
			return nil, err
		}
		b.Write(v)
	}
	b.WriteByte('}')
	return b.Bytes(), nil
}

// scalarLit the YAML literal of a scalar; false if it is not a scalar.
func scalarLit(v any) (string, bool) {
	switch x := v.(type) {
	case string:
		return lit(x), true
	case int:
		return strconv.Itoa(x), true
	case bool:
		if x {
			return "true", true
		}
		return "false", true
	}
	return "", false
}

// emit lays out key: v as lines, indented by indent.
//
// The shape matches hermes' dump: block style, children two spaces deeper, empty mappings/lists
// written as `{}` / `[]`, list items indented two spaces under their parent key.
func emit(key string, v any, indent int) []string {
	pad := strings.Repeat(" ", indent)
	if s, ok := scalarLit(v); ok {
		return []string{pad + key + ": " + s}
	}
	switch x := v.(type) {
	case omap:
		if len(x) == 0 {
			return []string{pad + key + ": {}"}
		}
		out := []string{pad + key + ":"}
		for _, e := range x {
			out = append(out, emit(e.K, e.V, indent+2)...)
		}
		return out
	case []any:
		if len(x) == 0 {
			return []string{pad + key + ": []"}
		}
		out := []string{pad + key + ":"}
		for _, item := range x {
			s, ok := scalarLit(item)
			if !ok {
				// Lists hold scalars only: that is all we write today, and laying out complex items
				// is another set of rules -- half-written is worse than not written.
				panic("hermes: emit: non-scalar list item")
			}
			out = append(out, pad+"  - "+s)
		}
		return out
	}
	panic("hermes: emit: unsupported value type")
}

// edit is one change: write Value at Path; Value nil = remove this key.
//
// FromOrig is for the restore path (see Restore in takeover.go): this place's value comes from
// the **original** (the pre-takeover copy) -- put it back as it was if present there, remove it
// if not. It does not go through put, it goes through putRaw.
type edit struct {
	Path     []string
	Value    any
	FromOrig bool
}

// put applies one edit and returns whether it really changed anything (no change, no record).
//
// If the key is already there: replace the whole block (the same when it was a block and is now
// a scalar -- leaving the child lines makes an unparseable file). If not: append at the end of
// the parent block, creating whatever levels are missing (see ensure).
func (d *doc) put(e edit) (bool, error) {
	if e.Value == nil {
		return d.remove(e.Path...), nil
	}
	parent, key := e.Path[:len(e.Path)-1], e.Path[len(e.Path)-1]
	from, to, indent := 0, len(d.lines), 0
	if len(parent) > 0 {
		p, err := d.ensure(parent...)
		if err != nil {
			return false, err
		}
		from, to, indent = p.head+1, p.end, d.childIndent(p)
	}
	block := emit(key, e.Value, indent)
	s, ok := d.find(from, to, indent, key)
	if !ok {
		d.insert(to, block...)
		return true, nil
	}
	if strings.Join(d.lines[s.head:s.end], "\n") == strings.Join(block, "\n") {
		return false, nil
	}
	// Same scalar, only a different quoting style (`normal` vs `"normal"`): no edit, so no false
	// record.
	if s.end == s.head+1 && len(block) == 1 {
		if want, ok := scalarLit(e.Value); ok && sameScalar(valueOf(d.lines[s.head]), want) {
			return false, nil
		}
	}
	d.replace(s, block...)
	return true, nil
}
