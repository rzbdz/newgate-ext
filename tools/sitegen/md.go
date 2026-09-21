// markdown 子集的渲染器。**故意的**小：只认我们自己在 site/src 里写的那几种构造。
//
// # 为什么不做成「尽量多认」的解析器
//
// 因为这里的失败方式是**静默的**：一段没被认出来的 markdown 会原样出现在页面上
// ——`#### 小标题` 就那么四个井号摆在正文里、一张少了分隔行的表变成一行竖线。
// 作者不会收到任何信号（他以为渲染器认了），读者看到的是排版坏了。
//
// 所以判据反过来：**认不出来的就报错退出**（编译失败，不是页面坏掉），
// 而「报错」比「记得重跑生成器」强——它连跑都不用记得。
// 这条与这个仓库别处的棘轮是同一条：错的产物比没有产物糟
// （见 tools/archmap 那句「画错的图比没图更糟」）。
//
// # 认哪些
//
//	# 一级（取第一个作页面标题）   ## 二级   ### 三级
//	正文段落（空行分隔）
//	- 无序列表  1. 有序列表（都**不支持嵌套**）
//	> 引用
//	``` 代码块（围栏内一个字都不解析）
//	| 表格 |（必须有分隔行）
//	---  水平线
//	行内：`代码`、**加粗**、[文字](链接)
//
// # 不认哪些（都报错，不静默）
//
//	#### 及更深   嵌套列表   缩进代码块   ![图](…)  源文件里的 HTML 标签
//	未闭合的 ``` / ` / ** / [  没头的表（第一行像表但没有分隔行）
//	=== 那种下划线式标题（setext）
//
// 每一句报错都带**行号**，调用方再补上文件名（见 loadLang）。
package main

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// renderMarkdown 把一份 markdown 渲染成 HTML 片段，并取出页面标题。
//
// 标题 = 第一行 `# `。它**从正文里摘掉**：模板自己画 `<h1>`（全站只有一个 h1，
// 这是无障碍与 SEO 都想要的形状），正文里再画一次就成了两个。
func renderMarkdown(src string) (body string, title string, err error) {
	src = strings.ReplaceAll(src, "\r\n", "\n")
	lines := strings.Split(src, "\n")

	var out strings.Builder
	var para []string // 攒着的段落行
	// featOpen：眉题（`## <small>01</small> …`）开了一个 `<section class="feat">`，
	// 还没关。见下面 closeFeat。
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
		ln := i + 1 // 给人看的行号

		trimmed := strings.TrimRight(line, " \t")
		if strings.TrimSpace(trimmed) == "" {
			if err := flushPara(); err != nil {
				return "", "", err
			}
			continue
		}

		// 缩进的东西：markdown 里是「代码块」或「嵌套列表」，我们两个都不认。
		// 静默的后果是那几行会被当成普通段落、缩进丢掉、排版塌成一行。
		if strings.HasPrefix(trimmed, "    ") || strings.HasPrefix(trimmed, "\t") {
			if err := flushPara(); err != nil {
				return "", "", err
			}
			return "", "", fmt.Errorf("第 %d 行缩进了：本站不认缩进代码块与嵌套列表，把它写成独立的段落（或者放进 ``` 围栏里）", ln)
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
				return "", "", fmt.Errorf("第 %d 行的 ``` 没有闭合", ln)
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
				return "", "", fmt.Errorf("第 %d 行用了 %d 级标题：本站只画到 ###（再深一层通常说明这一页该拆成两页）", ln, n)
			}
			text := strings.TrimSpace(trimmed[n:])
			if text == "" {
				return "", "", fmt.Errorf("第 %d 行是个空标题", ln)
			}
			h, err := inline(text)
			if err != nil {
				return "", "", err
			}
			if n == 1 {
				if title != "" {
					return "", "", fmt.Errorf("第 %d 行又是一个一级标题（页面标题只能是第一行那一个）", ln)
				}
				title = strings.TrimSpace(text)
				continue
			}
			if n == 2 {
				// 眉题：`## <small>01</small> 接管你的 CLI`。它是**一节的开头**，
				// 所以先把上一节关上（连着两个眉题 = 两节）。
				if eb, rest, ok, err := eyebrow(text); err != nil {
					return "", "", fmt.Errorf("第 %d 行: %w", ln, err)
				} else if ok {
					closeFeat()
					out.WriteString(`<section class="feat"><h2><small>` + esc(eb) + "</small> " + rest + "</h2>\n")
					featOpen = true
					continue
				}
			}
			// 别的标题（`## Fork 下来…`、`### …`）都**不属于上一节**：眉题开的那一节
			// 到下一个标题为止。没有这一句的话，那一节会一路吃到页尾。
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
					// **续行**：这一行不是新的列表项，但它接着上一项——中文一句话
					// 常常写满两行，`**加粗**` 也就不该被硬要求挤在一行里。
					// 块级构造（表格/引用/围栏/标题/缩进）不接，那是下一块东西。
					if strings.TrimSpace(t) != t {
						// 缩进了却不是新的一项 = **嵌套列表**（`  - 二层`）。
						// 主循环那道「缩进就报错」的检查在**列表内部**够不着这里
						// （这几行是被这个 for 吃掉的），所以补一条一模一样的判断
						// ——两处漏一处，嵌套列表就会被当成续行接进上一项，
						// 页面上那一项多出几个字，而作者以为它是子列表。
						if listMarker(strings.TrimSpace(t)) != "" {
							return "", "", fmt.Errorf("第 %d 行是嵌套列表：本站只画一层（拆成两段，或者把它写成独立的一句话）", i+1)
						}
						if len(t)-len(strings.TrimLeft(t, " \t")) >= 4 || strings.HasPrefix(t, "\t") {
							return "", "", fmt.Errorf("第 %d 行缩进了：本站不认缩进代码块与嵌套列表，把它放进 ``` 围栏里", i+1)
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
					return "", "", fmt.Errorf("第 %d 行把有序与无序列表混在一起了：分开写成两段", i+1)
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
			return "", "", fmt.Errorf("第 %d 行以 < 开头：本站的正文里不写 HTML（它会被原样转义显示出来）", ln)

		case isSetext(trimmed, lines, i):
			return "", "", fmt.Errorf("第 %d 行那种下划线式标题（=== / ---）不认，写成 ## ", ln)

		default:
			para = append(para, trimmed)
		}
	}
	if err := flushPara(); err != nil {
		return "", "", err
	}
	closeFeat()
	if title == "" {
		return "", "", fmt.Errorf("这份文件没有一级标题（第一行该是 `# 标题`）")
	}
	return out.String(), title, nil
}

