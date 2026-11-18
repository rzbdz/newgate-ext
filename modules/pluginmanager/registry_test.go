package pluginmanager

import (
	"strings"
	"testing"
	"time"

	cliapi "github.com/rzbdz/newgate-ext/modules/cli/extension"
	confighookapi "github.com/rzbdz/newgate-ext/modules/confighook"
	"github.com/rzbdz/newgate-ext/testing/testkit"
	modules "github.com/rzbdz/newgate/component"
	"github.com/rzbdz/newgate/component/entry"
	i18n "github.com/rzbdz/newgate/lib/i18n"
)

// stubCLI 让 plugin-manager 的 Start 能跑完：它要注入自己的命令与状态行。
// 界面会做命令查重、诊断汇总、help 组装，那些是界面自己的测试该管的事。
type stubCLI struct {
	commands []cliapi.Command
	statuses []cliapi.StatusProvider
}

func (s *stubCLI) Run(entry.Process) int { return 0 }

func (s *stubCLI) RegisterCommand(c cliapi.Command) (modules.Release, error) {
	s.commands = append(s.commands, c)
	return func() error { return nil }, nil
}

func (s *stubCLI) RegisterDiagnostics(cliapi.DiagnosticProvider) (modules.Release, error) {
	return func() error { return nil }, nil
}

func (s *stubCLI) RegisterStatus(p cliapi.StatusProvider) (modules.Release, error) {
	s.statuses = append(s.statuses, p)
	return func() error { return nil }, nil
}

func (s *stubCLI) RegisterStatusBlocks(cliapi.BlockProvider) (modules.Release, error) {
	return func() error { return nil }, nil
}

func (s *stubCLI) RegisterDump(cliapi.Dumper) (modules.Release, error) {
	return func() error { return nil }, nil
}

func (s *stubCLI) RegisterGlossary(cliapi.Glossarist) (modules.Release, error) {
	return func() error { return nil }, nil
}

func (s *stubCLI) RegisterVerbose(cliapi.Verbose) (modules.Release, error) {
	return func() error { return nil }, nil
}

// stubHooks 让 plugin-manager 的 Start 能跑完。真实的 confighook 会做字段查重，
// 那是它自己的测试该管的事；这里只关心 plugin-manager 自己那份账本。
type stubHooks struct{ fields map[string]string }

func newStubHooks() *stubHooks { return &stubHooks{fields: map[string]string{}} }

func (s *stubHooks) RegisterAgent(*confighookapi.Agent) (modules.Release, error) {
	return func() error { return nil }, nil
}

func (s *stubHooks) RegisterAgentInstaller(string, confighookapi.AgentInstaller) (modules.Release, error) {
	return func() error { return nil }, nil
}

func (s *stubHooks) RegisterAgentFacts(string, confighookapi.AgentFacts) (modules.Release, error) {
	return func() error { return nil }, nil
}

func (s *stubHooks) BindTakeover(string, confighookapi.ConfigTakeover) (modules.Release, error) {
	return func() error { return nil }, nil
}

func (s *stubHooks) RegisterStateField(owner, name string) (modules.Release, error) {
	s.fields[owner] = name
	return func() error { return nil }, nil
}

// start 装出「plugin-manager + 它的 confighook 依赖」这张最小真图。
func start(t *testing.T) Manager {
	t.Helper()
	testkit.Sandbox(t)
	hooks := newStubHooks()
	graph := testkit.Start(t,
		modules.Component{
			Name:     "stub-confighook",
			Type:     "infra",
			Provides: []modules.Provision{modules.Provide(confighookapi.ConfigHooksCapability, confighookapi.ConfigHooks(hooks))},
		},
		// 手写 stub：账本长在 modules/cli 的 service 上，而 cli 是重包
		// （拖着 runtime / gateway / breaker），本模块的测试引不起它——引了
		// 就成环。真账本的查重逻辑由 modules/cli 自己的测试守。
		modules.Component{
			Name:     "stub-cli",
			Type:     "cli",
			Provides: []modules.Provision{modules.Provide(cliapi.Capability, cliapi.CLI(&stubCLI{}))},
		},
		New(),
	)
	if hooks.fields["plugin-manager"] != StateKey {
		t.Fatalf("Start 没有登记 state 字段，拿到 %v", hooks.fields)
	}
	return testkit.Get(graph, Capability)
}

