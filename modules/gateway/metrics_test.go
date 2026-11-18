package gateway

import (
	"testing"

	"github.com/rzbdz/newgate-ext/modules/breaker/status"
)

func TestModelScoreLineShowsEveryObservedBucket(t *testing.T) {
	status := status.Status{
		Scores:  [4]int{1200, 3400, 0, 9100},
		Buckets: [4]int{2, 3, 0, 1},
		Samples: 6,
		ScoreMs: 1200,
	}
	got := modelScoreLine(status)
	// 断言的是**源语言**渲染出来的那句话（源语言在运行时是恒等路径，见 lib/i18n）。
	// n=1 走单数形式（i18n.N 的单数选择），所以最后一段是 "1 sample"。
	want := "fast · ≤4K 1200ms/2 samples · ≤32K 3400ms/3 samples · >128K 9100ms/1 sample"
	if got != want {
		t.Fatalf("modelScoreLine() = %q, want %q", got, want)
	}
}

func TestModelHealthStateSeparatesLaggyAndUnavailable(t *testing.T) {
	laggy := status.Status{Open: true, Grade: status.ProbeLaggy}
	if got := modelHealthState(laggy); got != healthLaggy {
		t.Fatalf("laggy state = %q", got)
	}
	unavailable := status.Status{Open: true, Grade: status.ProbeUnavailable}
	if got := modelHealthState(unavailable); got != healthUnavailable {
		t.Fatalf("unavailable state = %q", got)
	}
}

func TestSubMillisecondSampleIsStillScored(t *testing.T) {
	status := status.Status{Buckets: [4]int{2}, Samples: 2}
	if got := modelHealthState(status); got != healthFast {
		t.Fatalf("sub-millisecond sample state = %q", got)
	}
	if got := modelScoreLine(status); got != "fast · ≤4K <1ms/2 samples" {
		t.Fatalf("sub-millisecond score = %q", got)
	}
}
