package main

import (
	"fmt"
	stdhtml "html"
	"html/template"
	"os"
	"strings"
)

// pageTmpl 是整站的唯一一份 HTML 外壳。
//
// 为什么是一个 Go 模板而不是拼接：这一份东西**每一页都要用**，而它里面有几处
// 会重复出现的结构（导航的当前项、页脚的两条链）。拼字符串写下来，第一个改版
// 就会漏掉某一页的某一段。
var pageTmpl = template.Must(template.New("page").Parse(`<!doctype html>
<html lang="{{.HTMLang}}">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<link rel="canonical" href="{{.Canon}}">
{{.ThemeMeta}}
<title>{{.DocTitle}}</title>
<meta name="description" content="{{.Desc}}">
{{.Meta}}
<link rel="icon" type="image/svg+xml" href="{{.Base}}favicon.svg">
<link rel="stylesheet" href="{{.Base}}site.css">
</head>
<body>
<header class="top">
  <a class="brand" href="{{.Base}}"><strong>{{.Site.Name}}</strong></a>
  <span class="spacer"></span>
  <nav class="langs">{{range .Langs}}{{if .Ready}}<a href="{{.Href}}" {{if .Current}}class="on"{{end}}>{{.Label}}</a>{{else}}<span class="soon" title="not written yet">{{.Label}}</span>{{end}}{{end}}</nav>
</header>
<div class="shell">
  <nav class="side">
    <ul>
    {{range .Nav}}<li{{if .Current}} class="on"{{end}}><a href="{{.Href}}">{{.Title}}</a></li>
    {{end}}</ul>
  </nav>
  <main>
    <h1>{{.Page.Title}}</h1>
    {{.Page.Body}}
  </main>
</div>
<footer>
  <p>这一页讲的是发行版 <strong>{{.Site.Name}}</strong> 的装配。内核机制的权威原文在
     <a href="{{.Site.Kernel}}">rzbdz/newgate</a>。</p>
  <p><a href="{{.Site.Kernel}}">内核</a> · <a href="{{.Site.Releases}}">版本发布</a></p>
</footer>
</body>
</html>
`))

// landingTmpl 是首页那一页的外壳。与文档页共用头部/页脚的**语言切换**，但不共用
// 侧栏文档壳——首页是产品门面，不是一篇文档。
//
// 演示怎么嵌进首页：包一行 `<iframe src="{base}demo/">`，外面套一个仿浏览器窗口的
// 壳（.browser）。这不是截图，是**真的**界面在跑——演示页是同一个前端的第二个入口
// （见 modules/web-dashboard/web/vite.demo.config.ts），它喂的是假数据、没有后端。
var landingTmpl = template.Must(template.New("landing").Parse(`<!doctype html>
<html lang="{{.HTMLang}}">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<link rel="canonical" href="{{.Canon}}">
{{.ThemeMeta}}
<title>{{.DocTitle}}</title>
<meta name="description" content="{{.Desc}}">
{{.Meta}}
<link rel="icon" type="image/svg+xml" href="{{.Base}}favicon.svg">
<link rel="stylesheet" href="{{.Base}}site.css">
</head>
<body>
<header class="top">
  <a class="brand" href="{{.Base}}"><strong>{{.Site.Name}}</strong></a>
  <span class="spacer"></span>
  <nav class="langs">{{range .Langs}}{{if .Ready}}<a href="{{.Href}}" {{if .Current}}class="on"{{end}}>{{.Label}}</a>{{else}}<span class="soon" title="not written yet">{{.Label}}</span>{{end}}{{end}}</nav>
</header>

<div class="hero">
  <p class="lead">{{.Site.Name}}<span class="sep">/</span>{{.Site.Tagline}}</p>
  <h1>{{.Page.Title}}</h1>
  <p class="sub">{{.Desc}}</p>
  <p class="cta">
    <a class="button primary" href="{{.Base}}docs/quickstart/">快速开始</a>
    <a class="button ghost" href="{{.Site.Kernel}}">GitHub</a>
    <a class="button ghost" href="{{.Base}}docs/">文档</a>
  </p>

  <div class="browser" aria-label="newgate 的真实界面演示（可点）">
    <div class="browser-bar">
      <span class="dot"></span><span class="dot"></span><span class="dot"></span>
      <span class="addr">newgate web · 档位绑定 · 就地编辑</span>
    </div>
    <iframe src="{{.Base}}demo/" title="newgate 控制台演示 — 真组件，假数据，没有后端" loading="eager"></iframe>
  </div>
  <p class="demo-note">上面不是截图，是<strong>真界面</strong>：真组件、假数据，点得动、改得了、
     切得了模式。只有最后那一下「应用」不落盘——这里没有后端。</p>
  <ul class="chips">
    <li>一个静态二进制</li>
    <li>PATH shim 接管，配置就地改写</li>
    <li>零第三方依赖</li>
    <li>换版本不打断在途的会话</li>
    <li>MIT · 可 fork</li>
  </ul>
</div>

<main class="landing">{{.Page.Body}}</main>

<footer>
  <p>这一页讲的是发行版 <strong>{{.Site.Name}}</strong> 的装配。内核机制的权威原文在
     <a href="{{.Site.Kernel}}">rzbdz/newgate</a>。</p>
  <p><a href="{{.Site.Kernel}}">内核</a> · <a href="{{.Site.Releases}}">版本发布</a></p>
</footer>
</body>
</html>
`))

