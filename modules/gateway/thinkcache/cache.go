// Package thinkcache 帮客户端兜住它自己丢掉的推理内容。
//
// 为什么必须有这一层
//
// DeepSeek 的思考模式把推理内容当**对话状态**：请求一旦带了 tools，
// 历史里每条 assistant 消息都必须把当轮的推理内容原样带回来，哪怕那一轮
// 没做 tool call，否则 400。而且文档写明**必须逐字**——截断或摘要在某些
// 版本上同样被拒。
//
// 问题是客户端做不到。Claude Code 这类客户端对非官方端点会主动剥掉
// thinking 块（它假定只有官方签名的块能回传），opencode 那边则是把
// assistant 消息序列化进上下文时丢掉了 reasoning_content 这个它不认识的
// 字段。两边都不是配置能改的。
//
// 补空串能骗过「字段在不在」的检查，但那是**更坏**的结果：模型下一轮读到
// 自己上一轮「什么都没想」，推理质量直接塌掉。所以唯一正确的做法是网关
// 自己把上游吐出来的推理内容记住，下一轮原样插回去。
//
// 边界
//
//	只在内存里。推理内容就是对话内容，落盘是隐私决策，不该由我们静默替
//	用户做。进程重启就没了，那时调用方只能退回自己的占位策略（见
//	modules/deepseek/st-reasoning.go 的 reasoningFallback）——那一轮的推理
//	确实丢了，调用方必须在日志里说清楚，不能装作补上了。
//
//	不打日志。任何路径都不打内容本身，只打字节数和 key 的前缀。
//
//	有上限。按总字节封顶 + TTL 双重淘汰，绝不让它无限长。
package thinkcache

import (
	"container/list"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"sync"
	"time"
)

// Cache 一个按字节数封顶、带 TTL 的 LRU。可选挂一个落盘冷层（disk），
// 热层 miss 时到冷层找、命中再提升回热层——这就是「三级缓存」的二级：
// 客户端自己带回的 thinking 块是第一级（见 st-deepseek 的 pickReasoning），
// 内存是第二级，落盘是第三级。
type Cache struct {
	mu       sync.Mutex
	ll       *list.List // 队头 = 最近用过
	items    map[string]*list.Element
	bytes    int64
	maxBytes int64
	ttl      time.Duration
	disk     *DiskStore  // 可选落盘冷层（nil = 纯内存）
	onErr    func(error) // 冷层/解析失败的出口，见 SetErrorHandler

	hits, misses, puts, evictions uint64
	// unparsable 记「请求里那条 assistant 消息 / 它的 content 解析不出来」的次数
	// （见 KeysForAssistantMessage）。它和 misses 在当前实现里结果一样（都没
	// 命中），但**原因和处置完全不同**：miss 是缓存里真没有（去看上游与本进程
	// 的生命周期），unparsable 是客户端发来的 JSON 形态我们不认识（去看客户端）。
	// 两者在日志里都只能看到「补了空串」，所以要分开计数，否则那条线索是断的。
	unparsable uint64
}

type entry struct {
	key  string
	blob []byte
	at   time.Time
}

// Default 全局实例。64MB / 8 小时：实测一个整天长会话的推理内容量级是
// 1~2MB（见 dump 抽样：thinking 块 4~11KB/条），64MB 能并排放几十个会话；
// 8h 覆盖一个工作日，重启前的会话在当天内都能找回。
var Default = New(64<<20, 8*time.Hour)

// New 创建独立内存缓存；测试和非默认实例可据此选择自己的容量与 TTL。
func New(maxBytes int64, ttl time.Duration) *Cache {
	return &Cache{
		ll:       list.New(),
		items:    make(map[string]*list.Element),
		maxBytes: maxBytes,
		ttl:      ttl,
	}
}

// fail 报告一次「不改变这次请求的结果，但会改变下一次行为」的失败（冷层写不
// 进去、请求里的消息解析不出来）。没装出口就丢弃——这一层是 best-effort 的。
func (c *Cache) fail(err error) {
	if c == nil || err == nil {
		return
	}
	c.mu.Lock()
	fn := c.onErr
	c.mu.Unlock()
	if fn != nil {
		fn(err)
	}
}

