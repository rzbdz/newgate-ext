// The markdown subset renderer. **Deliberately** small: only what we write in site/src.
//
// # Why not a "recognize as much as possible" parser
//
// Because the failure mode here is **silent**: an unrecognized construct appears as is -
// `#### heading` sits in the prose as hashes, a table missing its separator becomes pipes.
// The author gets no signal (thinks it was recognized), and the reader sees broken layout.
//
// So the criterion flips: **anything unrecognized errors out** (a failed build, not a
// broken page), and "error" beats "remember to rerun the generator" - you need not recall.
// This is the same ratchet as elsewhere in this repo: a wrong artifact beats no artifact
// (see tools/archmap's "a wrong map is worse than none").
//
// # What is recognized
//
//	# level one (first is the page title)   ## level two   ### level three
//	body paragraphs (blank-line separated)
//	- bullet list  1. ordered list (both **no nesting**)
//	> blockquote
//	``` fenced code (nothing inside is parsed)
//	| table | (needs a separator row)
//	--- horizontal rule
//	inline: `code`, **bold**, [text](link)
//
// Links are in siteHref: **in-site addresses are site-root paths** (`/docs/tiers/`), and
// the mount prefix is added by the renderer. Authors should not see "this site lives
// under /newgate-ext/" - that is deployment detail; the site must serve from any subpath.
//
// # What is not recognized (all error, never silent)
//
//	#### and deeper   nested lists   indented code blocks   ![img](...)   HTML tags
//	unclosed ``` / ` / ** / [   headless tables (looks like a table, no separator)
//	=== setext headings
//
// Every error carries a **line number**; the caller adds the file name (see loadLang).
package main

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// parseImage parses one `![alt](src)`, returning alt, src and **the rest of the string**.
//
// Only the simplest form (no title, no parentheses in path): the site places images in
// exactly one spot, the architecture diagram; a full CommonMark parser is not worth it.
func parseImage(s string) (alt, src, rest string, err error) {
	if !strings.HasPrefix(s, "![") {
		return "", "", s, fmt.Errorf("not an image: %q", s)
	}
	close := strings.Index(s, "](")
	if close < 0 {
		return "", "", s, fmt.Errorf("image syntax is not closed: %q", s)
	}
	after := s[close+2:]
	end := strings.Index(after, ")")
	if end < 0 {
		return "", "", s, fmt.Errorf("the image address is not closed: %q", s)
	}
	return s[2:close], after[:end], after[end+1:], nil
}

