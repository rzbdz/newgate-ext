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

// pullerFunc 让测试不用真网络就能驱动整个循环。
type pullerFunc func(ctx context.Context, force bool) (Outcome, error)

func (f pullerFunc) Pull(ctx context.Context, force bool) (Outcome, error) { return f(ctx, force) }

func TestShouldLog(t *testing.T) {
	tests := []struct {
		fails     int
		errChange bool
		want      bool
		why       string
	}{
		{1, false, true, "第一个周期就必须说出来——否则「不静默」是空话"},
		{2, false, true, "第 2 次"},
		{3, false, false, "抑制"},
		{4, false, false, "抑制"},
		{5, false, true, "第 5 次"},
		{6, false, false, "抑制"},
		{10, false, true, "第 10 次"},
		{25, false, true, "第 25 次"},
		{50, false, true, "第 50 次"},
		{51, false, false, "50 之后按百次"},
		{100, false, true, "每 100 次"},
		{101, false, false, "抑制"},
		{3, true, true, "错误串一变立刻报：换了一种错就是新信息"},
		{999, true, true, "同上，哪怕次数很深"},
	}
	for _, tc := range tests {
		if got := shouldLog(tc.fails, tc.errChange); got != tc.want {
			t.Fatalf("shouldLog(%d, %v) = %v，想要 %v（%s）",
				tc.fails, tc.errChange, got, tc.want, tc.why)
		}
	}
}

func TestNextDelayAndJitter(t *testing.T) {
	// rnd=0.5 是抖动的中点（系数 1.0），用它验证退避序列本身。
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
			t.Fatalf("nextDelay(%v, %d, 0.5) = %v，想要 %v", tc.interval, tc.fails, got, tc.want)
		}
	}

	// 抖动必须真的在 ±20% 内，且两个方向都到得了。
	// 为什么必须有抖动：多台机器同样的 30 秒 ticker 会在启动时相位锁定，
	// 于是每 30 秒一起砸宿主一次、中间 30 秒全空。
	base := 100 * time.Second
	if got := jitter(base, 0); got != 80*time.Second {
		t.Fatalf("下界该是 -20%%，实际 %v", got)
	}
	if got := jitter(base, 0.5); got != base {
		t.Fatalf("中点该不动，实际 %v", got)
	}
	if got := jitter(base, 1); got != 120*time.Second {
		t.Fatalf("上界该是 +20%%，实际 %v", got)
	}
}

// TestPollerFirstRoundImmediate 锁住"不等第一个间隔"。
//
// daemon 重启后应当马上收敛，而不是先空转 30 秒——那 30 秒里用户看到的还是
// 旧配置，而"重启之后行为没变"正是最容易被当成"这个功能坏了"的现象。
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

		// 只让假时钟走 1 毫秒：够第一轮跑完，远不够一个间隔。
		time.Sleep(time.Millisecond)

		mu.Lock()
		got, at := rounds, firstAt
		mu.Unlock()
		if got < 1 {
			t.Fatal("1 毫秒内该已经拉过一轮")
		}
		if at.After(start.Add(time.Millisecond)) {
			t.Fatalf("第一轮该在启动时立刻发生，实际在 %v", at.Sub(start))
		}
	})
}

// TestPollerBackoffSequence 在**假时钟**下确定性跑完整个退避序列。
//
// 用 testing/synctest（Go 1.25+）：整个循环跑在一个时间被模拟的 bubble 里，
// 600 秒的退避在一次函数调用里就走完，既不用 sleep 也不会 flaky。这也是
// commit 0 升语言版本顺带拿到的东西之一。
func TestPollerBackoffSequence(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var mu sync.Mutex
		rounds := 0
		var delays []time.Duration
		pl := pullerFunc(func(ctx context.Context, force bool) (Outcome, error) {
			mu.Lock()
			rounds++
			mu.Unlock()
			return Outcome{Failed: true, ErrText: "连不上宿主"}, nil
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
			t.Fatalf("600 秒里该跑好几轮（退避 30+60+120+300），实际 %d 轮", n)
		}
		// 退避序列：30 → 60 → 120 → 240 → 300（封顶）。每一项都带 ±20% 抖动，
		// 所以判的是区间而不是等值。
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
				t.Fatalf("第 %d 轮的等待 %v 不在 [%v, %v]（退避序列或抖动不对）", i+1, got[i], lo, hi)
			}
		}
	})
}

// TestPollerStopCancelsInFlightPull：Stop 必须能打断**在飞**的请求并等它退干净。
//
// 优雅交接时这一点很重要：旧 daemon 要在排空后退出，不该被一个卡住的同步拖着。
// 实现上靠的是请求挂在 loop 的 ctx 上（client.go 的 fetch 用 NewRequestWithContext）。
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

// TestRoundStateLogPolicy 用真 logger 验一遍"不静默但不刷屏"。
func TestRoundStateLogPolicy(t *testing.T) {
	var buf bytes.Buffer
	lg := log.New(&buf, "", 0)
	rs := &roundState{}

	// 同一个错误连续 12 轮：只该报 1/2/5/10 四次。
	for i := 0; i < 12; i++ {
		rs.report(lg, Outcome{Failed: true, ErrText: "同一个错"}, 30*time.Second)
	}
	if n := strings.Count(buf.String(), "\n"); n != 4 {
		t.Fatalf("12 轮同样的失败该只报 4 次（第 1/2/5/10 次），实际 %d：\n%s", n, buf.String())
	}
	if !strings.Contains(buf.String(), "attempt 1") {
		t.Fatalf("第一次必须报（不然「不静默」是空话）:\n%s", buf.String())
	}

	// 换了一种错 → 立刻报。
	buf.Reset()
	rs.report(lg, Outcome{Failed: true, ErrText: "换了一种错"}, 30*time.Second)
	if buf.Len() == 0 {
		t.Fatal("错误串一变必须立刻报")
	}

	// 恢复 → 报一次"同步恢复"。
	buf.Reset()
	rs.report(lg, Outcome{}, 30*time.Second)
	if !strings.Contains(buf.String(), "sync recovered") {
		t.Fatalf("恢复该说一声:\n%s", buf.String())
	}

	// 空转 → 完全静默。
	buf.Reset()
	rs.report(lg, Outcome{Config: Channel{Kind: KindConfig, Gen: 42}}, 30*time.Second)
	if buf.Len() != 0 {
		t.Fatalf("代数没变的一轮必须完全静默，实际:\n%s", buf.String())
	}

	// 真的更新了 → 报一行，带上动了哪几个文件。
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
			t.Fatalf("日志里该有 %q:\n%s", want, out)
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
		t.Fatalf("多了该截断并说总数，实际 %q", got)
	}
	if strings.Contains(got, ", g") {
		t.Fatalf("截断之后不该还列出后面的: %q", got)
	}
	if fileSummary(Channel{}) != "" {
		t.Fatal("什么都没动时摘要该是空串")
	}
}
