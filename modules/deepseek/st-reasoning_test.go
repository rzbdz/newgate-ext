package deepseek

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/rzbdz/newgate/modules/gateway/special"
	"github.com/rzbdz/newgate/modules/gateway/thinkcache"
)

// claudeReq Claude Code 形态的上下文：/a/claude/ + /v1/messages。
//
// 这个包里的 deepseek 测试默认都是「Claude Code 打 DeepSeek 网关」这一幕
// ——插件就是为它写的。opencode 那条路（Agent 空 + /chat/completions）在
// TestDeepSeekOpencodeKeepsThinking 里单独立着。
func claudeReq(model string) *special.Request {
	return &special.Request{Model: model, Provider: "gw", BaseURL: "https://gw.example.com/v1",
		Protocol: "anthropic", Path: "/messages", Agent: "claude"}
}

// seedCache 往全局 thinkcache 里塞一条「上一轮上游真的给过」的推理原文。
//
// 2026-09-18 起，**有没有原文**是补与不补的唯一分界：有原文就补回去，没有
// 就一个字节都不碰（编一个占位符是错的，理由见 st-reasoning.go 文件头）。
// 所以凡是要验「补回去」的测试，都必须先在这里造出真实来源。
func seedCache(toolID, text string) {
	thinkcache.Default.Put([]string{thinkcache.ToolKey(toolID)}, []byte(text))
}

