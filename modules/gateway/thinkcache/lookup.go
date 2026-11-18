package thinkcache

import (
	"encoding/json"
	"fmt"

	i18n "github.com/rzbdz/newgate/lib/i18n"
)

// KeysForAssistantMessage 从**请求里**一条 assistant 消息算出候选 key，
// 顺序即优先级。这是 Observer.Keys() 的镜像：一边在响应里挂 key，一边在
// 下一轮的请求里按同样的规则找回来，两边必须严格对应，改一边就得改另一边。
//
// 两种方言都认（newgate 不做协议转换，客户端发什么方言就原样转发什么方言，
// 所以这里必须两边都能认）：
//
//	OpenAI    {"role":"assistant","content":"…","tool_calls":[{"id":"…"}]}
//	Anthropic {"role":"assistant","content":[{"type":"tool_use","id":"…"},…]}
func KeysForAssistantMessage(item []byte) []string {
	keys, _ := keysForAssistantMessage(item)
	return keys
}

// keysForAssistantMessage 同 KeysForAssistantMessage，但额外回答「这条消息
// （或它的 content）解析出来了吗」。
//
// 为什么要区分：以前三条失败路径（整条消息解析不了、content 是字符串但解析
// 失败、content 是块数组但解析失败）都折叠成同一个结果——空 key 列表，于是
// 调用方看到的是**未命中**。而「未命中」这个结论会把排查引向缓存和上游，
// 真正的原因却是客户端发来的 JSON 形态我们不认识。modules/deepseek 的
// skipCause 存在的唯一理由就是回答「为什么没有 thinking」，它现在有第三类
// 原因需要分辨（见 Cache.unparsable 的说明）。
func keysForAssistantMessage(item []byte) ([]string, bool) {
	var m struct {
		ToolCalls []struct {
			ID string `json:"id"`
		} `json:"tool_calls"`
		Content json.RawMessage `json:"content"`
	}
	if json.Unmarshal(item, &m) != nil {
		return nil, false
	}

	ok := true
	var keys []string
	for _, tc := range m.ToolCalls { // OpenAI 方言
		if k := ToolKey(tc.ID); k != "" {
			keys = append(keys, k)
		}
	}

	text := ""
	if len(m.Content) > 0 {
		switch m.Content[0] {
		case '"': // content 是字符串
			if json.Unmarshal(m.Content, &text) != nil {
				ok = false
			}
		case '[': // content 是块数组（Anthropic 方言）
			var blocks []struct {
				Type string `json:"type"`
				Text string `json:"text"`
				ID   string `json:"id"`
			}
			if json.Unmarshal(m.Content, &blocks) != nil {
				ok = false
				break
			}
			for _, b := range blocks {
				switch b.Type {
				case "tool_use":
					if k := ToolKey(b.ID); k != "" {
						keys = append(keys, k)
					}
				case "text":
					text += b.Text
				}
			}
		}
	}
	if k := TextKey(text); k != "" {
		keys = append(keys, k)
	}
	return keys, ok
}

// Lookup 按候选 key 依次查，命中即返回。
//
// 解析不出来的那一条会记进 Stats().Unparsable 并打一行日志：它在结果上和
// 「未命中」一样（调用方随后补空串），但原因完全不同，而日志里只看得到
// 「补了空串」。不记的话这个分类就永远断了——排查的人会去查缓存和上游，
// 而该看的是客户端发来的 JSON。
func (c *Cache) Lookup(item []byte) ([]byte, bool) {
	keys, parsed := keysForAssistantMessage(item)
	if !parsed {
		c.mu.Lock()
		c.unparsable++
		c.mu.Unlock()
		// 前 200 字节仍按字节截断（%.200s），与加 i18n 之前逐字节相同。
		// 消息必须是**单个字面量**（tools/i18n/scan 只认一个 BasicLit）——拼接
		// 出来的句子扫不到，账本里就会少一条。
		c.fail(i18n.E("thinkcache: cannot parse an assistant message in the request; this round is treated as a miss (empty string filled in) — a JSON shape problem on the client side, not a cache problem (first 200 bytes: {snippet})",
			i18n.A{"snippet": fmt.Sprintf("%.200s", item)}))
	}
	for _, k := range keys {
		if blob, ok := c.Get(k); ok {
			return blob, true
		}
	}
	return nil, false
}

