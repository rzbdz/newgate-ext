package ask

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	cliapi "github.com/rzbdz/newgate-ext/modules/cli/extension"
	"github.com/rzbdz/newgate-ext/modules/config/domain"
	"github.com/rzbdz/newgate-ext/modules/config/store"
	i18n "github.com/rzbdz/newgate/lib/i18n"
)

type askCommand struct{}

var (
	_ cliapi.Command    = askCommand{}
	_ cliapi.Documented = askCommand{}
	_ cliapi.Unstyled   = askCommand{}
)

func (askCommand) Names() []string { return []string{"ask"} }

func (askCommand) Help() cliapi.HelpLine {
	return cliapi.HelpLine{Section: cliapi.SectionRunOnce, Rank: rankAsk,
		Usage:   i18n.T("ask [--tier <tier>] [--profile <name>] <prompt…>", nil),
		Summary: i18n.T("send one prompt through the proxy and print the answer", nil)}
}

// Unstyled 说这一次的输出**不是 newgate 的版式**：它是模型的正文字，逐字打出去。
//
// 少了这一条，界面的版式审计会把模型的每一句话都当成「排版不合规」——而那些字是
// 上游给的，我们没有资格替它排版（见 cliapi.Unstyled）。
func (askCommand) Unstyled([]string) bool { return true }

// defaultTier 是没给 `--tier` 时用的档位。
//
// 选 normal 而不是最便宜的 light：`normal` 是这个产品定义的**主力档位**
// （见 docs/04-configuration.md 的档位阶梯），而 `ask` 的默认期待是「问一句正常
// 问题」。想省钱的人会自己写 `--tier light`，反过来（默认给最便宜的、要用好的
// 再显式要）会让第一次用的人以为这个工具很笨。
const defaultTier = "normal"

// defaultMaxTokens 是没给 `--max-tokens` 时的上限。Anthropic 方言**必须**带这个
// 字段，所以它不是一个可以省略的选项。
const defaultMaxTokens = 4096

func (askCommand) Run(host cliapi.Host, args []string) int {
	tier := flagOr(args, defaultTier, "--tier", "-t")
	profile := flagOr(args, "", "--profile", "--preset")
	maxTokens := defaultMaxTokens
	if v := flagOr(args, "", "--max-tokens"); v != "" {
		n, err := parsePositiveInt(v)
		if err != nil {
			return host.Die(64, i18n.T("--max-tokens wants a positive number, got {value}",
				i18n.A{"value": v}))
		}
		maxTokens = n
	}
	system := flagOr(args, "", "--system")

	prompt := strings.TrimSpace(strings.Join(leftover(args), " "))
	if prompt == "" {
		piped, err := readPipedStdin()
		if err != nil {
			return host.Die(74, i18n.T("cannot read the prompt from stdin: {err}", i18n.A{"err": err}))
		}
		prompt = strings.TrimSpace(piped)
	}
	if prompt == "" {
		return host.Die(64, i18n.T("nothing to ask — write a prompt, or pipe one in", nil))
	}

	if !host.DaemonRunning() {
		// **不自己把代理拉起来**：`ask` 的语义是「走那个正在跑的代理」，而悄悄起
		// 一个守护进程会把一次提问变成一次状态改变（接管、端口、pidfile）。
		// 说清楚要跑什么，比替用户决定强。
		return host.Die(69, i18n.T("the proxy is not running — start it with newgate start", nil))
	}

	body, err := json.Marshal(askRequest{
		Model: tier, MaxTokens: maxTokens, Stream: true, System: system,
		Messages: []askMessage{{Role: "user", Content: prompt}},
	})
	if err != nil {
		return host.Die(70, i18n.T("cannot build the request: {err}", i18n.A{"err": err}))
	}

	url := fmt.Sprintf("http://127.0.0.1:%d/v1/messages", proxyPort())
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return host.Die(70, i18n.T("cannot build the request: {err}", i18n.A{"err": err}))
	}
	req.Header.Set("Content-Type", "application/json")
	if profile != "" {
		// 单次 profile 覆盖（见 docs/05-gateway.md 的 /p/<profile>）：这一发走那条链，
		// **不动全局状态**。用 `ask` 试一条链不该把整个机器的默认改掉。
		req.URL.Path = "/p/" + profile + "/v1/messages"
	}

	// 不设整体超时：一次长回答本来就要几分钟。要中断就 Ctrl-C——它会把连接断掉，
	// 而代理那边对客户端取消是有处理的（不记失败、不摘牌）。
	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return host.Die(69, i18n.T("cannot reach the proxy at {url}: {err}",
			i18n.A{"url": url, "err": err}))
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		// 上游/代理的错误原样带出来：那句话是唯一能说明「为什么没答」的东西，
		// 而它常常是上游的报错原文（见「不静默」）。
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<10))
		return host.Die(69, i18n.T("the proxy answered HTTP {status}: {body}",
			i18n.A{"status": resp.StatusCode, "body": strings.TrimSpace(string(raw))}))
	}

	if err := streamAnswer(resp.Body, os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr)
		return host.Die(70, i18n.T("the answer broke off: {err}", i18n.A{"err": err}))
	}
	// 正文之后补一个换行：流式的正文自己不带结尾换行，不补的话 shell 提示符会
	// 贴在最后一句后面。
	fmt.Println()
	return 0
}

