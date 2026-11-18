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

// Unstyled says this run's output **is not newgate's
// layout**: it is the model's body text, printed verbatim.
//
// Without this, the UI's layout audit would treat every sentence of the model
// as "non-compliant formatting" -- and those words come from the upstream;
// we are in no position to lay them out for it (see cliapi.Unstyled).
func (askCommand) Unstyled([]string) bool { return true }

// defaultTier is the tier used when `--tier` is not given.
//
// normal rather than the cheapest light: `normal` is the **main tier** this product
// defines (see the tier ladder in docs/04-configuration.md), and `ask`'s default
// expectation is "ask a normal question". People who want to save money will write
// `--tier light` themselves; the reverse (default to the cheapest, and require asking
// explicitly for a good one) would make first-time users think this tool is dumb.
const defaultTier = "normal"

// defaultMaxTokens is the cap used when `--max-tokens` is not given. The Anthropic
// dialect **must** carry this field, so it is not an option that can be omitted.
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
		// **Do not bring the proxy up itself**: `ask` means "go through the proxy that is
		// running", and quietly starting a daemon would turn one question into a state change
		// (takeover, port, pidfile). Saying clearly what to run beats deciding for the user.
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
		// One-off profile override (see /p/<profile> in docs/05-gateway.md): this
		// request takes that chain and **does not touch global state**. Trying
		// a chain with `ask` should not change the whole machine's default.
		req.URL.Path = "/p/" + profile + "/v1/messages"
	}

	// No overall timeout: one long answer legitimately takes minutes. To interrupt, Ctrl-C -- it drops
	// the connection, and the proxy handles client cancellation (no failure recorded, no delisting).
	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return host.Die(69, i18n.T("cannot reach the proxy at {url}: {err}",
			i18n.A{"url": url, "err": err}))
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		// Upstream/proxy errors are carried out as is: that sentence is the only thing explaining
		// "why there was no answer", and it is often the upstream's error text (see "never silent").
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<10))
		return host.Die(69, i18n.T("the proxy answered HTTP {status}: {body}",
			i18n.A{"status": resp.StatusCode, "body": strings.TrimSpace(string(raw))}))
	}

	if err := streamAnswer(resp.Body, os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr)
		return host.Die(70, i18n.T("the answer broke off: {err}", i18n.A{"err": err}))
	}
	// Add a newline after the body: streamed body text carries no trailing
	// newline, and without it the shell prompt sticks to the last sentence.
	fmt.Println()
	return 0
}

// ---------- Request and stream ----------

type askMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type askRequest struct {
	// Model is the **tier name** (`light` / `normal` / …), not a model name -- resolving
	// it to which provider's which model is the proxy's job (see the package comment).
	Model     string       `json:"model"`
	MaxTokens int          `json:"max_tokens"`
	Stream    bool         `json:"stream"`
	System    string       `json:"system,omitempty"`
	Messages  []askMessage `json:"messages"`
}

// streamAnswer prints as it reads: the body goes to out, the reasoning chain goes to thinking.
//
// # Why the reasoning chain takes another stream
//
// Because **this command is for scripts**: `answer=$(newgate ask …)` must get clean body text.
// The reasoning chain is for humans (who want to know how the model got around), so it goes to
// stderr -- still visible on a terminal, while `$( )` captures not one byte of it into the body.
//
// On reasoning models this is a real difference: in one answer from deepseek / glm the reasoning
// chain is often several times longer than the body (measured; see docs/06-reasoning.md).
//
// Unknown events are always skipped, never allowed to break the whole run: the upstream
// adding fields or changing event types is routine, and "the answer stopped halfway
// because of an event name we had never seen" is the failure mode we least want.
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

// ---------- Little helpers ----------

// flagsWithValues are the "followed by a value" ones among **this command's own** options.
//
// They are stripped to get the prompt: `cliapi.Positional` only skips arguments starting with `-`,
// and the `normal` in `--tier normal` has no `-`, so it would be taken as the first positional
// -- `newgate ask --tier light what is the weather today` would send `light` out as the question.
var flagsWithValues = [][]string{
	{"--tier", "-t"}, {"--profile", "--preset"}, {"--system"}, {"--max-tokens"},
}

// leftover returns the positional arguments left after stripping this command's own options.
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
				i++ // skip its value too (`--tier normal`)
			}
			continue
		}
		out = append(out, a)
	}
	return out
}

// flagOr takes a value-taking option, or fallback when absent.
func flagOr(args []string, fallback string, names ...string) string {
	if v := cliapi.FlagValue(args, names...); v != "" {
		return v
	}
	return fallback
}

// matchesAnyFlag says whether this argument is "one of this
// command's value-taking options" (including the `--x=y` form).
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

// parsePositiveInt accepts only positive integers: `--max-tokens 0`
// and `--max-tokens many` should both error on the spot, rather than
// being sent and getting an incomprehensible 400 from the upstream.
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

// readPipedStdin reads stdin to the end when it is
// **a pipe/redirect**; on a terminal it reads nothing.
//
// The test is "is stdin a character device", not "is there something
// to read": ReadAll on a terminal would wait forever, and that is
// exactly the case of "the user gave no prompt, so we should error".
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

// proxyPort is the port the proxy listens on (the one from config, or domain.ProxyPort if unset).
func proxyPort() int {
	if p := store.LoadState().Port; p > 0 {
		return p
	}
	return domain.ProxyPort
}
