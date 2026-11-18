package breaker

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	i18n "github.com/rzbdz/newgate/lib/i18n"
)

// UseFile 让 daemon 的健康表跨优雅重启存活。只有 daemon 写这个文件；
// 探活结论由 CLI 通过控制端点提交，避免多进程并发覆盖。
//
// 文件名保持 `health.json`（paths.HealthFile）**刻意不改**：降级回滚用的
// known-good 二进制读的就是这个名字，改名等于回滚即丢状态。
func (b *table) UseFile(path string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.file = path
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		// `{err}` 不能省：Ef 把底层错误塞进实参，但**只有消息里写了 {err}
		// 才会显示出来**——省掉它，那些「读不了 health.json」的现场就只剩
		// 一句「读不了」，底层原因当场丢掉。
		b.loadErr = i18n.Ef(err, "cannot read {path}: {err}", i18n.A{"path": path})
		return b.loadErr
	}
	var entries []Status
	if err := json.Unmarshal(raw, &entries); err != nil {
		b.loadErr = i18n.Ef(err, "cannot parse {path}: {err}", i18n.A{"path": path})
		return b.loadErr
	}
	for _, s := range entries {
		key := bindingKey(s.Provider, s.Model)
		r := &record{
			fails:      s.Fails,
			bucket:     bucketFromName(s.Rule),
			shapeSkips: s.ShapeSkips,
			spared:     s.Spared,
			cooldown:   time.Duration(s.CooldownMs) * time.Millisecond,
		}
		if s.Fails > 0 && r.bucket == BucketNone {
			// 旧文件没有 Rule 字段：那时候只有一条账本，就是可用性。
			r.bucket = BucketAvailability
		}
		switch {
		case s.Open:
			r.openedAt = s.OpenedAt
			if r.openedAt.IsZero() {
				r.openedAt = time.Now().Add(-b.policy.rule(bucketOr(r.bucket)).Cooldown)
			}
			r.openUntil = s.OpenUntil
			if r.openUntil.IsZero() {
				// 旧文件没存到期时刻：按当时的冷却推一次。推不出来就当
				// 冷却已过——半开会在下一次 Available 时放行试探。
				r.openUntil = r.openedAt.Add(b.policy.rule(bucketOr(r.bucket)).Cooldown)
			}
			r.reason = s.Reason
			if r.fails < 1 {
				r.fails = 1
			}
		case s.Fails > 0:
			// 之前只有「已摘牌」的计数会被恢复；连锁未开的失败计数丢了，
			// 于是重启等于偷偷给每条 binding 一次免死金牌。
			r.reason = s.Reason
		}
		if r.fails > 0 || r.shapeSkips > 0 || !r.openedAt.IsZero() {
			b.records[key] = r
		}
		if s.Grade != "" {
			b.probes[key] = probeResult{
				Grade: s.Grade, LatencyMs: s.Latency, CheckedAt: s.Checked,
			}
		}
		score := latencyScore{}
		for i, n := range s.Buckets {
			if n > 0 {
				score.EWMA[i], score.Samples[i] = float64(s.Scores[i]), n
			}
		}
		// 兼容旧 health.json：当时只保存 ≤4KB 的一个分数。
		if score.Samples[0] == 0 && s.ScoreMs > 0 {
			score.EWMA[0], score.Samples[0] = float64(s.ScoreMs), 1
		}
		if score.Samples != [4]int{} {
			b.ranker.set(key, score)
		}
	}
	return nil
}

// bucketOr 把 BucketNone 折成可用性，用于取「旧文件的冷却基准」。
func bucketOr(b Bucket) Bucket {
	if b == BucketNone {
		return BucketAvailability
	}
	return b
}

func bucketFromName(name string) Bucket {
	for _, b := range []Bucket{BucketAvailability, BucketRateLimit, BucketConfig, BucketShape} {
		if b.ruleToken() == name {
			return b
		}
	}
	return BucketNone
}

func (b *table) persistLocked() {
	if b.file == "" {
		return
	}
	keys := b.entryKeysLocked()
	entries := make([]Status, 0, len(keys))
	for key := range keys {
		provider, model := splitBindingKey(key)
		r := b.records[key]
		s := Status{Provider: provider, Model: model}
		if r != nil {
			s.Fails = r.fails
			s.Rule = r.bucket.ruleToken()
			s.ShapeSkips = r.shapeSkips
			s.Spared = r.spared
			s.CooldownMs = r.cooldown.Milliseconds()
			s.Reason = r.reason
			s.State = r.state(b.now())
			if !r.openedAt.IsZero() {
				s.Open = true
				s.OpenedAt = r.openedAt
				s.OpenUntil = r.openUntil
			}
		}
		if p, ok := b.probes[key]; ok {
			s.Grade, s.Latency, s.Checked = p.Grade, p.LatencyMs, p.CheckedAt
		}
		b.ranker.fill(&s, key)
		entries = append(entries, s)
	}
	sortStatuses(entries)
	raw, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		b.reportLocked(fmt.Errorf("encode %s: %w", b.file, err))
		return
	}
	if err := os.MkdirAll(filepath.Dir(b.file), 0o2770); err != nil {
		b.reportLocked(fmt.Errorf("create health directory: %w", err))
		return
	}
	tmp := b.file + ".tmp"
	if err := os.WriteFile(tmp, append(raw, '\n'), 0o660); err != nil {
		b.reportLocked(fmt.Errorf("write %s: %w", tmp, err))
		return
	}
	if err := os.Chmod(tmp, 0o660); err != nil {
		b.reportLocked(fmt.Errorf("chmod %s: %w", tmp, err))
		return
	}
	if err := os.Rename(tmp, b.file); err != nil {
		b.reportLocked(fmt.Errorf("replace %s: %w", b.file, err))
		return
	}
	b.persistedAt = time.Now()
}

func sortStatuses(entries []Status) {
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].Provider != entries[j].Provider {
			return entries[i].Provider < entries[j].Provider
		}
		return entries[i].Model < entries[j].Model
	})
}
