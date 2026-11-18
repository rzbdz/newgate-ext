// Package dialect 记「这个 (provider, model) 听得懂哪些 API 方言」。
//
// 为什么需要它
//
// newgate 的转发层是纯字节直通：客户端发什么方言，就原样发给上游什么
// 方言（claude → anthropic /messages，opencode → openai /chat/completions），
// 不做协议转换。这能成立的前提是**上游听得懂客户端的方言**——聚合器
// （api.rvcompute.com 实测 2026-09）两个方言都收，但 anthropic 方言的
// 私有端点不一定实现：
//
//	POST /v1/messages              → 200（anthropic 方言本体）
//	POST /v1/messages/count_tokens → 404 "Invalid URL"（没实现）
//
// count_tokens 是 Claude Code 的水位条/自动压缩阈值，真值只有上游知道。
// 听得懂就转发拿真值，听不懂退回本地粗估——「听得懂吗」按 (provider,
// model) 记在这里，来源两个，都往同一个注册表里写：
//
//	Mark*()   转发时撞一次 404 / 200，当场学（gate 层面的 probe：
//	          第一个 count_tokens 就是一次探测，代价一个来回）
//	probe 包  newgate probe 主动打一发两种方言 + count_tokens，省掉
//	          那第一次撞墙，也让人在报告里看得见
//
// 注册表本身**只在内存里**，和 quirk 同一哲学：进程重启就从这个包的角度
// 忘了，重新学的代价是一个请求（daemon 会在第一个请求上补学）。
//
// 但有一层持久化，它住在 probe 而不在这里：`newgate probe` 把探明的结果写进
// ~/.config/newgate/probe-capabilities.json（`probe.LoadCachedCapabilities`
// 在 daemon 启动时装回）。所以准确的说法是「这个包不落盘，探明的结论由探针
// 落盘」——2026-09-18 之前这段注释写的是「进程重启就忘了」，而重启后其实装
// 得回来，把它当成事实会得出错误的结论（比如以为每次重启都要重探一轮）。
//
// 正反两面都要记：只记「支持」的话，每个「不支持」的上游每次请求都得
// 再撞一次 404。所以 Cap 既是能力位也是已探明位——Known 里为 0 的位
// 表示「探过了，明确不支持」，和「还没探过」区分开。
package dialect

import (
	"sort"
	"strings"
	"sync"
)

// Cap 一种方言能力。位掩码，一个 (provider, model) 可以同时有几个。
type Cap uint32

const (
	// CapOpenAI /chat/completions（openai 方言）可用。
	CapOpenAI Cap = 1 << iota
	// CapAnthropic /messages（anthropic 方言）可用。
	CapAnthropic
	// CapCountTokens anthropic 方言的 /messages/count_tokens 可用。
	CapCountTokens
)

func (c Cap) String() string {
	var parts []string
	if c&CapOpenAI != 0 {
		parts = append(parts, "openai")
	}
	if c&CapAnthropic != 0 {
		parts = append(parts, "anthropic")
	}
	if c&CapCountTokens != 0 {
		parts = append(parts, "count_tokens")
	}
	return strings.Join(parts, "+")
}

type entry struct {
	supported Cap
	known     Cap // known 里为 0 的位 = 探过了、明确不支持
}

var (
	mu   sync.RWMutex
	caps = map[string]entry{}
)

func key(provider, model string) string { return provider + "/" + model }

// SplitKey 是 key() 的逆：把 "provider/model" 拆回两半。
//
// 在**第一个** '/' 处切，不在最后一个：provider 是配置里的一个扁平键
// （实测 ark / smt-deepseek / kimi …，见 providers.json），而 model 是上游
// 自己的模型 id，**可以含 '/'**（openrouter 那类 `vendor/model` 的写法）。
// 2026-09-18 之前 Snapshot 用的是 LastIndexByte，于是 `a/openrouter/x` 会被
// 报成 provider="a/openrouter" —— 而 probe 的缓存恢复走的是另一份实现在
// 第一个 '/' 处切，两边对同一批键给出不同的 (provider, model)。
func SplitKey(k string) (provider, model string, ok bool) {
	i := strings.IndexByte(k, '/')
	if i <= 0 || i == len(k)-1 {
		return "", "", false
	}
	return k[:i], k[i+1:], true
}

// Mark 记「支持」。返回 true 表示这是新学到的。
func Mark(provider, model string, c Cap) bool {
	return mark(provider, model, c, true)
}

// MarkUnsupported 记「探过了，明确不支持」。之后 Supports 永远回 false
// ——除非上游哪天升级了（那就得重启 daemon 重新探，成本一个请求）。
func MarkUnsupported(provider, model string, c Cap) bool {
	return mark(provider, model, c, false)
}

func mark(provider, model string, c Cap, ok bool) bool {
	k := key(provider, model)
	mu.Lock()
	defer mu.Unlock()
	e := caps[k]
	if ok {
		// 从「不支持」改口成「支持」也是新信息（上游升级了）
		if e.supported&c == c && e.known&c == c {
			return false
		}
		e.supported |= c
		e.known |= c
		caps[k] = e
		return true
	}
	if e.known&c == c {
		return false
	}
	e.known |= c
	e.supported &^= c
	caps[k] = e
	return true
}

// Supports 查某个能力。known=false 表示还没探过——调用方自己定默认
// 行为（count_tokens 的默认是试一发，见 forward 的懒学习）。
func Supports(provider, model string, c Cap) (ok, known bool) {
	mu.RLock()
	defer mu.RUnlock()
	e := caps[key(provider, model)]
	if e.known&c == 0 {
		return false, false
	}
	return e.supported&c != 0, true
}

// Entry 给 newgate status / probe 报告展示。
type Entry struct {
	Provider string
	Model    string
	Supports Cap
	Known    Cap
}

func Snapshot() []Entry {
	mu.RLock()
	defer mu.RUnlock()
	var out []Entry
	for k, e := range caps {
		provider, model, ok := SplitKey(k)
		if !ok {
			continue
		}
		out = append(out, Entry{Provider: provider, Model: model, Supports: e.supported, Known: e.known})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Provider != out[j].Provider {
			return out[i].Provider < out[j].Provider
		}
		return out[i].Model < out[j].Model
	})
	return out
}

// Reset 仅供测试。
func Reset() {
	mu.Lock()
	defer mu.Unlock()
	caps = map[string]entry{}
}
