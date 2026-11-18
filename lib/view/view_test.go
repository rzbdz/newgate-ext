package view

import (
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

// 账本的行为与渲染无关，所以它在叶子里就能测——界面的测试要拖起 HTTP 与嵌入
// 资源，而这里要锁的是几件很具体的事：**注册不产出**（每一次 CLI 调用都会跑到
// 注册，而没有人会打开界面）、重名必须报错、撤销只撤自己那条、并发安全。

// TestRegisterDoesNotProduce 是这套东西最要紧的一条：登记一个产出函数**不能**
// 顺带把数据算出来。
//
// 它守的不是性能，是「每条 `newgate …` 命令都白读一遍全部配置」那个坑——第一版
// 就是那样（Start 里直接读出全部数据塞进概念），被删掉了。这个测试红了就说明
// 那个坑回来了。
func TestRegisterDoesNotProduce(t *testing.T) {
	r := NewRegistry()
	var calls atomic.Int64
	if _, err := r.Register("config", tsection("config"), func() ([]Concept, error) {
		calls.Add(1)
		return []Concept{{ID: "a", Kind: KindCode}}, nil
	}); err != nil {
		t.Fatal(err)
	}
	if n := calls.Load(); n != 0 {
		t.Fatalf("注册时产出了 %d 次——每一次 CLI 调用都会跑到注册", n)
	}
	if _, err := r.Snapshot(); err != nil {
		t.Fatal(err)
	}
	if n := calls.Load(); n != 1 {
		t.Fatalf("快照时该产出一次，实际 %d 次", n)
	}
}

// TestSnapshotAsksEveryTime：界面上的刷新得是真的刷新——CLI 刚建的档位文件要
// 出现在下一次快照里。缓存一份旧的等于让 daemon 的界面停在启动那一刻。
func TestSnapshotAsksEveryTime(t *testing.T) {
	r := NewRegistry()
	n := 0
	if _, err := r.Register("config", tsection("config"), func() ([]Concept, error) {
		n++
		return []Concept{{ID: "profile." + string(rune('0'+n)), Kind: KindMapping}}, nil
	}); err != nil {
		t.Fatal(err)
	}
	first, _ := r.Snapshot()
	second, _ := r.Snapshot()
	if first[0].ID == second[0].ID {
		t.Fatalf("两次快照拿到了同一个概念（%s）——产出函数只跑了一次", first[0].ID)
	}
}

// TestSnapshotCanAskJustSomeContributors：界面刷新计数器与日志（便宜）时不该
// 顺带把配置那一位也叫醒——那一位要重读并重新解析每一份 profile 与每一个源文件。
// 让「刷一下计数器」付那个成本，是把钱花在没人看的地方。
func TestSnapshotCanAskJustSomeContributors(t *testing.T) {
	r := NewRegistry()
	var cheap, expensive atomic.Int64
	mustCount(t, r, "gateway", &cheap)
	mustCount(t, r, "config", &expensive)

	if _, err := r.Snapshot(); err != nil {
		t.Fatal(err)
	}
	if cheap.Load() != 1 || expensive.Load() != 1 {
		t.Fatalf("不带参数该问全部: gateway=%d config=%d", cheap.Load(), expensive.Load())
	}

	if _, err := r.Snapshot("gateway"); err != nil {
		t.Fatal(err)
	}
	if cheap.Load() != 2 {
		t.Errorf("点名问的那位该被再问一次: %d", cheap.Load())
	}
	if expensive.Load() != 1 {
		t.Errorf("没点名的那位不该被吵醒: %d 次", expensive.Load())
	}

	// 名字不认识（那位没装、或刚被关掉）不报错：刷新是后台行为，为一个不在的
	// 贡献者让整次刷新失败，界面会停在上一帧。
	got, err := r.Snapshot("nobody")
	if err != nil {
		t.Fatalf("不认识的名字不该让刷新失败: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("问了不存在的贡献者，结果该是空的: %+v", got)
	}
}

func mustCount(t *testing.T, r *Registry, name string, n *atomic.Int64) {
	t.Helper()
	if _, err := r.Register(name, tsection(name), func() ([]Concept, error) {
		n.Add(1)
		return []Concept{{ID: name + ".x", Kind: KindSeries}}, nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestSnapshotRejectsDuplicateID(t *testing.T) {
	r := NewRegistry()
	mustRegister(t, r, "one", Concept{ID: "a", Kind: KindCode})
	mustRegister(t, r, "two", Concept{ID: "a", Kind: KindCode})
	_, err := r.Snapshot()
	if err == nil {
		t.Fatal("同一个 ID 被两家报上来该报错——否则界面上只会显示其中一个，" +
			"另一个的修改点永远点不到")
	}
	for _, want := range []string{"one", "two"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("报错要点名两家（谁和谁撞了），缺 %q: %v", want, err)
		}
	}
}

func TestSnapshotRequiresIdentityAndKind(t *testing.T) {
	r := NewRegistry()
	mustRegister(t, r, "x", Concept{Kind: KindCode})
	if _, err := r.Snapshot(); err == nil {
		t.Error("没有 ID 的概念该被拒绝（身份是前端定位它的唯一依据）")
	}
	r2 := NewRegistry()
	mustRegister(t, r2, "x", Concept{ID: "a"})
	if _, err := r2.Snapshot(); err == nil {
		t.Error("没有 Kind 的概念该被拒绝（界面不知道怎么渲染它）")
	}
}

// TestLedgerStampsTheSource：来源由账本填，不用贡献者自己报。报错了就是替别人
// 背锅（报错的文案里点名的是别人），而账本本来就知道这条是谁登记的。
func TestLedgerStampsTheSource(t *testing.T) {
	r := NewRegistry()
	mustRegister(t, r, "config", Concept{ID: "a", Kind: KindCode, Source: "撒谎的模块"})
	got, _ := r.Snapshot()
	if got[0].Source != "config" {
		t.Errorf("Source 该由账本填，实际 %q", got[0].Source)
	}
}

func TestRegisterRejectsEmptySourceAndNilContributor(t *testing.T) {
	r := NewRegistry()
	if _, err := r.Register("", tsection("x"), func() ([]Concept, error) { return nil, nil }); err == nil {
		t.Error("没有来源名的贡献者该被拒绝（报错与分组都要点名）")
	}
	if _, err := r.Register("x", tsection("x"), nil); err == nil {
		t.Error("nil 产出函数该被拒绝（界面上会静默什么都没有）")
	}
}

// TestReleaseOnlyRemovesItsOwn：撤销按**登记的序号**认人，不按名字。同一个模块
// 名重复登记是允许的，后来者不该被前一个的撤销带走。
func TestReleaseOnlyRemovesItsOwn(t *testing.T) {
	r := NewRegistry()
	first, err := r.Register("config", tsection("config"), func() ([]Concept, error) {
		return []Concept{{ID: "a", Kind: KindCode}}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := first(); err != nil {
		t.Fatal(err)
	}
	if all, _ := r.Snapshot(); len(all) != 0 {
		t.Fatalf("撤销之后不该还在: %+v", all)
	}
	if err := first(); err != nil {
		t.Fatalf("撤销该是幂等的: %v", err)
	}
	if _, err := r.Register("config", tsection("config"), func() ([]Concept, error) {
		return []Concept{{ID: "a", Kind: KindCode}}, nil
	}); err != nil {
		t.Fatal(err)
	}
	_ = first() // 再撤一次（幂等，且不许误删后来者那条）
	if all, _ := r.Snapshot(); len(all) != 1 {
		t.Fatalf("后来者的概念被误删了: %+v", all)
	}
}

// TestSnapshotIsSortedAndStable：诊断与前端分组都靠它。同一份装配跑两次，顺序
// 必须一样——不然用户会以为东西变了。
func TestSnapshotIsSortedAndStable(t *testing.T) {
	r := NewRegistry()
	mustRegister(t, r, "gateway", Concept{ID: "z", Kind: KindSeries})
	mustRegister(t, r, "config", Concept{ID: "b", Kind: KindCode}, Concept{ID: "a", Kind: KindCode})
	got, err := r.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"config/a", "config/b", "gateway/z"}
	for i, c := range got {
		if c.Source+"/"+c.ID != want[i] {
			t.Fatalf("Snapshot()[%d] = %s/%s，想要 %s", i, c.Source, c.ID, want[i])
		}
	}
}

// TestApplyCarriesTheEditThrough：概念不只报数据，还带**怎么改**。这条锁的是
// 「写的知识属于拥有那份数据的人」——BFF 只负责把前端那坨 JSON 原样转交。
func TestApplyCarriesTheEditThrough(t *testing.T) {
	r := NewRegistry()
	var got json.RawMessage
	var gotBase string
	mustRegister(t, r, "x", Concept{
		ID: "a", Kind: KindCode,
		Apply: func(edit json.RawMessage, base string) (string, error) {
			got, gotBase = edit, base
			return "sha256:new", nil
		},
	})
	c, err := r.Get("a")
	if err != nil {
		t.Fatal(err)
	}
	if c.Apply == nil {
		t.Fatal("Apply 没进快照——界面就永远写不了")
	}
	rev, err := c.Apply(json.RawMessage(`{"text":"hi"}`), "sha256:old")
	if err != nil || rev != "sha256:new" {
		t.Fatalf("Apply 的返回值该原样回给界面: rev=%q err=%v", rev, err)
	}
	if string(got) != `{"text":"hi"}` || gotBase != "sha256:old" {
		t.Errorf("编辑内容与基线该原样转交: edit=%s base=%s", got, gotBase)
	}
}

// TestGetReportsUnknownConcept：界面拿这个区分 404（前端那份快照过期了，重载
// 就好）与 500（某种真的坏了）。
func TestGetReportsUnknownConcept(t *testing.T) {
	r := NewRegistry()
	_, err := r.Get("nope")
	if !errors.Is(err, ErrUnknownConcept) {
		t.Fatalf("要能 errors.Is(ErrUnknownConcept) 认出来，实际: %v", err)
	}
	if !strings.Contains(err.Error(), "nope") {
		t.Errorf("报错里要有那个 ID（排查时第一个要问的就是它）: %v", err)
	}
}

// TestConcurrentRegisterAndSnapshot：模块的 Start（注册）与界面的渲染（快照）
// 跑在不同 goroutine 上。跑 -race 才有意义。
func TestConcurrentRegisterAndSnapshot(t *testing.T) {
	r := NewRegistry()
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(2)
		id := string(rune('a' + i))
		go func() {
			defer wg.Done()
			rel, err := r.Register("s"+id, tsection("s"+id), func() ([]Concept, error) {
				return []Concept{{ID: id, Kind: KindCode}}, nil
			})
			if err != nil {
				t.Errorf("Register(%s): %v", id, err)
				return
			}
			rel()
		}()
		go func() {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				_, _ = r.Snapshot()
				_, _ = r.Get(id)
			}
		}()
	}
	wg.Wait()
}

func mustRegister(t *testing.T, r *Registry, source string, concepts ...Concept) {
	t.Helper()
	if _, err := r.Register(source, tsection(source), func() ([]Concept, error) { return concepts, nil }); err != nil {
		t.Fatal(err)
	}
}

// tsection 造一个栏目名。绝大多数用例只关心账本的行为，不关心标题长什么样，
// 所以标题就用来源名——**栏目名的断言在 TestSections* 那几条里**，这里不该
// 顺手把它也钉一遍（钉了就会有人为了改测试而改标题）。
func tsection(name string) Section {
	return Title(func() string { return name })
}

// TestSectionTitlesAreAskedEverySnapshot 是「栏目名为什么是个函数」的棘轮。
//
// 界面上的那一栏叫什么由模块自己报，而**报的时候不能翻**：登记发生在各模块的
// Start 里，那时候语言层装好了没有是没有保证的（业务模块不依赖 modules/locale，
// 依赖它的只有发行版那份追加目录的模块）。登记时求值的标题会冻在源语言上，而
// 没有任何东西会因此变红——中文界面上那一栏一直叫 Configuration，看着只像
// 「还没翻」，不像一条 bug。
//
// 所以这条测试盯的不是「标题对不对」，而是**求值发生在哪一刻**：登记之后改掉
// 那句话的来源，下一次问栏名必须拿到新的。
func TestSectionTitlesAreAskedEverySnapshot(t *testing.T) {
	r := NewRegistry()
	title := "first"
	if _, err := r.Register("x", Title(func() string { return title }),
		func() ([]Concept, error) { return nil, nil }); err != nil {
		t.Fatal(err)
	}
	if got := sectionsOf(t, r)["x"]; got != "first" {
		t.Fatalf("栏名该是登记的那个闭包算出来的，实际 %q", got)
	}
	title = "second"
	if got := sectionsOf(t, r)["x"]; got != "second" {
		t.Fatalf("栏名在登记那一刻就定死了（%q）——换个语言它也不会变", got)
	}
}

// TestSectionsListSilentContributors：一个此刻一条概念都产不出来的源，仍然要
// 在侧栏里有一个位置。
//
// 它消失的代价不是「少一行」：用户会以为那个模块没装，然后去别处找（翻配置、
// 问 doctor）。今天真会这样的场景是配置目录整个读不了、omo 还没接管过、一个
// 开关点都没上报——而那时候恰恰最需要看见「这一栏在，只是空的」。
func TestSectionsListSilentContributors(t *testing.T) {
	r := NewRegistry()
	if _, err := r.Register("quiet", tsection("Quiet"),
		func() ([]Concept, error) { return nil, nil }); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Register("loud", tsection("Loud"),
		func() ([]Concept, error) { return []Concept{{ID: "a", Kind: KindCode}}, nil }); err != nil {
		t.Fatal(err)
	}
	got := r.Sections()
	if len(got) != 2 {
		t.Fatalf("两个源都该在栏目列表里，实际 %d 个: %+v", len(got), got)
	}
	if got[0].Source != "loud" || got[1].Source != "quiet" {
		t.Errorf("栏目该按 source 排序（界面顺序要跨重启稳定）: %+v", got)
	}
	if got[1].Title != "Quiet" {
		t.Errorf("没产出的那位也该有名字: %+v", got[1])
	}
}

// TestSectionsSurviveAnEmptyTitle：栏名是一句展示文案。它算不出来时兜成来源名，
// 而不是让整次快照失败——那代价是整个界面白屏，比一栏标题不好看严重得多。
func TestSectionsSurviveAnEmptyTitle(t *testing.T) {
	r := NewRegistry()
	if _, err := r.Register("config", Title(func() string { return "  " }),
		func() ([]Concept, error) { return nil, nil }); err != nil {
		t.Fatal(err)
	}
	if got := sectionsOf(t, r)["config"]; got != "config" {
		t.Errorf("空栏名该兜成来源名（机器标记，至少不是空的）: %q", got)
	}
}

// TestRegisterRejectsNoSectionTitle：漏报栏名要在**登记那一刻**红，不能等到
// 界面渲染——那一刻离这里隔着一次 HTTP 与一次渲染，谁也不会顺着摸回来，症状
// 是侧栏里一栏没有标题（或者顶着一个模块名）。
func TestRegisterRejectsNoSectionTitle(t *testing.T) {
	r := NewRegistry()
	rel, err := r.Register("x", Section{}, func() ([]Concept, error) { return nil, nil })
	if err == nil {
		_ = rel()
		t.Fatal("没报栏目名该被拒绝（用 view.Title）")
	}
	if !strings.Contains(err.Error(), "x") {
		t.Errorf("报错要点名是哪个源: %v", err)
	}
}

// TestNobodyDeclaresALandingByDefault：没人声明落点时，**行为与这条改动之前一模
// 一样**——一个 Default 都没有，界面照它原来那套规矩挑（第一个有卡片的那一栏）。
//
// 这条是这次改动的安全绳：装 `home` 的装配里首屏换了地方，不装的那些（`dist.json`
// 之外的四份规格书、内核自己的测试图）必须一个字都没变。破了它就是一次静默的
// 行为变更——而「首屏落在哪」没有任何测试会因此变红。
func TestNobodyDeclaresALandingByDefault(t *testing.T) {
	r := NewRegistry()
	for _, name := range []string{"breaker", "config", "gateway"} {
		if _, err := r.Register(name, tsection(name), func() ([]Concept, error) { return nil, nil }); err != nil {
			t.Fatal(err)
		}
	}
	for _, s := range r.Sections() {
		if s.Default {
			t.Errorf("%s 被标成了落点，而没人声明过", s.Source)
		}
	}
}

// TestLandingIsTheDeclaredSection：声明了落点的那一栏带上它，其余不带。
//
// 它端给界面的是 `default: true` 这一格（SectionInfo.Default），前端按它决定首屏
// 落在哪儿——在这之前，那件事是「谁的名字按字母序排在前面」这个偶然。
func TestLandingIsTheDeclaredSection(t *testing.T) {
	r := NewRegistry()
	if _, err := r.Register("breaker", tsection("Breaker"), func() ([]Concept, error) { return nil, nil }); err != nil {
		t.Fatal(err)
	}
	// 名字刻意排在字母序**最后**：落点靠声明，不靠在字母表里插队——插队那套
	// （把 source 起成 `aaa-home`）是这次改动要消灭的东西。
	if _, err := r.Register("home", tsection("Home").Landing(), func() ([]Concept, error) { return nil, nil }); err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, s := range r.Sections() {
		got[s.Source] = s.Default
	}
	if !got["home"] {
		t.Errorf("声明了 Landing 的那一栏没被标成落点: %+v", got)
	}
	if got["breaker"] {
		t.Errorf("没声明的那一栏不该是落点: %+v", got)
	}
}

// TestSeveralLandingsResolveBySource：两个模块都想当落点（那是发行版装机时的
// 选择冲突，不是谁写错了）时，按 Source 取最小的那个。
//
// 为什么要一条确定性规则：不定它的话，被选中的那个取决于各模块 Start 的先后，
// 而那种「装法一变、首屏就换地方」的错没有任何东西会红。取最小与 Sections 的
// 排序用同一把尺子，看的人因此不会困惑。
func TestSeveralLandingsResolveBySource(t *testing.T) {
	r := NewRegistry()
	for _, name := range []string{"zebra", "apple"} {
		if _, err := r.Register(name, tsection(name).Landing(), func() ([]Concept, error) { return nil, nil }); err != nil {
			t.Fatal(err)
		}
	}
	var landed []string
	for _, s := range r.Sections() {
		if s.Default {
			landed = append(landed, s.Source)
		}
	}
	if len(landed) != 1 || landed[0] != "apple" {
		t.Fatalf("多个落点该只留 source 最小的那个，实际 %v", landed)
	}
}

func sectionsOf(t *testing.T, r *Registry) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, s := range r.Sections() {
		out[s.Source] = s.Title
	}
	return out
}

// Row 上的动作按**行**定位：跑哪一个行的按钮，就只跑那一个。
//
// 这条锁的是「行 ID 真的参与了定位」——少了它，「把这一档换成 X」在一张有十行的卡上
// 会变成「跑第一行里叫这个名字的那个按钮」，而那种错的表现是**改错了别的行**。
func TestRunRowActionFindsTheRightRow(t *testing.T) {
	r := NewRegistry()
	var ran string
	btn := func(id string) Action {
		return Action{ID: id, Label: func() string { return id },
			Run: func() (string, error) { ran = id; return "", nil }}
	}
	mustRegister(t, r, "x", Concept{
		ID: "t", Kind: KindTable,
		Data: Table{Rows: []Row{
			{ID: "a", Cells: map[string]Cell{}, Actions: []Action{btn("probe")}},
			{ID: "b", Cells: map[string]Cell{}, Actions: []Action{btn("probe")}},
		}},
	})
	// 两行上有一个**同名**的按钮：这正是「按行定位」要能区分的那种情况。
	if _, err := r.RunRowAction("t", "b", "probe"); err != nil {
		t.Fatal(err)
	}
	if ran != "probe" {
		t.Fatalf("按钮没跑起来: %q", ran)
	}
	// 两行跑的都是同一个闭包，所以上面那条分不出 a/b——换一个能分的：每行各跑
	// 它自己的那个闭包（Run 返回行 ID）。
	var which string
	rowBtn := func(row string) Action {
		return Action{ID: "go", Label: func() string { return row },
			Run: func() (string, error) { which = row; return "", nil }}
	}
	r2 := NewRegistry()
	mustRegister(t, r2, "x", Concept{
		ID: "t", Kind: KindTable,
		Data: Table{Rows: []Row{
			{ID: "a", Cells: map[string]Cell{}, Actions: []Action{rowBtn("a")}},
			{ID: "b", Cells: map[string]Cell{}, Actions: []Action{rowBtn("b")}},
		}},
	})
	if _, err := r2.RunRowAction("t", "b", "go"); err != nil {
		t.Fatal(err)
	}
	if which != "b" {
		t.Fatalf("跑的是 %q 行，要的是 b 行", which)
	}
}

// 链卡（KindChains）上的行**也能挂动作**，而且行 ID 是全概念唯一的。
//
// 这条是 2026-09-22 加 chains 时的那个缺口：第一版 RunRowAction 只认 Table，于是
// 「一行的去处」这件事被渲染形状绑住了。判据两条——链卡里的动作跑得起来，以及
// **档位名单独一个不够用**（十份 profile 就有十个 heavy，只报 tie 会改错那一份）。
func TestRunRowActionWorksOnChainCards(t *testing.T) {
	r := NewRegistry()
	var got string
	head := func(row string, target string) Action {
		return Action{ID: "head:" + target, Label: func() string { return target },
			Run: func() (string, error) { got = row + "→" + target; return "", nil }}
	}
	mustRegister(t, r, "x", Concept{
		ID: "chains", Kind: KindChains,
		Data: Chains{Cards: []ChainCard{
			{Profile: "demo", Default: true, Roles: []ChainRow{
				{ID: "demo/heavy", Tier: "heavy", Actions: []Action{head("demo/heavy", "ark/lite")}},
			}},
			{Profile: "alt", Roles: []ChainRow{
				{ID: "alt/heavy", Tier: "heavy", Actions: []Action{head("alt/heavy", "zhipu/glm-4")}},
			}},
		}},
	})

	if _, err := r.RunRowAction("chains", "alt/heavy", "head:zhipu/glm-4"); err != nil {
		t.Fatal(err)
	}
	if got != "alt/heavy→zhipu/glm-4" {
		t.Fatalf("跑错行了：%q（两边都有叫 heavy 的行，只有行 ID 能分开它们）", got)
	}

	// 一行上**没有**这个动作时，报错要说得清是哪一行——界面手里那份快照旧了，
	// 而它唯一能拿到的线索就是这句话。
	_, err := r.RunRowAction("chains", "demo/heavy", "head:nope")
	if err == nil {
		t.Fatal("那一行上没有这个按钮，该报错")
	}
	if !strings.Contains(err.Error(), "demo/heavy") || !strings.Contains(err.Error(), "head:nope") {
		t.Errorf("报错该点明行与动作，实际: %v", err)
	}
}

// 一种 Kind 的行上不可能有动作时，报错说的是**「这种形状没有行动作」**，而不是
// 装作那一行存在过。
//
// 为什么这条值得有：加一种带行的 Kind 时忘了在 rowActions 里补一支，症状会是一句
// 「这一行不存在」——排查的人于是去查数据，而真相是代码里少了一个 case。
func TestRunRowActionOnAKindWithoutRows(t *testing.T) {
	r := NewRegistry()
	mustRegister(t, r, "x", Concept{ID: "c", Kind: KindCode, Data: struct{}{}})
	_, err := r.RunRowAction("c", "whatever", "go")
	if err == nil {
		t.Fatal("code 那一种没有行，该报错")
	}
	if !strings.Contains(err.Error(), KindCode) {
		t.Errorf("报错该说出是哪种 Kind 没有行动作，实际: %v", err)
	}
}

// 总览那一张卡上的按钮长在**卡片**上（一个 profile 一个「用它」），而
// RunConceptAction 找的是概念上的动作——所以它得按 Kind 往数据里看一眼。
//
// 为什么这条值得有两条断言：**十几张卡上都有同一族动作**（每份 profile 一个「给
// claude 用」），而账本按 ID 取的是第一个匹配。ID 里不带 profile 的话，用户想切到
// 第三张卡，实际改的是第一张——那是静默改错东西，界面上一点异样都没有。
func TestRunConceptActionReachesOverviewCards(t *testing.T) {
	r := NewRegistry()
	var got string
	use := func(id, profile string) Action {
		return Action{ID: id, Label: func() string { return "use " + profile },
			Run: func() (string, error) { got = profile; return "", nil }}
	}
	mustRegister(t, r, "x", Concept{
		ID: "overview", Kind: KindOverview,
		Data: Overview{Cards: []OverviewCard{
			{Profile: "production", Actions: []Action{use("use:claude:production", "production")}},
			{Profile: "cheap", Actions: []Action{use("use:claude:cheap", "cheap")}},
		}},
	})

	if _, err := r.RunConceptAction("overview", "use:claude:cheap"); err != nil {
		t.Fatal(err)
	}
	if got != "cheap" {
		t.Fatalf("跑错卡了：改到了 %q（两段机器标记缺一段就会这样）", got)
	}

	// 卡片上**没有**这个按钮时，报错要说得清是哪个动作——界面手里那份快照旧了，
	// 而它唯一能拿到的线索就是这句话。
	_, err := r.RunConceptAction("overview", "use:codex:cheap")
	if err == nil {
		t.Fatal("没有这个按钮，该报错")
	}
	if !strings.Contains(err.Error(), "use:codex:cheap") {
		t.Errorf("报错该点明动作，实际: %v", err)
	}
}

// 数据形状与 Kind 对不上时要**说出这句话**，不装作那一张卡上没有这个按钮。
func TestRunConceptActionOnAMismatchedOverview(t *testing.T) {
	r := NewRegistry()
	mustRegister(t, r, "x", Concept{ID: "c", Kind: KindOverview, Data: struct{}{}})
	_, err := r.RunConceptAction("c", "any")
	if err == nil {
		t.Fatal("形状对不上，该报错")
	}
	if !strings.Contains(err.Error(), "Overview") {
		t.Errorf("报错该说出形状不对，实际: %v", err)
	}
}

// 「这个客户端装没装」在报文上必须是**两个能分开的取值**。
//
// 它防的是 `omitempty` 这类看着无害的省字节：`Ready` 的两头都有意义（true = 装了、
// false = 这台机器上没有），省掉 `false` 之后两者在 JSON 里都是「字段缺席」，界面
// 于是既画不出那一栏的灰、也画不出「未安装」那个记号。它不报任何错——那两处只是
// 永远不出现。实测过：线上快照里没装的那个客户端报出来是 `None`。
func TestOverviewAgentSaysWhetherItIsInstalled(t *testing.T) {
	raw, err := json.Marshal(Overview{Agents: []OverviewAgent{
		{ID: "claude", Name: "claude", Profile: "production", Ready: true},
		{ID: "codex", Name: "codex", Profile: "production", Ready: false},
	}})
	if err != nil {
		t.Fatal(err)
	}
	var back struct {
		Agents []map[string]any `json:"agents"`
	}
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	for i, want := range []bool{true, false} {
		v, ok := back.Agents[i]["ready"]
		if !ok {
			t.Fatalf("第 %d 个客户端的报文里没有 ready：%s", i, raw)
		}
		if v != want {
			t.Errorf("第 %d 个的 ready 该是 %v，实际 %v", i, want, v)
		}
	}
}

// 「这一家是固定住的，还是跟着全局默认走的」也必须在报文上是**两个能分开的取值**。
//
// 这两件事此刻可能解析成同一份 profile（固定选 ds、而全局恰好也是 ds），但改掉全局
// 之后一个跟着变、一个不变——报文里只有 profile 的话，界面只能把它们说成同一句话，
// 于是用户看不出「我改全局会不会连带改了这一家」，「把这一家改回自动」那个动作也
// 没有前提可判（它成立的条件正是「它现在自己有设置」）。
func TestOverviewAgentSaysWhetherItIsPinned(t *testing.T) {
	raw, err := json.Marshal(Overview{Agents: []OverviewAgent{
		{ID: "claude", Profile: "ds", Own: false},
		{ID: "codex", Profile: "ds", Own: true},
	}})
	if err != nil {
		t.Fatal(err)
	}
	var back struct {
		Agents []map[string]any `json:"agents"`
	}
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	// 两个人指向**同一份** profile，唯一分得开它们的就是这一格。
	if back.Agents[0]["profile"] != back.Agents[1]["profile"] {
		t.Fatalf("这一条的前提取的是两份 profile 一样：%s", raw)
	}
	for i, want := range []bool{false, true} {
		v, ok := back.Agents[i]["own"]
		if !ok {
			t.Fatalf("第 %d 个客户端的报文里没有 own：%s", i, raw)
		}
		if v != want {
			t.Errorf("第 %d 个的 own 该是 %v，实际 %v", i, want, v)
		}
	}
}

// 「默认」那张卡上的动作**也必须够得着**。
//
// 它不在 Cards 里（它不是一个 profile），而在 Overview.Auto 上，所以遍历 Cards 的
// 那一版会漏掉它——症状是那张卡上每个按钮点下去都回一句「没有这个动作」：成员
// （`use:global:<profile>`）与「跟着默认走」（`auto:<agent>`）**只**长在它身上，
// 漏掉它等于把「改默认」和「改回自动」两条路一起断掉。
func TestRunConceptActionReachesTheAutoCard(t *testing.T) {
	r := NewRegistry()
	var got string
	mustRegister(t, r, "x", Concept{
		ID: "overview", Kind: KindOverview,
		Data: Overview{
			Auto: &OverviewCard{Auto: true, Profile: "production", Actions: []Action{
				{ID: "use:global:cheap", Label: func() string { return "cheap" },
					Run: func() (string, error) { got = "global"; return "", nil }},
				{ID: "auto:claude", Label: func() string { return "use for claude" },
					Run: func() (string, error) { got = "auto"; return "", nil }},
			}},
			Cards: []OverviewCard{{Profile: "cheap"}},
		},
	})

	if _, err := r.RunConceptAction("overview", "use:global:cheap"); err != nil {
		t.Fatal(err)
	}
	if got != "global" {
		t.Errorf("「默认」那张卡的成员动作没被跑到，实际 %q", got)
	}
	if _, err := r.RunConceptAction("overview", "auto:claude"); err != nil {
		t.Fatal(err)
	}
	if got != "auto" {
		t.Errorf("「跟着默认走」没被跑到，实际 %q", got)
	}
}

// 一个下拉的取值是空串时，那一格**也必须出现在报文里**。
//
// 「跟随系统」「跟随全局缺省」这些档位的机器取值就是空串，而空串对 select 是一个
// **合法取值**，不是「没有这一格」。省掉它，前端拿到 undefined，只能自己 `?? ""`；
// 那个兜底恰好对，但它是运气——选项表里不含空串的下拉会因此显示成第一项，而实际
// 存的是空，屏幕上说的与盘上存的不一样，且没有任何东西会红。
func TestToggleValueIsAlwaysOnTheWire(t *testing.T) {
	raw, err := json.Marshal(Toggles{Items: []ToggleItem{
		{ID: "theme", Kind: ToggleSelect, Label: "Theme", Options: []string{"", "dark"}},
		{ID: "on", Kind: ToggleSwitch, Label: "On", On: true},
	}})
	if err != nil {
		t.Fatal(err)
	}
	var back struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	for i, it := range back.Items {
		if _, ok := it["value"]; !ok {
			t.Errorf("第 %d 格的报文里没有 value：%s", i, raw)
		}
	}
}

// 栏目动作的 Tone **必须过到报文里**。
//
// 它有一条自己的转换路（SectionInfo 那里手工拼 ActionInfo），与 Action.MarshalJSON
// 是两条——2026-09-29 实测：`auto fallback` 那颗状态按钮在卡片动作上是亮的、在栏目
// 动作上永远不亮，因为那条路把 Tone 丢了。同一条信息有两条序列化路径时，「一条记得、
// 一条忘了」是最容易长出来的错，而它的症状只是一个颜色不对。
// Tone 是**每次快照求值**的，不是登记那一刻取下来的。
//
// 与 Label 同一条理由，但更硬：Label 是文案，取了就取了；Tone 是**状态**——动作在
// Start 里登记一次，而快照每次请求重算一次。当场取值的话，颜色永远停在进程启动时
// 那一刻，实测踩过（2026-09-30）：`newgate fallback off` 之后按钮还是绿的。
func TestActionToneIsReadAtSnapshotTime(t *testing.T) {
	r := NewRegistry()
	on := true
	mustRegister(t, r, "x", Concept{ID: "c", Kind: KindRecords, Data: Records{},
		Actions: []Action{{
			ID:    "fallback",
			Label: func() string { return "auto fallback" },
			Tone: func() string {
				if on {
					return ToneOK
				}
				return ToneBad
			},
			Run: func() (string, error) { return "", nil },
		}},
	})

	toneOf := func() string {
		t.Helper()
		list, err := r.Snapshot()
		if err != nil {
			t.Fatal(err)
		}
		for _, c := range list {
			if len(c.Actions) == 1 {
				// 走一遍端出去的那条路（前端拿到的就是它）。
				raw, err := json.Marshal(c.Actions[0])
				if err != nil {
					t.Fatal(err)
				}
				var back struct {
					Tone string `json:"tone"`
				}
				if err := json.Unmarshal(raw, &back); err != nil {
					t.Fatal(err)
				}
				return back.Tone
			}
		}
		t.Fatal("找不到那颗动作")
		return ""
	}

	if got := toneOf(); got != ToneOK {
		t.Fatalf("开着时该是 %q，实际 %q", ToneOK, got)
	}
	on = false
	if got := toneOf(); got != ToneBad {
		t.Errorf("关掉之后**下一次快照**就该变红，实际 %q——Tone 是在登记那一刻取的？", got)
	}
}

func TestSectionActionToneReachesTheWire(t *testing.T) {
	r := NewRegistry()
	bad := "bad"
	if _, err := r.Register("x",
		Title(func() string { return "x" }).Does(Action{
			ID:    "fallback",
			Label: func() string { return "auto fallback" },
			Tone:  func() string { return bad },
			Run:   func() (string, error) { return "", nil },
		}),
		func() ([]Concept, error) {
			return []Concept{{ID: "c", Kind: KindRecords, Data: Records{}}}, nil
		}); err != nil {
		t.Fatal(err)
	}
	for _, s := range r.Sections() {
		if s.Source != "x" || len(s.Actions) != 1 {
			continue
		}
		if s.Actions[0].Tone != bad {
			t.Errorf("栏目动作的 Tone 该是 %q，实际 %q——那条手工拼 ActionInfo 的路漏了它", bad, s.Actions[0].Tone)
		}
		return
	}
	t.Fatal("账本里没有那一栏")
}

// 栏目的 Fields（这一栏是干什么的）要**一路到报文**。
//
// 它决定界面在目录里把这一栏归到哪一类——丢了它，侧栏就退回「按贡献者自己写的分组
// 名聚堆」，而那种自由文本在两个发行版之间分不到一起。
func TestSectionFieldsReachTheWire(t *testing.T) {
	r := NewRegistry()
	mustRegister(t, r, "x", Concept{ID: "c", Kind: KindRecords, Data: Records{}})
	if _, err := r.Register("y", Title(func() string { return "y" }).Of(FieldSettings),
		func() ([]Concept, error) { return nil, nil }); err != nil {
		t.Fatal(err)
	}
	seen := false
	for _, s := range r.Sections() {
		if s.Source != "y" {
			continue
		}
		seen = true
		if s.Fields != FieldSettings {
			t.Errorf("Fields 该是 %q，实际 %q", FieldSettings, s.Fields)
		}
	}
	if !seen {
		t.Fatal("账本里没有那一栏")
	}
}

// TestRecordsBlankReachesTheWire：新增一条时的字段形状必须真的到得了界面。
//
// 这条防的是一个**只有空集才显形**的失效：字段的形状长在每一条记录上，所以前端
// 渲染一张新卡片时只能去抄一条现成的。一条都没有的时候无处可抄——点「新增」得到
// 一张一个输入框都没有的空卡片，而用户没有任何办法把内容填进去。
//
// 装机就是那个状态（一份还没配过任何东西的配置），所以它不是边角。
func TestRecordsBlankReachesTheWire(t *testing.T) {
	blank := []Field{
		{ID: "host", Label: "host", Kind: FieldText},
		{ID: "mode", Label: "mode", Kind: FieldSelect, Options: []string{"a", "b"}},
	}
	raw, err := json.Marshal(Records{CanAdd: true, AddLabel: "add", Blank: blank})
	if err != nil {
		t.Fatal(err)
	}
	var back struct {
		Blank []struct {
			ID      string   `json:"id"`
			Kind    string   `json:"kind"`
			Options []string `json:"options"`
		} `json:"blank"`
	}
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatalf("界面上读到的这一格解不出来：%v（%s）", err, raw)
	}
	if len(back.Blank) != 2 {
		t.Fatalf("blank 该有 2 格，实际 %d（%s）", len(back.Blank), raw)
	}
	if back.Blank[0].ID != "host" || back.Blank[0].Kind != FieldText {
		t.Errorf("第一格不对：%+v", back.Blank[0])
	}
	// Options 也要过去：下拉的候选只有贡献者知道（协议只有两家、档位只有四档）。
	if len(back.Blank[1].Options) != 2 {
		t.Errorf("下拉的候选丢了：%+v", back.Blank[1])
	}
}

// TestCellHrefReachesTheWire：一格里那个可点的地址必须真的到得了界面。
//
// 判据在这里而不是在贡献者那边：地址往往是**算出来的**（「这个远端界面挂在这个
// 端口的这个前缀下」），贡献者把它一并交出来，界面只负责画成一个 <a>。少了这一格
// 的搬运，那个地址在界面上就只是一段**长得像地址的文字**——用户全选、复制、粘到
// 浏览器里，每一步都可能出错，而它看起来完全正常。
func TestCellHrefReachesTheWire(t *testing.T) {
	raw, err := json.Marshal(Table{
		Columns: []Column{{ID: "route", Label: "route"}},
		Rows: []Row{{ID: "r", Cells: map[string]Cell{
			"route": {Text: "/ui/remote/ds/", Href: "/ui/remote/ds/"},
			"plain": {Text: "no link here"},
		}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	var back struct {
		Rows []struct {
			Cells map[string]struct {
				Text string `json:"text"`
				Href string `json:"href"`
			} `json:"cells"`
		} `json:"rows"`
	}
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatalf("解不出来：%v（%s）", err, raw)
	}
	if got := back.Rows[0].Cells["route"].Href; got != "/ui/remote/ds/" {
		t.Errorf("Href = %q（%s）", got, raw)
	}
	// 没有链接的格子不该凭空长出一个 href——空串是「这不是一个链接」。
	if _, present := back.Rows[0].Cells["plain"]; !present {
		t.Fatal("普通格子丢了")
	}
	if strings.Contains(string(raw), `"plain":{"text":"no link here","href"`) {
		t.Errorf("普通格子被写上了 href：%s", raw)
	}
}
