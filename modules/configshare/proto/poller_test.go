package proto

import (
	"bytes"
	"context"
	"log"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"
)

// pullerFunc lets tests drive the whole loop without a real network.
type pullerFunc func(ctx context.Context, force bool) (Outcome, error)

func (f pullerFunc) Pull(ctx context.Context, force bool) (Outcome, error) { return f(ctx, force) }

func TestShouldLog(t *testing.T) {
	tests := []struct {
		fails     int
		errChange bool
		want      bool
		why       string
	}{
		{1, false, true, "the first cycle must say it - otherwise 'never silent' is an empty promise"},
		{2, false, true, "2nd time"},
		{3, false, false, "suppressed"},
		{4, false, false, "suppressed"},
		{5, false, true, "5th time"},
		{6, false, false, "suppressed"},
		{10, false, true, "10th time"},
		{25, false, true, "25th time"},
		{50, false, true, "50th time"},
		{51, false, false, "after 50, every hundred"},
		{100, false, true, "every 100 times"},
		{101, false, false, "suppressed"},
		{3, true, true, "report at once when the error string changes: a different error is new information"},
		{999, true, true, "same as above, even deep in the count"},
	}
	for _, tc := range tests {
		if got := shouldLog(tc.fails, tc.errChange); got != tc.want {
			t.Fatalf("shouldLog(%d, %v) = %v, want %v (%s)",
				tc.fails, tc.errChange, got, tc.want, tc.why)
		}
	}
}

func TestNextDelayAndJitter(t *testing.T) {
	// rnd=0.5 is the jitter midpoint (factor 1.0), used to verify the backoff series itself.
	tests := []struct {
		interval time.Duration
		fails    int
		want     time.Duration
	}{
		{30 * time.Second, 0, 30 * time.Second}, // 正常节奏 = interval
		{10 * time.Second, 0, 10 * time.Second}, // 间隔与退避是两件事
		{30 * time.Second, 1, 30 * time.Second}, // 退避从基准起
		{30 * time.Second, 2, 60 * time.Second},
		{30 * time.Second, 3, 120 * time.Second},
		{30 * time.Second, 4, 240 * time.Second},
		{30 * time.Second, 5, 300 * time.Second},  // 封顶
		{30 * time.Second, 20, 300 * time.Second}, // 一直封顶
	}
	for _, tc := range tests {
		if got := nextDelay(tc.interval, tc.fails, 0.5); got != tc.want {
			t.Fatalf("nextDelay(%v, %d, 0.5) = %v, want %v", tc.interval, tc.fails, got, tc.want)
		}
	}

	// Jitter must really stay within ±20%, and both directions must be reachable.
	// Why jitter: many machines with the same 30s ticker lock phase at startup, so
	// they all hit the host every 30s with a 30s gap between.
	base := 100 * time.Second
	if got := jitter(base, 0); got != 80*time.Second {
		t.Fatalf("the lower bound should be -20%%, got %v", got)
	}
	if got := jitter(base, 0.5); got != base {
		t.Fatalf("the midpoint should not move, got %v", got)
	}
	if got := jitter(base, 1); got != 120*time.Second {
		t.Fatalf("the upper bound should be +20%%, got %v", got)
	}
}

// TestPollerFirstRoundImmediate pins "do not wait for the first interval".
//
// A restarted daemon should converge at once, not idle 30 seconds while users still see
// the old config - and "no change after restart" is what reads as "the feature broke".
func TestPollerFirstRoundImmediate(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var mu sync.Mutex
		var firstAt time.Time
		rounds := 0
		pl := pullerFunc(func(ctx context.Context, force bool) (Outcome, error) {
			mu.Lock()
			if rounds == 0 {
				firstAt = time.Now()
			}
			rounds++
			mu.Unlock()
			return Outcome{}, nil
		})
		start := time.Now()
		p := runLoop(context.Background(), pl, 30*time.Second, discardLogger(), nil)
		defer p.Stop()

		// Let the fake clock run 1ms: enough for round 1, far from a full interval.
		time.Sleep(time.Millisecond)

		mu.Lock()
		got, at := rounds, firstAt
		mu.Unlock()
		if got < 1 {
			t.Fatal("a round should already have run within 1 ms")
		}
		if at.After(start.Add(time.Millisecond)) {
			t.Fatalf("the first round should happen right at start, but it did at %v", at.Sub(start))
		}
	})
}

