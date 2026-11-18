package probe

import (
	"errors"
	"testing"
	"time"

	"github.com/rzbdz/newgate-ext/modules/gateway/dialect"
	"github.com/rzbdz/newgate-ext/modules/gateway/quirk"
)

func TestCapabilityCacheSurvivesProcessStateReset(t *testing.T) {
	t.Setenv("NEWGATE_HOME", t.TempDir())
	dialect.Reset()
	quirk.Default.Reset()
	t.Cleanup(func() {
		dialect.Reset()
		quirk.Default.Reset()
	})

	target := Target{Provider: "relay", Model: "model"}
	dialect.Mark(target.Provider, target.Model, dialect.CapOpenAI)
	dialect.MarkUnsupported(target.Provider, target.Model, dialect.CapAnthropic)
	dialect.MarkUnsupported(target.Provider, target.Model, dialect.CapCountTokens)
	quirk.Default.Mark(target.Provider, target.Model, quirk.NoThinkingDisable)

	cache, err := loadCapabilityCache()
	if err != nil {
		t.Fatalf("reading the cache in an empty environment should not error: %v", err)
	}
	cache.capture(target)
	if err := cache.save(); err != nil {
		t.Fatal(err)
	}
	dialect.Reset()
	quirk.Default.Reset()

	if err := LoadCachedCapabilities(); err != nil {
		t.Fatalf("loading the cache should not error: %v", err)
	}
	if ok, known := dialect.Supports(target.Provider, target.Model, dialect.CapOpenAI); !ok || !known {
		t.Fatal("a supported dialect was not restored from the cache")
	}
	if ok, known := dialect.Supports(target.Provider, target.Model, dialect.CapAnthropic); ok || !known {
		t.Fatal("an unsupported dialect was not restored from the cache")
	}
	if !quirk.Default.Has(target.Provider, target.Model, quirk.NoThinkingDisable) {
		t.Fatal("a quirk was not restored from the cache")
	}
}

type fakeTimeout struct{}

func (fakeTimeout) Error() string   { return "timeout" }
func (fakeTimeout) Timeout() bool   { return true }
func (fakeTimeout) Temporary() bool { return true }

func TestTimeoutRetryOnlyRetriesTimeoutOnce(t *testing.T) {
	calls := 0
	status, latency, err := timeoutRetry(func() (int, time.Duration, error) {
		calls++
		if calls == 1 {
			return 0, 12 * time.Second, fakeTimeout{}
		}
		return 200, 2 * time.Second, nil
	})
	if calls != 2 || status != 200 || latency != 2*time.Second || err != nil {
		t.Fatalf("timeout retry = calls:%d status:%d latency:%s err:%v",
			calls, status, latency, err)
	}

	calls = 0
	wantErr := errors.New("connection refused")
	_, _, err = timeoutRetry(func() (int, time.Duration, error) {
		calls++
		return 0, time.Millisecond, wantErr
	})
	if calls != 1 || !errors.Is(err, wantErr) {
		t.Fatalf("non-timeout retried: calls:%d err:%v", calls, err)
	}
}

func TestSummarizeCountsUniqueModels(t *testing.T) {
	results := []Result{
		{Profile: "gpt", Role: "normal", Provider: "p", Model: "terra", OK: false},
		{Profile: "gpt", Role: "mid", Provider: "p", Model: "terra", OK: false},
		{Profile: "gpt", Role: "heavy", Provider: "p", Model: "sol", OK: true},
		{Profile: "gpt", Role: "vision", Provider: "p", Model: "sol", OK: true},
		{Profile: "gpt", Role: "light", Provider: "p", Model: "luna", OK: true},
	}
	summary := Summarize(results)
	if len(summary) != 1 || summary[0].OK != 2 || summary[0].Bad != 1 {
		t.Fatalf("a repeated tier must not be counted twice: %+v", summary)
	}
	if got := UniqueCount(results); got != 3 {
		t.Fatalf("UniqueCount = %d, want 3", got)
	}
	if got := FailedCount(results); got != 1 {
		t.Fatalf("FailedCount = %d, want 1", got)
	}
}

