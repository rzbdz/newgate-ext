package codex_deepseek

import (
	"encoding/json"

	i18n "github.com/rzbdz/newgate/lib/i18n"
	"github.com/rzbdz/newgate/modules/config/domain"
	"github.com/rzbdz/newgate/modules/gateway/rewrite"
	"github.com/rzbdz/newgate/modules/gateway/special"
	"github.com/rzbdz/newgate/modules/pluginmanager"

	codexapi "github.com/rzbdz/newgate-ext/modules/codex"
	deepseekapi "github.com/rzbdz/newgate-ext/modules/deepseek"
)

func Treatments(client codexapi.Client, model deepseekapi.Model) []special.Plugin {
	return []special.Plugin{tools{client: client, model: model}}
}

// tools 是这一手补丁：把 Codex 的工具方言抬成 DeepSeek 认的形状（请求侧），并把
// 被抬过的那些在响应里改回去（响应侧）。
//
// 两个方向由**同一个插件**负责，不拆成两个。理由不是省事：响应侧要改哪几条，
// 判据是「请求侧降级了哪几个工具」，拆开就要让那个结论跨插件传递——而插件之间
// 只有 Request 这一条通道，把「谁被降级了」塞进它等于往公共契约里加一个只有两家
// 用得上的字段。
type tools struct {
	client codexapi.Client
	model  deepseekapi.Model
}

func (tools) Name() string { return "codex-deepseek" }

// Before 抢在别的插件前面：它读的是**客户端发来的原文**（input[0]），谁要是先
// 动了 input，它读到就不是原文了。
func (tools) Before() []string { return []string{"deepseek"} }
func (tools) After() []string  { return nil }

func (tools) Why() string {
	return i18n.T("Codex puts its tool definitions in input[0] (additional_tools) "+
		"while DeepSeek reads tools from the top level; without the lift the model "+
		"sees no tools at all and answers in its own text format, so the client "+
		"never gets a tool_result", nil)
}

func (t tools) Match(r *special.Request) bool {
	return r != nil && r.Agent == t.client.AgentID &&
		t.model.MatchTarget != nil &&
		t.model.MatchTarget(r.Model, r.Provider, r.BaseURL)
}

// Apply 改写请求：把工具提上来，或者只对顶层做降级。
//
// 两种路线（互相排斥）：
//
//  1. 读出 input[0].additional_tools（旧版格式）：把这棵树拍平、custom 降成
//     function、写进顶层 tools（覆盖原有的），原来的 input[0] 原样保留。
//     DeepSeek 会从这棵新造的树里读工具。
//  2. 没有 additional_tools，但顶层有 tools（新版格式）：里面可能是
//     namespace/function/web_search/custom 混着。DeepSeek 对前三种都能消化
//     （namespace 回显时原样带回，见 2026-09-21 实测），但对未知的 custom 报 400。
//     所以顺着扫一遍，只把 custom（除 apply_patch 外）降成 function，其余一个
//     字节不动。
//
// **路线 1 那一半 2026-09-23 搬去了 `modules/codex`**（`codex.lift-tools`）：把工具
// 从 input[0] 抬到顶层与上游是谁无关（Codex 配 Ark / Claude / Kimi 同样需要），
// 判据就不该住在一个叫 codex-deepseek 的模块里。搬走之后本模块**仍然读那个开关**
// （见 liftEnabled），否则「关掉 codex.lift-tools」在 DeepSeek 这条路上会变成空操作
// ——开关报了名却没人读，那是比没有开关更糟的一种谎（2026-09-21 在 live 上实测过
// 同一族的事）。
//
// 留着路线 1 这一支的理由是**它现在只在对的位置才跑**：判据是「顶层还没有可用的
// tools」（`HasUsableTopLevelTools`），而 codex-tools 排在本插件前面——所以正常的
// 装配里走的一律是路线 2（抬已经在前面做完了，这里只降级）。它剩下的用武之地是
// 客户端那一手没挂上时（本模块的单测、以及任何没有 codex 客户端 hook 的装配），
// 那时这一支仍能把请求修对，而不是把 custom 直接发给一个会 400 的上游。
//
// 这一手可以被单独关掉（`newgate plugin codex-deepseek.degrade-tools off`）——
// **开关点必须真的被读**，否则 `newgate plugin` 会报「已关闭」而请求照改不误
// （2026-09-21 实测到：关掉之后 live 上顶层 tools 照样被抬上来）。它的默认值是
// 开，所以关掉是排查动作，不是常态。
func (tools) Apply(body []byte, r *special.Request) ([]byte, []string, error) {
	if pluginmanager.Off(stateOf(r), SwitchDegradeTools) {
		return body, nil, nil
	}
	if liftEnabled(r) && !codexapi.HasUsableTopLevelTools(body) {
		if arr, ok := codexTools(body); ok {
			lifted, degraded, n := liftTools(arr)
			if n == 0 {
				return body, nil, nil
			}
			out, err := setTopLevelTools(body, lifted)
			if err != nil {
				return body, nil, err
			}
			// note 分两条报，因为它们是**两件事**：抬了几条（修根因的那一步），以及
			// 其中几条被降级了。合成一句会让「一个 custom 都没有」时那句变成
			// 「抬了 N 条，0 条被降级」——而 0 条降级不是噪音，是「这次没动过任何
			// 工具的字段」这条信息。见 lift.go 里那段「抬本身才是修根因」的说明。
			//
			// 抬的这一句与 modules/codex 的 codex-tools 说的是同一件事、同一句
			// 措辞——两条路走到这里对读者是一件事（工具被抬上去了），分两个措辞
			// 只会让排查的人先猜是哪一条路动的。正常情况下这里一次都不会命中：
			// codex-tools 排在本插件前面（见 ToolsLift.Before）。
			notes := []string{i18n.N(
				"lifted {n} tool declaration from input[0] to the top level (DeepSeek reads tools only there)",
				"lifted {n} tool declarations from input[0] to the top level (DeepSeek reads tools only there)",
				n, i18n.A{"n": n})}
			if len(degraded) > 0 {
				notes = append(notes, degradeNote(len(degraded)))
			}
			return out, notes, nil
		}
	}

	if toolsRaw, ok := rewrite.TopLevelRaw(body, "tools"); ok {
		degradedArr, degraded, changed := degradeTopLevel(toolsRaw)
		if !changed {
			return body, nil, nil
		}
		out, err := rewrite.ReplaceTopLevelRaw(body, "tools", degradedArr)
		if err != nil {
			return body, nil, err
		}
		return out, []string{degradeNote(len(degraded))}, nil
	}

	return body, nil, nil
}