// renderLanding 渲染首页（路由 "/"）。
//
// 首页的正文也是 markdown（`site/src/zh-Hans/index.md`），body 与文档页走同一个
// 渲染器——差别只在壳。这样就保住「内容在 markdown、样式在 CSS」这两条，首页要的
// 「门面感」由壳 + CSS 出，不在正文里堆 div。
func renderLanding(cfg site, l lang, p page) (string, error) {
	base := basePath()
	lvs := make([]langView, 0, len(cfg.Langs))
	for _, other := range cfg.Langs {
		lvs = append(lvs, langView{
			Label: other.Label,
			Href:  base + hrefOf(other.Code, "/"),
			Ready: other.Ready, Current: other.Code == l.Code,
		})
	}
	var b strings.Builder
	err := landingTmpl.Execute(&b, struct {
		Site      site
		Base      string
		HTMLang   string
		DocTitle  string
		Desc      string
		Canon     string
		ThemeMeta template.HTML
		Meta      template.HTML
		Page      struct {
			Route string
			Title string
			Body  template.HTML
			Lang  string
		}
		Langs []langView
	}{
		Site: cfg, Base: base, HTMLang: l.Code,
		DocTitle:  cfg.Name + " — " + cfg.Tagline,
		Desc:      cfg.Desc,
		Canon:     cfg.URL,
		ThemeMeta: themeMeta(cfg.Theme),
		Meta:      socialMeta(cfg, cfg.Name+" — "+cfg.Tagline, cfg.Desc, cfg.URL),
		Page: struct {
			Route string
			Title string
			Body  template.HTML
			Lang  string
		}{Route: p.Route, Title: p.Title, Body: template.HTML(p.Body), Lang: p.Lang},
		Langs: lvs,
	})
	if err != nil {
		return "", fmt.Errorf("套首页模板失败: %w", err)
	}
	return b.String(), nil
}

// navView / langView 是模板吃的那两种行（比配置多几个算好的字段）。
//
// 算好再交给模板（而不是在模板里比字符串）的理由：**链接怎么拼**只有一处实现
// ——站点的 base（GitHub Pages 上多一层 `/newgate-ext/`）、语言的段、路由的尾
// 斜杠，三件事都是「拼错了页面上就是 404」的东西。模板里 `{{.Base}}{{.Route}}`
// 那种写法看着也行，但它把规则散进了模板；将来加一个「子路径部署」就要在模板里
// 再找一遍。
type navView struct {
	Route   string
	Title   string
	Href    string
	Current bool
}

type langView struct {
	Label   string
	Href    string
	Ready   bool
	Current bool
}

// renderPage 把一页套进外壳。
func renderPage(cfg site, l lang, nav []navItem, p page) (string, error) {
	if p.Route == "/" && strings.TrimPrefix(p.Route, "/") == "" {
		// Landing 页（路由 "/"）：不用文档那套侧栏外壳，用专门的一页。
		// 判据写在函数里而不是 config：网站的设计只有一个首页，这不是「每条路由
		// 都可以各挑一套壳」的产品决策。
		return renderLanding(cfg, l, p)
	}
	return renderDocs(cfg, l, nav, p)
}

