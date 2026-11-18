package rewrite

import (
	"encoding/json"

	i18n "github.com/rzbdz/newgate/lib/i18n"
)

// ReplaceTopLevelString 在原始 JSON 字节里定位顶层 key 的字符串值，
// 只替换那一段，其余**每一个字节**原样保留——包括 key 顺序、缩进、空白、
// 数字字面量、Unicode 转义形式。
//
// 为什么不能用 Unmarshal/Marshal 往返：
//   - map[string]interface{} 会把整数变 float64（9007199254740993 → ...92）、
//     1e10 变 10000000000、所有 key 重排
//   - map[string]json.RawMessage 保住了嵌套字节，但顶层 key 仍被重排、空白仍被压掉
//
// 转发一个请求就该是转发，不该顺手重写它。
func ReplaceTopLevelString(body []byte, key, newVal string) ([]byte, error) {
	start, end, err := findTopLevelStringValue(body, key)
	if err != nil {
		return nil, err
	}
	quoted, err := json.Marshal(newVal)
	if err != nil {
		return nil, err
	}
	out := make([]byte, 0, len(body)-(end-start)+len(quoted))
	out = append(out, body[:start]...)
	out = append(out, quoted...)
	out = append(out, body[end:]...)
	return out, nil
}

// ReplaceTopLevelRaw 把顶层 key 的整个值换成 newRaw（原始 JSON 字节），
// 其余字节原样保留。用于只重写 tools 而不动 messages。
func ReplaceTopLevelRaw(body []byte, key string, newRaw []byte) ([]byte, error) {
	start, end, err := findTopLevelValue(body, key)
	if err != nil {
		return nil, err
	}
	out := make([]byte, 0, len(body)-(end-start)+len(newRaw))
	out = append(out, body[:start]...)
	out = append(out, newRaw...)
	out = append(out, body[end:]...)
	return out, nil
}

// TopLevelRaw 只读地取出顶层 key 的原始值字节。
func TopLevelRaw(body []byte, key string) ([]byte, bool) {
	s, e, err := findTopLevelValue(body, key)
	if err != nil {
		return nil, false
	}
	return body[s:e], true
}

// InsertTopLevelRaw 在顶层对象里**新增**一个 key，插在开头（紧跟 `{`），
// 其余每个字节原样保留。已经存在同名 key 时报错——调用方要先用
// TopLevelRaw 判断，然后决定是插入还是 ReplaceTopLevelRaw，
// 绝不能悄悄产生重复 key。
//
// 为什么插在开头而不是末尾：末尾要处理尾随空白、换行、以及「最后一个值
// 后面到 `}` 之间有什么」，出错概率高；开头只需要判断对象是不是空的。
func InsertTopLevelRaw(body []byte, key string, raw []byte) ([]byte, error) {
	if _, _, err := findTopLevelValue(body, key); err == nil {
		return nil, errDupKey
	}
	i := skipWS(body, 0)
	if i >= len(body) || body[i] != '{' {
		return nil, errNotObject
	}
	j := skipWS(body, i+1)
	if j >= len(body) {
		return nil, errNotObject
	}
	quoted, err := json.Marshal(key)
	if err != nil {
		return nil, err
	}
	ins := make([]byte, 0, len(quoted)+len(raw)+2)
	ins = append(ins, quoted...)
	ins = append(ins, ':')
	ins = append(ins, raw...)
	if body[j] != '}' { // 对象非空，得补逗号
		ins = append(ins, ',')
	}
	out := make([]byte, 0, len(body)+len(ins))
	out = append(out, body[:i+1]...)
	out = append(out, ins...)
	out = append(out, body[i+1:]...)
	return out, nil
}

// EnsureArrayItemField 给顶层数组（messages 等）里满足 match 的每个对象元素
// 补上一个字段——**只在该元素还没有这个字段时**补，已有的一律不动。
//
// 依然是纯字节手术：把所有插入点先收集起来，再一次性拼接。除了插入的那几段，
// 每条消息的 content 块、cache_control、Unicode 转义形式、缩进都逐字节不变。
// 这一点是硬要求：把整个 body 做一次 JSON 往返会重排 key、把大整数变成
// float64、压掉空白——转发一个请求就该是转发。
//
// match 收到的是这条元素完整的原始字节（一个 JSON 对象），可以直接用
// TopLevelString(item, "role") 之类去判断。
// 返回补了几条；数组不存在或元素不是对象时返回 0 而不报错的情况见 err。
func EnsureArrayItemField(body []byte, arrayKey, field string, rawVal []byte,
	match func(item []byte) bool) ([]byte, int, error) {

	return EnsureArrayItemFieldFunc(body, arrayKey, field,
		func([]byte) []byte { return rawVal }, match)
}