// degradeNote 是「把 custom 降成了 function」那句话。
//
// **两条来源共用一句**（input[0] 抬上来的、顶层本来就有的）：对读者来说那是同
// 一件事——同一个上游拒了同一族的声明——而两句话会让日志里同一个原因有两个
// 措辞，排查时得先猜是哪一条路动的。
func degradeNote(n int) string {
	return i18n.N(
		"degraded {n} custom tool to a standard function (DeepSeek rejects any custom tool other than apply_patch with 400)",
		"degraded {n} custom tools to standard functions (DeepSeek rejects any custom tool other than apply_patch with 400)",
		n, i18n.A{"n": n})
}

// stateOf 从插件上下文里取配置快照。允许 nil（旧调用面与单测常见）——
// pluginmanager.Off 对 nil 一律回 false，也就是「没关」。方向不能反：
// 快照缺席不该把补丁关掉。与 claudecode_deepseek 那份同款。
func stateOf(r *special.Request) *domain.State {
	if r == nil {
		return nil
	}
	return r.State
}

// Egress 认领这一发的响应改写。
//
// 判据从**客户端发来的原文**里读（`original`），因为 Apply 之后那几个 custom 已经
// 变成 function 了，从那份里再也分不出「谁原本是 custom」。见 lift.go 的说明。
//
// 开关点与 Apply **同一把尺子**：关掉之后请求侧不改，响应侧也就没有该改的东西
// ——两边只要有一边读了开关、另一边没读，就会出现「请求没降级、响应却被改成
// custom_tool_call」这种把客户端弄坏的不对称。
func (tools) Egress(r *special.Request, original []byte) special.Egress {
	if pluginmanager.Off(stateOf(r), SwitchDegradeTools) {
		return nil
	}
	degraded := degradedNames(r, original)
	if len(degraded) == 0 {
		return nil
	}
	return &egressTools{degraded: degraded}
}

// liftEnabled 读**客户端那一手**的开关（`codex.lift-tools`，住在 modules/codex）。
//
// 本模块只是它的读者，不是它的拥有者：抬工具是客户端方言的事，与上游无关
// （见 switches.go 里那段为什么）。读它的原因有两个，都是「两条尺子必须一致」：
//
//   - Apply：抬的那一手关着时，本模块的兜底那一支**不能**自己抬。否则「关掉
//     codex.lift-tools」在 DeepSeek 这条路上会变成空操作——开关报了名却没人读，
//     那是比没有开关更糟的一种谎（2026-09-21 在 live 上实测过同一族的事）。
//   - degradedNames：抬都不抬，就没有「被降级过的名字」可言，响应侧也就不该去
//     认领。两条路线上任何一个不对称都会把客户端弄坏。
//
// 快照缺席 = 没关（pluginmanager.Off 对 nil 回 false），方向不能反。
func liftEnabled(r *special.Request) bool {
	return !pluginmanager.Off(stateOf(r), codexapi.SwitchLiftTools)
}