// 有真实原文时，assistant 的 content[] 要带上 thinking 块并排在最前面——
// 客户端（Claude Code）对非官方端点会主动剥掉这些块，只能我们补。
func TestDeepSeekBackfillsThinkingBlockWhenThinkingOn(t *testing.T) {
	seedCache("t1", "上一轮真实想过的内容")
	body := []byte(`{"model":"deepseek-chat","thinking":{"type":"enabled","budget_tokens":1024},` +
		`"messages":[` +
		`{"role":"user","content":[{"type":"text","text":"hi"}]},` +
		`{"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"Read","input":{"n":9007199254740993}}]},` +
		`{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"ok"}]}` +
		`]}`)

	out, notes, err := reasoning{}.Apply(body, claudeReq("deepseek-chat"))
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(notes) == 0 {
		t.Fatal("改了东西却没回报 notes —— 违反「不静默」")
	}

	var got struct {
		Thinking json.RawMessage `json:"thinking"`
		Messages []struct {
			Role    string            `json:"role"`
			Content []json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("改完不是合法 JSON: %v\n%s", err, out)
	}

	// 客户端显式要了思考模式，不能被我们悄悄关掉
	if !strings.Contains(string(got.Thinking), "enabled") {
		t.Fatalf("客户端的 thinking 被改掉了: %s", got.Thinking)
	}

	// assistant 的 thinking 块必须在**开头**：协议要求它排在 tool_use 之前
	asst := got.Messages[1]
	if len(asst.Content) != 2 {
		t.Fatalf("assistant content 块数 = %d，想要 2", len(asst.Content))
	}
	if !strings.Contains(string(asst.Content[0]), `"type":"thinking"`) {
		t.Fatalf("第一个块不是 thinking: %s", asst.Content[0])
	}
	if !strings.Contains(string(asst.Content[1]), "tool_use") {
		t.Fatalf("原来的 tool_use 块丢了: %s", asst.Content[1])
	}

	// user 消息一个字节都不该被碰
	if strings.Contains(string(got.Messages[0].Content[0]), "thinking") {
		t.Fatal("user 消息被补了 thinking 块")
	}

	// 大整数必须原样：JSON 往返会把它变成 ...92
	if !strings.Contains(string(out), "9007199254740993") {
		t.Fatalf("大整数被改坏了:\n%s", out)
	}
}

// 思考模式没开时塞 thinking 块，会被上游以「关了还给我思考块」拒掉。
func TestDeepSeekNoThinkingBlockWhenDisabled(t *testing.T) {
	for _, body := range [][]byte{
		[]byte(`{"model":"deepseek-chat","thinking":{"type":"disabled"},"messages":[{"role":"assistant","content":[{"type":"text","text":"hi"}]}]}`),
	} {
		out, _, err := reasoning{}.Apply(body, claudeReq("deepseek-chat"))
		if err != nil {
			t.Fatalf("Apply: %v", err)
		}
		if strings.Contains(string(out), `"type":"thinking"`) {
			t.Fatalf("思考关着还是补了 thinking 块:\n%s", out)
		}
	}
}

// 已经带思考内容的消息一个字节都不动——包括上游加密过的 redacted_thinking。
func TestDeepSeekLeavesExistingThinkingAlone(t *testing.T) {
	for _, blk := range []string{
		`{"type":"thinking","thinking":"我在想","signature":"abc"}`,
		`{"type":"redacted_thinking","data":"xxx"}`,
	} {
		body := []byte(`{"thinking":{"type":"enabled"},"messages":[` +
			`{"role":"assistant","content":[` + blk + `,{"type":"text","text":"hi"}]}]}`)
		out, _, err := reasoning{}.Apply(body, claudeReq("deepseek-chat"))
		if err != nil {
			t.Fatalf("Apply: %v", err)
		}
		if strings.Count(string(out), `"type":"thinking"`)+
			strings.Count(string(out), `"type":"redacted_thinking"`) != 1 {
			t.Fatalf("给已有思考内容的消息又补了一块:\n%s", out)
		}
	}
}

// content 是纯字符串（Anthropic 允许）时没有块可插，跳过而不是报错。
func TestDeepSeekStringContentSkipped(t *testing.T) {
	body := []byte(`{"thinking":{"type":"enabled"},"messages":[{"role":"assistant","content":"hi"}]}`)
	out, _, err := reasoning{}.Apply(body, claudeReq("deepseek-chat"))
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if strings.Contains(string(out), `"type":"thinking"`) {
		t.Fatalf("往字符串 content 里插了块:\n%s", out)
	}
	if !json.Valid(out) {
		t.Fatalf("改完不是合法 JSON:\n%s", out)
	}
}

// reasoning_effort 与 thinking:disabled 互斥，上游会回
// 「thinking options type cannot be disabled when reasoning_effort is set」。
// 设了推理强度就别去关思考，改成补块。
func TestDeepSeekRespectsReasoningEffort(t *testing.T) {
	thinkcache.Default.Put([]string{thinkcache.TextKey("hi")}, []byte("上一轮真实想过的内容"))
	body := []byte(`{"model":"deepseek-chat","reasoning_effort":"high",` +
		`"messages":[{"role":"assistant","content":[{"type":"text","text":"hi"}]}]}`)
	out, _, err := reasoning{}.Apply(body, claudeReq("deepseek-chat"))
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if strings.Contains(string(out), `"type":"disabled"`) {
		t.Fatalf("设了 reasoning_effort 还去关思考:\n%s", out)
	}
	if !strings.Contains(string(out), `"type":"thinking"`) {
		t.Fatalf("没补 thinking 块:\n%s", out)
	}
	if strings.Contains(string(out), `"thinking":""`) {
		t.Fatalf("补了空 thinking 块——严格上游照样 400:\n%s", out)
	}
}

// 没有原文就**一个字节都不补**——占位符那条路已被实测推翻，见 st-reasoning.go
// 文件头。这条测试锁的是新契约：字段保持缺席，而不是被编一个值出来。
//
// 现场（2026-09-18）：全库 3494 条占位符，逐条拿 tool id 反查「记下本轮推理
// 内容」——**一条都没命中**，说明这些推理上游从来没给过我们。而实测（真实
// 上游，每格 3/3）证明 reasoning_content 在不在、是什么内容，对那个 400
// 毫无影响：省略字段 + 干净尾部 → 200。所以补占位符是纯粹的编造 + 烧 token。
func TestDeepSeekSkipsMessageWithoutReasoningSource(t *testing.T) {
	// 尾部带一条普通文字：这条测的是「补推理内容」那一半，尾部形状那一半
	// 由 TestTailShape* 系列单独管。尾部留成裸 tool_result 的话，第 4 手会
	// 按设计去补指令，这里就分不清是谁改的了。
	body := []byte(`{"model":"deepseek-chat","thinking":{"type":"enabled","budget_tokens":1024},` +
		`"messages":[` +
		`{"role":"assistant","content":[{"type":"tool_use","id":"t-never-cached-0001","name":"Read","input":{"n":9007199254740993}}]},` +
		`{"role":"user","content":[{"type":"tool_result","tool_use_id":"t-never-cached-0001","content":"ok"},{"type":"text","text":"接着来"}]}` +
		`]}`)

	out, notes, err := reasoning{}.Apply(body, claudeReq("deepseek-chat"))
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	// 一个字节都不许动：不补 reasoning_content，也不插 thinking 块。
	if string(out) != string(body) {
		t.Fatalf("没有原文时改动了请求（编造内容）：\n改前 %s\n改后 %s", body, out)
	}
	if strings.Contains(string(out), "reasoning_content") {
		t.Fatalf("凭空补了 reasoning_content:\n%s", out)
	}
	if strings.Contains(string(out), `"type":"thinking"`) {
		t.Fatalf("凭空插了 thinking 块:\n%s", out)
	}
	// 大整数必须原样：JSON 往返会把它变成 ...92
	if !strings.Contains(string(out), "9007199254740993") {
		t.Fatalf("大整数被改坏了:\n%s", out)
	}
	// 没改动就不该有 notes——「不静默」管的是**改动**，不改动不用报。
	if len(notes) != 0 {
		t.Fatalf("没改动却报了 notes: %v", notes)
	}
}

// 客户端自己带回的 thinking 块是推理原文：reasoning_content 直接用它的文本
// （不依赖缓存存活），也不再重复补块。
func TestDeepSeekUsesClientThinkingBlock(t *testing.T) {
	body := []byte(`{"model":"deepseek-chat","thinking":{"type":"enabled"},` +
		`"messages":[{"role":"assistant","content":[` +
		`{"type":"thinking","thinking":"我先想想","signature":"sig"},` +
		`{"type":"text","text":"答案"}]}]}`)

	out, _, err := reasoning{}.Apply(body, claudeReq("deepseek-chat"))
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	var got struct {
		Messages []struct {
			Role             string `json:"role"`
			ReasoningContent string `json:"reasoning_content"`
			Content          []struct {
				Type string `json:"type"`
			} `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("改完不是合法 JSON: %v\n%s", err, out)
	}

	a := got.Messages[0]
	if a.ReasoningContent != "我先想想" {
		t.Fatalf("reasoning_content 应取客户端 thinking 块原文，实际 %q", a.ReasoningContent)
	}
	if len(a.Content) != 2 {
		t.Fatalf("给已有 thinking 块的消息又补了一块:\n%s", out)
	}
}

// thinkcache 命中：reasoning_content 和 thinking 块都回填那轮真实的推理原文。
func TestDeepSeekUsesCacheReasoning(t *testing.T) {
	const toolID = "t-cache-hit-0002"
	thinkcache.Default.Put([]string{thinkcache.ToolKey(toolID)}, []byte("这轮真实的推理"))

	body := []byte(`{"model":"deepseek-chat","thinking":{"type":"enabled"},` +
		`"messages":[` +
		`{"role":"assistant","content":[{"type":"tool_use","id":"` + toolID + `","name":"Bash","input":{}}]}` +
		`]}`)

	out, _, err := reasoning{}.Apply(body, claudeReq("deepseek-chat"))
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	var got struct {
		Messages []struct {
			ReasoningContent string `json:"reasoning_content"`
			Content          []struct {
				Type     string `json:"type"`
				Thinking string `json:"thinking"`
			} `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("改完不是合法 JSON: %v\n%s", err, out)
	}

	a := got.Messages[0]
	if a.ReasoningContent != "这轮真实的推理" {
		t.Fatalf("reasoning_content 应取缓存原文，实际 %q", a.ReasoningContent)
	}
	if len(a.Content) == 0 || a.Content[0].Thinking != "这轮真实的推理" {
		t.Fatalf("thinking 块应取缓存原文:\n%s", out)
	}
}

// 计数诚实：跳过的那些，**原因必须分类报出来**，且不能被说成「用了真实原文」。
//
// 为什么分类是硬要求：用户的原话是「为什么没有 thinking？是你的缓存机制的问题，
// 还是请求本身就没有」——这两件事处置完全相反（前者要修缓存，后者只能接受），
// 只报个数等于没报。这条请求的 tool id 从没进过 thinkcache，所以必然是 nocache，
// 而且日志里要连 id 一起打出来，方便和「记下本轮推理内容」那行反查。
func TestDeepSeekSkipCauseReportedHonestly(t *testing.T) {
	const toolID = "t-never-cached-0003"
	body := []byte(`{"model":"deepseek-chat","thinking":{"type":"enabled"},` +
		`"messages":[{"role":"assistant","content":[` +
		`{"type":"tool_use","id":"` + toolID + `","name":"Read","input":{}}]}]}`)

	out, notes, err := reasoning{}.Apply(body, claudeReq("deepseek-chat"))
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if string(out) != string(body) {
		t.Fatalf("没有原文时改动了请求:\n%s", out)
	}
	all := strings.Join(notes, "\n")
	// **两个字段都没有原文 ⇒ 提前返回，一条 note 都不出**。这是有意的：
	// 一步都没动手就不该刷日志（每天上千条占位是这个会话的主要噪音源）。
	// 分类只在**确实动过手**时才报得出来，见下面那个混用例。
	if len(notes) != 0 {
		t.Fatalf("一条都没补却报了 notes:\n%s", all)
	}
}

// 混用：一条有原文、一条没有。动了手就必须把两边都如实报出来——
// 有原文的说「用了真实原文」，没原文的说「跳过」并带原因分类。
func TestDeepSeekSkipCauseReportedWhenPartlyRestored(t *testing.T) {
	const hitID, missID = "t-hit-0004", "t-miss-0004"
	seedCache(hitID, "这轮真实的推理")

	body := []byte(`{"model":"deepseek-chat","thinking":{"type":"enabled"},` +
		`"messages":[` +
		`{"role":"assistant","content":[{"type":"tool_use","id":"` + hitID + `","name":"Read","input":{}}]},` +
		`{"role":"assistant","content":[{"type":"tool_use","id":"` + missID + `","name":"Read","input":{}}]}` +
		`]}`)

	out, notes, err := reasoning{}.Apply(body, claudeReq("deepseek-chat"))
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !strings.Contains(string(out), "这轮真实的推理") {
		t.Fatalf("缓存命中却没回填:\n%s", out)
	}
	if strings.Contains(string(out), missID+`","name":"Read","input":{}}],"reasoning_content"`) {
		t.Fatalf("给缓存没命中的那条也补了内容:\n%s", out)
	}
	// 断言的是**英文原文 + 机器标记**（nocache 那类分类词、tool id），不是渲染
	// 出来的译文：译文随语言变，断言它会红（i18n 迁移时定的规矩）。
	all := strings.Join(notes, "\n")
	for _, want := range []string{"1 used the real reasoning text",
		"1 had no original text to backfill", "nocache", missID} {
		if !strings.Contains(all, want) {
			t.Fatalf("notes 里该有 %q（不静默 + 可反查）:\n%s", want, all)
		}
	}
	if strings.Contains(all, "backfilled thinking block on 2 assistant messages: "+
		"2 used the real reasoning text") {
		t.Fatalf("跳过的被虚报成真实原文:\n%s", all)
	}
}

// ---- opencode（裸 /v1 + OpenAI 方言）这条路上不能关思考 ----

// 现场（2026-09-15）：opencode 走 OpenAI 方言，压根不会写 thinking 这个
// Anthropic 字段，于是条条请求都被当成「客户端没要思考」补上 disabled——
// 用户看到的是「deepseek somehow 不思考了」。
//
// 修后：关思考只给 Claude Code（Agent=="claude"）。这里 Agent 空、路径
// /chat/completions，就是 opencode 的形态：thinking 一个字节都不许动，
// 但推理内容照旧替它回传（思考开着，上游就会要）。
func TestDeepSeekOpencodeKeepsThinking(t *testing.T) {
	seedCache("call_oa_1", "上一轮真实想过的内容")
	body := []byte(`{"model":"deepseek-flash","stream":true,"messages":[` +
		`{"role":"user","content":"看下这个文件"},` +
		`{"role":"assistant","content":"看完了","tool_calls":[` +
		`{"id":"call_oa_1","type":"function","function":{"name":"Read","arguments":"{}"}}]}` +
		`]}`)

	r := &special.Request{Model: "deepseek-flash", Provider: "smt-deepseek",
		BaseURL: "https://gw.example.com/v1", Protocol: "openai",
		Path: "/chat/completions"}
	out, notes, err := reasoning{}.Apply(body, r)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if strings.Contains(string(out), `"thinking"`) {
		t.Fatalf("opencode 的请求被关了思考（现场 bug）:\n%s", out)
	}

	var got struct {
		Messages []struct {
			ReasoningContent string `json:"reasoning_content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("改完不是合法 JSON: %v\n%s", err, out)
	}
	if got.Messages[1].ReasoningContent != "上一轮真实想过的内容" {
		t.Fatalf("缓存里有原文却没用它回填，实际 %q:\n%s",
			got.Messages[1].ReasoningContent, out)
	}
	if !strings.Contains(strings.Join(notes, "\n"), "reasoning_content") {
		t.Fatalf("改了 reasoning_content 却没回报（违反「不静默」）: %v", notes)
	}
}

// 第 3 手（content[] 开头的 thinking 块）是 Anthropic 方言的协议要求；
// OpenAI 方言里回传推理的载体是 reasoning_content，往人家的 content[] 里
// 塞一个上游不认识的块类型只会招 400。
func TestDeepSeekNoThinkingBlockInOpenAIDialect(t *testing.T) {
	seedCache("t-oa-0001", "上一轮真实想过的内容")
	body := []byte(`{"model":"deepseek-flash","thinking":{"type":"enabled"},` +
		`"messages":[{"role":"assistant","content":[` +
		`{"type":"tool_use","id":"t-oa-0001","name":"Read","input":{}}]}]}`)

	out, _, err := reasoning{}.Apply(body, &special.Request{Model: "deepseek-flash",
		Provider: "smt-deepseek", BaseURL: "https://gw.example.com/v1",
		Path: "/chat/completions"})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if strings.Contains(string(out), `"type":"thinking"`) {
		t.Fatalf("往 OpenAI 方言的 content[] 里塞了 thinking 块:\n%s", out)
	}
	if !strings.Contains(string(out), `"reasoning_content"`) {
		t.Fatalf("reasoning_content 该照补:\n%s", out)
	}
}

func TestDeepSeekToolLoopMigrationIsScopedAndRebased(t *testing.T) {
	ds := reasoning{}
	candidate := &special.Request{
		Model: "deepseek-flash", Provider: "smt-deepseek",
		BaseURL: "https://gw.example.com/v1", Path: "/messages",
	}
	if needed, _ := ds.NeedsToolLoopRebase("ark", "ark-code-latest", candidate); !needed {
		t.Fatal("DeepSeek 接手 Ark tool loop 应要求 rebase")
	}
	if needed, _ := ds.NeedsToolLoopRebase(
		"smt-deepseek", "deepseek-flash", candidate); needed {
		t.Fatal("DeepSeek 续自己的 tool loop 不该 rebase")
	}
	ark := &special.Request{Model: "ark-code-latest", Provider: "ark",
		BaseURL: "https://ark.example.com", Path: "/messages"}
	if needed, _ := ds.NeedsToolLoopRebase("smt-deepseek", "deepseek-flash", ark); needed {
		t.Fatal("deepseek special 不该约束迁出到 Ark")
	}

	body := []byte(`{"model":"deepseek-flash","messages":[` +
		`{"role":"assistant","content":[{"type":"tool_use","id":"t1"}]},` +
		`{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"ok"}]}` +
		`]}`)
	out, note, err := ds.RebaseToolLoop(body, candidate)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), toolLoopRebasePrompt) {
		t.Fatalf("没有追加 rebase 指令:\n%s", out)
	}
	if !strings.Contains(note, "lossily rebuilt a foreign tool loop") {
		t.Fatalf("rebase 没有明确回报: %q", note)
	}
}

// TestTailShapeRepairOnToolResultOnlyTail 锁住第 4 手：尾部只有 tool_result 时补指令。
//
// 为什么这一手非留不可（2026-09-18 删过一次又恢复的教训）：删掉之后日志里**看不到
// 400**——上游 400 之后自动沿链转移到下一个 provider，客户端拿到的是 200。
// 「看起来没坏」实际是「每一发都悄悄降级到别的模型」。
func TestTailShapeRepairOnToolResultOnlyTail(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{"user 轮收尾", `{"thinking":{"type":"adaptive"},"messages":[` +
			`{"role":"user","content":[{"type":"text","text":"开始吧"}]},` +
			`{"role":"assistant","content":[{"type":"thinking","thinking":"想一下"},{"type":"tool_use","id":"t1","name":"Bash","input":{}}]},` +
			`{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"ok"}]}]}`},
		{"user 轮之后还有 system 插话", `{"thinking":{"type":"adaptive"},"messages":[` +
			`{"role":"user","content":[{"type":"text","text":"开始吧"}]},` +
			`{"role":"assistant","content":[{"type":"thinking","thinking":"想一下"},{"type":"tool_use","id":"t1","name":"Bash","input":{}}]},` +
			`{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"ok"}]},` +
			`{"role":"system","content":[{"type":"text","text":"The user sent a new message while you were working: keep going."}]}]}`},
		{"并行工具轮：两个 tool_result", `{"thinking":{"type":"adaptive"},"messages":[` +
			`{"role":"user","content":[{"type":"text","text":"开始吧"}]},` +
			`{"role":"assistant","content":[{"type":"tool_use","id":"t1"},{"type":"tool_use","id":"t2"}]},` +
			`{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"a"},{"type":"tool_result","tool_use_id":"t2","content":"b"}]}]}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, notes, err := reasoning{}.Apply([]byte(tt.body), claudeReq("deepseek-flash"))
			if err != nil {
				t.Fatalf("Apply 报错: %v", err)
			}
			s := string(out)
			if !strings.Contains(s, `"text":`+mustJSON(toolLoopRebasePrompt)) {
				t.Fatalf("尾部没有补上 continue 指令:\n%s", s)
			}
			// 原内容一个字节都不能丢：tool_result 还在，历史消息没被动。
			if !strings.Contains(s, `"tool_use_id":"t1"`) ||
				!strings.Contains(s, `"text":"开始吧"`) {
				t.Fatalf("补尾部指令时改动了已有内容:\n%s", s)
			}
			// 指令必须落在**最后一条 user 消息**里，而不是数组末尾：
			// 尾随的 system 插话必须还排在它后面（上游不认 system 里的指令）。
			if i := strings.Index(s, mustJSON(toolLoopRebasePrompt)); i >= 0 {
				if k := strings.Index(s, "while you were working"); k >= 0 &&
					strings.Index(s, mustJSON(toolLoopRebasePrompt)) > k {
					t.Fatalf("continue 指令跑到了尾随 system 插话后面（上游不认 system 里的指令）:\n%s", s)
				}
			}
			if !containsNote(notes, "trailing user turn holds only tool_result") {
				t.Fatalf("没有回报 notes（不静默是硬要求）: %v", notes)
			}
		})
	}
}

