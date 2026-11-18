package thinkcache

import (
	"bytes"
	"encoding/json"
	"strings"

	i18n "github.com/rzbdz/newgate/lib/i18n"
)

// Observer 旁路观测一条响应，攒出「这一轮的推理内容」和它对应的 key。
//
// 严格只读：喂进来的字节原样是转发给客户端的那一份，Observer 不碰它、
// 也不产生任何回写。转发的字节保真是硬约束（docs/01-product.md），这里只是搭个便车看
// 一眼。看错了最坏的结果是这轮没缓存上，下一轮那条消息补不上原文（插件
// 不编内容，见 modules/deepseek/st-reasoning.go 文件头）。
//
// 用法：
//
//	ob := thinkcache.NewObserver()
//	… 每次 Read 到就 ob.Write(buf[:n]) …
//	ob.Commit(thinkcache.Default)   // 流结束时
type Observer struct {
	pending bytes.Buffer // SSE 还没凑齐一个事件的尾巴
	reason  bytes.Buffer // 累积的推理内容
	text    bytes.Buffer // 累积的正文（纯文本轮的 key 靠它）
	toolIDs []string
	seenID  map[string]bool
	// responsesDeltaItem 记「这个 Responses 推理项已经吐过增量了」。见
	// feedResponses 的说明：增量与 output_item.done 里那份完整原文是同内容，
	// 两条都收会让下一轮补回去的推理翻倍。
	responsesDeltaItem map[string]bool

	// streamFailure / streamFailed 记「上游在**事件流里**报的那次失败」。
	//
	// 为什么观测者要管这件事：Responses 方言的上游把失败塞在流里
	// （`response.failed`），HTTP 状态码是 **200**。于是数据面那条 learnQuirks
	// （只看 status >= 400）永远学不到，证据也没人留——用户看到的是「必现、
	// 过一会儿自己好」，而日志里连一句「上游拒了这一发」都没有。这条流只有
	// 这里在读，所以这件事只能在这里做（见 StreamFailure 与防抖那个判据）。
	streamFailure string
	streamFailed  bool

	// ---- 计数（2026-09-18 加，只为日志取证）----
	//
	// 存在的理由：线上出现「上游没给推理内容」时，只看结果（推理字节数 0）
	// 分不清是我们**没解析出来**还是上游**真没给**。用户的原话是「你他妈给我
	// trace 出来」。这几个计数就是那份 trace：哪一方言、收了多少块、其中
	// 多少个推理增量 / 正文增量、几个块连 JSON 都解析不了。
	chunks       int
	badChunks    int
	reasonDeltas int
	textDeltas   int
	sawOpenAI    bool
	sawAnthropic bool
	sawResponses bool
}

func NewObserver() *Observer {
	return &Observer{seenID: map[string]bool{}, responsesDeltaItem: map[string]bool{}}
}

// Write 喂原始响应字节。调用方可以任意分块——SSE 的事件边界由这里自己找。
func (o *Observer) Write(p []byte) {
	if o == nil {
		return
	}
	// 上限保护：一条流的推理内容再长也不该吃掉几百 MB。
	// 超了就停止累积——宁可这轮没缓存上，也不能把网关吃爆。
	if o.reason.Len() > 8<<20 {
		return
	}
	o.pending.Write(p)
	for {
		s := o.pending.Bytes()
		i := bytes.Index(s, []byte("\n\n"))
		if i < 0 {
			// 半个事件也可能很长（一个 chunk 里塞了大段推理），
			// 但没有边界就不能解析。给个上限防止无限攒。
			if o.pending.Len() > 4<<20 {
				o.pending.Reset()
			}
			return
		}
		event := make([]byte, i)
		copy(event, s[:i])
		o.pending.Next(i + 2)
		o.feedEvent(event)
	}
}

