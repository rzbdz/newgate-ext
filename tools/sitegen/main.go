// Command sitegen 把 site/src 下的 markdown 渲染成 site/dist 下的静态站。
//
// # 为什么自己写渲染器，而不是引一个 SSG
//
// 这个仓库有一条规矩：**构建必须完全离线**，前端工具链只在开发机出现
// （见 modules/web-dashboard/module.go 那段包注释——产物进版本控制就是为了它）。
// 引 Astro / VitePress 等于给 CI 加一条 node 依赖，或者让「改一句文档」变成
// 「记得跑一遍生成器」——后者是这个仓库已经吃过一次亏的那条路
// （i18n 账本当年挂在行号上，每改一行代码就过期）。
//
// 所以：内容是最朴素的 markdown，渲染器是我们自己的一个 Go 程序，**零第三方依赖**
// （go.mod 今天就是零依赖，这条不破），`go run ./tools/sitegen` 一条命令出站。
//
// # 它对「不认识的语法」的态度
//
// 见 md.go 的包注释：报错退出，而不是原样吐出去。这是这个工具唯一的一道棘轮，
// 也是它敢不写 `-check` 的原因——产物不进版本控制（site/dist 在 .gitignore 里，
// 与 dist/architecture.html 同一条理由：过期的那份会让人以为站点是那样），所以
// 没有落盘的基线可比；能守的就只剩「渲染器不认识的东西不许悄悄过去」。
package main

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

const (
	// srcDir/outDir 都是相对仓库根。
	srcDir = "site/src"
	outDir = "site/dist"
	// defaultLang 是**排在根路径**的那一种语言：它的页面落在 `/`、`/docs/`…，
	// 其余语言落在 `/<code>/` 下。今天只有中文，这个选择等于「/ 就是中文站」；
	// 加英文时英文进 `/en/`，中文的地址一个都不用变（书签不作废）。
	defaultLang = "zh-Hans"
	// markdownExt 是内容的扩展名。文件名即路由（见 routeOf）。
	markdownExt = ".md"
	// faviconSrc 是站点图标，**读产品那一份**，不在 site/src 下再放一个复制品。
	//
	// 一个品牌标记存两份，第一年不会有事，第二年就有一份是旧的——而「哪一份是新的」
	// 光看两个文件看不出来。演示页那边（vite.demo.config.ts 的 publicDir）指的就是
	// 同一个目录，所以两条路取到的是同一张图。
	//
	// 它是**静态文件**，不是前端工具链的一部分：`--docs`（不装 node 也能跑的那条）
	// 照样读得到它，因为它已经提交在仓库里。
	faviconSrc = "modules/web-dashboard/web/public/favicon.svg"
)

// site 是站点级配置（site/src/site.json）：站名、外链、语言表。
//
// 导航**不在这里**：它按语言各有一份（`site/src/<lang>/nav.json`）。理由很直接
// ——两种语言的目录结构不会一样（英文站可能少几页），把它们塞进同一份配置，
// 加语言的那天就要拆一次。
type site struct {
	Name    string `json:"name"`
	Tagline string `json:"tagline"`
	// Desc 是一句话说明这个站点是什么。**不是** tagline 的复读：tagline 是门面上
	// 那句大字（首页的 h1 就是它），Desc 是 `<meta name="description">` 与分享卡片
	// 上那句话——搜索引擎与聊天软件里的那两行正文只认后者。
	//
	// 分开的判据很实在：tagline 要短、要响（「为 vibecoding 而生的最佳网关」），
	// 而 description 要**说清楚是什么**（谁、站在哪两者之间、解决什么），否则链接
	// 分享出去只有一句口号，读的人得点进去才知道这玩意儿干什么。
	Desc string `json:"desc"`
	// URL 是站点发布出去的那个地址。canonical 与 og:url 按它 + 这一页的路由拼
	// ——所以它必须是**对外**的那个（今天的项目站带 `/newgate-ext/` 那一层），
	// 不是本地预览用的地址。
	URL      string `json:"url"`
	Kernel   string `json:"kernel"`
	Releases string `json:"releases"`
	Langs    []lang `json:"langs"`

	// Theme 是浏览器自己那块界面该染的颜色（手机上的地址栏、状态栏）。
	//
	// **不从 site.json 读**，是 run 读 site.css 之后填进来的：它的唯一正确取值就是
	// `--bg`，写在两个地方就会漂移（见 theme.go 的 themeColors）。
	Theme theme `json:"-"`
}

