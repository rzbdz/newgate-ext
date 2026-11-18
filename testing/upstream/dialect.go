package upstream

// This file is the message shapes of two dialects: non-streaming bodies, SSE chunks, and
// DeepSeek thinking mode's strictest form. Each piece names its counterpart in
// mock/fake_upstream.py; read the python first - it is the only truth, this is its stand-in.

import (
	"encoding/json"
	"net/http"
	"time"
)

// strictReasoningError is a verbatim copy of python's STRICT_REASONING_ERR.
func strictReasoningError() map[string]any {
	return map[string]any{
		"type": "error",
		"error": map[string]any{
			"type":    "invalid_request_error",
			"message": StrictReasoningMessage,
		},
	}
}

// strictReasoningViolation replicates python's strict_reasoning_violation; the criterion
// is **tail shape** (rewritten 2026-09-18 from measurement; the old code copied the docs'
// "every assistant must echo non-empty reasoning", which does not hold upstream):
//
//	last role:user content[] non-empty with **all** tool_result blocks -> violation
//
// Measured (real smt-deepseek/deepseek-flash, 3/3 per cell): reasoning_content as real
// text, omitted, empty or placeholder, **changes nothing**; one extra non-empty text
// block in that content[] (even a single space) turns the 400 into a 200.
//
// The error text is still upstream's (it reports "no new instruction this turn" as
// "reasoning not echoed") - text and real cause disagree, which is why shape, not text.
//
// Do not re-add the three disproven gates: has tools, thinking not off, every assistant.
// Measured, thinking:disabled with no tools at all is a 400 just the same.
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

// anthropicMessage is the non-streaming anthropic response (python's do_POST part).
//
// content's order is **semantics**, not layout: thinking block first, then tool_use; the
// thinkcache observer binds reasoning text to tool id through that adjacency (e2e chapter
// 8's backfill assertion relies on it). Do not reorder.
//
// tool_use appears only with tools in the request, id fixed at ToolUseID - fixed so the
// assertion "which tool call the backfill hit" is easy.
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

// chatCompletion is the non-streaming openai response (the last return in python).
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

// streamWords is how the streaming body is split (the ["MOCK","-","STREAM", f" {model}"]).
// Word by word so each chunk is its own write - a proxy that buffers sends nothing to count.
// Joined, the two dialects share one sentence: MOCK-STREAM <model>.
func streamWords(model string) []string {
	return []string{"MOCK", "-", "STREAM", " " + model}
}

// stream emits SSE chunk by chunk with a gap between - verifying the proxy does not
// buffer the whole response.
//
// Anthropic dialect follows Claude Code's real form: thinking block first, plus tool_use
// with tools - the thinkcache observer links reasoning to tool id by adjacency (e2e 8).
func (s *Server) stream(w http.ResponseWriter, r *http.Request, model string, anthropic, slow, tools bool) {
	delay := s.delays(slow)

	header := w.Header()
	header.Set("Content-Type", "text/event-stream")
	header.Set("Cache-Control", "no-cache")
	// Connection: close plus Transfer-Encoding: identity return this response to python's raw
	// byte stream: no Content-Length, no chunked, body bounded by the closed connection. Go
	// adds chunked to HTTP/1.1 responses with no length by default (net/http: "use chunked
	// transfer encoding to avoid closing the connection at EOF"), so the gateway would see a
	// framing the bash e2e never has. With identity, net/http follows the SSE recommendation
	// it quotes in the same comment (no chunks + close at end), so framing matches python
	// byte for byte.
	// Do not delete as redundant: tests still pass, but "how the proxy handles lengthless
	// streaming" quietly loses its coverage.
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

	// pause mimics python's time.sleep(delay). python's client disconnect raises
	// BrokenPipeError and wraps up; here select is on the request context, so the end of the
	// test or a client hang-up leaves no handler stuck in sleep (httptest.Server.Close waits
	// for all in-flight handlers; stuck = "the test hangs 5 seconds at the end").
	// false = time to wrap up.
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

	// The closing [DONE] goes out for both dialects - python does that (anthropic does not
	// send this line, but python does, and the e2e locks behaviour against it; copy it).
	_, _ = w.Write([]byte("data: [DONE]\n\n"))
	if flusher != nil {
		flusher.Flush()
	}
}
