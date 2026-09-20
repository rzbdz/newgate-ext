package webdashboard

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// 版面那一层的几条棘轮。都是**文本层面**的检查（CI 必须离线，前端的测试运行器
// 进不来），与 kinds_test.go / web_test.go 同一套取舍：查得了「有没有这回事」，
// 查不了「画出来好不好看」——后者只能靠人打开看一眼。

// frontendSources 读 web/src 下的全部前端源码（含子目录，kinds/ 也在内）。
//
// 只看 src/，不看 dist/：产物是压缩过的字节，正则在那里既读不出结构也容易误报。
func frontendSources(t *testing.T) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir("web/src", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		switch filepath.Ext(p) {
		case ".svelte", ".ts":
		default:
			return nil
		}
		b, rerr := os.ReadFile(p)
		if rerr != nil {
			return rerr
		}
		out[p] = string(b)
		return nil
	})
	if err != nil {
		t.Fatalf("读不到前端源码: %v", err)
	}
	if len(out) == 0 {
		t.Fatal("web/src 下一个 .svelte/.ts 都没读到——路径变了？这条断言等于没跑")
	}
	return out
}

// moduleLiterals 是各模块的**来源名**（登记 view 时用的那个机器标记）。
//
// 它们出现在前端就意味着界面开始认识模块了。「前端不认识模块」不是洁癖：那正是
// 「加一个模块的界面不用改前端」这句承诺的全部内容，而破坏它不需要谁下决心——
// 一句 `if (c.source === "config")` 就够了，而且当场就能跑通、没有任何东西会红。
var moduleLiterals = []string{
	"config", "gateway", "breaker", "plugin-manager", "runtime", "claudecode", "opencode-omo",
}

func TestFrontendDoesNotKnowModuleNames(t *testing.T) {
	// 判据是**代码里的字符串字面量**，不是文本里出现过这个词：注释里举一个 ID
	// 当例子（`config.file.mappings/demo.json`）是在解释格式，不是在认模块——
	// 而注释恰好是这个仓库写得最多的东西，把它一起算进来，这条棘轮会逼着人
	// 把那句话删掉。所以先剥注释（只剥注释，字符串原样留着）。
	var patterns []*regexp.Regexp
	for _, m := range moduleLiterals {
		patterns = append(patterns,
			regexp.MustCompile(`["'\x60]`+regexp.QuoteMeta(m)+`["'\x60]`))
	}
	for path, src := range frontendSources(t) {
		code := stripComments(src)
		for i, re := range patterns {
			if loc := re.FindStringIndex(code); loc != nil {
				t.Errorf("%s: 出现了模块名 %q —— 前端不该认识任何模块。"+
					"栏目名由各模块在登记时报（后端 view.Sections），界面把它画出来就行；"+
					"要按来源过滤，用拿到的 source 字符串比较，不要写死一个名字",
					path, moduleLiterals[i])
			}
		}
	}
}

// stripComments 去掉 `//` 与 `/* */` 注释，**字符串与模板串原样留着**。
//
// 为什么要自己写这三十行：正则剥注释会被字符串里的 `//`（一个 URL、一句文案里
// 的斜杠）骗到，把后半行当成注释切掉——那时候漏报的是真代码。状态机认得引号，
// 就不会。
func stripComments(s string) string {
	var b strings.Builder
	runes := []rune(s)
	for i := 0; i < len(runes); i++ {
		c := runes[i]
		switch {
		case c == '/' && i+1 < len(runes) && runes[i+1] == '/':
			for i < len(runes) && runes[i] != '\n' {
				i++
			}
			if i < len(runes) {
				b.WriteRune('\n')
			}
		case c == '/' && i+1 < len(runes) && runes[i+1] == '*':
			i += 2
			for i+1 < len(runes) && !(runes[i] == '*' && runes[i+1] == '/') {
				i++
			}
			i++ // 停在 '/' 上，循环的 i++ 跳过它
		case c == '"' || c == '\'' || c == '`':
			b.WriteRune(c)
			for i++; i < len(runes); i++ {
				b.WriteRune(runes[i])
				if runes[i] == '\\' && c != '`' && i+1 < len(runes) {
					i++
					b.WriteRune(runes[i])
					continue
				}
				if runes[i] == c {
					break
				}
			}
		default:
			b.WriteRune(c)
		}
	}
	return b.String()
}

