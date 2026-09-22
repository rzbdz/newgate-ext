package home

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rzbdz/newgate/lib/view"
	configapi "github.com/rzbdz/newgate/modules/config"
	configmod "github.com/rzbdz/newgate/modules/config"
	"github.com/rzbdz/newgate/modules/config/paths"
	"github.com/rzbdz/newgate/modules/config/store"
	"github.com/rzbdz/newgate/testing/testkit"
)

// 这一族的判据从**两处**来，各自有各自的理由。
//
// 一处在 configapi / lib/view 的注释里（行 ID 必须是 `profile/tier`、动作由 config
// 构造好了要原样透传、链卡上的行 ID 全概念唯一），一处在 product 那一边（首屏要
// 落在这一节上、空链要说得出话）。所以下面既有「照着端口给的结论摆」这种形状断言，
// 也有「挂进账本之后还成立吗」这种走界面那条路的断言。
//
// 所有测试都起一张**真的**小图（本模块 + config）：素材不是手搓的 configapi 结构体，
// 是沙箱里几份配置文件**解析出来的**。手搓的话，这里断言的是「我以为 config 会那么
// 给」，而真正的契约（Actions 的构造、Skips 的类目、Steps 的来源）一条都没被碰到。

// fixture 是一次测试的全部设施：沙箱里一份可用的配置根、一张真图、一个账本。
type fixture struct {
	t       *testing.T
	reg     *view.Registry
	cfg     configapi.Config
	concept view.Concept
}

// newFixture 开沙箱、起图、把这一节挂进账本，并**问一遍快照**。
//
// 走 Snapshot 而不是直接调 concepts()：账本那一跳是这一节的契约之一（身份不能空、
// Kind 必须给、同一个 ID 不能有第二个人报），而它在界面上真的会发生。
func newFixture(t *testing.T) *fixture {
	t.Helper()
	testkit.Sandbox(t)
	// 配置根本身要立起来：一份 profile 都没有与「连目录都没有」是两件事——
	// 前者是全新安装，后者是 ListProfiles 读不出来（那是错误，不是空配置）。
	if err := os.MkdirAll(paths.Mappings(), 0o770); err != nil {
		t.Fatal(err)
	}
	seedProviders(t, `{"providers":{
		"ark":{"base_url":"https://ark.example/v3","api_key":"k"},
		"zhipu":{"base_url":"https://zhipu.example","api_key":"k"}
	}}`)

	// 只装被测的东西：本模块 + 真的 config（链的结论只有它算得出来）。
	g := testkit.Start(t, New(), configmod.New())
	cfg := testkit.Get(g, configapi.Capability)

	f := &fixture{t: t, reg: view.NewRegistry(), cfg: cfg}
	if _, err := registerView(f.reg, cfg); err != nil {
		t.Fatalf("把这一节挂进账本失败: %v", err)
	}
	f.concept = f.snapshot()
	return f
}

// snapshot 重问一遍快照并取出这一节（每次调都是新的结论——产出函数每次重算）。
func (f *fixture) snapshot() view.Concept {
	f.t.Helper()
	all, err := f.reg.Snapshot()
	if err != nil {
		f.t.Fatalf("快照失败: %v", err)
	}
	for _, c := range all {
		if c.ID == conceptID {
			return c
		}
	}
	f.t.Fatalf("这一节没报出 %s（快照里有 %d 个概念）", conceptID, len(all))
	return view.Concept{}
}

// chains 取此刻这一节的数据形状。
func (f *fixture) chains() view.Chains {
	f.t.Helper()
	c := f.snapshot()
	if c.Kind != view.KindChains {
		f.t.Fatalf("Kind 该是 %q，实际 %q", view.KindChains, c.Kind)
	}
	// 断类型而不是断 JSON：形状是**契约**（前端也按 Kind 挑渲染器），转不过去就是
	// 有人换了 Data 的类型，而那在界面上只表现为「这一节白了」。
	data, ok := c.Data.(view.Chains)
	if !ok {
		f.t.Fatalf("Data 该是 view.Chains，实际 %T", c.Data)
	}
	return data
}

