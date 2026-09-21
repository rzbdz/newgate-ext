package main

import (
	"fmt"
	"html/template"
	"strings"
)

// themeMeta 是页头那两行（`<meta name="theme-color">`）。
//
// # 为什么是两行
//
// `theme-color` 的 content **不接受** `(prefers-color-scheme)`——那两行是同一个
// 属性的两次声明，浏览器按自己当前的主题取后一条匹配上的（media 属性今天只有
// Chromium 系认，其余浏览器取第一条，也就是暗色那条，正好是默认主题）。
//
// # 顺序（media 之前那一条）
//
// 先发**暗色**那条、再发亮色那条。载入时 URL 栏先被染成第一条那个颜色，随后才轮到
// 带 media 的那条生效——反过来写的话，亮色主题的用户会看到地址栏先闪一下亮色、
// 再变暗（或反之）。默认那条排前面是把那一闪压到最短的排法。
//
// 两行都没有时返回空串：模板里那一格就成了空行，没有副作用（与 socialMeta 同一条）。
func themeMeta(th theme) template.HTML {
	if th.Dark == "" || th.Light == "" {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "<meta name=\"theme-color\" content=%q>\n", th.Dark)
	fmt.Fprintf(&b, "<meta name=\"theme-color\" content=%q media=\"(prefers-color-scheme: light)\">\n", th.Light)
	return template.HTML(b.String())
}

// theme 是浏览器**自己那块界面**（手机上的地址栏、系统的状态栏）该染成什么色。
//
// 它只有一个来源：站点样式里那两条 `--bg`。
type theme struct {
	Dark  string
	Light string
}

// themeColors 从站点样式里读出两种主题下的底色。
//
// # 为什么是**读**样式，而不是在 site.json 里再写一遍
//
// `theme-color` 的全部作用就是「让浏览器自己那块界面与页面底色看起来是同一块」。
// 所以它必须**等于** `--bg`。在配置里再写一遍的话，改版换底色那天它会悄悄留在旧
// 值上，而症状是**只有手机上**能看见的一条颜色不对的地址栏——桌面浏览器根本不读
// 它，所以在开发机上永远看不出来。
//
// # 读不出来就报错，不猜
//
// 找不到、只有一条、多于两条，三种都报错。一个猜出来的颜色比没有 theme-color
// 更糟（它看起来是对的）。这与 md.go 对不认识的 markdown 构造的态度是同一条：
// 静默地画错是唯一不能接受的结果。
func themeColors(css string) (theme, error) {
	var th theme
	vals, err := cssVar(stripComments(css), "--bg")
	if err != nil {
		return th, err
	}
	switch len(vals) {
	case 2:
		// 顺序即含义：第一条是默认（暗色），第二条在
		// `@media (prefers-color-scheme: light)` 里（亮色）。这与 site.css 的
		// 结构对应——那里就是这么写的，而这条判据逼着它保持那个形状。
	default:
		return th, fmt.Errorf("site.css 里有 %d 条 `--bg`——只该有两条（默认那条是暗色，"+
			"`@media (prefers-color-scheme: light)` 里那条是亮色）。theme-color 取不出"+
			"唯一的值就会失准，所以这里不猜", len(vals))
	}
	for i, v := range vals {
		if !strings.HasPrefix(v, "#") {
			// theme-color 收任何 CSS 颜色，但**手机上的地址栏**只认得出纯色。
			// 值不是 `#` 开头（渐变、var(...)、关键字）时说出来，而不是发一个
			// 浏览器会忽略掉的字符串。
			return th, fmt.Errorf("site.css 里第 %d 条 `--bg` 的值是 %q——theme-color 要"+
				"一个 `#` 开头的纯色（地址栏染不了渐变）", i+1, v)
		}
	}
	th.Dark, th.Light = vals[0], vals[1]
	return th, nil
}

// cssVar 取出一份 CSS 里某个自定义属性的全部值（按出现次序）。
//
// 它**不是** CSS 解析器，只认「`--名字` 到下一个 `;`」这一种形状。够用，而且认错
// 的代价是**零条**——`themeColors` 立刻报错，不会静默取到一个别的属性。名字比对的
// 是 `--bg` 后面紧跟的那个字符，所以 `--bg-soft` 不会被误认为 `--bg`。
func cssVar(css, name string) ([]string, error) {
	var out []string
	for {
		i := strings.Index(css, name)
		if i < 0 {
			return out, nil
		}
		rest := css[i+len(name):]
		if rest == "" || (rest[0] != ':' && rest[0] != ' ') {
			// `--bg-soft:` / `--bgfoo:` 这类名字的一部分，不是它。
			css = rest
			continue
		}
		colon := strings.IndexByte(rest, ':')
		if colon < 0 {
			return nil, fmt.Errorf("`%s` 后面没有冒号", name)
		}
		// 名字与冒号之间只允许空白（`--bg : red` 也是合法的 CSS）。
		if strings.TrimSpace(rest[:colon]) != "" {
			css = rest
			continue
		}
		rest = rest[colon+1:]
		semi := strings.IndexByte(rest, ';')
		if semi < 0 {
			// 没闭合的声明：**当它不存在**而不是报错——一段被截断的 CSS 是编辑中途
			// 的样子，而这里要判的是「有几条 --bg」，不是「这份 CSS 合不合法」。
			return out, nil
		}
		if v := strings.TrimSpace(rest[:semi]); v != "" {
			out = append(out, v)
		}
		css = rest[semi+1:]
		if css == "" {
			return out, nil
		}
	}
}

// stripComments 抹掉 CSS 注释。
//
// 不做这一步的话，注释里出现的 `--bg` 会被 cssVar 当成一条声明——而这份样式的
// 注释写得很多（每一条判据都带一段「为什么」），里面提一句 `--bg` 是很正常的事。
func stripComments(css string) string {
	var b strings.Builder
	for {
		i := strings.Index(css, "/*")
		if i < 0 {
			b.WriteString(css)
			return b.String()
		}
		b.WriteString(css[:i])
		j := strings.Index(css[i:], "*/")
		if j < 0 {
			// 没闭合的注释：剩下的都当注释（浏览器也是这么读的）。
			return b.String()
		}
		css = css[i+j+2:]
	}
}
