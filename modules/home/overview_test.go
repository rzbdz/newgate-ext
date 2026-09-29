package home

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	modules "github.com/rzbdz/newgate/component"
	i18n "github.com/rzbdz/newgate/lib/i18n"
	"github.com/rzbdz/newgate/lib/view"
	breakerapi "github.com/rzbdz/newgate/modules/breaker"
	configapi "github.com/rzbdz/newgate/modules/config"
	configmod "github.com/rzbdz/newgate/modules/config"
	"github.com/rzbdz/newgate/modules/config/domain"
	"github.com/rzbdz/newgate/modules/config/paths"
	"github.com/rzbdz/newgate/modules/config/store"
	agentapi "github.com/rzbdz/newgate/modules/confighook"
	gatewayapi "github.com/rzbdz/newgate/modules/gateway"
	"github.com/rzbdz/newgate/testing/testkit"
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
		t.Fatalf("把这一节挂进账本失败: %v", err)
	}
	return f
}

// overview 取这一刻的首屏那一张卡。
func (f *ovFixture) overview() view.Overview {
	f.t.Helper()
	all, err := f.reg.Snapshot()
	if err != nil {
		f.t.Fatalf("快照失败: %v", err)
	}
	for _, c := range all {
		if c.ID != overviewID {
			continue
		}
		if c.Kind != view.KindOverview {
			f.t.Fatalf("Kind 该是 %q，实际 %q", view.KindOverview, c.Kind)
		}
		// 断类型而不是断 JSON：形状是**契约**（前端也按 Kind 挑渲染器），转不过去就是
		// 有人换了 Data 的类型，而那在界面上只表现为「这一屏白了」。
		data, ok := c.Data.(view.Overview)
		if !ok {
			f.t.Fatalf("Data 该是 view.Overview，实际 %T", c.Data)
		}
		return data
	}
	f.t.Fatalf("这一节没报出 %s", overviewID)
	return view.Overview{}
}