func renderDocs(cfg site, l lang, nav []navItem, p page) (string, error) {
	base := basePath()

	cur := map[string]bool{}
	nvs := make([]navView, 0, len(nav))
	for _, n := range nav {
		cur[n.Route] = true
		nvs = append(nvs, navView{Route: n.Route, Title: n.Title, Href: base + hrefOf(l.Code, n.Route), Current: n.Route == p.Route})
	}
	lvs := make([]langView, 0, len(cfg.Langs))
	for _, other := range cfg.Langs {
		lvs = append(lvs, langView{
			Label: other.Label,
			Href:  base + hrefOf(other.Code, p.Route),
			// Ready 由配置说了算，**不由「那一页存在」推**：英文站刚开时只有两页，
			// 按「页在不在」推的话，英文页头上的语言切换会时有时无。
			Ready:   other.Ready,
			Current: other.Code == l.Code,
		})
	}

	var b strings.Builder
	// pageData 的注释见下面那两处 template.HTML：**模板默认会转义**，而 Body 是
	// 我们自己已经渲染好的 HTML 片段（md.go 的 esc 是它唯一的出口）。所以这里
	// 显式声明那一格是可信的——反过来，正文里作者写的 `<` 早在 md.go 里就变成
	// `&lt;` 了，不会走到这儿。
	//
	// Desc 取页面自己的第一段。**不取 site.json 那句**：文档页有十来张，每一张
	// 分享出去都该说自己那一句（「排障」那一页的卡片写整站的 tagline，等于没有）。
	canon := cfg.URL + strings.TrimPrefix(hrefOf(l.Code, p.Route), "/")
	err := pageTmpl.Execute(&b, struct {
		Site      site
		Base      string
		HTMLang   string
		DocTitle  string
		Desc      string
		Canon     string
		ThemeMeta template.HTML
		Meta      template.HTML
		Page      struct {
			Route string
			Title string
			Body  template.HTML
			Lang  string
		}
		Nav   []navView
		Langs []langView
	}{
		Site:      cfg,
		Base:      base,
		HTMLang:   l.Code,
		DocTitle:  pageTitle(cfg, p),
		Desc:      pageDesc(p, cfg.Desc),
		Canon:     canon,
		ThemeMeta: themeMeta(cfg.Theme),
		Meta:      socialMeta(cfg, pageTitle(cfg, p), pageDesc(p, cfg.Desc), canon),
		Page: struct {
			Route string
			Title string
			Body  template.HTML
			Lang  string
		}{Route: p.Route, Title: p.Title, Body: template.HTML(p.Body), Lang: p.Lang},
		Nav:   nvs,
		Langs: lvs,
	})
	if err != nil {
		return "", fmt.Errorf("套模板失败: %w", err)
	}
	return b.String(), nil
}

// pageTitle 是一页在 <title> 与分享卡片上的名字。
//
// 两种页两种写法，因为两者读它的人不同：**首页**的名字就是整站那句（它是门面，
// 别人转的是「newgate-ext 是什么」），**文档页**要页面自己的标题在前（「排障 ·
// newgate-ext」——标签页一多，能分辨出哪一张是排障靠的就是前面那段）。
//
// 它同时供 <title> 与 og:title。两处各写一遍的话，症状是分享出去的那张卡片与
// 浏览器标签页上的名字不一样——而那种不一致只有把两者摆在一起才看得出来。
func pageTitle(cfg site, p page) string {
	if p.Route == "/" {
		return cfg.Name + " — " + cfg.Tagline
	}
	return p.Title + " · " + cfg.Name
}

// socialMeta 是分享卡片那几行（Open Graph + Twitter Card）。
//
// # 为什么要手写这几行
//
// 链接贴进聊天软件、论坛、Slack 时，对方读的是这几行**而不是**页面——没有
// og:title / og:description，一条链接就只有光秃秃一个地址。而这正好是这类工具的
// 主要传播方式（「你看这个」）。所谓「抄 cc-switch 那一版」，能搬的结构里这是最
// 实的一条：它的 og:* 一应俱全。
//
// # title / desc / canon 都从参数来，不在这里重新取
//
// 首页与文档页那三样**都不是同一个值**（首页的标题是整站的、文档页是它自己的；
// 说明同理；canonical 按路由拼），调用方已经算好了。在这里重算一遍就是两份会
// 漂移的真相——而且首页那一份会**悄悄变成错的那一份**（文档页十来张，标题都一样）。
//
// # 为什么没有 og:image
//
// 无图卡片（`summary`）比编一张图诚实。有一张 og:image 的卡片好看得多，但那要求
// 仓库里有一份**光栅**图（1200×630），而本站今天只有 favicon.svg——SVG 各家
// unfurl 器基本不认。做那份图要么引入一个图片库/截图工具（破了「构建离线、零
// 第三方依赖」），要么手绘一张二进制（没人维护得起）。所以：**没有就是没有**，
// 等真要做的时候再加一行，而不是先塞一个指向 404 的 og:image。
func socialMeta(cfg site, title, desc, canon string) template.HTML {
	var b strings.Builder
	w := func(prop, content string) {
		if content == "" {
			return
		}
		fmt.Fprintf(&b, "<meta property=%q content=%q>\n", prop, content)
	}
	n := func(name, content string) {
		if content == "" {
			return
		}
		fmt.Fprintf(&b, "<meta name=%q content=%q>\n", name, content)
	}
	w("og:type", "website")
	w("og:site_name", cfg.Name)
	w("og:title", title)
	w("og:description", desc)
	w("og:url", canon)
	w("og:locale", ogLocale(cfg.Langs))
	// summary 而不是 summary_large_image：没有图的那种卡片，后者的面积要求是白占的。
	n("twitter:card", "summary")
	n("twitter:title", title)
	n("twitter:description", desc)
	return template.HTML(b.String())
}

