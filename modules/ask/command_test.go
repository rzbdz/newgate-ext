package ask

import (
	"strings"
	"testing"

	cliapi "github.com/rzbdz/newgate-ext/modules/cli/extension"
)

// 这一条锁的是一个**发出去就会答错问题**的坑：`cliapi.Positional` 只跳过以 `-`
// 开头的参数，而带值选项的**值**不带 `-`。于是 `newgate ask --tier light 今天天气`
// 里，`light` 会被当成第一个位置参数——用户问的是「今天天气」，发出去的却是
// 「light 今天天气」。
//
// 它不会报错、不会红，只会**答得莫名其妙**，所以必须钉住。
func TestLeftoverDropsFlagValues(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"没有选项", []string{"今天", "天气"}, "今天 天气"},
		{"空格形式的选项值不算问题的一部分",
			[]string{"--tier", "light", "今天", "天气"}, "今天 天气"},
		{"等号形式的选项", []string{"--tier=light", "今天"}, "今天"},
		{"短选项", []string{"-t", "light", "今天"}, "今天"},
		{"几个选项混着来",
			[]string{"--tier", "light", "--profile", "ds", "--system", "只回中文", "今天"},
			"今天"},
		{"-- 之后一律是问题本身（哪怕长得像选项）",
			[]string{"--tier", "light", "--", "--help"}, "--help"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := strings.Join(leftover(c.args), " "); got != c.want {
				t.Errorf("leftover(%q) = %q，想要 %q", c.args, got, c.want)
			}
		})
	}
}

func TestParsePositiveInt(t *testing.T) {
	for _, ok := range []string{"1", "4096", "100000"} {
		n, err := parsePositiveInt(ok)
		if err != nil || n <= 0 {
			t.Errorf("parsePositiveInt(%q) = %d, %v；应当收下", ok, n, err)
		}
	}
	// 0 与负数与垃圾都当场拒——发出去只会换来上游一个看不懂的 400。
	for _, bad := range []string{"0", "-1", "很多", "12x", ""} {
		if _, err := parsePositiveInt(bad); err == nil {
			t.Errorf("parsePositiveInt(%q) 应当报错", bad)
		}
	}
}

// 这一条锁的是**给脚本用的那个契约**：正文一个字节都不许跑到 stderr，思维链一个
// 字节都不许跑到 stdout。deepseek / glm 的一发回答里思维链常常比正文长好几倍，
// 混进去的话 `answer=$(newgate ask …)` 拿到的东西没法用。
//
// 同时锁「认不出来的事件不能让整发断掉」：上游加字段、换事件类型都是常事，而
// 「打了一半就停」是最不该有的失败方式。
func TestStreamRoutesTextAndThinkingApart(t *testing.T) {
	sse := strings.Join([]string{
		"event: message_start",
		`data: {"type":"message_start","message":{"id":"x"}}`,
		"",
		`data: {"type":"content_block_delta","delta":{"type":"thinking_delta","thinking":"先想"}}`,
		"",
		// 没见过的类型：跳过，但不能断。
		`data: {"type":"something_new","payload":{"a":1}}`,
		"",
		`data: {"type":"content_block_delta","delta":{"type":"thinking_delta","thinking":"再想"}}`,
		"",
		`data: {"type":"content_block_delta","delta":{"type":"text_delta","text":"你好"}}`,
		"",
		`data: {"type":"content_block_delta","delta":{"type":"text_delta","text":"，世界"}}`,
		"",
		// 不是 JSON 的一行：同样不能断。
		"data: 这不是 JSON",
		"",
		`data: {"type":"message_stop"}`,
		"",
	}, "\n")

	var out, thinking strings.Builder
	if err := streamAnswer(strings.NewReader(sse), &out, &thinking); err != nil {
		t.Fatalf("streamAnswer 出错: %v", err)
	}
	if got := out.String(); got != "你好，世界" {
		t.Errorf("stdout 该只有正文，实际 %q", got)
	}
	if got := thinking.String(); got != "先想再想" {
		t.Errorf("stderr 该只有思维链，实际 %q", got)
	}
}

// 上游报错是一条**事件**，不是「流断了」：它必须变成一句能看见的错误返回，
// 而不是安静地打完剩下的一点点然后退出 0。
func TestStreamSurfacesAnErrorEvent(t *testing.T) {
	sse := `data: {"type":"error","error":{"message":"upstream said no"}}` + "\n"
	var out, thinking strings.Builder
	err := streamAnswer(strings.NewReader(sse), &out, &thinking)
	if err == nil {
		t.Fatal("error 事件应当变成错误返回")
	}
	if !strings.Contains(err.Error(), "upstream said no") {
		t.Errorf("错误里该带上上游那句话，实际 %v", err)
	}
}

// Unstyled 必须在：模型的正文不是 newgate 的版式，不声明的话界面的版式审计会
// 把上游的每一句话都当成「排版不合规」。
func TestAskIsUnstyled(t *testing.T) {
	var c cliapi.Command = askCommand{}
	if _, ok := c.(cliapi.Unstyled); !ok {
		t.Fatal("askCommand 必须实现 cliapi.Unstyled")
	}
	if _, ok := c.(cliapi.Documented); !ok {
		t.Fatal("askCommand 必须实现 cliapi.Documented（否则它不会出现在 --help 里）")
	}
}
