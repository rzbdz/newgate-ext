package codex

import (
	"os"
	"testing"

	"github.com/rzbdz/newgate/testing/testkit"
)

// 这一族锁的是「codex 用自己的模型名」这件事的两半：
//
//	哪一档该写成 codex 的哪个名字（rename 模式，写进 config.toml）
//	哪个名字该认回哪一档（请求进来时，靠动态角色）
//
// 两半用的是**同一张表**，所以最容易坏的错是「两边对不上」——那不会让任何东西
// 报错，只会让用户在 codex 里选了 gpt-5.6-luna，落到一个谁也说不清的档位上。
func seedModels(t *testing.T, body string) {
	t.Helper()
	testkit.Sandbox(t)
	if err := os.WriteFile(ModelsFile(), []byte(body), 0o660); err != nil {
		t.Fatal(err)
	}
}

func TestModeDefaultsToTakeoverAndRoundTrips(t *testing.T) {
	testkit.Sandbox(t)
	// 文件还不存在时是 takeover：**不变更任何人的现状**（那是 2026-09-21 之前
	// 唯一的行为，老装配升上来不该突然按新模式接管）。
	if got := Mode(); got != ModeTakeover {
		t.Fatalf("没有这份文件时模式是 %q，应当是 %q", got, ModeTakeover)
	}
	if err := SetMode(ModeRename); err != nil {
		t.Fatal(err)
	}
	if got := Mode(); got != ModeRename {
		t.Fatalf("写下 rename 之后读回来是 %q", got)
	}
	// 认不出来的值一律**当成缺省**，不写进文件里当第三种模式。
	if err := SetMode("banana"); err != nil {
		t.Fatal(err)
	}
	if got := Mode(); got != ModeTakeover {
		t.Fatalf("写了个不认识的值之后模式是 %q，应当回落到 %q", got, ModeTakeover)
	}
}

// TestTheTableComesFromTheFileOnceItHasOne 锁的是「界面上那张表所见即所得」。
//
// 语义 2026-09-21 改过一次：原来是「出厂值叠加文件」（手写很舒服，加一行就是加一行），
// 但做成界面上的表之后它是错的——用户在卡上删掉一行、保存，读的时候出厂值又把它顶
// 回来了：界面显示删除成功，实际什么也没发生。
//
// 所以现在是：文件里**有** models 这一段，它就是全部；没有才用出厂那五条。
// 这同时意味着「出厂那几条不会因为文件里没抄一遍就消失」只在**没有那一段**时成立。
func TestTheTableComesFromTheFileOnceItHasOne(t *testing.T) {
	// 没有那一段（只写了 mode）→ 出厂五条照旧。
	seedModels(t, `{"mode":"rename"}`)
	if got := len(effectiveModels()); got != len(defaultModels) {
		t.Errorf("文件里没有 models 那一段时应当用出厂表（%d 条），得到 %d 条", len(defaultModels), got)
	}

	// 有了那一段 → 它就是全部，出厂值不再参与。
	seedModels(t, `{"models":[{"slug":"gpt-7-nova","tier":"heavy"}]}`)
	got := effectiveModels()
	if len(got) != 1 || got[0].Slug != "gpt-7-nova" {
		t.Fatalf("文件里有 models 时应当以它为准，得到 %+v", got)
	}
	// 空数组也是「有」：那是用户把表清空了，不是「还没配」。
	seedModels(t, `{"models":[]}`)
	if got := len(effectiveModels()); got != 0 {
		t.Errorf("空数组是明确的「没有映射」，出厂值不该顶回来，得到 %d 条", got)
	}
}

// TestModelForFallsBackInsteadOfFailing：反查不到时回空串，调用方回落到档位名。
//
// 一张被改窄的表不该让接管写不出东西——那只是「这一档没有对应的 codex 模型」。
func TestModelForFallsBackInsteadOfFailing(t *testing.T) {
	// 文件里**没有** models 那一段 → 出厂表生效。
	seedModels(t, `{"mode":"rename"}`)
	if got := modelFor("heavy"); got != "gpt-6-astra" {
		t.Errorf("heavy 反查成 %q，应当是 gpt-6-astra（出厂表）", got)
	}
	if got := modelFor("nonexistent-tier"); got != "" {
		t.Errorf("没有对应模型的档位应当回空串（调用方据此回落成档位名），得到 %q", got)
	}
	if got := modelFor(""); got != "" {
		t.Errorf("空档位应当回空串，得到 %q", got)
	}
}

