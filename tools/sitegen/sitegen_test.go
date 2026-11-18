package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// This family tests the **renderer**, not the content. Content changes daily; two rules
// must not move:
//
//	1. recognized constructs are recognized (else stray `**` or a missing table);
//	2. **unrecognized constructs error** (the ratchet, see md.go's header).
//
// The second matters most: it is the premise for skipping `-check` (no baseline).
// Tested, "syntax we never implemented" goes red **at edit time**, not at page view.

func render(t *testing.T, src string) (string, string) {
	t.Helper()
	body, title, err := renderMarkdown(src)
	if err != nil {
		t.Fatalf("should render but errored: %v", err)
	}
	return body, title
}

func TestHeadingsAndTitle(t *testing.T) {
	body, title := render(t, "# Page title\n\n## Section\n\n### Subsection\n")
	if title != "Page title" {
		t.Errorf("title taken wrong: %q", title)
	}
	// The h1 is **not drawn into the body**: the template draws <h1>, so two would appear.
	if strings.Contains(body, "<h1>") {
		t.Errorf("the body drew an h1 again: %s", body)
	}
	for _, want := range []string{"<h2>Section</h2>", "<h3>Subsection</h3>"} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %s: %s", want, body)
		}
	}
}

func TestParagraphsAndInline(t *testing.T) {
	body, _ := render(t, "# T\n\na sentence with **bold**, `code` and a [link](https://x/).\n")
	for _, want := range []string{
		"<p>a sentence with <strong>bold</strong>, <code>code</code> and a " +
			"<a href=\"https://x/\">link</a>.</p>",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %s\nactual: %s", want, body)
		}
	}
}

// TestSiteInternalLinksGetTheMountPrefix: in-site body links must carry the mount prefix.
//
// # Found from **live data** (2026-09-21)
//
// Source writes site-root paths (`/docs/tiers/`), but the emitted address needs the
// `/newgate-ext/` segment. Emitted raw it 404s, and **silently everywhere**: the page
// renders, nav (template, prefixed) is fine, `--local` preview (root) is fine - only
// clicking online finds it. 16 links across 7 pages.
//
// So this tests more than "add the prefix": **each of three forms keeps its own rule**:
// external as is, anchors as is, in-site paths prefixed. Mixing two mispoints a link.
func TestSiteInternalLinksGetTheMountPrefix(t *testing.T) {
	body, _ := render(t, "# T\n\nsee [tiers](/docs/tiers/), [external](https://x/y), "+
		"[this page](#h).\n")
	for _, want := range []string{
		`<a href="/newgate-ext/docs/tiers/">tiers</a>`,
		`<a href="https://x/y">external</a>`,
		`<a href="#h">this page</a>`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %s\nactual: %s", want, body)
		}
	}

	// Unrecognized forms **error**, not ship a 404. Relative paths especially: the source
	// tree is not the published tree (`docs/tiers.md` maps to `/docs/tiers/`), and the
	// writer likely assumed it works as on GitHub.
	for _, bad := range []string{"docs/tiers/", "./x/", "//host/x", "x.md"} {
		if _, _, err := renderMarkdown("# T\n\nsee [this](" + bad + ").\n"); err == nil {
			t.Errorf("should error when the link is written as %q (it is a 404 online, "+
				"but the author thinks it is right)", bad)
		}
	}
}

// TestASoftWrappedBoldInsideAListIsOneItem is **the real shape of Chinese prose**.
//
// One sentence per line, so `**bold**` splits across two lines - the first version
// rendered each line as a list item and errored "unpaired". The error was right (it
// was unpaired) but **the criterion was wrong**: soft wraps should join. This locks joining.
func TestASoftWrappedBoldInsideAListIsOneItem(t *testing.T) {
	// Fixture stays Chinese: the subject is a bold span across a soft wrap in Chinese
	// prose written one sentence per line; the needle must match the fixture verbatim.
	body, _ := render(t, "# T\n\n- 前半句**加粗的\n  后半句**，结束了\n- 第二项\n")
	if strings.Count(body, "<li>") != 2 {
		t.Errorf("the two list items were split into %d (the soft wrap did not join):\n%s",
			strings.Count(body, "<li>"), body)
	}
	if !strings.Contains(body, "<strong>加粗的后半句</strong>") {
		t.Errorf("the bold was not joined across the wrap: %s", body)
	}
}