// SetErrorHandler 注入落盘/解析失败的出口（照 breaker.SetErrorHandler 的形状）。
//
// 为什么需要它：这一层是 best-effort 的，失败不影响正确性——但它决定
// 「重启之后还能不能找回上一进程的推理内容」，而那正是用户会来问的事
// （CLAUDE.md §3.1：冷层坏掉时重启后第一发大请求会因为思维链补不上被上游
// 400 「must be passed back」）。2026-09-18 之前压实的每一步失败都是
// `return` / `continue`，一次都没留下痕迹，唯一的现象是「重启后推理全没了」。
//
// daemon 在 AttachDisk 之后调它（serve.go）；不装就等于丢弃，与以前一致。
func SetErrorHandler(fn func(error)) {
	Default.mu.Lock()
	Default.onErr = fn
	d := Default.disk
	Default.mu.Unlock()
	if d != nil {
		d.mu.Lock()
		d.onErr = fn
		d.mu.Unlock()
	}
}

// AttachDisk 给全局 Default 挂上落盘冷层（三级缓存的第三级），并把盘上
// 已有的记录回灌进内存热层——这是 daemon 重启后找回上一进程推理内容的
// 唯一入口。路径在 ~/.config/newgate/thinkcache.bin（见 paths.ThinkCacheFile）。
//
// 失败（没权限 / 磁盘不可写）就返回 err，调用方决定降级：继续纯内存，
// 重启后那些轮次的推理原文就找不回来了——落盘永远不该反过来把代理搞挂。
func AttachDisk(path string, maxBytes int64) error {
	d, err := openDisk(path, maxBytes, Default.ttl)
	if err != nil {
		return err
	}
	Default.mu.Lock()
	Default.disk = d
	Default.mu.Unlock()

	now := time.Now()
	for k := range d.snapshot() {
		if blob, ok := d.get(k); ok {
			Default.putMem([]string{k}, blob, now)
		}
	}
	return nil
}

// Put 把一段推理内容挂到若干个 key 上。
//
// 一轮回答可能同时给出多个 tool_call，客户端下一轮回传时我们只要认出**任
// 意一个** id 就能找回这段推理，所以同一段内容挂多个 key。多挂的 key 各自
// 计一次字节数（宁可提前淘汰，也不做复杂的引用计数）。
func (c *Cache) Put(keys []string, blob []byte) {
	if len(keys) == 0 || len(blob) == 0 {
		return
	}
	// 拷一份：调用方的 buffer 会被复用
	cp := make([]byte, len(blob))
	copy(cp, blob)

	now := time.Now()
	c.putMem(keys, cp, now)
	if c.disk != nil {
		c.disk.appendKeys(keys, cp, now)
	}
}

// putMem 只写内存热层。回灌磁盘冷层时走这里，避免「回灌又写回磁盘」的死循环。
func (c *Cache) putMem(keys []string, cp []byte, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, k := range keys {
		if k == "" {
			continue
		}
		if el, ok := c.items[k]; ok {
			e := el.Value.(*entry)
			c.bytes += int64(len(cp)) - int64(len(e.blob))
			e.blob, e.at = cp, now
			c.ll.MoveToFront(el)
			continue
		}
		el := c.ll.PushFront(&entry{key: k, blob: cp, at: now})
		c.items[k] = el
		c.bytes += int64(len(cp))
	}
	c.puts++
	c.evictLocked(now)
}

// Get 取回推理内容。过期的当没有；热层 miss 时落到冷层找，命中再提升回热层。
func (c *Cache) Get(key string) ([]byte, bool) {
	if key == "" {
		return nil, false
	}
	c.mu.Lock()
	el, ok := c.items[key]
	if ok {
		e := el.Value.(*entry)
		if c.ttl > 0 && time.Since(e.at) > c.ttl {
			c.removeLocked(el)
			c.misses++
		} else {
			c.ll.MoveToFront(el)
			c.hits++
			c.mu.Unlock()
			return e.blob, true
		}
	}
	c.misses++
	hasDisk := c.disk != nil
	c.mu.Unlock()

	if !hasDisk {
		return nil, false
	}
	blob, ok := c.disk.get(key)
	if !ok {
		return nil, false
	}
	// 冷层命中：提升回热层（不再落盘——它本来就在盘上）
	c.putMem([]string{key}, blob, time.Now())
	return blob, true
}

