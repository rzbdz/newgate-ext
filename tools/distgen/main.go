// Command distgen 在构建期把仓库根的规格书（dist*.json）编译成装配清单。
//
// 它是内核 tools/genmodules 的**发行版那一半**：内核的生成器扫自己的 modules/
// 得到「内核有哪些模块」（app.CoreModules()），本工具把规格书点名的那几个自己的
// 模块 + 要关掉的内核模块，编译成一张 map[规格书名]app.Selection。
//
// 为什么要有这一步（而不是让 main 手写一行行 app.Entry）
//
// 同内核那条理由：「装哪些模块」从目录结构里就能读出来，手抄一遍没有信息量，
// 却有三个地方要同步（加目录、加 import、加一行）。装一个模块 = 把目录复制进
// modules/、把名字写进规格书、重新编译。
//
// 判据与内核共用一把尺子（tools/genmodules/scan）——「哪些目录算组件」与
// 「目录名怎么拧成 import 别名」都只有一份实现，否则同一个目录名会在两个仓库里
// 得到两种解释。
//
// # 一份生成物装下全部规格书
//
// 每份规格书各得一条 app.Selection，**同时**生成在 manifest/modules_gen.go 里，
// 构建时用 -ldflags -X main.spec=<文件名> 选一份。这样 -check 一次就能校验全部
// 变体：一份坏掉的 dist-simple-cli.json 会让默认构建就红——而不是等到有人真的
// 编那个变体时才发现，那时离「谁改坏了哪一行」已经很远了。
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"go/format"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	app "github.com/rzbdz/newgate/app"
	modscan "github.com/rzbdz/newgate/tools/genmodules/scan"
)

const (
	// importPath 是本 module 的路径：生成的清单 import 自己的模块时用它。
	importPath = "github.com/rzbdz/newgate-ext"
	// coreImportPath 是内核 module 的路径（生成物 import app 与 component 用它）。
	coreImportPath = "github.com/rzbdz/newgate"
	// modulesRelDir 是自己的模块目录（相对模块根 = 仓库根）。
	modulesRelDir = "modules"
	// genPkgDir/genFileName 是生成物的位置。刻意**不叫** dist/：那个目录是
	// build/build.sh 的二进制输出口，两个东西同名会让人对着 gitignore 猜。
	genPkgDir   = "manifest"
	genFileName = "modules_gen.go"
	// specGlob 是规格书的文件名模式（dist.json / dist-simple-cli.json）。
	specGlob = "dist*.json"
	// defaultSpec 是没注入 -ldflags -X main.spec 时用的那一份。
	defaultSpec = "dist.json"
	// disableAllToken 是规格书里「内核自带的全都不要」的写法：`"disable": ["*"]`。
	//
	// 为什么要有它：列满目录名的写法带一份**会腐坏的副本**——内核往 CoreModules()
	// 里加一个模块时，那份名单不会跟着长，于是「我以为全关掉了」的发行版悄悄多装了
	// 一个模块，而且不报错。`*` 编成 app.Selection.AllCore，跟着内核的模块表一起长。
	//
	// 它只在**规格书**这一层（产品数据）：内核那边的字段叫 AllCore，是个布尔值，
	// 不需要认识任何字符串记号。
	disableAllToken = "*"
)

// spec 是规格书（Spec）：本发行版由哪些模块组成、关掉内核的哪几个。
//
// DisallowUnknownFields 是刻意的：写错一个键名（"module" / "disabled"）如果被
// 静默忽略，症状是「我明明配了，怎么没生效」——那要等到运行期少了个功能才暴露。
type spec struct {
	Distribution string   `json:"distribution"`
	Modules      []string `json:"modules"`
	Disable      []string `json:"disable"`
}

// module 是一个待装配的自有模块目录。
type module struct {
	dir   string // modules/ 下的目录名
	path  string // 相对模块根的 import 路径后缀
	alias string // 生成的 import 别名
}

func main() {
	check := flag.Bool("check", false,
		"只校验生成结果是否与规格书一致，不写文件（CI/测试用）")
	flag.Parse()

	// 摊平之后（2026-09-20）：模块根就是仓库根——Go 代码在仓库根，规格书也在。
	root, err := moduleRoot()
	repoRoot := root
	if err != nil {
		fail(err)
	}

	specs, names, err := loadSpecs(repoRoot)
	if err != nil {
		fail(err)
	}
	mods, err := scan(filepath.Join(root, modulesRelDir))
	if err != nil {
		fail(err)
	}
	if err := validate(specs, mods); err != nil {
		fail(err)
	}

	content, err := format.Source(render(mods, specs, names))
	if err != nil {
		fail(fmt.Errorf("生成的源码不合法（这是生成器的 bug）: %w", err))
	}
	target := filepath.Join(root, genPkgDir, genFileName)

	if *check {
		current, err := os.ReadFile(target)
		if err != nil || !bytes.Equal(current, content) {
			fail(fmt.Errorf("%s 已过期：运行 `go run ./tools/distgen` 后重新提交", target))
		}
		return
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		fail(err)
	}
	if err := os.WriteFile(target, content, 0o644); err != nil {
		fail(err)
	}
	fmt.Printf("distgen: %d 份规格书 × %d 个自有模块 → %s\n", len(names), len(mods), target)
}

