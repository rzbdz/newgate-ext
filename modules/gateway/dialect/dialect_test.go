package dialect

import "testing"

func TestMarkAndSupports(t *testing.T) {
	Reset()
	defer Reset()

	if _, known := Supports("p", "m", CapCountTokens); known {
		t.Fatal("没探过就 known=true")
	}

	if !Mark("p", "m", CapAnthropic) {
		t.Fatal("第一次 Mark 应报新学到")
	}
	if Mark("p", "m", CapAnthropic) {
		t.Fatal("重复 Mark 不该再报新学到")
	}
	if ok, known := Supports("p", "m", CapAnthropic); !ok || !known {
		t.Fatalf("支持位读不出来: ok=%v known=%v", ok, known)
	}
	// 隔离：别的 (provider, model) 不受影响
	if _, known := Supports("p", "other", CapAnthropic); known {
		t.Fatal("key 串了")
	}
}

// TestMarkUnsupported 正反两面：明确「不支持」之后 Supports 永远回
// (false, true)——只记支持位的话，每个没有该端点的上游每次请求都得
// 再撞一次 404。
func TestMarkUnsupported(t *testing.T) {
	Reset()
	defer Reset()

	if !MarkUnsupported("p", "m", CapCountTokens) {
		t.Fatal("第一次 MarkUnsupported 应报新学到")
	}
	ok, known := Supports("p", "m", CapCountTokens)
	if ok || !known {
		t.Fatalf("明确不支持后应 (false,true)，实际 (%v,%v)", ok, known)
	}
	if MarkUnsupported("p", "m", CapCountTokens) {
		t.Fatal("重复 MarkUnsupported 不该再报新学到")
	}

	// 上游升级了：从「不支持」改口「支持」也是新信息
	if !Mark("p", "m", CapCountTokens) {
		t.Fatal("改口应报新学到")
	}
	if ok, _ := Supports("p", "m", CapCountTokens); !ok {
		t.Fatal("改口后读不出来")
	}
}

func TestCapString(t *testing.T) {
	if got := (CapOpenAI | CapAnthropic).String(); got != "openai+anthropic" {
		t.Errorf("Cap.String() = %q", got)
	}
	if got := CapCountTokens.String(); got != "count_tokens" {
		t.Errorf("Cap.String() = %q", got)
	}
	if got := Cap(0).String(); got != "" {
		t.Errorf("零值应为空串，实际 %q", got)
	}
}

func TestSnapshotSorted(t *testing.T) {
	Reset()
	defer Reset()
	Mark("b", "z", CapOpenAI)
	Mark("b", "a", CapOpenAI)
	Mark("a", "x", CapOpenAI)

	entries := Snapshot()
	if len(entries) != 3 {
		t.Fatalf("应有 3 条，实际 %d", len(entries))
	}
	want := []string{"a/x", "b/a", "b/z"}
	for i, e := range entries {
		if got := e.Provider + "/" + e.Model; got != want[i] {
			t.Errorf("第 %d 条 = %s，应为 %s（要稳定排序）", i, got, want[i])
		}
	}
}
