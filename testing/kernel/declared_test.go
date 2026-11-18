package kernel

import (
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// 这条棘轮守的是 app/matrix_test.go 文件头说的**第二类坏法**：
//
//	别的模块**在 Start 里伸手去拿**一个自己没声明的端口 → 摘掉提供者，构图期
//	不会失败（没人声明依赖），运行时才 panic。这是最坏的一种：构建通过、测试
//	通过、装出来的二进制在某个命令上炸。
//
// 摘除矩阵只能**间接**碰它：它一次摘一个模块，而只要还有**别人**声明了那个端口，
// 构图期就会先因为别人失败——伸手那一位的 panic 永远轮不到。2026-09-21 就是这样
// 挖出两个真 bug 的：
//
//	gateway  MustGet(config-hooks)  ——  一条边都没声明
//	runtime  MustGet(config-hooks)  ——  声明了同一个模块的**另一个** capability
//
// 第二条尤其说明为什么不能只看「两个模块之间有没有边」：那条边在（runtime →
// confighook，走的是 AgentCatalogCapability），所以组件粒度的检查看不见它。
// 真正的问题是**按 capability 算的**：你拿的这一个，声明了没有。
//
// 所以这条测试逐个模块地问：你的 `Start` 里 `MustGet` 的每一个 capability，
// 在你自己的 `Requires` 里吗？
//
// # 为什么用 AST 而不是正则
//
// 要问的是「这两个表达式指的是不是同一个 capability」。名字不够——每一家的
// `api.go` 都导出一个叫 `Capability` 的常量，光比 `Capability` 全都相等。所以
// 得把**包**也带上：同一个文件里，`confighookapi` 这个别名指向哪条 import 路径，
// 是文件自己写着的。于是键 = `import 路径 + 常量名`，两边各按各自文件的别名算，
// 别名不同也对得上。
func TestEveryMustGetIsDeclared(t *testing.T) {
	dirs, err := filepath.Glob(filepath.Join(modulesDir(t), "*"))
	if err != nil {
		t.Fatal(err)
	}
	checked := 0
	for _, dir := range dirs {
		if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
			continue
		}
		moduleGo := filepath.Join(dir, "module.go")
		if _, err := os.Stat(moduleGo); err != nil {
			continue // 不是模块（没有 module.go），不归这条管
		}

		declared := declaredCapabilities(t, dir)
		if len(declared) == 0 {
			// 一个 capability 都不依赖的模块（今天只有 entry 那类）：没有可比的
			// 东西，但也别静默——下面那句计数会把它算进去。
			checked++
			continue
		}
		for _, m := range mustGets(t, dir) {
			if m.unresolved != "" {
				t.Errorf("%s: 读不懂这一句 MustGet 的参数（%s）——它可能是把 capability "+
					"存进了变量。这条棘轮查不了它，请改成直接写常量。", m.pos, m.unresolved)
				continue
			}
			if !declared[m.key] {
				t.Errorf("%s: MustGet(%s) 拿的这个 capability **没有在 Requires 里声明**。\n"+
					"  它今天能跑，只是因为装配里恰好有人装了它；一旦没有，这一句在 Start 期 panic。\n"+
					"  修法：在 module.go 的 Requires 里加 modules.Need(%s)，"+
					"若它其实是可选的则改用 modules.Get 并处理缺失。",
					m.pos, m.key, m.key)
			}
		}
		checked++
	}
	if checked < 5 {
		t.Fatalf("只查到 %d 个模块——扫描退化了（目录布局变了？）", checked)
	}
}

// capKey 是一个 capability 的**跨文件身份**：`import 路径 + 常量名`。
type capKey string

type mustGet struct {
	pos        string // `文件:行`，报错时点名
	key        capKey
	unresolved string // 非空 = 解析不了，值是那句表达式的原文
}

// declaredCapabilities 收 `Requires` 里出现过的每一个 capability。
//
// 扫的是整个模块目录而不是只扫 module.go：`Requires` 必须写在 module.go 里
// （框架的约定），但它引用的常量可能定义在别处——扫全目录才不会因为「文件挪了」
// 而漏。
func declaredCapabilities(t *testing.T, dir string) map[capKey]bool {
	t.Helper()
	out := map[capKey]bool{}
	forEachFile(t, dir, func(fset *token.FileSet, f *ast.File, path string) {
		aliases := importAliases(f)
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			name, ok := calleeName(call)
			if !ok || (name != "Need" && name != "Optional") {
				return true
			}
			// 这些调用只在 module.go 的 Requires 里出现（框架的写法），但就算
			// 出现在别处也无害：多收一个不会让谁漏报。
			if len(call.Args) != 1 {
				return true
			}
			if k, ok := capKeyOf(call.Args[0], aliases); ok {
				out[k] = true
			}
			return true
		})
	})
	return out
}

