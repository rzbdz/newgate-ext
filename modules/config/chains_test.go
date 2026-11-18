package config

import (
	"os"
	"strings"
	"testing"

	"github.com/rzbdz/newgate-ext/modules/config/domain"
	"github.com/rzbdz/newgate-ext/modules/config/paths"
	"github.com/rzbdz/newgate-ext/modules/config/resolve"
	"github.com/rzbdz/newgate-ext/modules/config/store"
	"github.com/rzbdz/newgate-ext/testing/testkit"
)

// 这一对锁的是 Config 端口上那个**读**（Chains），因为它是「一屏看全部链」这件事
// 唯一的数据来源，而它的正确性不体现在画面上——画面上链少一站、跳过少一类，看起来
// 都像「这台机器就是这么配的」。所以这里逐条断言那几个**只有读端口知道**的事实：
// 顺序是调用方给的、profile 不存在要报错、maxAttempts 不许剪链、一次快照一个时刻。

// chainsFor 是唯一一处允许这么取端口的地方：这里测的正是那个方法本身。
func chainsFor(t *testing.T, profile string, keys ...string) *Chains {
	t.Helper()
	out, err := (&port{}).Chains(profile, keys...)
	if err != nil {
		t.Fatalf("Chains(%q, %v) 失败: %v", profile, keys, err)
	}
	return out
}

// seedProvidersIn **不重复开沙箱**：testkit.Sandbox 每次调用都换一个新的临时目录
// （`t.TempDir()` 每次给一个唯一路径），所以在同一个测试里再开一次会把前面 seed 的
// 那几个文件全落在上一份目录里——症状是「明明写了 demo.kv 却说没有这份档位」。
// providers_test.go 里那个 seedProviders 自带 Sandbox，是因为它的每个测试都从零开始。
func seedProvidersIn(t *testing.T, body string) {
	t.Helper()
	if err := os.MkdirAll(paths.Root(), 0o770); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.ProvidersFile(), []byte(body), 0o660); err != nil {
		t.Fatal(err)
	}
}

// 布局：一份 demo 打头（它是默认，所以是链头），一份 alt 挂在后面。
//
// demo 自己把 heavy 绑到 ark，别的档位靠 @heavy 引用；alt 补一个 demo 没有的
// light。这样一次就能同时看到「链头是 demo」「跨 profile 的链」「引用展开」。
func seedChains(t *testing.T) {
	t.Helper()
	testkit.Sandbox(t)
	seedProvidersIn(t, `{"providers":{
		"ark":{"base_url":"https://ark.example/v3","api_key":"k"},
		"zhipu":{"base_url":"https://zhipu.example","api_key":"k"},
		"side":{"base_url":"https://side.example","api_key":"k"}
	}}`)
	seedFile(t, "demo.kv", strings.Join([]string{
		"desc=demo base",
		"heavy=ark/deepseek-v3,ark/deepseek-lite",
		"normal=@heavy",
		"",
	}, "\n"))
	seedFile(t, "alt.kv", strings.Join([]string{
		"desc=alt base",
		"prio=50",
		"heavy=zhipu/glm-4",
		"light=zhipu/glm-4-flash",
		"",
	}, "\n"))
	if err := store.SetDefaultProfile("demo", false); err != nil {
		t.Fatalf("设默认档位失败: %v", err)
	}
}

// 空 profile = 此刻全局生效的那一份，且要**自报**它是不是生效的那一份。
//
// 这一格是界面上「当前配置」那个绿色标记的唯一来源（与 profileConcept 的 Note
// 同一件事）。读端口不报的话，界面只能自己拿 state.json 里的默认名去比——那又是
// 把配置的知识抄进界面。
func TestChainsWithoutAProfileMeansTheDefaultOne(t *testing.T) {
	seedChains(t)

	out := chainsFor(t, "", "heavy")
	if out.Profile != "demo" {
		t.Fatalf("没给 profile 时应当落在默认的那一份（demo），实际 %q", out.Profile)
	}
	if !out.Default {
		t.Error("demo 就是此刻生效的那一份，Default 应当是 true")
	}
	if got := out.Keys[0].Steps[0].Binding.String(); got != "ark/deepseek-v3" {
		t.Errorf("链头应当是 ark/deepseek-v3，实际 %q", got)
	}

	// 点名另一份：链头跟着换，而 Default 必须翻成 false（它不是生效的那一份）。
	alt := chainsFor(t, "alt", "heavy")
	if alt.Default {
		t.Error("alt 不是生效的那一份，Default 应当是 false")
	}
	if got := alt.Keys[0].Steps[0].Binding.String(); got != "zhipu/glm-4" {
		t.Errorf("点名 alt 之后链头应当是 zhipu/glm-4，实际 %q", got)
	}
}