// renderMarkdown renders one markdown file to an HTML fragment and extracts the page title.
//
// Title = the first `# ` line. It is **removed from the body**: the template draws `<h1>`
// itself (one h1 per site, the shape a11y and SEO want), and a second one makes two.
func renderMarkdown(src string) (body string, title string, err error) {
	src = strings.ReplaceAll(src, "\r\n", "\n")
	lines := strings.Split(src, "\n")

	var out strings.Builder
	var para []string // paragraph lines accumulated so far
	// featOpen: an eyebrow (`## <small>01</small> ...`) opened a `<section class="feat">`
	// that is not closed yet. See closeFeat below.
	featOpen := false
	closeFeat := func() {
		if featOpen {
			out.WriteString("</section>\n")
			featOpen = false
		}
	}
	flushPara := func() error {
		if len(para) == 0 {
			return nil
		}
		h, err := inline(joinLines(para))
		if err != nil {
			return err
		}
		out.WriteString("<p>" + h + "</p>\n")
		para = nil
		return nil
	}

	for i := 0; i < len(lines); i++ {
		line := lines[i]
		ln := i + 1 // the line number a human sees

		trimmed := strings.TrimRight(line, " \t")
		if strings.TrimSpace(trimmed) == "" {
			if err := flushPara(); err != nil {
				return "", "", err
			}
			continue
		}

		// Indented content: markdown calls it a code block or nested list; we accept neither.
		// Silent, those lines become a paragraph, indentation lost, layout collapses.
		if strings.HasPrefix(trimmed, "    ") || strings.HasPrefix(trimmed, "\t") {
			if err := flushPara(); err != nil {
				return "", "", err
			}
			return "", "", fmt.Errorf("line %d is indented: this site does not accept indented code blocks or nested lists, write it as its own paragraph (or inside a ``` fence)", ln)
		}

		switch {
		case strings.HasPrefix(trimmed, "```"):
			if err := flushPara(); err != nil {
				return "", "", err
			}
			fence := strings.TrimSpace(strings.TrimPrefix(trimmed, "```"))
			var code []string
			closed := false
			for j := i + 1; j < len(lines); j++ {
				if strings.HasPrefix(strings.TrimRight(lines[j], " \t"), "```") {
					i, closed = j, true
					break
				}
				code = append(code, lines[j])
			}
			if !closed {
				return "", "", fmt.Errorf("the ``` on line %d is not closed", ln)
			}
			out.WriteString(codeBlock(fence, code) + "\n")

		case strings.HasPrefix(trimmed, "#"):
			if err := flushPara(); err != nil {
				return "", "", err
			}
			n := 0
			for n < len(trimmed) && trimmed[n] == '#' {
				n++
			}
			if n > 3 {
				return "", "", fmt.Errorf("line %d uses a level-%d heading: this site only renders down to ### (one level deeper usually means this page should be split in two)", ln, n)
			}
			text := strings.TrimSpace(trimmed[n:])
			if text == "" {
				return "", "", fmt.Errorf("line %d is an empty heading", ln)
			}
			h, err := inline(text)
			if err != nil {
				return "", "", err
			}
			if n == 1 {
				if title != "" {
					return "", "", fmt.Errorf("line %d is another level-1 heading (the page title can only be the one on the first line)", ln)
				}
				title = strings.TrimSpace(text)
				continue
			}
			if n == 2 {
				// Eyebrow: `## <small>01</small> Take over your CLI`. It **starts a section**, so
				// close the previous one first (two eyebrows in a row = two sections).
				if eb, rest, ok, err := eyebrow(text); err != nil {
					return "", "", fmt.Errorf("line %d: %w", ln, err)
				} else if ok {
					closeFeat()
					out.WriteString(`<section class="feat"><h2><small>` + esc(eb) + "</small> " + rest + "</h2>\n")
					featOpen = true
					continue
				}
			}
			// Other headings (`## Fork it...`, `### ...`) **do not belong to the previous section**:
			// an eyebrow's section ends at the next heading. Without this it would run to the page end.
			closeFeat()
			out.WriteString(fmt.Sprintf("<h%d>%s</h%d>\n", n, h, n))

		case strings.HasPrefix(trimmed, "|"):
			if err := flushPara(); err != nil {
				return "", "", err
			}
			var rows []string
			for ; i < len(lines); i++ {
				t := strings.TrimRight(lines[i], " \t")
				if !strings.HasPrefix(t, "|") {
					i--
					break
				}
				rows = append(rows, t)
			}
			tbl, err := table(rows, ln)
			if err != nil {
				return "", "", err
			}
			out.WriteString(tbl + "\n")

		case strings.HasPrefix(trimmed, "> "), trimmed == ">":
			if err := flushPara(); err != nil {
				return "", "", err
			}
			var quote []string
			for ; i < len(lines); i++ {
				t := strings.TrimRight(lines[i], " \t")
				if !strings.HasPrefix(t, ">") {
					i--
					break
				}
				quote = append(quote, strings.TrimPrefix(strings.TrimPrefix(t, ">"), " "))
			}
			h, err := inline(joinLines(quote))
			if err != nil {
				return "", "", err
			}
			out.WriteString("<blockquote><p>" + h + "</p></blockquote>\n")

		case isHR(trimmed):
			if err := flushPara(); err != nil {
				return "", "", err
			}
			out.WriteString("<hr>\n")

		case listMarker(trimmed) != "":
			if err := flushPara(); err != nil {
				return "", "", err
			}
			var raw []string
			ordered := listMarker(trimmed) == "ol"
			for ; i < len(lines); i++ {
				t := strings.TrimRight(lines[i], " \t")
				if strings.TrimSpace(t) == "" {
					i--
					break
				}
				mark := listMarker(t)
				if mark == "" {
					// **Continuation**: this line is not a new list item but continues the previous
					// one - a Chinese sentence often wraps, so `**bold**` must not be forced onto one line.
					// Block constructs (table/quote/fence/heading/indent) do not continue; they are next.
					if strings.TrimSpace(t) != t {
						// Indented but not a new item = **nested list** (`  - level two`).
						// The main loop's "indent errors" check cannot reach **inside a list**
						// (this for consumes those lines), so repeat the exact same check
						// - miss it in one place and a nested list joins the previous item.
						// The item gains a few words on the page, and the author thinks it is a sublist.
						if listMarker(strings.TrimSpace(t)) != "" {
							return "", "", fmt.Errorf("line %d is a nested list: this site only renders one level (split it into two paragraphs, or write it as its own sentence)", i+1)
						}
						if len(t)-len(strings.TrimLeft(t, " \t")) >= 4 || strings.HasPrefix(t, "\t") {
							return "", "", fmt.Errorf("line %d is indented: this site does not accept indented code blocks or nested lists, put it inside a ``` fence", i+1)
						}
					}
					if len(raw) == 0 || !isContinuation(t) {
						i--
						break
					}
					raw[len(raw)-1] = joinLines([]string{raw[len(raw)-1], strings.TrimSpace(t)})
					continue
				}
				if (mark == "ol") != ordered {
					return "", "", fmt.Errorf("line %d mixes ordered and unordered lists: write them as two separate paragraphs", i+1)
				}
				raw = append(raw, inlineItem(t, mark))
			}
			items := make([]string, 0, len(raw))
			for _, r := range raw {
				it, err := inline(r)
				if err != nil {
					return "", "", err
				}
				items = append(items, it)
			}
			out.WriteString(list(ordered, items) + "\n")

		case strings.HasPrefix(trimmed, "<"):
			return "", "", fmt.Errorf("line %d starts with <: the body of this site does not contain HTML (it would be escaped and shown literally)", ln)

		case isSetext(trimmed, lines, i):
			return "", "", fmt.Errorf("underlined headings (=== / ---) on line %d are not supported, write ## ", ln)

		default:
			para = append(para, trimmed)
		}
	}
	if err := flushPara(); err != nil {
		return "", "", err
	}
	closeFeat()
	if title == "" {
		return "", "", fmt.Errorf("this file has no level-1 heading (the first line should be `# Title`)")
	}
	return out.String(), title, nil
}

