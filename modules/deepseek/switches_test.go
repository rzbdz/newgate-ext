package deepseek

import (
	"strings"
	"testing"

	"github.com/rzbdz/newgate/modules/config/domain"
	"github.com/rzbdz/newgate/modules/gateway/special"
	"github.com/rzbdz/newgate/modules/pluginmanager"
)

// reqWithOff 造一个 Claude Code × DeepSeek 的上下文，并把给定的开关点关掉。
func reqWithOff(t *testing.T, off ...string) *special.Request {
	t.Helper()
	cfg := pluginmanager.Config{Off: map[string]pluginmanager.Entry{}}
	for _, path := range off {
		cfg.Off[path] = pluginmanager.Entry{}
	}
	raw, err := cfg.Marshal()
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	r := claudeReq("deepseek-flash")
	r.State = &domain.State{ModuleConfig: map[string][]byte{pluginmanager.StateKey: raw}}
	return r
}

// threeHandsBody 一个能同时触发三手的请求：
//
//	第 2 手：assistant 没有 reasoning_content，而缓存里有原文 → 补
//	第 3 手：assistant 的 content[] 没有 thinking 块、思考开着、Anthropic 方言 → 补
//	第 4 手：最后一条 user 消息只有 tool_result → 追加 continue 指令
//
// 三手都挂在这**同一个 body** 上，正是为了验「关掉一手不影响另外两手」——
// 分开的 fixture 各有各的形状问题，验不出串扰。
const threeHandsBody = `{"thinking":{"type":"adaptive"},"messages":[` +
	`{"role":"user","content":[{"type":"text","text":"开始吧"}]},` +
	`{"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"Bash","input":{}}]},` +
	`{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"ok"}]}]}`

const seededReasoning = "上一轮上游真的给过的推理原文"

func applyThreeHands(t *testing.T, off ...string) string {
	t.Helper()
	seedCache("t1", seededReasoning)
	out, _, err := reasoning{}.Apply([]byte(threeHandsBody), reqWithOff(t, off...))
	if err != nil {
		t.Fatalf("Apply 报错: %v", err)
	}
	return string(out)
}

func TestThreeHandsFireWhenAllSwitchesOn(t *testing.T) {
	s := applyThreeHands(t)
	if !strings.Contains(s, `"reasoning_content":"`+seededReasoning+`"`) {
		t.Fatalf("第 2 手没补 reasoning_content:\n%s", s)
	}
	if !strings.Contains(s, `"thinking":"`+seededReasoning+`"`) {
		t.Fatalf("第 3 手没补 thinking 块:\n%s", s)
	}
	if !strings.Contains(s, `"text":`+mustJSON(toolLoopRebasePrompt)) {
		t.Fatalf("第 4 手没追加 continue 指令:\n%s", s)
	}
}

// TestSwitchesAreIndividuallyDisableable 是这个功能存在的理由：三手必须能**各自**
// 关掉。在此之前它们在 `newgate st` 那层只有一个名字 "deepseek"，「要么全要要么
// 全不要」——而排查时最常要做的恰恰是「只关掉第四手看看 400 还在不在」。
func TestSwitchesAreIndividuallyDisableable(t *testing.T) {
	cases := []struct {
		off                              string
		reasoning, thinkingBlock, tailOK bool
	}{
		{SwitchBackfillReasoning, false, true, true},
		{SwitchBackfillThinkingBlock, true, false, true},
		{SwitchTailShape, true, true, false},
		// 第 5 手只管 Responses 方言的 input[]，三手 fixture（messages 方言）
		// 上关掉它不该有任何影响——这条是「开关清单」跟着扩的那一格。
		{SwitchTailShapeResponses, true, true, true},
	}
	for _, c := range cases {
		t.Run(c.off, func(t *testing.T) {
			s := applyThreeHands(t, c.off)
			gotReasoning := strings.Contains(s, `"reasoning_content":"`+seededReasoning+`"`)
			gotThinking := strings.Contains(s, `"thinking":"`+seededReasoning+`"`)
			gotTail := strings.Contains(s, `"text":`+mustJSON(toolLoopRebasePrompt))

			if gotReasoning != c.reasoning {
				t.Errorf("第 2 手（reasoning_content）= %v，想要 %v\n%s", gotReasoning, c.reasoning, s)
			}
			if gotThinking != c.thinkingBlock {
				t.Errorf("第 3 手（thinking 块）= %v，想要 %v\n%s", gotThinking, c.thinkingBlock, s)
			}
			if gotTail != c.tailOK {
				t.Errorf("第 4 手（尾部指令）= %v，想要 %v\n%s", gotTail, c.tailOK, s)
			}
			// 关掉的那一手不许连累别的：上面三个期望里恰好只有一个是 false，
			// 所以这条断言等价于「串扰 = 失败」。
		})
	}
}