// TestPendingQuirksSurviveARestart 钉住「转发路上学到的毛病也要活过重启」。
//
// # 为什么这一条非有不可
//
// 用户 2026-09-22 报的故障是「重启后第一发 Codex → GLM 必然 400、过一会儿自己
// 好」，而它的成因是**两份时间尺度不同**：补丁的判据在内存表里（重启即空），
// 落盘缓存却只有 `newgate probe` 会写。于是换版/重启之后，那一发实打实的
// 400 得重新撞一次才学得回来——必现、且没有任何表面原因。
//
// 于是这里量的是那**两个时刻之间**：
//
//	1 撞一次 4xx（Learn 记进内存表）→ 记进待落盘那一份；
//	2 停机（Flush）→ 写进 probe-capabilities.json；
//	3 进程重启（内存表清空）；
//	4 LoadCachedCapabilities() → 内存表里必须又有了，**第一条请求就不会再撞**。
//
// 第 3 步必须是**真的一次清空**而不是「再建一张表」：内存表活下来这个测试就
// 变成了空转（它测的正是「没有内存表时靠盘上那份」，见 quirk.Default 的注释）。
func TestPendingQuirksSurviveARestart(t *testing.T) {
	t.Setenv("NEWGATE_HOME", t.TempDir())
	quirk.Default.Reset()
	t.Cleanup(quirk.Default.Reset)

	target := Target{Provider: "relay", Model: "always-thinks"}
	// 判据由拥有补丁的模块注册（这里是 modules/thinking 的那条，原文见
	// signatures.go）。测试里自己注册一条同形状的，而不是 import 那个模块：
	// probe 在 gateway 这一层，thinking 在它上面，import 过来就是一条反向的
	// 依赖边（同一条理由见 modules/gateway/direction_test.go）。
	rel, err := quirk.Default.RegisterSignature(quirk.Signature{
		Flag:  quirk.NoThinkingDisable,
		Any:   []string{"始终思考"},
		Label: "this model always thinks",
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if rel != nil {
			_ = rel()
		}
	})
	// 走的是真实判据而不是直接 Mark：这条路上「认出签名」本身也是被测的一环。
	learned := quirk.Default.Learn(target.Provider, target.Model, 400,
		[]byte(`{"error":{"message":"该模型始终思考，不支持关闭思考；请使用 low、high 或 max。"}}`))
	if len(learned) == 0 {
		t.Fatal("this message should be recognized (criterion in modules/thinking/signatures.go)")
	}
	var pending PendingQuirks
	if !pending.Record(target, QuirksOf(target)) {
		t.Fatal("a quirk just learned should be recorded in the copy waiting to be written")
	}
	if err := pending.Flush(); err != nil {
		t.Fatalf("writing on shutdown failed: %v", err)
	}

	// 重启：内存里那一位清掉。
	quirk.Default.Reset()
	if quirk.Default.Has(target.Provider, target.Model, quirk.NoThinkingDisable) {
		t.Fatal("after clearing, the in-memory table should be empty (otherwise this test tests nothing)")
	}

	if err := LoadCachedCapabilities(); err != nil {
		t.Fatalf("loading the cache should not error: %v", err)
	}
	if !quirk.Default.Has(target.Provider, target.Model, quirk.NoThinkingDisable) {
		t.Fatal("the bit is missing from the in-memory table after a restart - the first request would hit the 400 again, which is exactly the window to fix")
	}
}

// TestMergeQuirksIsAUnion 钉住「并进缓存是并集，不是覆盖」。
//
// 缓存里那两位方言结论来自一次真探活，被一次转发路上学到的毛病抹掉的话，症状是
// **下游按一个错的方言去发**（探活说这个端点只收 openai，抹成「不知道」之后
// 就会去试 anthropic）——而这一条没有任何症状指向那次落盘。
func TestMergeQuirksIsAUnion(t *testing.T) {
	t.Setenv("NEWGATE_HOME", t.TempDir())

	target := Target{Provider: "relay", Model: "m"}
	cache, _ := loadCapabilityCache()
	cache.Targets[target.String()] = capabilityEntry{
		Supports: uint32(dialect.CapOpenAI),
		Known:    uint32(dialect.CapOpenAI | dialect.CapAnthropic),
	}
	if err := cache.save(); err != nil {
		t.Fatal(err)
	}

	if _, err := MergeQuirks(map[Target]quirk.Flag{
		target: quirk.NoThinkingDisable,
	}); err != nil {
		t.Fatal(err)
	}

	after, _ := loadCapabilityCache()
	e := after.Targets[target.String()]
	if e.Known == 0 || e.Supports == 0 {
		t.Fatalf("the two dialect bits were wiped (%+v) - a conclusion learned by probing must not be overwritten by a single forward", e)
	}
	if quirk.Flag(e.Quirks) != quirk.NoThinkingDisable {
		t.Fatalf("the quirk bit was not merged in: %+v", e)
	}
}

// TestDiskQuirksReportsWhatIsWorthReloading：启动时那个问题——「盘上有没有值得
// 装回来的东西」——的答案只能从**毛病位非零**的目标来。
//
// 探活写下的那些方言结论（quirks==0）不该被算成「有一份缓存」，否则「这台机器
// 到底探过没有」这个判断会永远为真。
func TestDiskQuirksReportsWhatIsWorthReloading(t *testing.T) {
	t.Setenv("NEWGATE_HOME", t.TempDir())

	withQuirk := Target{Provider: "relay", Model: "always-thinks"}
	dialectOnly := Target{Provider: "relay", Model: "plain"}
	cache, _ := loadCapabilityCache()
	cache.Targets[withQuirk.String()] = capabilityEntry{Quirks: uint32(quirk.NoThinkingDisable)}
	cache.Targets[dialectOnly.String()] = capabilityEntry{Known: 1, Supports: 1}
	if err := cache.save(); err != nil {
		t.Fatal(err)
	}

	got := DiskQuirks()
	if len(got) != 1 || got[0] != withQuirk {
		t.Fatalf("DiskQuirks = %+v, only the one with the quirk bit should be reported", got)
	}
}
