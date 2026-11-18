package breaker

import (
	"sync"
	"time"

	"github.com/rzbdz/newgate-ext/modules/breaker/status"
)

// ranker 是「这条 binding 现在大概多快」的预测器，与可用性账本分开：
// 可用性关心「能不能用」，排序关心「谁先上」。两者共享 binding 键，但不共享
// 策略——一个 binding 可以既可用又很慢（排到链尾），也可以快但正在熔断
// （由 Available 摘掉，跟排序无关）。
//
// 它有独立的锁：热路径每发请求都要写一个延迟样本，不该和状态机的锁互相排队。
// 锁序永远是 table.mu → ranker.mu；ranker 的方法从不回调 table。
type ranker struct {
	mu     sync.Mutex
	scores map[string]latencyScore
}

type latencyScore struct {
	EWMA    [4]float64
	Samples [4]int
}

func newRanker() *ranker { return &ranker{scores: map[string]latencyScore{}} }

// keys 回报有哪些 binding 被观测过。
func (r *ranker) keys() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, 0, len(r.scores))
	for key := range r.scores {
		out = append(out, key)
	}
	return out
}

// observe 记一个首字节样本。EWMA 让近期表现权重大，同时避免单次抖动把顺序
// 永久改变；第一个样本直接落值，不跟 0 做加权。
func (r *ranker) observe(key string, contextBytes int, ttft time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	bucket := contextBucket(contextBytes)
	s := r.scores[key]
	ms := float64(ttft.Milliseconds())
	if s.Samples[bucket] == 0 {
		s.EWMA[bucket] = ms
	} else {
		const recentWeight = 0.30
		s.EWMA[bucket] = recentWeight*ms + (1-recentWeight)*s.EWMA[bucket]
	}
	s.Samples[bucket]++
	r.scores[key] = s
}

// score 返回当前上下文桶的预测 TTFT（毫秒）。没有同桶样本时取最近的相邻桶
// ——4K 的样本比 128K 的样本更能代表「同样是短请求」的表现。
func (r *ranker) score(key string, contextBytes int) (int, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.scoreLocked(key, contextBytes)
}

func (r *ranker) scoreLocked(key string, contextBytes int) (int, bool) {
	s := r.scores[key]
	bucket := contextBucket(contextBytes)
	if s.Samples[bucket] > 0 {
		return int(s.EWMA[bucket]), true
	}
	for distance := 1; distance < len(s.Samples); distance++ {
		if i := bucket - distance; i >= 0 && s.Samples[i] > 0 {
			return int(s.EWMA[i]), true
		}
		if i := bucket + distance; i < len(s.Samples) && s.Samples[i] > 0 {
			return int(s.EWMA[i]), true
		}
	}
	return 0, false
}

// rank 把延迟档位编码进排序键：流畅最前，未观测居中，可用最后。
// 卡顿/不可用正常已被 Available 摘除；若调用方只拿到分数快照，仍然沉底。
func (r *ranker) rank(key string, contextBytes int) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.rankLocked(key, contextBytes)
}

func (r *ranker) rankLocked(key string, contextBytes int) int {
	ms, known := r.scoreLocked(key, contextBytes)
	if !known {
		return 1_000_000
	}
	// 档位与阈值都来自 breaker/status（**只有一份**）：排序键就是把档位编进
	// 数值里，所以它和 UI 上那句「流畅 / 可用 / 卡顿」读的是同一套数。
	switch status.Grade(ms, true) {
	case status.LatencyFast:
		return ms
	case status.LatencyOK:
		return 2_000_000 + ms
	default:
		return 3_000_000 + ms
	}
}

// set 直接落一个档位（装载用）。
func (r *ranker) set(key string, s latencyScore) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.scores[key] = s
}

// fill 把某个 binding 的排序样本摊进快照行。
func (r *ranker) fill(s *Status, key string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	score := r.scores[key]
	if score.Samples[0] > 0 {
		s.ScoreMs = int(score.EWMA[0])
	}
	for i, n := range score.Samples {
		if n > 0 {
			s.Scores[i] = int(score.EWMA[i])
		}
		s.Buckets[i] = n
		s.Samples += n
	}
}

func contextBucket(n int) int {
	switch {
	case n <= 4*1024:
		return 0
	case n <= 32*1024:
		return 1
	case n <= 128*1024:
		return 2
	default:
		return 3
	}
}
