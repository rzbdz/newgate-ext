package deepseek

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/rzbdz/newgate/lib/i18n"
	"github.com/rzbdz/newgate/modules/gateway/rewrite"
	"github.com/rzbdz/newgate/modules/gateway/special"
	"github.com/rzbdz/newgate/modules/gateway/thinkcache"
)

var (
	_ special.Plugin           = (*reasoning)(nil)
	_ special.ToolLoopMigrator = (*reasoning)(nil)
	_ special.MetricProvider   = (*reasoning)(nil)
	_ special.ResponseAuditor  = (*reasoning)(nil)
)

func Treatments() []special.Plugin { return []special.Plugin{reasoning{}} }

// deepseek 修 DeepSeek（含各类转发 DeepSeek 的网关）在思考模式下的 400。
//
// 现场报错，两种方言各一句：
//
//	API Error: 400 The `reasoning_content` in the thinking mode must be
//	passed back to the API.                          （OpenAI 方言端点）
//	API Error: 400 The `content[].thinking` in the thinking mode must be
//	passed back to the API.                          （Anthropic 方言端点）
//
// 成因是同一件事：DeepSeek 的思考模型把思考内容当**对话状态的一部分**。
// 它上一轮回给客户端的 assistant 消息里带了思考内容，下一轮就要求你原样
// 回传；一旦某轮出现过 tool call，之后每一轮都查。而 Claude Code 这类
// 客户端对非 anthropic.com 的端点会**主动把 thinking 块剥掉**（它假定只有
// 官方端点签名的 thinking 块能回传），于是从第二轮起必然缺失，每次都 400。
// 上游不放过，客户端不可能知道，只能中间层补。
//
// 四手一起上，对应上游的四种口味：
//
//  1. **只有 Claude Code** 没写 thinking 时 → 补顶层
//     thinking:{"type":"disabled"}，从根上不进思考模式。这是 Anthropic 协议
//     里的合法字段，disabled 也是默认值，对不认识它的上游是语义无操作；
//     而 DeepSeek 是**必须显式写**才算关，省略不算。
//  2. messages 里的 assistant 消息补 reasoning_content（OpenAI 方言）——
//     **只在缓存/客户端真有原文时补**，没有就跳过，绝不编。
//  3. 思考模式确实开着、且这次是 Anthropic 方言时，给 assistant 消息的
//     content[] **开头**补 thinking 块（Anthropic 方言）。位置是协议的一部分
//     ——thinking 必须排在 text / tool_use 之前。同样只在有原文时补。
//  4. **尾部形状**：最后一条 role:user 消息的 content[] 里全是 tool_result 块、
//     一个字都没有时，给它追加一条最简指令（见 toolLoopRebasePrompt）。
//     这是唯一修**根因**的一手，前三手都只是把字段补齐。
//
// 第 3 手只在思考模式开着时做：思考关掉时再塞 thinking 块，反而会被上游
// 以「关了还给我思考块」拒掉。第 4 手**不能**挂在 thinkingOn 上（实测：
// thinking:disabled 且不带 tools，这条校验照样触发）。
//
// ---- 第 4 手曾被删掉一次，2026-09-18 当天恢复（值得记下来）----
//
// 起因是它的**措辞**：原来注入的是一句英文长句（"Continue from the tool
// results above. …"），那句话会进上游、也会进用户下一轮的对话历史，读起来像
// 「有人在替我说话」。用户报的是这个（原话：「就算以后要用，也只用最少字」）。
//
// 但当天一并把它**整手删掉**是错的，而且错得很隐蔽：删掉之后不再有那条
// note，日志里也看不到 400——因为上游 400 之后**自动沿链转移**，客户端拿到
// 的是下一个 provider 的 200。「看起来没坏」实际是「每一发都悄悄降级到别的
// 模型」。恢复当天实测（打真实 smt-deepseek/deepseek-flash）：
//
//	裸 [tool_result] 尾部             → 400
//	裸 [tool_result] + 尾随 system    → 400
//	裸 [tool_result] + text" "        → 200
//
// 复现路径（写成命令，下次别再靠猜）：把 dump 里任意一份 Claude Code 的
// client-sent 原样 POST 到 /p/ds/v1/messages，日志里就会出现
// `-> 400 ... must be passed back` 紧跟 `→ 沿链下一步`。
//
// 结论：判据（形状）是对的，措辞是错的。**只改措辞，别删判据。**
// 详见 docs/06-reasoning.md §2b。
//
// 第 1 手只给 Claude Code（`/a/claude/` 认出来，见 claudeCode）：它剥掉思考块，
// 思考开着也回不来，白花思考的时间和 token。**别的客户端不能关**——2026-09-15
// 现场：opencode 走 OpenAI 方言（`/chat/completions`），压根不会写 thinking 这个
// Anthropic 字段，于是条条请求都被当成「客户端没要思考」而关掉，用户报
// 「deepseek 不思考了」。opencode 要的是模型的默认思考，推理由第 2 手替它送回去。
//
// 第 3 手只对 Anthropic 方言做：OpenAI 方言里回传推理的载体是 reasoning_content
// （第 2 手），往人家的 content[] 里塞 thinking 块是塞一个它不认识的块类型。
//
// 补什么内容，两级来源，逐级降级：
//
//  1. 客户端自己带回的 thinking 块原文——它保留了就直接用，不依赖缓存；
//  2. gateway/thinkcache 里那一轮**真实**的推理内容；
//  3. **都没有 → 一个字节都不补**（这条是 2026-09-18 按实测改的）。
//
// 第 3 条原来补的是一个非空占位符（「这一轮的推理内容没有被保留…」，后来
// 缩成 "No thinking in this round"）。**那整件事是错的**，2026-09-18 实测推翻：
//
// 用户的判断是对的，而且是最简单的那条逻辑：上游自己都没给我们推理，那
// 「must be passed back」要求回传的东西**根本不存在**，我们凭什么替它编一个？
// 编出来的东西会进上游、进对话历史、每轮烧 token，而它一个字的信息量都没有。
//
// 实测（打真实上游 smt-deepseek/deepseek-flash，每格 3/3）——把 reasoning_content
// 的形态和**尾部形状**两个因素交叉：
//
//	reasoning_content   尾部                 结果
//	真实原文            [tool_result]        400 400 400
//	真实原文            [tool_result, text]  200 200 200
//	省略字段            [tool_result]        400 400 400
//	省略字段            [tool_result, text]  200 200 200
//
// 也就是说：**reasoning_content 在不在、是什么内容，对这个 400 毫无影响**，
// 唯一起作用的是尾部形状（见 docs/06-reasoning.md §2b）。再补测「两条
// assistant 消息都缺 reasoning_content、尾部干净」也是 3/3 200 —— 「历史里
// 每条 assistant 都得带推理」那条文档口径在这个上游上压根不成立。
//
// 所以第 2、3 手的**补内容**这一半是白干的：真实原文该补（客户端会剥块，
// 不补模型下一轮读不到自己的推理，那是质量问题不是 400 问题），但**没有
// 原文时补任何东西都是错的**，正确动作是**跳过这条消息**。
//
// 补的范围仍然是**所有** assistant 消息（有原文的都补上）。已经带了思考
// 内容的消息一律不碰。
//
// 摘除条件：哪天 DeepSeek 允许缺省这些字段了，`newgate st off deepseek`
// 就能验证；确认不需要了整个文件可以直接删掉。
type reasoning struct{}