// TestSectionTitlesComeFromTheWire：侧栏的栏目名必须**从后端来**。
//
// 这条链路断掉时不会报错——api.ts 的 `doc.sections ??= []` 会把缺的字段兜成空
// 数组，前端随即回落到来源名，于是侧栏里是 config / plugin-manager 这种机器标记。
// 看着只像「还没翻」，不像一条 bug。所以要一条断言盯着「这个字段真的有被读」，
// 而不是等谁在浏览器里看出来。
func TestSectionTitlesComeFromTheWire(t *testing.T) {
	src := frontendSources(t)

	api, ok := src["web/src/api.ts"]
	if !ok {
		t.Fatal("web/src/api.ts 不见了")
	}
	for _, want := range []string{"sections", "Section"} {
		if !strings.Contains(api, want) {
			t.Errorf("api.ts 里没有 %q——快照的栏目表得有个类型", want)
		}
	}

	// App 必须真的把 doc.sections 存下来并交给侧栏：只声明类型不读它，
	// 界面照样画来源名，而上面那条会通过。
	app, ok := src["web/src/App.svelte"]
	if !ok {
		t.Fatal("web/src/App.svelte 不见了")
	}
	if !strings.Contains(app, "doc.sections") {
		t.Error("App.svelte 没有读快照里的 sections——侧栏就只能拿来源名当标题")
	}
	if !strings.Contains(app, "Sidebar") {
		t.Error("App.svelte 没有把栏目表交给侧栏")
	}
}

// TestRoutingIsHashBased 钉住「位置存在 hash 里」这条决定。
//
// 为什么值得钉：改成路径路由**能跑通**（BFF 的 SPA 兜底认无扩展名的 GET），所以
// 将来有人顺手改掉它不会有任何东西变红——而代价要等到某个相对地址又解析错位置
// 才出现（见 nav.ts 顶部那段：/ui 那个 bug 就是这一类）。这条断言就是那时候的
// 一句提醒：换路由方式要想清楚运行期地址跟着文档路径走这件事。
func TestRoutingIsHashBased(t *testing.T) {
	src := frontendSources(t)
	nav, ok := src["web/src/nav.ts"]
	if !ok {
		t.Fatal("web/src/nav.ts 不见了（路由的解析与生成住在那儿）")
	}
	if !strings.Contains(nav, "parseHash") {
		t.Error("nav.ts 里没有 parseHash")
	}
	for path, s := range src {
		if strings.Contains(s, "history.pushState") {
			t.Errorf("%s: 用了 history.pushState——位置改用路径的话，"+
				"运行期地址就会跟着文档路径走（那正是 /ui 那个 bug 的形状）", path)
		}
	}
}

// TestNoFlatWaterfall：一条瀑布不许回来。
//
// 原来所有卡片按来源分组、一路往下铺（实测 4303px ≈ 4.8 屏），「改一个开关」的
// 第一步是滚三屏。这一条查的就是那个形状本身：`{#each sources as …}` 加 `<h2>`。
// 它是这整次改版最短的一句话。
func TestNoFlatWaterfall(t *testing.T) {
	app := frontendSources(t)["web/src/App.svelte"]
	if strings.Contains(app, "srch") {
		t.Error("App.svelte 里还有那个分组标题（.srch）——瀑布流回来了？")
	}
	if regexp.MustCompile(`#each\s+sources\s+as`).MatchString(app) {
		t.Error("App.svelte 还在整体遍历 sources 铺卡片——那是一条瀑布，不是有导航的工作台")
	}
	if !strings.Contains(app, "TabStrip") || !strings.Contains(app, "Sidebar") {
		t.Error("App.svelte 里没有侧栏或者 tab 条——版面又回到平铺了")
	}
}