// moduleRoot 从当前工作目录向上找到含 go.mod 的目录（`go run ./tools/distgen`
// 在 module 根跑，测试可能在别处跑，靠 go.mod 定位而不是相对路径）。
func moduleRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("向上找不到 go.mod（从 %s 开始）", dir)
		}
		dir = parent
	}
}

// loadSpecs 读仓库根的全部规格书，按文件名排序返回。
//
// 排序是为了生成物稳定：map 的遍历序是随机的，直接渲染会让每次生成的 diff 都飘。
func loadSpecs(repoRoot string) (map[string]spec, []string, error) {
	paths, err := filepath.Glob(filepath.Join(repoRoot, specGlob))
	if err != nil {
		return nil, nil, err
	}
	if len(paths) == 0 {
		return nil, nil, fmt.Errorf("%s 下没有规格书（%s）——发行版至少要有 `distribution` 与 `modules`", repoRoot, specGlob)
	}
	out := make(map[string]spec, len(paths))
	names := make([]string, 0, len(paths))
	for _, p := range paths {
		raw, err := os.ReadFile(p)
		if err != nil {
			return nil, nil, err
		}
		var s spec
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&s); err != nil {
			return nil, nil, fmt.Errorf("%s 解不出来: %w", filepath.Base(p), err)
		}
		name := filepath.Base(p)
		if s.Distribution == "" {
			return nil, nil, fmt.Errorf("%s 没有 distribution 字段", name)
		}
		if len(s.Modules) == 0 {
			return nil, nil, fmt.Errorf("%s 的 modules 是空的——一个模块都不装的发行版没有意义", name)
		}
		out[name] = s
		names = append(names, name)
	}
	sort.Strings(names)
	return out, names, nil
}

// scan 列出 modules/ 下所有「是组件」的目录，按字母序返回。
func scan(componentsDir string) ([]module, error) {
	dirs, err := modscan.Dirs(componentsDir)
	if err != nil {
		return nil, err
	}
	out := make([]module, 0, len(dirs))
	for _, dir := range dirs {
		rel := path.Join(modulesRelDir, dir)
		out = append(out, module{dir: dir, path: rel, alias: modscan.Ident("ext_", dir)})
	}
	return out, nil
}

// validate 把两份**互相不知道对方存在**的名单对起来，全部响亮报错。
//
// 这三条错都会伪装成别的问题，所以宁可在这里红：
//
//   - modules 点名了一个 modules/ 下不存在的目录 ⇒ 拼错了，或者忘了把目录
//     复制进来。症状本来是「编译期少一个包」，而那看起来像生成器的 bug。
//   - modules 里有目录没被任何规格书点名 ⇒ **不报错**（那是正常的：simple-cli
//     就是只给 dist-simple-cli.json 用的），但要让它可见。
//   - disable 点名了内核没有的模块 ⇒ 最坏的一种：用户以为关掉了，它其实在跑
//     （内核侧的 Selection.Load 也会再拦一道，这里是构建期更早的一道）。
func validate(specs map[string]spec, mods []module) error {
	own := make(map[string]bool, len(mods))
	for _, m := range mods {
		own[m.dir] = true
	}
	core := make(map[string]bool)
	for _, e := range app.CoreModules() {
		core[e.Dir] = true
	}

	named := map[string]bool{}
	for _, name := range sortedKeys(specs) {
		s := specs[name]
		for _, want := range s.Modules {
			if !own[want] {
				return fmt.Errorf("%s 点名了 %q，但 modules/%s 不是一个组件（缺 module.go 或没导出 New()）",
					name, want, want)
			}
			named[want] = true
		}
		for i, off := range s.Disable {
			if off == disableAllToken {
				if len(s.Disable) > 1 {
					return fmt.Errorf("%s 的 disable 里同时写了 %q 和具体名字（%v）——"+
						"%q 已经包含它们了。要全关就只留 %q；只想关几个就别写它",
						name, disableAllToken, s.Disable[i+1:], disableAllToken, disableAllToken)
				}
				continue
			}
			if !core[off] {
				return fmt.Errorf("%s 的 disable 点名了 %q，但内核没有这个模块"+
					"（写的是**目录名**吗？比如 claudecode_deepseek 而不是 claudecode-deepseek。"+
					"要关掉内核自带的全部，写 %q）", name, off, disableAllToken)
			}
		}
	}
	for _, m := range mods {
		if !named[m.dir] {
			fmt.Printf("distgen: 注意：modules/%s 没有被任何规格书点名（不装它）\n", m.dir)
		}
	}
	return nil
}

