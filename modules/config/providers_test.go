package config

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/rzbdz/newgate-ext/lib/view"
	"github.com/rzbdz/newgate-ext/modules/config/paths"
	"github.com/rzbdz/newgate-ext/testing/testkit"
	i18n "github.com/rzbdz/newgate/lib/i18n"
)

// provider 表那张卡的两半：**画出来的东西对不对**，以及**写下去的东西对不对**。
// 后者最要紧——那张卡是用户配凭据的地方，而凭据这件事只有一条规矩：值不许出去。

// seedProviders 在沙箱里放一份 providers.json。
func seedProviders(t *testing.T, body string) {
	t.Helper()
	testkit.Sandbox(t)
	if err := os.MkdirAll(paths.Root(), 0o770); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.ProvidersFile(), []byte(body), 0o660); err != nil {
		t.Fatal(err)
	}
}

// providersCard 取那张卡，顺便确认它还是**可写**的。
//
// 这条断言不是废话：这张卡存在之前的形态是「只读的原文」，而只读与可写之间的
// 差别在界面上只表现为一个保存按钮——掉了没有任何东西会红。
func providersCard(t *testing.T) view.Concept {
	t.Helper()
	c := providersConcept()
	if c.Kind != view.KindRecords {
		t.Fatalf("provider 表该是 %q，实际 %q", view.KindRecords, c.Kind)
	}
	if c.Broken != "" {
		t.Fatalf("这张卡读不出来: %s", c.Broken)
	}
	if c.Apply == nil {
		t.Fatal("provider 表不可写——那用户就只能去命令行加 provider 了")
	}
	return c
}

// fieldValue 按字段名取值（Records 的字段是数组，按 ID 找）。
func fieldValue(t *testing.T, rec view.Record, id string) view.Field {
	t.Helper()
	for _, f := range rec.Fields {
		if f.ID == id {
			return f
		}
	}
	t.Fatalf("记录 %s 里没有字段 %q", rec.ID, id)
	return view.Field{}
}

// TestTheKeyNeverLeavesTheDaemon 是这张卡的安全那一半：**凭据的值不进快照**。
//
// 判据是「值根本不出 daemon」，而不是「出去之前脱敏」：脱敏过的原文回到界面，
// 用户一保存就把 *** 写回磁盘了（那正是 providers.json 一直是只读的原因）。
// 值不出去，就没有写回来这回事。
func TestTheKeyNeverLeavesTheDaemon(t *testing.T) {
	seedProviders(t, `{"providers":{"demo":{"protocol":"anthropic","base_url":"https://x","api_key":"sk-super-secret"}}}`)

	card := providersCard(t)
	data := card.Data.(view.Records)
	if len(data.Items) != 1 {
		t.Fatalf("该有一家 provider，实际 %d", len(data.Items))
	}
	key := fieldValue(t, data.Items[0], "api_key")
	if key.Kind != view.FieldSecret {
		t.Errorf("api_key 该是 %q，实际 %q", view.FieldSecret, key.Kind)
	}
	if key.Value != "" {
		t.Errorf("凭据的值进了快照（%q）——它会经 HTTP 到浏览器、再被原样写回来", key.Value)
	}
	// 非凭据的字段照常给值：不然这张卡就没法用了。
	if got := fieldValue(t, data.Items[0], "base_url").Value; got != "https://x" {
		t.Errorf("base_url 该照常给值，实际 %q", got)
	}
	// 提示要说清「已经设过了」——那是用户唯一能看到的区别（值本身永远不显示）。
	// 判据拿两句 i18n 比，不写死中文：测试跑在源语言下，写死中文的那条会红在
	// 「有没有装目录」上，而它想验的是「设过 key 时提示与没设时不一样」。
	if key.Placeholder == "" || key.Placeholder == i18n.T("not set — paste the key here", nil) {
		t.Errorf("设过 key 时的提示该与没设时不同，实际 %q", key.Placeholder)
	}

	// 整张卡序列化出来也不许带那句密钥：它可能从别的字段漏出去（label、why…）。
	raw, err := json.Marshal(card.Data)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "sk-super-secret") {
		t.Fatalf("快照里带着明文密钥:\n%s", raw)
	}
}

// TestSavingWithoutTypingAKeyKeepsTheOldOne：界面手里没有那个值，它回传的空串
// 意思是「别动它」——不是「删掉」。
//
// 这条要是错了，症状是**用户改一次 base_url 就把 key 弄没了**，而且是静默的：
// 保存成功，下一次请求 401。
func TestSavingWithoutTypingAKeyKeepsTheOldOne(t *testing.T) {
	seedProviders(t, `{"providers":{"demo":{"base_url":"https://old","api_key":"sk-keep-me"}}}`)
	card := providersCard(t)

	base := baseOf(t, card)
	if _, err := card.Apply(json.RawMessage(`{"items":[{"id":"demo","values":{
		"name":"demo","base_url":"https://new","api_key":""}}]}`), base); err != nil {
		t.Fatalf("保存失败: %v", err)
	}

	got := readProviders(t)
	p := got["demo"]
	if p["base_url"] == nil || !strings.Contains(string(p["base_url"]), "https://new") {
		t.Errorf("base_url 没被改掉: %s", p["base_url"])
	}
	if p["api_key"] == nil || !strings.Contains(string(p["api_key"]), "sk-keep-me") {
		t.Errorf("回传空串把 key 弄没了: %s", p["api_key"])
	}
}

