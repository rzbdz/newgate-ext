package hermes

import (
	"encoding/json"
	"strings"
)

// 本文件是 `config.yaml` 的**字节手术**：按路径改几个键，其余逐字节不动。
//
// # 为什么不是 YAML 往返
//
// 与 codex 那份 TOML 同一条理由（见 modules/codex/takeover.go）：做一次解析→序列化，
// 顺序、引号风格、空行、以及 hermes 自己没写但用户手加的注释都会变——而那种损失不会
// 让任何测试变红。另一条是这个仓库自己的：发行版不为一个客户端拉进一整个 YAML 库
// （构建是离线的，go.mod 里每一行都要有理由）。
//
// # 它认识的 YAML 是哪一种
//
// **hermes 自己写出来的那一种**：块风格、两格缩进、空映射写成 `{}`、列表项缩进在
// 父键下面（`hermes_cli/config.py` 用 yaml.safe_dump 落盘，2026-11-04 本机那份
// 568 行就是这个形状）。手术只需要这几条：
//
//   - 一个键 = 一行 `<缩进>key: 值` 或 `<缩进>key:`（后面跟一个更深缩进的块）；
//   - 一个键的「块」= 它下面所有缩进更深的行（空行、注释、列表项都算在里面）；
//   - 写进去的标量一律用 JSON 字符串字面量（它同时是合法的 YAML 双引号标量），
//     所以不用操心 `:`、`#`、`yes`/`no` 这类会被 YAML 另作解释的取值。
//
// 认不出来的形状（流式的非空映射 `{a: 1}`、多行标量、锚点）**不硬改**：调用方拿到
// 错误，接管报一句让人能行动的话，文件不动——宁可接不上，也不写坏一份别人的配置。

// doc 是按行切开的文件。改动都在 lines 上做，最后再拼回去。
type doc struct {
	lines []string
	// trailingNL 原文是否以换行结尾——结果要保持一致（理由见 codex 的 rewrite）。
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

// indentOf 一行开头有几个空格；空行与纯注释行返回 -1（它们不划分块的边界）。
func indentOf(line string) int {
	t := strings.TrimLeft(line, " ")
	if t == "" || strings.HasPrefix(t, "#") {
		return -1
	}
	return len(line) - len(t)
}

// keyOf 取出 `key: …` / `key:` 这一行的键；不是映射键的行（列表项、续行）返回 false。
//
// 键必须是裸标识符（hermes 的键都是）：带引号的键、`? ` 复杂键一律不认——认错了
// 一行，改的就是别人的值。
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
		return "", false // `a:b` 是一个标量，不是键
	}
	k := t[:i]
	if strings.ContainsAny(k, " \t{}[],") {
		return "", false
	}
	return k, true
}

// valueOf 取出 `key: value` 那一行冒号之后的原文（去掉首尾空白）。
func valueOf(line string) string {
	t := strings.TrimLeft(line, " ")
	i := strings.Index(t, ":")
	if i < 0 {
		return ""
	}
	return strings.TrimSpace(t[i+1:])
}

// span 是一个键在文件里占的行：[head, end)。head 是键那一行，end 是它的块之后
// 第一行（或文件末尾）。indent 是键那一行的缩进。
type span struct {
	head, end, indent int
}

// blockEnd 从 head 的下一行起，找到第一行缩进 ≤ indent 的有效行。
//
// 例外：缩进**等于** indent 的列表项（`- x`）算在块里——YAML 允许 `key:` 下面的
// 列表与键同列（PyYAML 缺省就这么写），而那些项属于这个键。
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
	// 块末尾的空行/注释不归它：那多半是下一个键前面的分隔或说明。
	for end > head+1 && indentOf(d.lines[end-1]) < 0 {
		end--
	}
	return end
}

// find 在 [from, to) 里找缩进恰好为 indent 的键 key。
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

// lookup 按路径找一个键；任何一级不在就返回 false。
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

// childIndent 这个键的子键用几格缩进：有子键就照它的，没有就父缩进 + 2（hermes 的
// 写法）。照现有的走而不是写死 2：用户手改过缩进的文件，补进去的键要与邻居对齐，
// 否则就成了另一个父键的孩子。
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

// get 读一个标量（去掉引号）；不在、或它是个块，返回 false。
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

// errShape 是「这个形状我不硬改」的那一类错误。
type errShape struct{ path, why string }

func (e *errShape) Error() string { return e.path + ": " + e.why }

// ensure 保证路径上每一级都是一个**块**键（不存在就补在父块末尾），返回最后一级。
//
// 父键此刻是 `key: {}` / `key: null` / `key:` 这种「空映射」时，把它改成 `key:`
// 再往里补——hermes 把空映射写成 `{}`（`providers: {}` 就是）。父键是个非空标量
// 或非空流式映射时**不改**：那是一个形状不对的文件，覆盖掉它就是丢别人的值。
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

// remove 删掉 path 那个键（连同它的块）。返回是否真的删了。
//
// 删完父块空了，父键写回 `key: {}`：光秃秃的 `key:` 在 YAML 里是 **null**，不是空映射
// ——hermes 读到 `providers: null` 与读到 `{}` 走的不是同一条路（校验那一步会抓到它，
// 但不该靠它抓）。
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

// children 列出一个块键下面的直接子键（按出现顺序）。
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

// indentBlock 给 body 每一非空行前面加 n 个空格。
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

// lit 把一个字符串写成 YAML 标量字面量。
//
// 能裸写就裸写（与 hermes 自己的落盘风格一致，diff 小），否则用 JSON 字符串——
// JSON 字符串是合法的 YAML 双引号标量，所以「哪些字符要转义」只有一份答案。
// 「能裸写」的判据刻意保守：只认 [A-Za-z0-9._/-]，且不以数字或 `-` 开头、
// 不是 YAML 会另作解释的那几个词。`normal` 裸写，`newgate-ds` 裸写，
// `smt-claude/claude-opus-5-5` 裸写，`http://…` 加引号。
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

// unquote 去掉 YAML 标量两边的引号（双引号按 JSON 解，单引号按 YAML 的 `”` 转义）。
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

// sameScalar 两份字面量是不是同一个字符串值（`normal` 与 `"normal"` 是同一个）。
func sameScalar(a, b string) bool { return unquote(a) == unquote(b) }

// raw 取出 path 那个键原样的行（连同它的块），以及键那一行的缩进。
func (d *doc) raw(path ...string) ([]string, int, bool) {
	s, ok := d.lookup(path...)
	if !ok {
		return nil, 0, false
	}
	return append([]string(nil), d.lines[s.head:s.end]...), s.indent, true
}

// putRaw 把另一份文件里取来的原样行（见 raw）放回 path：已经在就整块换掉，不在就
// 补在父块末尾。缩进按差值平移——还原时原文与此刻的父级缩进可能不同（hermes 重写过
// 文件），平移之后它仍是那个父键的孩子。
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

// mapChildren 同 children，但只要**值是映射**的那些（下面跟着块，或者写成 `{}`）。
//
// 为什么要分开：真实文件里一个映射下面不一定只有一种东西。2026-11-04 本机那份
// config.yaml 的 `auxiliary:` 底下混进了十几个 `display` 的标量（`compact: false`、
// `skin: default`……——多半是哪一次迁移写错了缩进）。把它们当成辅助任务去改，就是
// 往 `compact: false` 里塞子键：第一版就是在这里被 ensure 拒掉的（它做对了）。
// 「哪些是任务」的判据于是是形状：值是映射的才是。
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
