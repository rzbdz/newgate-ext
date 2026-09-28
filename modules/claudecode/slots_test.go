package claudecode

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/rzbdz/newgate/lib/view"

	agentapi "github.com/rzbdz/newgate/modules/confighook"
	"github.com/rzbdz/newgate/modules/gateway/protocol"
	"github.com/rzbdz/newgate/testing/testkit"
)

// 槽位映射从 2026-09-21 起可配。这一族测的是那条链上最容易断的三处：读到的值要
// 真的进注入、写进来的东西要真的落盘、坏值要**响亮地**不起作用（而不是安静地
// 变成一个不存在的模型名发到上游）。

// slotOf 取一个槽位定义（按名字）。
func slotOf(t *testing.T, name string) agentapi.Slot {
	t.Helper()
	for _, s := range Agent().Slots {
		if s.Name == name {
			return s
		}
	}
	t.Fatalf("没有叫 %q 的槽位", name)
	return agentapi.Slot{}
}

// TestASlotFollowsTheConfig：改过的槽位，注入时走的是新档位。
//
// 断言打在 **BuildEnv** 上而不是 TierOf 上：用户看得见的那个结果是 env 里那个值，
// 而「TierOf 对了、BuildEnv 忘了用它」正是这类改动最容易留下的样子——界面显示
// 改了、行为没变。
func TestASlotFollowsTheConfig(t *testing.T) {
	testkit.Sandbox(t)
	writeState(t, map[string]any{SlotsKey: map[string]string{"sonnet": "normal"}})

	a := Agent()
	if got := agentapi.TierOf(facts{}, slotOf(t, "sonnet")); got != "normal" {
		t.Errorf("sonnet 该走 normal（配置里改了），实际 %q", got)
	}
	env := a.BuildEnv(8899, "tok", facts{})
	// 值带 `[1m]`（见 TestInjectedModelsCarryTheOneMMarker）：断言里写成
	// 档位名 + 标记的拼法，而不是只比档位名——「档位对了、标记丢了」正是
	// 这条链上最可能出现的那种半对。
	if got := env["ANTHROPIC_DEFAULT_SONNET_MODEL"]; got != "normal"+oneM {
		t.Errorf("注入的该是改过的档位，实际 %q", got)
	}
	// 没改过的那些不受影响。
	if got := env["ANTHROPIC_DEFAULT_OPUS_MODEL"]; got != "normal"+oneM {
		t.Errorf("opus 没配过，该是缺省 normal，实际 %q", got)
	}
	if got := env["ANTHROPIC_DEFAULT_HAIKU_MODEL"]; got != "light"+oneM {
		t.Errorf("haiku 没配过，该是缺省 light，实际 %q", got)
	}
}

// TestInjectedModelsCarryTheOneMMarker：每个模型槽位注入出去的值都带 `[1m]`。
//
// 为什么这条值得单独锁（2026-09-28）：Claude Code 拿这个后缀判断「这个模型按
// 100 万上下文对待」，不带它就按 200k 假设、窗口远没到就自己 compact——而用户
// 配的可能是几百万上下文的模型。症状是「明明买了大窗口，用不上」，看配置一切
// 正常，所以只能靠这条测试守住。
//
// 两件事都要在它身上成立，缺一条就等于没有：
//
//  1. **动态模式与钉死模式都带**。动态模式注入档位名（`normal`），钉死模式
//     （`--profile=xx`）注入真实模型名（`glm-4-plus`）——后者是用户点名要优先
//     支持的用法，而两条路在代码里是两处拼接（BuildEnv 与 launch 的钉死分支），
//     这里锁的是 claude 描述符这一侧（另一侧在 launch_test.go）。
//  2. **窗口声明那两个变量不沾它**。它们是数字（`strconv.Itoa` 出来的），拼上
//     标记会写进去一个解析不了的值——失败的样子是「窗口声明静默失效、又回到
//     200k 假设」，看起来像这条注入根本不存在。
func TestInjectedModelsCarryTheOneMMarker(t *testing.T) {
	testkit.Sandbox(t)
	a := Agent()
	env := a.BuildEnv(8899, "tok", facts{})
	for _, s := range a.EnvSlots() {
		got := env[s.EnvVar]
		// subagent 是唯一声明了 `inherit` 的槽位（见 TestAClientSpecificValueSurvives），
		// 它的取值不指向一个模型，所以描述符故意不给它标记——那一格由下面那条测。
		if s.Name == "subagent" {
			continue
		}
		want := agentapi.TierOf(facts{}, s) + oneM
		if got != want {
			t.Errorf("槽位 %s 注入的该是 %q，实际 %q", s.Name, want, got)
		}
	}
	// 标记本身只有一份：描述符引用的必须就是数据面剥的那个常量。抄一份的
	// 症状是「注入没生效」，不是编译不过。
	if oneM != protocol.OneMMarker {
		t.Errorf("标记该是数据面那个常量，实际 %q", oneM)
	}
	// 窗口声明（那两个数字变量）不归本函数管，归 launch 的注入——那边那条
	// 测试断言它不带标记。
	if _, there := env[a.ContextWindowEnv]; there {
		t.Errorf("窗口声明不该在 BuildEnv 里，实际 %v", env[a.ContextWindowEnv])
	}
}

