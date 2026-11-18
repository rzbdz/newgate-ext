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

// This ratchet guards the **second failure mode** in app/matrix_test.go's header:
//
//	another module **reaches for** an undeclared port in Start -> remove the provider,
//	graph construction does not fail (nobody declared), panic only at run time. The worst
//	kind: builds, tests, and the shipped binary dies on some command.
//
// The removal matrix only **indirectly** touches it: it removes one module at a time,
// and while **someone else** still declares that port, construction fails on them first
// - the reacher's panic never gets its turn. On 2026-09-21 this dug out two real bugs:
//
//	gateway  MustGet(config-hooks)  -  declared no edge at all
//	runtime  MustGet(config-hooks)  -  declared the same module's **other** capability
//
// The second shows why "is there an edge between two modules" is not enough: the edge
// exists (runtime -> confighook via AgentCatalogCapability), so component-granularity
// checks miss it. The real question is **per capability**: the one you take, declared?
//
// So this test asks each module: is every capability your `Start` MustGets in your
// own `Requires`?
//
// # Why AST, not regex
//
// The question is "do these two expressions mean the same capability". Names suffice
// not - every api.go exports a constant called `Capability`; comparing names is all-equal.
// So the **package** comes too: which import path the alias `confighookapi` points at is
// written in the file itself. Key = `import path + constant name`, each side resolving
// its own file's alias, so differing aliases still match.
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
			// A module depending on no capability (today only entry-like): nothing to compare,
			// but do not be silent - the count below includes it.
			checked++
			continue
		}
		for _, m := range mustGets(t, dir) {
			if m.unresolved != "" {
				t.Errorf("%s: cannot parse the argument to this MustGet (%s) -- it may have stored the capability "+
					"in a variable. This ratchet cannot check it; write the constant inline instead.", m.pos, m.unresolved)
				continue
			}
			if !declared[m.key] {
				t.Errorf("%s: the capability MustGet(%s) takes is **not declared in Requires**.\n"+
					"  It only works today because something in the assembly happens to install it; once nothing does, this line panics during Start.\n"+
					"  Fix: add modules.Need(%s) to Requires in module.go,"+
					"or if it is actually optional use modules.Get and handle the missing case.",
					m.pos, m.key, m.key)
			}
		}
		checked++
	}
	if checked < 5 {
		t.Fatalf("found only %d modules -- the scan has degraded (did the directory layout change?)", checked)
	}
}

// capKey is a capability's **cross-file identity**: `import path + constant name`.
type capKey string

type mustGet struct {
	pos        string // `文件:行`，报错时点名
	key        capKey
	unresolved string // 非空 = 解析不了，值是那句表达式的原文
}

// declaredCapabilities collects every capability appearing in `Requires`.
//
// It scans the whole module directory, not just module.go: `Requires` must be written in
// module.go (framework convention), but the constant referenced may be defined elsewhere -
// scanning the directory misses nothing when "a file moved".
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
			// These calls appear only in module.go's Requires (the framework's style), but
			// elsewhere is harmless: collecting one more never makes anyone under-report.
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

// mustGets collects `MustGet`'s second argument (the first is ctx).
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

// capKeyOf resolves `alias.ConstName` into a cross-file key.
//
// Unresolvable (not `pkg.Name` shaped, or the alias is not an import) -> false, **no
// guessing**. Guessing makes the ratchet misfire, and a misfiring ratchet soon gets an
// exemption line added, and then it is dead.
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

// calleeName takes `Fn` from `pkg.Fn(...)`; chained calls or function values return false.
//
// **Name only, not the receiver**: `modules.MustGet` and `component.MustGet` are one
// function (one package, two import paths), and aliases are per-file (`modules`,
// `component`, `c`...). Matching by name recognizes both.
func calleeName(call *ast.CallExpr) (string, bool) {
	switch fn := call.Fun.(type) {
	case *ast.SelectorExpr:
		return fn.Sel.Name, true
	case *ast.Ident:
		return fn.Name, true
	}
	return "", false
}

// importAliases is "in this file, which import path this alias points at".
//
// Explicit aliases (`confighookapi "…/modules/confighook"`) use theirs; without one, the
// path's last segment (Go's rule; `…/modules/confighook`'s package name is `confighook`).
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
	// printer, not hand-built strings: the expression may be a call (`Capability(...)`), and
	// a bad string would point the error at something unreadable.
	_ = printer.Fprint(&b, token.NewFileSet(), e)
	return b.String()
}

// forEachFile walks the module directory's **non-test** Go files, **subdirectories too**.
//
// Subdirectories because capabilities are not always taken in module.go: modules commonly
// split implementation into subpackages (`modules/runtime/launch`), and "who declares,
// who takes" must see those files.
//
// Tests excluded: MustGet in `_test.go` is the test's own graph; its business, not ours.
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
		t.Fatalf("walking %s failed: %v", dir, err)
	}
	sort.Strings(names) // 稳定顺序：同一处坏代码每次报同一行
	for _, path := range names {
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("parsing %s failed: %v", path, err)
		}
		fn(fset, f, path)
	}
}