func (reasoning) Name() string { return "deepseek" }

// Before/After 是**产品这一侧**的排序声明——内核 2026-09-20 起不再认识产品插件名
// （那条边以前写在内核的 st-always.go / st-background.go 里，方向是反的，而且
// 未注册的名字在排序图里静默忽略，所以在纯内核构建里一直空转）。
// 只有本插件同时知道两边的名字，所以由本插件声明：
//
//	After(["claude-bg"])       claude-bg 先动手（后台非流式请求改道/补 thinking），
//	                           本插件处理剩下的形态
//	Before(["always-thinks"]) 本插件先动手，内核那个兜底翻译器最后扫尾
func (reasoning) Before() []string { return []string{"always-thinks"} }
func (reasoning) After() []string  { return []string{"claude-bg"} }

func (reasoning) Why() string {
	return i18n.T("DeepSeek thinking mode wants reasoning passed back verbatim, but the "+
		"client strips it → 400\n"+
		"so we put it back (the original the client carries → thinkcache; **no original "+
		"means skip it, never invent one**); only the Claude Code path turns thinking "+
		"off outright (it strips the blocks, so thinking would be spent for nothing); "+
		"a foreign unclosed tool loop is first lossily rebuilt from the visible tool "+
		"results, then DeepSeek takes over", nil)
}

func (reasoning) Metrics() []special.MetricInfo {
	return []special.MetricInfo{{
		Action: "tool_loop_rebase",
		Hint: i18n.T("a lossy rebuild happened before taking over a foreign unclosed "+
			"tool loop", nil),
	}}
}

// NeedsToolLoopRebase 标出 DeepSeek 接手别家未闭合 tool loop 时需要有损重建。
//
// 这不是通用限制。2026-09-16 对同一份真实 thinking + tool_use 做 A/B：
//   - Ark → Ark:      3/3 200
//   - Ark → DeepSeek: 3/3 400 reasoning_content must be passed back
//   - DeepSeek → Ark: 3/3 200
//   - DeepSeek → DS:  3/3 200
//
// API 是无状态的；不兼容的是请求里携带的 reasoning/tool 编码。只拦迁入
// DeepSeek，别把这个上游怪癖扩大成所有 provider 都失去 fallback。
func (d reasoning) NeedsToolLoopRebase(originProvider, originModel string,
	candidate *special.Request) (bool, string) {
	if !d.Match(candidate) ||
		(candidate.Provider == originProvider && candidate.Model == originModel) {
		return false, ""
	}
	return true, i18n.T("taking over another upstream's unclosed reasoning/tool state "+
		"needs a lossy rebuild first", nil)
}

