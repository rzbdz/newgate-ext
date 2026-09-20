package claudecode

import (
	"testing"

	agentapi "github.com/rzbdz/newgate/modules/confighook"
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
	if got := env["ANTHROPIC_DEFAULT_SONNET_MODEL"]; got != "normal" {
		t.Errorf("注入的该是改过的档位，实际 %q", got)
	}
	// 没改过的那些不受影响。
	if got := env["ANTHROPIC_DEFAULT_OPUS_MODEL"]; got != "normal" {
		t.Errorf("opus 没配过，该是缺省 normal，实际 %q", got)
	}
	if got := env["ANTHROPIC_DEFAULT_HAIKU_MODEL"]; got != "light" {
		t.Errorf("haiku 没配过，该是缺省 light，实际 %q", got)
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