// 档位顺序**原样还回去**：谁在前谁在后是产品取舍（`domain.Roles` 是能力从高到低），
// 读端口不重排。
//
// 这条断言防的是「顺手按字母序排一下」——那会让界面上的档位次序变成 h/l/m/n，
// 而 `newgate status` 那边仍是能力序，同一份配置两个屏幕两种排法。
func TestChainsKeepsTheCallersKeyOrder(t *testing.T) {
	seedChains(t)

	out := chainsFor(t, "alt", "light", "heavy")
	var got []string
	for _, k := range out.Keys {
		got = append(got, k.Key)
	}
	if strings.Join(got, ",") != "light,heavy" {
		t.Errorf("键的次序应当是调用方给的 light,heavy，实际 %v", got)
	}

	// 没给键 = 跟着 domain.Roles（框架那五档）。
	all := chainsFor(t, "alt")
	if len(all.Keys) != len(domain.Roles) {
		t.Fatalf("没给键时应当列出全部 %d 档，实际 %d 档", len(domain.Roles), len(all.Keys))
	}
	for i, k := range all.Keys {
		if k.Key != domain.Roles[i] {
			t.Errorf("第 %d 档应当是 %q，实际 %q", i, domain.Roles[i], k.Key)
		}
	}
}

// 不存在的 profile **报错**，不给一份空链。
//
// 空链的形状是「这份档位什么都没配」，而事实是「没有这份档位」。静默的话界面上
// 会画出一张空卡，读的人以为自己的配置丢了——而真正的原因（名字打错、文件被删）
// 一个字都看不到。
func TestChainsRejectsAnUnknownProfile(t *testing.T) {
	seedChains(t)

	_, err := (&port{}).Chains("nope", "heavy")
	if err == nil {
		t.Fatal("问一份不存在的档位应当报错，实际给了答案")
	}
	if !strings.Contains(err.Error(), "nope") {
		t.Errorf("报错里应当带上问的那个名字，实际 %q", err.Error())
	}
}

// 链**不截断**：maxAttempts 是单次请求的执行上限，不是链的 membership。
//
// 这条锁的是一个真实踩过的观感 bug（2026-09-17）：`newgate status` 传了 Attempts()
// 之后第 4 站被静默裁掉，于是「normal 档里怎么没有 ark」查了半天——ark 一直在链上，
// 只是屏幕上看不见。读端口要是也这么传，界面会把同一句话再说一遍。
func TestChainsDoesNotTruncateAtMaxAttempts(t *testing.T) {
	seedChains(t)

	// 把执行上限压到 1 再读：链必须还是完整的那几条。
	st := store.LoadState()
	st.Chain.MaxAttempts = 1
	if err := store.SaveState(st); err != nil {
		t.Fatalf("写 state 失败: %v", err)
	}

	out := chainsFor(t, "demo", "heavy")
	steps := out.Keys[0].Steps
	if len(steps) < 2 {
		t.Fatalf("heavy 该有 2 站（ark/deepseek-v3, ark/deepseek-lite），"+
			"实际 %d 站：maxAttempts 不该剪链", len(steps))
	}
	for _, s := range out.Keys[0].Skips {
		if s.Kind == resolve.SkipMaxSteps {
			t.Errorf("出现了 %q 这类跳过：读端口不该按 maxAttempts 截断", resolve.SkipMaxSteps)
		}
	}
}

// 引用的链**跨 profile**，而且 Step 自己报来源。
//
// demo 的 normal 写成 `@heavy`，展开出来两站都属于 demo——这是引用；而
// `newgate tier` 那边的链头之后会接上别的 profile（按 priority 排）。两种都要
// 写出来源：不写的话用户会去自己正看着的那份文件里找一个不存在的候选。
func TestChainsReportsWhereEachStepCameFrom(t *testing.T) {
	seedChains(t)

	out := chainsFor(t, "demo", "normal")
	steps := out.Keys[0].Steps
	if len(steps) == 0 {
		t.Fatal("normal 档解析出来是空的：@heavy 的引用没有展开")
	}
	for i, s := range steps {
		if s.Profile == "" {
			t.Errorf("第 %d 站没写来自哪份 profile（%s）", i, s.Binding.String())
		}
	}
	// 引用就地展开：demo 的 normal 与 heavy 是同一条链。
	heavy := chainsFor(t, "demo", "heavy")
	if len(steps) != len(heavy.Keys[0].Steps) {
		t.Errorf("normal=@heavy 展开出 %d 站，而 heavy 自己是 %d 站——引用没有原地展开",
			len(steps), len(heavy.Keys[0].Steps))
	}
}

