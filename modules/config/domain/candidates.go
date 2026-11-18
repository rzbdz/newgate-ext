package domain

import (
	"encoding/json"
	"strings"

	"github.com/rzbdz/newgate/lib/i18n"
)

// Candidates 一个键的候选列表。接受四种 JSON 写法，内部统一成 list：
//
//	"heavy": {"provider":"p","model":"m"}      对象（现状，继续支持）
//	"heavy": "p/m"                             字符串简写
//	"heavy": "@normal"                         引用另一个键（就地展开那条链）
//	"heavy": ["@normal", {"provider":"p2",…}]   列表（引用与绑定可混写）
//
// 统一之后代码里只有一条路径。
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
	// 单个就写成对象，保持文件可读；多个写成数组
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

// ParseBindingString 解析 "provider/model" 或 "@另一个键"。
// model 里可以再含 /（有些模型名带斜杠）。
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