// TestWritingASlotReturnsItToTheDefault：把一行改回缺省 = 那个键消失，而不是留
// 一条同值记录。
//
// 留下同值记录有真实的代价：以后改缺省的人会发现自己的改动对一部分用户不生效，
// 而那些用户从没配过任何东西——那种「我改了、他们没变」是最难归因的一类。
func TestWritingASlotReturnsItToTheDefault(t *testing.T) {
	testkit.Sandbox(t)
	if err := WriteSlotTiers(map[string]string{"sonnet": "heavy"}); err != nil {
		t.Fatal(err)
	}
	if got := ReadSlotTiersOK(t, "sonnet"); got != "heavy" {
		t.Fatalf("写进去该读得回来，实际 %q", got)
	}
	// 改回缺省（mid）。
	if err := WriteSlotTiers(map[string]string{"sonnet": "mid"}); err != nil {
		t.Fatal(err)
	}
	ok, _ := ReadSlotTiers()
	if v, there := ok["sonnet"]; there {
		t.Errorf("改回缺省之后那个键该消失，实际还留着 %q", v)
	}
	if got := agentapi.TierOf(facts{}, slotOf(t, "sonnet")); got != "mid" {
		t.Errorf("改回缺省之后该走缺省 mid，实际 %q", got)
	}
}

// TestABadTierIsRefusedOnTheWayIn：写一个不存在的档位要被拒绝。
//
// 它会被原样注入成模型名。一个打错的档位名（`hevy`）在上游那边是「不存在的模型」，
// 症状是某些请求突然 400，而它跟配置之间隔着好几层——所以必须在入口拦住。
func TestABadTierIsRefusedOnTheWayIn(t *testing.T) {
	testkit.Sandbox(t)
	if err := WriteSlotTiers(map[string]string{"sonnet": "hevy"}); err == nil {
		t.Fatal("写一个不存在的档位该被拒绝")
	}
	if got := agentapi.TierOf(facts{}, slotOf(t, "sonnet")); got != "mid" {
		t.Errorf("被拒的写不该留下任何痕迹，实际走 %q", got)
	}
}

// TestABadTierInTheFileDoesNotBreakTakeover：手改坏的文件不能让注入跟着坏。
//
// state.json 是给人手改的，而这一格改坏的后果是「claude 起不来」——一个改错的
// 档位不该有这个后果。所以读的时候滤掉，回落到缺省，并且**报出来**（卡片上的
// why 会说），而不是安静地注入 `hevy`。
func TestABadTierInTheFileDoesNotBreakTakeover(t *testing.T) {
	testkit.Sandbox(t)
	writeState(t, map[string]any{SlotsKey: map[string]string{"sonnet": "hevy", "opus": "light"}})

	if got := agentapi.TierOf(facts{}, slotOf(t, "sonnet")); got != "mid" {
		t.Errorf("坏值该回落到缺省 mid，实际 %q", got)
	}
	if got := agentapi.TierOf(facts{}, slotOf(t, "opus")); got != "light" {
		t.Errorf("同一个文件里好的那一行该照常生效，实际 %q", got)
	}
	// 坏的那一行要在界面上说出来：不说的话它会一直静静地不起作用。
	if note := slotNote("sonnet", "mid"); note == "" {
		t.Error("坏值该在卡片上有一句话，现在什么都没说")
	}
	// 好的那一行也要有话说（用户改了，界面要看得见）。
	if note := slotNote("opus", "normal"); note == "" {
		t.Error("改过的那一行该说明它被改过")
	}
	// 没改过的行不多话。
	if note := slotNote("haiku", "light"); note != "" {
		t.Errorf("没改过的行不该有注解，实际 %q", note)
	}
}

