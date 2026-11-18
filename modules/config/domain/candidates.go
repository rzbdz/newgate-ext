package domain

import (
	"encoding/json"
	"strings"

	"github.com/rzbdz/newgate/lib/i18n"
)

// Candidates is a key's candidate list. Accepts four
// JSON spellings, unified internally into a list:
//
//	"heavy": {"provider":"p","model":"m"}      object (the status quo, still supported)
//	"heavy": "p/m"                             string shorthand
//	"heavy": "@normal"                         reference to another key (expands that chain in place)
//	"heavy": ["@normal", {"provider":"p2",...}] list (references and bindings can be mixed)
//
// After unification there is only one path through the code.
type Candidates []Binding

func (c *Candidates) UnmarshalJSON(b []byte) error {
	b = []byte(strings.TrimSpace(string(b)))
	if len(b) == 0 || string(b) == "null" {
		*c = nil
		return nil
	}
	switch b[0] {
	case '[':
		var raw []json.RawMessage
		if err := json.Unmarshal(b, &raw); err != nil {
			return err
		}
		out := make(Candidates, 0, len(raw))
		for i, r := range raw {
			bd, err := parseBinding(r)
			if err != nil {
				return i18n.Ef(err, "candidate {n}: {err}", i18n.A{"n": i + 1})
			}
			out = append(out, bd)
		}
		*c = out
		return nil
	default:
		bd, err := parseBinding(b)
		if err != nil {
			return err
		}
		*c = Candidates{bd}
		return nil
	}
}

func (c Candidates) MarshalJSON() ([]byte, error) {
	// Write a single one as an object, keeping the file readable; several as an array
	if len(c) == 1 {
		return json.Marshal(c[0])
	}
	return json.Marshal([]Binding(c))
}

func parseBinding(raw json.RawMessage) (Binding, error) {
	s := strings.TrimSpace(string(raw))
	if s == "" {
		return Binding{}, i18n.E("empty binding", nil)
	}
	if s[0] == '"' {
		var str string
		if err := json.Unmarshal(raw, &str); err != nil {
			return Binding{}, err
		}
		return ParseBindingString(str)
	}
	var b Binding
	if err := json.Unmarshal(raw, &b); err != nil {
		return Binding{}, err
	}
	switch {
	case b.Ref != "" && (b.Provider != "" || b.Model != ""):
		return Binding{}, i18n.E("a ref and provider/model cannot be combined: {binding}",
			i18n.A{"binding": s})
	case b.Ref != "":
		return b, nil
	case b.Provider == "" || b.Model == "":
		return Binding{}, i18n.E("provider and model must both be set: {binding}",
			i18n.A{"binding": s})
	}
	return b, nil
}

// ParseBindingString parses "provider/model" or "@another key".
// model may itself contain / (some model names have slashes).
func ParseBindingString(s string) (Binding, error) {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "@") {
		ref := strings.TrimSpace(s[1:])
		if ref == "" {
			return Binding{}, i18n.E("a ref must name a key, as in @key: {binding}",
				i18n.A{"binding": s})
		}
		if strings.ContainsAny(ref, "/ ") {
			return Binding{}, i18n.E("a ref carries the key only, no provider/model: {binding}",
				i18n.A{"binding": s})
		}
		return Binding{Ref: ref}, nil
	}
	i := strings.Index(s, "/")
	if i <= 0 || i == len(s)-1 {
		return Binding{}, i18n.E("a binding must be provider/model: {binding}",
			i18n.A{"binding": s})
	}
	return Binding{Provider: s[:i], Model: s[i+1:]}, nil
}
