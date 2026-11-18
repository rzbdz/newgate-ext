package pluginmanager

import (
	"encoding/json"
	"go/parser"
	"go/token"
	"strconv"
	"testing"
	"time"

	"github.com/rzbdz/newgate-ext/modules/config/domain"
)

// stateWith 造一个只带 plugin_manager 字段的 state 快照。
func stateWith(t *testing.T, cfg Config) *domain.State {
	t.Helper()
	raw, err := cfg.Marshal()
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return &domain.State{ModuleConfig: map[string][]byte{StateKey: raw}}
}

func TestOffDefaultsToFalse(t *testing.T) {
	// 用户没动过 = 没关。这条是 kill switch 语义的地基：出厂开着的补丁
	// 不能因为「没配置」就被关掉。
	for _, st := range []*domain.State{nil, {}, stateWith(t, Config{})} {
		if Off(st, "deepseek.tail-shape") {
			t.Fatalf("没设过的开关点不该是「已关」: %+v", st)
		}
	}
}

func TestOffHonoursExplicitSetting(t *testing.T) {
	st := stateWith(t, Config{Off: map[string]Entry{"deepseek.tail-shape": {}}})
	if !Off(st, "deepseek.tail-shape") {
		t.Fatal("显式关掉的开关点应该是「已关」（空 Until = 不限时）")
	}
	if Off(st, "deepseek.inject-thinking") {
		t.Fatal("没被关的那条不该受影响")
	}
}

func TestOffExpiresLazily(t *testing.T) {
	// 懒过期：没人在后台扫，读的时候比一下时间。过期当没设过。
	st := stateWith(t, Config{Off: map[string]Entry{
		"deepseek.tail-shape":      {Until: time.Now().Add(-time.Second)},
		"deepseek.inject-thinking": {Until: time.Now().Add(time.Hour)},
	}})
	if Off(st, "deepseek.tail-shape") {
		t.Fatal("过期的设定应该当没设过")
	}
	if !Off(st, "deepseek.inject-thinking") {
		t.Fatal("没过期的设定应该还在")
	}
}

func TestOffAndOnAreSeparateTables(t *testing.T) {
	// 两张表独立：Off 看 Off 表，On 看 On 表。一条 Path 只会出现在其中一张里，
	// 由 CLI 按 Switch.Default 决定——这样热路径两个函数都不必查注册表。
	st := stateWith(t, Config{On: map[string]Entry{"gateway.passthrough": {}}})
	if Off(st, "gateway.passthrough") {
		t.Fatal("On 表里的设定不该被 Off() 看见")
	}
	if !On(st, "gateway.passthrough") {
		t.Fatal("On() 应该看见 On 表里的设定")
	}
	if On(st, "deepseek.tail-shape") {
		t.Fatal("没设过的 mode 不该是「已开」")
	}
}

func TestParseNeverFails(t *testing.T) {
	// fail-open：坏 JSON 一律当「什么都没设过」。绝不因为一段坏配置把某个
	// 功能关掉——那比不生效更糟，因为它静默。
	for _, raw := range [][]byte{
		nil, {}, []byte("not json"), []byte(`{"off": "不是 map"}`),
		[]byte(`{"off": {"a": 123}}`), []byte(`[]`),
	} {
		st := &domain.State{ModuleConfig: map[string][]byte{StateKey: raw}}
		if Off(st, "deepseek.tail-shape") {
			t.Fatalf("坏配置不该被解释成「已关」: %q", raw)
		}
	}
}

func TestConfigRoundTrips(t *testing.T) {
	want := Config{
		Off: map[string]Entry{"deepseek.tail-shape": {Until: time.Unix(1800000000, 0).UTC()}},
		On:  map[string]Entry{"gateway.passthrough": {}},
	}
	raw, err := want.Marshal()
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got := Parse(raw)
	if !got.Off["deepseek.tail-shape"].Until.Equal(want.Off["deepseek.tail-shape"].Until) {
		t.Fatalf("Until 没往返回来: %+v", got.Off)
	}
	if _, ok := got.On["gateway.passthrough"]; !ok {
		t.Fatalf("On 表没往返回来: %+v", got.On)
	}
	// 不限时的那条不能因为 omitempty 变成「有值」。
	if !got.On["gateway.passthrough"].Until.IsZero() {
		t.Fatal("不限时应该是零值 Until")
	}
}