// eyebrow recognizes an eyebrow heading: `## <small>01</small> Take over your CLI`.
//
// # Why a syntax opening for it
//
// The home page's features are **numbered** (01...08), and the number should be a small
// eyebrow, not as heavy as the heading. Without an opening there are two roads, both
// worse: put the number in the heading text (`## 01 Take over...`, as black and big as
// the body, looking like part of the heading rather than a tick), or hand-write
// `<span>` - and HTML in the body is **forbidden** (the check for lines starting with <).
//
// It is a **closed** little construct: only a leading `<small>...</small>`; the rest
// still goes through inline rendering (`**bold**`, `code` still work). A whole HTML
// fragment, or a mid-line tag, still hits "no HTML in the body" - that author is
// thinking in HTML, while this page's body is markdown.
// Third return = whether the line is an eyebrow (otherwise the other two mean nothing).
func eyebrow(text string) (tag, rest string, ok bool, err error) {
	if !strings.HasPrefix(text, "<small>") {
		if strings.HasPrefix(text, "<") {
			// Leading < but not <small>: rather than falling into "no HTML" (whose message
			// is "no HTML in the body" though the author wrote our supported eyebrow
			// syntax, just wrong), spell out the one valid form here.
			return "", "", false, fmt.Errorf("a leading < has only one legal form: `## <small>01</small> Title`")
		}
		return "", "", false, nil
	}
	end := strings.Index(text, "</small>")
	if end < 0 {
		return "", "", false, fmt.Errorf("<small> is not closed (an eyebrow is written `## <small>01</small> Title`)")
	}
	tag = text[len("<small>"):end]
	rest = strings.TrimSpace(text[end+len("</small>"):])
	if rest == "" {
		return "", "", false, fmt.Errorf("there is no heading after the eyebrow (`## <small>01</small> Take over your CLI`)")
	}
	if strings.ContainsAny(tag, "<>&\"") {
		return "", "", false, fmt.Errorf("do not put markup inside an eyebrow: %q", tag)
	}
	h, err := inline(rest)
	if err != nil {
		return "", "", false, err
	}
	return tag, h, true, nil
}