type lang struct {
	Code string `json:"code"`
	// Label 是页头上那个语言切换里的字（「中文」「English」）。
	Label string `json:"label"`
	// Ready 为 false = 这一种还没写（页头画成禁用态）。**不谎称有英文**：
	// 画一个点得动的 English 而它后面什么都没有，比不画更糟。
	Ready bool `json:"ready"`
}

// navItem 是导航表的一行（`site/src/<lang>/nav.json`）。
//
// 标题写在这里、不取页面自己的 `# 标题`：两处都要写一遍是**故意的**。侧栏那个
// 名字要短（「快速开始」），而页面标题往往更长更完整（「快速开始：从零到第一条
// 链」）；强行让它们相等，就得为了侧栏好看去砍正文的标题。
type navItem struct {
	Route string `json:"route"`
	Title string `json:"title"`
}

// page 是一页渲染好的东西。
type page struct {
	Route string // 带首尾斜杠："/"、"/docs/"、"/docs/quickstart/"
	Title string // 页面自己的 <h1>（<title> 用它）
	Body  string // 渲染好的 HTML 片段
	Lang  string
}

// outFile 是要写进 dist 的一份东西（先攒齐、最后一起落盘，见 run 的注释）。
type outFile struct {
	rel   string
	bytes []byte
}

func main() {
	root, err := repoRoot()
	if err != nil {
		fail(err)
	}
	if err := run(root); err != nil {
		fail(err)
	}
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "sitegen:", err)
	os.Exit(1)
}

func run(root string) error {
	src := filepath.Join(root, srcDir)
	out := filepath.Join(root, outDir)

	cfg, err := readSite(src)
	if err != nil {
		return err
	}
	// 样式在这里读一次（模板也要用它算 theme-color），下面落盘时用的是同一份字节：
	// 读两次的话，两处看到的是同一时刻的同一份文件这件事就得靠运气。
	css, err := os.ReadFile(filepath.Join(src, "site.css"))
	if err != nil {
		return fmt.Errorf("读不到站点样式（%s）: %w", path.Join(srcDir, "site.css"), err)
	}
	if cfg.Theme, err = themeColors(string(css)); err != nil {
		return fmt.Errorf("%s: %w", path.Join(srcDir, "site.css"), err)
	}
	if len(cfg.Langs) == 0 {
		return fmt.Errorf("%s 里一种语言都没声明（langs 是空）", path.Join(srcDir, "site.json"))
	}
	var defaultReady bool
	for _, l := range cfg.Langs {
		if l.Code == defaultLang {
			defaultReady = true
		}
	}
	if !defaultReady {
		return fmt.Errorf("langs 里没有 %q——它是排在根路径上的那一种（见 main.go 的 defaultLang）", defaultLang)
	}

	// 先把这一趟要写的全部攒起来，最后才动 dist/：渲染失败时（md.go 会报错）
	// 旧的那一份**原样留着**，而不是被清空成半成品——「站点打不开」比「站点是
	// 上一版」糟得多。
	var files []outFile
	routes := map[string]bool{}

	for _, l := range cfg.Langs {
		if !l.Ready {
			continue
		}
		pages, err := loadLang(src, l.Code)
		if err != nil {
			return err
		}
		nav, err := readNav(src, l.Code)
		if err != nil {
			return err
		}
		if err := checkNav(nav, pages); err != nil {
			return err
		}
		for _, p := range pages {
			routes[p.Route] = true
			html, err := renderPage(cfg, l, nav, p)
			if err != nil {
				return err
			}
			files = append(files, outFile{rel: filepath.Join(langPrefix(l.Code), filepath.FromSlash(strings.Trim(p.Route, "/")), "index.html"), bytes: []byte(html)})
		}
	}

	files = append(files, outFile{rel: "site.css", bytes: css})

	// 站点图标：从产品那一份**抄过来**（见 faviconSrc 的注释）。拷一份而不是让页面
	// 直接链过去，是因为站点的产物要能**独立端出去**——站点目录是部署的那一份，
	// 它指着仓库里另一个目录的话，本地预览与 Pages 上都会去请求一个不存在的地址。
	fav, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(faviconSrc)))
	if err != nil {
		return fmt.Errorf("读不到站点图标（%s）: %w", faviconSrc, err)
	}
	files = append(files, outFile{rel: "favicon.svg", bytes: fav})
	// .nojekyll：GitHub Pages 默认拿 Jekyll 过一遍，而它会把下划线开头的文件与目录
	// 吃掉（我们今天的资源没有，但这道门是纯损失、零收益——一个空文件就关掉它）。
	files = append(files, outFile{rel: ".nojekyll", bytes: nil})

	if err := emit(out, files); err != nil {
		return err
	}
	// 页数**单独数**，不用 len(files) 去减常数：那个常数每加一份静态文件就要改一次，
	// 而改漏了不会报错——它只是让日志里的数字慢慢变成谎话。
	pages := 0
	for _, f := range files {
		if strings.HasSuffix(f.rel, ".html") {
			pages++
		}
	}
	fmt.Printf("sitegen: %d 页 → %s\n", pages, out)
	return nil
}

