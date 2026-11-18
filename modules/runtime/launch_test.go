package runtime

import (
	"strings"
	"testing"

	cliapi "github.com/rzbdz/newgate-ext/modules/cli/extension"
	agentapi "github.com/rzbdz/newgate-ext/modules/confighook"
	"github.com/rzbdz/newgate-ext/modules/runtime/agentstate"
	"github.com/rzbdz/newgate/lib/i18n"
)

func TestSplitLaunch(t *testing.T) {
	cases := []struct {
		name        string
		args        []string
		agent       string
		profile     string
		passthrough string
		wantErr     bool
		errID       string
	}{
		{"bare agent", []string{"claude"}, "claude", "", "", false, ""},
		{"profile after agent, eq form", []string{"claude", "--profile=ds"}, "claude", "ds", "", false, ""},
		{"profile after agent, space form", []string{"claude", "--profile", "ds"}, "claude", "ds", "", false, ""},
		{"profile before agent", []string{"--profile", "ds", "claude"}, "claude", "ds", "", false, ""},
		{"run explicit", []string{"run", "claude", "--profile=ds"}, "claude", "ds", "", false, ""},
		{"passthrough flags", []string{"claude", "--resume", "abc"}, "claude", "", "--resume|abc", false, ""},
		{"passthrough after profile", []string{"claude", "--profile", "ds", "-p", "hi"}, "claude", "ds", "-p|hi", false, ""},
		{"preset alias", []string{"--preset", "toy", "claude"}, "claude", "toy", "", false, ""},
		{"unknown flag before agent", []string{"--bogus", "claude"}, "", "", "", true,
			"Unknown option {option} (an unknown option before the tool counts as a typo, docs/08-operations.md rule 5)"},
		{"no agent", []string{"--profile", "ds"}, "", "", "", true,
			"Which agent to launch? e.g. newgate claude or newgate run claude"},
		{"unknown agent", []string{"nope", "--profile=ds"}, "", "", "", true,
			"Unknown agent {agent} (known: {list})"},
		{"conflicting profile", []string{"claude", "--profile=ds", "--preset=glm"}, "", "", "", true,
			"Both profile {first} and {second} were given, and they disagree"},
		{"profile missing value", []string{"claude", "--profile"}, "", "", "", true,
			"{option} needs a profile name"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			agent, profile, passthrough, err := splitLaunch(agentstate.Catalog(), c.args)
			if c.wantErr {
				if err == nil {
					t.Fatalf("期望报错，却成功了（agent=%q profile=%q）", agent, profile)
				}
				// 断言**消息身份**（语言无关）：换语言或改措辞时，这里该红的是措辞
				// 变了这件事，而不是「译文没跟上」。
				if c.errID != "" {
					if got := i18n.ID(err); got != c.errID {
						t.Fatalf("报错身份 = %q，应为 %q（渲染出来是 %q）", got, c.errID, err)
					}
				}
				return
			}
			if err != nil {
				t.Fatalf("不该报错：%v", err)
			}
			if agent != c.agent {
				t.Errorf("agent = %q，应为 %q", agent, c.agent)
			}
			if profile != c.profile {
				t.Errorf("profile = %q，应为 %q", profile, c.profile)
			}
			got := strings.Join(passthrough, "|")
			if got != c.passthrough {
				t.Errorf("passthrough = %q，应为 %q", got, c.passthrough)
			}
		})
	}
}

// TestLaunchFamilyClaimsExactlyOneHelpLine 包装启动那一族在 help 里只占一行。
//
// 「`run <agent> [args…]` 只出现一次」这条**曾经是靠实例数凑出来的**：2026-09-18
// 之前每个 agent 一个 launchCommand，实例按 agentID 返回空 Usage 来不占行。那样
// 注册期就得知道全部 agent（见 launch.go 的说明：那条路让 `newgate claude` 在
// cli 被排到最前之后直接失联）。现在只有一条命令、一行 help，这条测试改成锁
// **动词表**：它必须现查目录，且不把 agent 名写进 help。
//
// 守在这里而不是界面那边：**是这一族多交了一行，不是界面漏了去重**。界面按
// 约定「空 Usage = 不占行」处理，把去重塞进界面就等于让它又认识一遍这些命令。
func TestLaunchFamilyClaimsExactlyOneHelpLine(t *testing.T) {
	catalog := testCatalog{
		"claude":   {ID: "claude", Bin: []string{"claude"}},
		"opencode": {ID: "opencode", Bin: []string{"opencode"}},
	}
	cmds := launchCommands(nil, catalog)
	if len(cmds) != 1 {
		t.Fatalf("期望恰好 1 条命令，实际 %d 条", len(cmds))
	}

	var claimed []string
	for _, c := range cmds {
		doc, ok := c.(interface{ Help() cliapi.HelpLine })
		if !ok {
			t.Fatal("launchCommand 必须实现 Documented")
		}
		line := doc.Help()
		if line.Usage == "" {
			continue // 按约定不占行
		}
		claimed = append(claimed, line.Usage)
		if line.Usage == "run <agent> [args…]" && line.Section != cliapi.SectionRunOnce {
			t.Errorf("`run` 那行的槽位 = %q，应为 %q", line.Section, cliapi.SectionRunOnce)
		}
	}
	if len(claimed) != 1 {
		t.Fatalf("这一族只该有一行 help，实际 %d 行：%v", len(claimed), claimed)
	}
	if claimed[0] != "run <agent> [args…]" {
		t.Fatalf("那一行应是 `run <agent> [args…]`，实际 %q", claimed[0])
	}

	// 动词表**现查目录**：装配之后长出来的 agent 立刻可分派，不需要重启。
	names := cmds[0].Names()
	if got := strings.Join(names, ","); got != "claude,opencode,run" {
		t.Fatalf("Names() = %q，应为 `claude,opencode,run`", got)
	}
	catalog["glm"] = &agentapi.Agent{ID: "glm", Bin: []string{"glm"}}
	if got := strings.Join(cmds[0].Names(), ","); got != "claude,glm,opencode,run" {
		t.Fatalf("新 agent 注册之后 Names() = %q——动词表是快照，不是现查", got)
	}

	// help 里**不许出现 agent 名**：那是各客户端模块的键，列出来等于界面又认识
	// 了一遍客户端。
	for _, name := range []string{"claude", "opencode", "glm"} {
		if strings.Contains(claimed[0], name) {
			t.Errorf("help 行 %q 里出现了 agent 名 %q", claimed[0], name)
		}
	}
}
