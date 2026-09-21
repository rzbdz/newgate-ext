package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 这一族测的是**渲染器**，不是内容。内容会天天改，而渲染器的两条判据不该跟着动：
//
//	一、该认的构造认得出来（认不出来 = 页面上多几个 `**` 或者少一张表）；
//	二、**不认识的构造报错**（这条才是棘轮，见 md.go 的文件头）。
//
// 第二条尤其重要：它正是不写 `-check` 的前提（站点产物不进版本控制，没有落盘的
// 基线可比）。把这条测住，「作者写了一种我们没实现的语法」这件事才会在**改文档
// 的那一刻**红，而不是等到有人打开页面才发现排版烂了。

func render(t *testing.T, src string) (string, string) {
	t.Helper()
	body, title, err := renderMarkdown(src)
	if err != nil {
		t.Fatalf("该渲染成功却报错：%v", err)
	}
	return body, title
}

func TestHeadingsAndTitle(t *testing.T) {
	body, title := render(t, "# 页面标题\n\n## 一节\n\n### 一小节\n")
	if title != "页面标题" {
		t.Errorf("标题取错了：%q", title)
	}
	// 一级标题**不画进正文**：模板自己画 <h1>，画两次就会出现两个 h1。
	if strings.Contains(body, "<h1>") {
		t.Errorf("正文里又画了一次 h1：%s", body)
	}
	for _, want := range []string{"<h2>一节</h2>", "<h3>一小节</h3>"} {
		if !strings.Contains(body, want) {
			t.Errorf("少了 %s：%s", want, body)
		}
	}
}

func TestParagraphsAndInline(t *testing.T) {
	body, _ := render(t, "# T\n\n一句 **粗** 的话，带 `代码` 与 [链](https://x/)。\n")
	for _, want := range []string{
		"<p>一句 <strong>粗</strong> 的话，带 <code>代码</code> 与 <a href=\"https://x/\">链</a>。</p>",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("少了 %s\n实际：%s", want, body)
		}
	}
}

// TestASoftWrappedBoldInsideAListIsOneItem 这条是**中文正文的真实形状**。
//
// 一句话写满一行就换行，于是 `**加粗**` 会被劈成两行——第一版把每一行当一个列表项
// 渲染，于是它报「没配上对」。报错是对的（它确实没配上），但**判据错了**：软换行
// 该接起来。这条测试锁住接起来这个行为。
func TestASoftWrappedBoldInsideAListIsOneItem(t *testing.T) {
	body, _ := render(t, "# T\n\n- 前半句**加粗的\n  后半句**，结束了\n- 第二项\n")
	if strings.Count(body, "<li>") != 2 {
		t.Errorf("两个列表项被拆成了 %d 个（软换行没接起来）:\n%s", strings.Count(body, "<li>"), body)
	}
	if !strings.Contains(body, "<strong>加粗的后半句</strong>") {
		t.Errorf("加粗没有跨行合上：%s", body)
	}
}

// TestCJKJoinHasNoSpace：中文换行接起来时**不许**补空格。
//
// 补了的症状是多一个看得见的缝：「站在 Claude Code 和你选定的模型之间」中间那句
// 会变成「… 和你… 」——单看一句像手误，整页看就是排版坏了，而没有人会为它报 bug。
func TestCJKJoinHasNoSpace(t *testing.T) {
	body, _ := render(t, "# T\n\n这是一个很长很长的句子，写到一半\n换行了然后继续。\n")
	if !strings.Contains(body, "写到一半换行了") {
		t.Errorf("中文换行被塞了空格：%s", body)
	}
	// 英文那半边反过来：**要**有空格，否则两个词会粘成一个。
	body, _ = render(t, "# T\n\nthis sentence wraps\ninto two lines\n")
	if !strings.Contains(body, "wraps into two") {
		t.Errorf("英文换行没补空格：%s", body)
	}
}

func TestCodeFenceKeepsEverything(t *testing.T) {
	body, _ := render(t, "# T\n\n```jsonc\n{ \"a\": 1 }   // 注释里的 ** 与 ` 都不该被解析\n```\n")
	if !strings.Contains(body, `<code class="lang-jsonc">`) {
		t.Errorf("围栏的语言标记没进 class：%s", body)
	}
	if !strings.Contains(body, "// 注释里的 ** 与 ` 都不该被解析") {
		t.Errorf("围栏里的内容被解析了：%s", body)
	}
}

func TestTableNeedsASeparator(t *testing.T) {
	body, _ := render(t, "# T\n\n| A | B |\n| --- | ---: |\n| 1 | 2 |\n")
	for _, want := range []string{"<th>A</th>", "<th style=\"text-align:right\">B</th>", "<td>1</td>", "<td style=\"text-align:right\">2</td>"} {
		if !strings.Contains(body, want) {
			t.Errorf("少了 %s：%s", want, body)
		}
	}
	if _, _, err := renderMarkdown("# T\n\n| A | B |\n| 1 | 2 |\n"); err == nil {
		t.Error("表没有分隔行时该报错（它会渲染成一行竖线，作者不会收到任何信号）")
	}
}

func TestQuoteAndRule(t *testing.T) {
	body, _ := render(t, "# T\n\n> 一句引用\n> 接着说\n\n---\n")
	if !strings.Contains(body, "<blockquote><p>一句引用接着说</p></blockquote>") {
		t.Errorf("引用没接起来：%s", body)
	}
	if !strings.Contains(body, "<hr>") {
		t.Errorf("水平线没画出来：%s", body)
	}
}

