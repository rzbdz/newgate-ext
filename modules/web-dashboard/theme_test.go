package webdashboard

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/rzbdz/newgate/testing/testkit"
)

// 皮肤这一层要锁的是三件事：**登记处认人不认序**、**存下来的那套不在了要老实说**、
// **换皮肤这件事真的落到盘上**。三件都是「不报错、只是界面不对」那一类。

func name(s string) func() string { return func() string { return s } }

// TestThemeRegistryRejectsDuplicates 两套皮肤抢同一个 ID 时**报错**，而不是后到的
// 覆盖先到的。
//
// 为什么不是覆盖：那样「哪一套赢了」取决于装配顺序（目录名字母序），而这件事没有
// 人看得出来——界面上只是颜色不对，且换台机器可能就不一样。报出来的话，两个模块
// 的作者各看一眼就知道该改谁。
func TestThemeRegistryRejectsDuplicates(t *testing.T) {
	r := newThemeRegistry()
	rel, err := r.RegisterTheme(Theme{ID: "dark", Name: name("Black")})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.RegisterTheme(Theme{ID: "dark", Name: name("另一个黑")}); err == nil {
		t.Fatal("同一个 ID 登记两次该报错")
	}
	// 撤回来之后**能重新登记**——不然 testing/testkit 在同一个进程里反复装图时，
	// 第二次会撞上一个看起来像「皮肤写坏了」的错误。
	if err := rel(); err != nil {
		t.Fatal(err)
	}
	if _, err := r.RegisterTheme(Theme{ID: "dark", Name: name("Black")}); err != nil {
		t.Fatalf("撤回来之后该能重新登记: %v", err)
	}
}

// TestThemeRegistryNeedsIDAndName：少一样都不是一套能用的皮肤。
//
// 没有 ID 就存不进 state.json（选它等于没选）；没有 Name，界面那个下拉里会出现
// 一个**空白选项**——点下去界面是变了，但没人知道刚才选的是什么。
func TestThemeRegistryNeedsIDAndName(t *testing.T) {
	r := newThemeRegistry()
	if _, err := r.RegisterTheme(Theme{Name: name("没有 id")}); err == nil {
		t.Error("没有 ID 该报错")
	}
	if _, err := r.RegisterTheme(Theme{ID: "x"}); err == nil {
		t.Error("没有 Name 该报错")
	}
}

// TestThemeDocDropsAThemeThatIsGone：存下来那套**不在这次装配里**（模块被关掉、
// 目录被删）时，报文里 active 报空。
//
// 两个方向都要对：报空，界面才落回出厂令牌（而不是抱着一个不存在的 ID 不动）；
// 而**盘上那个值不许被改掉**——用户可能只是临时关掉了那个模块，写回去就把这个选择
// 永久抹掉了，而抹掉它的那一下用户什么都没点。
func TestThemeDocDropsAThemeThatIsGone(t *testing.T) {
	testkit.Sandbox(t)
	r := newThemeRegistry()
	if _, err := r.RegisterTheme(Theme{ID: "dark", Name: name("Black")}); err != nil {
		t.Fatal(err)
	}
	if err := setSelectedTheme("gone"); err != nil {
		t.Fatal(err)
	}
	doc := themeDocOf(r)
	if doc.Active != "" {
		t.Errorf("存的那套不在了，active 该报空，实际 %q", doc.Active)
	}
	if got := selectedTheme(); got != "gone" {
		t.Errorf("盘上那个值不该被改掉（装回来还要用它），实际 %q", got)
	}
	if len(doc.Themes) != 1 || doc.Themes[0].ID != "dark" {
		t.Errorf("装着的皮肤该照常报出来，实际 %+v", doc.Themes)
	}
	// 名字在这里就翻好（语言是后端解析的，前端再猜一遍就有第二处）。
	if doc.Themes[0].Name != "Black" {
		t.Errorf("名字该求值过，实际 %q", doc.Themes[0].Name)
	}
}

// TestThemesEndpoint 换皮肤这条路走通：读得到表、换得动、不认识的 ID 报出来。
func TestThemesEndpoint(t *testing.T) {
	testkit.Sandbox(t)
	h := newHandler()
	reg, err := h.themes.RegisterTheme(Theme{ID: "dark", Name: name("Black"), Dark: true, CSS: "--bg: #000;"})
	if err != nil {
		t.Fatal(err)
	}
	defer reg()

	get := func() themeDoc {
		t.Helper()
		rec := get(t, h, "/api/themes")
		if rec.Code != http.StatusOK {
			t.Fatalf("读皮肤表该 200，实际 %d：%s", rec.Code, rec.Body)
		}
		var doc themeDoc
		if err := json.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
			t.Fatal(err)
		}
		return doc
	}
	set := func(body string) int { return post(t, h, "/api/themes", body).Code }

	if got := get(); got.Active != "" || len(got.Themes) != 1 {
		t.Fatalf("开头该是「没选过」+ 一套皮肤，实际 %+v", got)
	}
	if code := set(`{"id":"dark"}`); code != http.StatusOK {
		t.Fatalf("换皮肤该 200，实际 %d", code)
	}
	if got := get(); got.Active != "dark" {
		t.Errorf("换完之后 active 该是 dark，实际 %q", got.Active)
	}
	// 空 id = 回到出厂那套令牌。它是这一格**本来就有**的第四个取值，不是清空。
	if code := set(`{"id":""}`); code != http.StatusOK {
		t.Fatalf("回到出厂该 200，实际 %d", code)
	}
	if got := get(); got.Active != "" {
		t.Errorf("回到出厂之后 active 该是空，实际 %q", got.Active)
	}
	// 不认识的 ID：报错而不是悄悄落回出厂——「点了没反应」与「这套皮肤没了」
	// 是两句话，而前者会让人以为界面卡了。
	if code := set(`{"id":"nope"}`); code != http.StatusBadRequest {
		t.Errorf("不认识的皮肤该 400，实际 %d", code)
	}
	// 没有 Content-Type 的 POST：这条口能改盘上的东西，与其余几条写口同一条规矩。
	// host 给空串 = 用 raw 的默认那条路（它专门给「换一个 Host / Content-Type」用）。
	if code := raw(t, h, http.MethodPost, "/api/themes", `{"id":"dark"}`, loopback, "").Code; code != http.StatusUnsupportedMediaType {
		t.Errorf("没有 JSON Content-Type 该 415，实际 %d", code)
	}
}