// EnsureArrayItemFieldFunc 同 EnsureArrayItemField，但值**逐条算**：
// val 收到这条元素的原始字节，返回要插的值，返回 nil 表示这条跳过。
//
// 为什么需要逐条：推理内容回填时每条 assistant 消息要插的是**它自己那轮**
// 的推理，不是同一个常量。给所有消息插同一段推理，等于把别人的思考塞给
// 这一轮，比补空串更糟。
func EnsureArrayItemFieldFunc(body []byte, arrayKey, field string,
	val func(item []byte) []byte, match func(item []byte) bool) ([]byte, int, error) {

	s, e, err := findTopLevelValue(body, arrayKey)
	if err != nil {
		return body, 0, err
	}
	arr := body[s:e]
	spans, ok := arrayItemSpans(arr)
	if !ok {
		return body, 0, errNotArray
	}
	quoted, err := json.Marshal(field)
	if err != nil {
		return body, 0, err
	}

	type point struct {
		off   int    // body 里的绝对偏移（元素 `{` 之后）
		raw   []byte // 这条元素要插的值
		comma bool   // 元素非空时要补逗号
	}
	var points []point

	for _, sp := range spans {
		item := arr[sp[0]:sp[1]]
		if len(item) == 0 || item[0] != '{' {
			continue // 元素不是对象：不是我们该管的形状，跳过
		}
		if _, _, err := findTopLevelValue(item, field); err == nil {
			continue // 已经有了，一个字节都不碰
		}
		if match != nil && !match(item) {
			continue
		}
		raw := val(item)
		if raw == nil {
			continue
		}
		j := skipWS(item, 1)
		points = append(points, point{
			off:   s + sp[0] + 1,
			raw:   raw,
			comma: j < len(item) && item[j] != '}',
		})
	}

	if len(points) == 0 {
		return body, 0, nil // 没东西要补：调用方保持原字节
	}

	out := make([]byte, 0, len(body)+len(points)*(len(quoted)+16))
	prev := 0
	for _, p := range points {
		out = append(out, body[prev:p.off]...)
		out = append(out, quoted...)
		out = append(out, ':')
		out = append(out, p.raw...)
		if p.comma {
			out = append(out, ',')
		}
		prev = p.off
	}
	out = append(out, body[prev:]...)
	return out, len(points), nil
}

// EnsureArrayItemArrayHead 往顶层数组 arrayKey（messages）里满足 match 的每个
// 对象元素的**子数组**字段 field（content）的**开头**插入 rawVal，
// 且只在 need(childArr) 为真时插。返回插了几条。
//
// 为什么要单独一个原语：EnsureArrayItemField 只能往元素对象上补顶层字段，
// 够不着元素里面那个 content 数组。而 Anthropic 协议里 thinking 是
// content[] 的一个**块**，且必须排在 text / tool_use 之前——位置是协议的
// 一部分，不能随便追加到末尾。
//
// 依旧是纯字节手术：先把所有插入点收集齐，再一次拼接。除了插进去的那几段，
// 每个块的 cache_control、tool_use 的 input、缩进、转义形式逐字节不变。
//
// 跳过（不算错，也不算改）：元素不是对象、没有 field 字段、field 的值不是
// 数组（content 是纯字符串时就是这样）。
func EnsureArrayItemArrayHead(body []byte, arrayKey, field string, rawVal []byte,
	match func(item []byte) bool, need func(childArr []byte) bool) ([]byte, int, error) {

	return EnsureArrayItemArrayHeadFunc(body, arrayKey, field,
		func([]byte) []byte { return rawVal }, match, need)
}

