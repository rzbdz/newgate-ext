package breaker

import (
	"testing"
	"time"
)

// TestRankOrderIsFluentThenUnknownThenUsable：排序键要能直接当 sort key 用，
// 所以顺序完全由它自己编码：流畅在最前（返回真实 ms，一定 < 100 万），
// 没观测过的居中（100 万），卡顿的沉底（200 万起）。
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
		t.Fatalf("排序键顺序不对: fast=%d unknown=%d mid=%d slow=%d", fast, unknown, mid, slow)
	}
}

// TestRankThresholdsMatchTheScore：排序键里的 3000/12000 分桶阈值只有这一份
// ——2026-09-17 之前 CLI 的 rankFromProxy 把它们重写了一遍，daemon 改阈值
// CLI 不会跟着变。
func TestRankThresholdsMatchTheScore(t *testing.T) {
	b, _ := clocked()
	b.ObserveSuccess("relay", "m", 1024, 2999*time.Millisecond)
	if got, score := b.Rank("relay", "m", 1024), b.Score("relay", "m", 1024); got != score {
		t.Fatalf("流畅档的排序键应等于预测值: rank=%d score=%d", got, score)
	}
	b.ObserveSuccess("relay", "n", 1024, 3*time.Second)
	if got, score := b.Rank("relay", "n", 1024), b.Score("relay", "n", 1024); got != 2_000_000+score {
		t.Fatalf("可用档应带 200 万偏移: rank=%d score=%d", got, score)
	}
	b.ObserveSuccess("relay", "o", 1024, 12*time.Second)
	if got, score := b.Rank("relay", "o", 1024), b.Score("relay", "o", 1024); got != 2_000_000+score {
		t.Fatalf("12000ms 正好是可用档的上界: rank=%d score=%d", got, score)
	}
	b.ObserveSuccess("relay", "p", 1024, 12001*time.Millisecond)
	if got, score := b.Rank("relay", "p", 1024), b.Score("relay", "p", 1024); got != 3_000_000+score {
		t.Fatalf("超过 12000ms 应沉底: rank=%d score=%d", got, score)
	}
}

// TestScoreFallsBackToNearestBucket：4K 的样本比 128K 的样本更能代表
// 「同样是短请求」的表现，所以缺档时先找最近的邻居，而不是直接当未知。
func TestScoreFallsBackToNearestBucket(t *testing.T) {
	b, _ := clocked()
	b.ObserveSuccess("relay", "m", 1024, 2*time.Second) // 档位 0

	if got := b.Score("relay", "m", 1); got != 2000 {
		t.Fatalf("同档位应直接命中: %d", got)
	}
	if got := b.Score("relay", "m", 16*1024); got != 2000 {
		t.Fatalf("档位 1 缺样本时应回退到相邻的档位 0: %d", got)
	}
	if got := b.Score("relay", "m", 300*1024); got != 2000 {
		t.Fatalf("档位 3 缺样本时应回退到最远的档位 0: %d", got)
	}
	if got := b.Score("relay", "never-seen", 1024); got != 6000 {
		t.Fatalf("完全没观测过应用 6s 中性值: %d", got)
	}
}

// TestObserveSuccessIsEWMA：近期权重大，但单次抖动不会把顺序永久改变。
func TestObserveSuccessIsEWMA(t *testing.T) {
	b, _ := clocked()
	b.ObserveSuccess("relay", "m", 16*1024, 4*time.Second)
	if got := b.Score("relay", "m", 16*1024); got != 4000 {
		t.Fatalf("第一个样本应直接落值: %d", got)
	}
	b.ObserveSuccess("relay", "m", 16*1024, 2*time.Second)
	// 0.30*2000 + 0.70*4000 = 3400
	if got := b.Score("relay", "m", 16*1024); got != 3400 {
		t.Fatalf("EWMA 计算不对: %d", got)
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

// TestRankIsIndependentOfAvailability：一个 binding 可以既可用又很慢（排到
// 链尾），也可以快但正在熔断（由 Available 摘掉，跟排序无关）。两者共享键，
// 不共享策略。
func TestRankIsIndependentOfAvailability(t *testing.T) {
	b, _ := clocked()
	for i := 0; i < 2; i++ {
		connFail(b, "relay", "m")
	}
	b.ObserveSuccess("relay", "m", 1024, 500*time.Millisecond)

	if b.Available("relay", "m") {
		t.Fatal("摘牌状态没生效")
	}
	if got := b.Rank("relay", "m", 1024); got != 500 {
		t.Fatalf("摘牌不应该动排序键: %d", got)
	}
	if got := state(t, b, "relay", "m"); got.Rank != 500 || got.ScoreMs != 500 {
		t.Fatalf("快照里的排序键应来自 ranker: %+v", got)
	}
}
