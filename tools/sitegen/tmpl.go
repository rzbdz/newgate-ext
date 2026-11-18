package main

import (
	"fmt"
	stdhtml "html"
	"html/template"
	"os"
	"strings"
)

// pageTmpl is the site's one HTML shell.
//
// Why a Go template rather than string concatenation: this shell is **used by every page**
// and contains structures that repeat (the current nav item, the two footer links). Hand-
// concatenated, the first redesign misses one section of one page.
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
  <p>This page describes the assembly of the <strong>{{.Site.Name}}</strong> distribution. The authoritative text on kernel
     mechanisms is in <a href="{{.Site.Kernel}}">rzbdz/newgate</a>.</p>
  <p><a href="{{.Site.Kernel}}">内核</a> · <a href="{{.Site.Releases}}">版本发布</a></p>
</footer>
</body>
</html>
`))

// landingTmpl is the home page's shell. It shares the header/footer **language switch**
// with doc pages but not the sidebar doc shell - the home page is a product front.
//
// How the demo is embedded: wrap `<iframe src="{base}demo/">` in a browser-window shell
// (.browser). That is not a screenshot; the **real** UI runs - the demo page is the same
// front end's second entry point (see modules/web-dashboard/web/vite.demo.config.ts).
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

  <!-- The one box on the home page has **two tabs**: the running UI (a real component fed
       fake data) and the assembly map (one SVG, a static file).

       Why side by side in the same box instead of two stacked sections: they answer two
       sides of the same question -- "what does this look like" and "how is this put
       together". Split into two sections, the first screen would carry two big blocks in a
       row and nobody scrolls to the second.

       It opens on the **UI** tab (that is the selling point); role=tablist and
       aria-selected are for screen readers, and the arrow keys switch. Both share the same
       .browser frame, so switching moves nothing but the contents -- which is what keeps
       it from feeling like a page jump. -->
  <div class="browser" data-tabs>
    <div class="browser-bar">
      <span class="dot"></span><span class="dot"></span><span class="dot"></span>
      <div class="browser-tabs" role="tablist" aria-label="newgate 控制台">
        <button role="tab" aria-selected="true" data-tab="ui">newgate web</button>
        <button role="tab" aria-selected="false" data-tab="arch">装配图</button>
      </div>
      <span class="addr">newgate-ext / dist.json</span>
    </div>
    <div class="browser-pane" data-pane="ui" role="tabpanel">
      <iframe src="{{.Base}}demo/" title="newgate 控制台" loading="lazy"></iframe>
    </div>
    <div class="browser-pane" data-pane="arch" role="tabpanel" hidden>
      <img src="{{.Base}}architecture.svg" alt="一份规格书喂给内核的组装器，本发行版的全部模块装进同一个二进制">
    </div>
  </div>
  <ul class="chips">
    <li>一个静态二进制</li>
    <li>PATH shim 接管，配置就地改写</li>
    <li>只有一个第三方依赖（SSH）</li>
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
<script>
  // The home page box's two tabs (see .browser above).
  //
  // Hand-written, nothing pulled in: no build step, no third-party scripts, and this
  // is the **only** place needing JS. Pulling in a framework would sell that rule.
  //
  // Two behaviors: tabs switch pages; a tab click **keeps focus in the button row**
  // (else focus falls to body and screen readers restart). Arrow keys too.
  (function () {
    var box = document.querySelector('.browser[data-tabs]');
    if (!box) return;
    var tabs = [].slice.call(box.querySelectorAll('[role=tab]'));
    var panes = [].slice.call(box.querySelectorAll('[data-pane]'));
    function show(name) {
      tabs.forEach(function (t) {
        var on = t.dataset.tab === name;
        t.setAttribute('aria-selected', on ? 'true' : 'false');
      });
      panes.forEach(function (p) { p.hidden = p.dataset.pane !== name; });
    }
    tabs.forEach(function (t, i) {
      t.addEventListener('click', function () { show(t.dataset.tab); });
      t.addEventListener('keydown', function (e) {
        var d = e.key === 'ArrowRight' ? 1 : e.key === 'ArrowLeft' ? -1 : 0;
        if (!d) return;
        e.preventDefault();
        var next = tabs[(i + d + tabs.length) % tabs.length];
        show(next.dataset.tab);
        next.focus();
      });
    });
  })();
</script>
</body>
</html>
`))

// renderLanding renders the home page (route "/").
//
// The home page body is markdown too (`site/src/zh-Hans/index.md`), going through the same
// renderer as doc pages - only the shell differs. That keeps "content in markdown, styles
// in CSS"; the front-page feel comes from shell + CSS, not divs piled into the body.
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
		return "", fmt.Errorf("rendering the landing template failed: %w", err)
	}
	return b.String(), nil
}

// navView / langView are the two row kinds the template eats (config plus computed fields).
//
// Computed before the template (not string compares inside it) because **how links are
// built** has one implementation - the site's base (one more `/newgate-ext/` on GitHub
// Pages), the language segment, the route's trailing slash: three things where "get it
// wrong and 404". `{{.Base}}{{.Route}}` looks fine but scatters the rule into the
// template; a future "subpath deploy" would need another search there.
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

