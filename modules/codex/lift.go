package codex

// codex 客户端的**工具方言**提到上游看得懂的位置。
//
// 这层是「codex 发的形状」与「任意上游读的形状」之间的归一化。它**不**做模型侧
// 的修整（custom → function 之类）——那是 crossings 的事（见 modules/codex_deepseek）。
// 它的全部职责是：把 Codex 0.155 那种把工具树挂在 `input[0].additional_tools`
// 里的旧版形状**逐字节**拍平、写到顶层 `tools`，让上游看见工具。
//
// 实测（2026-09-23，打真实 smt-ark / smt-deepseek / smt-claude / smt-gemini /
// smt-codex / kimi / minimax，每格 3/3）：
//
//	顶层 tools 已存在（非空数组）  → 上游拿到 function_call
//	input[0].additional_tools         → 上游或 400（gemini/gpt/minimax），
//	                                   或 200 但零 tool_call（ark/deepseek/kimi/claude）
//
// ark/claude/kimi 200 但不出 tool_call 是最坑的那一族——它**不是错**，客户端只能
// 拿到一段正常文本，于是「从来没成功获取 tool_result」。这一手要修的就是这个。
// gemini/gpt/minimax 直接 400 的那条更直观，但本质原因一致：上游**根本**没去读
// input[0] 这个位置，只读顶层 `tools`。这条规矩的来源是上游的文档与行为实测，
// 不是 Codex 的预期——但既然上游就这样，本层就照做。

import (
	"bytes"

	i18n "github.com/rzbdz/newgate/lib/i18n"
	rw "github.com/rzbdz/newgate/modules/gateway/rewrite"
)

// 本包内 takeover.go 把 `rewrite` 用作了**局部函数名**（负责 TOML 字节手术的那
// 一手，见那里）。Go 的包级名字空间里不能同时出现「rewrite 包」与「rewrite 函
// 数」，所以这里用 `rw` 别名——功能上是同一份原语，名字上绕开冲突。

// ToolsItem 是 Codex 把工具树放进 input[] 时那一项的 `type` 值。
//
// 它是**客户端方言**——属于 codex，**不**属于任何上游，也不是 Responses 方言的一部分。
// 所以它住 modules/codex 的常量里，内核里一个字都不该出现（转发热路径连
// "responses" 都不认识）。
const ToolsItem = "additional_tools"

// ToolsInInput 从请求体里取出 Codex 的工具树（嵌套形状）。
//
// 「找不到」不是错误——Codex 0.155 也用顶层 `tools` 的新版形状（见
// HasUsableTopLevelTools），那种就不是这一手管的对象。返回 nil, false 表示
// 「归别人管」。
func ToolsInInput(body []byte) ([]byte, bool) {
	input, ok := rw.TopLevelRaw(body, "input")
	if !ok {
		return nil, false
	}
	items, ok := rw.ArrayItems(input)
	if !ok {
		return nil, false
	}
	for _, item := range items {
		if kind, _ := rw.TopLevelString(item, "type"); kind == ToolsItem {
			arr, ok := rw.TopLevelRaw(item, "tools")
			return arr, ok
		}
	}
	return nil, false
}

// HasUsableTopLevelTools 报告顶层 `tools` 是不是已经是一个**非空**数组。
//
// 真值表（实测）：
//
//	`"tools": [...]`（非空） → 用上游那份（不去动）
//	`"tools": []`           → 当作缺席，抬上去
//	`"tools": null`         → 同上
//	缺席                    → 同上
//
// 为什么空数组/null 视为「缺席」：实测 Codex 0.155 在带 `additional_tools` 项的请求
// 里把顶层 `tools` 写成 `null` 或 `[]`（这是它的旧版形状），那种情况应当按"我要用
// 嵌套那份"处理；新版形状里 `tools` 一定是**非空**数组，这是判断的界线。
func HasUsableTopLevelTools(body []byte) bool {
	raw, has := rw.TopLevelRaw(body, "tools")
	if !has || len(raw) == 0 {
		return false
	}
	trimmed := bytes.TrimSpace(raw)
	if bytes.Equal(trimmed, []byte("null")) {
		return false
	}
	items, ok := rw.ArrayItems(trimmed)
	return ok && len(items) > 0
}

