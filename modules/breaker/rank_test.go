package breaker

import (
	"testing"
	"time"
)

// TestRankOrderIsFluentThenUnknownThenUsable: the sort key must be usable directly as a sort key,
// so the order is entirely encoded by it: fluent first (returns the real ms, always < 1,000,000),
// never observed in the middle (1,000,000), laggy sunk to the bottom (from 2,000,000).
func TestRankOrderIsFluentThenUnknownThenUsable(t *testing.T) {
	b, _ := clocked()
	b.ObserveSuccess("relay", "fast", 1024, 800*time.Millisecond)
	b.ObserveSuccess("relay", "mid", 1024, 5*time.Second)
	b.ObserveSuccess("relay", "slow", 1024, 30*time.Second)

	fast := b.Rank("relay", "fast", 1024)
	unknown := b.Rank("relay", "never-seen", 1024)
	mid := b.Rank("relay", "mid", 1024)
	slow := b.Rank("relay", "slow", 1024)
	if !(fast < unknown && unknown < mid && mid < slow) {
		t.Fatalf("wrong ordering key order: fast=%d unknown=%d mid=%d slow=%d", fast, unknown, mid, slow)
	}
}

// TestRankThresholdsMatchTheScore: the 3000/12000 bucketing thresholds in
// the sort key exist only here -- before 2026-09-17 the CLI's rankFromProxy
// rewrote them, so a daemon threshold change did not reach the CLI.
func TestRankThresholdsMatchTheScore(t *testing.T) {
	b, _ := clocked()
	b.ObserveSuccess("relay", "m", 1024, 2999*time.Millisecond)
	if got, score := b.Rank("relay", "m", 1024), b.Score("relay", "m", 1024); got != score {
		t.Fatalf("the ordering key of the fluent tier should equal the prediction: rank=%d score=%d", got, score)
	}
	b.ObserveSuccess("relay", "n", 1024, 3*time.Second)
	if got, score := b.Rank("relay", "n", 1024), b.Score("relay", "n", 1024); got != 2_000_000+score {
		t.Fatalf("the usable tier should carry a 2,000,000 offset: rank=%d score=%d", got, score)
	}
	b.ObserveSuccess("relay", "o", 1024, 12*time.Second)
	if got, score := b.Rank("relay", "o", 1024), b.Score("relay", "o", 1024); got != 2_000_000+score {
		t.Fatalf("12000ms is exactly the upper bound of the usable tier: rank=%d score=%d", got, score)
	}
	b.ObserveSuccess("relay", "p", 1024, 12001*time.Millisecond)
	if got, score := b.Rank("relay", "p", 1024), b.Score("relay", "p", 1024); got != 3_000_000+score {
		t.Fatalf("over 12000ms should sink to the bottom: rank=%d score=%d", got, score)
	}
}

// TestScoreFallsBackToNearestBucket: a 4K sample represents "also a short
// request" better than a 128K one, so when the bucket is missing it takes
// the nearest neighbor first, instead of treating it as unknown outright.
func TestScoreFallsBackToNearestBucket(t *testing.T) {
	b, _ := clocked()
	b.ObserveSuccess("relay", "m", 1024, 2*time.Second) // 档位 0

	if got := b.Score("relay", "m", 1); got != 2000 {
		t.Fatalf("the same tier should hit directly: %d", got)
	}
	if got := b.Score("relay", "m", 16*1024); got != 2000 {
		t.Fatalf("tier 1 with no sample should fall back to the adjacent tier 0: %d", got)
	}
	if got := b.Score("relay", "m", 300*1024); got != 2000 {
		t.Fatalf("tier 3 with no sample should fall back to the farthest tier 0: %d", got)
	}
	if got := b.Score("relay", "never-seen", 1024); got != 6000 {
		t.Fatalf("never observed should apply the neutral 6s value: %d", got)
	}
}

// TestObserveSuccessIsEWMA: recent samples weigh more, but
// a single jitter does not permanently change the order.
func TestObserveSuccessIsEWMA(t *testing.T) {
	b, _ := clocked()
	b.ObserveSuccess("relay", "m", 16*1024, 4*time.Second)
	if got := b.Score("relay", "m", 16*1024); got != 4000 {
		t.Fatalf("the first sample should land directly: %d", got)
	}
	b.ObserveSuccess("relay", "m", 16*1024, 2*time.Second)
	// 0.30*2000 + 0.70*4000 = 3400
	if got := b.Score("relay", "m", 16*1024); got != 3400 {
		t.Fatalf("wrong EWMA computation: %d", got)
	}
}

func TestContextBucketBoundaries(t *testing.T) {
	tests := []struct {
		bytes int
		want  int
	}{
		{0, 0}, {4 * 1024, 0},
		{4*1024 + 1, 1}, {32 * 1024, 1},
		{32*1024 + 1, 2}, {128 * 1024, 2},
		{128*1024 + 1, 3}, {8 << 20, 3},
	}
	for _, tt := range tests {
		if got := contextBucket(tt.bytes); got != tt.want {
			t.Errorf("contextBucket(%d) = %d, want %d", tt.bytes, got, tt.want)
		}
	}
}

// TestRankIsIndependentOfAvailability: a binding can be usable yet very
// slow (sorted to the chain tail), or fast but currently tripped (delisted
// by Available, unrelated to ordering). They share the key, not the policy.
func TestRankIsIndependentOfAvailability(t *testing.T) {
	b, _ := clocked()
	for i := 0; i < 2; i++ {
		connFail(b, "relay", "m")
	}
	b.ObserveSuccess("relay", "m", 1024, 500*time.Millisecond)

	if b.Available("relay", "m") {
		t.Fatal("the dropped state did not take effect")
	}
	if got := b.Rank("relay", "m", 1024); got != 500 {
		t.Fatalf("dropping must not move the ordering key: %d", got)
	}
	if got := state(t, b, "relay", "m"); got.Rank != 500 || got.ScoreMs != 500 {
		t.Fatalf("the ordering key in the snapshot should come from the ranker: %+v", got)
	}
}