// EnsureArrayItemArrayHeadFunc 同上，但值逐条算（val 返回 nil = 跳过）。
// 理由同 EnsureArrayItemFieldFunc：每条 assistant 消息要插的是它自己那轮的
// thinking 块。
func EnsureArrayItemArrayHeadFunc(body []byte, arrayKey, field string,
	val func(item []byte) []byte,
	match func(item []byte) bool, need func(childArr []byte) bool) ([]byte, int, error) {

	s, e, err := findTopLevelValue(body, arrayKey)
	if err != nil {
		return body, 0, err
	}
	arr := body[s:e]
	spans, ok := arrayItemSpans(arr)
	if !ok {
		return body, 0, errNotArray
	}

	type point struct {
		off   int    // body 里的绝对偏移（子数组 `[` 之后）
		raw   []byte // 这条元素要插的块
		comma bool   // 子数组非空时要补逗号
	}
	var points []point

	for _, sp := range spans {
		item := arr[sp[0]:sp[1]]
		if len(item) == 0 || item[0] != '{' {
			continue
		}
		if match != nil && !match(item) {
			continue
		}
		cs, ce, err := findTopLevelValue(item, field)
		if err != nil {
			continue // 没有 content 字段
		}
		child := item[cs:ce]
		if len(child) == 0 || child[0] != '[' {
			continue // content 是字符串：没有块可插
		}
		if need != nil && !need(child) {
			continue
		}
		raw := val(item)
		if raw == nil {
			continue
		}
		j := skipWS(child, 1)
		points = append(points, point{
			off:   s + sp[0] + cs + 1,
			raw:   raw,
			comma: j < len(child) && child[j] != ']',
		})
	}

	if len(points) == 0 {
		return body, 0, nil
	}

	out := make([]byte, 0, len(body)+len(points)*32)
	prev := 0
	for _, p := range points {
		out = append(out, body[prev:p.off]...)
		out = append(out, p.raw...)
		if p.comma {
			out = append(out, ',')
		}
		prev = p.off
	}
	out = append(out, body[prev:]...)
	return out, len(points), nil
}

// AppendLastArrayItemArray 往顶层数组最后一个对象元素的子数组末尾追加 rawVal。
// match 用来确认最后一项确实是调用方要处理的形状。只插入这一段字节，其余
// 请求（包括大整数、字段顺序和未知字段）保持原样。
func AppendLastArrayItemArray(body []byte, arrayKey, field string, rawVal []byte,
	match func(item []byte) bool) ([]byte, bool, error) {
	s, e, err := findTopLevelValue(body, arrayKey)
	if err != nil {
		return body, false, err
	}
	arr := body[s:e]
	spans, ok := arrayItemSpans(arr)
	if !ok {
		return body, false, errNotArray
	}
	if len(spans) == 0 {
		return body, false, nil
	}
	sp := spans[len(spans)-1]
	item := arr[sp[0]:sp[1]]
	if len(item) == 0 || item[0] != '{' || (match != nil && !match(item)) {
		return body, false, nil
	}
	cs, ce, err := findTopLevelValue(item, field)
	if err != nil {
		return body, false, err
	}
	child := item[cs:ce]
	if len(child) == 0 || child[0] != '[' {
		return body, false, errNotArray
	}
	close := len(child) - 1
	for close > 0 {
		switch child[close] {
		case ' ', '\t', '\r', '\n':
			close--
			continue
		}
		break
	}
	if child[close] != ']' {
		return body, false, errNotArray
	}
	j := skipWS(child, 1)
	ins := rawVal
	if j < len(child) && child[j] != ']' {
		ins = append([]byte(","), rawVal...)
	}
	off := s + sp[0] + cs + close
	out := make([]byte, 0, len(body)+len(ins))
	out = append(out, body[:off]...)
	out = append(out, ins...)
	out = append(out, body[off:]...)
	return out, true, nil
}

// AppendArrayItemArrayAt 与 AppendLastArrayItemArray 同形，但按下标挑元素，
// 而不是"从后往前找第一个 match 为真的"。
//
// 存在的理由（2026-09-17 现场，2026-09-18 复核）：deepseek 的尾部形状修复要锚
// 在**最后一条 role:user 的消息**上，而下标可能不是数组最后一项——Claude Code
// 会在 tool_result 之后追加一条 role:"system" 的插话，数组最后一项是那条
// system。写成"从后往前找"在长历史里会去改一条中段的 tool_result-only 老消息
// （那种轮次到处都是），把一句用户没说过的话塞进对话中段。
func AppendArrayItemArrayAt(body []byte, arrayKey, field string, itemIndex int,
	rawVal []byte) ([]byte, bool, error) {
	s, e, err := findTopLevelValue(body, arrayKey)
	if err != nil {
		return body, false, err
	}
	arr := body[s:e]
	spans, ok := arrayItemSpans(arr)
	if !ok {
		return body, false, errNotArray
	}
	if itemIndex < 0 || itemIndex >= len(spans) {
		return body, false, nil
	}
	sp := spans[itemIndex]
	item := arr[sp[0]:sp[1]]
	if len(item) == 0 || item[0] != '{' {
		return body, false, nil
	}
	cs, ce, err := findTopLevelValue(item, field)
	if err != nil {
		return body, false, err
	}
	child := item[cs:ce]
	if len(child) == 0 || child[0] != '[' {
		return body, false, errNotArray
	}
	close := len(child) - 1
	for close > 0 {
		switch child[close] {
		case ' ', '\t', '\r', '\n':
			close--
			continue
		}
		break
	}
	if child[close] != ']' {
		return body, false, errNotArray
	}
	j := skipWS(child, 1)
	ins := rawVal
	if j < len(child) && child[j] != ']' {
		ins = append([]byte(","), rawVal...)
	}
	off := s + sp[0] + cs + close
	out := make([]byte, 0, len(body)+len(ins))
	out = append(out, body[:off]...)
	out = append(out, ins...)
	out = append(out, body[off:]...)
	return out, true, nil
}

