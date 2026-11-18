package gateway

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// 数据面不认识任何策略——这条法则今天是一条**测试**，不是一条注释。
//
// 2026-09-18 把 breaker 从「gateway 的库」倒成「gateway 的消费者」那一轮，判据
// 写的是可机械核对的一行：
//
//	command grep -rn "breaker" modules/gateway/forward/ modules/gateway/module.go modules/gateway/serve.go
//
// 必须是 0 行。为什么值得用一条测试钉住：**破了它，编译照样过、单测照样绿、e2e
// 照样全绿**——因为 breaker → gateway 这条依赖边还在，import 一句就能再长回去，
// 而行为一字不变。倒置之前的样子就是那样：`forward.Server` 上挂一个 Health 字段，
// 热路径直接写 `breaker.KindConnError`、按 `BucketShape` 分支，于是「谁算失败」
// 「记进哪本账」「留什么痕」这些**政策**长在了内核的 if 里，而依赖图上一点痕迹
// 都没有。唯一的守卫就是「有没有人去看」。
//
// 两条判据分开写，因为它们的理由不同：
//
//  1. **数据面源码里不出现那个名字**（forward 整个目录 + module.go + serve.go）。
//     不是「不 import」——是连名字都不认识：政策将来换个实现、换套词汇，数据面
//     这一侧一个字都不用动。今天它靠 policy.Registry 的四个决策点说话。
//  2. **modules/gateway 里不 import 策略包本身**（`…/modules/breaker"`，那个装
//     逻辑的包）。`…/modules/breaker/status` 是**wire 叶子**：优雅交接期间新旧
//     二进制混跑，控制面文档的形状是跨版本的公共契约，docs/03-architecture.md §2
//     明确允许直接 import，不算依赖边（command_metrics.go 就是这么用的）。
//
// **测试文件不受判据 1 约束**：它们大量引用这个词，正是为了解释「为什么不 import
// 它」（见 forward/testutil_test.go 那段），改掉反而丢掉现场。所以这里跳过
// `_test.go`——包括本文件自己。
func TestTheDataPlaneDoesNotNameAnyStrategy(t *testing.T) {
	root := gatewayRoot(t)

	named := map[string]bool{"module.go": true, "serve.go": true}
	for _, path := range sourceFiles(t, root) {
		rel, err := filepath.Rel(root, path)
		if err != nil {
			t.Fatalf("算相对路径失败 %s: %v", path, err)
		}
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("读不了 %s: %v", path, err)
		}
		text := string(body)

		// 判据 1 只管这三处：forward 整个目录，以及根上的两个文件。
		if named[rel] || strings.HasPrefix(filepath.ToSlash(rel), "forward/") {
			for i, line := range strings.Split(text, "\n") {
				if strings.Contains(line, "breaker") {
					t.Errorf("%s:%d 数据面源码里出现了策略的名字（判据 1）——"+
						"这是把政策长回内核 if 里的第一步：\n    %s\n"+
						"   决策请走 policy.Registry 的四个口（见 modules/gateway/policy）",
						rel, i+1, strings.TrimSpace(line))
				}
			}
		}

		// 判据 2 管整个子树：import 那个装逻辑的包 = 依赖边又长回去了。
		//
		// 针尖拆成两截拼起来是**故意的**：不拆的话本文件自己就含那串字面量，
		// `command grep -rln 'modules/breaker"' modules/gateway/` 会把自己报出来，
		// 判据就永远清不了零。
		if strings.Contains(text, `"github.com/rzbdz/newgate/modules/`+`breaker"`) {
			t.Errorf("%s 直接 import 了策略包（判据 2）——"+
				"方向应该是策略 Need(gateway) 并 RegisterFilter，而不是数据面拉它进来", rel)
		}
	}
}

// TestTheDataPlaneDoesNotKnowThePortSharingMechanism 是同一把尺子的第二条：
// **数据面不认识「一个端口上挂多个服务」这件事**。
//
// 2026-09-20 的第一版实现把 porthub 查表写进了 catch-all（`forward.dispatch`），
// 编译过、转发照常、单测全绿——唯一的症状是数据面从此知道了这个进程的部署形态：
// 端口上还住着谁、谁先接住哪个路径。那些是**守护进程入口**的知识（它把数据面的
// handler 交给 porthub，换回这个端口的根 handler，见 serve.go）。
//
// 判据与策略那条一样是「连名字都不出现」，范围只到 forward/：serve.go 与
// module.go **必须**认识它（那就是合成发生的地方），这也是判据 1 那两个文件在
// 这里被排除的原因。
func TestTheDataPlaneDoesNotKnowThePortSharingMechanism(t *testing.T) {
	root := gatewayRoot(t)
	dir := filepath.Join(root, "forward")
	checked := 0
	for _, path := range sourceFiles(t, dir) {
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("读不了 %s: %v", path, err)
		}
		checked++
		for i, line := range strings.Split(string(body), "\n") {
			if strings.Contains(line, "porthub") {
				t.Errorf("%s:%d 数据面源码里出现了端口共享机制的名字——"+
					"数据面只该拿到一个 handler，不知道它从哪来、也不知道端口上还有谁：\n    %s\n"+
					"   合成请留在守护进程入口（serve.go 的 rootHandler 那一段）",
					path, i+1, strings.TrimSpace(line))
			}
		}
	}
	if checked == 0 {
		t.Fatal("forward/ 里一个源文件都没扫到——判据会变成空转")
	}
}

// gatewayRoot 是 modules/gateway 的绝对路径（本文件所在目录）。
func gatewayRoot(t *testing.T) string {
	t.Helper()
	_, this, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller 拿不到本文件路径")
	}
	return filepath.Dir(this)
}

// sourceFiles 列出网关子树里的非测试源码。测试文件是故意排除的（见上面那段）。
func sourceFiles(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		name := d.Name()
		if d.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			return nil
		}
		out = append(out, path)
		return nil
	})
	if err != nil {
		t.Fatalf("遍历 %s 失败: %v", root, err)
	}
	if len(out) == 0 {
		t.Fatalf("%s 里一个源文件都没找到——判据会变成空转", root)
	}
	return out
}
