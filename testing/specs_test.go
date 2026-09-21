package disttesting

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"testing"
)

// siteDefaultLang 是公开站里**排在根路径上**的那一种语言（详见 tools/sitegen 的
// defaultLang）。写在这里而不是 import 过来：那一位住在 `package main` 里，
// 而这条测试要读的是**盘上那个目录名**——两处都写一次，改语言时这里会读不到文件
// 而报错（那正是想要的：静默地跳过检查才是坏的）。
const siteDefaultLang = "zh-Hans"

// 这一条锁的是**规格书之间**的一处关系：dev 那一份必须恰好是旗舰那一份**加上**
// arch-diagram。
//
// # 为什么值得一条测试（它拦过一次真实的漂移）
//
// CLAUDE.md 把 dev 定义成「默认模块 + arch-diagram」，而这不是一个描述、是一条
// **要求**：dev 存在的全部意义是「让我看见**产品那份装配**的内部结构」。它把
// `/arch` 挂出去，而那张图画的是**这个进程此刻装了什么**——dev 少装一个模块，
// 这张图就不再是产品那张图了，而它恰恰是 review 架构时唯一在看的东西。
//
// 漂移是怎么发生的（2026-09-20 → 09-21 实测）：`a7b7fde` 建 `dist-dev.json` 时
// 抄的是**当时**的默认模块表 + arch-diagram；第二天 codex / codex_deepseek 加进
// `dist.json`（`20aa52e` / `969ae33`），没有一条东西提醒「还有第二份规格书在抄
// 这张表」。于是 dev 静默地少了两块，而两件事都没人报：dev 照样编得过、CI 照样绿、
// 图上只是**少了两个模块**——少掉的东西在一张架构图上看起来和「这两个模块不存在」
// 一模一样。
//
// 这正是「列满的名单是那份表的副本」那条判据的第二个实例（第一份是内核模块表，
// 见 CLAUDE.md §3 关于 `"disable": ["*"]` 的那段）。区别是这一份**没有别的写法**
// ——dev 与 default 的差集就是它的定义，所以副本只能存在，那就用一条测试把它钉住。
//
// 判据按**集合**比，不按数组：规格书里写的次序对装配没有影响（启动顺序由
// `tools/distgen` 跑真的 Resolve 算出来，见 manifest/order_gen.go），比次序只会
// 让「有人调整了抄写顺序」这种无害的改动报红。
func TestDevSpecIsTheDefaultPlusTheMap(t *testing.T) {
	root := repoRoot(t)
	def := readSpecModules(t, filepath.Join(root, "dist.json"))
	dev := readSpecModules(t, filepath.Join(root, "dist-dev.json"))

	// 期望：default 的每一个模块都在 dev 里，外加 arch-diagram；反向也成立——
	// 多出来的是**这个差集本身**的一部分，多一个少一个都要说。
	want := map[string]bool{archDiagram: true}
	for _, m := range def {
		want[m] = true
	}

	have := map[string]bool{}
	for _, m := range dev {
		have[m] = true
	}

	var missing, extra []string
	for m := range want {
		if !have[m] {
			missing = append(missing, m)
		}
	}
	for m := range have {
		if !want[m] {
			extra = append(extra, m)
		}
	}
	sort.Strings(missing)
	sort.Strings(extra)

	if len(missing) > 0 {
		t.Errorf("dist-dev.json 少了 %v——dev 是「默认模块 + %s」，少装的模块在 /arch 那张图上"+
			"看起来与「它不存在」一模一样（见这条测试的注释）。加进 dist-dev.json 再 "+
			"`go run ./tools/distgen`", missing, archDiagram)
	}
	if len(extra) > 0 {
		t.Errorf("dist-dev.json 多了 %v——它只该比默认那份多一个 %s；多出来的那些会让 /arch "+
			"画出一个**产品里并不存在**的装配，而那比少画更糟（它看起来是对的）",
			extra, archDiagram)
	}
}

// siteModuleCountRe 抓站点上那句「N 个模块」。
//
// 用正则而不是整句比对：这句是**给人读的话**，措辞随时可以改（改句子不该让一条
// 判据变红）；这条判据管的是**里面那个数**。
var siteModuleCountRe = regexp.MustCompile(`(\d+) 个模块`)

