// Command sitegen renders the markdown under site/src into a static site under site/dist.
//
// # Why write a renderer instead of using an SSG
//
// This repo has a rule: **the build must be fully offline**, the front-end toolchain
// only on dev machines (see modules/web-dashboard/module.go's package doc - the artifact
// is version-controlled for exactly this). Pulling Astro / VitePress means a node
// dependency in CI, or "editing a sentence of docs" becoming "remember to run the
// generator" - the road this repo already regretted once (the i18n ledger on line
// numbers, aging on every changed code line).
//
// So: content is plainest markdown, our own Go renderer with **zero third-party deps**
// (go.mod is zero-dep today; keep it), `go run ./tools/sitegen` ships.
//
// # Its attitude to "syntax it does not know"
//
// See md.go's package doc: error out rather than emit as is. That is this tool's one
// ratchet, and why it dares skip `-check`: output is not version-controlled (site/dist
// in .gitignore: a stale copy makes people think the site is like that).
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
	// srcDir/outDir are both relative to the repo root.
	srcDir = "site/src"
	outDir = "site/dist"
	// defaultLang is the language **served at the root path**: its pages land at `/`,
	// `/docs/`..., other languages under `/<code>/`. Today only Chinese, so it means
	// "/ is the Chinese site"; adding English puts it in `/en/`, no Chinese URL changes.
	defaultLang = "zh-Hans"
	// markdownExt is the content extension. File name is the route (see routeOf).
	markdownExt = ".md"
	// faviconSrc is the site icon, **read from the product's copy**, not a duplicate under site/src.
	//
	// Two copies of a brand mark: harmless year one, one stale by year two - and "which
	// is current" is invisible from the files. The demo page (vite.demo.config.ts's
	// publicDir) points at the same directory, so both paths get the same image.
	//
	// It is a **static file**, not part of a front-end toolchain: `--docs` (the path
	// that runs without node) reads it too, because it is committed in the repo.
	faviconSrc = "modules/web-dashboard/web/public/favicon.svg"
	// archSrc is the architecture diagram, under site/src, **version-controlled with the site**.
	//
	// The opposite trade from favicon: "change it with the source" - the diagram draws
	// this repo's assembly, and a module change is a stroke. Under site/src, one
	// `site/build.sh` picks it up, no extra copy to remember. README references the same
	// file (`site/src/architecture.svg`) - github renders relative SVGs, so no second copy.
	archSrc = "site/src/architecture.svg"
)

// site is site-level config (site/src/site.json): site name, external links, language table.
//
// Navigation is **not** here: it has one file per language (`site/src/<lang>/nav.json`).
// The reason is direct - two languages rarely share a directory structure (the English
// site may have fewer pages), and merging them means one split the day a language is added.
type site struct {
	Name    string `json:"name"`
	Tagline string `json:"tagline"`
	// Desc is one sentence on what this site is. **Not** a tagline repeat: the tagline is
	// the big headline (the home page's h1), Desc is the `<meta name="description">` and
	// share-card sentence - only the latter is read by search engines and chat apps.
	//
	// The split is practical: a tagline is short and loud ("the best gateway for
	// vibecoding"), a description **says what it is** (who, between what, solving what);
	// otherwise a shared link is just a slogan and readers must click to find out.
	Desc string `json:"desc"`
	// URL is the address the site is published at. canonical and og:url are built from it
	// plus this page's route - so it must be the **public** one (today's project site
	// carries the `/newgate-ext/` segment), not the local preview address.
	URL      string `json:"url"`
	Kernel   string `json:"kernel"`
	Releases string `json:"releases"`
	Langs    []lang `json:"langs"`

	// Theme is the color the browser's **own UI** (phone address bar, status bar) takes.
	//
	// **Not read from site.json**: run fills it in after reading site.css - its only
	// correct value is `--bg`, and two copies drift (see theme.go's themeColors).
	Theme theme `json:"-"`
}

type lang struct {
	Code string `json:"code"`
	// Label is the text in the header's language switch ("Chinese", "English").
	Label string `json:"label"`
	// Ready false = this language is not written yet (drawn disabled in the header). It
	// **does not claim English exists**: an English button that does nothing is worse.
	Ready bool `json:"ready"`
}

// navItem is one row of the nav table (`site/src/<lang>/nav.json`).
//
// The title is written here, not the page's `# heading`: writing it twice is
// **deliberate**. The sidebar name must be short ("Quick start"), the page heading is
// often longer ("Quick start: zero to first chain"); matching them would cut body headings.
type navItem struct {
	Route string `json:"route"`
	Title string `json:"title"`
}