func goodSwitch(path string) Switch {
	return Switch{Path: path, Title: "标题", Why: "理由", Danger: DangerQuirk, Default: true}
}

func TestRegisterSelfRejectsBadInput(t *testing.T) {
	m := start(t)

	cases := []struct {
		name     string
		module   string
		switches []Switch
		want     string
	}{
		{"空模块名", "", nil, "must not be empty"},
		{"重复上报", "plugin-manager", nil, "already reported"},
		{"Path 不姓本模块", "deepseek", []Switch{goodSwitch("thinking.always-thinks")}, "must start with the module name"},
		{"Path 只等于模块名", "deepseek", []Switch{goodSwitch("deepseek")}, "must start with the module name"},
		{"没写 Title/Why", "deepseek", []Switch{{Path: "deepseek.x", Danger: DangerQuirk}}, "Title"},
		{
			"footgun 不带时限", "deepseek",
			[]Switch{{Path: "deepseek.x", Title: "t", Why: "w", Danger: DangerFootgun}},
			"must carry a default time limit",
		},
		{
			"不认识的 Danger", "deepseek",
			[]Switch{{Path: "deepseek.x", Title: "t", Why: "w", Danger: "危险"}},
			"unknown Danger",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			release, err := m.RegisterSelf(c.module, c.switches)
			if err == nil {
				if release != nil {
					_ = release()
				}
				t.Fatalf("期望被拒，却注册成功了")
			}
			// 断言的是**消息身份**（英文原文），不是渲染出来的那句话：换一门
			// 语言、改一处措辞，都不该让这条测试跟着红（见 lib/i18n 的 Error）。
			if !strings.Contains(i18n.ID(err), c.want) {
				t.Fatalf("报错身份 %q 里没有 %q", i18n.ID(err), c.want)
			}
		})
	}
}

func TestRegisterSelfRejectsCrossModulePathCollision(t *testing.T) {
	m := start(t)
	// 两个模块报同一个 Path。第二次必须当场报错——静默先到先得是这个功能
	// 最不该有的行为（那样「谁占了这个开关」在列表里看不出来）。
	ok, err := m.RegisterSelf("deepseek", []Switch{goodSwitch("deepseek.tail-shape")})
	if err != nil {
		t.Fatalf("第一次注册不该失败: %v", err)
	}
	defer func() { _ = ok() }()

	// 换个模块名，但 Path 前缀是它自己的、撞的是已存在的那个 Path。
	if _, err := m.RegisterSelf("gateway", []Switch{goodSwitch("deepseek.tail-shape")}); err == nil {
		t.Fatal("跨模块 Path 撞车应该被拒")
	}
}

func TestRegisterSelfReleaseIsSymmetric(t *testing.T) {
	m := start(t)
	before := len(m.Modules())

	release, err := m.RegisterSelf("deepseek", []Switch{goodSwitch("deepseek.tail-shape")})
	if err != nil {
		t.Fatalf("注册失败: %v", err)
	}
	if _, ok := m.Lookup("deepseek.tail-shape"); !ok {
		t.Fatal("注册后 Lookup 应该找得到")
	}

	if err := release(); err != nil {
		t.Fatalf("撤销失败: %v", err)
	}
	if _, ok := m.Lookup("deepseek.tail-shape"); ok {
		t.Fatal("撤销后 Lookup 不该还找得到")
	}
	if got := len(m.Modules()); got != before {
		t.Fatalf("撤销后模块数 = %d，想要 %d", got, before)
	}
	// 陈旧 Release 必须是幂等的 no-op（Stop 可能被调用不止一次）。
	if err := release(); err != nil {
		t.Fatalf("重复撤销该是 no-op，却报错: %v", err)
	}
}