// ContinuationOrigin 判断请求是否正处于未闭合的 tool loop，并找出 tool call
// 的产生上游。只看请求尾部：普通 user 新回合即使历史里有 tool calls，也不应
// 被永久粘住。
//
// 三种方言：
//   - Anthropic：最后一条 user.content[] 含 tool_result
//   - OpenAI：请求末尾连续的 role=tool 消息
//   - Responses（Codex 走的那条）：顶层 input[] 末尾连续的 function_call_output
//
// 为什么 Responses 那一支必须补上（2026-09-23）：少了它，Codex 的 tool loop
// 在这条判据眼里**根本不算未闭合**——于是「这一轮的 tool call 是哪家产的」
// 无处可查，跨上游迁移（special.RebaseToolLoop）永远不被触发，而 DeepSeek
// 接手别家未闭合的 reasoning/tool 状态是要 400 的（见 modules/deepseek/
// st-reasoning.go 的 A/B 实测）。症状与「补丁没生效」一样，但根因在观测侧：
// 请求里那个 call_id 我们从来没记过出身。
//
// 形状以真实流量为准（dump/err-429-req001474.client-sent.json，2026-09-23）：
// 顶层 input[] 里每一项都是 `{"type":"message"|"function_call"|"function_call_output", …}`，
// 工具输出那一项带 `call_id`。
func (c *Cache) ContinuationOrigin(body []byte) (Origin, bool) {
	var req struct {
		Messages []struct {
			Role       string          `json:"role"`
			Content    json.RawMessage `json:"content"`
			ToolCallID string          `json:"tool_call_id"`
		} `json:"messages"`
		// Responses 方言：对话在顶层 input[]，没有 messages。
		Input []struct {
			Type   string `json:"type"`
			CallID string `json:"call_id"`
		} `json:"input"`
	}
	if json.Unmarshal(body, &req) != nil {
		return Origin{}, false
	}

	var ids []string
	switch {
	case len(req.Messages) > 0:
		last := req.Messages[len(req.Messages)-1]
		switch last.Role {
		case "tool": // OpenAI 方言；一次并行调用可能有多条连续 tool 消息
			for i := len(req.Messages) - 1; i >= 0 && req.Messages[i].Role == "tool"; i-- {
				if id := req.Messages[i].ToolCallID; id != "" {
					ids = append(ids, id)
				}
			}
		case "user": // Anthropic 方言；多个 tool_result 在同一个 content[] 里
			if len(last.Content) == 0 || last.Content[0] != '[' {
				return Origin{}, false
			}
			var blocks []struct {
				Type      string `json:"type"`
				ToolUseID string `json:"tool_use_id"`
			}
			if json.Unmarshal(last.Content, &blocks) != nil {
				return Origin{}, false
			}
			for _, b := range blocks {
				if b.Type == "tool_result" && b.ToolUseID != "" {
					ids = append(ids, b.ToolUseID)
				}
			}
		default:
			return Origin{}, false
		}
	case len(req.Input) > 0:
		// Responses 方言：末尾连续的 function_call_output。与 OpenAI 那一支同形
		// ——一次并行调用会有连续几条，而中间夹一条别的（user 消息、function_call）
		// 就说明这一轮已经闭合（客户端在等新指令，不是在等工具结果）。
		for i := len(req.Input) - 1; i >= 0 && req.Input[i].Type == "function_call_output"; i-- {
			if id := req.Input[i].CallID; id != "" {
				ids = append(ids, id)
			}
		}
		if len(ids) == 0 {
			return Origin{}, false
		}
	default:
		return Origin{}, false
	}

	var found Origin
	for _, id := range ids {
		origin, ok := c.GetOrigin(id)
		if !ok {
			continue
		}
		if found.Provider == "" {
			found = origin
			continue
		}
		if found.Provider != origin.Provider || found.Model != origin.Model {
			return Origin{}, false // 同一批 tool results 来源冲突：不猜
		}
	}
	return found, found.Provider != ""
}