func (o *Observer) feedEvent(event []byte) {
	for _, line := range bytes.Split(event, []byte("\n")) {
		line = bytes.TrimSuffix(line, []byte("\r"))
		if !bytes.HasPrefix(line, []byte("data:")) {
			continue // event: / id: / 注释行，与我们无关
		}
		payload := bytes.TrimSpace(line[len("data:"):])
		if len(payload) == 0 || bytes.Equal(payload, []byte("[DONE]")) {
			continue
		}
		o.feedChunk(payload)
	}
}

// sseChunk 同时容纳**三种**方言。用 json.Unmarshal 是安全的：这里只读、不回写，
// 不存在「往返把请求改坏」的问题（那条规矩管的是转发的字节）。
//
// `Delta` 必须是 RawMessage，不能声明成对象：三种方言里它有两种形状——OpenAI 与
// Anthropic 是对象，Responses（`response.reasoning_text.delta`）是**字符串**。
// 写成对象的话，Responses 的每一个增量事件都会让**整个 chunk** 反序列化失败
// （badChunks++ 然后整块丢掉），症状是 Codex 那一路一个推理字节都观测不到——
// 而日志上它看起来与「上游真没给推理」一模一样（Wire 那行只会报一堆坏块）。
// 现场：2026-09-23，Codex 走 /responses 时 thinkcache 一条都记不下。
type sseChunk struct {
	// OpenAI 方言
	Choices []struct {
		Delta struct {
			Content          string `json:"content"`
			ReasoningContent string `json:"reasoning_content"`
			Reasoning        string `json:"reasoning"` // 部分聚合网关用这个名字
			ToolCalls        []struct {
				ID string `json:"id"`
			} `json:"tool_calls"`
		} `json:"delta"`
	} `json:"choices"`

	// Anthropic 与 Responses 共用：类型名都在顶层。
	Type string `json:"type"`

	// Anthropic 方言：delta 是对象
	Delta json.RawMessage `json:"delta"`

	ContentBlock struct {
		Type     string `json:"type"`
		ID       string `json:"id"`
		Thinking string `json:"thinking"`
		Text     string `json:"text"`
	} `json:"content_block"`

	// Responses 方言：增量事件用 item_id 指认自己属于哪个推理项（`id` 是 item
	// 身份，`output_index` 只是位置——改写器按后者记账，观测按前者去重）。
	ItemID string `json:"item_id"`

	// Responses 方言：output_item.added / .done 带一个 item。
	Item responsesItem `json:"item"`

	// Responses 方言：收尾的 response.completed 带着整份 response 对象
	// （非流式那条路走的是同一个形状，见 ObserveBody）。
	Response responsesBody `json:"response"`

	// 顶层那个错误对象：`response.failed` 的变体里有的是这一种（不带 response）。
	Error json.RawMessage `json:"error"`
}

// responsesItem 是 Responses 的一个 output 项。
//
// `id` 与 `call_id` 是两个不同的东西：前者是 item 身份（推理项的 id 长这样），
// 后者是**工具调用**的 id——也就是客户端下一轮会放进 function_call_output 的
// 那个值，是 thinkcache 挂 key / 记出身的键。
type responsesItem struct {
	Type    string `json:"type"`
	ID      string `json:"id"`
	CallID  string `json:"call_id"`
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
}

// anthropicDelta 是 Anthropic 的 delta 对象。
type anthropicDelta struct {
	Type     string `json:"type"`
	Text     string `json:"text"`
	Thinking string `json:"thinking"`
}

// responsesBody 是 Responses 的 response 对象（流式收尾与非流式共用形状）。
type responsesBody struct {
	Output []responsesItem `json:"output"`
	// 失败那一支带的错误（`response.failed` 的 response.error）。
	Error struct {
		Message string `json:"message"`
	} `json:"error"`
}

// responsesReasoning 把一个 Responses output 项里的推理原文拼出来。
//
// 形状是 `{"type":"reasoning","content":[{"type":"reasoning_text","text":…}]}`。
// **summary 那一路（`summary_text`）不算**：它是给人看的摘要，不是上游要求回传的
// 那份原文，拿它补回去就是编内容（见 modules/deepseek/st-reasoning.go 的「不编」）。
func responsesReasoning(item responsesItem) string {
	if item.Type != "reasoning" {
		return ""
	}
	var b strings.Builder
	for _, c := range item.Content {
		if c.Type == "reasoning_text" {
			b.WriteString(c.Text)
		}
	}
	return b.String()
}

