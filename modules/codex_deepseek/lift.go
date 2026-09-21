package codex_deepseek

import (
	"bytes"

	"github.com/rzbdz/newgate/modules/gateway/rewrite"
)

// Tools 是 Codex 放工具定义的那条 input 项的类型名。
//
// 它是**客户端的方言**，不是上游的字段名——所以它住在这里，而内核里一个字都不
// 该出现（转发热路径那条链上连 "responses" 都不认识）。
const Tools = "additional_tools"

// ApplyPatch 是上游唯一原生支持的 custom 工具名。
//
// 实测（2026-09-21，打真实 smt-deepseek/deepseek-flash）：把别的 custom 工具原样
// 发过去是 400 `Unsupported custom tool: 'exec'. Only 'apply_patch' is supported.`
// 所以除了它，其余 custom 一律降级成 function。
const ApplyPatch = "apply_patch"

// freeformParams 是给降级出来的 function 补的参数表。
//
// 它必须存在：OpenAI 的 function 工具**要求**有 parameters，而 custom 工具没有
// 这个字段。形状是一个字符串参数——custom 工具的输入本来就是一整段自由文本
// （Codex 的 exec 收的是一段 JavaScript），塞进一个 `input` 字段是语义上最接近的
// 表达，模型也一眼看得懂「把原文放这儿」。
var freeformParams = []byte(`{"type":"object","properties":{"input":{"type":"string"}},"required":["input"]}`)

// liftTools 把 Codex 的工具树抬成 DeepSeek 认的顶层 tools 数组。
//
// 返回 (新数组, 被降级的工具名, 抬出了东西没有)。
//
// **抬本身才是修根因的那一步**，降级只是顺带：哪怕一个 custom 都没有（全是原生
// function），只要它们挂在 input[0] 里，模型就一个工具也看不见。所以「有没有抬出
// 东西」看的是**叶子数**，不是「有没有降级过」。
//
// 抬的是**两层结构**：`namespace`（Codex 用来分组，如 functions / collaboration）
// 在上游没有对应概念，拍平成同一个数组——工具名本来就是全局唯一的，分组只是给
// 人看的。拍平之后每个叶子要么原样（原生 function），要么降级（custom → function）。
//
// 全程字节手术：叶子元素的字节除了被降级的那几个（改一个字段值、插一个字段）
// 之外逐字不动。reasoning 那条规矩在这里同样成立——能不动就不动。
// degradeTopLevel 处理「工具已经躺在顶层 tools 里」的形状。
//
// 两种来源（见 apply 的注释）：Codex 旧版把工具挂在 input[0].additional_tools 里，
// 新版直接放在顶层 tools（含 namespace 分组、web_search）。顶层那份是 DeepSeek
// 已经在读的，**不需要抬**，但仍可能夹着一条 `custom`——而 DeepSeek 只认
// apply_patch 一个 custom，别的一律 400（实测 2026-09-21）。所以对顶层这份只做
// 一件事：把非 apply_patch 的 custom 降级成 function。其余字节一个不动。
//
// 返回：改写后的数组、被降级的工具名、有没有真的动过。没 custom 时 changed 为
// false，调用方据此原样转发——「没有要修的就不动手」，与 reasoning 那条规矩同。
func degradeTopLevel(arr []byte) (out []byte, degraded map[string]bool, changed bool) {
	degraded = map[string]bool{}
	items, ok := rewrite.ArrayItems(arr)
	if !ok {
		return arr, degraded, false
	}
	var buf bytes.Buffer
	buf.WriteByte('[')
	touched := false
	for i, item := range items {
		if i > 0 {
			buf.WriteByte(',')
		}
		kind, _ := rewrite.TopLevelString(item, "type")
		if kind != "custom" {
			buf.Write(item)
			continue
		}
		name, _ := rewrite.TopLevelString(item, "name")
		if name == "" || name == ApplyPatch {
			buf.Write(item)
			continue
		}
		conv, err := toFunction(item)
		if err != nil {
			buf.Write(item)
			continue
		}
		degraded[name] = true
		touched = true
		buf.Write(conv)
	}
	buf.WriteByte(']')
	return buf.Bytes(), degraded, touched
}