// TestCJKJoinHasNoSpace: joining Chinese lines must **not** add a space.
//
// With one, a visible seam appears: "between Claude Code and the model you chose"
// becomes "... and the ..." - a typo alone, broken type as a page, and nobody reports it.
func TestCJKJoinHasNoSpace(t *testing.T) {
	// Fixture stays Chinese: this test's subject is CJK joining, so the characters on both
	// sides of the wrap must be Chinese, and the needle matches them verbatim.
	body, _ := render(t, "# T\n\n这是一个很长很长的句子，写到一半\n换行了然后继续。\n")
	if !strings.Contains(body, "写到一半换行了") {
		t.Errorf("a space was inserted at the Chinese line wrap: %s", body)
	}
	// The English half is the reverse: it **needs** a space, or two words fuse into one.
	body, _ = render(t, "# T\n\nthis sentence wraps\ninto two lines\n")
	if !strings.Contains(body, "wraps into two") {
		t.Errorf("the English line wrap did not get a space: %s", body)
	}
}

func TestCodeFenceKeepsEverything(t *testing.T) {
	body, _ := render(t, "# T\n\n```jsonc\n{ \"a\": 1 }   "+
		"// ** and ` inside a comment must not be parsed\n```\n")
	if !strings.Contains(body, `<code class="lang-jsonc">`) {
		t.Errorf("the fence language marker did not make it into the class: %s", body)
	}
	if !strings.Contains(body, "// ** and ` inside a comment must not be parsed") {
		t.Errorf("the content inside the fence was parsed: %s", body)
	}
}

func TestTableNeedsASeparator(t *testing.T) {
	body, _ := render(t, "# T\n\n| A | B |\n| --- | ---: |\n| 1 | 2 |\n")
	for _, want := range []string{
		"<th>A</th>", "<th style=\"text-align:right\">B</th>",
		"<td>1</td>", "<td style=\"text-align:right\">2</td>",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %s: %s", want, body)
		}
	}
	if _, _, err := renderMarkdown("# T\n\n| A | B |\n| 1 | 2 |\n"); err == nil {
		t.Error("a table without a separator row should error (it renders as a row of " +
			"pipes and the author gets no signal)")
	}
}

func TestQuoteAndRule(t *testing.T) {
	body, _ := render(t, "# T\n\n> one quote\n> and then this\n\n---\n")
	if !strings.Contains(body, "<blockquote><p>one quote and then this</p></blockquote>") {
		t.Errorf("the quote was not joined: %s", body)
	}
	if !strings.Contains(body, "<hr>") {
		t.Errorf("the horizontal rule was not drawn: %s", body)
	}
}

// TestEscapesHTML is a **safety** criterion: a < written in the body must become &lt;.
//
// The site is static and the source is ours, so this is not "anti-attack"; it is
// anti-"write it wrong": a placeholder like `<your-name>` reaching HTML raw is eaten by
// the browser as an unknown tag, and that paragraph loses a chunk out of nowhere.
func TestEscapesHTML(t *testing.T) {
	body, _ := render(t, "# T\n\nreplace <your-name> with your name, and & must be escaped too.\n")
	if !strings.Contains(body, "&lt;your-name&gt;") || !strings.Contains(body, "&amp;") {
		t.Errorf("not escaped: %s", body)
	}
}

// TestUnrecognizedSyntaxIsAnError: **this is the ratchet**. Each case is "silently drawn
// wrong" - the page renders, just misformatted, while the author thinks it was accepted.
func TestUnrecognizedSyntaxIsAnError(t *testing.T) {
	cases := []struct {
		name string
		src  string
	}{
		{"level-4 heading", "# T\n\n#### too deep\n"},
		{"indented code block", "# T\n\n    four spaces of indent\n"},
		{"nested list", "# T\n\n- one level\n  - two levels\n"},
		// Image syntax **no longer errors unconditionally**: our own svg passes (see
		// md.go's parseImage). Still rejected: **external images and bitmaps** - they
		// depend on someone else's uptime or break "readable offline anywhere".
		{"external image", "# T\n\n![diagram](https://example.com/a.svg)\n"},
		{"bitmap", "# T\n\n![image](a.png)\n"},
		{"raw HTML", "# T\n\n<div>hand-written tag</div>\n"},
		{"underlined heading", "Title\n===\n"},
		{"unclosed fence", "# T\n\n```bash\nnewgate status\n"},
		{"unclosed bold", "# T\n\nhere is **an unclosed bold\n"},
		{"unclosed inline code", "# T\n\nhere is `some code\n"},
		{"unclosed link", "# T\n\nsee [this page](/docs/ right\n"},
		{"no level-1 heading", "## only level two\n"},
		{"two level-1 headings", "# one\n\n# two\n"},
		{"ordered and unordered mixed", "# T\n\n- one\n1. two\n"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, _, err := renderMarkdown(c.src); err == nil {
				t.Error("this form should error (it renders wrong silently while the author " +
					"thinks the renderer accepted it), but it rendered successfully")
			}
		})
	}
}

