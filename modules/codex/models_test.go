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

// TestTheTableIsLayeredNotReplaced 锁的是「加一行就生效」这条承诺。
//
// 出厂那五条不必抄进文件里——用户想加一个新模型时只写那一条，**其余四条照旧在**。
// 如果哪天改成「文件存在就整份替换」，症状是用户加了一行、另外四个模型当场失效，
// 而配置文件里看起来一切正常。
func TestTheTableIsLayeredNotReplaced(t *testing.T) {
	seedModels(t, `{"models":[{"slug":"gpt-7-nova","tier":"heavy"},{"slug":"gpt-5.5","tier":"mid"}]}`)

	got := map[string]string{}
	for _, e := range effectiveModels() {
		got[e.Slug] = e.Tier
	}
	if got["gpt-7-nova"] != "heavy" {
		t.Errorf("新加的模型没生效：%v", got)
	}
	if got["gpt-5.5"] != "mid" {
		t.Errorf("文件里的值没有盖住出厂值：gpt-5.5 = %q，应当是 mid", got["gpt-5.5"])
	}
	for _, slug := range []string{"gpt-6-astra", "gpt-5.6-sol", "gpt-5.6-terra", "gpt-5.6-luna"} {
		if _, ok := got[slug]; !ok {
			t.Errorf("出厂的那条 %s 因为文件里没抄一遍就没了：%v", slug, got)
		}
	}
}

// TestModelForFallsBackInsteadOfFailing：反查不到时回空串，调用方回落到档位名。
//
// 一张被改窄的表不该让接管写不出东西——那只是「这一档没有对应的 codex 模型」。
func TestModelForFallsBackInsteadOfFailing(t *testing.T) {
	seedModels(t, `{"models":[]}`)
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
	seedModels(t, `{"mode":"rename","models":[{"slug":"gpt-7-nova","tier":"heavy"}]}`)

	// 槽位表没写过 → 两个槽位都用登记时的缺省档位（都是 normal，见 agent.go）。
	got := slotValue("model")
	if got == "normal" {
		t.Fatal("rename 模式下写出去的还是档位名——那次切换等于没发生")
	}
	if got != "gpt-5.6-sol" {
		t.Errorf("normal 档对应的 codex 模型是 %q，应当是 gpt-5.6-sol（表里第一条 normal）", got)
	}
	// 表里新加的那一条要**排在出厂值前面**：用户加一行新模型，想要的就是用它，
	// 而反查取第一条——出厂值排前面的话，那一行认得出来、却永远写不进文件。
	if got := modelFor("heavy"); got != "gpt-7-nova" {
		t.Errorf("heavy 反查成 %q，应当是文件里新加的 gpt-7-nova（文件优先）", got)
	}
	// 没有被文件覆盖的档位照旧走出厂值。
	if got := modelFor("light"); got != "gpt-5.5" {
		t.Errorf("light 反查成 %q，应当是出厂值 gpt-5.5", got)
	}
}
