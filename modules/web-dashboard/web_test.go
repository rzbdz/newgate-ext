package webdashboard

import (
	"io/fs"
	"regexp"
	"strings"
	"testing"
)

// 前端产物**进版本控制**（与 manifest/modules_gen.go 同一套做法）：这样 `go build`
// 与 CI 完全离线，前端工具链只在开发机上出现。代价是「忘了跑 pnpm build」不会被
// Go 编译发现——这个文件就是那个代价的看门人。
//
// 它能查的只有「嵌进来的这份是不是自洽的」，查不了「它是不是最新的」（那要跑
// node，而 CI 必须离线）。但两种常见的翻车它都拦得住：dist 被清空（产物没了）
// 与 index.html 指着一份已经不存在的 hash 文件（改完前端没重新构建就提交）。

var assetRef = regexp.MustCompile(`/ui/assets/[A-Za-z0-9._-]+`)

func TestEmbeddedFrontendIsSelfConsistent(t *testing.T) {
	sub, err := fs.Sub(assets, AssetDir)
	if err != nil {
		t.Fatalf("嵌入的前端目录读不出来（%s 不存在？）: %v", AssetDir, err)
	}
	index, err := fs.ReadFile(sub, "index.html")
	if err != nil {
		t.Fatalf("产物里没有 index.html——前端没构建就提交了？: %v", err)
	}
	html := string(index)
	if strings.Contains(html, "placeholder") {
		t.Fatal("发出去的还是那句 placeholder——web/dist 是构建产物，得先跑 pnpm build")
	}
	refs := assetRef.FindAllString(html, -1)
	if len(refs) == 0 {
		t.Fatal("index.html 一个资源都没引用——这次构建是空的？")
	}
	for _, ref := range refs {
		name := strings.TrimPrefix(ref, Prefix+"/")
		if _, err := fs.ReadFile(sub, name); err != nil {
			t.Errorf("index.html 引用了 %s，但产物里没有它——"+
				"改完前端要重新 pnpm build 再提交（文件名带内容哈希，对不上就是过期）", name)
		}
	}
}
