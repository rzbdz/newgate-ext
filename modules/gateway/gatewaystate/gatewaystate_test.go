package gatewaystate

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/rzbdz/newgate-ext/modules/config/domain"
	"github.com/rzbdz/newgate-ext/modules/config/store"
	"github.com/rzbdz/newgate-ext/testing/testkit"
)

func st(m map[string][]byte) *domain.State {
	return &domain.State{ModuleConfig: m}
}

func raw(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// TestParseFallsBackToFactory 没有这一段、或者 JSON 坏掉，都当出厂态。
// 方向是 fail-open：读不出来**别关人东西**（关了会让 DeepSeek 那边开始回 400）。
func TestParseFallsBackToFactory(t *testing.T) {
	for _, c := range []struct {
		name string
		st   *domain.State
	}{
		{"nil state", nil},
		{"empty ModuleConfig", st(nil)},
		{"the JSON of this section is broken", st(map[string][]byte{Key: []byte("{not json")})},
	} {
		t.Run(c.name, func(t *testing.T) {
			if !RepairEnabled(c.st) {
				t.Fatal("schema repair should default to on")
			}
			if !SpecialEnabled(c.st) {
				t.Fatal("the special layer should default to on")
			}
			if PluginOff(c.st, "deepseek") {
				t.Fatal("nothing configured means no plugin disabled")
			}
		})
	}
}

// TestFallbackDefaultsOn 没表过态 = **开**（换人照旧）。
//
// 这条守的是 fail-open 的那一半：没有这一段、JSON 坏了、老文件里根本没这个键，
// 都必须落在「照旧换人」上。落在「关掉」上的话，一台机器升级后会**静默地不再
// 换人**，而症状只在某个 provider 真的挂了的那天才显形——平时请求照常 200，
// 谁也不会去查（这正是 status 里那一行存在的理由，见 modules/gateway/status.go）。
func TestFallbackDefaultsOn(t *testing.T) {
	for _, c := range []struct {
		name string
		st   *domain.State
	}{
		{"nil state", nil},
		{"empty ModuleConfig", st(nil)},
		{"the JSON of this section is broken", st(map[string][]byte{Key: []byte("{not json")})},
		{"this section exists, but fallback was never written", st(map[string][]byte{Key: raw(t, Config{SchemaRepair: boolp(true)})})},
	} {
		t.Run(c.name, func(t *testing.T) {
			if FallbackOff(c.st) {
				t.Fatal("with no stated preference it should be 'switch as before' - especially when it cannot be read, fallback must not be turned off")
			}
		})
	}
}

// TestFallbackRoundTrip 写侧走一遍真 store：`newgate fallback off` 之后，
// **另一个进程**（守护进程）读到的必须也是关。
//
// 为什么值得单独立一条：这个开关的写端与读端分居两个进程，中间只隔着这次写下的
// JSON 原文，谁也不会当场发现两边分家了。所以这里断言两件事——解析出来的布尔，
// 以及**那段 JSON 的形状**：字段名或嵌套写错时，前者可能被「默认开」蒙对，
// 后者不会（`FallbackOff` 只看 `Gateway.fallback`，写在别处它照样报「关」，
// 而真实读路径与展示层看的是同一份 Parse，谁也发现不了）。
func TestFallbackRoundTrip(t *testing.T) {
	testkit.Sandbox(t)
	if _, err := store.Init(false); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if FallbackOff(store.LoadState()) {
		t.Fatal("the factory state should be 'switch as before'")
	}

	if err := SetFallback(false); err != nil {
		t.Fatalf("SetFallback(false): %v", err)
	}
	after := store.LoadState()
	if !FallbackOff(after) {
		t.Fatal("after disabling it still reads as on - the CLI and the daemon would diverge")
	}
	var got struct {
		Fallback *bool `json:"fallback"`
	}
	if err := json.Unmarshal(after.ModuleConfig[Key], &got); err != nil {
		t.Fatalf("cannot decode the gateway section: %v (raw %s)", err, after.ModuleConfig[Key])
	}
	if got.Fallback == nil || *got.Fallback {
		t.Fatalf("wrong on-disk shape: gateway section = %s, want fallback:false", after.ModuleConfig[Key])
	}
	// 顺带守一条：写这个开关不该碰到同一段里的别的指针字段。Config 里三个 *bool
	// 长得一样，`c.Fallback = &on` 抄错成别人是很容易发生、且**只在下次读的时候**
	// 才显形的那种错（症状是「我关的是 fallback，schema 修补怎么没了」）。
	if !RepairEnabled(after) || !SpecialEnabled(after) {
		t.Fatal("writing fallback flipped other switches in the same section")
	}

	if err := SetFallback(true); err != nil {
		t.Fatalf("SetFallback(true): %v", err)
	}
	if FallbackOff(store.LoadState()) {
		t.Fatal("after setting it back to on it is still off")
	}
}

// TestParseReadsLegacyKeys 老文件（顶层 typed 字段）必须还能读出来。
//
// 这条守的是一次**静默的**行为反转：store 对顶层不认识的键是无损保留的，老键
// 既不会自动消失、也不会被新读路径看到。不读它，一台显式 `special_treatment:
// false` 过的机器升级后会自己变回全开——用户关掉的东西悄悄回来了。
//
// ⚠️ 这条测试能过有一个**前提**：那几个键必须真的不再是 `domain.State` 上的
// typed 字段。store.LoadState 按 json tag 反射出「已知字段」表，**已知的键会被
// 结构化解析掉、根本进不了 ModuleConfig**，于是 legacyConfig 永远读不到它。
// 2026-09-18 实测踩过：删字段的正则漏了 debug，于是 debug 的老键迁移**静默失效**
// （老值读不到、新值写不进旧字段，state.json 顶层留着一个看着像生效的 debug:true，
// 而网关已经不读它了）。所以下面每加一个老键，都要确认它已经不在 domain.State 里。
func TestParseReadsLegacyKeys(t *testing.T) {
	off := false
	old := st(map[string][]byte{
		legacy.SchemaRepair:     raw(t, false),
		legacy.SpecialTreatment: raw(t, off),
		legacy.SpecialOff:       raw(t, []string{"deepseek", "glm"}),
		legacy.Debug:            raw(t, true),
		legacy.DebugUntil:       raw(t, "2099-01-01T00:00:00Z"),
	})
	if RepairEnabled(old) {
		t.Fatal("the old key schema_repair=false was not read")
	}
	if SpecialEnabled(old) {
		t.Fatal("the old key special_treatment=false was not read")
	}
	if !PluginOff(old, "deepseek") || !PluginOff(old, "glm") {
		t.Fatal("the old key special_treatment_off was not read")
	}
	if PluginOff(old, "thinking") {
		t.Fatal("a plugin that was never disabled was judged disabled")
	}
	if !DebugActive(old) {
		t.Fatal("the old key debug=true plus an unexpired debug_until was not read")
	}
	if got := DebugUntilDisplay(old); got != "2099-01-01T00:00:00Z" {
		t.Fatalf("the old key debug_until was not read: %q", got)
	}
}

// TestLegacyKeysAreNotTypedFields 是上面那条的**守卫**：老键必须不是
// domain.State 上的 typed 字段。
//
// 为什么值得单独立一条：字段还在的话，失败方式极其隐蔽——结构化解析把键吃掉，
// ModuleConfig 里没有它，legacyConfig 读不到，一切「看起来正常」，只是用户的设置
// 静默丢了。用反射直接问 struct，比靠人记得牢。
func TestLegacyKeysAreNotTypedFields(t *testing.T) {
	typ := reflect.TypeOf(domain.State{})
	for _, key := range []string{
		legacy.SchemaRepair, legacy.SpecialTreatment, legacy.SpecialOff,
		legacy.Debug, legacy.DebugUntil,
	} {
		for i := 0; i < typ.NumField(); i++ {
			tag := strings.SplitN(typ.Field(i).Tag.Get("json"), ",", 2)[0]
			if tag == key {
				t.Fatalf("domain.State still has the typed field %q: store parses it structurally, "+
					"so the old-key migration silently fails (unreadable, and not left in ModuleConfig)."+
					"the field must be deleted first for this package to read it.", key)
			}
		}
	}
}

// TestLegacyFillsOnlyUnsetFields 逐字段回退：新段里**设过**的字段胜出，
// 没设过的才读老键。
//
// 两个方向都要守：
//   - 没设过 → 读老键。否则分两步搬（新段先建好、老键后搬）时那一步会静默丢设置。
//   - 设过 → 不理老键。否则用户把开关拨回出厂态之后，老值会把它翻回去。
func TestLegacyFillsOnlyUnsetFields(t *testing.T) {
	t.Run("a field set in the new section wins", func(t *testing.T) {
		on := true
		s := st(map[string][]byte{
			Key:                     raw(t, Config{SpecialTreatment: &on}),
			legacy.SpecialTreatment: raw(t, false),
		})
		if !SpecialEnabled(s) {
			t.Fatal("the new section explicitly wrote true, but the old key's false flipped it back")
		}
	})
	t.Run("a field not set in the new section falls back to the old key", func(t *testing.T) {
		// 新段只写了 schema_repair（这正是分两步搬时的真实形态）。
		s := st(map[string][]byte{
			Key:                     raw(t, Config{SchemaRepair: boolp(true)}),
			legacy.Debug:            raw(t, true),
			legacy.DebugUntil:       raw(t, "2099-01-01T00:00:00Z"),
			legacy.SpecialTreatment: raw(t, false),
		})
		if !DebugActive(s) {
			t.Fatal("the new section never set debug, so it should fall back to the old key - otherwise the old setting is silently lost")
		}
		if SpecialEnabled(s) {
			t.Fatal("the new section never set special_treatment, so it should fall back to the old key")
		}
	})
}

func boolp(v bool) *bool { return &v }

// TestUpdateWritesNewAndDropsLegacy 写侧：值落到新段、老键当场删掉。
func TestUpdateWritesNewAndDropsLegacy(t *testing.T) {
	testkit.Sandbox(t)
	if _, err := store.Init(false); err != nil {
		t.Fatalf("Init: %v", err)
	}
	// 先造一份带老键的 state。
	s := store.LoadState()
	s.ModuleConfig[legacy.SpecialTreatment] = raw(t, false)
	s.ModuleConfig[legacy.SchemaRepair] = raw(t, true)
	if err := store.SaveState(s); err != nil {
		t.Fatal(err)
	}
	// 迁移期读得到老值。
	if SpecialEnabled(store.LoadState()) {
		t.Fatal("before the write the old key's false should be read")
	}

	if err := SetSpecialPlugin("deepseek", false); err != nil {
		t.Fatal(err)
	}

	after := store.LoadState()
	if _, still := after.ModuleConfig[legacy.SpecialTreatment]; still {
		t.Fatal("the old key was not removed - it would sit in state.json forever and make people think it still applies")
	}
	if _, still := after.ModuleConfig[legacy.SchemaRepair]; still {
		t.Fatal("the old key schema_repair was not removed")
	}
	if PluginOff(after, "deepseek") != true {
		t.Fatal("the new section was not written")
	}
	// 老值被带进了新段：写的那一刻先读到了 false，所以它必须活下来。
	if SpecialEnabled(after) {
		t.Fatal("the old key's false was dropped during migration - something the user turned off came back")
	}
}