func (c *Cache) evictLocked(now time.Time) {
	// 先清过期的（从队尾开始，那边最旧）
	if c.ttl > 0 {
		for el := c.ll.Back(); el != nil; {
			prev := el.Prev()
			if now.Sub(el.Value.(*entry).at) <= c.ttl {
				break
			}
			c.removeLocked(el)
			c.evictions++
			el = prev
		}
	}
	for c.bytes > c.maxBytes {
		el := c.ll.Back()
		if el == nil {
			return
		}
		c.removeLocked(el)
		c.evictions++
	}
}

func (c *Cache) removeLocked(el *list.Element) {
	e := el.Value.(*entry)
	c.ll.Remove(el)
	delete(c.items, e.key)
	c.bytes -= int64(len(e.blob))
}

// Stats 给 newgate status / st 展示。不含内容。
type Stats struct {
	Entries   int
	Bytes     int64
	MaxBytes  int64
	Hits      uint64
	Misses    uint64
	Puts      uint64
	Evictions uint64
	// Unparsable 请求里解析不出来的 assistant 消息条数（见 Lookup）。与 Misses
	// 分开报：miss 的处置是查缓存和上游，这个的处置是查客户端发来的 JSON。
	Unparsable uint64
}

func (c *Cache) Stats() Stats {
	c.mu.Lock()
	defer c.mu.Unlock()
	return Stats{
		Entries: c.ll.Len(), Bytes: c.bytes, MaxBytes: c.maxBytes,
		Hits: c.hits, Misses: c.misses, Puts: c.puts, Evictions: c.evictions,
		Unparsable: c.unparsable,
	}
}

// ToolKey 按 tool_call id 做 key。
//
// 这是最稳的 key：id 由上游生成，客户端**必须**逐字回传（tool_result 要靠
// 它对上号），所以它在两次请求里必然一致。而「请求带了 tools」正是上游要求
// 回传推理内容的唯一场景，两者刚好重合。
func ToolKey(id string) string {
	if id == "" {
		return ""
	}
	return "tool:" + id
}

const originKeyPrefix = "origin:"

// Origin 是产生某个 tool call 的实际上游。思考模式下，assistant 的 reasoning
// 与整组 tool calls 属于同一个未闭合状态；在 tool_result 回来之前不能把它们
// 搬给另一家上游。只存路由身份，不存请求或响应正文。
type Origin struct {
	Profile  string `json:"profile,omitempty"`
	Provider string `json:"provider"`
	Model    string `json:"model"`
}

func originKey(id string) string {
	if id == "" {
		return ""
	}
	return originKeyPrefix + id
}

// PutOrigin 把响应里的 tool call 绑定到实际产生它的上游。复用 Cache/DiskStore
// 的 TTL、容量与落盘机制，因此 daemon 重启后未闭合 tool loop 仍能回到原上游。
func (c *Cache) PutOrigin(toolIDs []string, origin Origin) {
	if c == nil || origin.Provider == "" || origin.Model == "" {
		return
	}
	blob, err := json.Marshal(origin)
	if err != nil {
		return
	}
	var keys []string
	for _, id := range toolIDs {
		if k := originKey(id); k != "" {
			keys = append(keys, k)
		}
	}
	c.Put(keys, blob)
}

// GetOrigin 取回一个 tool call 的产生上游。
func (c *Cache) GetOrigin(toolID string) (Origin, bool) {
	var origin Origin
	blob, ok := c.Get(originKey(toolID))
	if !ok || json.Unmarshal(blob, &origin) != nil ||
		origin.Provider == "" || origin.Model == "" {
		return Origin{}, false
	}
	return origin, true
}

// TextKey 按 assistant 正文的哈希做 key，用于**没有** tool_call 的那些轮。
//
// 文档说得很明确：一旦请求带了 tools，历史里每条 assistant 消息都要带推理
// 内容，哪怕那一轮没做 tool call。所以纯文本轮也得能找回来，而它身上唯一
// 会被客户端原样带回的东西就是正文。
//
// 弱点：客户端要是改写过正文（截断、加前缀）就 miss。接受它——主路径
// （带 tool_call 的轮）走 ToolKey，那条是稳的。
func TextKey(text string) string {
	t := strings.TrimSpace(text)
	if t == "" {
		return ""
	}
	h := sha256.Sum256([]byte(t))
	return "text:" + hex.EncodeToString(h[:12])
}
