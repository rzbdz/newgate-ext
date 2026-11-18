// Package metrics 网关的计数器：请求在路上遇到的每一类「被网关处理过的
// 事」都记一笔——拦截（count_tokens 本地应答、special 改写、分类器改道）、
// 超时（首字节）、转移（链 fallback）、取消。
//
// 为什么需要它：newgate 在路径上做的事越来越多（切档、禁思考、改道、
// count_tokens 兜底、超时换人），每件事单看日志都是个案，合起来才看得见
// 「这条链今天健康吗、谁在被误杀」。调参数（比如非流式首字节 12s 是不是
// 太紧）必须有数据，不能靠感觉（2026-09-09 用户定的方向）。
//
// 只在内存里，随 daemon 重启归零——粒度是「这一程 daemon 的经历」，
// 与 health/quirk/dialect 同一哲学。名字用点分命名空间，别的地方按
// 名字 Inc 就行，不用预注册。
package metrics

import (
	"sort"
	"sync"
)

// Default 网关进程的全局计数器。daemon 里所有热路径共用这一份。
var Default = New()

type counters struct {
	mu sync.Mutex
	m  map[string]uint64
}

// New 独立一份计数器（测试用；生产路径用 Default）。
func New() *counters { return &counters{m: map[string]uint64{}} }

// Inc 记一笔。并发安全；名字不需要预注册。
func (c *counters) Inc(name string) {
	c.mu.Lock()
	c.m[name]++
	c.mu.Unlock()
}

// Add 累加一个数值。
func (c *counters) Add(name string, n uint64) {
	c.mu.Lock()
	c.m[name] += n
	c.mu.Unlock()
}

// Snapshot 按名字排序的快照（显示稳定，好 diff）。
func (c *counters) Snapshot() map[string]uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make(map[string]uint64, len(c.m))
	for k, v := range c.m {
		out[k] = v
	}
	return out
}

// Reset 仅供测试。
func (c *counters) Reset() {
	c.mu.Lock()
	c.m = map[string]uint64{}
	c.mu.Unlock()
}

// SortedKeys 名字有序（Snapshot 的配套显示用）。
func SortedKeys(m map[string]uint64) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
