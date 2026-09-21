package main

import (
	"fmt"
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
<title>{{.DocTitle}}</title>
{{if .Desc}}<meta name="description" content="{{.Desc}}">
{{end}}<link rel="icon" type="image/svg+xml" href="{{.Base}}favicon.svg">
<link rel="stylesheet" href="{{.Base}}site.css">
</head>
<body>
<header class="top">
  <a class="brand" href="{{.Base}}"><strong>{{.Site.Name}}</strong></a>
  <span class="tag">{{.Site.Tagline}}</span>
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
<title>{{.DocTitle}}</title>
{{if .Desc}}<meta name="description" content="{{.Desc}}">
{{end}}<link rel="icon" type="image/svg+xml" href="{{.Base}}favicon.svg">
<link rel="stylesheet" href="{{.Base}}site.css">
</head>
<body>
<header class="top">
  <a class="brand" href="{{.Base}}"><strong>{{.Site.Name}}</strong></a>
  <span class="tag">{{.Site.Tagline}}</span>
  <span class="spacer"></span>
  <nav class="langs">{{range .Langs}}{{if .Ready}}<a href="{{.Href}}" {{if .Current}}class="on"{{end}}>{{.Label}}</a>{{else}}<span class="soon" title="not written yet">{{.Label}}</span>{{end}}{{end}}</nav>
</header>

<div class="hero">
  <p class="lead">{{.Site.Name}}</p>
  <h1>{{.Page.Title}}</h1>
  <p class="sub">{{.Site.Tagline}}</p>
  <p class="cta">
    <a class="button primary" href="{{.Base}}docs/quickstart/">快速开始</a>
    <a class="button ghost" href="{{.Site.Kernel}}">GitHub</a>
  </p>

  <div class="browser" aria-label="newgate 的真实界面演示（可点）">
    <div class="browser-bar">
      <span class="dot"></span><span class="dot"></span><span class="dot"></span>
      <span class="addr">newgate web · 档位绑定 · 就地编辑</span>
    </div>
    <iframe src="{{.Base}}demo/" title="newgate 控制台演示 — 真组件，假数据，没有后端" loading="eager"></iframe>
  </div>
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
		Site     site
		Base     string
		HTMLang  string
		DocTitle string
		Desc     string
		Page     struct {
			Route string
			Title string
			Body  template.HTML
			Lang  string
		}
		Langs []langView
	}{
		Site: cfg, Base: base, HTMLang: l.Code,
		DocTitle: cfg.Name + " — " + cfg.Tagline,
		Desc:     cfg.Tagline,
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
	err := pageTmpl.Execute(&b, struct {
		Site     site
		Base     string
		HTMLang  string
		DocTitle string
		Desc     string
		Page     struct {
			Route string
			Title string
			Body  template.HTML
			Lang  string
		}
		Nav   []navView
		Langs []langView
	}{
		Site:     cfg,
		Base:     base,
		HTMLang:  l.Code,
		DocTitle: p.Title + " · " + cfg.Name,
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

// basePath 是站点在域名下的挂载点。
//
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
