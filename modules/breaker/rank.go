package breaker

import (
	"sync"
	"time"

	"github.com/rzbdz/newgate-ext/modules/breaker/status"
)

// ranker predicts "roughly how fast this binding is now", separate from the availability
// ledger: availability cares about "can it be used", ordering about "who goes first". They
// share the binding key but not the policy -- a binding can be usable yet very slow (sorted to
// the chain tail), or fast but currently tripped (delisted by Available, unrelated to ordering).
//
// It has its own lock: the hot path writes a latency sample on every request,
// and that should not queue against the state machine's lock. Lock order is
// always table.mu -> ranker.mu; ranker's methods never call back into table.
type ranker struct {
	mu     sync.Mutex
	scores map[string]latencyScore
}

type latencyScore struct {
	EWMA    [4]float64
	Samples [4]int
}

func newRanker() *ranker { return &ranker{scores: map[string]latencyScore{}} }

// keys reports which bindings have been observed.
func (r *ranker) keys() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, 0, len(r.scores))
	for key := range r.scores {
		out = append(out, key)
	}
	return out
}

// observe records one time-to-first-byte sample. EWMA gives recent
// performance more weight while avoiding a single jitter permanently changing
// the order; the first sample lands directly, without weighting against 0.
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

// score returns the predicted TTFT (milliseconds) for the current context
// bucket. With no sample in the same bucket it takes the nearest neighboring
// bucket -- a 4K sample represents "also a short request" better than a 128K one.
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

// rank encodes the latency tier into the sort key: fluent first, unobserved in the
// middle, usable last. Laging/unavailable have normally already been delisted by
// Available; if a caller only has a score snapshot, they still sink to the bottom.
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
	// Both the tiers and the thresholds come from breaker/status (**one copy
	// only**): the sort key is just the tier encoded into a number, so it and
	// that "fluent / usable / laggy" line in the UI read the same numbers.
	switch status.Grade(ms, true) {
	case status.LatencyFast:
		return ms
	case status.LatencyOK:
		return 2_000_000 + ms
	default:
		return 3_000_000 + ms
	}
}

// set lands a tier directly (for loading).
func (r *ranker) set(key string, s latencyScore) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.scores[key] = s
}

// fill spreads a binding's ordering sample into the snapshot row.
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