// degradedNames 从客户端原文里读出「会被降级」的工具名。
//
// 判据与 Apply 逐字一致（`type: "custom"` 且不是 apply_patch）——两处必须
// 用同一条尺子，否则会出现「请求侧没改、响应侧却去改」这种对不上的情况。
// 所以对两种来源（input[0].additional_tools 与顶层 tools）各走一遍对应的
// 降级扫描，不另写一份判断；而 input[0] 那一支**跟着客户端的抬开关走**，
// 理由见 liftEnabled。
func degradedNames(r *special.Request, original []byte) map[string]bool {
	degraded := map[string]bool{}
	if liftEnabled(r) {
		if arr, ok := codexTools(original); ok {
			_, d, _ := liftTools(arr)
			for k := range d {
				degraded[k] = true
			}
		}
	}
	if toolsRaw, ok := rewrite.TopLevelRaw(original, "tools"); ok {
		_, d, _ := degradeTopLevel(toolsRaw)
		for k := range d {
			degraded[k] = true
		}
	}
	if len(degraded) == 0 {
		return nil
	}
	return degraded
}

// egressTools 是一发的响应改写者。
//
// 它带着**这一发的状态**（哪几个工具被降级了、正在流式吐出的那几条调用攒到了
// 哪儿），所以每次认领都给一个新实例：多个请求同时在线上跑，状态不能跨请求串。
// 不带锁的另一个理由是它的生命周期就是这一发——从认领到流结束。
type egressTools struct {
	degraded map[string]bool

	// calls 是这一发里**正在吐**的降级调用，按 output_index 索引。
	// 请求侧把那几个工具声明成了 function，响应侧的 arguments 是一段一段
	// delta 吐出来的，得攒齐了才知道 input 是什么。
	calls map[int]*degradedCall
}

// degradedCall 是一条降级调用的现场。
//
// 为什么要攒：上游把参数分成几十个 delta 事件吐（实测一条 exec 调用 47 个），
// 而**只有 done 那一条带完整 arguments**。可 codex 认的是 custom 工具，它等的
// 是 output_item.done 上那条完整 item——所以要么把 delta 也改对（并且自己拼出
// 完整 input），要么在 output_item.done 那一刻去别处找。前者是唯一能同时满足
// 两条流的做法：攒下来的 args 就是 output_item.done 要填的 input。
type degradedCall struct {
	// item 是 added 那一刻我们改写过的 item（type 已经是 custom_tool_call）。
	item map[string]any

	// args 是累计的 arguments 原文（delta 拼起来的那份 JSON）。
	args string
}

// Egress 改写**一条 SSE 事件**。改不动或不该改一律返回 nil（调用方原样转发）。
func (e *egressTools) Egress(event []byte) []byte {
	payload, start, end, ok := splitDataLine(event)
	if !ok {
		return nil
	}
	var ev map[string]any
	if json.Unmarshal(payload, &ev) != nil {
		return nil // 不是 JSON（注释行、[DONE]）：不归我管
	}
	idx := indexOf(ev["output_index"])
	switch ev["type"] {
	case "response.output_item.added":
		item, _ := ev["item"].(map[string]any)
		if item == nil || item["type"] != "function_call" {
			return nil
		}
		name, _ := item["name"].(string)
		if !e.degraded[name] {
			return nil
		}
		// 客户端认定它是 custom：字段名跟着换（input 而不是 arguments），
		// 值先给空串，等 done 那一刻填攒好的原文。
		delete(item, "arguments")
		item["type"] = "custom_tool_call"
		item["input"] = ""
		e.remember(idx, item)
		return spliceEvent(event, start, end, ev)

	case "response.function_call_arguments.delta":
		call, ok := e.calls[idx]
		if !ok {
			return nil
		}
		call.args += fmtString(ev["delta"])
		ev["type"] = "response.custom_tool_call_input.delta"
		return spliceEvent(event, start, end, ev)

	case "response.function_call_arguments.done":
		call, ok := e.calls[idx]
		if !ok {
			return nil
		}
		input := call.finalize(fmtString(ev["arguments"]))
		ev["type"] = "response.custom_tool_call_input.done"
		ev["input"] = input
		delete(ev, "arguments")
		return spliceEvent(event, start, end, ev)

	case "response.output_item.done":
		call, ok := e.take(idx)
		if !ok {
			return nil
		}
		// 这一条事件自己就带着完整的 arguments（实测上游两种都发：delta 拼起来
		// 的和 done 上那份是一样的），所以优先用它，攒下来的那份兜底。
		item, _ := ev["item"].(map[string]any)
		fromItem := ""
		if item != nil {
			fromItem = fmtString(item["arguments"])
		}
		call.item["input"] = call.finalize(fromItem)
		ev["item"] = call.item
		return spliceEvent(event, start, end, ev)

	case "response.completed":
		resp, _ := ev["response"].(map[string]any)
		if resp == nil {
			return nil
		}
		out, _ := resp["output"].([]any)
		touched := false
		for _, raw := range out {
			item, _ := raw.(map[string]any)
			if item == nil || item["type"] != "function_call" {
				continue
			}
			name, _ := item["name"].(string)
			if !e.degraded[name] {
				continue
			}
			item["type"] = "custom_tool_call"
			item["input"] = unwrapInput(fmtString(item["arguments"]))
			delete(item, "arguments")
			touched = true
		}
		if !touched {
			return nil
		}
		return spliceEvent(event, start, end, ev)
	}
	return nil
}

