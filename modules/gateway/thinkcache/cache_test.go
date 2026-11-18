package thinkcache

import (
	"strings"
	"testing"
	"time"
)

const reasoning = "先看 \"main.go\"\n再看 src\\a"

// SSE 的事件边界和 TCP 的读边界毫无关系：一段推理完全可能被切在
// `reasoning_` 和 `content` 之间。逐字节喂是最狠的边界测试。
func TestObserverSurvivesArbitraryChunking(t *testing.T) {
	stream := `data: {"choices":[{"delta":{"reasoning_content":"先看 \"main.go\"\n"}}]}` + "\n\n" +
		`data: {"choices":[{"delta":{"reasoning_content":"再看 src\\a"}}]}` + "\n\n" +
		`data: {"choices":[{"delta":{"content":"好的"},"index":0}]}` + "\n\n" +
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_x"}]}}]}` + "\n\n" +
		"data: [DONE]\n\n"

	for _, size := range []int{1, 3, 17, 4096} {
		o := NewObserver()
		for i := 0; i < len(stream); i += size {
			j := i + size
			if j > len(stream) {
				j = len(stream)
			}
			o.Write([]byte(stream[i:j]))
		}
		if got := o.Reasoning(); got != reasoning {
			t.Fatalf("分块 %d 字节时推理内容不对\n want %q\n got  %q", size, reasoning, got)
		}
		keys := o.Keys()
		if len(keys) != 2 || keys[0] != ToolKey("call_x") {
			t.Fatalf("分块 %d 字节时 key 不对: %v", size, keys)
		}
	}
}

// Anthropic 方言：thinking_delta / content_block_start(tool_use)
func TestObserverAnthropicDialect(t *testing.T) {
	o := NewObserver()
	for _, ev := range []string{
		`{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"先看 \"main.go\"\n"}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"再看 src\\a"}}`,
		`{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_9","name":"Read"}}`,
	} {
		o.Write([]byte("event: x\ndata: " + ev + "\n\n"))
	}
	if got := o.Reasoning(); got != reasoning {
		t.Fatalf("want %q, got %q", reasoning, got)
	}
	if k := o.Keys(); len(k) == 0 || k[0] != ToolKey("toolu_9") {
		t.Fatalf("key 不对: %v", k)
	}
}

