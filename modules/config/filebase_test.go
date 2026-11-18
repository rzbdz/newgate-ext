package config

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/rzbdz/newgate-ext/lib/view"
	"github.com/rzbdz/newgate-ext/modules/config/paths"
	"github.com/rzbdz/newgate-ext/modules/config/store"
	"github.com/rzbdz/newgate-ext/testing/testkit"
)

// 原文那一栏（`kind: code`）的 data **必须带基线**。
//
// 这是 2026-09-20 的现场：改 kv 的原文点保存**永远失败**，改控件却好好的。根因
// 是 fileData 少了 `base` 字段——界面把它交回来的是空串，而后端把空串读成「我
// 加载时它还不存在」，盘上却明明有这份文件，于是 CAS 当场判过期，报「这个文件在
// 页面加载之后被别人改过」。控件那半一直带 base，所以只有原文那一半坏。
//
// 一条断言就够：拿这个概念自己报的 base 去写，必须能写成。
func TestTheRawHalfCarriesItsBaseline(t *testing.T) {
	testkit.Sandbox(t)
	abs := seedFile(t, "demo.kv", "desc=demo\nnormal=p/m\n")

	var code view.Concept
	for _, c := range fileConcepts() {
		if c.ID == "config.file.mappings/demo.kv" {
			code = c
		}
	}
	if code.ID == "" {
		t.Fatal("没报出 mappings/demo.kv 那张原文卡")
	}
	data, ok := code.Data.(fileData)
	if !ok {
		t.Fatalf("Data 不是 fileData，是 %T", code.Data)
	}
	if data.Base == "" {
		t.Fatal("原文那一半没带基线——界面交回空串，后端会把它读成「文件不存在」，保存必被判过期")
	}
	if want := store.Revision(abs); data.Base != want {
		t.Errorf("基线与盘上那份对不上:\n  卡片说 %s\n  盘上说 %s", data.Base, want)
	}
	if code.Apply == nil {
		t.Fatal("原文卡该是可写的（不是每一份都带凭据）")
	}

	// 拿它自己报的基线写一次——必须成功。这就是用户那个动作。
	body, _ := json.Marshal(map[string]string{"text": "desc=改过了\n"})
	if _, err := code.Apply(body, data.Base); err != nil {
		t.Fatalf("用卡片自己报的基线保存原文都失败: %v", err)
	}
	got, _ := os.ReadFile(abs)
	if string(got) != "desc=改过了\n" {
		t.Errorf("原文没写进去: %q", got)
	}
	// 被别人抢先改过之后仍然要拦住（基线不是摆设）。
	if _, err := code.Apply(body, data.Base); err == nil {
		t.Error("基线过期还让写？两次改动会互相静默覆盖")
	}
	_ = paths.Mappings()
}