func TestPruneDropsExpiredOnly(t *testing.T) {
	cfg := Config{
		Off: map[string]Entry{
			"a.x": {Until: time.Now().Add(-time.Minute)},
			"b.y": {Until: time.Now().Add(time.Minute)},
			"c.z": {},
		},
	}
	got := cfg.Prune()
	if _, ok := got.Off["a.x"]; ok {
		t.Fatal("过期项该被清掉")
	}
	if _, ok := got.Off["b.y"]; !ok {
		t.Fatal("未过期项不该被清掉")
	}
	if _, ok := got.Off["c.z"]; !ok {
		t.Fatal("不限时项不该被清掉")
	}
}

func TestRemaining(t *testing.T) {
	until := time.Now().Add(time.Minute).Round(time.Second)
	st := stateWith(t, Config{
		Off: map[string]Entry{"deepseek.tail-shape": {Until: until}},
		On:  map[string]Entry{"gateway.passthrough": {}},
	})
	if got := Remaining(st, "deepseek.tail-shape"); !got.Equal(until) {
		t.Fatalf("Remaining = %v, 想要 %v", got, until)
	}
	// 不限时的返回零值（调用方据此决定要不要显示「还有 …」）。
	if got := Remaining(st, "gateway.passthrough"); !got.IsZero() {
		t.Fatalf("不限时该返回零值，得到 %v", got)
	}
	if got := Remaining(st, "没这条"); !got.IsZero() {
		t.Fatalf("没设过的该返回零值，得到 %v", got)
	}
}

// TestQueryIsPure 守的是这个包最重要的一条纪律：Off/On 跑在转发热路径上、
// 每个请求都要调，所以它们**只读传进来的配置快照**——无锁、无 IO、不查注册表、
// 不开文件。
//
// 为什么用解析 import 而不是靠 review：这条纪律一旦破了，症状是「转发循环里多了
// 一把锁」或「CLI 改了配置但 daemon 读到的是另一份」——都很晚才被发现。而它同时
// 还是一条**缝**：将来要让共享配置层统一治理车队（共享层 overlay + 机器本地层，
// 本地优先），做法是共享层把值合并进快照，这两个函数一个字都不用改。谁让
// query.go 自己去读文件，这条缝就焊死了。
func TestQueryIsPure(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "query.go", nil, parser.ImportsOnly)
	if err != nil {
		t.Fatalf("解析 query.go: %v", err)
	}
	allowed := map[string]bool{
		strconv.Quote("time"): true,
		strconv.Quote("github.com/rzbdz/newgate-ext/modules/config/domain"): true,
	}
	for _, imp := range f.Imports {
		if !allowed[imp.Path.Value] {
			t.Fatalf("query.go 多了一个 import %s —— 热路径查询必须是纯函数："+
				"只读传进来的 *domain.State，不许查注册表、不许开文件、不许读环境变量。"+
				"见 TestQueryIsPure 的注释（它还是一条给共享配置层留的缝）。",
				imp.Path.Value)
		}
	}
}

// TestOffIsDeterministic 同一个快照调一千次结果必须一致，且不修改它。
func TestOffIsDeterministic(t *testing.T) {
	st := stateWith(t, Config{Off: map[string]Entry{"deepseek.tail-shape": {}}})
	before, _ := json.Marshal(st.ModuleConfig)
	for i := 0; i < 1000; i++ {
		if !Off(st, "deepseek.tail-shape") {
			t.Fatalf("第 %d 次结果变了", i)
		}
	}
	after, _ := json.Marshal(st.ModuleConfig)
	if string(before) != string(after) {
		t.Fatal("Off() 改动了传进来的快照")
	}
}