// toolLoopRebasePrompt 是「尾部没有用户指令」时补上去的那一句话。
//
// **两个调用点共用它**：
//   - repairTailShape（Apply 第 4 步）：尾部形状本身就有问题，客户端没意识到；
//   - RebaseToolLoop：跨上游迁移，外来 hidden reasoning 不能原样接手。
//
// 2026-09-18 从一句英文长句改成一个词。起因是用户的原话：「prompt 注入也尽可能
// 用最简单的、不影响流程的，比如（"继续"）这种，别搞太复杂了」。这句注入会进
// 上游、**也会进对话历史**——长句在那里就是一段用户没写过的正文，越长越像
// 「有人在替我说话」。
//
// 必须诚实说清的一点：当初那次 A/B（Ark → DeepSeek，追加前稳定 400、追加后
// 5/5 200）用的是那句长英文。起作用的是「尾部多了一条**普通用户指令**」这个
// 事实本身，不是那句话的内容——上游要的是「这一轮有新指令」，不是一个解释。
// 换成「继续」之后 2026-09-18 在真实 Claude Code 请求体上复测过：
// 裸 [tool_result] 尾部 400，同一个请求体只加一个 " "（空格）就 200。
//
// 也**不是**随便什么内容都行：空串不行。实测 [tool_result, text""] 会被上游
// 以 `missing field text` 拒掉（它的解析层对空串等同于字段缺席）。
//
// **这一句不走 i18n，而且永远不该走**：它被 json.Marshal 之后写进**请求体的
// 字节**，是发给上游的协议数据，不是给谁看的文案。翻译它等于改变发出去的内容
// （上游对这段文本没有语义要求，但用户的历史里会长住这段字）。它同时也是包级
// const，而包初始化早于装语言。
const toolLoopRebasePrompt = "继续"

// RebaseToolLoop 把 tool_result 变成同时带普通用户指令的新回合。旧 reasoning、
// tool_use、tool_result 全部保留作可见上下文，只追加这一段；实测 Ark →
// DeepSeek 原请求稳定 400，追加后 5/5 200。
func (reasoning) RebaseToolLoop(body []byte, _ *special.Request) ([]byte, string, error) {
	q, _ := json.Marshal(toolLoopRebasePrompt)
	block := []byte(`{"type":"text","text":` + string(q) + `}`)
	out, changed, err := rewrite.AppendLastArrayItemArray(body, "messages", "content",
		block, func(item []byte) bool {
			role, _ := rewrite.TopLevelString(item, "role")
			return role == "user"
		})
	if err != nil {
		return nil, "", err
	}
	if !changed {
		return body, "", nil
	}
	return out, i18n.T("lossily rebuilt a foreign tool loop: kept the tool results and "+
		"appended a plain user instruction to continue", nil), nil
}

// Match 只认 DeepSeek：模型名、provider 名、base URL 任一处出现 deepseek。
//
// 为什么看这三处：模型名最可靠（deepseek-chat / deepseek-reasoner），但经过
// 聚合网关时可能被改名，此时 provider 名或 endpoint 里通常仍留着痕迹。
//
// 反向兜底：Anthropic 官方端点一律不碰。它对未知字段是严格的，而它也从来
// 不会报这个错——真有人在官方端点上挂了个叫 deepseek 的 provider，也不该
// 让这个补丁去给它加字段。
func (reasoning) Match(r *special.Request) bool {
	return r != nil && MatchTarget(r.Model, r.Provider, r.BaseURL)
}

