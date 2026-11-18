package cli

import (
	"strings"
	"testing"

	cliapi "github.com/rzbdz/newgate-ext/modules/cli/extension"
)

// argv 解析这一族的测试。它锁的是 2026-09-18 那个现场：
//
//	$ newgate start --force
//	newgate: 不认识的 agent "--force"（已知：[claude opencode]）
//
// ——**而 doctor 自己就在推荐这条命令**（「配置不可用，拒绝接管 … 强行接管：
// newgate start --force」）。同一个坑还有 `newgate probe --json`（`--json` 被当成
// profile 名）。当时契约包里有**两个**取参函数：`Positional` 跳过选项、`Arg` 纯数
// 下标，而 15 个调用点全用了错的那个。`Arg` 已删，只剩这一个。
//
// 所以这些用例不只是「函数行为」，它们是**那条命令以后还能不能敲**的锁。
func TestPositionalSkipsOptions(t *testing.T) {
	tests := []struct {
		name string
		args []string
		i    int
		want string
	}{
		{"纯选项没有位置参数", []string{"--force"}, 0, ""},
		{"选项在前，位置参数在后", []string{"--json", "ds"}, 0, "ds"},
		{"位置参数夹在选项中间", []string{"--a", "one", "--b", "two"}, 1, "two"},
		{"短选项同样跳过", []string{"-f", "logs"}, 0, "logs"},
		{"下标越界", []string{"only"}, 3, ""},
		{"空参数", nil, 0, ""},
		// 单独一个 `-` 按惯例是位置参数（stdin），不是选项。
		{"单破折号算位置参数", []string{"-"}, 0, "-"},
		// `--` 是选项结束符：它自己不是位置参数，它后面的全是。
		{"结束符自身不占位置", []string{"--"}, 0, ""},
		{"结束符之后全是位置参数", []string{"--", "--force"}, 0, "--force"},
		{"结束符之前照常跳过", []string{"--x", "a", "--", "--y"}, 1, "--y"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := cliapi.Positional(tt.args, tt.i); got != tt.want {
				t.Errorf("Positional(%q, %d) = %q, want %q", tt.args, tt.i, got, tt.want)
			}
		})
	}
}

func TestFlagIsExactAndStopsAtTerminator(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want bool
	}{
		{"命中", []string{"--force"}, true},
		{"与位置参数混在一起", []string{"claude", "--force"}, true},
		{"没给就是没有", []string{"claude"}, false},
		{"前缀不算（那是另一个选项）", []string{"--forcefully"}, false},
		// 带值的写法归 FlagValue，Flag 不做 `=` 展开——两条路各管一个形状，
		// 混在一起就会让「--x 有没有值」变得没法回答。
		{"等号形式不归它管", []string{"--force=1"}, false},
		{"结束符之后不再有选项", []string{"--", "--force"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := cliapi.Flag(tt.args, "--force"); got != tt.want {
				t.Errorf("Flag(%q, --force) = %v, want %v", tt.args, got, tt.want)
			}
		})
	}
}

// `newgate upgrade@v0.9.0` 拆成动词 + `@值`：值交给命令解释，界面不认识它。
func TestSplitAtVerb(t *testing.T) {
	s := &service{}
	if _, err := s.RegisterCommand(fakeCommand{names: []string{"upgrade"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RegisterCommand(fakeCommand{names: []string{"odd@name"}}); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		args []string
		want []string
	}{
		{"拆开并保留后续参数", []string{"upgrade@v0.9.0", "--check"}, []string{"upgrade", "@v0.9.0", "--check"}},
		{"前置选项照常跳过", []string{"--profile", "ds", "upgrade@main"}, []string{"--profile", "ds", "upgrade", "@main"}},
		{"没有 @ 不动", []string{"upgrade", "binary"}, []string{"upgrade", "binary"}},
		{"动词不认识就不拆（让未知命令照常报错）", []string{"nope@v1"}, []string{"nope@v1"}},
		{"空值不拆", []string{"upgrade@"}, []string{"upgrade@"}},
		{"名字里本来就带 @ 的命令按原样分派", []string{"odd@name"}, []string{"odd@name"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := splitAtVerb(s, tt.args)
			if strings.Join(got, " ") != strings.Join(tt.want, " ") {
				t.Errorf("splitAtVerb(%q) = %q, want %q", tt.args, got, tt.want)
			}
		})
	}
}