// AppendTopLevelArrayItem 往**顶层那个数组**追加一个元素，其余字节原样保留。
//
// 与 AppendLastArrayItemArray / AppendArrayItemArrayAt 的区别是「插进哪个数组」：
// 那两个插进的是**某个元素内部的**数组字段（messages[i].content），这一个插进的是
// 顶层数组本身。Responses 方言（Codex）的尾部形状修复要补的正好是后者——一条新的
// 顶层 input[] 项，而不是某个项 content 里的一块。
//
// 空数组不补逗号（`[` 紧跟 `]` 时补出来的是 `[,x]`，不是合法 JSON）；非空数组插在
// 收尾 `]` 之前，跳过 `]` 前的空白，所以原来那点缩进与换行逐字还在。
func AppendTopLevelArrayItem(body []byte, arrayKey string, rawVal []byte) ([]byte, bool, error) {
	s, e, err := findTopLevelValue(body, arrayKey)
	if err != nil {
		return body, false, err
	}
	arr := body[s:e]
	spans, ok := arrayItemSpans(arr)
	if !ok {
		return body, false, errNotArray
	}
	close := len(arr) - 1
	for close > 0 {
		switch arr[close] {
		case ' ', '\t', '\r', '\n':
			close--
			continue
		}
		break
	}
	if close <= 0 || arr[close] != ']' {
		return body, false, errNotArray
	}
	off := s + close
	ins := rawVal
	if len(spans) > 0 {
		ins = append([]byte(","), rawVal...)
	}
	out := make([]byte, 0, len(body)+len(ins))
	out = append(out, body[:off]...)
	out = append(out, ins...)
	out = append(out, body[off:]...)
	return out, true, nil
}

// ArrayItems 把一个 JSON 数组的原始字节切成各元素的原始字节（只读判断用，
// 比如「这条 content[] 里有没有 thinking 块」）。切片指向原字节，不拷贝。
func ArrayItems(arr []byte) ([][]byte, bool) {
	spans, ok := arrayItemSpans(arr)
	if !ok {
		return nil, false
	}
	out := make([][]byte, 0, len(spans))
	for _, sp := range spans {
		out = append(out, arr[sp[0]:sp[1]])
	}
	return out, true
}

// arrayItemSpans 返回数组里每个元素相对 arr 的 [start,end)。
// 扫不动（不是数组、或值残缺）时返回 false——调用方一律按「形状不认识，
// 不动它」处理，绝不去猜。
func arrayItemSpans(arr []byte) ([][2]int, bool) {
	if len(arr) == 0 || arr[0] != '[' {
		return nil, false
	}
	var out [][2]int
	i := 1
	for {
		i = skipWS(arr, i)
		if i >= len(arr) {
			return nil, false
		}
		if arr[i] == ']' {
			return out, true
		}
		if arr[i] == ',' {
			i++
			continue
		}
		ve, ok := scanValue(arr, i)
		if !ok {
			return nil, false
		}
		out = append(out, [2]int{i, ve})
		i = ve
	}
}

var (
	errNotObject = i18n.E("request body top level is not a JSON object", nil)
	errNoKey     = i18n.E("no such key at the top level", nil)
	errNotString = i18n.E("value of the key is not a string", nil)
	errNotArray  = i18n.E("value of the key is not an array", nil)
	errDupKey    = i18n.E("the top level already has this key", nil)
)

// findTopLevelStringValue 同 findTopLevelValue，但要求值是字符串。
func findTopLevelStringValue(body []byte, key string) (int, int, error) {
	s, e, err := findTopLevelValue(body, key)
	if err != nil {
		return 0, 0, err
	}
	if body[s] != '"' {
		return 0, 0, errNotString
	}
	return s, e, nil
}

