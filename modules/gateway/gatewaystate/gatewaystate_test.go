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
		{"空 ModuleConfig", st(nil)},
		{"这一段的 JSON 是坏的", st(map[string][]byte{Key: []byte("{不是 json")})},
	} {
		t.Run(c.name, func(t *testing.T) {
			if !RepairEnabled(c.st) {
				t.Fatal("schema 修补该默认开")
			}
			if !SpecialEnabled(c.st) {
				t.Fatal("special 层该默认开")
			}
			if PluginOff(c.st, "deepseek") {
				t.Fatal("没配过就不该有插件被关")
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
		{"空 ModuleConfig", st(nil)},
		{"这一段的 JSON 是坏的", st(map[string][]byte{Key: []byte("{不是 json")})},
		{"这一段在，但没写过 fallback", st(map[string][]byte{Key: raw(t, Config{SchemaRepair: boolp(true)})})},
	} {
		t.Run(c.name, func(t *testing.T) {
			if FallbackOff(c.st) {
				t.Fatal("没表过态就该是「照旧换人」——读不出来时尤其不能把 fallback 关掉")
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
		t.Fatal("出厂态该是「照旧换人」")
	}

	if err := SetFallback(false); err != nil {
		t.Fatalf("SetFallback(false): %v", err)
	}
	after := store.LoadState()
	if !FallbackOff(after) {
		t.Fatal("关掉之后读到的还是开——CLI 与守护进程会分家")
	}
	var got struct {
		Fallback *bool `json:"fallback"`
	}
	if err := json.Unmarshal(after.ModuleConfig[Key], &got); err != nil {
		t.Fatalf("gateway 段解不开: %v（原文 %s）", err, after.ModuleConfig[Key])
	}
	if got.Fallback == nil || *got.Fallback {
		t.Fatalf("落盘形状不对：gateway 段 = %s, want fallback:false", after.ModuleConfig[Key])
	}
	// 顺带守一条：写这个开关不该碰到同一段里的别的指针字段。Config 里三个 *bool
	// 长得一样，`c.Fallback = &on` 抄错成别人是很容易发生、且**只在下次读的时候**
	// 才显形的那种错（症状是「我关的是 fallback，schema 修补怎么没了」）。
	if !RepairEnabled(after) || !SpecialEnabled(after) {
		t.Fatal("写 fallback 把同一段里别的开关带翻了")
	}

	if err := SetFallback(true); err != nil {
		t.Fatalf("SetFallback(true): %v", err)
	}
	if FallbackOff(store.LoadState()) {
		t.Fatal("拨回 on 之后还关着")
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
		t.Fatal("老键 schema_repair=false 没被读到")
	}
	if SpecialEnabled(old) {
		t.Fatal("老键 special_treatment=false 没被读到")
	}
	if !PluginOff(old, "deepseek") || !PluginOff(old, "glm") {
		t.Fatal("老键 special_treatment_off 没被读到")
	}
	if PluginOff(old, "thinking") {
		t.Fatal("没关过的插件被误判成关了")
	}
	if !DebugActive(old) {
		t.Fatal("老键 debug=true + 未到期的 debug_until 没被读到")
	}
	if got := DebugUntilDisplay(old); got != "2099-01-01T00:00:00Z" {
		t.Fatalf("老键 debug_until 没被读到: %q", got)
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
				t.Fatalf("domain.State 上还有 typed 字段 %q：store 会把它结构化解析掉，"+
					"老键迁移**静默失效**（读不到、也不留在 ModuleConfig 里）。"+
					"字段必须先删掉，本包才能读到它。", key)
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
	t.Run("新段设过的字段胜出", func(t *testing.T) {
		on := true
		s := st(map[string][]byte{
			Key:                     raw(t, Config{SpecialTreatment: &on}),
			legacy.SpecialTreatment: raw(t, false),
		})
		if !SpecialEnabled(s) {
			t.Fatal("新段显式写了 true，却被老键的 false 翻回去了")
		}
	})
	t.Run("新段没设过的字段回退到老键", func(t *testing.T) {
		// 新段只写了 schema_repair（这正是分两步搬时的真实形态）。
		s := st(map[string][]byte{
			Key:                     raw(t, Config{SchemaRepair: boolp(true)}),
			legacy.Debug:            raw(t, true),
			legacy.DebugUntil:       raw(t, "2099-01-01T00:00:00Z"),
			legacy.SpecialTreatment: raw(t, false),
		})
		if !DebugActive(s) {
			t.Fatal("新段没设过 debug，该回退读老键——否则老设置静默丢了")
		}
		if SpecialEnabled(s) {
			t.Fatal("新段没设过 special_treatment，该回退读老键")
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
		t.Fatal("写之前该读到老键的 false")
	}

	if err := SetSpecialPlugin("deepseek", false); err != nil {
		t.Fatal(err)
	}

	after := store.LoadState()
	if _, still := after.ModuleConfig[legacy.SpecialTreatment]; still {
		t.Fatal("老键没被删掉——它会永远躺在 state.json 里让人以为还生效")
	}
	if _, still := after.ModuleConfig[legacy.SchemaRepair]; still {
		t.Fatal("老键 schema_repair 没被删掉")
	}
	if PluginOff(after, "deepseek") != true {
		t.Fatal("新段没写进去")
	}
	// 老值被带进了新段：写的那一刻先读到了 false，所以它必须活下来。
	if SpecialEnabled(after) {
		t.Fatal("老键的 false 在迁移时被丢了——用户关掉的东西自己回来了")
	}
}