// 响应侧挂的 key 和请求侧找的 key 必须严格对应，改一边就得改另一边。
func TestKeysMirrorBetweenResponseAndRequest(t *testing.T) {
	cases := []struct {
		name     string
		respTool string
		respText string
		reqItem  string
	}{
		{"OpenAI 方言 tool_call", "call_x", "",
			`{"role":"assistant","content":"","tool_calls":[{"id":"call_x"}]}`},
		{"Anthropic 方言 tool_use", "toolu_9", "",
			`{"role":"assistant","content":[{"type":"tool_use","id":"toolu_9"}]}`},
		{"纯文本轮（OpenAI）", "", "好的，我看完了",
			`{"role":"assistant","content":"好的，我看完了"}`},
		{"纯文本轮（Anthropic）", "", "好的，我看完了",
			`{"role":"assistant","content":[{"type":"text","text":"好的，我看完了"}]}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := New(1<<20, time.Hour)
			o := NewObserver()
			o.reason.WriteString(reasoning)
			o.text.WriteString(tc.respText)
			if tc.respTool != "" {
				o.addTool(tc.respTool)
			}
			if n, _ := o.Commit(c); n == 0 {
				t.Fatal("没存进去")
			}
			blob, ok := c.Lookup([]byte(tc.reqItem))
			if !ok {
				t.Fatalf("请求侧找不回来。响应侧 key=%v，请求侧 key=%v",
					o.Keys(), KeysForAssistantMessage([]byte(tc.reqItem)))
			}
			if string(blob) != reasoning {
				t.Fatalf("内容不一致: %q", blob)
			}
		})
	}
}

// 一轮多个 tool_call：认出任意一个 id 都要能找回同一段推理。
func TestAnyToolIDFindsTheSameReasoning(t *testing.T) {
	c := New(1<<20, time.Hour)
	c.Put([]string{ToolKey("a"), ToolKey("b"), ToolKey("c")}, []byte(reasoning))
	for _, id := range []string{"a", "b", "c"} {
		item := `{"role":"assistant","tool_calls":[{"id":"` + id + `"}]}`
		if blob, ok := c.Lookup([]byte(item)); !ok || string(blob) != reasoning {
			t.Fatalf("id=%s 找不回来", id)
		}
	}
}

func TestTTLAndByteCap(t *testing.T) {
	c := New(1<<20, 20*time.Millisecond)
	c.Put([]string{"k"}, []byte(reasoning))
	if _, ok := c.Get("k"); !ok {
		t.Fatal("刚存进去就没了")
	}
	time.Sleep(40 * time.Millisecond)
	if _, ok := c.Get("k"); ok {
		t.Fatal("过了 TTL 还在")
	}

	// 字节封顶：塞超量后总字节数不许越界，最早的被淘汰
	small := New(300, time.Hour)
	blob := []byte(strings.Repeat("x", 100))
	for _, k := range []string{"a", "b", "c", "d", "e"} {
		small.Put([]string{k}, blob)
	}
	st := small.Stats()
	if st.Bytes > st.MaxBytes {
		t.Fatalf("超出上限: %d > %d", st.Bytes, st.MaxBytes)
	}
	if _, ok := small.Get("a"); ok {
		t.Fatal("最早的没被淘汰")
	}
	if _, ok := small.Get("e"); !ok {
		t.Fatal("最新的被淘汰了")
	}
}

// 没有能对上号的 key 就别存——存了也永远找不回来，白占地方。
func TestNoKeysNoStore(t *testing.T) {
	c := New(1<<20, time.Hour)
	o := NewObserver()
	o.reason.WriteString(reasoning)
	if n, k := o.Commit(c); n != 0 || k != 0 {
		t.Fatalf("没有 key 也存了: %d 字节 / %d key", n, k)
	}
	if c.Stats().Entries != 0 {
		t.Fatal("缓存里多了东西")
	}
}

func TestContinuationOriginOnlyWhileToolLoopIsOpen(t *testing.T) {
	c := New(1<<20, time.Hour)
	origin := Origin{Profile: "ark", Provider: "ark", Model: "ark-code-latest"}
	c.PutOrigin([]string{"call_a", "call_b"}, origin)

	anthropic := []byte(`{"messages":[
			{"role":"assistant","content":[{"type":"tool_use","id":"call_a"}]},
			{"role":"user","content":[{"type":"tool_result","tool_use_id":"call_a"}]}
		]}`)
	if got, ok := c.ContinuationOrigin(anthropic); !ok || got != origin {
		t.Fatalf("Anthropic tool loop 来源错误: ok=%v got=%+v", ok, got)
	}

	openAI := []byte(`{"messages":[
			{"role":"assistant","tool_calls":[{"id":"call_b"}]},
			{"role":"tool","tool_call_id":"call_b","content":"ok"}
		]}`)
	if got, ok := c.ContinuationOrigin(openAI); !ok || got != origin {
		t.Fatalf("OpenAI tool loop 来源错误: ok=%v got=%+v", ok, got)
	}

	newTurn := []byte(`{"messages":[
			{"role":"tool","tool_call_id":"call_b","content":"ok"},
			{"role":"user","content":"new question"}
		]}`)
	if _, ok := c.ContinuationOrigin(newTurn); ok {
		t.Fatal("普通 user 新回合不该继续锁定旧 provider")
	}

	// Responses 方言（Codex 走的那条）：对话在顶层 input[]，工具输出是
	// function_call_output 项。少了这一支的话，Codex 的 tool loop 在这条判据
	// 眼里根本不算未闭合——跨上游迁移永远不触发，而 DeepSeek 接手别家未闭合的
	// reasoning/tool 状态是要 400 的。
	responses := []byte(`{"model":"normal","input":[
			{"type":"function_call","call_id":"call_b","name":"exec","arguments":"{}"},
			{"type":"function_call_output","call_id":"call_b","output":"ok"}
		]}`)
	if got, ok := c.ContinuationOrigin(responses); !ok || got != origin {
		t.Fatalf("Responses tool loop 来源错误: ok=%v got=%+v", ok, got)
	}

	// 并行调用：末尾连着两条输出，两条都算这一轮。
	parallel := []byte(`{"input":[
			{"type":"function_call_output","call_id":"call_a","output":"1"},
			{"type":"function_call_output","call_id":"call_b","output":"2"}
		]}`)
	if _, ok := c.ContinuationOrigin(parallel); !ok {
		t.Fatal("连着两条 function_call_output 仍是未闭合的一轮")
	}

	// 尾部已经不是 function_call_output → 这一轮闭合了（客户端在等新指令）。
	responsesClosed := []byte(`{"input":[
			{"type":"function_call_output","call_id":"call_b","output":"ok"},
			{"type":"message","role":"user","content":[{"type":"input_text","text":"接着做"}]}
		]}`)
	if _, ok := c.ContinuationOrigin(responsesClosed); ok {
		t.Fatal("尾部是普通消息时不该继续锁定旧 provider")
	}
}

// 非流式响应也要能观测到。
func TestObserveBody(t *testing.T) {
	o := NewObserver()
	o.ObserveBody([]byte(`{"choices":[{"message":{"role":"assistant",` +
		`"reasoning_content":"先看 \"main.go\"\n再看 src\\a","content":"好",` +
		`"tool_calls":[{"id":"call_z"}]}}]}`))
	if got := o.Reasoning(); got != reasoning {
		t.Fatalf("want %q got %q", reasoning, got)
	}
	if k := o.Keys(); len(k) == 0 || k[0] != ToolKey("call_z") {
		t.Fatalf("key 不对: %v", k)
	}
}

// Responses 方言（Codex 走的那条）也要能被观测到。
//
// 这条链上曾经有一处静默的断点（2026-09-23）：sseChunk 的 `delta` 声明成了对象，
// 而 `response.reasoning_text.delta` 的 delta 是**字符串**——每一个推理增量事件都
// 让整个 chunk 反序列化失败，于是 Codex 那一路一个推理字节都记不下。它的表现与
// 「上游真没给推理」完全一样（推理 0 字节），只有 Wire 那行的坏块计数能分辨。
//
// 顺带钉住去重：同一段推理在增量事件与 output_item.done 里各出现一次，收两遍
// 下一轮补回去的就是重复内容。
func TestObserverResponsesDialect(t *testing.T) {
	stream := `event: response.created` + "\n" + `data: {"type":"response.created"}` + "\n\n" +
		`event: response.output_item.added` + "\n" +
		`data: {"type":"response.output_item.added","output_index":0,"item":{"type":"reasoning","id":"rs_1"}}` + "\n\n" +
		`event: response.reasoning_text.delta` + "\n" +
		`data: {"type":"response.reasoning_text.delta","output_index":0,"item_id":"rs_1","delta":"先看 \"main.go\"\n"}` + "\n\n" +
		`event: response.reasoning_text.delta` + "\n" +
		`data: {"type":"response.reasoning_text.delta","output_index":0,"item_id":"rs_1","delta":"再看 src\\a"}` + "\n\n" +
		// 收尾那条带着**同一份**完整原文：不能重复计入。
		`event: response.output_item.done` + "\n" +
		`data: {"type":"response.output_item.done","output_index":0,"item":{"type":"reasoning","id":"rs_1","content":[{"type":"reasoning_text","text":"先看 \"main.go\"\n再看 src\\a"}]}}` + "\n\n" +
		`event: response.output_item.done` + "\n" +
		`data: {"type":"response.output_item.done","output_index":1,"item":{"type":"function_call","id":"item_1","call_id":"call_00_abc","name":"exec","arguments":"{}"}}` + "\n\n"

	o := NewObserver()
	// 分块喂：事件边界与读边界无关这条约束对方言一视同仁。
	for i := 0; i < len(stream); i += 7 {
		j := i + 7
		if j > len(stream) {
			j = len(stream)
		}
		o.Write([]byte(stream[i:j]))
	}
	if o.badChunks != 0 {
		t.Fatalf("有 %d 个块解析不了——delta 又被声明成了对象？", o.badChunks)
	}
	if got := o.Reasoning(); got != reasoning {
		t.Fatalf("推理内容不对（重复计入或漏收）\n want %q\n got  %q", reasoning, got)
	}
	keys := o.Keys()
	if len(keys) == 0 || keys[0] != ToolKey("call_00_abc") {
		t.Fatalf("key 该挂在 call_id 上（不是 item id）：%v", keys)
	}
	if !strings.Contains(o.Wire(), "responses") {
		t.Fatalf("Wire 该认出这是 responses 方言: %s", o.Wire())
	}
}

// 非流式的 Responses 响应（一个 response 对象，与流式收尾同形状）。
func TestObserveResponsesBody(t *testing.T) {
	o := NewObserver()
	o.ObserveBody([]byte(`{"id":"resp_1","object":"response","output":[` +
		`{"type":"reasoning","id":"rs_1","content":[{"type":"reasoning_text","text":"先看 \"main.go\"\n再看 src\\a"}]},` +
		`{"type":"function_call","id":"item_1","call_id":"call_00_abc","name":"exec","arguments":"{}"}]}`))
	if got := o.Reasoning(); got != reasoning {
		t.Fatalf("want %q got %q", reasoning, got)
	}
	if k := o.Keys(); len(k) == 0 || k[0] != ToolKey("call_00_abc") {
		t.Fatalf("key 不对: %v", k)
	}
}

// Responses 的 summary 不是「要求回传的那份原文」，一个字节都不该收。
//
// 现场：上游的 reasoning 项同时带 content[]（原文）与 summary[]（给人看的摘要）。
// 拿摘要当原文补回去就是编内容——正是 st-reasoning.go 那条「不编」要防的事。
func TestObserverResponsesIgnoresSummary(t *testing.T) {
	o := NewObserver()
	o.ObserveBody([]byte(`{"output":[{"type":"reasoning","id":"rs_1",` +
		`"summary":[{"type":"summary_text","text":"一段摘要"}],"content":[]}]}`))
	if got := o.Reasoning(); got != "" {
		t.Fatalf("summary 不该被当成推理原文，实际收了 %q", got)
	}
}

// TestObserverNotesStreamFailure 钉住「上游把失败塞在流里」这件事被抓到了。
//
// 为什么它必须存在：Responses 方言在 stream:true 时先 200 开流，再把失败塞进
// 事件流（response.failed）。这条流只有 Observer 在读，所以「上游拒了这一发」
// 这个事实**只能**从这里问出来——它接不上，数据面那条 learnQuirks（只看
// status>=400）就永远学不到，用户看到的是「必现、过一会儿自己好」。2026-09-22
// 报的那句「stream disconnected before completion」正是这个形态。
func TestObserverNotesStreamFailure(t *testing.T) {
	o := NewObserver()
	if _, failed := o.StreamFailure(); failed {
		t.Fatal("正常流不该报失败")
	}
	o.Write([]byte(`event: response.created` + "\n" +
		`data: {"type":"response.created","response":{"id":"resp_1","status":"in_progress"}}` + "\n\n"))
	o.Write([]byte(`event: response.failed` + "\n" +
		`data: {"type":"response.failed","response":{"id":"resp_1","status":"failed","output":[],` +
		`"error":{"code":"invalid_request_error","message":"该模型始终思考，不支持关闭思考；请使用 low、high 或 max。"}}}` + "\n\n"))
	msg, failed := o.StreamFailure()
	if !failed {
		t.Fatal("流里那条 response.failed 没被记下来")
	}
	// 原文一字不改：它是 quirk 签名的**匹配输入**（judged by bytes.Contains），
	// 改一个字节就等于这条签名永远匹配不上。
	if !strings.Contains(msg, "始终思考") {
		t.Fatalf("原文丢了：%q", msg)
	}
}

// TestObserverStreamFailureTakesTheFirstOnly 只记**第一条**。
//
// 一条流里报两次失败时，第一次那句才是失败的原因，后面那些是它的回声（上游
// 有时会补一条 aborted/错误摘要）。取第一条也意味着这件事不会被后面的正常
// 事件覆盖掉。
func TestObserverStreamFailureTakesTheFirstOnly(t *testing.T) {
	o := NewObserver()
	for _, m := range []string{"第一次的原因", "后面的回声"} {
		o.Write([]byte(`data: {"type":"response.failed","response":{"error":{"message":"` + m + `"}}}` + "\n\n"))
	}
	if msg, _ := o.StreamFailure(); msg != "第一次的原因" {
		t.Fatalf("该留第一条，实际 %q", msg)
	}
}

// TestObserverStreamFailureWithoutResponseObject 认另一种信封。
//
// 实测见过两种：失败那句话有的在 `response.error.message` 里，有的只带一个
// 顶层的 `error` 对象。**「上游拒了这一发」这件事本身**比那句话重要——取不到
// 原文时留一句兜底的，但不能当没发生。
func TestObserverStreamFailureWithoutResponseObject(t *testing.T) {
	o := NewObserver()
	o.Write([]byte(`data: {"type":"response.failed","error":{"code":"server_error","message":"upstream exploded"}}` + "\n\n"))
	msg, failed := o.StreamFailure()
	if !failed {
		t.Fatal("带顶层 error 的变体没被认出来")
	}
	if !strings.Contains(msg, "upstream exploded") {
		t.Fatalf("兜底那句该带上原文，实际 %q", msg)
	}
}