// TestNilStateMeansEveryHandRuns：快照缺席时一律按「没关」处理。
//
// 方向不能反：开关点默认就是开的，一个 nil 快照把补丁关掉就是 fail-closed——
// 而 fail-closed 的后果正是这些补丁当初要修的那个 400。旧调用面（单测、系统层
// 直接构造 Request 的地方）State 常常是 nil。
func TestNilStateMeansEveryHandRuns(t *testing.T) {
	seedCache("t1", seededReasoning)
	r := claudeReq("deepseek-flash") // 注意：不设 State
	out, _, err := reasoning{}.Apply([]byte(threeHandsBody), r)
	if err != nil {
		t.Fatalf("Apply 报错: %v", err)
	}
	s := string(out)
	if !strings.Contains(s, `"reasoning_content":"`+seededReasoning+`"`) ||
		!strings.Contains(s, `"thinking":"`+seededReasoning+`"`) ||
		!strings.Contains(s, `"text":`+mustJSON(toolLoopRebasePrompt)) {
		t.Fatalf("State 为 nil 时三手都该照常跑（fail-open）:\n%s", s)
	}
}

// TestSwitchesOfOtherModulesDoNotLeak：别的模块的开关点同名不同前缀时不能误伤。
func TestSwitchesOfOtherModulesDoNotLeak(t *testing.T) {
	s := applyThreeHands(t, "claudecode-deepseek.inject-thinking")
	if !strings.Contains(s, `"reasoning_content":"`+seededReasoning+`"`) ||
		!strings.Contains(s, `"text":`+mustJSON(toolLoopRebasePrompt)) {
		t.Fatalf("关掉别的模块的开关点，不该影响本模块的三手:\n%s", s)
	}
}

// TestResponsesTailShapeSwitchDisablesHand5：第 5 手也能**单独**关掉。
//
// 它和第 4 手管的是同一个误报的两个方言，但动的是两个数组（messages 的
// content[] vs input[]），所以各有一个开关点——排查时经常要「只关 Responses
// 那一手看看 400 还在不在」。
func TestResponsesTailShapeSwitchDisablesHand5(t *testing.T) {
	body := []byte(`{"model":"deepseek-flash","input":[` +
		`{"type":"function_call","call_id":"call_faedac561213487ebeea7731","name":"bash","arguments":"{}"},` +
		`{"type":"function_call_output","call_id":"call_faedac561213487ebeea7731","output":"ok"}]}`)

	on, notes, err := reasoning{}.Apply(body, reqWithOff(t))
	if err != nil {
		t.Fatalf("Apply 报错: %v", err)
	}
	if !strings.Contains(string(on), mustJSON(toolLoopRebasePrompt)) {
		t.Fatalf("第 5 手（开关开着）没追加 continue 指令:\n%s", on)
	}
	if !containsNote(notes, "Responses input[] ends on a function_call_output") {
		t.Fatalf("改了东西却没回报 notes: %v", notes)
	}

	off, notesOff, err := reasoning{}.Apply(body, reqWithOff(t, SwitchTailShapeResponses))
	if err != nil {
		t.Fatalf("Apply 报错: %v", err)
	}
	if string(off) != string(body) {
		t.Fatalf("开关关着还是改了请求:\n%s", off)
	}
	if containsNote(notesOff, "Responses input[] ends on a function_call_output") {
		t.Fatalf("开关关着却报了第 5 手 note: %v", notesOff)
	}
}
