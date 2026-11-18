package launch

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/rzbdz/newgate-ext/modules/config/domain"
	agentapi "github.com/rzbdz/newgate-ext/modules/confighook"
)

// sandboxStore 在 NEWGATE_HOME 沙箱里铺一份最小配置：一个 provider、
// 两个 profile——glm 声明了窗口，tiny 没声明。
func sandboxStore(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("NEWGATE_HOME", dir)
	write := func(rel, content string) {
		t.Helper()
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("providers.json", `{"providers":{"smt-glm":{"base_url":"https://x/v1","api_key":"k"}}}`)
	write("mappings/glm.json", `{
		"name": "glm", "priority": 20,
		"context_window": 1000000, "auto_compact_window": 500000,
		"roles": {"heavy": "smt-glm/glm-5.3", "mid": "smt-glm/glm-5.3",
			"light": "smt-glm/glm-4.5-air", "vision": "smt-glm/glm-4.5-air"}
	}`)
	// tiny 只写 heavy 的话，主力槽（四档化后是 normal）在它身上没有候选，
	// 链会掉到下一个 profile——所以这里显式给 tiny 一条 normal（也是给它
	// 的测试用例留个「主力档真名跟 profile 走」的断言点）。
	write("mappings/tiny.json", `{"name":"tiny","priority":30,
		"roles":{"heavy":"smt-glm/glm-4-plus","normal":"smt-glm/glm-4-plus"}}`)
}

// TestBuildInjectWindowEnv 窗口声明的注入。Claude Code 不认识我们注入的
// 真实模型名（glm-5.3），按「未知模型」默认 200k 窗口提前 compact——
// profile 里声明了 context_window/auto_compact_window 才有 env（2026-09
// 实测二进制 2.1.265 的解析链：MAX_CONTEXT_TOKENS 只对未知模型生效，
// AUTO_COMPACT_WINDOW 优先级最高、window=min(两者)）。客户端描述符还能给
// 这两个数封顶（见 TestBuildInjectWindowCap）。
// testAgent 是内核测试用的**合成**客户端定义：真实的 claude 定义住在发行版里
// （客户端接入是产品取舍，2026-09-20 搬走）。窗口 env 的**名字**如今也来自定义
// （agentapi.Agent.ContextWindowEnv / AutoCompactEnv）——内核不认识任何一家的变量名，
// 所以这里用合成的名字，测的是「配置里声明了就按定义的名字注入」这条机制。
func testAgent() *agentapi.Agent {
	return &agentapi.Agent{
		ID:               "test-client",
		Bin:              []string{"test-client"},
		Dialect:          "anthropic",
		BaseURLEnv:       "TEST_BASE_URL",
		AuthEnv:          "TEST_API_KEY",
		ContextWindowEnv: "TEST_MAX_CONTEXT_TOKENS",
		AutoCompactEnv:   "TEST_AUTO_COMPACT_WINDOW",
		// 三个槽位与档位一一对应：这样断言读起来就是「槽位拿到自己那一档」，
		// 不需要知道任何一家客户端的槽位命名（claude 的 opus/fable/haiku 是它的知识）。
		Slots: []agentapi.Slot{
			{Name: "heavy", Tier: "heavy", EnvVar: "TEST_HEAVY_MODEL"},
			{Name: "normal", Tier: "normal", EnvVar: "TEST_NORMAL_MODEL"},
			{Name: "light", Tier: "light", EnvVar: "TEST_LIGHT_MODEL"},
		},
	}
}

func TestBuildInjectWindowEnv(t *testing.T) {
	sandboxStore(t)
	st := &domain.State{Port: 8899}
	a := testAgent()

	t.Run("声明了窗口的 profile → 两个 env 都注入", func(t *testing.T) {
		inject, _ := buildInject(a, nil, st, "glm", "")
		if got := inject["TEST_MAX_CONTEXT_TOKENS"]; got != "1000000" {
			t.Errorf("MAX_CONTEXT_TOKENS = %q，应为 1000000", got)
		}
		if got := inject["TEST_AUTO_COMPACT_WINDOW"]; got != "500000" {
			t.Errorf("AUTO_COMPACT_WINDOW = %q，应为 500000", got)
		}
	})

	t.Run("没声明的 profile → 不注入", func(t *testing.T) {
		inject, _ := buildInject(a, nil, st, "tiny", "")
		if _, has := inject["TEST_MAX_CONTEXT_TOKENS"]; has {
			t.Error("tiny 没配 context_window，不该注入 MAX_CONTEXT_TOKENS")
		}
		if _, has := inject["TEST_AUTO_COMPACT_WINDOW"]; has {
			t.Error("tiny 没配 auto_compact_window，不该注入 AUTO_COMPACT_WINDOW")
		}
	})

	t.Run("不存在的 profile → fail-open 不注入", func(t *testing.T) {
		inject, _ := buildInject(a, nil, st, "ghost", "")
		if _, has := inject["TEST_MAX_CONTEXT_TOKENS"]; has {
			t.Error("profile 不存在时窗口 env 不该出现")
		}
	})

	t.Run("只有 context_window → 只注入一个", func(t *testing.T) {
		// 两个都独立生效：用户可以只报真实窗口、让客户端自己定 compact 点。
		dir := os.Getenv("NEWGATE_HOME")
		if err := os.WriteFile(filepath.Join(dir, "mappings", "glm.json"),
			[]byte(`{"name":"glm","priority":20,"context_window":1000000,
				"roles":{"heavy":"smt-glm/glm-5.3"}}`), 0o644); err != nil {
			t.Fatal(err)
		}
		inject, _ := buildInject(a, nil, st, "glm", "")
		if got := inject["TEST_MAX_CONTEXT_TOKENS"]; got != "1000000" {
			t.Errorf("MAX_CONTEXT_TOKENS = %q，应为 1000000", got)
		}
		if _, has := inject["TEST_AUTO_COMPACT_WINDOW"]; has {
			t.Error("没配 auto_compact_window 就不该注入")
		}
	})
}

// TestBuildInjectWindowCap 客户端的封顶（ContextWindowCap / AutoCompactCap）：
// profile 声明得再大也不超过客户端的数，且**超过时要说一声**。
//
// 现场（2026-11-12）：1M 窗口把会话顶到最后一格，400 之后连 /compact 都发不出去
// ——压缩请求本身也超限。封顶的作用是给压缩留出余量；悄悄把用户要的 1M 换掉
// 则会制造「配置看着对、/context 显示 512K」的困惑，所以要有一条 warn。
func TestBuildInjectWindowCap(t *testing.T) {
	sandboxStore(t)
	st := &domain.State{Port: 8899}
	a := testAgent()
	a.ContextWindowCap = 512000
	a.AutoCompactCap = 400000

	t.Run("profile 声明 1M → 封到 512K/400K，并 warn", func(t *testing.T) {
		inject, warns := buildInject(a, nil, st, "glm", "")
		if got := inject["TEST_MAX_CONTEXT_TOKENS"]; got != "512000" {
			t.Errorf("MAX_CONTEXT_TOKENS = %q，应封到 512000", got)
		}
		if got := inject["TEST_AUTO_COMPACT_WINDOW"]; got != "400000" {
			t.Errorf("AUTO_COMPACT_WINDOW = %q，应封到 400000", got)
		}
		if len(warns) != 1 {
			t.Errorf("把 1M 封成 512K 必须有一条 warn，实际 %v", warns)
		}
	})

	t.Run("profile 没声明窗口 → 上限当声明，compact 不兜底", func(t *testing.T) {
		inject, warns := buildInject(a, nil, st, "tiny", "")
		if got := inject["TEST_MAX_CONTEXT_TOKENS"]; got != "512000" {
			t.Errorf("没声明窗口时该用客户端上限 512000，实际 %q", got)
		}
		if _, has := inject["TEST_AUTO_COMPACT_WINDOW"]; has {
			t.Error("没声明 compact 时不该注入——客户端会按自己的窗口算压缩点")
		}
		if len(warns) != 0 {
			t.Errorf("没声明大窗口，不该 warn，实际 %v", warns)
		}
	})

	t.Run("profile 声明得比上限小 → 原样", func(t *testing.T) {
		dir := os.Getenv("NEWGATE_HOME")
		if err := os.WriteFile(filepath.Join(dir, "mappings", "glm.json"),
			[]byte(`{"name":"glm","priority":20,"context_window":200000,
				"auto_compact_window":150000,"roles":{"heavy":"smt-glm/glm-5.3"}}`), 0o644); err != nil {
			t.Fatal(err)
		}
		inject, warns := buildInject(a, nil, st, "glm", "")
		if got := inject["TEST_MAX_CONTEXT_TOKENS"]; got != "200000" {
			t.Errorf("MAX_CONTEXT_TOKENS = %q，应为 200000", got)
		}
		if got := inject["TEST_AUTO_COMPACT_WINDOW"]; got != "150000" {
			t.Errorf("AUTO_COMPACT_WINDOW = %q，应为 150000", got)
		}
		if len(warns) != 0 {
			t.Errorf("没超过上限，不该 warn，实际 %v", warns)
		}
	})
}

// TestBuildInjectModelNames 槽位命名的两种模式（2026-09-09 定稿）：
// 默认动态——槽位保持档位名，--set-profile 对跑着的会话立刻生效；
// 显式 --profile 钉死——注入真实模型名，显示的就是这次真用的。
func TestBuildInjectModelNames(t *testing.T) {
	sandboxStore(t)
	st := &domain.State{Port: 8899}
	a := testAgent()

	t.Run("默认（动态）：槽位 = 档位名，base URL 不带 /p/", func(t *testing.T) {
		inject, _ := buildInject(a, nil, st, "glm", "")
		if got := inject["TEST_BASE_URL"]; got != "http://127.0.0.1:8899/a/test-client" {
			t.Errorf("BASE_URL = %q", got)
		}
		// 四档化（2026-09-16）：opus 槽 = 主力档 normal，fable 槽才是 heavy
		if got := inject["TEST_NORMAL_MODEL"]; got != "normal" {
			t.Errorf("动态模式槽位应是档位名，NORMAL_MODEL = %q", got)
		}
		if got := inject["TEST_HEAVY_MODEL"]; got != "heavy" {
			t.Errorf("HEAVY_MODEL = %q，应为 heavy", got)
		}
		if got := inject["TEST_LIGHT_MODEL"]; got != "light" {
			t.Errorf("LIGHT_MODEL = %q，应为 light", got)
		}
	})

	t.Run("显式 profile（钉死）：槽位 = 真实模型名，base URL 带 /p/", func(t *testing.T) {
		inject, _ := buildInject(a, nil, st, "glm", "glm")
		if got := inject["TEST_BASE_URL"]; got != "http://127.0.0.1:8899/a/test-client/p/glm" {
			t.Errorf("BASE_URL = %q", got)
		}
		if got := inject["TEST_NORMAL_MODEL"]; got != "glm-5.3" {
			t.Errorf("钉死模式应是真实名，NORMAL_MODEL = %q", got)
		}
		if got := inject["TEST_LIGHT_MODEL"]; got != "glm-4.5-air" {
			t.Errorf("LIGHT_MODEL = %q，应为 glm-4.5-air", got)
		}
	})

	t.Run("钉死到别的 profile：真名跟被选中的 profile 走", func(t *testing.T) {
		// 默认链头是 glm，--profile tiny：真名必须是 tiny 的，不然界面
		// 显示的和实际跑的对不上
		inject, _ := buildInject(a, nil, st, "tiny", "tiny")
		if got := inject["TEST_NORMAL_MODEL"]; got != "glm-4-plus" {
			t.Errorf("NORMAL_MODEL = %q，应为 tiny 的 glm-4-plus", got)
		}
		if _, has := inject["TEST_MAX_CONTEXT_TOKENS"]; has {
			t.Error("窗口应该跟被选中的 profile（tiny，没声明）走")
		}
	})
}

// TestModelSuffixSurvivesBothModes 槽位声明的模型名后缀（客户端的能力标记）
// 在**两种命名模式**下都必须跟着值走，且**只**跟着模型名走。
//
// 为什么这条要单独锁（2026-09-28）：标记的落点是「注入出去的模型名」，而这条
// 注入在代码里有**两处**——BuildEnv（动态：档位名）与 buildInject 的钉死分支
// （真实模型名）。用户点名要优先支持的是后者（`newgate claude --profile=xx`），
// 而它恰好是后加的那一处：动态模式对了、钉死模式丢了标记，症状是「切了 profile
// 之后 1M 上下文又没了」，而配置看起来一切正常。
//
// 标记本身是**合成的**（`@1m`）：内核不认识任何一家的真实标记——真实那个由客户端
// 模块给（发行版 modules/claudecode 用 gateway/protocol.OneMMarker）。这里测的是
// 机制：「声明了就拼、两种模式都拼、数字变量不拼」。
func TestModelSuffixSurvivesBothModes(t *testing.T) {
	sandboxStore(t)
	st := &domain.State{Port: 8899}
	a := testAgent()
	const mark = "@1m"
	for i := range a.Slots {
		a.Slots[i].ModelSuffix = mark
	}

	t.Run("动态模式：档位名带标记", func(t *testing.T) {
		inject, _ := buildInject(a, nil, st, "glm", "")
		if got := inject["TEST_NORMAL_MODEL"]; got != "normal"+mark {
			t.Errorf("NORMAL_MODEL = %q，应为 normal%s", got, mark)
		}
		if got := inject["TEST_LIGHT_MODEL"]; got != "light"+mark {
			t.Errorf("LIGHT_MODEL = %q，应为 light%s", got, mark)
		}
	})

	t.Run("钉死模式：真实模型名带标记（--profile=xx 那条路）", func(t *testing.T) {
		inject, _ := buildInject(a, nil, st, "glm", "glm")
		if got := inject["TEST_NORMAL_MODEL"]; got != "glm-5.3"+mark {
			t.Errorf("NORMAL_MODEL = %q，应为 glm-5.3%s", got, mark)
		}
		if got := inject["TEST_LIGHT_MODEL"]; got != "glm-4.5-air"+mark {
			t.Errorf("LIGHT_MODEL = %q，应为 glm-4.5-air%s", got, mark)
		}
	})

	// 窗口声明是**数字**（strconv.Itoa 出来的），拼上标记会写进去一个解析不了的
	// 值——失败的样子是「窗口声明静默失效、又回到 200k 假设」，看起来像这条注入
	// 根本不存在。所以数字变量一次都不许过 Apply。
	t.Run("窗口声明不沾标记", func(t *testing.T) {
		inject, _ := buildInject(a, nil, st, "glm", "glm")
		if got := inject["TEST_MAX_CONTEXT_TOKENS"]; got != "1000000" {
			t.Errorf("MAX_CONTEXT_TOKENS = %q，应为 1000000（不带标记）", got)
		}
		if got := inject["TEST_AUTO_COMPACT_WINDOW"]; got != "500000" {
			t.Errorf("AUTO_COMPACT_WINDOW = %q，应为 500000（不带标记）", got)
		}
	})
}

// TestBuildInjectOverridesInherited execReal 的 env 白名单保证注入值赢过
// 用户 shell 里残留的同名变量——「切了没生效」最常见的成因。
// 这里只验证 buildInject 产出正确；覆盖发生在 execReal，由 e2e 盖着。
func TestBuildInjectOverridesInherited(t *testing.T) {
	sandboxStore(t)
	st := &domain.State{Port: 8899}
	a := testAgent()

	t.Setenv("TEST_AUTO_COMPACT_WINDOW", "666") // 用户自己设过别的值
	inject, _ := buildInject(a, nil, st, "glm", "")
	if got := inject["TEST_AUTO_COMPACT_WINDOW"]; got != "500000" {
		t.Fatalf("profile 的声明应该赢过用户环境里的旧值: %q", got)
	}
}

// TestBuildInjectWarnsWhenConfigUnreadable 配置读不出来时必须留一句话。
//
// 这条路径是 fail-open 的：读不到配置就退回「槽位保持档位名」，代理仍能按档位
// 名路由，所以这次调用照样起得来。但它同时**少注入**了两样东西——真实模型名，
// 以及 profile 声明的窗口（不给的话 Claude Code 按 200k 假设提前 compact）。
// 后者是用户能直接感觉到的行为变化，而 2026-09-18 之前这条 if 没有 else，
// 整块注入静默跳过，一个字都不说。
func TestBuildInjectWarnsWhenConfigUnreadable(t *testing.T) {
	sandboxStore(t)
	st := &domain.State{Port: 8899}
	a := testAgent()

	// 把 providers.json 弄坏：Load 在第一步就失败。
	p := filepath.Join(os.Getenv("NEWGATE_HOME"), "providers.json")
	if err := os.WriteFile(p, []byte("{ 这不是 json"), 0o644); err != nil {
		t.Fatal(err)
	}

	inject, warns := buildInject(a, nil, st, "glm", "glm")
	if len(warns) == 0 {
		t.Fatal("配置读不出来时必须给出警告，否则用户只看到「窗口没生效」而不知道原因")
	}
	// fail-open：槽位仍然注入了（档位名），只是不是真实模型名。
	if got := inject["TEST_NORMAL_MODEL"]; got != "normal" {
		t.Errorf("读不到配置时槽位应保持档位名 normal，实际 %q", got)
	}
	if _, has := inject["TEST_MAX_CONTEXT_TOKENS"]; has {
		t.Error("读不到配置时不该有窗口声明")
	}
}