// TestTypingAKeyReplacesIt：敲了就是改（这是新增一家 provider 的唯一办法）。
func TestTypingAKeyReplacesIt(t *testing.T) {
	seedProviders(t, `{"providers":{"demo":{"base_url":"https://x","api_key":"sk-old"}}}`)
	card := providersCard(t)
	if _, err := card.Apply(json.RawMessage(`{"items":[{"id":"demo","values":{
		"name":"demo","api_key":"sk-new"}}]}`), baseOf(t, card)); err != nil {
		t.Fatal(err)
	}
	if p := readProviders(t)["demo"]; !strings.Contains(string(p["api_key"]), "sk-new") {
		t.Errorf("敲进去的 key 没写下去: %s", p["api_key"])
	}
}

// TestANewProviderAndADeletedOne：界面交回来的是**它手里的全部记录**，没交的就是
// 删掉；id 为空的那条是新增。
//
// 改名走的是同一条路（id 对不上就是删一条加一条），所以这一条同时钉住了「改名不是
// 静默留下两家」。
func TestANewProviderAndADeletedOne(t *testing.T) {
	seedProviders(t, `{"providers":{"old":{"base_url":"https://o"},"kept":{"base_url":"https://k"}}}`)
	card := providersCard(t)
	if _, err := card.Apply(json.RawMessage(`{"items":[
		{"id":"kept","values":{"name":"kept","base_url":"https://k"}},
		{"id":"","values":{"name":"fresh","base_url":"https://f","api_key":"sk-fresh"}}
	]}`), baseOf(t, card)); err != nil {
		t.Fatal(err)
	}
	got := readProviders(t)
	if len(got) != 2 {
		t.Fatalf("该剩两家（kept 与 fresh），实际 %d: %v", len(got), keysOf(got))
	}
	if got["old"] != nil {
		t.Error("没交回来的那家该被删掉——不然界面上的删除按钮是个摆设")
	}
	if !strings.Contains(string(got["fresh"]["api_key"]), "sk-fresh") {
		t.Errorf("新增那家没写全: %v", got["fresh"])
	}
}

// TestUnknownKeysSurvive：这份文件里可能有界面不认识的东西（手写的备注、将来
// core 加的字段）。整份重写会把它们抹掉，而用户不会知道是哪一次保存弄丢的。
func TestUnknownKeysSurvive(t *testing.T) {
	seedProviders(t, `{"providers":{"demo":{"base_url":"https://x","my_note":"手写的"}},"other_section":1}`)
	card := providersCard(t)
	if _, err := card.Apply(json.RawMessage(`{"items":[{"id":"demo","values":{
		"name":"demo","base_url":"https://y"}}]}`), baseOf(t, card)); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(paths.ProvidersFile())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "my_note") {
		t.Errorf("provider 里不认识的键被抹掉了:\n%s", raw)
	}
	if !strings.Contains(string(raw), "other_section") {
		t.Errorf("顶层不认识的段被抹掉了:\n%s", raw)
	}
}

// TestDuplicateNamesAreRejected：两条记录写同一个名字时，静默留一条的结果是
// 「我改的那家不见了」。报错，让用户看见。
func TestDuplicateNamesAreRejected(t *testing.T) {
	seedProviders(t, `{"providers":{}}`)
	card := providersCard(t)
	_, err := card.Apply(json.RawMessage(`{"items":[
		{"id":"","values":{"name":"dup","base_url":"https://a"}},
		{"id":"","values":{"name":"dup","base_url":"https://b"}}
	]}`), baseOf(t, card))
	if err == nil {
		t.Fatal("重名该被拒绝")
	}
	if !strings.Contains(err.Error(), "dup") {
		t.Errorf("报错要点名是哪个名字: %v", err)
	}
}

// TestAProviderWithoutANameIsRejected：名字是档位绑定引用它的那个键，空的等于
// 写了一条谁也指不到的东西。
func TestAProviderWithoutANameIsRejected(t *testing.T) {
	seedProviders(t, `{"providers":{}}`)
	card := providersCard(t)
	if _, err := card.Apply(json.RawMessage(`{"items":[{"id":"","values":{"name":"  "}}]}`),
		baseOf(t, card)); err == nil {
		t.Fatal("没有名字的 provider 该被拒绝")
	}
}

// TestAMissingFileIsNotAnError：全新安装还没建 providers.json。那不是故障——
// 界面该能照着它加第一家。
func TestAMissingFileIsNotAnError(t *testing.T) {
	testkit.Sandbox(t)
	card := providersConcept()
	if card.Broken != "" {
		t.Fatalf("文件不存在不该是一张坏卡片: %s", card.Broken)
	}
	data := card.Data.(view.Records)
	if len(data.Items) != 0 {
		t.Fatalf("空表该没有记录，实际 %d", len(data.Items))
	}
	if !data.CanAdd {
		t.Error("空表最需要的恰恰是「加一家」——CanAdd 该是 true")
	}
	if _, err := card.Apply(json.RawMessage(`{"items":[{"id":"","values":{
		"name":"first","base_url":"https://f","api_key":"sk-1"}}]}`), ""); err != nil {
		t.Fatalf("从零建一家该成功: %v", err)
	}
	if p := readProviders(t)["first"]; p == nil {
		t.Error("第一家没写下去")
	}
}

// ---------- 小工具 ----------

// baseOf 是这张卡交出去的基线。刻意从**卡片自己**取（而不是现算一个），因为
// 「基线是不是跟着卡一起给的」正是要钉住的东西之一：前端保存时就靠它。
func baseOf(t *testing.T, c view.Concept) string {
	t.Helper()
	got, ok := c.Data.(view.Records)
	if !ok {
		t.Fatalf("Data 该是 view.Records，实际 %T", c.Data)
	}
	return got.Base
}

// readProviders 读回 providers 那一段的原文（保持原始 JSON）。
func readProviders(t *testing.T) map[string]map[string]json.RawMessage {
	t.Helper()
	got, err := readProvidersRaw(paths.ProvidersFile())
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func keysOf(m map[string]map[string]json.RawMessage) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