func (reasoning) Apply(body []byte, r *special.Request) ([]byte, []string, error) {
	var notes []string
	out := body

	// 是否开启思考只看请求的显式字段。Claude Code × DeepSeek 的缺省关闭
	// 由组合模块 claudecode_deepseek 负责，本模型模块不认识调用端。
	thinkingOn := true
	if raw, has := rewrite.TopLevelRaw(out, "thinking"); has {
		if t, _ := rewrite.TopLevelString(raw, "type"); t == "disabled" {
			thinkingOn = false
		}
	}
	// 认不出 type（adaptive、或形状不认识）时按「开着」处理：多试一步顶多是
	// 冗余，漏试就少补一块。

	// 没有 messages（如 /v1/models 之类）就到此为止，不算错。
	if _, has := rewrite.TopLevelRaw(out, "messages"); !has {
		return out, notes, nil
	}

	// 2) 给 assistant 消息补 reasoning_content（OpenAI 方言那句报错）。
	//    有真实原文才补；没有就跳过这条消息，并记下**为什么**没原文。
	//
	//    note 的规矩（2026-09-18 定）：**动过手才报**。一步都没动手就没
	//     notes——「不静默」管的是改写，不是每次路过。跳过的原因分类先攒
	//     pending，最后一步之后如果这个请求确实被改过，再连同原因一起报出来。
	var sk1 skipTally
	var pending []string
	restored := 0
	valReasoning := func(item []byte) []byte {
		if q, ok := pickReasoning(item); ok {
			restored++
			return q
		}
		sk1.add(skipCauseOf(item), toolIDsAny(item))
		return nil // 没有原文 → 这条一个字节都不补
	}
	//    被开关点关掉的那一手**不打** per-request note：那是用户自己拧的，每发
	//    都报一遍正是上面那条规矩要消掉的噪音。可见性走 `newgate plugin` 与
	//    `newgate status`，不走日志。
	if !enabled(r, SwitchBackfillReasoning) {
		// 第 2 手被关掉，跳过。
	} else if nb, n, err := rewrite.EnsureArrayItemFieldFunc(out, "messages",
		"reasoning_content", valReasoning, isAssistant); err != nil {
		// messages 形状不认识：前面那些仍然有效，这步放弃。
		notes = append(notes, i18n.T("messages left untouched ({err})",
			i18n.A{"err": err.Error()}))
	} else if n > 0 {
		out = nb
		notes = append(notes, reasoningNote("reasoning_content", n, restored, sk1))
	} else if sk1.total > 0 {
		// 一条都没补上：**先攒着**，等看这一发到底有没有被改过（见函数末尾）。
		pending = append(pending, reasoningNote("reasoning_content", sk1.total, 0, sk1))
	}

	// 3) 思考模式开着 → assistant 的 content[] 开头必须有 thinking 块
	//    （Anthropic 方言那句报错）。同样：只有真实原文才补。
	//    只对 Anthropic 方言做：OpenAI 方言里回传推理的载体是 reasoning_content。
	if thinkingOn && anthropicDialect(r) && enabled(r, SwitchBackfillThinkingBlock) {
		restored := 0
		var sk2 skipTally
		valBlock := func(item []byte) []byte {
			// 与第 2 步同一份来源（pickReasoning）：客户端带回的 thinking 块
			// 原文 → thinkcache 真实推理。**不能**图省事去读第 2 步刚写进去的
			// reasoning_content 当来源——那是循环论证（第 2 步的值本来就是这里
			// 挑出来的），而且会让日志里的「用了真实原文」计数虚高。
			if q, ok := pickReasoning(item); ok {
				restored++
				return []byte(`{"type":"thinking","thinking":` + string(q) + `}`)
			}
			sk2.add(skipCauseOf(item), toolIDsAny(item))
			return nil // 没有原文 → 不插块
		}
		if nb, n, err := rewrite.EnsureArrayItemArrayHeadFunc(out, "messages", "content",
			valBlock, isAssistant, lacksThinking); err != nil {
			notes = append(notes, i18n.T("content left untouched ({err})",
				i18n.A{"err": err.Error()}))
		} else if n > 0 {
			out = nb
			notes = append(notes, reasoningNote(i18n.T("thinking block", nil),
				n, restored, sk2))
		} else if sk2.total > 0 {
			pending = append(pending, reasoningNote(i18n.T("thinking block", nil),
				sk2.total, 0, sk2))
		}
	}

	// 4) 尾部形状：最后一条 user 消息的 content[] 里只有 tool_result、一个字都
	//    没有时，追加一条最简用户指令。详见 repairTailShape 与文件头第 4 手。
	//
	//    **不能挂在 thinkingOn 上**：2026-09-17/18 两次实测都确认，把顶层写成
	//    thinking:{"type":"disabled"} 且不带 tools，同一条校验照样触发。
	if !enabled(r, SwitchTailShape) {
		// 第 4 手被关掉，跳过。它是唯一修根因的一手，关掉之后裸 tool_result
		// 尾部会直接撞上游那条误报的 400。
	} else if nb, changed, err := repairTailShape(out); err != nil {
		notes = append(notes, i18n.T("tail shape left untouched ({err})",
			i18n.A{"err": err.Error()}))
	} else if changed {
		out = nb
		notes = append(notes, i18n.T("the trailing user turn holds only tool_result and no "+
			"user instruction — appended a continue instruction (the upstream misreports "+
			"an instruction-less tail as a missing reasoning_content)", nil))
	}

	// 前面攒下的「跳过」报告，只在**这一发确实被改过**时才吐出来。
	//
	// 为什么是这个条件：「不静默」管的是**改写**——用户有权知道自己发出去的
	// 请求被动过哪一笔。而「没原文可补」在没动手的时候不算改写（字段本来就
	// 缺席，我们一个字节没碰），每天上千条这种日志是这个会话最大的噪音源，
	// 真出事时反而淹没了有用的行。反过来，只要这一发被改过，跳过就必须
	// 一并报出来——那时它和「用了真实原文」是同一笔账的两半，少报一半就是
	// 不诚实。
	if len(pending) > 0 && len(notes) > 0 {
		notes = append(notes, pending...)
	}

	return out, notes, nil
}