// 被排除的那一份在 Skips 里出现，而不是从屏幕上消失。
//
// 它与 Steps 是两件事：被跳过的候选常常一步都不在链上（这一条就是），所以只报
// Steps 的话「我明明配了这一份」与「它为什么没进链」都无处可说。
func TestChainsReportsSkippedProfiles(t *testing.T) {
	seedChains(t)
	seedFile(t, "side.kv", "prio=90\nexcluded=true\nheavy=side/model\n")

	out := chainsFor(t, "demo", "heavy")
	found := false
	for _, s := range out.Keys[0].Skips {
		if s.Profile == "side" && s.Kind == resolve.SkipExcluded {
			found = true
		}
	}
	if !found {
		t.Errorf("被排除的 side 应当出现在 Skips 里（kind=%q），实际 %+v",
			resolve.SkipExcluded, out.Keys[0].Skips)
	}
	for _, s := range out.Keys[0].Steps {
		if s.Binding.Provider == "side" {
			t.Error("被排除的 side 不该出现在链上")
		}
	}
}

// 写坏的那一份不许把整次查询弄挂——它被 store.Load 跳过，其余照常。
//
// 这一条是「一个手改坏的文件不该让所有 agent 停摆」那条规矩在读端口上的样子：
// 读端口要是把 Load 的错误直接透出去，界面会白屏，而磁盘上别的配置是好的。
func TestChainsSurvivesABrokenProfileFile(t *testing.T) {
	seedChains(t)
	// 冒号不是 `=`：kv 解析器会拒（见 ParseProfileKV 的 unknown key 分支）。
	seedFile(t, "broken.kv", "normal: ark/deepseek-v3\n")

	out := chainsFor(t, "demo", "heavy")
	if len(out.Keys[0].Steps) == 0 {
		t.Error("一个坏文件把整次查询弄空了：好的那些档位应当照常报出来")
	}
}

// 端口的**读**与**写**是同一条路：面板上那个 `Chains` 拿到的，必须就是
// 命令行 `newgate tier` 拿到的（同一次快照、同一个 resolve）。
//
// 这条断言防的是「面板自己拼一份」——那种实现今天看起来对，等 resolve 改了
// （多一种 Skip、排序规则变了）就会悄悄和 CLI 分叉。
func TestChainsAgreesWithTheCommandLine(t *testing.T) {
	seedChains(t)

	snap, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	want, _ := resolve.BuildChain("heavy", snap.Profiles, snap.Providers,
		resolve.Opts{Active: snap.State.DefaultProfile, MaxSteps: 0})

	got := chainsFor(t, "", "heavy").Keys[0].Steps
	if len(got) != len(want) {
		t.Fatalf("端口给了 %d 站，resolve 直接算出来是 %d 站", len(got), len(want))
	}
	for i := range got {
		if got[i].String() != want[i].String() {
			t.Errorf("第 %d 站：端口 %q，resolve %q", i, got[i].String(), want[i].String())
		}
	}
}

// 主目录那份 state 里 Chain 的字段名只有一种拼法，读端口不该顺手给它起别名。
// （`store.LoadState` 会在缺省时补上值，所以这里只确认写进去的能读回来。）
func TestChainsReadsTheSameSnapshotAsEverythingElse(t *testing.T) {
	seedChains(t)

	if _, err := os.Stat(paths.StateFile()); err != nil {
		t.Fatalf("state.json 没落盘：%v", err)
	}
	if got := chainsFor(t, "", "heavy").Profile; got != store.LoadState().DefaultProfile {
		t.Errorf("端口说链头是 %q，而 state 里默认是 %q——两者读的不是同一份快照",
			got, store.LoadState().DefaultProfile)
	}
}

// allChains 同上：这里测的就是那个方法。
func allChains(t *testing.T, keys ...string) []*Chains {
	t.Helper()
	out, err := (&port{}).AllChains(keys...)
	if err != nil {
		t.Fatalf("AllChains(%v) 失败: %v", keys, err)
	}
	return out
}