func (f *fixture) seedFile(rel, body string) { f.t.Helper(); seedFile(f.t, rel, body) }

func (f *fixture) setDefault(name string) {
	f.t.Helper()
	if err := store.SetDefaultProfile(name, false); err != nil {
		f.t.Fatalf("设默认档位失败: %v", err)
	}
}

// seedProviders 落一份 providers.json（**不重复开沙箱**：见 seedFile 的注释）。
func seedProviders(t *testing.T, body string) {
	t.Helper()
	if err := os.MkdirAll(paths.Root(), 0o770); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.ProvidersFile(), []byte(body), 0o660); err != nil {
		t.Fatal(err)
	}
}

// seedFile 落一份档位文件。与内核那几条测试同一套（复用 kernel 的布局：名字就是
// 文件名，扩展名决定格式）。
//
// **一个测试只开一次沙箱**（testkit.Sandbox 每次调用都换一个新的临时目录）——
// 所以它由 newFixture 开一次，这一层只往里写文件。
func seedFile(t *testing.T, rel, body string) string {
	t.Helper()
	if err := os.MkdirAll(paths.Mappings(), 0o770); err != nil {
		t.Fatal(err)
	}
	abs := filepath.Join(paths.Mappings(), rel)
	if err := os.WriteFile(abs, []byte(body), 0o660); err != nil {
		t.Fatal(err)
	}
	return abs
}

// cardOf / rowIn 按名字取一张卡、一行。找不到即 Fatal——那说明这一屏少了东西，
// 而这一屏上「少一行」看起来只像「这台机器没这么配」。
func cardOf(t *testing.T, ch view.Chains, profile string) view.ChainCard {
	t.Helper()
	for _, c := range ch.Cards {
		if c.Profile == profile {
			return c
		}
	}
	t.Fatalf("没有 %q 这张卡（现在有 %d 张）", profile, len(ch.Cards))
	return view.ChainCard{}
}

func rowIn(t *testing.T, card view.ChainCard, tier string) view.ChainRow {
	t.Helper()
	for _, r := range card.Roles {
		if r.Tier == tier {
			return r
		}
	}
	t.Fatalf("%s 这张卡上没有 %q 这一档（现在有 %d 档）", card.Profile, tier, len(card.Roles))
	return view.ChainRow{}
}

// ---------- 首屏落在它上 ----------

// TestTheSectionIsTheLanding：这一节声明自己是落点，而且栏名/分组真的报到了界面上。
//
// 这条测试防的是一个**没有别的东西会红**的失效：Landing 是链在 Title 后面的一个
// 方法（见 view.Landing），掉了它编译照过、界面照开，只是首屏安静地回到「Source
// 字母序最前的那一栏」——那看起来只像「排版不合我意」。
//
// Group 那一格同理：`.In` 掉了的话这一栏会排到侧栏最上面（不分组的那几栏在最前，
// 见 Sidebar.svelte），而它自己就是首屏落点，于是「为什么它在第一位」在界面上
// 无从解释。
func TestTheSectionIsTheLanding(t *testing.T) {
	f := newFixture(t)
	sections := f.reg.Sections()
	if len(sections) != 1 {
		t.Fatalf("这一节只该登记一栏，实际 %d 栏", len(sections))
	}
	s := sections[0]
	if s.Source != Name {
		t.Errorf("Source 该是机器标记 %q，实际 %q", Name, s.Source)
	}
	if !s.Default {
		t.Error("这一节声明了 Landing()，Sections 里必须带上 default")
	}
	if s.Title == "" {
		t.Error("栏名不能空（界面上会是一栏没有标题的按钮）")
	}
	if s.Group == "" {
		t.Error("这一节该归到「概览」那一组：不分组的那几栏排在最前面，而它正是落点")
	}
}