// isSetext recognizes old-style headings: text, then a next line of all = or -.
//
// Recognized separately because it **looks like** what we accept (`---` is a rule):
// silent, you get text with a rule under it, which the author thought was a heading.
func isSetext(trimmed string, lines []string, i int) bool {
	if i+1 >= len(lines) {
		return false
	}
	next := strings.TrimRight(lines[i+1], " \t")
	if next == "" {
		return false
	}
	return strings.Trim(next, "=") == "" || strings.Trim(next, "-") == ""
}

func isHR(t string) bool {
	if len(t) < 3 {
		return false
	}
	c := t[0]
	if c != '-' && c != '*' && c != '_' {
		return false
	}
	return strings.Trim(t, string(c)) == ""
}

// listMarker says whether this line is a list item, and which kind ("ul" / "ol").
func listMarker(t string) string {
	if strings.HasPrefix(t, "- ") || t == "-" {
		return "ul"
	}
	i := 0
	for i < len(t) && t[i] >= '0' && t[i] <= '9' {
		i++
	}
	if i > 0 && strings.HasPrefix(t[i:], ". ") {
		return "ol"
	}
	return ""
}

// isContinuation says whether this line **continues** the previous paragraph (soft wrap).
//
// Criterion: would it open another block construct - table, quote, fence, heading,
// rule, indent. If not, it is the unfinished half of the previous sentence - normal in
// Chinese prose (one sentence per line); `**bold**` in a list item must not error on a wrap.
func isContinuation(t string) bool {
	for _, p := range []string{"|", ">", "```", "#", "    ", "\t"} {
		if strings.HasPrefix(t, p) {
			return false
		}
	}
	return !isHR(t) && listMarker(t) == ""
}

func inlineItem(t, mark string) string {
	if mark == "ul" {
		return strings.TrimSpace(strings.TrimPrefix(t, "-"))
	}
	i := strings.Index(t, ". ")
	return strings.TrimSpace(t[i+2:])
}

func list(ordered bool, items []string) string {
	tag := "ul"
	if ordered {
		tag = "ol"
	}
	var b strings.Builder
	b.WriteString("<" + tag + ">\n")
	for _, it := range items {
		b.WriteString("<li>" + it + "</li>\n")
	}
	b.WriteString("</" + tag + ">")
	return b.String()
}

func codeBlock(fence string, code []string) string {
	cls := ""
	if fence != "" {
		// The language tag is only used for the class (no highlight lib: offline open).
		lang := fence
		if i := strings.IndexAny(lang, " \t"); i >= 0 {
			lang = lang[:i]
		}
		cls = ` class="lang-` + esc(lang) + `"`
	}
	return "<pre><code" + cls + ">" + esc(strings.Join(code, "\n")) + "</code></pre>"
}

