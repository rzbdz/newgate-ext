package hermes

import (
	"bytes"
	"encoding/json"
	"strconv"
	"strings"
)

// 本文件是写进 config.yaml 的**值**：有序映射、列表、标量，以及把它们排成 YAML 行。
//
// 为什么值要有类型（而不是直接拼字符串行）：同一份值要交给两个读者——
//
//  1. emit 把它排成 YAML 行，写进文件；
//  2. 校验那一步（verify.go）把它序列化成 JSON，交给 **hermes 自己的 YAML 解析器**
//     去比：「原文件 + 这些改动」解析出来的东西，必须与「写出来的文件」解析出来的
//     **逐值相等**。
//
// 只有值是结构化的，第二步才有一个可比的「期望」——拼出来的字符串行没有。

// kv 是有序映射的一项。
type kv struct {
	K string
	V any
}

// omap 是**有序**映射：写进文件的顺序就是这里的顺序（给人看的文件，每次接管排出
// 同一个样子，理由见 codex 的 rewrite）。
type omap []kv

// MarshalJSON 按顺序写成 JSON 对象（校验时交给 Python，它只关心值）。
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

// scalarLit 标量的 YAML 字面量；不是标量返回 false。
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

// emit 把 key: v 排成行，缩进 indent。
//
// 形状与 hermes 落盘的一致：块风格、子级多两格、空映射/空列表写成 `{}` / `[]`、
// 列表项缩进在父键下面两格。
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
				// 列表里只放标量：今天写的只有那种，复杂项的排版是另一套规则，
				// 写一半不如不写。
				panic("hermes: emit: non-scalar list item")
			}
			out = append(out, pad+"  - "+s)
		}
		return out
	}
	panic("hermes: emit: unsupported value type")
}

// edit 是一处改动：把 Path 写成 Value；Value 为 nil = 删掉这个键。
//
// FromOrig 是还原那条路用的（见 takeover.go 的 Restore）：这一处的值取自**原件**
// （接管前那一份）——原件里有就放回原样，没有就删掉。它不走 put，走 putRaw。
type edit struct {
	Path     []string
	Value    any
	FromOrig bool
}

// put 执行一处改动，返回是否真的改了（没变的不记账）。
//
// 那个键已经在：整块换掉（原来是块、现在是标量也一样——留着子行就成了一份解析
// 不了的文件）。不在：补在父块末尾，父级缺的一路补出来（见 ensure）。
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
	// 同一个标量、只是引号写法不同（`normal` vs `"normal"`）：不改，免得记一笔假账。
	if s.end == s.head+1 && len(block) == 1 {
		if want, ok := scalarLit(e.Value); ok && sameScalar(valueOf(d.lines[s.head]), want) {
			return false, nil
		}
	}
	d.replace(s, block...)
	return true, nil
}