func sortedKeys(m map[string]spec) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// render 生成装配清单源码。生成物必须**自己**就是 gofmt 干净的（同内核那条理由：
// check-fmt 独立跑，没对齐的 import 块报的是「格式不对」，离真因隔着好几步）。
func render(mods []module, specs map[string]spec, names []string) []byte {
	byDir := make(map[string]module, len(mods))
	for _, m := range mods {
		byDir[m.dir] = m
	}

	var b bytes.Buffer
	b.WriteString("// Code generated by tools/distgen. DO NOT EDIT.\n")
	b.WriteString("//\n")
	b.WriteString("// 由仓库根的规格书（dist*.json）编译而来：每条 app.Selection 就是一份规格书\n")
	b.WriteString("// 要装的东西——自己的模块 + 要关掉的内核模块。构建时用\n")
	b.WriteString("// -ldflags -X main.spec=<文件名> 选一份，DefaultSpec 是没注入时的兜底。\n")
	b.WriteString("//\n")
	b.WriteString("// 规格书里的 `\"disable\": [\"*\"]` 编成 app.Selection.AllCore：内核自带的全都不要，\n")
	b.WriteString("// 只留关不掉的那些（组合根自己要用的端口）。\n")
	b.WriteString("\npackage " + genPkgDir + "\n\n")
	b.WriteString("import (\n")
	b.WriteString("\t\"fmt\"\n")
	b.WriteString("\t\"strings\"\n\n")
	b.WriteString("\tapp \"" + coreImportPath + "/app\"\n")
	b.WriteString("\tmodules \"" + coreImportPath + "/component\"\n")
	for _, m := range mods {
		fmt.Fprintf(&b, "\t%s \"%s/%s\"\n", m.alias, importPath, m.path)
	}
	b.WriteString(")\n\n")

	b.WriteString("// DefaultSpec 是没注入 -X main.spec 时用的规格书。\n")
	b.WriteString("//\n")
	b.WriteString("// 它让 `go run ./cmd/newgate` 与 `go test ./...` 不用先编一遍就能跑——\n")
	b.WriteString("// 默认那份就是仓库根的主规格书，与正式产物一致。\n")
	fmt.Fprintf(&b, "const DefaultSpec = %q\n\n", defaultSpec)

	b.WriteString("// Specs 是仓库根每份规格书各自的装配选择。\n")
	b.WriteString("func Specs() map[string]app.Selection {\n")
	b.WriteString("\treturn map[string]app.Selection{\n")
	for _, name := range names {
		s := specs[name]
		fmt.Fprintf(&b, "\t\t%q: {\n", name)
		if len(s.Disable) == 1 && s.Disable[0] == disableAllToken {
			// 一句话，而不是十几个目录名：AllCore 跟着内核的模块表一起长。
			b.WriteString("\t\t\tAllCore: true,\n")
		} else if len(s.Disable) > 0 {
			fmt.Fprintf(&b, "\t\t\tDisable: []string{%s},\n", quoteList(s.Disable))
		}
		b.WriteString("\t\t\tExtra: []app.Entry{\n")
		for _, want := range s.Modules {
			m := byDir[want]
			fmt.Fprintf(&b, "\t\t\t\t{Dir: %q, Component: %s.New()},\n", m.dir, m.alias)
		}
		b.WriteString("\t\t\t},\n")
		b.WriteString("\t\t},\n")
	}
	b.WriteString("\t}\n}\n\n")

	b.WriteString("// SpecNames 列出全部规格书名，按文件名排序。\n")
	fmt.Fprintf(&b, "func SpecNames() []string { return []string{%s} }\n\n", quoteList(names))

	b.WriteString("// Loader 按规格书名装图。名字认不出来就**报错**，不退回默认那份：\n")
	b.WriteString("// 「我明明编的是简单版，怎么界面上有两个 ui」这种问题，值得在启动的第一秒\n")
	b.WriteString("// 就说清楚，而不是让用户自己去比对二进制。\n")
	b.WriteString("type Loader struct{ Spec string }\n\n")
	b.WriteString("func (l Loader) Load() ([]modules.Component, error) {\n")
	b.WriteString("\tname := l.Spec\n")
	b.WriteString("\tif name == \"\" {\n")
	b.WriteString("\t\tname = DefaultSpec\n")
	b.WriteString("\t}\n")
	b.WriteString("\tsel, ok := Specs()[name]\n")
	b.WriteString("\tif !ok {\n")
	b.WriteString("\t\treturn nil, fmt.Errorf(\"没有叫 %q 的规格书（可用：%s）\", name, strings.Join(SpecNames(), \", \"))\n")
	b.WriteString("\t}\n")
	b.WriteString("\treturn sel.Load()\n")
	b.WriteString("}\n")
	return b.Bytes()
}

// quoteList 把一串名字渲染成 Go 字面量列表（`"a", "b"`）。
func quoteList(xs []string) string {
	parts := make([]string, 0, len(xs))
	for _, x := range xs {
		parts = append(parts, fmt.Sprintf("%q", x))
	}
	return strings.Join(parts, ", ")
}

// fail 是生成器的出口：一句话说清哪里错，退出码 1。
func fail(err error) {
	fmt.Fprintln(os.Stderr, "distgen:", err)
	os.Exit(1)
}
