package home

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/rzbdz/newgate-ext/lib/view"
	breakerapi "github.com/rzbdz/newgate-ext/modules/breaker"
	configapi "github.com/rzbdz/newgate-ext/modules/config"
	configmod "github.com/rzbdz/newgate-ext/modules/config"
	"github.com/rzbdz/newgate-ext/modules/config/domain"
	"github.com/rzbdz/newgate-ext/modules/config/paths"
	"github.com/rzbdz/newgate-ext/modules/config/store"
	agentapi "github.com/rzbdz/newgate-ext/modules/confighook"
	gatewayapi "github.com/rzbdz/newgate-ext/modules/gateway"
	"github.com/rzbdz/newgate-ext/testing/testkit"
	modules "github.com/rzbdz/newgate/component"
	i18n "github.com/rzbdz/newgate/lib/i18n"
)

// 这一族验的是**首屏那张卡**（home.overview）：它比链卡多的那两样东西——「这个
// 客户端此刻走谁」与「链上每一站快不快」——是不是真的按契约摆出来了。
//
// 与 chains_test.go 同一套做法：起一张**真的**小图（本模块 + config，外加这一屏
// 用得上的那几个端口），素材是沙箱里几份配置文件解析出来的。三个可选端口各给一份
// **假**的替身（见 provides）：把它们真装起来各要一串别的依赖，而那些依赖与这里要
// 验的东西没有关系。替身实现的是**契约接口**，不是把真模块的内部抄一遍。

// provides 造一个只提供某个能力的测试组件。
func provides(name string, p modules.Provision) modules.Component {
	return modules.Component{
		Name:     name,
		Desc:     func() string { return "test stub" },
		Type:     "others",
		Provides: []modules.Provision{p},
		Start:    func(context.Context, modules.Context) error { return nil },
		Stop:     func(context.Context) error { return nil },
	}
}

// installedFacts 是「这家客户端装了」那份运行时事实（见 confighook.AgentFacts）。
//
// 为什么不用 Agent.Bin 去 PATH 上找（那是缺省判据）：测试机上没有 claude / codex
// 这两条命令，而**测试不该依赖开发机上装了什么**。事实优先于 PATH，正是真实现里
// 那条判据的原话（见 confighook.InstalledDefault）。
type installedFacts bool

func (f installedFacts) Installed() bool             { return bool(f) }
func (installedFacts) SlotTier(agentapi.Slot) string { return "" }

// ovFixture 是本族的全部设施。
type ovFixture struct {
	t    *testing.T
	reg  *view.Registry
	cfg  configapi.Config
	deps overviewDeps
}

// newOverviewFixture 开沙箱、起图、把这一节挂进账本。
//
// deps 由 extra 里那几个替身决定：**不同的测试要装不同的那一批可选端口**，而
// 「一个都没装」正是其中一种（那时这一屏必须照常成立）。
func newOverviewFixture(t *testing.T, extra ...modules.Component) *ovFixture {
	t.Helper()
	testkit.Sandbox(t)
	if err := os.MkdirAll(paths.Mappings(), 0o770); err != nil {
		t.Fatal(err)
	}
	// 配置根要立起来：一份 profile 都没有与「连目录都没有」是两件事——前者是全新
	// 安装，后者是 ListProfiles 读不出来（那是错误，不是空配置）。
	seedProviders(t, `{"providers":{
		"ark":{"base_url":"https://ark.example/v3","api_key":"k"},
		"zhipu":{"base_url":"https://zhipu.example","api_key":"k"}
	}}`)
	comps := append([]modules.Component{New(), configmod.New()}, extra...)
	g := testkit.Start(t, comps...)

	f := &ovFixture{t: t, reg: view.NewRegistry(), cfg: testkit.Get(g, configapi.Capability)}
	// 取三个可选端口。缺席是合法的（零值），而那正是其中一条被测的性质。
	if v, ok := testkit.Maybe(g, agentapi.AgentCatalogCapability); ok {
		f.deps.agents = v
	}
	if v, ok := testkit.Maybe(g, breakerapi.Capability); ok {
		f.deps.health = v
	}
	if _, err := registerView(f.reg, f.cfg, f.deps); err != nil {
		t.Fatalf("registering this section into the ledger failed: %v", err)
	}
	return f
}

