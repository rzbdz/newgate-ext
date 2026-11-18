package kernel

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// TestOrderingEdgesStayInsideTheKernel: no plugin ordering edge may point outside the kernel.
//
// # Why this
//
// special plugins order by **name** (`Before()` / `After()`, see
// modules/gateway/special's Ordered), and the graph **silently ignores unknown names**.
// So "a kernel plugin says it comes after deepseek" has two subtle flaws:
//
//   - **wrong direction**: deepseek / glm live in the distribution, product plugins. The
//     kernel neither knows them nor should - the edge belongs to the **owner** (the
//     product says "I come before always-thinks"), for only it knows both names;
//   - **spins silently**: a pure-kernel build has no deepseek plugin, so the edge does
//     not exist, and nothing tells you. Wrong name, renamed, moved - all "nothing
//     happened".
//
// Before 2026-09-20 the kernel had two such edges (thinking/st-always.go's After and
// claudecode/st-background.go's Before), found by the user reading code - this test
// makes that find a mechanism.
//
// # Criterion
//
// Static, read from source, no assembly (pure-kernel builds lack those names anyway).
// A "plugin" is special.Plugin's shape: one receiver type with `Name()`, `Match()`,
// `Apply()`. **Not by method name alone** - `testkit.Graph.Before(...)` is test
// infrastructure's component-order assertion, same name, misfired on 2026-09-20.
//
// Then two sentences:
//
//	kernel plugins' `Name()` literals -> the kernel's own plugin names
//	kernel plugins' `Before()`/`After()` literals -> ordering edges, each must be in that set
//
// It also catches typos: a wrong name is not in the set either, silent as well today.
func TestOrderingEdgesStayInsideTheKernel(t *testing.T) {
	root := repoRoot(t)

	type pluginShape struct {
		methods map[string]bool
		names   []string
		before  []string
		after   []string
	}
	byType := map[string]*pluginShape{}
	shapeOf := func(name string) *pluginShape {
		if byType[name] == nil {
			byType[name] = &pluginShape{methods: map[string]bool{}}
		}
		return byType[name]
	}

	fset := token.NewFileSet()
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "bin", "dist", "node_modules":
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return nil
		}
		file, err := parser.ParseFile(fset, p, nil, 0)
		if err != nil {
			return err
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Recv == nil || fn.Body == nil {
				continue
			}
			shape := shapeOf(receiverType(fn))
			shape.methods[fn.Name.Name] = true
			switch fn.Name.Name {
			case "Name":
				shape.names = append(shape.names, returnedStrings(fn.Body)...)
			case "Before":
				shape.before = append(shape.before, returnedStrings(fn.Body)...)
			case "After":
				shape.after = append(shape.after, returnedStrings(fn.Body)...)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("scanning the source: %v", err)
	}

	defined := map[string]bool{}
	edges := map[string][]string{}
	plugins := 0
	for name, shape := range byType {
		if !shape.methods["Name"] || !shape.methods["Match"] || !shape.methods["Apply"] {
			continue // 不是插件，Before/After 是别人的同名方法
		}
		plugins++
		for _, n := range shape.names {
			defined[n] = true
		}
		edges[name+".Before"] = shape.before
		edges[name+".After"] = shape.after
	}

	// The lower bound is 1, not 3: only always-thinks remains as a kernel request plugin
	// (client ones moved with claudecode). The criterion exists so "what was scanned still
	// looks like a plugin", not for the count - only 0 means the criterion is broken.
	if plugins < 1 || len(defined) == 0 {
		t.Fatalf("scanned %d plugins, %d names -- the check has degraded (did the plugin shape change? did the directory move?)",
			plugins, len(defined))
	}
	for edge, names := range edges {
		for _, name := range names {
			if !defined[name] {
				t.Errorf("%s writes %q, but no kernel plugin's Name() returns that name --"+
					"the kernel's ordering edges may only point at **the kernel's own** plugins; to order a product plugin before/after a kernel plugin,"+
					"declare it itself (see Before/After in the distribution repo's modules/deepseek)", edge, name)
			}
		}
	}
}

// receiverType takes the method receiver's type name (pointer stripped).
func receiverType(fn *ast.FuncDecl) string {
	if len(fn.Recv.List) == 0 {
		return "?"
	}
	expr := fn.Recv.List[0].Type
	if star, ok := expr.(*ast.StarExpr); ok {
		expr = star.X
	}
	if ident, ok := expr.(*ast.Ident); ok {
		return ident.Name
	}
	return "?"
}

// returnedStrings collects every string literal in one method body.
//
// No syntax parsing (`return []string{"a"}` and `return "a"` both count): what is
// wanted is "which names this method mentions"; one extra only loosens it, never misfires.
func returnedStrings(body *ast.BlockStmt) []string {
	var out []string
	ast.Inspect(body, func(node ast.Node) bool {
		lit, ok := node.(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return true
		}
		if value, err := strconv.Unquote(lit.Value); err == nil {
			out = append(out, value)
		}
		return true
	})
	return out
}
