package webdashboard

import (
	"os"
	"regexp"
	"sort"
	"testing"
)

// 内核声明了哪些 Kind（`core/lib/view`）与前端能渲染哪些（`ConceptCard.svelte`
// 里那串 `concept.kind === "…"`）必须**对得上**。
//
// 为什么要有这条：`lib/view` 的 Kind 常量是**契约**（「前端只认这几种，加一个
// 模块的面不需要改前端」），可两半住在两个仓库里——内核声明，发行版的前端实现。
// 声明了一个却没人渲染时，症状是**静默的**：那个概念落到最后的兜底分支，界面上
// 出现一坨原始 JSON。2026-09-20 就是这么发现 `table` 的——它被声明了很久，既没有
// 贡献者也没有渲染器，而没有任何东西会因此变红。
//
// 反方向也查：前端有一个内核不认识的 Kind 时，那个分支是**死代码**（永远走不到），
// 而它读起来像「这个界面支持 X」。
//
// 边界：这是**文本层面**的检查（CI 必须离线，前端测试运行器进不来），它查的是
// 「有没有这个分支」，查不了「渲染对不对」——那要靠人打开看一眼。同一套取舍见
// web_test.go（产物进版本控制 + Go 侧看门）与 i18n_test.go（字典走文本比对）。

// kindConst 抓内核里的 Kind 常量：`KindTable = "table"`。
//
// 只认 `Kind` 开头且值是纯小写/连字符的：内核里还有别的 Kind 常量（比如
// `KindConnError`），但它们的值是大驼峰（`"conn_error"` 之类），是**别的词汇表**
// （事件/分类），不是渲染方式。用值去区分，比用名字列表去排除稳。
var kindConst = regexp.MustCompile(`(?m)^\s*Kind[A-Za-z]*\s*=\s*"([a-z][a-z-]*)"`)

// kindBranch 抓前端的渲染分支：`{:else if concept.kind === "code"}`。
var kindBranch = regexp.MustCompile(`concept\.kind === "([a-z][a-z-]*)"`)

func TestEveryDeclaredKindHasARenderer(t *testing.T) {
	declared := kindsIn(t, "../../core/lib/view/view.go", kindConst, "内核的 Kind 常量")
	rendered := kindsIn(t, "web/src/ConceptCard.svelte", kindBranch, "前端的渲染分支")

	if len(declared) == 0 {
		t.Fatal("一个 Kind 都没解析出来——正则与 lib/view 对不上了（改名或换了写法）")
	}
	if len(rendered) == 0 {
		t.Fatal("一个渲染分支都没解析出来——正则与 ConceptCard 对不上了")
	}

	var missing, dead []string
	for k := range declared {
		if !rendered[k] {
			missing = append(missing, k)
		}
	}
	for k := range rendered {
		if !declared[k] {
			dead = append(dead, k)
		}
	}
	sort.Strings(missing)
	sort.Strings(dead)

	for _, k := range missing {
		t.Errorf("内核声明了 Kind %q，前端没有渲染分支——那个概念会落到兜底分支，"+
			"界面上是一坨原始 JSON（2026-09-20 的 table 就是这样）", k)
	}
	for _, k := range dead {
		t.Errorf("前端有 Kind %q 的渲染分支，可内核没声明这个 Kind——"+
			"那个分支永远走不到，读起来却像「界面支持 X」", k)
	}
}

// kindsIn 从一个文件里把集合抓出来。what 用于报错时点名人话（路径写错时，
// 「读不到文件」比「测试挂了」有用得多）。
func kindsIn(t *testing.T, path string, re *regexp.Regexp, what string) map[string]bool {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读不到 %s（%s）：%v\n"+
			"    core/ 是 submodule，没 checkout 的话这条读不到——CI 用的是 submodules: recursive",
			path, what, err)
	}
	out := map[string]bool{}
	for _, m := range re.FindAllStringSubmatch(string(raw), -1) {
		out[m[1]] = true
	}
	return out
}
