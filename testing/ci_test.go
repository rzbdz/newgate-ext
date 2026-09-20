package disttesting

import (
	"path/filepath"
	"testing"

	"github.com/rzbdz/newgate/tools/ciyaml"
)

// TestWorkflowsAreStructurallyValid 是发行版这一侧的 CI 配置棘轮。
//
// 判据只有一份实现（core/tools/ciyaml，发行版通过 replace 用的是同一把尺子），
// 与内核那条（core/app/ci_test.go）是同一个包的两个入口。为什么发行版也要有：
// 内核的棘轮只看自己那棵树，而**发行版的 workflow 是发行版自己的**——三个 job
// （core-test / dist-test / 零 token 端到端）里任何一个写坏，症状都一样：GitHub
// 0 秒拒掉整个 run，一个 job 都不起，看着像测试挂了，实际是测试根本没跑。
//
// core/ 一起查：它是 submodule，CI 里 checkout 得到，坏了同样是整轮哑火。
func TestWorkflowsAreStructurallyValid(t *testing.T) {
	root, err := filepath.Abs("..")
	if err != nil {
		t.Fatalf("算仓库根: %v", err)
	}
	for _, sub := range []string{root, filepath.Join(root, "core")} {
		findings, err := ciyaml.Check(sub)
		if err != nil {
			t.Fatalf("CI 配置检查跑不起来——判据退化了（%s 的 workflow 目录挪了？）: %v", sub, err)
		}
		for _, f := range findings {
			t.Errorf("%s/%s", filepath.Base(sub), f)
		}
	}
}