// 生效的那一份**排在最前**，其余按名字。
//
// 这一条锁的是「一屏」那一屏的读法：第一眼要找的是「我现在站在哪一份上」，
// 而它要是按名字夹在十几个档位中间，那一屏就白搭了。
func TestAllChainsPutsTheActiveOneFirst(t *testing.T) {
	seedChains(t)
	// 一份名字排在 demo 前面的，用来证明第一格不是靠名字来的。
	seedFile(t, "aaa.kv", "heavy=ark/deepseek-v3\n")

	got := allChains(t, "heavy")
	if len(got) != 3 {
		t.Fatalf("三份档位文件该报三份，实际 %d 份", len(got))
	}
	if got[0].Profile != "demo" || !got[0].Default {
		t.Errorf("生效的 demo 该在最前且自报 Default，实际首格是 %q（Default=%v）",
			got[0].Profile, got[0].Default)
	}
	if got[1].Profile != "aaa" || got[2].Profile != "alt" {
		t.Errorf("其余该按名字排（aaa, alt），实际 %q, %q", got[1].Profile, got[2].Profile)
	}
	for _, c := range got[1:] {
		if c.Default {
			t.Errorf("%q 不是生效的那一份，不该自报 Default", c.Profile)
		}
	}
}

// 一次调用与逐份问出来的是**同一个答案**。
//
// 「一次快照」那条性质（这一屏上没有两个时刻）在这里**测不出来**：要看见它得让两次
// 读盘之间有人写一笔，而那是并发时序，不是断言。所以这一条只锁能锁的那半——聚合
// 出来的每一份卡，与单独问那一份拿到的**逐字段相同**，也就是聚合没有自己另算一份。
// 剩下的那条性质写在 AllChains 的注释里，是它的设计理由，不是这里假装验过的东西。
func TestAllChainsAgreesWithAskingEachProfile(t *testing.T) {
	seedChains(t)

	for _, c := range allChains(t, "heavy") {
		one := chainsFor(t, c.Profile, "heavy")
		if c.Default != one.Default {
			t.Errorf("%q 的 Default 对不上：聚合 %v，单独问 %v", c.Profile, c.Default, one.Default)
		}
		if len(c.Keys) != len(one.Keys) || len(c.Keys[0].Steps) != len(one.Keys[0].Steps) {
			t.Fatalf("%q 的链对不上：聚合 %d 档/%d 站，单独问 %d 档/%d 站",
				c.Profile, len(c.Keys), len(c.Keys[0].Steps), len(one.Keys), len(one.Keys[0].Steps))
		}
		for i := range c.Keys[0].Steps {
			if c.Keys[0].Steps[i].String() != one.Keys[0].Steps[i].String() {
				t.Errorf("%q 第 %d 站：聚合 %q，单独问 %q", c.Profile, i,
					c.Keys[0].Steps[i].String(), one.Keys[0].Steps[i].String())
			}
		}
		// 动作也得一致：它是按这一份的原文长出来的，聚合那条路不能拿到别人的。
		if len(c.Keys[0].Actions) != len(one.Keys[0].Actions) {
			t.Errorf("%q 的动作数对不上：聚合 %d，单独问 %d",
				c.Profile, len(c.Keys[0].Actions), len(one.Keys[0].Actions))
		}
	}
}

// 每一档的链**换得到**：动作是这一档那几个候选长出来的，标签就是绑定本身。
func TestChainActionsOfferEveryOtherCandidate(t *testing.T) {
	seedChains(t)

	out := chainsFor(t, "demo", "heavy")
	acts := out.Keys[0].Actions
	if len(acts) != 1 {
		t.Fatalf("heavy 有两个候选（ark/deepseek-v3, ark/deepseek-lite），"+
			"该给 1 个「换成另一个」的按钮，实际 %d 个", len(acts))
	}
	if got := acts[0].Label(); got != "ark/deepseek-lite" {
		t.Errorf("按钮的字该是那个绑定本身，实际 %q", got)
	}
	// 第一个候选（此刻的链头）不给按钮：点了不改变任何事。
	for _, a := range acts {
		if a.ID == "head:ark/deepseek-v3" {
			t.Error("第一个候选是此刻的链头，不该有一个「换成它」的按钮")
		}
	}
	// 只有一个候选、或者候选是别的 profile 顶上来的，都不给动作。
	alt := chainsFor(t, "alt", "heavy")
	if len(alt.Keys[0].Actions) != 0 {
		t.Errorf("alt 的 heavy 只写了一个候选，不该给动作，实际 %d 个", len(alt.Keys[0].Actions))
	}
	// demo 的 normal 是 `@heavy`（引用）：它自己那一行只写了这一个，所以没有可换的
	// ——按链上那几站去排是错的，那些字面写在 heavy 那一行下面。
	normal := chainsFor(t, "demo", "normal")
	if len(normal.Keys[0].Actions) != 0 {
		t.Errorf("normal=@heavy 自己没有第二个候选，不该给动作，实际 %d 个", len(normal.Keys[0].Actions))
	}
}