// overview 取这一刻的首屏那一张卡。
func (f *ovFixture) overview() view.Overview {
	f.t.Helper()
	all, err := f.reg.Snapshot()
	if err != nil {
		f.t.Fatalf("snapshot failed: %v", err)
	}
	for _, c := range all {
		if c.ID != overviewID {
			continue
		}
		if c.Kind != view.KindOverview {
			f.t.Fatalf("Kind should be %q, actual %q", view.KindOverview, c.Kind)
		}
		// 断类型而不是断 JSON：形状是**契约**（前端也按 Kind 挑渲染器），转不过去就是
		// 有人换了 Data 的类型，而那在界面上只表现为「这一屏白了」。
		data, ok := c.Data.(view.Overview)
		if !ok {
			f.t.Fatalf("Data should be view.Overview, actual %T", c.Data)
		}
		return data
	}
	f.t.Fatalf("this section did not report %s", overviewID)
	return view.Overview{}
}

// auto 取网格最前面那一张「默认」卡（它不是一个 profile，见 view.OverviewCard.Auto）。
func (f *ovFixture) auto() view.OverviewCard {
	f.t.Helper()
	a := f.overview().Auto
	if a == nil {
		f.t.Fatal("there is no \"default\" card on the landing screen")
	}
	if !a.Auto {
		f.t.Fatal("that card was not marked Auto -- the UI will draw it as a profile")
	}
	return *a
}

func (f *ovFixture) card(profile string) view.OverviewCard {
	f.t.Helper()
	for _, c := range f.overview().Cards {
		if c.Profile == profile {
			return c
		}
	}
	f.t.Fatalf("there is no %q card on the landing screen", profile)
	return view.OverviewCard{}
}

// actions 是这张卡上动作 ID 的集合（断言只问「有没有」，不问顺序）。
func actions(c view.OverviewCard) map[string]bool {
	out := map[string]bool{}
	for _, a := range c.Actions {
		out[a.ID] = true
	}
	return out
}

// seedFile 落一份 profile（复用 chains_test.go 里那个 helper）。
func (f *ovFixture) seedFile(rel, body string) {
	f.t.Helper()
	seedFile(f.t, rel, body)
}

// twoProfiles 是这一族里最常用的一副牌：production（全局默认）与 cheap。
func (f *ovFixture) twoProfiles() {
	f.t.Helper()
	seedFile(f.t, "production.kv", "normal = ark/deepseek-v3\n")
	seedFile(f.t, "cheap.kv", "normal = zhipu/glm-4\n")
	if err := store.SetDefaultProfile("production", false); err != nil {
		f.t.Fatal(err)
	}
}

// ---------- 这一屏的两条路 ----------

// TestOverviewIsTheOnlyCardOnTheLandingSection 这一节**只有一张卡**。
//
// 曾经有两张：首屏那张总览，外加一张 `home.chains`（逐条读的那一屏）。2026-09-29
// 按用户的话删掉了后者（原话：「这个 tab、卡片就是没用的，直接删除：候选链」）——
// 它讲的每一件事，首屏那张的时刻表上都有，而它多买到的只有一个标签条。这一条钉住
// 「删干净了」：多回来一张，界面上就多一条要点的标签。
func TestOverviewIsTheOnlyCardOnTheLandingSection(t *testing.T) {
	f := newOverviewFixture(t)
	f.twoProfiles()

	all, err := f.reg.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 {
		ids := make([]string, 0, len(all))
		for _, c := range all {
			ids = append(ids, c.ID)
		}
		t.Fatalf("this section should have only one card, actual %d: %v", len(all), ids)
	}
	ov := all[0]
	if ov.ID != overviewID {
		t.Errorf("that one should be %s, actual %s", overviewID, ov.ID)
	}
	if ov.Kind != view.KindOverview {
		t.Errorf("Kind should be %q, actual %q", view.KindOverview, ov.Kind)
	}
	// 它摆的每一份 profile 都得在（少一张就是「某条链在界面上不存在」，而那件事
	// 没有任何东西会报出来）。
	if got := len(ov.Data.(view.Overview).Cards); got != 2 {
		t.Errorf("both tiers should each have a card, actual %d", got)
	}
}

