package disttesting

import (
	"path/filepath"
	"testing"

	"github.com/rzbdz/newgate/tools/i18n/check"
)

// TestLocalizationStaysConsistent 是发行版这一侧的本地化棘轮。
//
// 与内核那条（core/app/i18n_test.go）是**同一份判据的两个入口**：实现只有一份
// （core/tools/i18n/check，发行版通过 replace 用的是同一把尺子），差别只在路径
// ——发行版的目录是 `modules/i18n/catalogs`，而不是内核的 `lib/i18n/catalogs`。
//
// 为什么发行版也要有：内核的棘轮只看自己那棵树（扫描器遇到嵌套的 Go module 就
// 停下，见 core/tools/i18n/scan）。发行版这些模块说的话——客户端接管、上游怪癖
// 补丁、界面——内核一句都不认识，也就一条都管不到。少了这条，发行版会慢慢长出
// 一堆没人管的硬编码中文，而内核那边全绿。
func TestLocalizationStaysConsistent(t *testing.T) {
	root, err := filepath.Abs("..")
	if err != nil {
		t.Fatalf("算仓库根: %v", err)
	}
	findings, err := check.Run(check.Options{
		Root:       root,
		CatalogDir: "modules/i18n/catalogs",
		Allowlist:  "tools/i18n-allowlist.json",
	})
	if err != nil {
		t.Fatalf("本地化检查跑不起来: %v", err)
	}
	for _, f := range findings {
		if f.Level == "error" {
			t.Errorf("%s", f.String())
		}
	}

	// 退化守卫：判据自己坏掉时必须红，而不是「一条都不报」看起来像通过。
	// 目录挪了、账本被清空、扫描器扫不到东西——这几种坏法都不会自己出声。
	cats, cerr := check.LoadCatalogs(filepath.Join(root, "modules/i18n/catalogs"))
	if cerr != nil {
		t.Fatalf("读不了译文目录——判据退化了: %v", cerr)
	}
	if len(cats) == 0 {
		t.Fatal("一份译文都没有——判据退化了（目录挪了？catalogs 里只剩账本？）")
	}
	led, lerr := check.LoadLedger(filepath.Join(root, "modules/i18n/catalogs"))
	if lerr != nil {
		t.Fatalf("账本读不出来——判据退化了: %v", lerr)
	}
	if len(led.Messages) == 0 {
		t.Fatal("账本是空的——扫描器坏了，或者发行版源码里一条 i18n.T 都没有了")
	}
}