// table renders a table. rows are the **consecutive lines starting with |**.
//
// A table must have a separator row (`| --- | --- |`) - that is GFM syntax, and the
// only place where "meant a table, forgot the separator" gets caught: otherwise it
// renders as a paragraph with a line of pipes, looking broken with no hint to the author.
func table(rows []string, line int) (string, error) {
	if len(rows) < 2 {
		return "", fmt.Errorf("a table starts on line %d, but it only has one row -- a table needs at least a header plus a separator row (| --- |)", line)
	}
	cells := func(s string) []string {
		s = strings.TrimSpace(strings.Trim(s, "|"))
		parts := strings.Split(s, "|")
		for i := range parts {
			parts[i] = strings.TrimSpace(parts[i])
		}
		return parts
	}
	head := cells(rows[0])
	sep := cells(rows[1])
	for _, c := range sep {
		if strings.Trim(c, ":-") != "" {
			return "", fmt.Errorf("line %d should be the table separator row (only - and :), actually %q", line+1, c)
		}
	}
	if len(sep) != len(head) {
		return "", fmt.Errorf("the separator row on line %d has %d cells, the header has %d", line+1, len(sep), len(head))
	}
	align := make([]string, len(sep))
	for i, c := range sep {
		l, r := strings.HasPrefix(c, ":"), strings.HasSuffix(c, ":")
		switch {
		case l && r:
			align[i] = "center"
		case r:
			align[i] = "right"
		}
	}
	var b strings.Builder
	b.WriteString("<table>\n<thead><tr>")
	for i, c := range head {
		h, err := inline(c)
		if err != nil {
			return "", err
		}
		b.WriteString("<th" + alignAttr(align[i]) + ">" + h + "</th>")
	}
	b.WriteString("</tr></thead>\n<tbody>\n")
	for _, row := range rows[2:] {
		cs := cells(row)
		if len(cs) != len(head) {
			return "", fmt.Errorf("table row %d has %d cells, the header has %d", line+2, len(cs), len(head))
		}
		b.WriteString("<tr>")
		for i, c := range cs {
			v, err := inline(c)
			if err != nil {
				return "", err
			}
			b.WriteString("<td" + alignAttr(align[i]) + ">" + v + "</td>")
		}
		b.WriteString("</tr>\n")
	}
	b.WriteString("</tbody></table>")
	return b.String(), nil
}

func alignAttr(a string) string {
	if a == "" {
		return ""
	}
	return ` style="text-align:` + a + `"`
}

// joinLines joins the lines of a paragraph into one.
//
// Chinese and English join differently: English lines need a space (`the` + `gateway`
// must not fuse), Chinese **must not** (the extra space is a visible burr in CJK type).
// The criterion is whether the previous line's last and this line's first char are CJK.
func joinLines(lines []string) string {
	var b strings.Builder
	for i, l := range lines {
		if i > 0 {
			if !(isCJK(lastRune(lines[i-1])) && isCJK(firstRune(l))) {
				b.WriteString(" ")
			}
		}
		b.WriteString(l)
	}
	return b.String()
}

func firstRune(s string) rune {
	for _, r := range s {
		return r
	}
	return 0
}

func lastRune(s string) rune {
	var last rune
	for _, r := range s {
		last = r
	}
	return last
}

// isCJK is that block: CJK characters and full-width punctuation. Good enough; no full Unicode.
func isCJK(r rune) bool {
	switch {
	case r >= 0x4E00 && r <= 0x9FFF: // Han characters
		return true
	case r >= 0x3000 && r <= 0x303F: // Chinese punctuation
		return true
	case r >= 0xFF00 && r <= 0xFFEF: // full width
		return true
	}
	return false
}

