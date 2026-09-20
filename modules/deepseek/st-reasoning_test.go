package deepseek

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/rzbdz/newgate/go/modules/gateway/special"
	"github.com/rzbdz/newgate/go/modules/gateway/thinkcache"
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
	all := strings.Join(notes, "\n")
	for _, want := range []string{"1 条用了真实的推理原文", "1 条没有原文可补", "nocache", missID} {
		if !strings.Contains(all, want) {
			t.Fatalf("notes 里该有 %q（不静默 + 可反查）:\n%s", want, all)
		}
	}
	if strings.Contains(all, "补 thinking 块：2 条用了真实的推理原文") {
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
	if !strings.Contains(note, "有损重建") {
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
				t.Fatalf("尾部没有补上继续指令:\n%s", s)
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
					t.Fatalf("继续指令跑到了尾随 system 插话后面（上游不认 system 里的指令）:\n%s", s)
				}
			}
			if !containsNote(notes, "尾") {
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
			`{"type":"text","text":"继续"}]}]}`},
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
			// 数出现次数而不是 Contains：fixture 自己就可能带一句「继续」
			// （那正是「本来就能过的尾部」的样子），Contains 会误报。
			before := strings.Count(tt.body, mustJSON(toolLoopRebasePrompt))
			after := strings.Count(string(out), mustJSON(toolLoopRebasePrompt))
			if after != before {
				t.Fatalf("不该动的尾部被动了（补了 %d 条）:\n%s", after-before, out)
			}
			if containsNote(notes, "尾") {
				t.Fatalf("不该动的尾部却报了尾部 note: %v", notes)
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
	if !containsNote(notes, "尾") {
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
