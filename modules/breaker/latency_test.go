package breaker

import (
	"testing"
	"time"

	"github.com/rzbdz/newgate-ext/lib/view"
)

// LatencySample 是**这一屏上唯一一处**把延迟算成颜色的地方（健康表的延迟列与首屏
// 每站那一格都读它），所以几个档位各来一条，外加「没样本」那条。
//
// 为什么「没样本」那条最要紧：画一个 `0ms` 是在说「快得没有延迟」，而真相是
// 「不知道」——在一个用来挑快的那条的页面上，这两句话的差别就是选错人。
func TestLatencySampleGradesAndColours(t *testing.T) {
	for _, c := range []struct {
		name    string
		sampled bool
		ms      int64
		wantOK  bool
		tone    string
	}{
		{"没探过就什么都不给", false, 0, false, ""},
		// 边界按 status.Grade 的口径：<3000 快、<=12000 可用、其余慢。
		{"1ms 是快", true, 1, true, view.ToneOK},
		{"2999ms 还是快", true, 2999, true, view.ToneOK},
		{"3000ms 起是可用", true, 3000, true, view.ToneWarn},
		{"12000ms 还是可用", true, 12000, true, view.ToneWarn},
		{"12001ms 是慢", true, 12001, true, view.ToneBad},
	} {
		t.Run(c.name, func(t *testing.T) {
			b := Status{Latency: c.ms}
			if c.sampled {
				b.Checked = time.Now()
			}
			ms, tone, ok := LatencySample(b)
			if ok != c.wantOK {
				t.Fatalf("ok 该是 %v，实际 %v", c.wantOK, ok)
			}
			if tone != c.tone {
				t.Fatalf("颜色该是 %q，实际 %q", c.tone, tone)
			}
			// 数字**原样**出去：这一格显示的就是样本本身，界面上不重算、不四舍五入
			// （它旁边那几格才是聚合出来的评分）。
			if ok && ms != c.ms {
				t.Fatalf("该原样给 %dms，实际 %dms", c.ms, ms)
			}
		})
	}
}

// BindingKey 是**跨模块**的机器标记：健康表的行 ID 与首屏按它去找那一条，两边各
// 拼一次就必须拼成同一个串。拼错的症状是「延迟那一格永远空着」——不报错，只是
// 一直没数据。
func TestBindingKeyJoinsProviderAndModel(t *testing.T) {
	if got := BindingKey("ark", "deepseek-v3"); got != "ark/deepseek-v3" {
		t.Fatalf("该是 ark/deepseek-v3，实际 %q", got)
	}
}