// page is one rendered page.
type page struct {
	Route string // with leading and trailing slashes: "/", "/docs/", "/docs/quickstart/"
	Title string // the page's own <h1> (<title> uses it)
	Body  string // the rendered HTML fragment
	Lang  string
}

// outFile is one thing to write into dist (collect first, write at the end; see run).
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
	// Styles are read once here (the template also uses them for theme-color); writing
	// uses the same bytes - two reads would rely on luck to see the same file.
	css, err := os.ReadFile(filepath.Join(src, "site.css"))
	if err != nil {
		return fmt.Errorf("cannot read the site stylesheet (%s): %w", path.Join(srcDir, "site.css"), err)
	}
	if cfg.Theme, err = themeColors(string(css)); err != nil {
		return fmt.Errorf("%s: %w", path.Join(srcDir, "site.css"), err)
	}
	if len(cfg.Langs) == 0 {
		return fmt.Errorf("no language is declared in %s (langs is empty)", path.Join(srcDir, "site.json"))
	}
	var defaultReady bool
	for _, l := range cfg.Langs {
		if l.Code == defaultLang {
			defaultReady = true
		}
	}
	if !defaultReady {
		return fmt.Errorf("langs does not contain %q -- it is the one served at the root path (see defaultLang in main.go)", defaultLang)
	}

	// Collect everything to write first, touch dist/ last: if rendering fails (md.go
	// errors) the old copy **stays as is**, not left half-written - "site unreachable"
	// is far worse than "site is one version behind".
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

	// Site icon: **copied** from the product's copy (see faviconSrc). A copy rather than a
	// link because the site artifact must be **servable on its own** - the site directory is
	// what is deployed; pointing into another repo dir would 404 in preview and on Pages.
	fav, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(faviconSrc)))
	if err != nil {
		return fmt.Errorf("cannot read the site icon (%s): %w", faviconSrc, err)
	}
	files = append(files, outFile{rel: "favicon.svg", bytes: fav})

	// Architecture diagram: copied into the output too (same "site must stand alone" rule).
	arch, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(archSrc)))
	if err != nil {
		return fmt.Errorf("cannot read the architecture diagram (%s): %w", archSrc, err)
	}
	files = append(files, outFile{rel: "architecture.svg", bytes: arch})
	// .nojekyll: GitHub Pages runs Jekyll by default, and it swallows files and directories
	// starting with an underscore (we have none today, but the gate is pure cost, zero gain).
	files = append(files, outFile{rel: ".nojekyll", bytes: nil})

	if err := emit(out, files); err != nil {
		return err
	}
	// Page count is **counted separately**, not len(files) minus a constant: that constant
	// changes with every static file; a missed update never errors, the log just starts lying.
	pages := 0
	for _, f := range files {
		if strings.HasSuffix(f.rel, ".html") {
			pages++
		}
	}
	fmt.Printf("sitegen: %d pages -> %s\n", pages, out)
	return nil
}

// langPrefix is a language's URL prefix (the default language is empty = served at the root).
//
// It and defaultLang are two sides of one thing, so they sit together - written apart,
// someone changes one and the Chinese site moves to /zh-Hans/ with every bookmark dead.
func langPrefix(code string) string {
	if code == defaultLang {
		return ""
	}
	return code
}

// routeOf turns one markdown file under `site/src/<lang>/` into a route.
//
//	index.md              -> /
//	docs/index.md         -> /docs/
//	docs/quickstart.md    -> /docs/quickstart/
//
// Directory-style routes (trailing slash) use the GitHub Pages redirect: GH Pages 301s
// `/docs` to `/docs/`, so both addresses reach the same place. `/docs.html` has no
// redirect, but then the tree no longer matches the URLs (one more mental layer).
func routeOf(rel string) string {
	rel = strings.TrimSuffix(filepath.ToSlash(rel), markdownExt)
	rel = strings.TrimSuffix(rel, "/index")
	if rel == "" || rel == "index" {
		return "/"
	}
	return "/" + rel + "/"
}

