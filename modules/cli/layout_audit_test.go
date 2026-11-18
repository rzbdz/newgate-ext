package cli

import (
	"fmt"
	"strings"
	"testing"

	"github.com/rzbdz/newgate-ext/lib/style"
)

// fakeDoc 是一条模块贡献的命令，用来把「模块自己声明的 help 行」也纳入宽度
// 守护。真实贡献者（naked / plugin / omo）都在各自模块里，cli 的测试 import
// 不到它们（会成环），所以这里造一条形状相同的。
type fakeDoc struct {
	name string
	line HelpLine
}

func (f fakeDoc) Names() []string        { return []string{f.name} }
func (f fakeDoc) Run(Host, []string) int { return 0 }
func (f fakeDoc) Help() HelpLine         { return f.line }

// usageFixture 装一个带模块贡献的 service：一条并进已有节，一条自开新节。
// 名字故意取得长——上一版就是被一个 19 字符的模块名撑破过版面。
//
// 账本就在 service 上，直接用真的（不手写 stub）：这样 --help 的组装路径与
// 线上是同一份代码，包括命令名查重。
func usageFixture(t *testing.T) *service {
	t.Helper()
	s := &service{}
	// 名字各不相同：账本按 Names 查重（撞名当场报错，这是它的不变量之一）。
	for i, line := range []HelpLine{
		{Section: SectionMaintenance, Usage: "claudecode-deepseek-thing on|off [时长]", Summary: "一个很长的模块名，用来把左列撑开"},
		{Section: SectionModules, Usage: "plugin [模块[.路径]] [on|off] [时长]", Summary: "全部模块按分类列出；开关某个模块或某个开关点"},
	} {
		if _, err := s.RegisterCommand(fakeDoc{name: fmt.Sprintf("fake%d", i), line: line}); err != nil {
			t.Fatalf("注册失败: %v", err)
		}
	}
	return s
}

// TestUsageNeverExceedsLayoutWidth 守两件事：CLI 自己的帮助不超宽，**模块贡献
// 的那些行也不超宽**。后者是关键——贡献行是别人写的，CLI 必须在渲染时兜住，
// 否则一个模块把自己的 Usage 写长一点就能把整个 --help 撑破。
func TestUsageNeverExceedsLayoutWidth(t *testing.T) {
	for _, text := range []string{usageText(nil), usageText(usageFixture(t))} {
		for i, line := range strings.Split(text, "\n") {
			if width := style.VisibleWidth(line); width > style.MaxColumns {
				t.Fatalf("usage line %d is %d columns: %q", i+1, width, line)
			}
		}
	}
}

// TestUsageIncludesModuleContributions 守「命令搬回模块之后 help 也跟着搬」。
//
// 上一版把 naked / plugin 从 usageText 的硬编码列表里删掉、却没有别的地方渲染
// 模块贡献，结果是两条命令从 --help 里**彻底消失**——命令还在，但用户看不见等于
// 不存在。这条测试拦的就是这个：贡献的 Usage 必须出现在最终文本里。
func TestUsageIncludesModuleContributions(t *testing.T) {
	text := usageText(usageFixture(t))
	for _, want := range []string{"claudecode-deepseek-thing", "plugin [模块"} {
		if !strings.Contains(text, want) {
			t.Fatalf("--help 里没有 %q：\n%s", want, text)
		}
	}
	// 自开的节要有自己的标题，而不是被吞进上一节。标题断言的是**显示名**
	// （源语言就是英文那句）：槽位的键是 ASCII 身份，文案归目录
	// ——见 extension.Section.Display。
	if !strings.Contains(text, SectionModules.Display()+"\n") {
		t.Fatalf("模块自开的节没有渲染出来：\n%s", text)
	}
}