// mustGets 收 `MustGet` 的第二个参数（第一个是 ctx）。
func mustGets(t *testing.T, dir string) []mustGet {
	t.Helper()
	var out []mustGet
	forEachFile(t, dir, func(fset *token.FileSet, f *ast.File, path string) {
		aliases := importAliases(f)
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			name, ok := calleeName(call)
			if !ok || name != "MustGet" || len(call.Args) != 2 {
				return true
			}
			pos := fset.Position(call.Pos())
			if k, ok := capKeyOf(call.Args[1], aliases); ok {
				out = append(out, mustGet{pos: pos.String(), key: k})
				return true
			}
			out = append(out, mustGet{
				pos:        pos.String(),
				unresolved: exprString(call.Args[1]),
			})
			return true
		})
	})
	return out
}

// capKeyOf 把 `别名.常量名` 解析成跨文件的键。
//
// 解不出来（参数不是 `pkg.Name` 这种形状、或者那个别名不是一条 import）就返回
// false——**不猜**。猜错的代价是这条棘轮开始报假警，而报假警的棘轮很快就会被
// 人加一行豁免，然后它就死了。
func capKeyOf(e ast.Expr, aliases map[string]string) (capKey, bool) {
	sel, ok := e.(*ast.SelectorExpr)
	if !ok {
		return "", false
	}
	pkg, ok := sel.X.(*ast.Ident)
	if !ok {
		return "", false
	}
	path, ok := aliases[pkg.Name]
	if !ok {
		return "", false
	}
	return capKey(path + "." + sel.Sel.Name), true
}

// calleeName 取 `pkg.Fn(...)` 里的 `Fn`；链式或函数值一律返回 false。
//
// **只看方法名、不看接收者**：`modules.MustGet` 与 `component.MustGet` 是同一个
// 函数（同一个包，两个 import 路径），而别名是各文件自己起的（`modules`、
// `component`、`c`…）。按名字认，两边都认得出来。
func calleeName(call *ast.CallExpr) (string, bool) {
	switch fn := call.Fun.(type) {
	case *ast.SelectorExpr:
		return fn.Sel.Name, true
	case *ast.Ident:
		return fn.Name, true
	}
	return "", false
}

// importAliases 是「这个文件里这个别名指向哪条 import 路径」。
//
// 显式别名（`confighookapi "…/modules/confighook"`）用它给的；没写别名的用路径
// 最后一段（Go 的规矩，而 `…/modules/confighook` 的包名就是 `confighook`）。
func importAliases(f *ast.File) map[string]string {
	out := map[string]string{}
	for _, imp := range f.Imports {
		path := strings.Trim(imp.Path.Value, `"`)
		name := path
		if i := strings.LastIndex(path, "/"); i >= 0 {
			name = path[i+1:]
		}
		if imp.Name != nil {
			name = imp.Name.Name
		}
		out[name] = path
	}
	return out
}

func exprString(e ast.Expr) string {
	var b strings.Builder
	// printer 而不是自己拼：表达式可能是个调用（`Capability(...)`），拼错了
	// 报错信息就会指着一个读不懂的东西。
	_ = printer.Fprint(&b, token.NewFileSet(), e)
	return b.String()
}

// forEachFile 遍历模块目录下的**非测试** Go 文件，**含子目录**。
//
// 含子目录是因为 capability 未必在 module.go 里拿：模块把实现拆进子包是常事
// （`modules/runtime/launch` 这种），而「谁声明、谁拿」的比对必须看到那些文件。
//
// 测试不算：`_test.go` 里的 MustGet 是测试自己搭的图，它拿什么由那个测试负责，
// 与模块的 Requires 无关。
func forEachFile(t *testing.T, dir string, fn func(*token.FileSet, *ast.File, string)) {
	t.Helper()
	var names []string
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		names = append(names, path)
		return nil
	})
	if err != nil {
		t.Fatalf("遍历 %s 失败: %v", dir, err)
	}
	sort.Strings(names) // 稳定顺序：同一处坏代码每次报同一行
	for _, path := range names {
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("解析 %s 失败: %v", path, err)
		}
		fn(fset, f, path)
	}
}
