package disttesting

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	i18n "github.com/rzbdz/newgate/lib/i18n"
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

// TestTheBundlesMatchTheJSON 是「改了 JSON 忘了跑 bundle」的棘轮。
//
// 发行版是**另一个 Go module**，它自己带一张目录表（`modules/i18n/catalogs`），
// 而运行期读的是编译期那份 `.bin`（见内核 lib/i18n/bundle.go：解析 JSON 占了
// 每条 `newgate …` 命令装配开销的小一半）。于是它多了一种**静默的失效方式**：
// `.bin` 落后于 `.json` 时，界面照常跑，只是少了最近加的那几句译文，谁也不会报错。
//
// 所以判据落在内容上：把嵌进去的那份解开，与磁盘上的 JSON 逐条比。
func TestTheBundlesMatchTheJSON(t *testing.T) {
	root, err := filepath.Abs("..")
	if err != nil {
		t.Fatalf("算仓库根: %v", err)
	}
	dir := filepath.Join(root, "modules/i18n/catalogs")

	// 账本。
	ledRaw, err := os.ReadFile(filepath.Join(dir, i18n.LedgerBundleName))
	if err != nil {
		t.Fatalf("读不了账本 bundle: %v", err)
	}
	fromBin, err := i18n.DecodeLedger(ledRaw)
	if err != nil {
		t.Fatalf("账本 bundle 解不开: %v", err)
	}
	jsonRaw, err := os.ReadFile(filepath.Join(dir, i18n.LedgerName))
	if err != nil {
		t.Fatalf("读不了账本 JSON: %v", err)
	}
	fromJSON, err := i18n.ParseLedger(jsonRaw)
	if err != nil {
		t.Fatalf("账本 JSON 解不开: %v", err)
	}
	if len(fromBin.Messages) != len(fromJSON.Messages) {
		t.Fatalf("账本条数：bundle %d，JSON %d —— 改了 JSON 没跑 `tools/i18n bundle`？",
			len(fromBin.Messages), len(fromJSON.Messages))
	}
	for id, want := range fromJSON.Messages {
		got, ok := fromBin.Messages[id]
		if !ok || got.Where != want.Where || got.One != want.One ||
			got.Other != want.Other || got.Note != want.Note ||
			!slices.Equal(got.Args, want.Args) {
			t.Errorf("账本 %q 对不上（bundle 落后于 JSON？）\n  想要 %+v\n  实际 %+v", id, want, got)
		}
	}

	// 译文。
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	langs := 0
	for _, e := range ents {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".json") || name == i18n.LedgerName {
			continue
		}
		langs++
		binRaw, err := os.ReadFile(filepath.Join(dir, strings.TrimSuffix(name, ".json")+".bin"))
		if err != nil {
			t.Fatalf("读不了 %s 的 bundle: %v", name, err)
		}
		binCat, err := i18n.DecodeCatalog(binRaw)
		if err != nil {
			t.Fatalf("%s 的 bundle 解不开: %v", name, err)
		}
		raw, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		jsonCat, err := i18n.ParseCatalog(raw)
		if err != nil {
			t.Fatalf("%s 解不开: %v", name, err)
		}
		if len(binCat.Messages) != len(jsonCat.Messages) {
			t.Fatalf("%s 条数：bundle %d，JSON %d —— 改了 JSON 没跑 `tools/i18n bundle`？",
				name, len(binCat.Messages), len(jsonCat.Messages))
		}
		for id, want := range jsonCat.Messages {
			got, ok := binCat.Messages[id]
			if !ok || (got != want && !(got.Empty() && want.Empty())) {
				t.Errorf("%s / %q 对不上（bundle 落后于 JSON？）\n  想要 %+v\n  实际 %+v",
					name, id, want, got)
			}
		}
	}
	if langs == 0 {
		t.Fatal("一份译文都没有——判据退化了")
	}
}