// repairTailShape 修「最后一条 user 消息只有 tool_result」这个形状。
//
// 判据三条，全部实测过（2026-09-17 与 2026-09-18 各一遍，3/3，打真实
// smt-deepseek/deepseek-flash）：
//
//  1. **锚在「最后一条 role:user 的消息」，不是数组的最后一项**。Claude Code
//     会在 tool_result 之后追加一条 role:"system" 的插话（「The user sent a new
//     message while you were working: …」），数组最后一项是那条 system，而被上游
//     拒掉的却是它前面那条只有 tool_result 的 user 轮。上游不认 system 里的指令。
//     现场：dump/err-400-req000464、err-400-req000412。
//  2. **那块 content[] 里必须全是 tool_result 块**，不是「没有 text 块」。
//     后者太宽，会把本来就能过的尾部一起改掉——[image] 和 [tool_result, image]
//     实测都是 200。所以线画在「除 tool_result 之外的任何块都算有指令」上。
//  3. 调用点**不在 thinkingOn 闸门里**（见 Apply 第 4 步）。
//
// 已经带了文字/图片的尾部一律不碰——它本来就能过，多塞一句话只是往用户的对话
// 里加噪音。
//
// 用 AppendArrayItemArrayAt 而不是 AppendLastArrayItemArray：按下标挑。不写成
// 「从后往前找第一条合形状的」是因为长历史里中段的 tool_result-only 轮到处都是，
// 从后往前找会去改一条**不该动**的老消息。
func repairTailShape(body []byte) ([]byte, bool, error) {
	raw, ok := rewrite.TopLevelRaw(body, "messages")
	if !ok {
		return body, false, nil
	}
	items, ok := rewrite.ArrayItems(raw)
	if !ok || len(items) == 0 {
		return body, false, nil
	}

	// 1) 最后一条 role:user 消息的下标。
	idx := -1
	for i, it := range items {
		if role, _ := rewrite.TopLevelString(it, "role"); role == "user" {
			idx = i
		}
	}
	if idx < 0 {
		return body, false, nil
	}

	// 它之后**不能有 assistant 消息**。那种尾部（数组以 assistant 收尾）是另一条
	// 规则在管，而且追加指令证明**没有用**：实测把继续指令追加到那个 user 轮上，
	// 3/3 还是 400。既然改不好，就别改：往用户的对话里塞一句模型看不见效果的
	// 噪音，比 400 更糟。
	//
	// 那一族从真实客户端到不了：Claude Code 的请求要么以 user 的 tool_result 收尾
	// （等模型接着干），要么以 user 的文字收尾，要么在两者之后追加一条 role:"system"
	// 的插话。dump 里全部 err-400 过了一遍，没有一份是 assistant 收尾。
	for _, it := range items[idx+1:] {
		if role, _ := rewrite.TopLevelString(it, "role"); role == "assistant" {
			return body, false, nil
		}
	}

	// 2) 它的 content[] 非空、且**全是** tool_result 块。
	content, ok := rewrite.TopLevelRaw(items[idx], "content")
	if !ok {
		return body, false, nil
	}
	blocks, ok := rewrite.ArrayItems(content)
	if !ok || len(blocks) == 0 {
		// content 是普通字符串（本来就有文字），或形状不认识：不动。
		return body, false, nil
	}
	for _, b := range blocks {
		if t, _ := rewrite.TopLevelString(b, "type"); t != "tool_result" {
			return body, false, nil
		}
	}

	q, err := json.Marshal(toolLoopRebasePrompt)
	if err != nil {
		return body, false, err
	}
	block := []byte(`{"type":"text","text":` + string(q) + `}`)
	return rewrite.AppendArrayItemArrayAt(body, "messages", "content", idx, block)
}

// pickReasoning 为一条 assistant 消息选出回传的推理原文（JSON 编码后的字符串值）。
//
// 两级来源，严格对应客户端回传的形态：
//  1. 消息自己的 content[] 里带的 thinking 块——客户端保留了原文就直接用，
//     不依赖缓存存活；
//  2. thinkcache 里那轮真实发生的推理（tool id / 正文哈希找回）。
//
// 都没有 → false，调用方**跳过这条消息**（一个字节都不补），并记下为什么
// 没有原文可补（见 skipCause）。编一个占位符是错的，理由见文件头。
func pickReasoning(item []byte) ([]byte, bool) {
	if t := clientThinkingText(item); t != "" {
		if q, err := json.Marshal(t); err == nil {
			return q, true
		}
	}
	if blob, ok := thinkcache.Default.Lookup(item); ok {
		if q, err := json.Marshal(string(blob)); err == nil {
			return q, true
		}
	}
	return nil, false
}