// renderPage wraps one page in the shell.
func renderPage(cfg site, l lang, nav []navItem, p page) (string, error) {
	if p.Route == "/" && strings.TrimPrefix(p.Route, "/") == "" {
		// Landing (route "/"): not the doc sidebar shell but a dedicated page.
		// The criterion lives in the function, not config: a site has one home page; this is
		// not a product decision each route can make for itself.
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
			// Ready is decided by config, **not inferred from "the page exists"**: a new English
			// site starts with two pages; inferred, the header's language switch would flicker.
			Ready:   other.Ready,
			Current: other.Code == l.Code,
		})
	}

	var b strings.Builder
	// pageData's comment is at the two template.HTML spots below: **templates escape by
	// default**, and Body is HTML we already rendered (md.go's esc is its only exit). So
	// this cell is declared trusted explicitly - conversely, a `<` in the body became
	// `&lt;` back in md.go and never reaches here.
	//
	// Desc takes the page's own first paragraph. **Not site.json's sentence**: a dozen doc
	// pages each share their own (the troubleshooting card saying the tagline says nothing).
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
		return "", fmt.Errorf("rendering the template failed: %w", err)
	}
	return b.String(), nil
}

// pageTitle is a page's name in <title> and on the share card.
//
// Two page kinds, two forms, two audiences: the **home page** uses the site sentence
// (it is the front, shared as "what is newgate-ext"), **doc pages** put their own
// title first ("Troubleshooting - newgate-ext" - with many tabs, that prefix tells).
//
// It feeds both <title> and og:title. Written twice, the card and the tab disagree -
// an inconsistency no one sees unless the two sit side by side.
func pageTitle(cfg site, p page) string {
	if p.Route == "/" {
		return cfg.Name + " — " + cfg.Tagline
	}
	return p.Title + " · " + cfg.Name
}

// socialMeta is the share-card lines (Open Graph + Twitter Card).
//
// # Why hand-write these lines
//
// Pasted into a chat app, forum or Slack, the other side reads **these lines, not the
// page** - without og:title / og:description a link is just a bare address. And that
// is exactly how tools like this spread ("look at this"). Of the cc-switch structure
// worth copying, its full og:* set is the most substantial: nothing else.
//
// # title / desc / canon all come from parameters, never recomputed here
//
// Home and doc pages differ in all three; the caller already computed them (home title
// is the site's, doc pages their own; description likewise; canonical per route).
// Recomputing here is a second truth that drifts, and the home page's copy would
// **silently become the wrong one** (a dozen doc pages, all titled alike).
//
// # Why no og:image
//
// An image-less card (`summary`) is more honest than a made-up image. A card with an
// og:image looks far better but needs a **raster** image (1200x630), and this site has
// only favicon.svg - SVG unfurlers mostly reject it. Making one means an image library
// or a hand-drawn binary. So: **none is none**, not an og:image pointing at a 404.
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
	// summary, not summary_large_image: the latter's larger area is wasted without an image.
	n("twitter:card", "summary")
	n("twitter:title", title)
	n("twitter:description", desc)
	return template.HTML(b.String())
}

// ogLocale turns the header's language table into what og:locale expects (zh-Hans -> zh_CN).
//
// Unrecognized values pass through as is: og:locale is `<language>_<TERRITORY>` free-form,
// and guessing wrong (writing an undeclared territory) is worse than writing nothing.
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

// pageDesc is a doc page's description: **that page's own first paragraph**, not site-wide.
//
// A dozen doc pages all sharing "the best gateway for vibecoding" makes the card carry
// no information - before clicking, readers cannot tell "Troubleshooting" from "write a module".
//
// Without a first paragraph it falls back to the site sentence (a page opening with a
// table): **having something to say** beats "saying it most accurately".
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

// descLimit is the description's cap (in **characters**, not bytes).
//
// 120 is the worst case: 120 Chinese characters fits search results unclipped, and 120
// English characters is well under the limit. Search engines and chat apps truncate on
// their own; this only keeps the whole body out.
const descLimit = 120

// plainText turns a small HTML snippet back into plain text: strip tags, unescape entities.
//
// It serves only "description", so **correct parsing is not a goal**: the body is ours
// (md.go's handful of constructs, no odd nesting), enough is enough. A full HTML parser
// would be a bigger machine than the problem it solves.
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
	// Tags leave gaps (both `<strong>` removal keeps the sentence joined, but block constructs
	// break a line); collapse consecutive whitespace to one space and trim both ends.
	return strings.Join(strings.Fields(stdhtml.UnescapeString(b.String())), " ")
}

// truncateRunes truncates by character and **adds no ellipsis**: a Chinese "..." takes
// a cell, and each display truncates by itself. Cutting mid-word is ugly, but nothing
// is more clownish than "an ellipsis in the description, then another from the platform".
func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

// basePath is where the site is mounted under the domain. //
// GitHub Pages project sites are `https://<user>.github.io/<repo>/`, so **all** links
// must carry that prefix. Hardcoding `/newgate-ext/` breaks local `python3 -m http.server`
// preview (which serves from the root) - so read an environment variable, `/` locally:
//
//	SITEGEN_BASE=/ go run ./tools/sitegen
//
// The default (the project-site segment) is what CI and real deployment use.
func basePath() string {
	if b := os.Getenv("SITEGEN_BASE"); b != "" {
		if !strings.HasSuffix(b, "/") {
			b += "/"
		}
		return b
	}
	return "/newgate-ext/"
}

// hrefOf builds an **in-site** address (no base): language segment + route.
func hrefOf(langCode, route string) string {
	p := langPrefix(langCode)
	if p != "" {
		p += "/"
	}
	return p + strings.TrimPrefix(route, "/")
}