// TestLocalSvgFigureIsAllowed: our own svg passes, rendered as <img class="figure">.
//
// It pairs with "external/bitmap still errors" above: the architecture diagram is the
// one allowed image kind (2026-09-30, user request), and the scope **must be fixed**.
func TestLocalSvgFigureIsAllowed(t *testing.T) {
	body, _, err := renderMarkdown("# T\n\n![description](../../architecture.svg)\n")
	if err != nil {
		t.Fatalf("the site's own svg should be allowed, but it errored: %v", err)
	}
	for _, want := range []string{
		`src="../../architecture.svg"`, `alt="description"`, `class="figure"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the render result should contain %s, actual: %s", want, body)
		}
	}
}

// TestRouteOf: file name is the route; both kinds of index land on a directory.
func TestRouteOf(t *testing.T) {
	cases := map[string]string{
		"index.md":           "/",
		"docs/index.md":      "/docs/",
		"docs/quickstart.md": "/docs/quickstart/",
		"a/b/c.md":           "/a/b/c/",
	}
	for in, want := range cases {
		if got := routeOf(in); got != want {
			t.Errorf("routeOf(%q) = %q, should be %q", in, got, want)
		}
	}
}

// TestDefaultLangIsAtTheRoot pins "adding a language does not kill old addresses".
//
// Chinese is the language at the root (see defaultLang): `/docs/` is Chinese. English
// would go to `/en/` with no Chinese URL changing - the reverse (Chinese to `/zh-Hans/`)
// would kill every bookmark and external link the day a language is added.
func TestDefaultLangIsAtTheRoot(t *testing.T) {
	if got := langPrefix(defaultLang); got != "" {
		t.Errorf("the default language URL prefix should be empty, actual %q", got)
	}
	if got := langPrefix("en"); got != "en" {
		t.Errorf("other languages should fall under their own segment, actual %q", got)
	}
	if got := hrefOf(defaultLang, "/docs/quickstart/"); got != "docs/quickstart/" {
		t.Errorf("in-site address for the default language built wrong: %q", got)
	}
	if got := hrefOf("en", "/docs/quickstart/"); got != "en/docs/quickstart/" {
		t.Errorf("in-site address for a non-default language built wrong: %q", got)
	}
}

// TestCheckNavCatchesBothDirections: nav and pages must match both ways.
//
// Each direction is a real mistake, and neither **fails** rendering: a page written but
// not in the nav (nobody finds it), or a nav entry for a deleted page (clicking 404s).
func TestCheckNavCatchesBothDirections(t *testing.T) {
	pages := []page{{Route: "/", Title: "home"}, {Route: "/docs/", Title: "docs"}}
	nav := []navItem{{Route: "/", Title: "home"}, {Route: "/docs/", Title: "docs"}}

	if err := checkNav([]navItem{{Route: "/", Title: "home"}}, pages); err == nil {
		t.Error("should error when a page is not in the navigation")
	}
	gone := append(append([]navItem{}, nav...), navItem{Route: "/gone/", Title: "gone"})
	if err := checkNav(gone, pages); err == nil {
		t.Error("should error when the navigation has a page that does not exist")
	}
	if err := checkNav(nav, pages); err != nil {
		t.Errorf("it matched but errored: %v", err)
	}
}

// TestEyebrowHeadings tests the home page's numbered feature headings with an eyebrow.
//
// It is not only "recognized": an eyebrow's section must close at **the next heading**.
// Otherwise the page end gets a stray </section> or the last section runs to the end -
// both just "look odd" in a browser, nobody files a bug, so pin them here.
func TestEyebrowHeadings(t *testing.T) {
	body, _ := render(t, "# T\n\n## <small>01</small> Section one\n\nBody one.\n\n"+
		"## Fork it\n\nBody two.\n")
	if !strings.Contains(body, `<section class="feat"><h2><small>01</small> Section one</h2>`) {
		t.Errorf("the eyebrow did not open a section: %s", body)
	}
	// The second section (a plain heading, no eyebrow) **must not** be swallowed by the first.
	i := strings.Index(body, "</section>")
	j := strings.Index(body, "<h2>Fork it</h2>")
	if i < 0 || j < i {
		t.Errorf("section one did not close at the next heading:\n%s", body)
	}
	if strings.Count(body, "</section>") != 1 {
		t.Errorf("the section pair appears more than once: %s", body)
	}
}

// TestEyebrowOnlyWhereWeSaid: the eyebrow is **a closed little construct**; a typo errors.
//
// Each case is "the author thinks it is right, the page is just odd" - especially the
// first: they wrote our supported syntax, half wrong, and "no HTML" would leave them lost.
func TestEyebrowOnlyWhereWeSaid(t *testing.T) {
	cases := map[string]string{
		"not closed":                "# T\n\n## <small>01 Title\n",
		"no title after it":         "# T\n\n## <small>01</small>\n",
		"markup inside the eyebrow": "# T\n\n## <small><b>01</b></small> Title\n",
		"some other small tag":      "# T\n\n## <span>01</span> Title\n",
	}
	for name, src := range cases {
		t.Run(name, func(t *testing.T) {
			if _, _, err := renderMarkdown(src); err == nil {
				t.Error("this form should error (it renders wrong silently while the " +
					"author thinks the renderer accepted it)")
			}
		})
	}
}

// TestPageDescComesFromThePage: a doc page's description is **that page's own first paragraph**.
//
// A wrong pick does not go red; it just makes a dozen share cards say the site tagline,
// and that shows only after someone really pastes the link.
func TestPageDescComesFromThePage(t *testing.T) {
	body, _, err := renderMarkdown("# Troubleshooting\n\nRun `newgate doctor` first.\n\n" +
		"## One\n\nAfterword\n")
	if err != nil {
		t.Fatal(err)
	}
	got := pageDesc(page{Route: "/docs/troubleshooting/", Body: body}, "the site-wide sentence")
	if got != "Run newgate doctor first." {
		t.Errorf("description did not take the first paragraph: %q", got)
	}
	// Opening table, no paragraph: fall back to the site line - saying something beats
	// saying it best.
	if got := pageDesc(page{Route: "/x/", Body: "<table><tr><td>x</td></tr></table>"},
		"the site-wide sentence"); got != "the site-wide sentence" {
		t.Errorf("with no paragraph it should fall back to the site-wide sentence: %q", got)
	}
}

// TestPageTitleDiffersByPage: <title> and og:title are **the same** value, two page kinds.
//
// The home page uses the site sentence (others share "what is newgate-ext"), doc pages put
// their own title first (with many tabs open, that is how you spot the troubleshooting one).
func TestPageTitleDiffersByPage(t *testing.T) {
	cfg := site{Name: "newgate-ext", Tagline: "the best gateway built for vibecoding"}
	if got := pageTitle(cfg, page{Route: "/"}); got != "newgate-ext — "+
		"the best gateway built for vibecoding" {
		t.Errorf("home title: %q", got)
	}
	docPage := page{Route: "/docs/troubleshooting/", Title: "Troubleshooting"}
	if got := pageTitle(cfg, docPage); got != "Troubleshooting · newgate-ext" {
		t.Errorf("doc page title: %q", got)
	}
}

// TestSiteConfigMustCarryDescAndURL: without these the site still renders, only the share
// card is empty - so it can only error at generation time (same criterion as checkNav).
func TestSiteConfigMustCarryDescAndURL(t *testing.T) {
	dir := t.TempDir()
	write := func(body string) string {
		if err := os.WriteFile(filepath.Join(dir, "site.json"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return dir
	}
	if _, err := readSite(write(`{"name":"x","desc":"d","url":"https://e/"}`)); err != nil {
		t.Errorf("should pass but errored: %v", err)
	}
	if _, err := readSite(write(`{"name":"x","url":"https://e/"}`)); err == nil {
		t.Error("should error when desc is missing")
	}
	if _, err := readSite(write(`{"name":"x","desc":"d"}`)); err == nil {
		t.Error("should error when url is missing")
	}
	// Missing trailing slash = **completed**, not an error: addresses are built in one place.
	cfg, err := readSite(write(`{"name":"x","desc":"d","url":"https://e/abc"}`))
	if err != nil || cfg.URL != "https://e/abc/" {
		t.Errorf("the trailing slash was not completed: %q %v", cfg.URL, err)
	}
}

// TestEmitWipesTheOutput pins "start over whole".
//
// Overwriting hides well: after deleting a doc, its rendered index.html stays on disk
// with no nav entry - a page **only search engines and old links reach**, and the author
// never learns they missed the delete.
func TestEmitWipesTheOutput(t *testing.T) {
	out := t.TempDir()
	stale := filepath.Join(out, "old", "index.html")
	if err := os.MkdirAll(filepath.Dir(stale), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stale, []byte("left over from the last run"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := emit(out, []outFile{{rel: "index.html", bytes: []byte("the new one")}}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(stale); err == nil {
		t.Error("the page left over from the last run is still on disk - " +
			"the whole rebuild did not happen")
	}
	b, err := os.ReadFile(filepath.Join(out, "index.html"))
	if err != nil || string(b) != "the new one" {
		t.Errorf("the new copy was written wrong: %q %v", b, err)
	}
}

// TestThemeColorComesFromTheCSS: `theme-color` takes the two `--bg` values from the CSS.
//
// This pair guards two sides of one thing:
//
//  1. the value comes from **the stylesheet** (not a second copy in config - that
//     drifts, visible only as a wrong-colored address bar on a phone);
//  2. when it cannot be read it **errors** (a wrong value is worse than none: it looks right).
func TestThemeColorComesFromTheCSS(t *testing.T) {
	th, err := themeColors(":root { --bg: #0f1115; }\n" +
		"@media (prefers-color-scheme: light) { :root { --bg: #f6f7f9; } }\n")
	if err != nil {
		t.Fatalf("should have extracted the colors but errored: %v", err)
	}
	if th.Dark != "#0f1115" || th.Light != "#f6f7f9" {
		t.Errorf("extracted wrong: %+v", th)
	}
	// Order is meaning: the first (default) is dark. Reversed, a light-theme user sees the
	// bar tinted dark then jump to light - side by side, nobody sees which should come first.
	if got := string(themeMeta(th)); !strings.Contains(got, `content="#0f1115"`) ||
		strings.Index(got, "#0f1115") > strings.Index(got, "#f6f7f9") {
		t.Errorf("the dark one is not first: %s", got)
	}

	for _, c := range []struct{ name, css string }{
		{"no --bg at all", ":root { --ink: #fff; }"},
		{"only one", ":root { --bg: #0f1115; }"},
		{"three", ":root{--bg:#a;} :root{--bg:#b;} :root{--bg:#c;}"},
		{"not a solid color", ":root { --bg: linear-gradient(#a, #b); } :root { --bg: #fff; }"},
		{"is var()", ":root { --bg: var(--other); } :root { --bg: #fff; }"},
	} {
		t.Run(c.name, func(t *testing.T) {
			if _, err := themeColors(c.css); err == nil {
				t.Error("this CSS should error (guessing a color is worse than no theme-color)")
			}
		})
	}

	// A name fragment does not count: `--bg-soft` / `--bgfoo` are not `--bg`.
	// Get this wrong and one extra `--bg-soft` in the stylesheet makes theme-color report "three".
	th, err = themeColors(":root { --bg: #0f1115; --bg-soft: #161a21; }\n" +
		"@media (prefers-color-scheme: light) { :root { --bg: #f6f7f9; } }\n")
	if err != nil || th.Dark != "#0f1115" {
		t.Errorf("`--bg-soft` was counted as `--bg`: %+v %v", th, err)
	}

	// A `--bg` mentioned in a comment does not count (this stylesheet comments a lot).
	th, err = themeColors(":root { /* --bg: #000; the line above is real */ --bg: #0f1115; }\n" +
		"@media (prefers-color-scheme: light) { :root { --bg: #f6f7f9; } }\n")
	if err != nil || th.Dark != "#0f1115" {
		t.Errorf("the --bg inside a comment was counted: %+v %v", th, err)
	}
}

// TestTheRealStylesheetHasATableTheme is the **live** half of this criterion: the two
// `--bg` values in the real site.css must be readable and must be solid colors.
//
// The test above tests the function with my own CSS - green even when the repo's
// stylesheet changed shape. This one reads **the copy being published**.
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
		t.Fatalf("cannot extract theme-color from the site stylesheet: %v", err)
	}
	if !strings.HasPrefix(th.Dark, "#") || !strings.HasPrefix(th.Light, "#") {
		t.Errorf("what was extracted is not a solid color: %+v", th)
	}
}