// TestRolesAreContributedOnlyInRenameMode 是这次改动里**最要紧**的一条。
//
// 这些名字一旦登记成动态角色，`IsKnownRole` 就为真，于是「具体模型名反解回它所属
// 档位、并把点名的模型放最前」那条路被短路。而那条路是**别的客户端**在用的：本机
// smt-codex 的链上就挂着 gpt-5.6-terra。无条件登记 = 悄悄改掉没让路的人的行为。
//
// 所以这条测试钉的是「不让路的人不受影响」：takeover 模式下这个提供者必须**一个
// 角色都不贡献**。
func TestRolesAreContributedOnlyInRenameMode(t *testing.T) {
	testkit.Sandbox(t)
	rs, err := rolesProvider{}.Roles()
	if err != nil {
		t.Fatal(err)
	}
	if len(rs) != 0 {
		t.Errorf("takeover 模式下贡献了 %d 个角色，应当是 0 个——"+
			"登记它们会短路掉「具体模型名反解回档位」那条路，而那条路上走着别人的请求", len(rs))
	}

	if err := SetMode(ModeRename); err != nil {
		t.Fatal(err)
	}
	rs, err = rolesProvider{}.Roles()
	if err != nil {
		t.Fatal(err)
	}
	if len(rs) == 0 {
		t.Fatal("rename 模式下也一个角色都没贡献——那 codex 发的模型名会 404")
	}
	byKey := map[string]string{}
	for _, r := range rs {
		byKey[r.Key] = r.Default
	}
	// 逐个对一遍出厂表：**两边是同一张表**，对不上就是「在 codex 里选了谁、
	// 落到哪一档说不清」。
	for _, want := range defaultModels {
		if byKey[want.Slug] != want.Tier {
			t.Errorf("角色 %s 的缺省档位是 %q，表里写的是 %q", want.Slug, byKey[want.Slug], want.Tier)
		}
	}
}

// TestRenameModeWritesCodexModelNames：模式的**全部意义**就在这一条断言上。
//
// takeover 模式写档位名（codex 只认一个模型，切不动）；rename 模式写 codex 自己的
// 模型名，于是它的选择器里每一项都认得，而每一项进来时被认回档位。
func TestRenameModeWritesCodexModelNames(t *testing.T) {
	testkit.Sandbox(t)
	seedModels(t, `{"mode":"rename","models":[{"slug":"gpt-7-nova","tier":"heavy"},
	                                        {"slug":"gpt-5.6-luna","tier":"normal"}]}`)

	// 槽位表没写过 → 两个槽位都用登记时的缺省档位（都是 normal，见 agent.go）。
	got := slotValue("model")
	if got == "normal" {
		t.Fatal("rename 模式下写出去的还是档位名——那次切换等于没发生")
	}
	if got != "gpt-5.6-luna" {
		t.Errorf("normal 档对应的 codex 模型是 %q，应当是表里那条 gpt-5.6-luna", got)
	}
	// 表里**没有**的档位：回落成档位名（不报错、也不是空串）。
	// 这是「所见即所得」的代价，也是它该有的样子——界面上那张表里没有 light，
	// 那就没有哪个 codex 模型名能代表 light。
	if got := modelFor("light"); got != "" {
		t.Errorf("light 不在表里，反查应当是空串，得到 %q", got)
	}
	// 文件里那条也反查得到（它是此刻唯一的一条 heavy）。
	if got := modelFor("heavy"); got != "gpt-7-nova" {
		t.Errorf("heavy 反查成 %q，应当是 gpt-7-nova", got)
	}
	// 而表里没有的档位回空串（调用方回落成档位名）。文件里只写了一条，
	// 所以 light 此刻**没有**对应的 codex 模型——这是「所见即所得」的代价，
	// 也是它该有的样子：界面上那张表只有一行。
	if got := modelFor("light"); got != "" {
		t.Errorf("light 不在表里，反查应当是空串，得到 %q", got)
	}
}