// FlattenToolTree 把嵌套的工具树（`additional_tools` 数组里可能出现的
// `namespace` 容器）拍平成单个顶层 `tools` 数组。
//
// `namespace` 在 Codex 那边是给客户端分组看的（functions / collaboration），上游
// 没有对应概念（实测：smt-deepseek / smt-ark 直接读 `tools` 数组，对 namespace
// 既不报错也不展开——「看不见」等价于「嵌套形状失败」）。拍平之后每个叶子
// 字节不动：未知 `type` 的项也原样保留（让 Codex 新加的工具类型发出去，
// 而不让我们猜）。
//
// 跨仓库的旧版判断（kernel 注释里那一条）：Codex 的 `custom` 类工具在上游是否
// 被接受，**按上游**——不是这一层管的事。这一层只做"抬上来"，不做"降级"。
//
// 返回值是 (新数组, 叶子数)。叶子数用于：(1) 决定"有没有动过"——0 = 原样转发，
// 与 reasoning 那条规矩同；(2) 报 notes 时给读者真话。
func FlattenToolTree(arr []byte) ([]byte, int) {
	items, ok := rw.ArrayItems(arr)
	if !ok {
		return nil, 0
	}
	leaves := collectLeaves(items)
	if len(leaves) == 0 {
		return nil, 0
	}
	var buf bytes.Buffer
	buf.WriteByte('[')
	for i, leaf := range leaves {
		if i > 0 {
			buf.WriteByte(',')
		}
		buf.Write(leaf)
	}
	buf.WriteByte(']')
	return buf.Bytes(), len(leaves)
}

// collectLeaves 递归收叶子。namespace 容器进去，别的 type 原样收。
func collectLeaves(items [][]byte) [][]byte {
	out := make([][]byte, 0, len(items))
	for _, item := range items {
		kind, _ := rw.TopLevelString(item, "type")
		if kind == "namespace" {
			nested, ok := rw.TopLevelRaw(item, "tools")
			if !ok {
				out = append(out, item) // 形状不认识就当叶子
				continue
			}
			nestedItems, ok := rw.ArrayItems(nested)
			if !ok {
				out = append(out, item)
				continue
			}
			out = append(out, collectLeaves(nestedItems)...)
			continue
		}
		out = append(out, item)
	}
	return out
}

// SetTopLevelTools 把 `tools` 字段写成本次抬出来的数组。
//
// 真值表（实测 Codex 0.155.1）：
//
//	缺席            → Insert（这是真正的「我要给它补上」）
//	`"tools":null`  → Replace（已是同一个键，只是值是 null，不是缺席）
//	`"tools":[]`    → Replace
//	`"tools":[...]` → Replace
//
// 之所以「null / [] 走 Replace」而不是「按缺席走 Insert」：InsertTopLevelRaw 在键
// 已存在时报「the top level already has this key」——所以「视为缺席」只能是在
// Apply 那一层做决定，写到这里必须忠实地 Replace。
//
// 错误一律原样上抛：调用方按 fail-open 决定要不要原样转发。
func SetTopLevelTools(body, tools []byte) ([]byte, error) {
	if _, has := rw.TopLevelRaw(body, "tools"); has {
		return rw.ReplaceTopLevelRaw(body, "tools", tools)
	}
	return rw.InsertTopLevelRaw(body, "tools", tools)
}

// noteLifted 报「抬了 N 条工具」。N 是叶子数——`namespace` 容器不计数里。
//
// 「有没有抬」看叶子数而不是「有没有降级过」：哪怕一个 custom 都没有（全是原生
// function），只要它们挂在 input[0] 里，模型就一个工具都看不见。修的是这一步
// 本身；降级是别的层（crossings）的另一件事——把这两件事混在同一句 note 里会
// 让「一个 custom 都没有」时那句变成「抬了 N 条，0 条降级」，多出来的「0 条」不是
// 噪音而是「这次没动过任何工具的字段」这条信息，独立成句更稳。
func noteLifted(n int) string {
	return i18n.N(
		"lifted {n} tool declaration from input[0] to the top level (most upstreams read tools only there)",
		"lifted {n} tool declarations from input[0] to the top level (most upstreams read tools only there)",
		n, i18n.A{"n": n})
}
