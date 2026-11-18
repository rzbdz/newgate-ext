package upstream

// 这个文件是两个方言的报文形状：非流式响应体、SSE 块序列、以及 DeepSeek 思考
// 模式的最严口径。每段都标了 mock/fake_upstream.py 里对应的那段，
// 改之前先去读 python 版——那边的行为是唯一真相，这里只是它的替身。

import (
	"encoding/json"
	"net/http"
	"time"
)

// strictReasoningError 是 python STRICT_REASONING_ERR 的逐字翻版。
func strictReasoningError() map[string]any {
	return map[string]any{
		"type": "error",
		"error": map[string]any{
			"type":    "invalid_request_error",
			"message": StrictReasoningMessage,
		},
	}
}

// strictReasoningViolation 复刻 python 的 strict_reasoning_violation，判据是
// **尾部形状**（2026-09-18 按实测重写，原来的实现照抄的是官方文档口径——
// 「每条 assistant 都得回传非空推理」——那条口径在真实上游上不成立）：
//
//	最后一条 role:user 消息的 content[] 非空、且里面**全是** tool_result 块 → 违规
//
// 实测依据（打真实 smt-deepseek/deepseek-flash，每格 3/3）：reasoning_content
// 是真实原文、省略、空串还是占位符，对结果**毫无影响**；同一个请求体只在那个
// content[] 里多一个非空 text 块（哪怕只有一个空格）就从 400 变 200。
//
// 报错文案仍是上游那句（它把「你这轮没有新指令」报成了「推理没回传」）——
// 文案与真实原因不一致，正是不能按文案判、只能按形状判的原因。
//
// 复刻时别加回这三个已经被证伪的闸门：带 tools、思考没关、每条 assistant 都查。
// 实测里 thinking:disabled 连 tools 都不带，一样 400。
func strictReasoningViolation(body map[string]any) bool {
	if body == nil {
		return false
	}
	messages, _ := body["messages"].([]any)
	var lastUser map[string]any
	for _, entry := range messages {
		message, ok := entry.(map[string]any)
		if !ok || message["role"] != "user" {
			continue
		}
		lastUser = message
	}
	if lastUser == nil {
		return false
	}
	blocks, ok := lastUser["content"].([]any)
	if !ok || len(blocks) == 0 {
		return false // content 是字符串 = 本来就有文字，放行
	}
	for _, entry := range blocks {
		block, ok := entry.(map[string]any)
		if !ok || block["type"] != "tool_result" {
			return false // 除 tool_result 之外的任何块都算「有指令」
		}
	}
	return true
}

// anthropicMessage 是非流式 anthropic 响应（python 的 do_POST 里那一段）。
//
// content 的顺序是**语义**，不是排版：thinking 块在前、紧跟 tool_use，
// thinkcache 的观察者正是靠这个相邻关系把推理原文和 tool id 绑在一起
// （e2e 第 8 章的回填断言依赖它）。别重排。
//
// tool_use 只在请求带了 tools 时出现，id 固定 ToolUseID——固定才方便断言
// 「回填的那一发认的是哪个 tool 调用」。
func anthropicMessage(model string, tools bool) map[string]any {
	content := []any{
		map[string]any{
			"type":      "thinking",
			"thinking":  ThinkingText,
			"signature": ThinkingSignature,
		},
	}
	if tools {
		content = append(content, map[string]any{
			"type":  "tool_use",
			"id":    ToolUseID,
			"name":  ToolName,
			"input": map[string]any{"path": "x"},
		})
	}
	content = append(content, map[string]any{
		"type": "text",
		"text": "MOCK-OK model=" + model,
	})
	return map[string]any{
		"id":          MessageID,
		"type":        "message",
		"role":        "assistant",
		"model":       model,
		"content":     content,
		"stop_reason": "end_turn",
		"usage":       map[string]any{"input_tokens": 7, "output_tokens": 5},
	}
}

// chatCompletion 是非流式 openai 响应（python 里最后那个 return）。
func chatCompletion(model string) map[string]any {
	return map[string]any{
		"id":     ChatCompletionID,
		"object": "chat.completion",
		"model":  model,
		"choices": []any{map[string]any{
			"index":         0,
			"finish_reason": "stop",
			"message": map[string]any{
				"role":    "assistant",
				"content": "MOCK-OK model=" + model,
			},
		}},
		"usage": map[string]any{"prompt_tokens": 7, "completion_tokens": 5, "total_tokens": 12},
	}
}

// streamWords 是流式正文的切法（python 里那个 ["MOCK","-","STREAM", f" {model}"]）。
// 逐词发是为了让每一块都是独立的一次 write——代理要是攒着不发，块就数不出来。
// 拼起来是两个方言共用的一句话：MOCK-STREAM <model>。
func streamWords(model string) []string {
	return []string{"MOCK", "-", "STREAM", " " + model}
}