func (o *Observer) feedChunk(payload []byte) {
	var c sseChunk
	if json.Unmarshal(payload, &c) != nil {
		o.badChunks++
		return // 不认识的形状：跳过，不是错
	}
	o.chunks++

	for _, ch := range c.Choices { // OpenAI 方言
		d := ch.Delta
		o.sawOpenAI = true
		if d.ReasoningContent != "" {
			o.reasonDeltas++
			o.reason.WriteString(d.ReasoningContent)
		} else if d.Reasoning != "" {
			o.reasonDeltas++
			o.reason.WriteString(d.Reasoning)
		}
		if d.Content != "" {
			o.textDeltas++
			o.text.WriteString(d.Content)
		}
		for _, tc := range d.ToolCalls {
			o.addTool(tc.ID)
		}
	}

	switch c.Type { // Anthropic 方言
	case "content_block_start":
		o.sawAnthropic = true
		if c.ContentBlock.Type == "tool_use" {
			o.addTool(c.ContentBlock.ID)
		}
		if c.ContentBlock.Thinking != "" {
			o.reasonDeltas++
			o.reason.WriteString(c.ContentBlock.Thinking)
		}
		if c.ContentBlock.Text != "" {
			o.textDeltas++
			o.text.WriteString(c.ContentBlock.Text)
		}
	case "content_block_delta":
		o.sawAnthropic = true
		var d anthropicDelta
		if len(c.Delta) > 0 && json.Unmarshal(c.Delta, &d) == nil {
			switch d.Type {
			case "thinking_delta":
				o.reasonDeltas++
				o.reason.WriteString(d.Thinking)
			case "text_delta":
				o.textDeltas++
				o.text.WriteString(d.Text)
			}
		}
	}

	o.feedResponses(c)
	o.noteStreamFailure(c)
}

// noteStreamFailure 记下「上游在流里报的这次失败」。
//
// 两种形状都认：`response.failed` 事件本身带的 `response.error.message`，以及
// 只带一个错误对象的变体。取不到原文时留一句兜底的话——**有失败这件事本身**
// 比那句话更重要（那句话是给日志和判据用的，而「上游拒了」这个事实才是数据面
// 要拿去学的东西）。
func (o *Observer) noteStreamFailure(c sseChunk) {
	if c.Type != "response.failed" || o.streamFailed {
		return
	}
	o.streamFailed = true
	o.streamFailure = strings.TrimSpace(c.Response.Error.Message)
	if o.streamFailure == "" {
		o.streamFailure = strings.TrimSpace(string(c.Error))
	}
}