// TestEscapesHTML 是一条**安全**判据：正文里写的 < 必须变成 &lt;。
//
// 站点是静态的、源文件是我们自己写的，所以这不是一条「防攻击」的测试；它是防
// 「写坏」的：`<your-name>` 这种占位符原样进 HTML 会被浏览器当成一个不认识的标签
// 吃掉，页面上那一段就凭空少了一块。
func TestEscapesHTML(t *testing.T) {
	body, _ := render(t, "# T\n\n把 <your-name> 换成你的名字，& 也要转。\n")
	if !strings.Contains(body, "&lt;your-name&gt;") || !strings.Contains(body, "&amp;") {
		t.Errorf("没转义：%s", body)
	}
}

// TestUnrecognizedSyntaxIsAnError：**这就是那条棘轮**。每条都是「静默地画错」的
// 形状——页面出得来，只是排版不对，而作者以为渲染器认了。
func TestUnrecognizedSyntaxIsAnError(t *testing.T) {
	cases := []struct {
		name string
		src  string
	}{
		{"四级标题", "# T\n\n#### 太深了\n"},
		{"缩进代码块", "# T\n\n    缩进四个空格\n"},
		{"嵌套列表", "# T\n\n- 一层\n  - 二层\n"},
		{"图片语法", "# T\n\n![图](a.png)\n"},
		{"裸 HTML", "# T\n\n<div>手写标签</div>\n"},
		{"下划线式标题", "标题\n===\n"},
		{"未闭合的围栏", "# T\n\n```bash\nnewgate status\n"},
		{"未闭合的加粗", "# T\n\n这里有 **一个加粗\n"},
		{"未闭合的行内码", "# T\n\n这里有 `一段代码\n"},
		{"未闭合的链接", "# T\n\n看 [这一页](/docs/ 吧\n"},
		{"没有一级标题", "## 只有二级\n"},
		{"两个一级标题", "# 一\n\n# 二\n"},
		{"有序无序混着", "# T\n\n- 一\n1. 二\n"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, _, err := renderMarkdown(c.src); err == nil {
				t.Error("这种写法该报错（它会静默地画错，而作者以为渲染器认了），却渲染成功了")
			}
		})
	}
}

// TestRouteOf：文件名即路由，两种 index 都要落到目录上。
func TestRouteOf(t *testing.T) {
	cases := map[string]string{
		"index.md":           "/",
		"docs/index.md":      "/docs/",
		"docs/quickstart.md": "/docs/quickstart/",
		"a/b/c.md":           "/a/b/c/",
	}
	for in, want := range cases {
		if got := routeOf(in); got != want {
			t.Errorf("routeOf(%q) = %q，该是 %q", in, got, want)
		}
	}
}

// TestDefaultLangIsAtTheRoot 锁住「加语言时旧地址不作废」这条。
//
// 中文是排在根上的那一种（见 defaultLang）：`/docs/` 就是中文的。英文将来进
// `/en/`，中文的地址一个都不用变——反过来做（今天中文进 `/zh-Hans/`）的话，
// 加语言那天所有书签与外部链接一起作废。
func TestDefaultLangIsAtTheRoot(t *testing.T) {
	if got := langPrefix(defaultLang); got != "" {
		t.Errorf("默认语言的 URL 前缀该是空串，实际 %q", got)
	}
	if got := langPrefix("en"); got != "en" {
		t.Errorf("别的语言该落在自己的段下，实际 %q", got)
	}
	if got := hrefOf(defaultLang, "/docs/quickstart/"); got != "docs/quickstart/" {
		t.Errorf("默认语言的站内地址拼错了：%q", got)
	}
	if got := hrefOf("en", "/docs/quickstart/"); got != "en/docs/quickstart/" {
		t.Errorf("非默认语言的站内地址拼错了：%q", got)
	}
}

// TestCheckNavCatchesBothDirections：导航与页面必须双向对上。
//
// 两个方向各是一种真实会犯的错，而两种都**不会**让渲染失败：写了页面忘了挂导航
// （那一页谁也找不到）、导航里留着删掉的页面（点进去 404）。所以只能在这里说。
func TestCheckNavCatchesBothDirections(t *testing.T) {
	pages := []page{{Route: "/", Title: "首页"}, {Route: "/docs/", Title: "文档"}}

	if err := checkNav([]navItem{{Route: "/", Title: "首页"}}, pages); err == nil {
		t.Error("有一页不在导航里时该报错")
	}
	if err := checkNav([]navItem{{Route: "/", Title: "首页"}, {Route: "/docs/", Title: "文档"}, {Route: "/gone/", Title: "没了"}}, pages); err == nil {
		t.Error("导航里有不存在的页面时该报错")
	}
	if err := checkNav([]navItem{{Route: "/", Title: "首页"}, {Route: "/docs/", Title: "文档"}}, pages); err != nil {
		t.Errorf("对上了却报错：%v", err)
	}
}

// TestEmitWipesTheOutput 锁住「整份重来」。
//
// 覆盖写的症状很隐蔽：删掉一篇文档之后，它渲染出来的 index.html 还留在盘上，
// 而导航里已经没有它了——那种页面**只有搜索引擎和旧链接找得到**，作者永远不会
// 发现自己删漏了。
func TestEmitWipesTheOutput(t *testing.T) {
	out := t.TempDir()
	stale := filepath.Join(out, "old", "index.html")
	if err := os.MkdirAll(filepath.Dir(stale), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stale, []byte("上一版留下的"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := emit(out, []outFile{{rel: "index.html", bytes: []byte("新的")}}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(stale); err == nil {
		t.Error("上一版留下的那篇还在盘上——整份重来没生效")
	}
	b, err := os.ReadFile(filepath.Join(out, "index.html"))
	if err != nil || string(b) != "新的" {
		t.Errorf("新那份没写对：%q %v", b, err)
	}
}
