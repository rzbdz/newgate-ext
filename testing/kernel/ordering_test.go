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

// TestOrderingEdgesStayInsideTheKernel 内核里不许出现指向内核之外的插件排序边。
//
// # 为什么需要这条
//
// special 插件的先后是按**名字**连边的（`Before()` / `After()`，见
// modules/gateway/special 的 Ordered），而排序图对**未注册的名字静默忽略**。
// 于是「内核的插件声明自己排在 deepseek 后面」这种写法有两个毛病，而且都很隐蔽：
//
//   - **方向反了**：deepseek / glm 住在发行版里，是产品插件。内核不认识它们，
//     也不该认识——那条边该由**拥有者**声明（产品说「我排在 always-thinks
//     前面」），因为只有它同时知道两边的名字；
//   - **空转且无声**：纯内核构建里没有叫 deepseek 的插件，那条边压根不存在，
//     而没有任何东西会告诉你。写错了、改名了、插件被搬走了，症状都是「什么都没
//     发生」。
//
// 2026-09-20 之前内核里就有两处这样的边（thinking/st-always.go 的 After 与
// claudecode/st-background.go 的 Before），是用户看代码时发现的——这条测试就是
// 把那次的发现变成机制。
//
// # 判据
//
// 静态的、从源码读，不依赖装配（纯内核构建里那些名字本来就不存在，跑起来也测不出）。
// 「插件」的形状按 special.Plugin 认：同一个接收者类型上同时有 `Name()`、`Match()`、
// `Apply()` 三个方法。**不按方法名单独认**——`testkit.Graph.Before(...)` 是测试设施
// 的组件顺序断言，名字撞上了而已（2026-09-20 实测被它误报过）。
//
// 然后两句话：
//
//	内核插件 `Name()` 的字面量集合 → 内核自己的插件名
//	内核插件 `Before()`/`After()` 的字面量 → 排序边，每一条都必须落在上面那个集合里
//
// 它同时也拦拼错：写错的插件名同样不在集合里，而那种错在今天同样是静默的。
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
		t.Fatalf("扫源码：%v", err)
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

	// 下界是 1 而不是 3：内核这一侧现在只剩 always-thinks 一个请求插件（客户端那些
	// 跟着 claudecode 搬去发行版了）。这条判据的存在意义是「扫到的东西还像插件」，
	// 不是数量——真掉到 0 才是判据坏了。
	if plugins < 1 || len(defined) == 0 {
		t.Fatalf("扫到 %d 个插件、%d 个名字——判据退化了（插件形状变了？目录挪了？）",
			plugins, len(defined))
	}
	for edge, names := range edges {
		for _, name := range names {
			if !defined[name] {
				t.Errorf("%s 里写了 %q，但内核没有任何插件的 Name() 返回这个名字——"+
					"内核的排序边只许指向**内核自己的**插件；产品插件要排在内核插件的前/后，"+
					"由它自己声明（见发行版仓库 modules/deepseek 的 Before/After）", edge, name)
			}
		}
	}
}

// receiverType 取方法接收者的类型名（剥掉指针）。
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

// returnedStrings 收一个方法体里出现的全部字符串字面量。
//
// 不解析语法结构（`return []string{"a"}` 与 `return "a"` 都要认）：这里要的是
// 「这个方法提到了哪些名字」。多收一个常量串只会让集合更大 = 判据更宽松，不会误报。
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