// TestOverviewAgentsAndUseActions 客户端目录在时：顶上按它列标签，「用它」按钮按
// 每个客户端各报一个，**已经在用的那个不报**。
//
// 每条判据都是产品上真会遇到的那一种：
//   - `codex` 单独设过（state.active.codex=cheap），所以 production 那张卡上要有
//     `use:codex`；
//   - `claude` 没单独设过（跟着全局默认 = production），所以 production 那张卡上
//     **不该**有 `use:claude`——把它指给它自己是件没有意义的事；
//   - 全局那一档（`use:global`）只在「这份不是全局默认」时出现；
//   - **没装的客户端一个按钮都不报**：改了也不会有东西经过它。
func TestOverviewAgentsAndUseActions(t *testing.T) {
	cat := testkit.NewCatalog().
		Add(&agentapi.Agent{ID: "claude"}, &agentapi.Agent{ID: "codex"}, &agentapi.Agent{ID: "opencode"}).
		WithFacts("claude", installedFacts(true)).
		WithFacts("codex", installedFacts(true)).
		WithFacts("opencode", installedFacts(false))
	f := newOverviewFixture(t, provides("catalog-stub",
		modules.Provide(agentapi.AgentCatalogCapability, cat.AsCatalog())))
	f.twoProfiles()
	if err := store.SetActiveProfile("codex", "cheap"); err != nil {
		t.Fatal(err)
	}

	ov := f.overview()
	var ids []string
	for _, a := range ov.Agents {
		ids = append(ids, a.ID)
	}
	// 末尾那档空的「全部客户端」**必须在**：`use:global:<profile>` 只在当前这一档
	// 是它的时候画得出来（见 Overview.svelte 的 actions()），少这一档就等于把
	// 「所有客户端一起换链」从这一屏上删掉——而且是静默的。
	if strings.Join(ids, ",") != "claude,codex,opencode," {
		t.Fatalf("tabs should be sorted by id, all three clients present, and an empty tier at the end, actual %v", ids)
	}
	last := ov.Agents[len(ov.Agents)-1]
	if last.ID != "" || !last.Ready {
		t.Errorf("the last tier should be the empty \"all clients\" tab and clickable, actual %+v", last)
	}
	if last.Profile != "production" {
		t.Errorf("the empty tier should report the global default production, actual %q", last.Profile)
	}
	for _, a := range ov.Agents {
		if a.ID == "" {
			continue
		}
		want := a.ID != "opencode"
		if a.Ready != want {
			t.Errorf("Ready for %s should be %v, actual %v", a.ID, want, a.Ready)
		}
		if a.ID == "codex" && a.Profile != "cheap" {
			t.Errorf("codex has its own cheap setting, the tab should say so, actual %q", a.Profile)
		}
		if a.ID == "claude" && a.Profile != "production" {
			t.Errorf("claude has no override, it should report the global default production, actual %q", a.Profile)
		}
		// Own 与 Profile 是两件事，而这里正是它们分得开的地方：codex 单独设过
		// （Own=true），claude 只是跟着全局默认走、恰好解析成 production（Own=false）。
		// 少了这一格，界面只能把这两个状态说成同一句话——而它们的路由行为不同：
		// 改掉全局默认，claude 会跟着变，codex 不会。
		if a.ID == "codex" && !a.Own {
			t.Error("codex has its own override, so Own should be true")
		}
		if a.ID == "claude" && a.Own {
			t.Error("claude has no override and just follows the default, so Own should be false")
		}
	}

	prod := actions(f.card("production"))
	if !prod[useActionID("codex", "production")] {
		t.Error("the production card should have use:codex (codex on its own is set to cheap)")
	}
	// **这一条是这一整块的重点**：claude 此刻确实走在 production 上（它跟着全局
	// 默认，而默认就是 production），但它**没有固定**在 production 上——「固定它」
	// 与「让它跟着」是两个状态，所以「就用它」这个按钮必须还在。前一版按解析结果
	// 去比，判的是「它已经在用了」，于是用户想固定住的那一步**永远做不出来**。
	if !prod[useActionID("claude", "production")] {
		t.Error("claude follows the default on production but is not pinned -- there should be a use:claude to pin it")
	}
	if prod[useAutoPrefix+"claude"] {
		t.Error("claude is not pinned to any of them, so it should not offer \"back to auto\"")
	}
	if prod[useActionID(useGlobal, "production")] {
		t.Error("production is the global default, so it should not offer use:global")
	}
	if prod[useActionID("opencode", "production")] {
		t.Error("opencode is not installed, so its buttons should not be offered")
	}

	cheap := actions(f.card("cheap"))
	if !cheap[useActionID("claude", "cheap")] {
		t.Error("the cheap card should have use:claude:cheap (pin claude to cheap)")
	}
	if cheap[useActionID("codex", "cheap")] {
		t.Error("codex is already pinned to cheap, so use:codex should not be offered again")
	}
	// **同一张卡上还有「把自动指到它」那一条**：它在「自动」那一档下才会被画出来
	// （前端按当前这一档挑，见 Overview.svelte 的 actions）。两句话长在同一张卡上
	// 是有意的——同一张卡、同一个手势，含义由你在哪一档决定。
	if !cheap[useActionID(useGlobal, "cheap")] {
		t.Error("the cheap card should have use:global:cheap (clicking it under the auto tier points auto at cheap)")
	}

	// 「自动」那张：它此刻 resolve 到 production，而它自己**不摆成员表**——能指向
	// 哪几份由下面那几张 profile 卡自己说（点它们就是改自动）。
	au := f.auto()
	if au.Profile != "production" {
		t.Errorf("\"auto\" should report the production that it currently resolves to, actual %q", au.Profile)
	}
	if !au.Default || au.File == "" {
		t.Errorf("\"auto\" should carry the default marker and the file it points at, actual %+v", au)
	}
	am := actions(au)
	if am[useActionID(useGlobal, "cheap")] {
		t.Error("the \"auto\" card should have no member actions -- you choose by clicking a profile card itself")
	}
	if !am[useAutoPrefix+"codex"] {
		t.Error("codex has its own override, so there should be a \"follow auto\" entry")
	}
	if am[useAutoPrefix+"claude"] {
		t.Error("claude has no override and already follows, so there should be no \"follow auto\" for it")
	}
	// 它的链必须与它指向的那一份**逐字相同**：两张卡各建一次的话，两次读盘之间配置
	// 变了，同一份 profile 会在两张卡上显示两条不同的链。
	if len(au.Roles) != len(f.card("production").Roles) {
		t.Errorf("the \"auto\" chain should match the production one word for word, tier counts %d vs %d",
			len(au.Roles), len(f.card("production").Roles))
	}
	if cheap[useActionID("opencode", "cheap")] {
		t.Error("opencode is not installed, so its buttons should not be offered")
	}
}