// TestTheSiteStatesTheDefaultSpecsModuleCount：公开站上那句「N 个模块」必须等于
// `dist.json` 装了几个。
//
// # 为什么会漂（实测漂过一次）
//
// `d5555a4` 写这一页时 `dist.json` 是 11 个，第二天 `969ae33` 把 codex_deepseek
// 加进默认规格，那句话就变成了**假的**——而站点照样生成、CI 照样绿。它与
// TestDevSpecIsTheDefaultPlusTheMap 是**同一类**漂移：一句从某份规格书抄来的事实，
// 抄的时候是对的，源变了没人提醒。
//
// # 为什么不去掉那个数
//
// 它是这一页的开场：「这个站讲的是哪一份装配」需要一个具体的量。含糊过去
// （「装了一整排模块」）等于把那句话的用处删掉——**补上那个数**比删掉它好，
// 前提是它不会再烂掉。
//
// 站点是 markdown、数是代码，两者之间没有构建期的接缝（站点产物不进版本控制
// ——见 tools/sitegen/main.go 的文件头），所以钉住它的唯一地方就是这里。
func TestTheSiteStatesTheDefaultSpecsModuleCount(t *testing.T) {
	root := repoRoot(t)
	mods := readSpecModules(t, filepath.Join(root, "dist.json"))

	// 这一页是「文档」的落点页：它明说自己在讲 dist.json 那一份，所以数得对得上。
	// 别的页不写这个数，真写了也不归这条管——那是另一个决定（要不要到处都断言）。
	path := filepath.Join(root, "site", "src", siteDefaultLang, "docs", "index.md")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读不到站点那一页（%s）: %v", path, err)
	}
	m := siteModuleCountRe.FindStringSubmatch(string(b))
	if m == nil {
		t.Fatalf("%s 里找不到「N 个模块」那句——"+
			"这一页的开场就是在说「本站讲的是哪一份装配」，那句没了这条判据就空转了", path)
	}
	if m[1] != strconv.Itoa(len(mods)) {
		t.Errorf("站点上写着 %s 个模块，而 dist.json 装了 %d 个——"+
			"这是一句从规格书抄来的事实，改规格书的人不会记得去改文档（实测漂过一次："+
			"11 → 12）。改 %s", m[1], len(mods), path)
	}
}

// archDiagram 是 dev 相对 default **唯一**允许多出来的那一个。
//
// 写在这里而不是从某处读：它是这条判据的**全部内容**。「dev 那份是不是该装
// arch-diagram」不是一个可以推导的问题——那是这个发行版的产品决定（见
// CLAUDE.md §5），所以它只能被写下来一次，然后由测试守着。
const archDiagram = "arch-diagram"

// readSpecModules 读一份规格书的 modules 列表。
//
// **从盘上读，不从 manifest 读**：manifest/modules_gen.go 是这两份规格书的
// *产物*，拿产物去校验源，两份一起错的时候（改了规格书忘了重新生成）它俩是一致
// 的，这条测试就空转了。盘上那份 JSON 才是源。
func readSpecModules(t *testing.T, path string) []string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读不到 %s: %v", filepath.Base(path), err)
	}
	var spec struct {
		Modules []string `json:"modules"`
	}
	if err := json.Unmarshal(b, &spec); err != nil {
		t.Fatalf("%s 不是合法 JSON: %v", filepath.Base(path), err)
	}
	if len(spec.Modules) == 0 {
		t.Fatalf("%s 的 modules 是空的——那份规格书什么都没装，判据会空转成绿的",
			filepath.Base(path))
	}
	return spec.Modules
}

// repoRoot 从测试的包目录往上找仓库根（同时有 go.mod 与 dist.json 的那一层）。
//
// 与 tools/sitegen 的 repoRoot 同一条判据：只认 go.mod 的话 `core/` 也是一个
// module，会认到内核那一层去。
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := filepath.Abs("..")
	if err != nil {
		t.Fatalf("算仓库根: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			if _, err := os.Stat(filepath.Join(dir, "dist.json")); err == nil {
				return dir
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("找不到仓库根（该同时有 go.mod 与 dist.json）")
		}
		dir = parent
	}
}