// TestPollerBackoffSequence runs the whole backoff series deterministically on a **fake clock**.
//
// Uses testing/synctest (Go 1.25+): the loop runs in a bubble with simulated time, so
// 600 seconds of backoff pass in one call - no sleeps, no flakiness. Commit 0's language
// bump bought this as a side effect.
func TestPollerBackoffSequence(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var mu sync.Mutex
		rounds := 0
		var delays []time.Duration
		pl := pullerFunc(func(ctx context.Context, force bool) (Outcome, error) {
			mu.Lock()
			rounds++
			mu.Unlock()
			return Outcome{Failed: true, ErrText: "cannot reach the host"}, nil
		})
		p := runLoop(context.Background(), pl, 30*time.Second, discardLogger(),
			func(_ int, d time.Duration) {
				mu.Lock()
				delays = append(delays, d)
				mu.Unlock()
			})
		defer p.Stop()

		time.Sleep(600 * time.Second)

		mu.Lock()
		got := append([]time.Duration(nil), delays...)
		n := rounds
		mu.Unlock()

		if n < 4 {
			t.Fatalf("several rounds should fit in 600 seconds (backoff 30+60+120+300), got %d rounds", n)
		}
		// Backoff series: 30 -> 60 -> 120 -> 240 -> 300 (capped). Each has ±20% jitter,
		// so the assertion checks ranges, not equality.
		wantBase := []time.Duration{
			30 * time.Second, 60 * time.Second, 120 * time.Second,
			240 * time.Second, 300 * time.Second,
		}
		for i, base := range wantBase {
			if i >= len(got) {
				break
			}
			lo := time.Duration(float64(base) * 0.8)
			hi := time.Duration(float64(base) * 1.2)
			if got[i] < lo || got[i] > hi {
				t.Fatalf("wait %v of round %d is outside [%v, %v] (wrong backoff sequence or jitter)", i+1, got[i], lo, hi)
			}
		}
	})
}

// TestPollerStopCancelsInFlightPull: Stop must interrupt an **in-flight** request and wait.
//
// That matters in graceful handoff: the old daemon must not be dragged out by a stuck
// sync. The mechanism is the request hanging off the loop's ctx (fetch, client.go).
func TestPollerStopCancelsInFlightPull(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		started := make(chan struct{})
		var once sync.Once
		pl := pullerFunc(func(ctx context.Context, force bool) (Outcome, error) {
			once.Do(func() { close(started) })
			<-ctx.Done() // 模拟一个卡住的对端
			return Outcome{}, ctx.Err()
		})
		p := runLoop(context.Background(), pl, time.Minute, discardLogger(), nil)
		<-started
		p.Stop() // 必须返回：不能因为对端不响应而卡住 shutdown
	})
}

// TestRoundStateLogPolicy checks "not silent, not a flood" against a real logger.
func TestRoundStateLogPolicy(t *testing.T) {
	var buf bytes.Buffer
	lg := log.New(&buf, "", 0)
	rs := &roundState{}

	// The same error 12 rounds in a row: report only on 1/2/5/10, four times.
	for i := 0; i < 12; i++ {
		rs.report(lg, Outcome{Failed: true, ErrText: "same error"}, 30*time.Second)
	}
	if n := strings.Count(buf.String(), "\n"); n != 4 {
		t.Fatalf("12 identical failures should be reported only 4 times (1st/2nd/5th/10th), got %d:\n%s", n, buf.String())
	}
	if !strings.Contains(buf.String(), "attempt 1") {
		t.Fatalf("the first one must be reported (otherwise 'never silent' is empty):\n%s", buf.String())
	}

	// A different error -> report at once.
	buf.Reset()
	rs.report(lg, Outcome{Failed: true, ErrText: "a different error"}, 30*time.Second)
	if buf.Len() == 0 {
		t.Fatal("a changed error string must be reported at once")
	}

	// Recovery -> one "sync recovered" line.
	buf.Reset()
	rs.report(lg, Outcome{}, 30*time.Second)
	if !strings.Contains(buf.String(), "sync recovered") {
		t.Fatalf("recovery should be announced:\n%s", buf.String())
	}

	// Idle -> fully silent.
	buf.Reset()
	rs.report(lg, Outcome{Config: Channel{Kind: KindConfig, Gen: 42}}, 30*time.Second)
	if buf.Len() != 0 {
		t.Fatalf("a round with an unchanged generation must be fully silent, got:\n%s", buf.String())
	}

	// A real update -> one line, naming which files moved.
	buf.Reset()
	rs.report(lg, Outcome{
		PrevGen: 41,
		Config: Channel{
			Kind: KindConfig, Gen: 42, Changed: true,
			Written: []string{"providers.json"}, Removed: []string{"mappings/old.json"},
		},
		Secrets: Channel{Kind: KindSecrets, Gen: 7, Changed: true},
	}, 30*time.Second)
	out := buf.String()
	for _, want := range []string{"gen 41 → 42", "providers.json", "-mappings/old.json", "secrets updated"} {
		if !strings.Contains(out, want) {
			t.Fatalf("the log should contain %q:\n%s", want, out)
		}
	}
}

func TestFileSummaryTruncates(t *testing.T) {
	ch := Channel{}
	for _, n := range []string{"a", "b", "c", "d", "e", "f", "g", "h"} {
		ch.Written = append(ch.Written, n)
	}
	got := fileSummary(ch)
	if !strings.Contains(got, "and 8 in total") {
		t.Fatalf("excess entries should be truncated and the total stated, got %q", got)
	}
	if strings.Contains(got, ", g") {
		t.Fatalf("after truncation the rest must not be listed: %q", got)
	}
	if fileSummary(Channel{}) != "" {
		t.Fatal("the summary should be an empty string when nothing changed")
	}
}