func (f *ovFixture) card(profile string) view.OverviewCard {
	f.t.Helper()
	for _, c := range f.overview().Cards {
		if c.Profile == profile {
			return c
		}
	}
	f.t.Fatalf("首屏上没有 %q 这张卡", profile)
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

// TestOverviewIsTheLandingConcept 首屏那一张要真的挂在这一节上，而且排在链卡前面。
// 靠的是 Order（不是字母序），见 view.Concept.Order。
func TestOverviewIsTheLandingConcept(t *testing.T) {
	f := newOverviewFixture(t)
	f.seedFile("production.kv", "normal = ark/deepseek-v3\n")

	all, err := f.reg.Snapshot()
	if err != nil {
		t.Fatalf("快照失败: %v", err)
	}
	byID := map[string]view.Concept{}
	for _, c := range all {
		byID[c.ID] = c
	}
	ov, ok := byID[overviewID]
	if !ok {
		t.Fatalf("快照里没有 %s", overviewID)
	}
	ch, ok := byID[conceptID]
	if !ok {
		t.Fatalf("快照里没有 %s", conceptID)
	}
	if ov.Order >= ch.Order {
		t.Fatalf("首屏那张该排在链卡前面：overview Order=%d，chains Order=%d", ov.Order, ch.Order)
	}
	// 两张卡说的必须是**同一份**链（一次 AllChains 喂两条，见 view.go 的 concepts）。
	ovData := ov.Data.(view.Overview)
	chData := ch.Data.(view.Chains)
	if len(ovData.Cards) != len(chData.Cards) {
		t.Fatalf("两张卡的 profile 数对不上：overview %d，chains %d",
			len(ovData.Cards), len(chData.Cards))
	}
	for i := range ovData.Cards {
		if ovData.Cards[i].Profile != chData.Cards[i].Profile {
			t.Errorf("第 %d 张卡说的不是同一份 profile：%q vs %q",
				i, ovData.Cards[i].Profile, chData.Cards[i].Profile)
		}
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
		t.Fatalf("标签该按 id 排、三个客户端都在、末尾一档空的，实际 %v", ids)
	}
	last := ov.Agents[len(ov.Agents)-1]
	if last.ID != "" || !last.Ready {
		t.Errorf("末尾那档该是空的「全部客户端」且能点，实际 %+v", last)
	}
	if last.Profile != "production" {
		t.Errorf("空那一档该报全局默认 production，实际 %q", last.Profile)
	}
	for _, a := range ov.Agents {
		if a.ID == "" {
			continue
		}
		want := a.ID != "opencode"
		if a.Ready != want {
			t.Errorf("%s 的 Ready 该是 %v，实际 %v", a.ID, want, a.Ready)
		}
		if a.ID == "codex" && a.Profile != "cheap" {
			t.Errorf("codex 单独设过 cheap，标签上该写它，实际 %q", a.Profile)
		}
		if a.ID == "claude" && a.Profile != "production" {
			t.Errorf("claude 没单设过，该报全局默认 production，实际 %q", a.Profile)
		}
	}

	prod := actions(f.card("production"))
	if !prod[useActionID("codex", "production")] {
		t.Error("production 那张卡上该有 use:codex（codex 单独设的是 cheap）")
	}
	if prod[useActionID("claude", "production")] {
		t.Error("claude 跟着全局默认、正用着 production，不该再报一个 use:claude")
	}
	if prod[useActionID(useGlobal, "production")] {
		t.Error("production 就是全局默认，不该报 use:global")
	}
	if prod[useActionID("opencode", "production")] {
		t.Error("opencode 没装，不该报它的按钮")
	}

	cheap := actions(f.card("cheap"))
	for _, want := range []string{useActionID(useGlobal, "cheap"), useActionID("claude", "cheap")} {
		if !cheap[want] {
			t.Errorf("cheap 那张卡上该有 %s", want)
		}
	}
	if cheap[useActionID("codex", "cheap")] {
		t.Error("codex 正用着 cheap，不该再报一个 use:codex")
	}
	if cheap[useActionID("opencode", "cheap")] {
		t.Error("opencode 没装，不该报它的按钮")
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
		t.Fatalf("开头 claude 不该有单独设过，实际 %q", got)
	}
	if _, err := f.reg.RunConceptAction(overviewID, useActionID("claude", "cheap")); err != nil {
		t.Fatalf("跑 use:claude 失败: %v", err)
	}
	if got := store.LoadState().Active["claude"]; got != "cheap" {
		t.Fatalf("跑完 use:claude 之后 state.active.claude 该是 cheap，实际 %q", got)
	}
	// 同一个动作跑第二次时它已经不在那一行上了（「已经用它」的不报）——这正是
	// 「按钮消失」在协议上的样子，而它必须报成一句说得通的话，不是静默。
	if _, err := f.reg.RunConceptAction(overviewID, useActionPrefix+"claude"); err == nil {
		t.Error("已经用着它之后这个动作不该还存在，第二次点该报出来")
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
		t.Fatal("健康表一条都没记下来——快照的键与 RecordProbe 的键对不上？")
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
		t.Fatal("production 上该有一行 normal")
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
			t.Fatalf("链上少了 %s", c.model)
		}
		if s.LatencyMs == 0 {
			t.Errorf("%s 该带一个延迟样本", c.model)
			continue
		}
		if s.LatencyTone != c.tone {
			t.Errorf("%s 的语气该是 %q，实际 %q（%dms）", c.model, c.tone, s.LatencyTone, s.LatencyMs)
		}
	}
	if s := byModel["unprobed-model"]; s.LatencyMs != 0 || s.LatencyTone != "" {
		t.Errorf("没样本的那一站什么都不该带，实际 %dms/%q", s.LatencyMs, s.LatencyTone)
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
		t.Fatalf("没有客户端目录时该只有一档空的「全部客户端」，实际 %+v", ov.Agents)
	}
	if len(ov.Cards) != 2 {
		t.Fatalf("两张卡都该在，实际 %d 张", len(ov.Cards))
	}
	if !actions(f.card("cheap"))[useActionID(useGlobal, "cheap")] {
		t.Error("没有客户端可选时，全局那一档是唯一能换链的手段，必须在")
	}
	for _, c := range ov.Cards {
		if actions(c)["probe"] {
			t.Errorf("%s 那张卡上报了探活按钮，可这一张图里没有网关", c.Profile)
		}
		for _, r := range c.Roles {
			for _, s := range r.Steps {
				if s.LatencyMs != 0 || s.LatencyTone != "" {
					t.Errorf("没有健康表时不该有延迟：%s/%s 带了 %dms", s.Provider, s.Model, s.LatencyMs)
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
		t.Fatal("有一条没通，这一发必须报出来（静默成功是最坏的答案）")
	}
	if !strings.Contains(err.Error(), "a/m2") {
		t.Errorf("那句话该点名哪一条没通，实际：%v", err)
	}
	if strings.Contains(err.Error(), "a/m1") {
		t.Errorf("通了的那条不该被算进不通里，实际：%v", err)
	}

	if ok := probeBatch(func(string, string) (gatewayapi.ProbeOutcome, error) {
		return gatewayapi.ProbeOutcome{OK: true, Status: 200}, nil
	}, targets); ok != nil {
		t.Errorf("全都通了就不该有横幅，实际：%v", ok)
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
			t.Errorf("%s 该正好出现一次，实际 %d 次", want, seen[want])
		}
	}
	if len(got) != 3 {
		t.Errorf("去重之后该是 3 条，实际 %d 条", len(got))
	}
}

func bind(provider, model string) configapi.Step {
	return configapi.Step{Binding: domain.Binding{Provider: provider, Model: model}}
}