// langPrefix 是某种语言在 URL 上的前缀片段（默认语言是空串 = 排在根上）。
//
// 它与 defaultLang 是同一件事的两面，所以写在一起——分开写就会有人只改一处，
// 症状是「中文站整站搬去了 /zh-Hans/ 而没人发现书签全废」。
func langPrefix(code string) string {
	if code == defaultLang {
		return ""
	}
	return code
}

// routeOf 把 `site/src/<lang>/` 下的一份 markdown 变成路由。
//
//	index.md              → /
//	docs/index.md         → /docs/
//	docs/quickstart.md    → /docs/quickstart/
//
// 目录式路由（带尾斜杠）是为了 GitHub Pages 上「不带尾斜杠」那一跳：GH Pages 会
// 把 `/docs` 301 到 `/docs/`，两种地址都指得到同一个地方。写成 `/docs.html`
// 就没有这一跳，但目录结构会长得跟 URL 不一样（多一层心智负担）。
func routeOf(rel string) string {
	rel = strings.TrimSuffix(filepath.ToSlash(rel), markdownExt)
	rel = strings.TrimSuffix(rel, "/index")
	if rel == "" || rel == "index" {
		return "/"
	}
	return "/" + rel + "/"
}

// loadLang 读一种语言下的全部 markdown，按路由排序。
func loadLang(src, code string) ([]page, error) {
	dir := filepath.Join(src, code)
	if _, err := os.Stat(dir); err != nil {
		return nil, fmt.Errorf("语言目录不存在（%s）——langs 里声明了它就得有内容: %w", path.Join(srcDir, code), err)
	}
	var pages []page
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(p, markdownExt) {
			return nil
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		raw, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		body, title, err := renderMarkdown(string(raw))
		if err != nil {
			// 报错要带**是哪一份文件**：渲染器只会说「第 N 行有个 ####」，
			// 而没有文件名的话，作者得在自己写的几篇里找。
			return fmt.Errorf("%s: %w", path.Join(srcDir, code, filepath.ToSlash(rel)), err)
		}
		pages = append(pages, page{Route: routeOf(rel), Title: title, Body: body, Lang: code})
		return nil
	})
	if err != nil {
		return nil, err
	}
	if len(pages) == 0 {
		return nil, fmt.Errorf("%s 下一份 markdown 都没有", path.Join(srcDir, code))
	}
	sort.Slice(pages, func(i, j int) bool { return pages[i].Route < pages[j].Route })
	return pages, nil
}