// skipCause 是「这一条为什么没有推理原文可补」的分类计数。
//
// 为什么要有它：这个分类是 2026-09-18 那次结论的直接证据链。原始疑问是
// 「为什么没有 thinking？是缓存坏了，还是请求本来就没有？」——这两件事的
// 处置完全相反：
//
//   - 上游那一轮就没给 ⇒ 只能跳过（补任何东西都是编）；
//   - 上游给了、我们没存住 ⇒ 该去修 thinkcache 的命中率。
//
// 分类只取**能从请求本身读出来**的事实（不依赖运行时缓存状态，因为日志是
// 事后读的，缓存早被顶掉了）：
//
//	notool   这条 assistant 消息没有 tool call——纯文本轮，只能靠正文哈希
//	         找回，客户端改写正文就 miss（TextKey 的已知弱点）
//	nocache  有 tool call，但它的 id 在 thinkcache 里查不到
//	nokey    既没 tool_use 也没正文字段——形状本身就不认识
//
// 「客户端有没有带回 thinking 块」不在这里分类：clientThinkingText 已经先试过
// 了，能取到就不会走到这一层。所以**走到这一层就等价于「客户端没带」**，
// 这是分支结构保证的。
//
// 2026-09-18 的实测结果：全库 3494 条跳过、分类**全是 nocache**，逐条拿
// tool id 反查「记下本轮推理内容」——**一条都没命中**。结论：这些推理上游
// 从来没给过我们（adaptive 的语义就是模型自己决定想不想），不是缓存问题。
// 同一份请求连打 6 次，上游 3 次给思考、3 次不给，就是这个原因。
type skipCause string

const (
	causeNoToolUse  skipCause = "notool"  // 纯文本轮（靠正文哈希找回，miss）
	causeToolNotIn  skipCause = "nocache" // 有 tool_use 但缓存查不到
	causeNoIdentity skipCause = "nokey"   // 既无 tool_use 也无正文，认不出
)

// skipCauseOf 判定一条 assistant 消息为什么没有原文可补（分类见上）。
func skipCauseOf(item []byte) skipCause {
	if hasToolUse(item) {
		return causeToolNotIn
	}
	if text, ok := rewrite.TopLevelString(item, "content"); ok && strings.TrimSpace(text) != "" {
		return causeNoToolUse
	}
	return causeNoIdentity
}

// hasToolUse 这条消息带 tool call 吗（两种方言都认）。
func hasToolUse(item []byte) bool {
	return len(toolIDsAny(item)) > 0
}

// toolIDsAny 抽出这条 assistant 消息的 tool call id，两种方言都认。
//
//	Anthropic  content[].{type:tool_use, id}
//	OpenAI     tool_calls[].id
//
// 为什么要两种：分类日志和缓存找回必须**认同一批 id**。thinkcache 的
// KeysForAssistantMessage 就是两方言都认的（那边用 json 解析），这边如果只认
// Anthropic，OpenAI 方言的轮次会被错报成 notool，日志给出的结论就是错的。
func toolIDsAny(item []byte) []string {
	ids := toolUseIDs(item) // Anthropic 方言
	if len(ids) > 0 {
		return ids
	}
	raw, ok := rewrite.TopLevelRaw(item, "tool_calls") // OpenAI 方言
	if !ok {
		return nil
	}
	items, ok := rewrite.ArrayItems(raw)
	if !ok {
		return nil
	}
	for _, tc := range items {
		if id, _ := rewrite.TopLevelString(tc, "id"); id != "" {
			ids = append(ids, id)
		}
	}
	return ids
}

// skipTally 是分类计数 + 跳过消息的身份样本。
//
// 为什么要记 id（2026-09-18 加）：用户的原话是「为什么没有 thinking？到底是你
// 缓存机制的问题，还是请求本身就没有/为空——你他妈给我 trace 出来」。只报个数
// 永远分不清这两件事：跳过是**结果**，日志里没有能对上的**身份**就没法反查。
//
// 有了 id 就能和另外两条日志对起来：
//
//	[proxy] #N recorded … bytes of reasoning and N keys … this round
//	            ↑ 这一轮的推理存进了缓存（内核 forward.go 打的）
//	[proxy] #N 本轮上游没给推理内容（N 个 tool call）
//	            ↑ 上游压根没给
//
// 两条日志行的**原文随 i18n 一起变成了英文**（本模块迁移时同步更新），所以要
// 去 grep 的是英文那两串，不是这里的转述。
//
// 拿跳过消息的 id 去 grep 这两条：
//   - 命中「没给推理」⇒ 请求本身就没有（上游没思考），跳过是唯一正确动作；
//   - 命中「记下推理」⇒ 缓存里**曾经**有过，是后来丢了（TTL/淘汰/重启）⇒ 缓存问题；
//   - 两条都不命中 ⇒ 这一轮没经过我们（别的 provider 产生的历史）或响应没被观测到。
//
// 内存代价：每条跳过的消息一个 id 字符串。一次请求最多几百条，且只活到写日志
// 那一刻——不缓存、不落盘。
type skipTally struct {
	total  int
	byKind map[skipCause]int
	ids    []string // 跳过消息的 tool call id（纯文本轮没有 id，不计入）
}