// eyebrow 认「眉题」这种二级标题：`## <small>01</small> 接管你的 CLI`。
//
// # 为什么为它开一个语法口子
//
// 首页那一排特性是**有编号的**（01…08），而编号该画成一个小眉题、不该和标题一样重。
// 不开口子的话只有两条路，两条都比它糟：把编号直接写进标题文字（`## 01 接管…`，
// 于是编号和正文一样黑一样大，看起来像标题的一部分而不是一个刻度），或者让正文
// 手写 `<span>`——而正文里的 HTML 是被**明文禁止**的（下面那条 < 开头的检查）。
//
// 它是一个**闭合**的小构造：只认行首那一个 `<small>…</small>`，后面的活照样走
// 行内渲染（`**加粗**`、`code` 都还有效）。写了一整个 HTML 小片段、或者把标签
// 写在中间，仍然落到「正文里不写 HTML」那条报错上——那样写的人八成在按 HTML
// 想事情，而这一页的正文是 markdown。
//
// 返回第三个值 = 这一行是不是眉题（不是的话后面两个值无意义）。
func eyebrow(text string) (tag, rest string, ok bool, err error) {
	if !strings.HasPrefix(text, "<small>") {
		if strings.HasPrefix(text, "<") {
			// 行首以 < 开头却不是 <small>：与其回去撞「不写 HTML」那条（报错的话
			// 是「正文里不写 HTML」，而作者明明写的是我们支持的眉题语法，只是写错
			// 了一点），不如在这里把唯一的那个写法说清楚。
			return "", "", false, fmt.Errorf("行首的 < 只有一种合法写法：`## <small>01</small> 标题`")
		}
		return "", "", false, nil
	}
	end := strings.Index(text, "</small>")
	if end < 0 {
		return "", "", false, fmt.Errorf("<small> 没有闭合（眉题的写法是 `## <small>01</small> 标题`）")
	}
	tag = text[len("<small>"):end]
	rest = strings.TrimSpace(text[end+len("</small>"):])
	if rest == "" {
		return "", "", false, fmt.Errorf("眉题后面没有标题（`## <small>01</small> 接管你的 CLI`）")
	}
	if strings.ContainsAny(tag, "<>&\"") {
		return "", "", false, fmt.Errorf("眉题里不写标记：%q", tag)
	}
	h, err := inline(rest)
	if err != nil {
		return "", "", false, err
	}
	return tag, h, true, nil
}

// isSetext 认「文字 + 下一行全是 = 或 -」这种老式标题。
//
// 单独认它是因为它**看起来像**我们认的东西（`---` 是水平线），静默的后果是一行
// 文字后面跟一条横线，而作者以为那是标题。
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

// listMarker 说这一行是不是列表项，是的话是哪种（"ul" / "ol"）。
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

// isContinuation 说这一行是不是上一段的**续行**（软换行），而不是一块新东西。
//
// 判据是「它会不会开启另一种块级构造」：表格、引用、围栏、标题、水平线、缩进。
// 都不是的话，它就是上一句没写完的那半句——中文正文里这是常态（一句话写满一行
// 就换行），而列表项里的 `**加粗**` 不该因为作者换了一次行就报「没配上对」。
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
		// 语言标记只用来上类名（渲染端不引高亮库：站点要能离线双击打开）。
		lang := fence
		if i := strings.IndexAny(lang, " \t"); i >= 0 {
			lang = lang[:i]
		}
		cls = ` class="lang-` + esc(lang) + `"`
	}
	return "<pre><code" + cls + ">" + esc(strings.Join(code, "\n")) + "</code></pre>"
}