func TestFootgunWithTTLIsAccepted(t *testing.T) {
	m := start(t)
	release, err := m.RegisterSelf("gateway", []Switch{{
		Path: "gateway.passthrough", Title: "直通", Why: "旁路全部修补",
		Danger: DangerFootgun, Default: false, TTL: 5 * time.Minute,
	}})
	if err != nil {
		t.Fatalf("带时限的 footgun 应该注册得了: %v", err)
	}
	defer func() { _ = release() }()

	sw, ok := m.Lookup("gateway.passthrough")
	if !ok {
		t.Fatal("Lookup 失败")
	}
	if sw.TTL != 5*time.Minute || sw.Default {
		t.Fatalf("字段没保住: %+v", sw)
	}
}

// TestModulesEnumerateUnregisteredOnes 是「newgate plugin 列出所有插件」的地基：
// 没参与开关体系的模块也必须列得出来。判据来自组件图，不是「谁上报过」——
// 否则「这个模块没有开关」和「这个模块忘了注册」会长得一模一样。
func TestModulesEnumerateUnregisteredOnes(t *testing.T) {
	m := start(t)
	// 名单由**内核**在装配完成后递一次（component.CatalogAware）；这里用类型断言
	// 直接调同一个方法，因为「递进来的是一张指定的图」这个场景在生产里只有内核
	// 那一处，为它开一个导出的测试入口反而会让两条路并存。
	//
	// 递进来的就是整张图——包含 plugin-manager 自己（它在 Start 里也 RegisterSelf
	// 了自己，走的是同一个口，没有后门）。
	catalog := m.(interface{ SetCatalog([]modules.Component) })
	catalog.SetCatalog([]modules.Component{
		{Name: "config", Type: "infra"},
		{Name: "plugin-manager", Type: "infra"},
		{Name: "deepseek", Type: "model"},
		{Name: "wrapper", Type: "client"},
	})
	if _, err := m.RegisterSelf("deepseek", []Switch{goodSwitch("deepseek.tail-shape")}); err != nil {
		t.Fatalf("注册失败: %v", err)
	}

	got := map[string]Module{}
	var order []string
	for _, mod := range m.Modules() {
		got[mod.Name] = mod
		order = append(order, mod.Name)
	}
	if len(got) != 4 {
		t.Fatalf("应该列出 4 个模块，得到 %v", order)
	}
	// 顺序 = 组件图顺序（启动顺序），不是注册顺序。
	want := []string{"config", "plugin-manager", "deepseek", "wrapper"}
	for i, name := range want {
		if order[i] != name {
			t.Fatalf("顺序 = %v，想要 %v", order, want)
		}
	}
	if len(got["wrapper"].Switches) != 0 {
		t.Fatal("没上报的模块不该有开关点")
	}
	if got["wrapper"].Type != "client" {
		t.Fatalf("Type = %q", got["wrapper"].Type)
	}
	if len(got["deepseek"].Switches) != 1 {
		t.Fatalf("上报的开关点没合进来: %+v", got["deepseek"])
	}
	// 图里没有、但上报过的：显式列出来而不是静默吞掉（app 层测试会拦这种配置
	// 错误——模块名与组件名不一致）。这里是 plugin-manager 自己 + deepseek。
	catalog.SetCatalog(nil)
	if n := len(m.Modules()); n != 2 {
		t.Fatalf("图为空时该列出 2 个上报过的模块（plugin-manager + deepseek），得到 %d", n)
	}
}

func TestDisplayOrderHasNoDuplicates(t *testing.T) {
	seen := map[Type]bool{}
	for _, typ := range DisplayOrder() {
		if seen[typ] {
			t.Fatalf("DisplayOrder 里 %q 出现了两次——同一组会被渲染成两段", typ)
		}
		seen[typ] = true
	}
	if !seen[TypeOthers] {
		t.Fatal("DisplayOrder 必须包含 others：不在表里的分类要靠它兜底")
	}
}