// finalize 定下这条改写的 input：优先用事件自己带的那份（它一定是完整的），
// 没有才退回攒下来的 delta。
//
// 两份在正常流里是同一段字节（实测一条 exec 调用 47 个 delta，拼起来与 done 上
// 那份逐字相同），所以这不是「选一个更好的」——是「少发 delta 的上游也能对」。
func (c *degradedCall) finalize(fromEvent string) string {
	raw := fromEvent
	if raw == "" {
		raw = c.args
	}
	return unwrapInput(raw)
}

func (e *egressTools) remember(idx int, item map[string]any) {
	if e.calls == nil {
		e.calls = map[int]*degradedCall{}
	}
	e.calls[idx] = &degradedCall{item: item}
}

func (e *egressTools) take(idx int) (*degradedCall, bool) {
	call, ok := e.calls[idx]
	if ok {
		delete(e.calls, idx)
	}
	return call, ok
}

// unwrapInput 拆掉 function 参数外面那层壳，取出 custom 工具真正要的那串原文。
//
// 请求侧把 `{"cmd": "…"}` 整段塞进了 function 的 `input` 参数，响应侧模型自然也
// 按那个形状回 `{"input": "<那段 JS 原文>"}`。Codex 要的是**里面那串原文**——
// 不拆的话它拿整个 JSON 去 eval，报 `SyntaxError: Unexpected token ':'`，然后
// 一轮一轮重试同一个坏调用（实测 18 轮还没停，`codex exec` 永远不退出）。
//
// 拆不出来时原样返回：宁可让客户端看见一段它认不出的原文（它会自己报错，那是一条
// 能排查的错），也不能在这里编一段它没写过的代码。
func unwrapInput(s string) string {
	var args map[string]any
	if json.Unmarshal([]byte(s), &args) != nil {
		return s
	}
	inner, ok := args["input"].(string)
	if !ok {
		return s
	}
	return inner
}

func fmtString(v any) string {
	s, _ := v.(string)
	return s
}

func indexOf(v any) int {
	f, ok := v.(float64)
	if !ok {
		return -1
	}
	return int(f)
}

// splitDataLine 把一条 SSE 事件里的 data 行切成 (JSON 载荷, 载荷起点, 终点)。
//
// 事件里只有一行 data（上游就是这么发的），所以找第一行 `data: ` 即可。返回的
// 两个下标是**载荷在整条事件字节里的位置**，改写时只替换这一段——`event:` 行、
// 前后换行、结尾空行都逐字不动。
func splitDataLine(event []byte) ([]byte, int, int, bool) {
	at := 0
	for at < len(event) {
		lineEnd := indexByte(event, at, '\n')
		if lineEnd < 0 {
			lineEnd = len(event)
		}
		line := event[at:lineEnd]
		trimmed := trimCR(line)
		if len(trimmed) >= 5 && string(trimmed[:5]) == "data:" {
			start := at + (len(line) - len(trimmed)) + 5
			start = skipSpaces(event, start)
			return event[start:lineEnd], start, lineEnd, true
		}
		if lineEnd >= len(event) {
			break
		}
		at = lineEnd + 1
	}
	return nil, 0, 0, false
}

// spliceEvent 把改写后的载荷拼回整条事件。
//
// 载荷长度会变（多了 `"input"`、少了 `"arguments"`），但**这是响应**：事件之间没有
// 长度字段互相关联，SSE 的边界是空行。所以只换这一段就够，其余字节原样。
func spliceEvent(event []byte, start, end int, payload map[string]any) []byte {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil
	}
	out := make([]byte, 0, len(event)-(end-start)+len(body))
	out = append(out, event[:start]...)
	out = append(out, body...)
	out = append(out, event[end:]...)
	return out
}

func indexByte(b []byte, from int, c byte) int {
	for i := from; i < len(b); i++ {
		if b[i] == c {
			return i
		}
	}
	return -1
}

func trimCR(line []byte) []byte {
	if n := len(line); n > 0 && line[n-1] == '\r' {
		return line[:n-1]
	}
	return line
}

func skipSpaces(b []byte, i int) int {
	for i < len(b) && (b[i] == ' ' || b[i] == '\t') {
		i++
	}
	return i
}