// loadLang reads every markdown file of one language, sorted by route.
func loadLang(src, code string) ([]page, error) {
	dir := filepath.Join(src, code)
	if _, err := os.Stat(dir); err != nil {
		return nil, fmt.Errorf("the language directory does not exist (%s) -- if langs declares it, it has to have content: %w", path.Join(srcDir, code), err)
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
			// The error must carry **which file**: the renderer only says "line N has a ####",
			// and without a file name the author searches their own pages for it.
			return fmt.Errorf("%s: %w", path.Join(srcDir, code, filepath.ToSlash(rel)), err)
		}
		pages = append(pages, page{Route: routeOf(rel), Title: title, Body: body, Lang: code})
		return nil
	})
	if err != nil {
		return nil, err
	}
	if len(pages) == 0 {
		return nil, fmt.Errorf("%s contains no markdown at all", path.Join(srcDir, code))
	}
	sort.Slice(pages, func(i, j int) bool { return pages[i].Route < pages[j].Route })
	return pages, nil
}

func readSite(src string) (site, error) {
	var cfg site
	p := filepath.Join(src, "site.json")
	b, err := os.ReadFile(p)
	if err != nil {
		return cfg, fmt.Errorf("cannot read the site config (%s): %w", path.Join(srcDir, "site.json"), err)
	}
	if err := json.Unmarshal(b, &cfg); err != nil {
		return cfg, fmt.Errorf("%s is not valid JSON: %w", path.Join(srcDir, "site.json"), err)
	}
	if cfg.Name == "" {
		return cfg, fmt.Errorf("%s has no name", path.Join(srcDir, "site.json"))
	}
	if cfg.Desc == "" {
		// Without it the page still renders; only the share card is empty - so say it here,
		// not when someone clicks a link (same criterion as checkNav: silent gaps shout here).
		return cfg, fmt.Errorf("%s has no desc (<meta name=\"description\"> and share cards both use it; do not substitute the tagline)", path.Join(srcDir, "site.json"))
	}
	if cfg.URL == "" {
		return cfg, fmt.Errorf("%s has no url (canonical and share cards build absolute addresses from it)", path.Join(srcDir, "site.json"))
	}
	if !strings.HasSuffix(cfg.URL, "/") {
		// Absolute addresses are **built**, so it either ends in / or every concatenation
		// remembers one. The former removes the latter: today og:url misses a slash and
		// disagrees with canonical, visible only when the two cards are side by side.
		cfg.URL += "/"
	}
	return cfg, nil
}

func readNav(src, code string) ([]navItem, error) {
	p := filepath.Join(src, code, "nav.json")
	b, err := os.ReadFile(p)
	if err != nil {
		return nil, fmt.Errorf("cannot read the navigation table (%s): %w", path.Join(srcDir, code, "nav.json"), err)
	}
	var nav []navItem
	if err := json.Unmarshal(b, &nav); err != nil {
		return nil, fmt.Errorf("%s is not valid JSON: %w", path.Join(srcDir, code, "nav.json"), err)
	}
	return nav, nil
}

// checkNav is the site's only "a step was skipped" gate: nav and pages must match both ways.
//
// Each direction is a real mistake: a page written but not in the nav (nobody finds it),
// or a nav entry for a deleted page (clicking 404s). Neither fails rendering, so only
// this check can speak.
func checkNav(nav []navItem, pages []page) error {
	have := map[string]string{}
	for _, p := range pages {
		have[p.Route] = p.Title
	}
	listed := map[string]bool{}
	for _, n := range nav {
		if _, ok := have[n.Route]; !ok {
			return fmt.Errorf("navigation entry %q has no matching page (expected %s/%s%s, or remove it from nav.json)",
				n.Route, srcDir, n.Route, markdownExt)
		}
		listed[n.Route] = true
		if n.Title == "" {
			return fmt.Errorf("navigation entry %q has no title", n.Route)
		}
	}
	for _, p := range pages {
		if !listed[p.Route] {
			return fmt.Errorf("page %q (%s) is not in the navigation table -- adding a doc means also hooking it into nav.json", p.Route, p.Title)
		}
	}
	return nil
}

// emit writes the collected files into dist. **Start over whole**: delete, then write.
//
// Why not overwrite: deleting a doc leaves its rendered index.html on disk, with no nav
// entry - a page **only search engines and old links reach**, and the author never learns.
// Starting over costs one rm; worth it.
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

// repoRoot walks up to the repo root (the level with both go.mod and dist.json).
//
// Same criterion as tools/archmap's repoRoot: go.mod alone also matches `core/` (a
// submodule), so running from a subdirectory would resolve to the kernel's level.
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
			return "", fmt.Errorf("cannot find the repo root (it must have both go.mod and dist.json)")
		}
		dir = parent
	}
}