// stream 逐块吐一条 SSE，每块之间留间隔——用来验证代理没有把整个响应缓冲下来。
//
// Anthropic 方言按 Claude Code 的真实形态吐块：thinking 块在前、带 tools 时
// 加 tool_use 块——thinkcache 观察者靠这两个块的相邻关系把推理内容和 tool id
// 关联起来（e2e 第 8 章的回填断言依赖它）。
func (s *Server) stream(w http.ResponseWriter, r *http.Request, model string, anthropic, slow, tools bool) {
	delay := s.delays(slow)

	header := w.Header()
	header.Set("Content-Type", "text/event-stream")
	header.Set("Cache-Control", "no-cache")
	// Connection: close 与 Transfer-Encoding: identity 一起，让这条响应回到
	// python 版的裸字节流形态：没有 Content-Length、没有 chunked，体以
	// 「连接关掉」为界。Go 默认会给 HTTP/1.1 的无长度响应加 chunked
	// （net/http 源码里的原话是 "use chunked transfer encoding to avoid
	// closing the connection at EOF"），那会让网关看到 bash e2e 里不存在的
	// 一种分帧。显式写 identity 之后 net/http 走的是它在同一段注释里引的
	// SSE 推荐姿势（不分块 + 响应完关连接），分帧与 python 逐字节一致。
	// 别当成冗余删掉：删了测试照样过，但「代理怎么处理无长度流式」这段
	// 覆盖就悄悄没了。
	header.Set("Connection", "close")
	header.Set("Transfer-Encoding", "identity")
	w.WriteHeader(http.StatusOK)

	flusher, _ := w.(http.Flusher)
	send := func(event map[string]any) bool {
		payload, err := json.Marshal(event)
		if err != nil {
			return false
		}
		if _, err := w.Write(append(append([]byte("data: "), payload...), '\n', '\n')); err != nil {
			return false // 客户端跑了（在途流被掐、测试结束）——安静收摊
		}
		if flusher != nil {
			flusher.Flush() // 不 Flush 就不是流式：net/http 会攒到缓冲满才发
		}
		return true
	}

	// pause 模拟 python 的 time.sleep(delay)。python 那边客户端断了会
	// BrokenPipeError 收摊；这里 select 在请求 context 上，测试结束或客户端
	// 断开时不会留下一个卡在 sleep 里的 handler（httptest.Server.Close 要等
	// 所有在途 handler 退干净，卡住就成了「测试结束挂 5 秒」的那种怪现象）。
	// 返回 false = 该收摊了。
	pause := func() bool {
		if delay <= 0 {
			return r.Context().Err() == nil
		}
		timer := time.NewTimer(delay)
		defer timer.Stop()
		select {
		case <-r.Context().Done():
			return false
		case <-timer.C:
			return true
		}
	}

	if anthropic {
		if !send(map[string]any{
			"type":    "message_start",
			"message": map[string]any{"id": MessageID, "model": model, "content": []any{}},
		}) {
			return
		}
		if !send(map[string]any{
			"type":          "content_block_start",
			"index":         0,
			"content_block": map[string]any{"type": "thinking", "thinking": ""},
		}) {
			return
		}
		if !send(map[string]any{
			"type":  "content_block_delta",
			"index": 0,
			"delta": map[string]any{"type": "thinking_delta", "thinking": ThinkingText},
		}) {
			return
		}
		index := 1
		if tools {
			if !send(map[string]any{
				"type":  "content_block_start",
				"index": index,
				"content_block": map[string]any{
					"type": "tool_use", "id": ToolUseID, "name": ToolName, "input": map[string]any{},
				},
			}) {
				return
			}
			index++
		}
		if !send(map[string]any{
			"type":          "content_block_start",
			"index":         index,
			"content_block": map[string]any{"type": "text", "text": ""},
		}) {
			return
		}
		for _, word := range streamWords(model) {
			if !send(map[string]any{
				"type":  "content_block_delta",
				"index": index,
				"delta": map[string]any{"type": "text_delta", "text": word},
			}) {
				return
			}
			if !pause() {
				return
			}
		}
		_ = send(map[string]any{"type": "message_stop"})
	} else {
		for _, word := range streamWords(model) {
			if !send(map[string]any{
				"object": "chat.completion.chunk",
				"model":  model,
				"choices": []any{map[string]any{
					"index": 0,
					"delta": map[string]any{"content": word},
				}},
			}) {
				return
			}
			if !pause() {
				return
			}
		}
		_ = send(map[string]any{
			"object": "chat.completion.chunk",
			"model":  model,
			"choices": []any{map[string]any{
				"index":         0,
				"delta":         map[string]any{},
				"finish_reason": "stop",
			}},
		})
	}

	// 收尾的 [DONE] 两个方言都发——python 版就是这样的（anthropic 官方并不发
	// 这一行，但 python 版发了，而 e2e 锁的行为正是对着它跑出来的，照搬）。
	_, _ = w.Write([]byte("data: [DONE]\n\n"))
	if flusher != nil {
		flusher.Flush()
	}
}