// ReadSlotTiersOK 是测试用的小helper：读回来某一格的值。
func ReadSlotTiersOK(t *testing.T, slot string) string {
	t.Helper()
	ok, _ := ReadSlotTiers()
	return ok[slot]
}

// TestAClientSpecificValueSurvives：客户端自己的取值不能被当成打错的档位名滤掉。
//
// `subagent` 的说明里写着「set it to inherit to hand that back to per-slot
// resolution」——那是 **Claude Code 的**语义，newgate 只是原样注入这个字符串。第一版
// 只拿 domain.Roles 当判据，于是 `inherit` 界面上设不了、手改的还会被静默忽略：
// 说明里写着可以，而照做之后什么都没发生。
//
// 例外由客户端模块自己声明（agentapi.Slot.Also），所以这条同时锁住了那条边界——
// 内核与配置层不认识 `inherit` 这个词，它们只知道「这个槽位声明了它」。
func TestAClientSpecificValueSurvives(t *testing.T) {
	testkit.Sandbox(t)
	if err := WriteSlotTiers(map[string]string{"subagent": "inherit"}); err != nil {
		t.Fatalf("客户端自己的取值该收得下: %v", err)
	}
	a := Agent()
	if got := agentapi.TierOf(facts{}, slotOf(t, "subagent")); got != "inherit" {
		t.Errorf("该原样走 inherit，实际 %q", got)
	}
	if got := a.BuildEnv(8899, "tok", facts{})["CLAUDE_CODE_SUBAGENT_MODEL"]; got != "inherit" {
		t.Errorf("该原样注入，实际 %q", got)
	}
	// 这个例外只属于声明了它的那个槽位：别的槽位写 inherit 仍然是打错。
	if err := WriteSlotTiers(map[string]string{"opus": "inherit"}); err == nil {
		t.Error("opus 没声明 inherit，该被拒绝")
	}
	// 下拉里也要有它——不然用户看得到说明、设不了值。
	opts := allowedFor("subagent")
	if !contains(opts, "inherit") || !contains(opts, "mid") {
		t.Errorf("subagent 的取值该是档位 + inherit，实际 %v", opts)
	}
	if contains(allowedFor("opus"), "inherit") {
		t.Error("opus 的取值里不该有 inherit")
	}
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

// TestTheCardsAreLockedWhenTheClientIsNotThere：没装 claude 的机器上，这两张卡
// 整张锁灰。
//
// 用户的原话：「未安装的东西，直接全锁灰」。给 claude 配槽位、调分类器，在一台没有
// claude 的机器上一个字节都不会生效——卡片照常可编辑的话，用户会改完才发现白改。
//
// 判据落在**理由那句话**上，不是「有没有锁这个动作」：锁了但不说为什么，用户只会
// 以为界面坏了。同时锁的是**两张**卡——只锁一张的话，另一张还在那里能改。
func TestTheCardsAreLockedWhenTheClientIsNotThere(t *testing.T) {
	testkit.Sandbox(t)
	// 两个方向都要**自己造**：跑这条测试的人正在用 claude，所以「PATH 上有没有」
	// 取决于哪台机器——假定它没有，这条测试在开发机上必红。
	t.Setenv("PATH", t.TempDir()) // 一个空目录：什么都找不到
	if reason := lockReason(); reason == "" {
		t.Fatal("PATH 上没有 claude，这两张卡该锁灰")
	}
	for _, c := range []view.Concept{classifierConcept(), slotsConcept()} {
		if c.Locked == "" {
			t.Errorf("%s 该带一句锁灰的理由", c.ID)
		}
	}
	// 装上之后（造一个真的可执行的同名文件）不该再锁。
	dir := t.TempDir()
	bin := filepath.Join(dir, "claude")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	if reason := lockReason(); reason != "" {
		t.Errorf("PATH 上有 claude 了，不该再锁：%q", reason)
	}
}