// TestOverviewUseActionWritesTheHeader 点一下「就用它」真的要落到 state.json 里
// 那个客户端的那一行上。
//
// 走**动作**这条路（RunConceptAction）而不是直接调 store：界面上真的点的就是它，
// 而这条路上有一跳（账本按概念 ID 找到卡、再在卡的动作里找那个 ID）只在调用那一刻
// 才成立——动作 ID 拼错（`use:` 后面那一段）在这一跳上表现为「按钮点了没反应」。
func TestOverviewUseActionWritesTheHeader(t *testing.T) {
	cat := testkit.NewCatalog().Add(&agentapi.Agent{ID: "claude"}).
		WithFacts("claude", installedFacts(true))
	f := newOverviewFixture(t, provides("catalog-stub",
		modules.Provide(agentapi.AgentCatalogCapability, cat.AsCatalog())))
	f.twoProfiles()

	if got := store.LoadState().Active["claude"]; got != "" {
		t.Fatalf("at the start claude should have no override, actual %q", got)
	}
	if _, err := f.reg.RunConceptAction(overviewID, useActionID("claude", "cheap")); err != nil {
		t.Fatalf("running use:claude failed: %v", err)
	}
	if got := store.LoadState().Active["claude"]; got != "cheap" {
		t.Fatalf("after running use:claude, state.active.claude should be cheap, actual %q", got)
	}
	// 同一个动作跑第二次时它已经不在那一行上了（「已经用它」的不报）——这正是
	// 「按钮消失」在协议上的样子，而它必须报成一句说得通的话，不是静默。
	if _, err := f.reg.RunConceptAction(overviewID, useActionPrefix+"claude"); err == nil {
		t.Error("once it is already in use this action should no longer exist, the second click should report it")
	}
}