func readSite(src string) (site, error) {
	var cfg site
	p := filepath.Join(src, "site.json")
	b, err := os.ReadFile(p)
	if err != nil {
		return cfg, fmt.Errorf("读不到站点配置（%s）: %w", path.Join(srcDir, "site.json"), err)
	}
	if err := json.Unmarshal(b, &cfg); err != nil {
		return cfg, fmt.Errorf("%s 不是合法 JSON: %w", path.Join(srcDir, "site.json"), err)
	}
	if cfg.Name == "" {
		return cfg, fmt.Errorf("%s 里没有 name", path.Join(srcDir, "site.json"))
	}
	if cfg.Desc == "" {
		// 缺了它页面照样出得来，只有分享出去的那张卡片是空的——所以在这里说，
		// 而不是等谁点开链接才发现（同 checkNav 那条的判据：静默的漏项要在这里嚷）。
		return cfg, fmt.Errorf("%s 里没有 desc（<meta name=\"description\"> 与分享卡片都用它，不该拿 tagline 顶）", path.Join(srcDir, "site.json"))
	}
	if cfg.URL == "" {
		return cfg, fmt.Errorf("%s 里没有 url（canonical 与分享卡片按它拼绝对地址）", path.Join(srcDir, "site.json"))
	}
	if !strings.HasSuffix(cfg.URL, "/") {
		// 绝对地址是**拼**出来的，所以它要么以 / 结尾、要么每一处拼的时候都记得补一个。
		// 前者让后者不存在：今天 og:url 就少一个斜杠地与 canonical 不一致，而两张卡片
		// 摆在一起才看得出来。
		cfg.URL += "/"
	}
	return cfg, nil
}

func readNav(src, code string) ([]navItem, error) {
	p := filepath.Join(src, code, "nav.json")
	b, err := os.ReadFile(p)
	if err != nil {
		return nil, fmt.Errorf("读不到导航表（%s）: %w", path.Join(srcDir, code, "nav.json"), err)
	}
	var nav []navItem
	if err := json.Unmarshal(b, &nav); err != nil {
		return nil, fmt.Errorf("%s 不是合法 JSON: %w", path.Join(srcDir, code, "nav.json"), err)
	}
	return nav, nil
}

// checkNav 是站点这一侧唯一的「漏了一步」的看门人：导航表与页面必须**双向对上**。
//
// 两个方向各是一种真实会犯的错：写了页面却忘了挂进导航（那一页谁也找不到），
// 或者导航里留着一页已经删掉的东西（点进去 404）。两种都不会让渲染失败，
// 所以只能在这里说。
func checkNav(nav []navItem, pages []page) error {
	have := map[string]string{}
	for _, p := range pages {
		have[p.Route] = p.Title
	}
	listed := map[string]bool{}
	for _, n := range nav {
		if _, ok := have[n.Route]; !ok {
			return fmt.Errorf("导航里的 %q 没有对应的页面（该有 %s/%s%s，或者把它从 nav.json 里删掉）",
				n.Route, srcDir, n.Route, markdownExt)
		}
		listed[n.Route] = true
		if n.Title == "" {
			return fmt.Errorf("导航里的 %q 没有标题", n.Route)
		}
	}
	for _, p := range pages {
		if !listed[p.Route] {
			return fmt.Errorf("页面 %q（%s）不在导航表里——加一篇文档要同时挂上 nav.json", p.Route, p.Title)
		}
	}
	return nil
}

// emit 把攒好的文件写进 dist。**先整份重来**：删掉整个目录再写。
//
// 为什么不是「覆盖写」：删掉一篇文档时，它渲染出来的那个 index.html 会留在盘上，
// 而导航里已经没有它了——那种页面**只有搜索引擎和旧链接找得到**，作者永远不会
// 发现自己删漏了。整份重来的代价是一次 rm，值得。
func emit(out string, files []outFile) error {
	if err := os.RemoveAll(out); err != nil {
		return err
	}
	for _, f := range files {
		p := filepath.Join(out, f.rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(p, f.bytes, 0o644); err != nil {
			return err
		}
	}
	return nil
}

// repoRoot 向上找到仓库根（同时有 go.mod 与 dist.json 的那一层）。
//
// 与 tools/archmap 的 repoRoot 同一条判据：只认 go.mod 的话，`core/` 也是一个
// module（它是 submodule），从某个子目录跑起来会认到内核那一层去。
func repoRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			if _, err := os.Stat(filepath.Join(dir, "dist.json")); err == nil {
				return dir, nil
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("找不到仓库根（该同时有 go.mod 与 dist.json）")
		}
		dir = parent
	}
}