func (t *skipTally) add(c skipCause, ids []string) {
	t.total++
	if t.byKind == nil {
		t.byKind = map[skipCause]int{}
	}
	t.byKind[c]++
	t.ids = append(t.ids, ids...)
}

// detail 渲染成日志片段；没有跳过项时返回空串。
//
// 顺序写死（notool → nocache → nokey），因为日志是要**跨请求比对**的：今天
// 这一栏里 nocache 占多数，明天 notool 占多数，含义完全不同。map 的随机
// 遍历顺序会让每一行长得不一样，没法比。只报非零项，避免三个 0 占版面。
func (t *skipTally) detail() string {
	if t == nil || t.total == 0 {
		return ""
	}
	var parts []string
	for _, k := range []skipCause{causeNoToolUse, causeToolNotIn, causeNoIdentity} {
		if n := t.byKind[k]; n > 0 {
			parts = append(parts, fmt.Sprintf("%s %d", k, n))
		}
	}
	if len(parts) == 0 {
		return ""
	}
	// 计数之间的分隔符也是**语言数据**（中文是顿号），所以它自己是一条消息：
	// 把分隔符硬写成 ASCII 逗号的话，中文那一行会变成中英混排。
	items := strings.Join(parts, i18n.T(", ", nil))
	if len(t.ids) == 0 {
		return i18n.T("({items}; skipped messages have no tool id (plain text turns))",
			i18n.A{"items": items})
	}
	// 上限 8 个：够看出「是不是同一批老消息每次都出现」，又不至于把一行日志
	// 撑成几 KB。超出的只报个数。
	shown := t.ids
	more := ""
	if len(shown) > 8 {
		more = i18n.T(" and {n} in all", i18n.A{"n": len(shown)})
		shown = shown[:8]
	}
	return i18n.T("({items}; tool ids of skipped messages: {ids}{more})",
		i18n.A{"items": items, "ids": strings.Join(shown, " "), "more": more})
}

// clientThinkingText 抽出这条消息 content[] 里 thinking 块的文本。
//
// 客户端（开了 interleaved thinking 的 Claude Code）有时会把自己那轮的
// thinking 块原样带回来——那是推理原文，别浪费。多个块就拼起来。
// redacted_thinking 没有 thinking 字段，自然取不到文本。
func clientThinkingText(item []byte) string {
	content, ok := rewrite.TopLevelRaw(item, "content")
	if !ok || len(content) == 0 || content[0] != '[' {
		return ""
	}
	blocks, ok := rewrite.ArrayItems(content)
	if !ok {
		return ""
	}
	var sb strings.Builder
	for _, b := range blocks {
		if len(b) == 0 || b[0] != '{' {
			continue
		}
		if t, _ := rewrite.TopLevelString(b, "type"); t == "thinking" {
			if s, ok := rewrite.TopLevelString(b, "thinking"); ok {
				sb.WriteString(s)
			}
		}
	}
	return sb.String()
}

// reasoningNote 把「补回真实原文的」和「跳过没补的」分开报，跳过的那部分
// **必须带上原因分类**。
//
// 必须分开：跳过意味着模型这一轮读不到自己上一轮的推理，思考质量会掉。
// 这不是成功，用户有权在日志里一眼看出发生了多少次（docs/01-product.md 的「不静默」）。
//
// 分类的理由见 skipCause 的注释——「上游那一轮本来就没给」和
// 「给了但我们没存住」是两件处置完全相反的事，只报个数等于没报。
func reasoningNote(what string, total, restored int, sk skipTally) string {
	// `assistant` 是 JSON 里的 role 值，留在消息外面；`{n}` 由 N() 注入。
	s := i18n.N("backfilled {what} on {n} assistant message: {restored} used the real "+
		"reasoning text",
		"backfilled {what} on {n} assistant messages: {restored} used the real "+
			"reasoning text",
		total, i18n.A{"what": what, "restored": restored})
	if sk.total > 0 {
		s += i18n.T(", {n} had no original text to backfill and were **left untouched**"+
			"{detail} — those rounds the model cannot see its own reasoning",
			i18n.A{"n": sk.total, "detail": sk.detail()})
	}
	return s
}

func isAssistant(item []byte) bool {
	role, _ := rewrite.TopLevelString(item, "role")
	return role == "assistant"
}