// TestOverviewLatencyLandsOnTheStation 健康表里有样本时，链上那一站要带上数字与
// 颜色，而颜色按 breaker 的档位来（阈值只有一处实现，见 breaker/status）。
//
// 这条守的是首屏最要紧的一件事：用户是拿它比快慢的。三个档位各来一条，而「没样本」
// 那一条**必须什么都不带**——画一个 0ms 是在说「快得没有延迟」，而真相是「不知道」。
func TestOverviewLatencyLandsOnTheStation(t *testing.T) {
	table := breakerapi.NewTable()
	f := newOverviewFixture(t, provides("breaker-stub",
		modules.Provide(breakerapi.Capability, table)))
	f.seedFile("production.kv",
		"normal = ark/fast-model, ark/ok-model, ark/slow-model, ark/unprobed-model\n")

	// 三条各记一发探活结论。慢的那条 13000ms 越过 SlowMs=12000。
	for _, c := range []struct {
		model string
		ms    time.Duration
	}{{"fast-model", 900 * time.Millisecond}, {"ok-model", 4 * time.Second}, {"slow-model", 13 * time.Second}} {
		table.RecordProbe("ark", c.model, 200, 0, c.ms, 12*time.Second, "")
	}
	if n := len(table.Snapshot()); n == 0 {
		t.Fatal("not a single health record was kept -- do the snapshot keys and the RecordProbe keys not match?")
	}

	// 档位行按能力从高到低排（domain.Roles），而这份 profile 只写了 normal——
	// 不能拿 Roles[0]（那是 heavy，空的）。
	var row view.OverviewRow
	for _, r := range f.card("production").Roles {
		if r.Tier == "normal" {
			row = r
		}
	}
	if row.Tier == "" {
		t.Fatal("there should be a normal row on production")
	}
	byModel := map[string]view.OverviewStep{}
	for _, s := range row.Steps {
		byModel[s.Model] = s
	}
	for _, c := range []struct{ model, tone string }{
		{"fast-model", view.ToneOK}, {"ok-model", view.ToneWarn}, {"slow-model", view.ToneBad},
	} {
		s, ok := byModel[c.model]
		if !ok {
			t.Fatalf("the chain is missing %s", c.model)
		}
		if s.LatencyMs == 0 {
			t.Errorf("%s should carry a latency sample", c.model)
			continue
		}
		if s.LatencyTone != c.tone {
			t.Errorf("the tone of %s should be %q, actual %q (%dms)", c.model, c.tone, s.LatencyTone, s.LatencyMs)
		}
	}
	if s := byModel["unprobed-model"]; s.LatencyMs != 0 || s.LatencyTone != "" {
		t.Errorf("the station with no sample should carry nothing, actual %dms/%q", s.LatencyMs, s.LatencyTone)
	}
}