// ---------- 空配置 ----------

// TestNoProfilesIsAnEmptyScreen：一份 profile 都没有时**不 panic**、卡片列表为空。
//
// 这是全新安装的样子（providers.json 刚建、还没写过任何档位文件），而它是这一节
// 最常见的第一次运行——一个 nil 解引用会在这里白屏，而不是在别的什么地方。
func TestNoProfilesIsAnEmptyScreen(t *testing.T) {
	f := newFixture(t)

	ch := f.chains()
	if len(ch.Cards) != 0 {
		t.Fatalf("没有任何档位文件时报了 %d 张卡：%+v", len(ch.Cards), ch.Cards)
	}
	// 空的是**列表**，不是 Data 本身：前端读 `data?.cards ?? []`（见 Chains.svelte），
	// 两者都可以，但形状要稳定——今天给的是非 nil 的空切片（Chains() 里 make 出来的）。
	if ch.Cards == nil {
		t.Error("空配置该给一个空列表，不是把形状整个省掉")
	}
}

// ---------- 一屏：链头、空链、次序 ----------

// TestRowsCarryTheHeadOfEachTier：一档一行，链头是**第一站**，空链没有链头。
//
// 「一档一行」的第一行是这条测试唯一能锁住的东西里最容易错的那个：链**不截断**
// （maxAttempts 是执行上限，不是 membership），所以 heavy 那一行后面还得跟着
// ark/deepseek-lite——只画链头的话，那一站就从屏幕上消失了，而它明明在链上。
func TestRowsCarryTheHeadOfEachTier(t *testing.T) {
	f := newFixture(t)
	f.seedFile("demo.kv", "desc=demo\nheavy=ark/deepseek-v3,ark/deepseek-lite\nlight=ark/deepseek-lite\n")
	f.setDefault("demo")

	card := cardOf(t, f.chains(), "demo")
	if card.Default != true {
		t.Error("demo 是此刻生效的那一份，卡头要自报 default")
	}
	if card.File != filepath.Join("mappings", "demo.kv") {
		t.Errorf("File 该是相对配置根的那份文件（mappings/demo.kv），实际 %q", card.File)
	}

	heavy := rowIn(t, card, "heavy")
	if heavy.Head != "ark/deepseek-v3" {
		t.Errorf("heavy 的链头该是第一个候选 ark/deepseek-v3，实际 %q", heavy.Head)
	}
	if len(heavy.Steps) != 2 {
		t.Fatalf("heavy 该给完整的两站（不按 maxAttempts 截断），实际 %d 站", len(heavy.Steps))
	}
	if heavy.Steps[1].Model != "deepseek-lite" {
		t.Errorf("第二站该是 ark/deepseek-lite，实际 %q/%q", heavy.Steps[1].Provider, heavy.Steps[1].Model)
	}
	if heavy.Steps[0].Profile != "demo" {
		t.Errorf("每一站要写出来自哪份 profile，实际 %q", heavy.Steps[0].Profile)
	}
	if heavy.Tone != "" {
		t.Errorf("有链的时候不着色（Tone 只说有没有可用的链头），实际 %q", heavy.Tone)
	}
	if heavy.Note != "" {
		t.Errorf("一个候选都没跳过时链尾不该有话，实际 %q", heavy.Note)
	}

	if got := rowIn(t, card, "light").Head; got != "ark/deepseek-lite" {
		t.Errorf("light 的链头该是 ark/deepseek-lite，实际 %q", got)
	}

	// 次序是**端口给的**（domain.Roles 的能力序），这一层不重排：按字母序排出来
	// 会是 h/l/m/n，而终端那边仍是能力序——同一份配置两个屏幕两种排法。
	var order []string
	for _, r := range card.Roles {
		order = append(order, r.Tier)
	}
	if strings.Join(order, ",") != "heavy,normal,mid,light,vision" {
		t.Errorf("档位次序该原样是 port 给的那一串，实际 %v", order)
	}
}