// inline renders inline constructs. **Unrecognized text stays as is** (but for the few
// that error below): inline syntax lacks the strong "looks broken" consequences of blocks.
func inline(s string) (string, error) {
	// Check bold pairs first: a lone `**` shows up as is, and the author thinks it is bold.
	if strings.Count(s, "**")%2 != 0 {
		return "", fmt.Errorf("** inline is not paired: %q", s)
	}
	var b strings.Builder
	for i := 0; i < len(s); {
		switch {
		case s[i] == '`':
			j := strings.IndexByte(s[i+1:], '`')
			if j < 0 {
				return "", fmt.Errorf("an inline backtick is not closed: %q", s)
			}
			b.WriteString("<code>" + esc(s[i+1:i+1+j]) + "</code>")
			i += j + 2

		case strings.HasPrefix(s[i:], "**"):
			j := strings.Index(s[i+2:], "**")
			if j < 0 {
				return "", fmt.Errorf("** inline is not closed: %q", s)
			}
			inner, err := inline(s[i+2 : i+2+j])
			if err != nil {
				return "", err
			}
			b.WriteString("<strong>" + inner + "</strong>")
			i += j + 4

		case strings.HasPrefix(s[i:], "!["):
			// **The architecture diagram is the one allowed kind** (2026-09-30, user asked).
			//
			// This used to error unconditionally, citing README's "no screenshots/terminal
			// transcripts". That ban is on **screen-recording screenshots** (which make
			// people not want to read), not "explain structure with one image" - the latter
			// is exactly what the home page lacks most.
			//
			// Fixed on `src`: **our own .svg**. External images add an uptime dependency.
			alt, src, rest, err := parseImage(s[i:])
			if err != nil {
				return "", err
			}
			if !strings.HasSuffix(src, ".svg") || strings.Contains(src, "://") {
				return "", fmt.Errorf("the site only serves its own svg (external links and bitmaps are not accepted, see the README style rules): %q", src)
			}
			b.WriteString(`<img class="figure" src="` + esc(src) + `" alt="` + esc(alt) + `">`)
			i += len(s[i:]) - len(rest)

		case s[i] == '[':
			j := strings.Index(s[i:], "](")
			if j < 0 {
				return "", fmt.Errorf("an inline [ is not paired with ](link): %q", s)
			}
			k := strings.IndexByte(s[i+j+2:], ')')
			if k < 0 {
				return "", fmt.Errorf("an inline link is not closed: %q", s)
			}
			text, href := s[i+1:i+j], s[i+j+2:i+j+2+k]
			inner, err := inline(text)
			if err != nil {
				return "", err
			}
			link, err := siteHref(href)
			if err != nil {
				return "", err
			}
			b.WriteString(`<a href="` + esc(link) + `">` + inner + `</a>`)
			i += j + 2 + k + 1

		default:
			// **Iterate by rune, not byte**: in `string(s[i])` that s[i] is a byte, and int-to-
			// string converts by **rune** - every Chinese lead byte becomes a Latin-1 char
			// (mangled like `ä¸º`). The mistake is vicious: ASCII (commands, code, English)
			// is fine and only Chinese is mangled, so at first glance it is "an encoding
			// problem" and you check the file's charset, while the file was fine all along.
			r, size := utf8.DecodeRuneInString(s[i:])
			b.WriteString(esc(string(r)))
			i += size
		}
	}
	return b.String(), nil
}

// siteHref turns a link the author wrote into the address actually emitted.
//
// # Why this has to happen here
//
// In-site addresses are written as **site-root paths** (`/docs/tiers/`) - authors should
// not hand-write the deployment prefix. GitHub Pages serves a project site under
// `/<repo>/`, so the same `/docs/tiers/` is right in source and a **404** when emitted raw.
//
// This has really happened, and **silently**: every page rendered, the nav (a template
// with prefixed addresses) was fine, and only **links in the body** 404'd. The local
// `--local` preview serves from the root, so locally it is right - only clicking online
// finds it (2026-09-21: 16 links across 7 pages).
//
// # What is accepted and what is not
//
// Accepted: external (http/https/mailto, as is), in-site absolute paths (prefix added),
// in-page anchors (as is, same page). Not accepted: relative paths like `a.md` and
// protocol-relative `//host` - the former has no meaning here (the source tree is not
// the published tree, `docs/tiers.md` maps to `/docs/tiers/`, unbuildable by concat),
// the latter rare and easy to get wrong. Both error out, better than shipping a 404.
func siteHref(href string) (string, error) {
	switch {
	case strings.HasPrefix(href, "//"):
		return "", fmt.Errorf("link %q is protocol-relative: write it out as a full https://... (this site does not need protocol-relative)", href)
	case strings.HasPrefix(href, "/"):
		return basePath() + strings.TrimPrefix(href, "/"), nil
	case strings.HasPrefix(href, "#"):
		return href, nil
	}
	for _, p := range []string{"http://", "https://", "mailto:"} {
		if strings.HasPrefix(href, p) {
			return href, nil
		}
	}
	return "", fmt.Errorf("unrecognized link %q: in-site addresses are written as a path under the site root (`/docs/tiers/`), "+
		"out-of-site ones as a full https://... -- **never a relative path**: the source tree and the published "+
		"tree are not the same thing, and the address that builds is a 404 online (that is how 16 of them "+
		"slipped through on 2026-09-21)", href)
}

// esc escapes the five HTML characters. **This is the only exit**: every string that
// enters the body passes through it, so "a < in source never becomes a tag" has one gate.
func esc(s string) string {
	r := strings.NewReplacer(
		"&", "&amp;",
		"<", "&lt;",
		">", "&gt;",
		`"`, "&quot;",
		"'", "&#39;",
	)
	return r.Replace(s)
}
