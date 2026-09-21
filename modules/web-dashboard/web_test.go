package webdashboard

import (
	"bytes"
	"encoding/xml"
	"io"
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
//
// 「忘了 pnpm build」的另一半由 i18n_test.go 的 TestShippedBundleHasEverySentence
// 守：加了一句文案而没重新构建时，源字典里有、bundle 里没有，那条会点名。
// 剩下查不了的（样式、布局、逻辑）照样查不了——那种只能靠人打开看一眼。

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

// TestEmbeddedSVGsParse 断言产物里每一份 SVG 都是**合法的 XML**。
//
// # 为什么需要这一条（2026-09-21 实测）
//
// favicon 从加上那天起就没在浏览器里出现过，而它「看起来」处处正常：文件在、
// `index.html` 引着它、服务器回 200、Content-Type 是 image/svg+xml、字节数与仓库里
// 那份一模一样。curl 验完全绿——**它就是画不出来**。
//
// 根因在一个字面上看不出问题的地方：注释正文里写了 CSS 变量名 `--bg`，而 XML 注释
// **不许出现连续两个连字符**。Chromium 拿到它直接解析失败（`parsererror`，
// 根元素退化成 html），表现是标签页上**什么都不画**——没有报错、没有占位符。
//
// 这正是这一层看门人该拦的那类错：不是「产物是不是最新的」（那要跑 node），也不是
// 「文件在不在」（上一条查了），而是**「嵌进来的这份字节，浏览器画得出来吗」**。
// 用 Go 自带的 encoding/xml 判，离线、零依赖，而且判据与浏览器**同一条规矩**
// ——上面那个 `--` 就是它先报出来的。
//
// 只查 SVG：`.js`/`.css` 不是 XML，`.html` 会被 Svelte 的模板语法绊倒（那是另一套
// 解析器的事，浏览器对它有容错，拿 XML 的标准去量它只会得到假警）。
func TestEmbeddedSVGsParse(t *testing.T) {
	sub, err := fs.Sub(assets, AssetDir)
	if err != nil {
		t.Fatalf("嵌入的前端目录读不出来（%s 不存在？）: %v", AssetDir, err)
	}
	checked := 0
	err = fs.WalkDir(sub, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".svg") {
			return err
		}
		b, err := fs.ReadFile(sub, path)
		if err != nil {
			return err
		}
		checked++
		dec := xml.NewDecoder(bytes.NewReader(b))
		for {
			if _, err := dec.Token(); err == io.EOF {
				return nil
			} else if err != nil {
				t.Errorf("产物里的 %s 不是合法的 XML：%v\n"+
					"  浏览器拿到这种文件的表现是**什么都不画**（没有报错、没有占位符），"+
					"所以只能在这一层拦。常见坑：注释正文里出现连续两个连字符（XML 禁止），"+
					"比如 CSS 变量名 `--bg` 直接写进注释。", path, err)
				return nil
			}
		}
	})
	if err != nil {
		t.Fatalf("遍历嵌入产物失败: %v", err)
	}
	// 一条会让上面那个循环空转成绿的空集守卫：favicon 就住在这里，一份 SVG 都没有
	// 说明产物变了或扫描坏了。
	if checked == 0 {
		t.Fatal("嵌入产物里一份 SVG 都没扫到——favicon 呢？（循环是空转的，这条测试没在查任何东西）")
	}
}