// TestAnEmptyChainIsRedAndSaysWhy：所有候选都被跳过时，这一行是空链——没有链头、
// 语气是 bad、链尾说得清为什么。
//
// 这三种「空」在界面上的样子完全不同：没链头是一句 dim 的 `no steps`、bad 让它
// 着色、Note 是唯一能告诉用户「去加个 api_key」的东西。少了 Note，用户看到的就是
// 一条**红的空链**，而不知道该动哪里（这正是 config 那边「跳过只给汇总、但必须给」
// 的那条取舍在这张卡上的落点）。
func TestAnEmptyChainIsRedAndSaysWhy(t *testing.T) {
	f := newFixture(t)
	// 绑了一家**没定义过**的 provider：候选进不了链，类目是 undefined。
	f.seedFile("demo.kv", "heavy=ghost/model\n")
	f.setDefault("demo")

	row := rowIn(t, cardOf(t, f.chains(), "demo"), "heavy")
	if row.Head != "" {
		t.Errorf("空链没有链头，实际 %q", row.Head)
	}
	if len(row.Steps) != 0 {
		t.Errorf("空链不该有站，实际 %d 站", len(row.Steps))
	}
	if row.Tone != view.ToneBad {
		t.Errorf("空链该是 bad，实际 %q", row.Tone)
	}
	if row.Note == "" {
		t.Fatal("空链必须说清为什么（没有它，用户看到的就是一条红的空链）")
	}
	// 那句话里要有**类目**（哪一种没成功）与**数量**（一个候选）。类目名是这里
	// 自己的措辞，所以断言的是「那句话说的是 undefined 那一类」，不是整句。
	if !strings.Contains(row.Note, skipLabel(skipUndefined)) {
		t.Errorf("链尾该点名那一类（%q）：%q", skipLabel(skipUndefined), row.Note)
	}
	if !strings.Contains(row.Note, "1 candidate") {
		t.Errorf("只有一个候选时该用单数形式，实际 %q", row.Note)
	}
}

// TestSkipCategoriesAreAllNamed 是那张类目表的棘轮。
//
// skipKindOrder / skipLabel 认的是 resolve 打在 Skip.Kind 上的**机器标记**，而那几个
// 字符串在本包是**重写了一遍的字面量**（理由见 chains.go：链的结论一律走 configapi，
// 发行版不 import resolve）。重写的代价是「内核改了标记名」这种漂移不会被编译器
// 发现——症状是一整类跳过**静默消失**（数量不报、名字不画），而链卡上少了几个数字
// 看起来完全正常。
//
// 所以这里拿沙箱里**真的解析出来的** skips 过一遍：每一个出现过的 Kind 都必须落进
// 那张表里被认出来（否则 skipLabel 会把它归进「other」，数量对不上）。
func TestSkipCategoriesAreAllNamed(t *testing.T) {
	f := newFixture(t)
	// 一次凑出多种类目：undefined（provider 不存在）、no-key（没有凭据）、
	// duplicate（同一个候选写了两遍）、excluded（被排除的另一份 profile）。
	seedProviders(t, `{"providers":{
		"nokey":{"base_url":"https://nokey.example"},
		"ark":{"base_url":"https://ark.example/v3","api_key":"k"}
	}}`)
	f.seedFile("demo.kv", strings.Join([]string{
		"heavy=ghost/model,nokey/m,ark/deepseek-v3,ark/deepseek-v3",
		"",
	}, "\n"))
	f.seedFile("side.kv", "prio=90\nexcluded=true\nheavy=ark/deepseek-v3\n")
	f.setDefault("demo")

	all, err := f.cfg.AllChains()
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, one := range all {
		for _, chain := range one.Keys {
			for _, s := range chain.Skips {
				seen[skipKind(s)] = true
			}
		}
	}
	if len(seen) < 3 {
		t.Fatalf("这份配置该凑出好几类跳过，实际只有 %v——下面的断言会空转成绿的", seen)
	}
	for kind := range seen {
		known := false
		for _, k := range skipKindOrder {
			if k == kind {
				known = true
			}
		}
		if !known {
			t.Errorf("跳过类目 %q 不在这张表里：它会被静默归进 %q，数量对不上。"+
				"resolve 改了标记名？", kind, skipOther)
		}
	}
	// 换一个方向：表里认得出的类目，措辞不能把它写成兜底那句。
	for _, kind := range skipKindOrder {
		if kind == skipOther {
			continue
		}
		if seen[kind] && skipLabel(kind) == skipLabel(skipOther) {
			t.Errorf("类目 %q 被标成了「其他」的说法", kind)
		}
	}
}