// table 渲染一张表。rows 是**连续的、以 | 开头**的那几行。
//
// 表必须有分隔行（`| --- | --- |`）——那既是 GFM 的语法，也是唯一能让「本来想写
// 表格、但忘了分隔行」这件事被抓住的地方：不抓的话它会渲染成一个段落，里面一行
// 竖线，看着像坏了，而作者收到的是一个他看不懂的页面。
func table(rows []string, line int) (string, error) {
	if len(rows) < 2 {
		return "", fmt.Errorf("第 %d 行起是一张表，但它只有一行——表格至少要有表头 + 一行分隔（| --- |）", line)
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
			return "", fmt.Errorf("第 %d 行该是表格的分隔行（只有 - 与 :），实际是 %q", line+1, c)
		}
	}
	if len(sep) != len(head) {
		return "", fmt.Errorf("第 %d 行的分隔行有 %d 格，表头有 %d 格", line+1, len(sep), len(head))
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
			return "", fmt.Errorf("表格第 %d 行有 %d 格，表头有 %d 格", line+2, len(cs), len(head))
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

// joinLines 把一段里的多行接成一行。
//
// 中文与英文的接法不一样：英文行之间要一个空格（`the` + `gateway` 不能连成一个
// 词），而中文之间**不要**（多出来的空格在 CJK 排版里是看得见的毛刺）。判据就是
// 上一行的末字符与这一行的首字符是不是都在 CJK 区。
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

// isCJK 是「中日韩文字与全角标点」那一块。够用就好，不必穷举 Unicode。
func isCJK(r rune) bool {
	switch {
	case r >= 0x4E00 && r <= 0x9FFF: // 汉字
		return true
	case r >= 0x3000 && r <= 0x303F: // 中文标点
		return true
	case r >= 0xFF00 && r <= 0xFFEF: // 全角
		return true
	}
	return false
}

// inline 渲染行内构造。**认不出来的原样留着**（除了下面那几条会报错的）：
// 行内语法没有块级那么强的「看着像坏了」的后果。
func inline(s string) (string, error) {
	// 加粗的配对先查：`**` 落单时它会原样出现，而作者以为加了粗。
	if strings.Count(s, "**")%2 != 0 {
		return "", fmt.Errorf("行内的 ** 没配上对：%q", s)
	}
	var b strings.Builder
	for i := 0; i < len(s); {
		switch {
		case s[i] == '`':
			j := strings.IndexByte(s[i+1:], '`')
			if j < 0 {
				return "", fmt.Errorf("行内的反引号没闭合：%q", s)
			}
			b.WriteString("<code>" + esc(s[i+1:i+1+j]) + "</code>")
			i += j + 2

		case strings.HasPrefix(s[i:], "**"):
			j := strings.Index(s[i+2:], "**")
			if j < 0 {
				return "", fmt.Errorf("行内的 ** 没闭合：%q", s)
			}
			inner, err := inline(s[i+2 : i+2+j])
			if err != nil {
				return "", err
			}
			b.WriteString("<strong>" + inner + "</strong>")
			i += j + 4

		case strings.HasPrefix(s[i:], "!["):
			return "", fmt.Errorf("不认图片语法（本站不放截图，见 README 那份风格约定）：%q", s)

		case s[i] == '[':
			j := strings.Index(s[i:], "](")
			if j < 0 {
				return "", fmt.Errorf("行内的 [ 没配上 ](链接)：%q", s)
			}
			k := strings.IndexByte(s[i+j+2:], ')')
			if k < 0 {
				return "", fmt.Errorf("行内的链接没闭合：%q", s)
			}
			text, href := s[i+1:i+j], s[i+j+2:i+j+2+k]
			inner, err := inline(text)
			if err != nil {
				return "", err
			}
			b.WriteString(`<a href="` + esc(href) + `">` + inner + `</a>`)
			i += j + 2 + k + 1

		default:
			// **按 rune 走，不按 byte**：`string(s[i])` 里那个 s[i] 是 byte，
			// 而整数转字符串是按 **rune** 转的——中文的每一个首字节都会变成
			// 一个 Latin-1 字符（`为` → `ä¸º`）。这个错很凶：ASCII 部分
			// （命令、代码、英文）全都正常，只有中文烂掉，所以第一眼会以为
			// 是「编码问题」而去查文件的 charset，而文件一直是好的。
			r, size := utf8.DecodeRuneInString(s[i:])
			b.WriteString(esc(string(r)))
			i += size
		}
	}
	return b.String(), nil
}

// esc 转义 HTML 里那五个字符。**这里是唯一的出口**：所有会进正文的文字都过它，
// 所以「源文件里的 < 不会变成标签」这条只有一个地方要管。
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