// findTopLevelValue 返回顶层 key 的值在 body 中的字节区间 [start,end)。
// 只看深度 1 的键，所以嵌套在 tools 里的同名 "model" 不会被误伤。
func findTopLevelValue(body []byte, key string) (int, int, error) {
	i := skipWS(body, 0)
	if i >= len(body) || body[i] != '{' {
		return 0, 0, errNotObject
	}
	i++ // 进入对象，深度 = 1

	for {
		i = skipWS(body, i)
		if i >= len(body) {
			return 0, 0, errNoKey
		}
		if body[i] == '}' {
			return 0, 0, errNoKey
		}
		if body[i] == ',' {
			i++
			continue
		}
		if body[i] != '"' {
			return 0, 0, errNotObject
		}

		// 读 key
		kStart := i
		kEnd, ok := scanString(body, i)
		if !ok {
			return 0, 0, errNotObject
		}
		var k string
		if err := json.Unmarshal(body[kStart:kEnd], &k); err != nil {
			return 0, 0, errNotObject
		}
		i = skipWS(body, kEnd)
		if i >= len(body) || body[i] != ':' {
			return 0, 0, errNotObject
		}
		i = skipWS(body, i+1)
		if i >= len(body) {
			return 0, 0, errNotObject
		}

		vStart := i
		vEnd, ok := scanValue(body, i)
		if !ok {
			return 0, 0, errNotObject
		}
		if k == key {
			return vStart, vEnd, nil
		}
		i = vEnd
	}
}

func skipWS(b []byte, i int) int {
	for i < len(b) {
		switch b[i] {
		case ' ', '\t', '\n', '\r':
			i++
		default:
			return i
		}
	}
	return i
}

// scanString 从 b[i]=='"' 开始，返回闭合引号之后的位置。
func scanString(b []byte, i int) (int, bool) {
	if i >= len(b) || b[i] != '"' {
		return 0, false
	}
	i++
	for i < len(b) {
		switch b[i] {
		case '\\':
			i += 2 // 跳过转义序列（\uXXXX 的后续字符都不是 " 或 \，安全）
		case '"':
			return i + 1, true
		default:
			i++
		}
	}
	return 0, false
}

// scanValue 返回 b[i] 开始的这个 JSON 值之后的位置。
// 只需要正确划界，不需要校验合法性——校验是上游的事，我们只管别改坏。
func scanValue(b []byte, i int) (int, bool) {
	if i >= len(b) {
		return 0, false
	}
	switch b[i] {
	case '"':
		return scanString(b, i)
	case '{', '[':
		open, close := b[i], byte('}')
		if open == '[' {
			close = ']'
		}
		depth := 0
		for i < len(b) {
			switch b[i] {
			case '"':
				n, ok := scanString(b, i)
				if !ok {
					return 0, false
				}
				i = n
				continue
			case open:
				depth++
			case close:
				depth--
				if depth == 0 {
					return i + 1, true
				}
			}
			i++
		}
		return 0, false
	default:
		// 数字 / true / false / null：扫到分隔符为止
		for i < len(b) {
			switch b[i] {
			case ',', '}', ']', ' ', '\t', '\n', '\r':
				return i, true
			}
			i++
		}
		return i, true
	}
}

// TopLevelString 只读地取出顶层某个字符串字段。
func TopLevelString(body []byte, key string) (string, bool) {
	s, e, err := findTopLevelStringValue(body, key)
	if err != nil {
		return "", false
	}
	var v string
	if json.Unmarshal(body[s:e], &v) != nil {
		return "", false
	}
	return v, true
}

// TopLevelBool 只读地取出顶层某个布尔字段（stream 用）。
func TopLevelBool(body []byte, key string) bool {
	i := skipWS(body, 0)
	if i >= len(body) || body[i] != '{' {
		return false
	}
	i++
	for {
		i = skipWS(body, i)
		if i >= len(body) || body[i] == '}' {
			return false
		}
		if body[i] == ',' {
			i++
			continue
		}
		if body[i] != '"' {
			return false
		}
		kStart := i
		kEnd, ok := scanString(body, i)
		if !ok {
			return false
		}
		var k string
		if json.Unmarshal(body[kStart:kEnd], &k) != nil {
			return false
		}
		i = skipWS(body, kEnd)
		if i >= len(body) || body[i] != ':' {
			return false
		}
		i = skipWS(body, i+1)
		vStart := i
		vEnd, ok := scanValue(body, i)
		if !ok {
			return false
		}
		if k == key {
			return string(body[vStart:vEnd]) == "true"
		}
		i = vEnd
	}
}