// feedResponses 观测 Responses 方言（Codex 走的那条）的事件。
//
// 事件名是 `response.<x>` 三段式，与 Anthropic 的 `content_block_*` 在同一个
// `type` 字段上不重叠，所以单独一支，不把上面那些 case 撑长。
//
// 推理文本有**两条**来路，必须去重：
//
//	response.reasoning_text.delta   增量（正常路径）
//	response.output_item.done       item.content[] 里那份**完整**原文
//
// 两条都收就会把同一段推理记两遍，而下一轮补回去的就是重复内容（上游要的是原文，
// 多一份就变了）。所以按 item id 记「这个 item 吐过增量没有」：吐过就以增量为准，
// 没吐过才用 done 里那份完整的。`response.completed` 里那份 response.output 只在
// **整条流一个推理字节都没收到**时兜底——正常流到那儿时早就记完了。
func (o *Observer) feedResponses(c sseChunk) {
	if !strings.HasPrefix(c.Type, "response.") {
		return
	}
	o.sawResponses = true
	switch c.Type {
	case "response.reasoning_text.delta":
		var s string
		if len(c.Delta) > 0 && json.Unmarshal(c.Delta, &s) == nil && s != "" {
			o.reasonDeltas++
			if c.ItemID != "" {
				o.responsesDeltaItem[c.ItemID] = true
			}
			o.reason.WriteString(s)
		}
	case "response.output_text.delta":
		var s string
		if len(c.Delta) > 0 && json.Unmarshal(c.Delta, &s) == nil && s != "" {
			o.textDeltas++
			o.text.WriteString(s)
		}
	case "response.output_item.added", "response.output_item.done":
		if c.Item.Type == "function_call" && c.Item.CallID != "" {
			o.addTool(c.Item.CallID)
		}
		if c.Item.Type == "reasoning" && !o.responsesDeltaItem[c.Item.ID] {
			if s := responsesReasoning(c.Item); s != "" {
				o.reasonDeltas++
				o.reason.WriteString(s)
			}
		}
	case "response.completed":
		if o.reason.Len() == 0 {
			o.absorbResponsesBody(c.Response)
		}
		for _, item := range c.Response.Output {
			if item.Type == "function_call" && item.CallID != "" {
				o.addTool(item.CallID)
			}
		}
	}
}

// absorbResponsesBody 把一份 Responses response 对象里的推理与工具调用吸收进来。
func (o *Observer) absorbResponsesBody(r responsesBody) {
	for _, item := range r.Output {
		if s := responsesReasoning(item); s != "" {
			o.reasonDeltas++
			o.reason.WriteString(s)
		}
		if item.Type == "function_call" && item.CallID != "" {
			o.addTool(item.CallID)
		}
	}
}

func (o *Observer) addTool(id string) {
	if id == "" || o.seenID[id] {
		return
	}
	o.seenID[id] = true
	o.toolIDs = append(o.toolIDs, id)
}

// ObserveBody 非流式响应：整个 body 一次看完。
func (o *Observer) ObserveBody(body []byte) {
	var r struct {
		// OpenAI
		Choices []struct {
			Message struct {
				Content          string `json:"content"`
				ReasoningContent string `json:"reasoning_content"`
				Reasoning        string `json:"reasoning"`
				ToolCalls        []struct {
					ID string `json:"id"`
				} `json:"tool_calls"`
			} `json:"message"`
		} `json:"choices"`
		// Anthropic
		Content []struct {
			Type     string `json:"type"`
			Text     string `json:"text"`
			Thinking string `json:"thinking"`
			ID       string `json:"id"`
		} `json:"content"`
		// Responses（非流式那份就是一个 response 对象，与流式收尾同一形状）
		Output []responsesItem `json:"output"`
	}
	if json.Unmarshal(body, &r) != nil {
		return
	}
	for _, ch := range r.Choices {
		m := ch.Message
		if m.ReasoningContent != "" {
			o.reason.WriteString(m.ReasoningContent)
		} else if m.Reasoning != "" {
			o.reason.WriteString(m.Reasoning)
		}
		o.text.WriteString(m.Content)
		for _, tc := range m.ToolCalls {
			o.addTool(tc.ID)
		}
	}
	for _, b := range r.Content {
		switch b.Type {
		case "thinking":
			o.reason.WriteString(b.Thinking)
		case "text":
			o.text.WriteString(b.Text)
		case "tool_use":
			o.addTool(b.ID)
		}
	}
	if len(r.Output) > 0 {
		o.sawResponses = true
		o.absorbResponsesBody(responsesBody{Output: r.Output})
	}
}

// StreamFailure 报这一发上游在**流里**报的那个失败：原文 + 有没有。
func (o *Observer) StreamFailure() (string, bool) { return o.streamFailure, o.streamFailed }