// anthropicDialect 客户端这次发的是 Anthropic 方言吗（/v1/messages）。
//
// 判据用路径，不用 r.Protocol：protocol 说的是「怎么发到上游」（实测聚合
// 网关的 provider 全标 "openai"，却照样收 /v1/messages 的 Anthropic 方言
// 请求），而客户端发什么路径才是这次请求本身的方言。
func anthropicDialect(r *special.Request) bool {
	return r != nil && strings.HasPrefix(r.Path, "/messages")
}

// lacksThinking 这条 content[] 里有没有思考内容。
// redacted_thinking 也算——那是上游自己加密过的思考块，有它就说明思考内容
// 已经原样回传了，再往前插一个空块只会多一个块。
func lacksThinking(content []byte) bool {
	items, ok := rewrite.ArrayItems(content)
	if !ok {
		return false // 形状不认识：不动
	}
	for _, it := range items {
		if len(it) == 0 || it[0] != '{' {
			continue
		}
		switch t, _ := rewrite.TopLevelString(it, "type"); t {
		case "thinking", "redacted_thinking":
			return false
		}
	}
	return true
}

// AuditReasoning 产出「本轮回传推理内容」的逐条审计报告，供 400 现场取证用。
//
// 直接解析 we-sent 的 messages，逐条 assistant 消息标出有没有 reasoning_content。
// **没有的那些不是我们的错**：那是上游那一轮本来就没给（见文件头的实测）。
// 报告里连 tool_use id 一起打出来，拿它去 grep 日志里的 recorded … bytes of
// reasoning 那行（内核 forward.go 打的，见报告正文里的原文）就能确认——命中
// 说明是我们弄丢的，不命中说明上游没给过。
//
// 这是纯只读分析，不依赖运行时的缓存状态——缓存此刻可能已经被后续请求顶掉，
// 但 400 发生时写下的这份报告是当时事实的定格。
func AuditReasoning(out []byte) string {
	msgs, ok := rewrite.TopLevelRaw(out, "messages")
	if !ok {
		return i18n.T("(no messages field, cannot audit)\n", nil)
	}
	items, ok := rewrite.ArrayItems(msgs)
	if !ok {
		return i18n.T("(messages is not an array, cannot audit)\n", nil)
	}
	var b strings.Builder
	assistant, real, missing := 0, 0, 0
	for i, it := range items {
		if !isAssistant(it) {
			continue
		}
		assistant++
		if _, has := rewrite.TopLevelString(it, "reasoning_content"); has {
			real++
			continue
		}
		missing++
		// `msg[0]` / `reasoning_content` 都是机器标记（字段名与下标），留在外面。
		b.WriteString(i18n.T("msg[{i}] has no reasoning_content  {keys}\n",
			i18n.A{"i": i, "keys": msgKeys(it)}))
	}
	// 「bytes of reasoning」那句是内核 thinkcache 侧真正打出来的日志行原文
	// （forward.go 的 recorded … bytes of reasoning），要 grep 就得给原文。
	return i18n.T("assistant messages: {total} in all, {real} with reasoning text, "+
		"{missing} without\n"+
		"(the ones without are **what the upstream never gave us that round**, not "+
		"something we lost — see the tool id below,\n"+
		"  grep the log for \"bytes of reasoning\" and you will see) one by one:\n{listing}",
		i18n.A{"total": assistant, "real": real, "missing": missing, "listing": b.String()})
}

func (reasoning) AuditResponse(body []byte) string { return AuditReasoning(body) }

// msgKeys 抽出这条 assistant 消息的 tool_use id（缓存找回的 key 就靠它），
// 纯文本轮没有 tool_use，靠正文哈希。取证时拿 id 反查 thinkcache 该不该有。
func msgKeys(item []byte) string {
	toolIDs := toolUseIDs(item)
	if len(toolIDs) == 0 {
		return i18n.T("(plain text turn, no tool_use)", nil)
	}
	return "tool_use ids: " + strings.Join(toolIDs, " ")
}

// toolUseIDs 抽出 Anthropic 方言 content[] 里 tool_use 块的 id。
//
// 与 thinkcache.KeysForAssistantMessage 认的是同一批 id（那边用 json 解析，
// 这边是纯字节手术的原语）——**必须是同一批**，否则「没有原文可补的原因」
// 这个分类就会和缓存实际的 key 对不上，日志里的 nocache 会指向错误的结论。
func toolUseIDs(item []byte) []string {
	c, ok := rewrite.TopLevelRaw(item, "content")
	if !ok {
		return nil
	}
	bs, ok := rewrite.ArrayItems(c)
	if !ok {
		return nil
	}
	var ids []string
	for _, blk := range bs {
		if t, _ := rewrite.TopLevelString(blk, "type"); t != "tool_use" {
			continue
		}
		if id, _ := rewrite.TopLevelString(blk, "id"); id != "" {
			ids = append(ids, id)
		}
	}
	return ids
}
