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

// TestSiteInternalLinksGetTheMountPrefix：正文里的站内链接要带上站点挂载前缀。
//
// # 这条判据是**实测漏出来的**（2026-09-21）
//
// 源文件里写的是网站根下的路径（`/docs/tiers/`），而发出去的地址必须带
// `/newgate-ext/` 那一层。原样发是 404，而这个错**每一处都是静默的**：页面出得来、
// 导航（走模板、拼的是带前缀的地址）全绿、`--local` 预览（从根服务）也是对的——
// 只有线上点一下才发现。实测 16 条、分布在 7 页上。
//
// 所以这里不只测「补前缀」这一个动作，还测**三种写法各归各的**：外部地址原样、
// 页内锚点原样、站内路径补前缀。搞混任何两种，症状都是页面上某一条链接指向别处。
func TestSiteInternalLinksGetTheMountPrefix(t *testing.T) {
	body, _ := render(t, "# T\n\n看 [档位](/docs/tiers/)、[外链](https://x/y)、[本页](#h)。\n")
	for _, want := range []string{
		`<a href="/newgate-ext/docs/tiers/">档位</a>`,
		`<a href="https://x/y">外链</a>`,
		`<a href="#h">本页</a>`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("少了 %s\n实际：%s", want, body)
		}
	}

	// 认不出来的写法**报错**，不发一个 404 出去。相对路径尤其要拦：源文件树与
	// 发布出去的树不是一回事（`docs/tiers.md` 对应 `/docs/tiers/`，靠拼拼不出来），
	// 而写的人多半以为它跟 GitHub 上看到的一样。
	for _, bad := range []string{"docs/tiers/", "./x/", "//host/x", "x.md"} {
		if _, _, err := renderMarkdown("# T\n\n看 [这个](" + bad + ")。\n"); err == nil {
			t.Errorf("链接写成 %q 时该报错（它在线上是 404，而作者以为对）", bad)
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

// TestEyebrowHeadings 测首页那一排带编号的特性标题（`## <small>01</small> …`）。
//
// 它不只是「认不认」：眉题开的那一节要在**下一个标题**处关上。忘了关的话页尾会
// 多一个 </section> 或者上一节一路吃到页尾——两种在浏览器里都只是「看起来有点怪」，
// 谁也不会报 bug，所以只能在这里锁住。
func TestEyebrowHeadings(t *testing.T) {
	body, _ := render(t, "# T\n\n## <small>01</small> 第一节\n\n正文一。\n\n## Fork 下来\n\n正文二。\n")
	if !strings.Contains(body, `<section class="feat"><h2><small>01</small> 第一节</h2>`) {
		t.Errorf("眉题没开成一节：%s", body)
	}
	// 第二节（没有眉题的普通标题）**不该**被吃进第一节里。
	i := strings.Index(body, "</section>")
	j := strings.Index(body, "<h2>Fork 下来</h2>")
	if i < 0 || j < i {
		t.Errorf("第一节没有在下一个标题处关上：\n%s", body)
	}
	if strings.Count(body, "</section>") != 1 {
		t.Errorf("section 的配对不止一处：%s", body)
	}
}

// TestEyebrowOnlyWhereWeSaid：眉题是**一个闭合的小构造**，写歪了要报错。
//
// 每一条都是「作者以为写对了、页面上只是怪一点」的形状——尤其第一条：他写的是
// 我们支持的写法，只是写错一半，报「正文里不写 HTML」会让他完全摸不着头脑。
func TestEyebrowOnlyWhereWeSaid(t *testing.T) {
	cases := map[string]string{
		"没有闭合":   "# T\n\n## <small>01 标题\n",
		"后面没标题":  "# T\n\n## <small>01</small>\n",
		"眉题里带标记": "# T\n\n## <small><b>01</b></small> 标题\n",
		"别的小标签":  "# T\n\n## <span>01</span> 标题\n",
	}
	for name, src := range cases {
		t.Run(name, func(t *testing.T) {
			if _, _, err := renderMarkdown(src); err == nil {
				t.Error("这种写法该报错（它会静默地画错，而作者以为渲染器认了）")
			}
		})
	}
}

// TestPageDescComesFromThePage：文档页那句说明取的是**这一页自己的第一段**。
//
// 取错了不会红，只会让十来张分享卡片写着同一句整站 tagline——而那种事只有在有人
// 真把链接贴出去之后才看得出来。
func TestPageDescComesFromThePage(t *testing.T) {
	body, _, err := renderMarkdown("# 排障\n\n先跑 `newgate doctor`。\n\n## 一\n\n后话\n")
	if err != nil {
		t.Fatal(err)
	}
	got := pageDesc(page{Route: "/docs/troubleshooting/", Body: body}, "整站那句")
	if got != "先跑 newgate doctor。" {
		t.Errorf("描述没取第一段：%q", got)
	}
	// 一页开篇就是一张表（没有段落）时退回整站那句：有话说比说得最准重要。
	if got := pageDesc(page{Route: "/x/", Body: "<table><tr><td>x</td></tr></table>"}, "整站那句"); got != "整站那句" {
		t.Errorf("没有段落时该退回整站那句：%q", got)
	}
}

// TestPageTitleDiffersByPage：<title> 与 og:title 是**同一个**值，且两种页两种写法。
//
// 首页用整站那句（别人转的是「newgate-ext 是什么」），文档页把页面自己的标题摆在
// 前面（标签页一多，能分辨出哪一张是排障就靠它）。
func TestPageTitleDiffersByPage(t *testing.T) {
	cfg := site{Name: "newgate-ext", Tagline: "为 vibecoding 而生的最佳网关"}
	if got := pageTitle(cfg, page{Route: "/"}); got != "newgate-ext — 为 vibecoding 而生的最佳网关" {
		t.Errorf("首页标题：%q", got)
	}
	if got := pageTitle(cfg, page{Route: "/docs/troubleshooting/", Title: "排障"}); got != "排障 · newgate-ext" {
		t.Errorf("文档页标题：%q", got)
	}
}

// TestSiteConfigMustCarryDescAndURL：这两格缺了站点照样出得来，只有分享出去的那张
// 卡片是空的——所以只能在生成时报错（同 checkNav 那条判据）。
func TestSiteConfigMustCarryDescAndURL(t *testing.T) {
	dir := t.TempDir()
	write := func(body string) string {
		if err := os.WriteFile(filepath.Join(dir, "site.json"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return dir
	}
	if _, err := readSite(write(`{"name":"x","desc":"d","url":"https://e/"}`)); err != nil {
		t.Errorf("该通过却报错：%v", err)
	}
	if _, err := readSite(write(`{"name":"x","url":"https://e/"}`)); err == nil {
		t.Error("没有 desc 时该报错")
	}
	if _, err := readSite(write(`{"name":"x","desc":"d"}`)); err == nil {
		t.Error("没有 url 时该报错")
	}
	// url 缺尾斜杠是**补上**而不是报错：绝对地址是拼出来的，补在这里就只有一处要管。
	cfg, err := readSite(write(`{"name":"x","desc":"d","url":"https://e/abc"}`))
	if err != nil || cfg.URL != "https://e/abc/" {
		t.Errorf("尾斜杠没补上：%q %v", cfg.URL, err)
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

// TestThemeColorComesFromTheCSS：`theme-color` 取的是样式里那两条 `--bg`。
//
// 这一对判据护的是同一件事的两面：
//
//	一、取出来的是**样式里的值**（不是配置里另写一份——那份会漂移，而症状只在手机上
//	    看得见一条颜色不对的地址栏）；
//	二、取不出来时**报错**（错值比没有值更糟：它看起来是对的）。
func TestThemeColorComesFromTheCSS(t *testing.T) {
	th, err := themeColors(":root { --bg: #0f1115; }\n@media (prefers-color-scheme: light) { :root { --bg: #f6f7f9; } }\n")
	if err != nil {
		t.Fatalf("该取出来却报错：%v", err)
	}
	if th.Dark != "#0f1115" || th.Light != "#f6f7f9" {
		t.Errorf("取错了：%+v", th)
	}
	// 顺序即含义：第一条（默认那条）是暗色。反过来的话亮色主题的用户会看到地址栏
	// 先被染成暗色再跳亮——而两行摆在一起没人看得出哪一行该在前面。
	if got := string(themeMeta(th)); !strings.Contains(got, `content="#0f1115"`) ||
		strings.Index(got, "#0f1115") > strings.Index(got, "#f6f7f9") {
		t.Errorf("暗色那条没排在前面：%s", got)
	}

	for _, c := range []struct{ name, css string }{
		{"一条 --bg 都没有", ":root { --ink: #fff; }"},
		{"只有一条", ":root { --bg: #0f1115; }"},
		{"三条", ":root{--bg:#a;} :root{--bg:#b;} :root{--bg:#c;}"},
		{"不是纯色", ":root { --bg: linear-gradient(#a, #b); } :root { --bg: #fff; }"},
		{"是 var()", ":root { --bg: var(--other); } :root { --bg: #fff; }"},
	} {
		t.Run(c.name, func(t *testing.T) {
			if _, err := themeColors(c.css); err == nil {
				t.Error("这种 CSS 该报错（猜一个颜色比不写 theme-color 更糟）")
			}
		})
	}

	// 名字的一部分不算：`--bg-soft` / `--bgfoo` 都不是 `--bg`。
	// 写歪这一处的话，样式里多加一条 `--bg-soft` 就会让 theme-color 报「三条」。
	th, err = themeColors(":root { --bg: #0f1115; --bg-soft: #161a21; }\n" +
		"@media (prefers-color-scheme: light) { :root { --bg: #f6f7f9; } }\n")
	if err != nil || th.Dark != "#0f1115" {
		t.Errorf("`--bg-soft` 被当成了 `--bg`：%+v %v", th, err)
	}

	// 注释里提到的 `--bg` 不算一条（这份样式的注释写得多，里面顺口提一句很正常）。
	th, err = themeColors(":root { /* --bg: #000; 上面那条才是真的 */ --bg: #0f1115; }\n" +
		"@media (prefers-color-scheme: light) { :root { --bg: #f6f7f9; } }\n")
	if err != nil || th.Dark != "#0f1115" {
		t.Errorf("注释里的 --bg 被算进去了：%+v %v", th, err)
	}
}

// TestTheRealStylesheetHasATableTheme 是这条判据的**活体**那一半：真的 site.css
// 里那两条 `--bg` 必须取得到、取出来必须是纯色。
//
// 上面那条测的是函数，喂的是我自己写的 CSS——那种测试在「函数对、而仓库里那份样式
// 换了写法」时是绿的。这一条读的是**要发布的那一份**。
func TestTheRealStylesheetHasATableTheme(t *testing.T) {
	root, err := repoRoot()
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(root, srcDir, "site.css"))
	if err != nil {
		t.Fatal(err)
	}
	th, err := themeColors(string(b))
	if err != nil {
		t.Fatalf("站点样式里取不出 theme-color：%v", err)
	}
	if !strings.HasPrefix(th.Dark, "#") || !strings.HasPrefix(th.Light, "#") {
		t.Errorf("取出来的不是纯色：%+v", th)
	}
}