// Wire 汇报这条响应**线路层**看到了什么，供日志取证。
//
// 一行说清「上游到底发了什么」：哪一方言、多少块、其中多少个推理增量。
// 与 ReasoningBytes 配对看就能立刻分辨两件事：
//
//	Wire: 200 块 / 推理增量 0 / 正文增量 40   ⇒ 上游真的没发推理（请求侧的事）
//	Wire: 0 块（全是坏块）/ …                  ⇒ 我们没解析出来（网关侧的事）
//
// 不打任何内容，只有计数和布尔。
func (o *Observer) Wire() string {
	if o == nil {
		return i18n.T("(no observer)", nil)
	}
	dialect := i18n.T("unknown dialect", nil)
	switch {
	case o.sawAnthropic && o.sawOpenAI:
		dialect = i18n.T("mixed dialects", nil)
	case o.sawAnthropic:
		dialect = "anthropic"
	case o.sawOpenAI:
		dialect = "openai"
	case o.sawResponses:
		dialect = "responses"
	}
	if o.badChunks > 0 {
		return i18n.T("{dialect} {chunks} chunks / {reasoning} reasoning deltas / {text} text deltas / **{bad} chunks unparsable**",
			i18n.A{"dialect": dialect, "chunks": o.chunks, "reasoning": o.reasonDeltas,
				"text": o.textDeltas, "bad": o.badChunks})
	}
	return i18n.T("{dialect} {chunks} chunks / {reasoning} reasoning deltas / {text} text deltas",
		i18n.A{"dialect": dialect, "chunks": o.chunks, "reasoning": o.reasonDeltas,
			"text": o.textDeltas})
}

// Keys 这一轮的推理内容该挂在哪些 key 上。
func (o *Observer) Keys() []string {
	var keys []string
	for _, id := range o.toolIDs {
		keys = append(keys, ToolKey(id))
	}
	if k := TextKey(o.text.String()); k != "" {
		keys = append(keys, k)
	}
	return keys
}

// Commit 入库。返回缓存了多少字节、挂了几个 key；没看到推理内容就是 (0,0)。
// 调用方拿这两个数字写日志——**不要打内容**。
func (o *Observer) Commit(c *Cache) (nbytes, nkeys int) {
	return o.CommitWithOrigin(c, Origin{})
}

// CommitWithOrigin 除推理原文外，还记录 tool call 的实际产生上游。即使这一轮
// 没有 reasoning，也要记 origin：tool loop 的方言状态仍不能安全跨 provider。
func (o *Observer) CommitWithOrigin(c *Cache, origin Origin) (nbytes, nkeys int) {
	if o == nil || c == nil {
		return 0, 0
	}
	c.PutOrigin(o.toolIDs, origin)
	if o.reason.Len() == 0 {
		return 0, 0
	}
	keys := o.Keys()
	if len(keys) == 0 {
		return 0, 0 // 没有能对上号的 key，存了也找不回来
	}
	c.Put(keys, o.reason.Bytes())
	return o.reason.Len(), len(keys)
}

// Reasoning 观测到的推理内容（测试与非流式路径用）。
func (o *Observer) Reasoning() string { return o.reason.String() }

// ReasoningBytes 观测到的推理内容长度，不产生拷贝。
//
// 为什么要有它而不是 len(Reasoning())：调用点在转发循环里、每一条响应都跑，
// 而推理内容动辄上百 KB——为了写一行日志把整段复制成 string 是白花的。
func (o *Observer) ReasoningBytes() int {
	if o == nil {
		return 0
	}
	return o.reason.Len()
}

// ToolCalls 这一轮观测到的 tool call 个数（去重后）。
//
// 给日志用：上游一轮「有 tool call 却一个字节推理都没给」时，下一轮的补丁
// 对这条消息**一条都补不上**（插件不编占位符），而**那是上游本来就没给，
// 不是我们弄丢的**——这两件事的处置完全相反，日志必须能分开（见
// modules/deepseek/st-reasoning.go 的 skipCause）。
func (o *Observer) ToolCalls() int {
	if o == nil {
		return 0
	}
	return len(o.toolIDs)
}

// LooksLikeSSE 粗判一个 Content-Type 是不是事件流。
func LooksLikeSSE(contentType string) bool {
	return strings.Contains(strings.ToLower(contentType), "event-stream")
}