// ---------- 行 ID ----------

// TestRowIDsAreProfileSlashTier：行 ID 是 `profile/tier`，而且在**整个概念里唯一**。
//
// 档位名单独一个不够用：`heavy` 在十份 profile 里出现十次，而「把这一档换成 X」
// 要动的是某一份文件里的某一行（见 view.ChainRow.ID）。两份 profile 各有 heavy 时
// 两个 ID 必须分得开——分不开的那个后果不是显示错，是**点了一个按钮改了另一份文件**。
func TestRowIDsAreProfileSlashTier(t *testing.T) {
	f := newFixture(t)
	f.seedFile("demo.kv", "heavy=ark/deepseek-v3,ark/deepseek-lite\n")
	f.seedFile("alt.kv", "heavy=zhipu/glm-4,zhipu/glm-4-flash\n")
	f.setDefault("demo")

	ch := f.chains()
	if len(ch.Cards) != 2 {
		t.Fatalf("两份档位文件该是两张卡，实际 %d 张", len(ch.Cards))
	}
	if ch.Cards[0].Profile != "demo" {
		t.Errorf("生效的那一份排在最前（端口给的次序要原样保留），实际首张是 %q", ch.Cards[0].Profile)
	}

	ids := map[string]string{}
	for _, card := range ch.Cards {
		for _, row := range card.Roles {
			want := card.Profile + "/" + row.Tier
			if row.ID != want {
				t.Errorf("%s 的 %s 行 ID 该是 %q，实际 %q", card.Profile, row.Tier, want, row.ID)
			}
			if prev, dup := ids[row.ID]; dup {
				t.Errorf("行 ID %q 出现了两次（%s 与 %s）——"+
					"点了这一行会改到那一行", row.ID, prev, card.Profile)
			}
			ids[row.ID] = card.Profile
		}
	}
	// 两份 profile 都写了 heavy，两个 ID 必须分得开。
	if ids["demo/heavy"] != "demo" || ids["alt/heavy"] != "alt" {
		t.Errorf("两份 heavy 的行 ID 没分开：%v", ids)
	}
}

// ---------- 动作：原样透传，且真的跑得起来 ----------

