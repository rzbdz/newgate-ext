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

// UseFile lets the daemon's health table survive a graceful restart. Only
// the daemon writes this file; probe conclusions are submitted by the CLI
// through the control endpoint, avoiding multi-process concurrent overwrites.
//
// The file name stays `health.json` (paths.HealthFile) **deliberately
// unchanged**: the known-good binary used for downgrade rollback reads
// exactly this name, and renaming it means a rollback loses state.
func (b *table) UseFile(path string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.file = path
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		// `{err}` cannot be omitted: Ef puts the underlying error into the args, but **it
		// only shows if the message contains {err}** -- omit it and those "cannot read health.json"
		// scenes degrade to one sentence "cannot read", with the underlying cause lost on the spot.
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
			// Old files have no Rule field: back then there was only one ledger, availability.
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
				// Old files did not store the expiry: infer one from the cooldown of that time. If it cannot be
				// inferred, treat the cooldown as over -- half-open lets a trial through on the next Available.
				r.openUntil = r.openedAt.Add(b.policy.rule(bucketOr(r.bucket)).Cooldown)
			}
			r.reason = s.Reason
			if r.fails < 1 {
				r.fails = 1
			}
		case s.Fails > 0:
			// Previously only the "delisted" count was restored; the failure count below
			// the threshold was lost, so a restart quietly gave every binding one free pass.
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
		// Compatible with old health.json: back then only one score of <=4KB was saved.
		if score.Samples[0] == 0 && s.ScoreMs > 0 {
			score.EWMA[0], score.Samples[0] = float64(s.ScoreMs), 1
		}
		if score.Samples != [4]int{} {
			b.ranker.set(key, score)
		}
	}
	return nil
}

// bucketOr folds BucketNone into availability, used to get "the old file's cooldown base".
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