// ---------- 请求与流 ----------

type askMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type askRequest struct {
	// Model 是**档位名**（`light` / `normal` / …），不是模型名——解析成哪家哪个
	// 模型是代理的事（见包注释）。
	Model     string       `json:"model"`
	MaxTokens int          `json:"max_tokens"`
	Stream    bool         `json:"stream"`
	System    string       `json:"system,omitempty"`
	Messages  []askMessage `json:"messages"`
}

// streamAnswer 边读边打：正文进 out，思维链进 thinking。
//
// # 为什么思维链走另一个流
//
// 因为**这一条命令是给脚本用的**：`answer=$(newgate ask …)` 必须拿到干净的正文。
// 思维链是给人看的（想知道模型怎么绕过来的），所以它走 stderr——人在终端上照样
// 看得见，被 `$( )` 捕获时则一个字节都不进正文。
//
// 这在 reasoning 模型上是实打实的差别：deepseek / glm 的一发回答里，思维链常常
// 比正文长好几倍（实测见 docs/06-reasoning.md）。
//
// 认不出来的事件一律跳过，不让整发断掉：上游加字段、换事件类型都是常事，而
// 「因为一个没见过的事件名，回答打了一半就停了」是最不该有的失败方式。
func streamAnswer(r io.Reader, out, thinking io.Writer) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64<<10), 8<<20)
	for sc.Scan() {
		payload, ok := strings.CutPrefix(strings.TrimSpace(sc.Text()), "data:")
		if !ok {
			continue
		}
		payload = strings.TrimSpace(payload)
		if payload == "" || payload == "[DONE]" {
			continue
		}
		var ev streamEvent
		if err := json.Unmarshal([]byte(payload), &ev); err != nil {
			continue
		}
		switch ev.Type {
		case "content_block_delta":
			switch ev.Delta.Type {
			case "text_delta":
				if _, err := io.WriteString(out, ev.Delta.Text); err != nil {
					return err
				}
			case "thinking_delta":
				if _, err := io.WriteString(thinking, ev.Delta.Thinking); err != nil {
					return err
				}
			}
		case "error":
			if ev.Error != nil {
				return errors.New(ev.Error.Message)
			}
		}
	}
	return sc.Err()
}

type streamEvent struct {
	Type  string `json:"type"`
	Delta struct {
		Type     string `json:"type"`
		Text     string `json:"text"`
		Thinking string `json:"thinking"`
	} `json:"delta"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

// ---------- 小工具 ----------

// flagsWithValues 是**本命令自己的**选项里那些「后面跟一个值」的。
//
// 剥掉它们是为了拿到 prompt：`cliapi.Positional` 只跳过以 `-` 开头的参数，而
// `--tier normal` 里的 `normal` 不带 `-`，于是它会被当成第一个位置参数——
// `newgate ask --tier light 今天天气如何` 就会把 `light` 当成问题问出去。
var flagsWithValues = [][]string{
	{"--tier", "-t"}, {"--profile", "--preset"}, {"--system"}, {"--max-tokens"},
}

// leftover 返回「剥掉本命令自己的选项之后」剩下的位置参数。
func leftover(args []string) []string {
	out := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			out = append(out, args[i+1:]...)
			break
		}
		if matchesAnyFlag(a, flagsWithValues) {
			if !strings.Contains(a, "=") {
				i++ // 连同它的值一起跳过（`--tier normal`）
			}
			continue
		}
		out = append(out, a)
	}
	return out
}

// flagOr 取一个带值选项，取不到就用 fallback。
func flagOr(args []string, fallback string, names ...string) string {
	if v := cliapi.FlagValue(args, names...); v != "" {
		return v
	}
	return fallback
}

// matchesAnyFlag 说这个参数是不是「本命令的某个带值选项」（含 `--x=y` 写法）。
func matchesAnyFlag(a string, groups [][]string) bool {
	for _, names := range groups {
		for _, n := range names {
			if a == n || strings.HasPrefix(a, n+"=") {
				return true
			}
		}
	}
	return false
}

// parsePositiveInt 只收正整数：`--max-tokens 0` 与 `--max-tokens 很多` 都该当场
// 报错，而不是发出去让上游回一个看不懂的 400。
func parsePositiveInt(s string) (int, error) {
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0, errors.New("not a number")
		}
		n = n*10 + int(r-'0')
	}
	if n <= 0 {
		return 0, errors.New("not positive")
	}
	return n, nil
}

// readPipedStdin 在**管道/重定向**时把 stdin 读完；终端上则什么都不读。
//
// 判据是「stdin 是不是字符设备」而不是「有没有东西可读」：在终端上 ReadAll 会
// 一直等下去，而那正是「用户没给 prompt、我们该报错」的情形。
func readPipedStdin() (string, error) {
	st, err := os.Stdin.Stat()
	if err != nil {
		return "", err
	}
	if st.Mode()&os.ModeCharDevice != 0 {
		return "", nil
	}
	raw, err := io.ReadAll(io.LimitReader(os.Stdin, 4<<20))
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

// proxyPort 是代理监听的端口（配置里的那个，没配就是 domain.ProxyPort）。
func proxyPort() int {
	if p := store.LoadState().Port; p > 0 {
		return p
	}
	return domain.ProxyPort
}