// TestTailShapeLeavesNormalTailsAlone：这些尾部本来就能过，一个字节都不许动。
//
// 判据是「content[] 里**全是** tool_result 块」，不是「没有 text 块」——后者太宽，
// 会把下面 [image] / [tool_result, image] 这两种实测 3/3 放行的尾部也改掉
// （打真实 smt-deepseek/deepseek-flash，3/3）。
func TestTailShapeLeavesNormalTailsAlone(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{"尾部有文字", `{"thinking":{"type":"adaptive"},"messages":[` +
			`{"role":"user","content":[{"type":"text","text":"hi"}]}]}`},
		{"尾部是 assistant（另一条规则在管，追加指令证明没用）", `{"thinking":{"type":"adaptive"},"messages":[` +
			`{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"ok"}]},` +
			`{"role":"assistant","content":[{"type":"text","text":"说完了"}]}]}`},
		{"只有 image 块", `{"thinking":{"type":"adaptive"},"messages":[` +
			`{"role":"user","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"iVBORw0KGgo="}}]}]}`},
		{"tool_result + image", `{"thinking":{"type":"adaptive"},"messages":[` +
			`{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"ok"},` +
			`{"type":"image","source":{"type":"base64","media_type":"image/png","data":"iVBORw0KGgo="}}]}]}`},
		{"tool_result + text（已经能过）", `{"thinking":{"type":"adaptive"},"messages":[` +
			`{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"ok"},` +
			`{"type":"text","text":"continue"}]}]}`},
		{"tool_result-only 的轮不是最后一条 user 轮", `{"thinking":{"type":"adaptive"},"messages":[` +
			`{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"ok"}]},` +
			`{"role":"assistant","content":[{"type":"text","text":"干完了"}]},` +
			`{"role":"user","content":[{"type":"text","text":"再来一个"}]}]}`},
		{"content 是字符串", `{"thinking":{"type":"adaptive"},"messages":[` +
			`{"role":"user","content":"纯文本"}]}`},
		{"OpenAI 方言：尾部是 role:tool", `{"thinking":{"type":"adaptive"},"messages":[` +
			`{"role":"assistant","tool_calls":[{"id":"c1","type":"function","function":{"name":"Bash","arguments":"{}"}}]},` +
			`{"role":"tool","tool_call_id":"c1","content":"ok"}]}`},
		{"没有 messages", `{"thinking":{"type":"adaptive"}}`},
		{"messages 为空", `{"thinking":{"type":"adaptive"},"messages":[]}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, notes, err := reasoning{}.Apply([]byte(tt.body),
				claudeReq("deepseek-flash"))
			if err != nil {
				t.Fatalf("Apply 报错: %v", err)
			}
			// 数出现次数而不是 Contains：fixture 自己就可能带一句 "continue"
			// （那正是「本来就能过的尾部」的样子），Contains 会误报。
			before := strings.Count(tt.body, mustJSON(toolLoopRebasePrompt))
			after := strings.Count(string(out), mustJSON(toolLoopRebasePrompt))
			if after != before {
				t.Fatalf("不该动的尾部被动了（补了 %d 条）:\n%s", after-before, out)
			}
			if containsNote(notes, "trailing user turn holds only tool_result") {
				t.Fatalf("不该动的尾部却报了尾部 note: %v", notes)
			}
		})
	}
}

// TestTailShapeInjectionDependsOnToolCallOrigin 锁住修复判据的第三条（出身）。
//
// docs/06-reasoning.md §1.1 的实测表（每格 3/3，打真实
// smt-deepseek/deepseek-flash）：裸尾 + 历史里全是聚合器自产的 `call_NN_…` →
// 200，不该被改写；混进任何一个别家产的 id（`call_<hex>` / `toolu_…`）→ 400，
// 必须补。这条是「健康请求也被改写」那个 bug 的回归点——判据曾经只有尾部形状
// 与锚点，第三维（出身）漏掉了，于是正常链路也被塞了一句话。
//
// 自产那一族必须**认两位序号**（00/01/…）：一轮里并行发几个 tool call 时聚合器
// 就是按 00、01、… 依次编号的（2026-09-22 真实流量：call_00_ 2917、call_01_ 705、
// call_02_ 16、call_03_ 8、call_04_ 2）。只认 00 会把最常见的双工具轮判成外来，
// 于是「健康链路也被注入」原样复发——下面带序号的几条就是那个回归点。
func TestTailShapeInjectionDependsOnToolCallOrigin(t *testing.T) {
	tests := []struct {
		name   string
		body   string
		inject bool
	}{
		{"全部自产 call_00_ + 裸尾 → 不注入", `{"thinking":{"type":"adaptive"},"messages":[` +
			`{"role":"user","content":[{"type":"text","text":"开始吧"}]},` +
			`{"role":"assistant","content":[{"type":"tool_use","id":"call_00_abc123","name":"Bash","input":{}}]},` +
			`{"role":"user","content":[{"type":"tool_result","tool_use_id":"call_00_abc123","content":"ok"}]}]}`, false},
		{"并行第二轮 call_01_ + 裸尾 → 不注入", `{"thinking":{"type":"adaptive"},"messages":[` +
			`{"role":"user","content":[{"type":"text","text":"开始吧"}]},` +
			`{"role":"assistant","content":[` +
			`{"type":"tool_use","id":"call_00_abc123","name":"Bash","input":{}},` +
			`{"type":"tool_use","id":"call_01_def456","name":"Read","input":{}}]},` +
			`{"role":"user","content":[` +
			`{"type":"tool_result","tool_use_id":"call_00_abc123","content":"ok"},` +
			`{"type":"tool_result","tool_use_id":"call_01_def456","content":"ok"}]}]}`, false},
		{"两位以上序号 call_42_ + 裸尾 → 不注入", `{"thinking":{"type":"adaptive"},"messages":[` +
			`{"role":"user","content":[{"type":"text","text":"开始吧"}]},` +
			`{"role":"assistant","content":[{"type":"tool_use","id":"call_42_abc123","name":"Bash","input":{}}]},` +
			`{"role":"user","content":[{"type":"tool_result","tool_use_id":"call_42_abc123","content":"ok"}]}]}`, false},
		{"序号只有一位 call_0_（不是自产形态）+ 裸尾 → 注入", `{"thinking":{"type":"adaptive"},"messages":[` +
			`{"role":"user","content":[{"type":"text","text":"开始吧"}]},` +
			`{"role":"assistant","content":[{"type":"tool_use","id":"call_0_abc123","name":"Bash","input":{}}]},` +
			`{"role":"user","content":[{"type":"tool_result","tool_use_id":"call_0_abc123","content":"ok"}]}]}`, true},
		{"外来 call_<hex> + 裸尾 → 注入", `{"thinking":{"type":"adaptive"},"messages":[` +
			`{"role":"user","content":[{"type":"text","text":"开始吧"}]},` +
			`{"role":"assistant","content":[{"type":"tool_use","id":"call_faedac561213487ebeea7731","name":"Bash","input":{}}]},` +
			`{"role":"user","content":[{"type":"tool_result","tool_use_id":"call_faedac561213487ebeea7731","content":"ok"}]}]}`, true},
		{"外来 toolu_… + 裸尾 → 注入", `{"thinking":{"type":"adaptive"},"messages":[` +
			`{"role":"user","content":[{"type":"text","text":"开始吧"}]},` +
			`{"role":"assistant","content":[{"type":"tool_use","id":"toolu_01ABCDEF","name":"Bash","input":{}}]},` +
			`{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_01ABCDEF","content":"ok"}]}]}`, true},
		{"自产与外来混着 + 裸尾 → 注入", `{"thinking":{"type":"adaptive"},"messages":[` +
			`{"role":"user","content":[{"type":"text","text":"开始吧"}]},` +
			`{"role":"assistant","content":[{"type":"tool_use","id":"call_00_abc123","name":"Bash","input":{}}]},` +
			`{"role":"user","content":[{"type":"tool_result","tool_use_id":"call_00_abc123","content":"ok"}]},` +
			`{"role":"assistant","content":[{"type":"tool_use","id":"call_faedac561213487ebeea7731","name":"Bash","input":{}}]},` +
			`{"role":"user","content":[{"type":"tool_result","tool_use_id":"call_faedac561213487ebeea7731","content":"ok"}]}]}`, true},
		{"本来就带文字的尾部 → 一律不碰（哪怕 id 是外来的）", `{"thinking":{"type":"adaptive"},"messages":[` +
			`{"role":"assistant","content":[{"type":"tool_use","id":"call_faedac561213487ebeea7731","name":"Bash","input":{}}]},` +
			`{"role":"user","content":[{"type":"tool_result","tool_use_id":"call_faedac561213487ebeea7731","content":"ok"},` +
			`{"type":"text","text":"continue"}]}]}`, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, notes, err := reasoning{}.Apply([]byte(tt.body), claudeReq("deepseek-flash"))
			if err != nil {
				t.Fatalf("Apply 报错: %v", err)
			}
			before := strings.Count(tt.body, mustJSON(toolLoopRebasePrompt))
			after := strings.Count(string(out), mustJSON(toolLoopRebasePrompt))
			if got := after > before; got != tt.inject {
				t.Fatalf("注入 = %v，想要 %v（前 %d 后 %d）:\n%s", got, tt.inject, before, after, out)
			}
			if got := containsNote(notes, "trailing user turn holds only tool_result"); got != tt.inject {
				t.Fatalf("尾部 note = %v，想要 %v: %v", got, tt.inject, notes)
			}
		})
	}
}

// TestTailShapeRepairedWhenThinkingOff：思考关着也照样修。
//
// 这条校验**与思考开关无关**。实测 3/3：同一份 tool_result-only 尾部，顶层写
// thinking:{"type":"disabled"} 且不带 tools，上游还是回
// 「reasoning_content must be passed back」。
func TestTailShapeRepairedWhenThinkingOff(t *testing.T) {
	body := []byte(`{"thinking":{"type":"disabled"},"messages":[` +
		`{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"ok"}]}]}`)
	out, notes, err := reasoning{}.Apply(body, claudeReq("deepseek-flash"))
	if err != nil {
		t.Fatalf("Apply 报错: %v", err)
	}
	if !strings.Contains(string(out), mustJSON(toolLoopRebasePrompt)) {
		t.Fatalf("思考关着时没补尾部指令（实测这条校验不受 thinking 影响）:\n%s", out)
	}
	if !containsNote(notes, "trailing user turn holds only tool_result") {
		t.Fatalf("没有回报 notes: %v", notes)
	}
}

// mustJSON 断言补进去的文本**恰好**是 toolLoopRebasePrompt（不是更长的话）。
// 措辞是这条路最容易失控的地方：它会长住进用户的对话历史。
func mustJSON(s string) string {
	q, err := json.Marshal(s)
	if err != nil {
		panic(err)
	}
	return string(q)
}

func containsNote(notes []string, sub string) bool {
	for _, n := range notes {
		if strings.Contains(n, sub) {
			return true
		}
	}
	return false
}

// TestResponsesTailShapeRepair 锁住第 5 手：Responses 方言（/v1/responses）的
// input[] 以**别家产**的 function_call_output 收尾时，往末尾追加一条普通 user
// 指令。
//
// 依据是 2026-09-22 实测（独立探针直打真实 smt-deepseek/deepseek-flash，每格
// 3/3）：自产 `call_NN_…` 的 call_id + [function_call, function_call_output]
// 收尾 → 200，不该被改写；外来 `call_faedac…` 的 call_id + 同样收尾 → 400
// `The reasoning_text … must be passed back`，同一个 body 末尾补一条普通 user
// 消息 → 200。
func TestResponsesTailShapeRepair(t *testing.T) {
	newBody := func(callID string) string {
		return `{"model":"deepseek-flash","input":[` +
			`{"type":"message","role":"user","content":[{"type":"input_text","text":"go"}]},` +
			`{"type":"function_call","call_id":"` + callID + `","name":"bash","arguments":"{}"},` +
			`{"type":"function_call_output","call_id":"` + callID + `","output":"ok"}]}`
	}
	// 追加项必须逐字是这一条：type/role/content[].type 都是 Responses 方言的
	// 协议字段，正文用 json.Marshal(toolLoopRebasePrompt)。
	injected := `{"type":"message","role":"user","content":[{"type":"input_text","text":` +
		mustJSON(toolLoopRebasePrompt) + `}]}`
	responsesReq := func() *special.Request {
		return &special.Request{Model: "deepseek-flash", Provider: "smt-deepseek",
			BaseURL: "https://gw.example.com/v1", Protocol: "openai",
			Path: "/responses", Agent: "codex"}
	}

	tests := []struct {
		name   string
		callID string
		want   bool
	}{
		{"自产 call_00_… → 不注入", "call_00_abc123", false},
		{"自产 call_01_…（并行第二个）→ 不注入", "call_01_def456", false},
		{"外来 call_<hex> → 注入", "call_faedac561213487ebeea7731", true},
		{"外来 toolu_… → 注入", "toolu_01ABCDEF", true},
		{"序号一位的 call_0_…（认不出）→ 注入", "call_0_abc123", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := newBody(tt.callID)
			out, notes, err := reasoning{}.Apply([]byte(body), responsesReq())
			if err != nil {
				t.Fatalf("Apply 报错: %v", err)
			}
			s := string(out)
			if got := strings.Contains(s, injected); got != tt.want {
				t.Fatalf("注入 = %v，想要 %v:\n%s", got, tt.want, s)
			}
			if !tt.want {
				if s != body {
					t.Fatalf("不该动的 body 被动了:\n%s", s)
				}
				if len(notes) != 0 {
					t.Fatalf("没改动却报了 notes: %v", notes)
				}
				return
			}
			// 必须追加在 input[] 的**末尾**——形状判据看的就是最后一项。
			if strings.Index(s, injected) < strings.Index(s, `"type":"function_call_output"`) {
				t.Fatalf("指令没有追加在 input[] 末尾:\n%s", s)
			}
			if !json.Valid(out) {
				t.Fatalf("改完不是合法 JSON:\n%s", out)
			}
			if !containsNote(notes, "Responses input[] ends on a function_call_output") {
				t.Fatalf("改了东西却没回报第 5 手 notes: %v", notes)
			}
		})
	}
}

// TestResponsesTailShapeLeavesOtherShapesAlone：形状不中就不碰。
func TestResponsesTailShapeLeavesOtherShapesAlone(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{"最后一项不是 function_call_output", `{"model":"deepseek-flash","input":[` +
			`{"type":"function_call","call_id":"call_faedac561213487ebeea7731","name":"bash","arguments":"{}"}]}`},
		{"外来 call_id 但尾随还有 assistant 消息", `{"model":"deepseek-flash","input":[` +
			`{"type":"function_call_output","call_id":"call_faedac561213487ebeea7731","output":"ok"},` +
			`{"type":"message","role":"assistant","content":[{"type":"output_text","text":"done"}]}]}`},
		{"没有 input 数组（messages 方言）", `{"model":"deepseek-flash","messages":[` +
			`{"role":"user","content":[{"type":"text","text":"hi"}]}]}`},
		{"input 为空数组", `{"model":"deepseek-flash","input":[]}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &special.Request{Model: "deepseek-flash", Provider: "smt-deepseek",
				BaseURL: "https://gw.example.com/v1", Protocol: "openai",
				Path: "/responses", Agent: "codex"}
			out, notes, err := reasoning{}.Apply([]byte(tt.body), r)
			if err != nil {
				t.Fatalf("Apply 报错: %v", err)
			}
			if string(out) != tt.body {
				t.Fatalf("不该动的 body 被动了:\n%s", out)
			}
			if containsNote(notes, "Responses input[] ends on a function_call_output") {
				t.Fatalf("形状不中却报了第 5 手 note: %v", notes)
			}
		})
	}
}

// TestImagesSurviveTheDeepSeekPatch：一张带图片的请求，走完 DeepSeek 的 reasoning
// 补丁，图片块与它的 base64 **必须逐字节原样**。
//
// 这就是「newgate 支不支持图片」的定论之一。转发层本身是纯字节直通（只换 model、
// 可选修 tools），不会坏图片；真正可能弄丢图片的是**上游补丁**——它往 assistant
// 的 content[] 里插 thinking 块（见 AppendLastArrayItemArray）。这一条证明那个插入
// 只在指定的 assistant 消息上动手，用户消息里的图片一个字节都不碰。
func TestImagesSurviveTheDeepSeekPatch(t *testing.T) {
	seedCache("t1", "上一轮真实想过的内容")
	const png = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNgYPhfDwAChwGA60e6kgAAAABJRU5ErkJggg=="
	imageBlock := `{"type":"image","source":{"type":"base64","media_type":"image/png","data":"` + png + `"}}`
	body := []byte(`{"model":"deepseek-chat","thinking":{"type":"enabled","budget_tokens":1024},` +
		`"messages":[` +
		`{"role":"user","content":[{"type":"text","text":"看图"},` + imageBlock + `]},` +
		`{"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"Read","input":{"n":1}}]},` +
		`{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"ok"}]}` +
		`]}`)

	out, notes, err := reasoning{}.Apply(body, claudeReq("deepseek-chat"))
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(notes) == 0 {
		t.Fatal("改了东西却没回报 notes")
	}
	if !bytes.Contains(out, []byte(imageBlock)) {
		t.Fatalf("图片块没原样到达上游（或被改写了）:\n%s", out)
	}
	if bytes.Contains(out, []byte(png)) && !bytes.Contains(out, []byte(`"data":"`+png)) {
		t.Fatal("base64 内容被动了")
	}

	// 用户那条消息里的图片必须仍在它原来的地方：补丁只该动 assistant。
	var got struct {
		Messages []struct {
			Role    string            `json:"role"`
			Content []json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("改完不是合法 JSON: %v", err)
	}
	if len(got.Messages[0].Content) != 2 {
		t.Fatalf("用户消息的 content 块数变了（原来 2，现在 %d）——补丁动了不该动的地方", len(got.Messages[0].Content))
	}
	if !strings.Contains(string(got.Messages[0].Content[1]), png) {
		t.Fatal("用户消息里那张图片变了")
	}
}

// TestRebaseToolLoopResponsesDialect：跨上游迁移那一手在 responses 方言里也得成立。
//
// 现场（2026-09-23）：Codex 把对话挂在顶层 input[]、**没有 messages**，于是这条
// 迁移路径上的 AppendLastArrayItemArray 找不到 messages、返回 changed=false——
// 跨上游迁移静默 no-op。而它 no-op 的正是 RebaseToolLoop 存在的唯一理由（迁移到
// DeepSeek 的未闭合 tool loop 是要 400 的，见本文件里那份 A/B）。补的那一支往
// input[] 末尾追加一条普通 user 消息项。
//
// 锁两件事，一件都不能少：
//   - responses 形状真的被改了（不是 no-op），且追加项是**逐字**的方言形状
//     （type/role/content[].type 三处都是 responses 的名字，写成 text 块就是坏请求）；
//   - 没有 input 的形状（如 /v1/models）一个字节都不动，也不报 note。
func TestRebaseToolLoopResponsesDialect(t *testing.T) {
	responsesReq := func() *special.Request {
		return &special.Request{
			InModel: "normal", Tier: "normal", Model: "deepseek-chat",
			Provider: "ds", BaseURL: "https://api.deepseek.com", Protocol: "openai",
			Path: "/responses", Agent: "codex"}
	}

	body := `{"model":"normal","stream":false,"input":[` +
		`{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]},` +
		`{"type":"function_call","call_id":"call_00_abc","name":"exec","arguments":"{}"},` +
		`{"type":"function_call_output","call_id":"call_00_abc","output":"done"}]}`
	out, note, err := reasoning{}.RebaseToolLoop([]byte(body), responsesReq())
	if err != nil {
		t.Fatalf("RebaseToolLoop: %v", err)
	}
	if note == "" {
		t.Fatal("改了请求却一个 note 都没有（不静默是硬要求）")
	}
	if string(out) == body {
		t.Fatal("responses 形状下没动过——迁移是静默 no-op，正是这次要修的")
	}
	var got struct {
		Input []struct {
			Type    string `json:"type"`
			Role    string `json:"role"`
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"input"`
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("产物解不开：%v\n%s", err, out)
	}
	if len(got.Input) != 4 {
		t.Fatalf("input 应当多出一项（3 → 4），得到 %d：%s", len(got.Input), out)
	}
	last := got.Input[3]
	if last.Type != "message" || last.Role != "user" {
		t.Errorf("追加项应当是普通 user 消息，得到 type=%q role=%q", last.Type, last.Role)
	}
	if len(last.Content) != 1 || last.Content[0].Type != "input_text" ||
		last.Content[0].Text != toolLoopRebasePrompt {
		t.Errorf("追加项的 content 形状不对（responses 要 input_text，不是 text）：%+v", last.Content)
	}
	// 老字节一个不许动：前面三项还是原样。
	if !strings.Contains(string(out), `"call_id":"call_00_abc","output":"done"}`) {
		t.Errorf("原有的 function_call_output 被动过了：%s", out)
	}

	// 没有 input 的形状：不碰，也不报。
	other := `{"model":"normal","input_text":"hi"}`
	out2, note2, err := reasoning{}.RebaseToolLoop([]byte(other), responsesReq())
	if err != nil {
		t.Fatalf("RebaseToolLoop(非 responses): %v", err)
	}
	if note2 != "" {
		t.Errorf("没动手却报了 note：%q", note2)
	}
	_ = out2
}