func liftTools(arr []byte) (out []byte, degraded map[string]bool, lifted bool) {
	degraded = map[string]bool{}
	leaves := liftInto(nil, arr, degraded)
	if len(leaves) == 0 {
		return nil, degraded, false
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
	return buf.Bytes(), degraded, true
}

// liftInto 递归拍平工具树，把每个叶子收进 dst，顺路记下被降级的工具名。
//
// 单条叶子降级失败（形状不认识）时**保留原样**并继续：一条坏工具不该把整份工具表
// 丢掉——那会让模型连别的工具都看不见，比发一条它可能不认的声明糟得多
// （fail-open，与插件层那条规矩同）。
//
// 认不出的 type 也原样留着（default 那一支）：将来 Codex 加一种新的工具类型时，
// 抬上去的是它写的原文，而不是我们猜的形状。
func liftInto(dst [][]byte, arr []byte, degraded map[string]bool) [][]byte {
	items, ok := rewrite.ArrayItems(arr)
	if !ok {
		return dst
	}
	for _, item := range items {
		kind, _ := rewrite.TopLevelString(item, "type")
		switch kind {
		case "namespace":
			nested, ok := rewrite.TopLevelRaw(item, "tools")
			if !ok {
				dst = append(dst, item)
				continue
			}
			dst = liftInto(dst, nested, degraded)
		case "custom":
			name, _ := rewrite.TopLevelString(item, "name")
			if name == "" || name == ApplyPatch {
				dst = append(dst, item) // 上游原生认这一条，别动它
				continue
			}
			conv, err := toFunction(item)
			if err != nil {
				dst = append(dst, item)
				continue
			}
			degraded[name] = true
			dst = append(dst, conv)
		default:
			dst = append(dst, item)
		}
	}
	return dst
}

// toFunction 把一条 custom 声明改写成 function 声明。
//
// 只动两处：`type` 的值，和补一个 `parameters`。description 原样留着——模型挑
// 工具靠的就是它（Codex 那段 exec 说明长得像文档，是有用的）。
func toFunction(item []byte) ([]byte, error) {
	out, err := rewrite.ReplaceTopLevelString(item, "type", "function")
	if err != nil {
		return nil, err
	}
	if _, has := rewrite.TopLevelRaw(out, "parameters"); has {
		return out, nil
	}
	return rewrite.InsertTopLevelRaw(out, "parameters", freeformParams)
}

// codexTools 取出客户端请求里那条 additional_tools 的工具数组。
//
// 找不到就返回 false——「这份请求不是 Codex 的形状」不是错误，是「不归我管」。
func codexTools(body []byte) ([]byte, bool) {
	input, ok := rewrite.TopLevelRaw(body, "input")
	if !ok {
		return nil, false
	}
	items, ok := rewrite.ArrayItems(input)
	if !ok {
		return nil, false
	}
	for _, item := range items {
		if kind, _ := rewrite.TopLevelString(item, "type"); kind == Tools {
			arr, ok := rewrite.TopLevelRaw(item, "tools")
			return arr, ok
		}
	}
	return nil, false
}

// setTopLevelTools 把顶层 `tools` 写成本次抬出来的数组。
//
// 客户端那份请求里 `tools` 通常是**缺席**或 `null`（实测 0.155.1 两种都见过），
// 所以插入与替换都要处理。这条区别不能用「先插再替换」抹平：InsertTopLevelRaw
// 对已存在的键直接报错，而报错会被上层当成「这个插件的这一手没跑」。
func setTopLevelTools(body, tools []byte) ([]byte, error) {
	if _, has := rewrite.TopLevelRaw(body, "tools"); has {
		return rewrite.ReplaceTopLevelRaw(body, "tools", tools)
	}
	return rewrite.InsertTopLevelRaw(body, "tools", tools)
}