// TestRowActionsArePassedThroughUntouched：config 构造好的动作**原样**出现在对应行上
// （数量与 ID 都一致），而且跑一下真的改到盘上。
//
// 为什么不是「形状对了就行」：那些闭包捕获的是**这一份文件的这一档**（文件路径、
// 此刻的基线、换成谁），它们是 config 的知识，这一层只是搬运。搬运错了的两种方式
// 都很安静——挂错了行（点了改别的档）、或者重造了一份动作（写盘的知识在发行版里
// 长出了第二份）。所以这里与端口逐条比 ID，并且真的 `Run()` 一次看盘。
//
// 跑那一下走的是 view 给行准备的入口（RunRowAction）：那正是界面点按钮时走的路，
// 而它按 `(概念 ID, 行 ID, 动作 ID)` 三步定位——行 ID 那一步错了，这里就会报
// 「这一行没有这个动作」。
func TestRowActionsArePassedThroughUntouched(t *testing.T) {
	f := newFixture(t)
	f.seedFile("demo.kv", "heavy=ark/deepseek-v3,ark/deepseek-lite\n")
	f.setDefault("demo")

	// 端口给的（这一层的输入）与卡片上的（这一层的输出）必须逐条一致。
	all, err := f.cfg.AllChains()
	if err != nil {
		t.Fatal(err)
	}
	var want []view.Action
	for _, one := range all {
		if one.Profile != "demo" {
			continue
		}
		for _, chain := range one.Keys {
			if chain.Key == "heavy" {
				want = chain.Actions
			}
		}
	}
	if len(want) != 1 {
		t.Fatalf("heavy 有两个候选（第一个是链头、不给按钮），端口该给 1 个动作，实际 %d 个", len(want))
	}

	row := rowIn(t, cardOf(t, f.chains(), "demo"), "heavy")
	if len(row.Actions) != len(want) {
		t.Fatalf("动作该原样透传：端口 %d 个，行上 %d 个", len(want), len(row.Actions))
	}
	for i := range want {
		if row.Actions[i].ID != want[i].ID {
			t.Errorf("第 %d 个动作的 ID 对不上：端口 %q，行上 %q", i, want[i].ID, row.Actions[i].ID)
		}
	}

	// 跑一下：**盘上那一档的次序真的换了**，而且换的是这一份文件。
	if _, err := f.reg.RunRowAction(conceptID, row.ID, row.Actions[0].ID); err != nil {
		t.Fatalf("跑行上的动作失败: %v", err)
	}
	raw, err := store.LoadProfileRaw("demo")
	if err != nil {
		t.Fatal(err)
	}
	if got := raw.Roles["heavy"]; len(got) != 2 || got[0].String() != "ark/deepseek-lite" {
		t.Fatalf("盘上 heavy 的次序没换对: %v", got)
	}
	// 换完之后**重问一遍快照**：链头跟着换（这一节每次重算，不需要被通知）。
	if got := rowIn(t, cardOf(t, f.chains(), "demo"), "heavy").Head; got != "ark/deepseek-lite" {
		t.Errorf("换链头之后这一行该报新的链头，实际 %q", got)
	}
}

// TestRowActionsStayOnTheirOwnCard：另一份 profile 的行上**没有**这一份的动作。
//
// 行 ID 是跨卡唯一的（上面那条），而动作是挂在行上的——两件事合起来的失效方式很安静：
// 构造某张卡时用了**另一份** profile 的结论，用户点 alt 那一行的按钮，改的是 demo
// 的文件。所以这里既断言「没有可换的人时不给按钮」，也断言那一次点击**碰不到别的
// 文件**（界面手里那份快照旧了时，它会拿着一个不存在的动作来问）。
func TestRowActionsStayOnTheirOwnCard(t *testing.T) {
	f := newFixture(t)
	f.seedFile("demo.kv", "heavy=ark/deepseek-v3,ark/deepseek-lite\n")
	// alt 只有一个候选：它不该有任何按钮（没有可换的人）。
	f.seedFile("alt.kv", "heavy=zhipu/glm-4\n")
	f.setDefault("demo")

	ch := f.chains()
	alt := rowIn(t, cardOf(t, ch, "alt"), "heavy")
	if len(alt.Actions) != 0 {
		t.Errorf("alt 的 heavy 只有一个候选，不该有按钮，实际 %d 个", len(alt.Actions))
	}
	if _, err := f.reg.RunRowAction(conceptID, "alt/heavy", "head:ark/deepseek-lite"); err == nil {
		t.Error("alt 那一行上没有 demo 的动作——这一步该报错（界面手里那份快照旧了）")
	}
	// demo 的文件一个字节都不该被那一次点击碰到。
	raw, err := store.LoadProfileRaw("demo")
	if err != nil {
		t.Fatal(err)
	}
	if got := raw.Roles["heavy"]; len(got) != 2 || got[0].String() != "ark/deepseek-v3" {
		t.Errorf("那一次点击碰了别的 profile: %v", got)
	}
}