// ogLocale 把页头的语言表翻成 og:locale 认的写法（zh-Hans → zh_CN）。
//
// 认不出来的原样带过去：og:locale 本来就是 `<language>_<TERRITORY>` 的自由格式，
// 猜错一个（把没声明过的地域写上去）比不写更糟。
func ogLocale(langs []lang) string {
	for _, l := range langs {
		if !l.Ready {
			continue
		}
		if r, ok := ogLocales[l.Code]; ok {
			return r
		}
		return l.Code
	}
	return ""
}

var ogLocales = map[string]string{
	"zh-Hans": "zh_CN",
	"zh-Hant": "zh_TW",
	"en":      "en_US",
	"ja":      "ja_JP",
}

// pageDesc 是文档页那句说明：**这一页自己的第一段**，不是整站的 tagline。
//
// 十来页文档分享出去全写着同一句「为 vibecoding 而生的最佳网关」的话，那张卡片
// 就不带任何信息——点进去之前读的人分不出「排障」和「写一个模块」。
//
// 取不到第一段就退回整站那句（比如一页开篇就是一张表）：**有话说**比「说得最准」
// 重要，一句泛泛的介绍也好过一张空卡片。
func pageDesc(p page, fallback string) string {
	body := p.Body
	i := strings.Index(body, "<p>")
	if i < 0 {
		return fallback
	}
	j := strings.Index(body[i:], "</p>")
	if j < 0 {
		return fallback
	}
	text := plainText(body[i+len("<p>") : i+j])
	if text == "" {
		return fallback
	}
	return truncateRunes(text, descLimit)
}

// descLimit 是那句说明的上限（按**字符**算，不是字节）。
//
// 120 这个数按最坏情况挑的：中文一句话 120 字在搜索结果里刚好不截断，而英文同样
// 120 个字符远在限制之内。搜索引擎与聊天软件各自还有自己的截断，这里只是别把
// 整段正文塞进去。
const descLimit = 120

// plainText 把一小段 HTML 还原成纯文字：去掉标签、解开实体。
//
// 它服务的只有「描述」这一个用处，所以**不追求正确解析**：正文是我们自己渲染的
// （md.go 那几种构造，没有嵌套的怪东西），够用就行。写成一个完整 HTML 解析器的
// 话，那台机器比它要解决的问题大。
func plainText(html string) string {
	var b strings.Builder
	inTag := false
	for _, r := range html {
		switch {
		case r == '<':
			inTag = true
		case r == '>':
			inTag = false
		case !inTag:
			b.WriteRune(r)
		}
	}
	// 标签之间会留下空隙（`<strong>` 被摘掉之后那句话还是连着的，但块级构造会断开
	// 一行），把连续空白压成一个空格并且首尾去干净。
	return strings.Join(strings.Fields(stdhtml.UnescapeString(b.String())), " ")
}

// truncateRunes 按字符截断，并且**不加省略号**：中文的「…」会占掉一格，而各家
// 展示时本来就会自己截。截在半个词上不好看，但没有比「描述里自带一个省略号，
// 后面又跟一个平台的省略号」更常见的小丑行为。
func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

// basePath 是站点在域名下的挂载点。//
// GitHub Pages 的项目站是 `https://<user>.github.io/<repo>/`，所以**所有**链接都
// 得带那一层前缀。写死 `/newgate-ext/` 会让本地 `python3 -m http.server` 预览不了
// （那是从根服务的）——这里读一个环境变量，本地预览时设成 `/`：
//
//	SITEGEN_BASE=/ go run ./tools/sitegen
//
// 默认值（项目站那一层）是 CI 与真实部署走的那条，因为它才是「用户看到的那份」。
func basePath() string {
	if b := os.Getenv("SITEGEN_BASE"); b != "" {
		if !strings.HasSuffix(b, "/") {
			b += "/"
		}
		return b
	}
	return "/newgate-ext/"
}

// hrefOf 拼一个**指向站内**的地址（不含 base）：语言段 + 路由。
func hrefOf(langCode, route string) string {
	p := langPrefix(langCode)
	if p != "" {
		p += "/"
	}
	return p + strings.TrimPrefix(route, "/")
}