// 点一下那个按钮：**盘上那一档的次序真的换了**，而且换的是这一份文件、这一档。
func TestSwitchHeadRewritesTheFile(t *testing.T) {
	seedChains(t)

	out := chainsFor(t, "demo", "heavy")
	acts := out.Keys[0].Actions
	if len(acts) == 0 {
		t.Fatal("没有动作可跑：上面那条的前提取不到")
	}
	if _, err := acts[0].Run(); err != nil {
		t.Fatalf("换链头失败: %v", err)
	}

	// 文件里：ark/deepseek-lite 该在最前，ark/deepseek-v3 还在（是重排不是替换）。
	raw, err := store.LoadProfileRaw("demo")
	if err != nil {
		t.Fatal(err)
	}
	got := raw.Roles["heavy"]
	if len(got) != 2 {
		t.Fatalf("重排之后该还是 2 个候选，实际 %d 个：%v", len(got), got)
	}
	if got[0].String() != "ark/deepseek-lite" {
		t.Errorf("文件里 heavy 的第一个候选该换成 ark/deepseek-lite，实际 %q", got[0].String())
	}
	if got[1].String() != "ark/deepseek-v3" {
		t.Errorf("被顶下去的那个该还在链上（第二），实际 %q", got[1].String())
	}
	// 解析出来也跟着换：这一档此刻走的是新链头。
	if head := chainsFor(t, "demo", "heavy").Keys[0].Steps[0].Binding.String(); head != "ark/deepseek-lite" {
		t.Errorf("换完之后 heavy 的链头该是 ark/deepseek-lite，实际 %q", head)
	}
	// 别的档位、别的文件**一个字节都不该动**。
	if n := len(chainsFor(t, "demo", "normal").Keys[0].Steps); n == 0 {
		t.Error("normal（@heavy）在换链头之后解析成空了——那一档的文件被改坏了")
	}
	if got := chainsFor(t, "alt", "heavy").Keys[0].Steps[0].Binding.String(); got != "zhipu/glm-4" {
		t.Errorf("换 demo 的链头不该碰到 alt，它的 heavy 该还是 zhipu/glm-4，实际 %q", got)
	}
}

// 换链头之前**先看盘**：这一档要是已经被别人改过（动作手里那个候选没了），
// 就报错，而不是照着一个过期的快照写。
//
// 这条是「两个写者」那条规矩在行内动作上的样子。行上的动作按设计不接参数（见
// lib/view 的 Action.Run），所以它拿不到界面手里那份基线，只能在下笔前自己读一次
// ——这一次读就是判据：候选不在了，说明盘上已经不是这一屏看到的那份配置。
//
// 判据是**一个字节都不写**：文件里还是别人写的那份。
//
// （这条锁的是「读到写之间」的前半段。真正的并发窗口——读完到写之间那一瞬——由
// store.WriteIfUnchanged 的 flock + 基线比对兜住，那个已经由 store 自己的测试
// 锁着，这里不重复。）
func TestSwitchHeadRefusesWhenTheFileMovedOn(t *testing.T) {
	seedChains(t)

	acts := chainsFor(t, "demo", "heavy").Keys[0].Actions
	if len(acts) == 0 {
		t.Skip("seedChains 里 demo 的 heavy 只有一个候选？上面的用例前提变了")
	}
	// 别人（命令行、另一个标签页）把这一档改成了别的候选。
	seedFile(t, "demo.kv", "desc=demo base\nheavy=zhipu/glm-4\n")

	_, err := acts[0].Run()
	if err == nil {
		t.Fatal("候选已经不在文件里了，这一次该报错而不是照写")
	}
	raw, err := store.LoadProfileRaw("demo")
	if err != nil {
		t.Fatal(err)
	}
	if got := raw.Roles["heavy"]; len(got) != 1 || got[0].String() != "zhipu/glm-4" {
		t.Errorf("报错时一个字节都不该写，实际文件里是 %v", got)
	}
}