// TestOverviewWithoutOptionalPorts 一个可选端口都没装时，这一屏必须**照常出卡**。
//
// 这是「少一样就整张卡消失」那类错唯一会红的地方：健康表不在就没有延迟、网关不在
// 就没有探活按钮、客户端目录不在就只有那档「全部客户端」——而链一张不少，那句
// 「所有客户端都用它」也还在（它是这种装配下唯一能把链换掉的手段）。
func TestOverviewWithoutOptionalPorts(t *testing.T) {
	f := newOverviewFixture(t)
	f.twoProfiles()

	ov := f.overview()
	if len(ov.Agents) != 1 || ov.Agents[0].ID != "" || !ov.Agents[0].Ready {
		t.Fatalf("with no client catalog there should be only one empty \"all clients\" tier, actual %+v", ov.Agents)
	}
	if len(ov.Cards) != 2 {
		t.Fatalf("both cards should be there, actual %d", len(ov.Cards))
	}
	if !actions(f.card("cheap"))[useActionID(useGlobal, "cheap")] {
		t.Error("\"point auto at it\" must be present -- with no client installed it is the only way to switch chains")
	}
	for _, c := range ov.Cards {
		if actions(c)["probe"] {
			t.Errorf("%s card offers a probe button, but this graph has no gateway", c.Profile)
		}
		for _, r := range c.Roles {
			for _, s := range r.Steps {
				if s.LatencyMs != 0 || s.LatencyTone != "" {
					t.Errorf("with no health table there should be no latency: %s/%s carried %dms", s.Provider, s.Model, s.LatencyMs)
				}
			}
		}
	}
}

// ---------- 探活的扇出 ----------

// TestProbeBatchReportsWhatWentWrong 探一批时，**没通的那几条要被说出来**。
//
// 按这个按钮的人要的答案正是「哪几家不通」，所以那几条必须出现在返回的那句话里；
// 全都通了才返回 nil（那时横幅不该出现）。
func TestProbeBatchReportsWhatWentWrong(t *testing.T) {
	targets := []probeTarget{
		{provider: "a", model: "m1", key: "a/m1"},
		{provider: "a", model: "m2", key: "a/m2"},
	}
	err := probeBatch(func(provider, model string) (gatewayapi.ProbeOutcome, error) {
		if model == "m2" {
			return gatewayapi.ProbeOutcome{}, i18n.E("no route to host", nil)
		}
		return gatewayapi.ProbeOutcome{OK: true, Status: 200}, nil
	}, targets)
	if err == nil {
		t.Fatal("one did not get through, this request must report it (a silent success is the worst answer)")
	}
	if !strings.Contains(err.Error(), "a/m2") {
		t.Errorf("the sentence should name which one did not get through, actual: %v", err)
	}
	if strings.Contains(err.Error(), "a/m1") {
		t.Errorf("the one that got through must not be counted as failed, actual: %v", err)
	}

	if ok := probeBatch(func(string, string) (gatewayapi.ProbeOutcome, error) {
		return gatewayapi.ProbeOutcome{OK: true, Status: 200}, nil
	}, targets); ok != nil {
		t.Errorf("when everything gets through there should be no banner, actual: %v", ok)
	}
}

// TestProbeTargetsDedupe 同一条 binding 在一屏上出现很多次（跨 profile 的链是常态），
// 而探活**一次只该打它一发**：同一个上游在同一时刻被我们打两发既没有新信息，也是
// 在给对面上压力。
func TestProbeTargetsDedupe(t *testing.T) {
	chains := []configapi.Chain{
		{Key: "normal", Steps: []configapi.Step{bind("p", "m1"), bind("p", "m2")}},
		{Key: "light", Steps: []configapi.Step{bind("p", "m2"), bind("p", "m1"), bind("q", "m1")}},
	}
	seen := map[string]int{}
	got := probeTargetsOf(chains)
	for _, t2 := range got {
		seen[t2.key]++
	}
	for _, want := range []string{"p/m1", "p/m2", "q/m1"} {
		if seen[want] != 1 {
			t.Errorf("%s should appear exactly once, actual %d times", want, seen[want])
		}
	}
	if len(got) != 3 {
		t.Errorf("after deduplication there should be 3 entries, actual %d", len(got))
	}
}

func bind(provider, model string) configapi.Step {
	return configapi.Step{Binding: domain.Binding{Provider: provider, Model: model}}
}
