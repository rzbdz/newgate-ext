package codex

import (
	"io/ioutil"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/rzbdz/newgate/lib/i18n"
	"github.com/rzbdz/newgate/modules/config/paths"
	"github.com/rzbdz/newgate/modules/config/store"
	agentapi "github.com/rzbdz/newgate/modules/confighook"
)

// 本文件是 **codex 的接管**：改写 `~/.codex/config.toml`，把它指到本地代理。
//
// # 为什么是 TOML 而不是环境变量
//
// 另外两家客户端都有 env 入口（claude 是 ANTHROPIC_BASE_URL，opencode 是
// opencode.json），codex 没有：它的 base_url、provider、模型名**只认那份 TOML**
// （`-c key=value` 是命令行覆盖，不是常驻配置）。所以 codex 走的是 MechConfig
// 那条路——与 opencode 同机制，而不是 shim。
//
// # 为什么是字节手术，不是 TOML 往返
//
// `~/.codex/config.toml` 是**人手写的**，里面有注释、有 `[projects."/root"]` 这种
// 一路长出来的段、还有 `[notice.model_migrations]` 这种它自己记的账。做一次
// 解析→序列化的往返，这些全会消失——而「配置里我写的注释没了」正是那种用户会
// 记很久、我们却不会因此变红的损失（与内核那条「请求体不做 JSON 往返」同源）。
//
// 所以这里只做三件事，每件都只碰它该碰的那几行：
//
//	顶层 `model`           → 档位名（语义档位，见 docs/01-product.md）
//	顶层 `model_provider`  → newgate
//	`[model_providers.newgate]` 整段 → 指向本地代理

// ProviderID 是我们在 config.toml 里占的那个 provider 名。
//
// 用 "newgate" 而不是 "openai" 之类的名字：codex 对**内置** provider id 有保留
// （实测它的报错原文是 `model_providers contains reserved built-in provider IDs`），
// 而 newgate 不在保留名单里。
const ProviderID = "newgate"

// providerTable 是那段配置的表头。IsTakenOver 与改写都以它为准——**一处定义**，
// 免得「判断是否接管过」与「改写哪一段」对不上（那会导致重复接管时反复插入）。
const providerTable = "[model_providers." + ProviderID + "]"

// ConfigPath 是 codex 读的那份文件。
//
// `$CODEX_HOME` 优先，因为那是 codex 自己的规矩（它内部就是
// `os.environ.get("CODEX_HOME", "~/.codex")`）。写死 `~/.codex` 会让「用
// CODEX_HOME 隔离一次会话」的人接管到别处去：他改的那份没动，我们动的那份他
// 没在用——而两边看起来都成功了。
func ConfigPath() string {
	if h := strings.TrimSpace(getenv("CODEX_HOME")); h != "" {
		return filepath.Join(h, "config.toml")
	}
	return filepath.Join(paths.Home(), ".codex", "config.toml")
}

// getenv 是这一层唯一的间接：测试要能把 CODEX_HOME 指到沙箱，而**不能**动真实
// 用户的 ~/.codex（那是「测试不碰真实配置」那条规矩的落点）。
var getenv = os.Getenv

// Takeover 是交给 confighook 的那份接管。
type Takeover struct {
	// Tier 是这个客户端此刻该用的档位（空 = 用描述符里的缺省）。
	//
	// 它由**调用方**在 Apply 那一刻求值（见 module.go 的 tierOf）：槽位映射是
	// 用户可配的，而配置读一次就定下来的话，用户改完槽位会发现 codex 那边没动。
	Tier func() string
	// ContextWindow 是这个客户端此刻该声明的窗口（0 = 不写这一行）。
	//
	// 为什么要写它：codex 不认识我们那些档位名，会打一句
	// `Model metadata for 'X' not found. Defaulting to fallback metadata`，
	// 而那份兜底元数据决定它**什么时候自动 compact**——猜小了会让长会话被过早
	// 截断，那是用户直接能感觉到的行为变化。档位映射到真模型这件事只有 newgate
	// 知道，所以这个数只能由我们告诉它。
	ContextWindow func() int
}

var _ agentapi.ConfigTakeover = Takeover{}

func (Takeover) Targets() []string { return []string{ConfigPath()} }

// IsTakenOver 判据是**那段表头在不在**，不是「文件里出现过 newgate 这个词」：
// 后者会被用户自己写的一句注释命中，于是 `newgate off codex` 会去还原一份我们从
// 没改过的文件（覆盖掉他刚写的东西）。
func (Takeover) IsTakenOver(target string) bool {
	b, err := ioutil.ReadFile(target)
	if err != nil {
		return false
	}
	return hasMarker(b)
}

func hasMarker(b []byte) bool {
	return strings.Contains(string(b), providerTable)
}

// Apply 改写目标文件。找不到那个文件时**不创建**，报一句让人能行动的话。
//
// 为什么不像 opencode 那样「不存在就跳过」：opencode 的目标里有几份是可选的
// （omo 那份可以没有），而 codex 的 config.toml 是它唯一的位置——一台装了 codex
// 的机器上它一定在（codex 自己会建）。不在 = 这台机器上根本没有 codex，
// 那正是该说出来的事，而不是安静地跳过。
func (t Takeover) Apply(port int) ([]*agentapi.TakeoverReport, error) {
	target := ConfigPath()
	rep := &agentapi.TakeoverReport{File: target}

	raw, err := ioutil.ReadFile(target)
	if err != nil {
		return []*agentapi.TakeoverReport{{
			File:    target,
			Skipped: i18n.T("no such file — is codex installed on this machine?", nil),
		}}, nil
	}
	if err := agentapi.BackupFile(target, hasMarker); err != nil {
		return []*agentapi.TakeoverReport{rep}, err
	}

	tier := "normal"
	if t.Tier != nil {
		if v := t.Tier(); v != "" {
			tier = v
		}
	}
	window := 0
	if t.ContextWindow != nil {
		window = t.ContextWindow()
	}

	out, reps := rewrite(string(raw), port, tier, window)
	rep.Rewrites = reps
	return []*agentapi.TakeoverReport{rep}, agentapi.WriteAtomic(target, []byte(out), 0o600)
}

// Restore 从 original/ 还原。逃生舱：还原之后删掉 newgate，codex 照常能用。
func (Takeover) Restore() ([]string, error) {
	target := ConfigPath()
	done, err := agentapi.RestoreFile(target)
	if err != nil || !done {
		return nil, err
	}
	return []string{target}, nil
}

// ---------- TOML 手术 ----------

// rewrite 是**纯函数**：给一份 TOML 原文，还一份改过的 + 一份「改了哪几处」的
// 账。写成纯函数是为了它能被单测直接钉住——接管这件事最容易坏的地方就是
// 「改错了行」或「多插了一段」，而这两种错在真机上都要等用户发现。
func rewrite(src string, port int, tier string, window int) (string, []string) {
	lines := strings.Split(src, "\n")
	var reps []string

	// ---- 1. 顶层标量 ----
	//
	// 只认**第一个表头之前**的键：`[projects."/root"]` 底下也有 `model` 级别的
	// 短键（`trust_level` 就是），而 TOML 里同名的键在各表里互不相干——认错了
	// 表就会去改一个跟模型毫无关系的值。
	want := map[string]string{
		"model":          quote(tier),
		"model_provider": quote(ProviderID),
	}
	if window > 0 {
		want["model_context_window"] = strconv.Itoa(window)
	}
	seen := map[string]bool{}
	inTable := false
	for i, ln := range lines {
		if strings.HasPrefix(strings.TrimSpace(ln), "[") {
			inTable = true
			continue
		}
		if inTable {
			continue
		}
		k, ok := topLevelKey(ln)
		if !ok {
			continue
		}
		v, wanted := want[k]
		if !wanted {
			continue
		}
		seen[k] = true
		if strings.TrimSpace(ln) == k+" = "+v {
			continue // 已经是我们写的那个值，别记一笔假账
		}
		reps = append(reps, k+" -> "+v)
		lines[i] = k + " = " + v
	}

	// 缺的键补在**文件最前面**：顶层键写在表头之后就会变成那个表的键（TOML 没有
	// 「回到顶层」的写法），所以补的位置只有第一个表头之前。插在最前面而不是
	// 第一个表头之前，是因为后者要处理「前面有没有空行」这类排版细节，而排在最上面
	// 的代价只是用户的注释往下挪了一行。
	var head []string
	for _, k := range []string{"model", "model_provider"} {
		if seen[k] {
			continue
		}
		head = append(head, k+" = "+want[k])
		reps = append(reps, k+" -> "+want[k])
	}
	if len(head) > 0 {
		lines = append(head, lines...)
	}
	if window > 0 && !seen["model_context_window"] {
		// 插在 model_provider 后面而不是文件最前面：`model_provider` 此刻一定在
		// 顶层（缺的刚补在最前面，有的本来就在第一个表头之前），所以这一行也落在
		// 顶层。连着 model 那两行放，读的人一眼看得出这三行是一件事。
		lines = insertAfterKey(lines, "model_provider", "model_context_window = "+strconv.Itoa(window))
		reps = append(reps, "model_context_window -> "+strconv.Itoa(window))
	}

	// ---- 2. provider 段 ----
	block := providerBlock(port)
	start, end := -1, -1
	for i, ln := range lines {
		if strings.TrimSpace(ln) == providerTable {
			start = i
			end = i + 1
			for end < len(lines) && !strings.HasPrefix(strings.TrimSpace(lines[end]), "[") {
				end++
			}
			break
		}
	}
	if start >= 0 {
		out := make([]string, 0, len(lines)+len(block))
		out = append(out, lines[:start]...)
		out = append(out, block...)
		out = append(out, lines[end:]...)
		lines = out
		reps = append(reps, i18n.T("{table} rewritten", i18n.A{"table": providerTable}))
	} else {
		// 追加到末尾：表头之前先空一行，免得跟上一个表的最后一个键粘在一起。
		if n := len(lines); n > 0 && strings.TrimSpace(lines[n-1]) != "" {
			lines = append(lines, "")
		}
		lines = append(lines, block...)
		reps = append(reps, i18n.T("{table} added", i18n.A{"table": providerTable}))
	}

	out := strings.Join(lines, "\n")
	// 原文以换行结尾时，结果也要（追加那段之后，末尾那个空元素会跑到中间去）。
	// 少了它，文件在一次接管之后会多出「\ No newline at end of file」这条无谓的
	// diff——用户 review 我们的改动时会以为我们碰了最后一行。
	if strings.HasSuffix(src, "\n") && !strings.HasSuffix(out, "\n") {
		out += "\n"
	}
	return out, reps
}

// insertAfterKey 在某个顶层键那一行之后插一行。
//
// 找不到那个键就追加到末尾——那是**兜底**而不是正常路径：调用它之前我们刚保证
// 过那个键一定存在（缺的补在最前面）。
func insertAfterKey(lines []string, key, line string) []string {
	for i, ln := range lines {
		if k, ok := topLevelKey(ln); ok && k == key {
			out := make([]string, 0, len(lines)+1)
			out = append(out, lines[:i+1]...)
			out = append(out, line)
			return append(out, lines[i+1:]...)
		}
	}
	return append(lines, line)
}

// topLevelKey 从一行里取出 `键 = 值` 的键。
//
// 判据刻意挑剔（空行、注释、表头、带空格或引号的键一律不算），因为它唯一的用途
// 是「顶层标量」——而顶层标量的键一定是裸标识符。宽一点就会把
// `[projects."/root"]` 里那种带引号的键也认进来。
func topLevelKey(line string) (string, bool) {
	t := strings.TrimSpace(line)
	if t == "" || strings.HasPrefix(t, "#") || strings.HasPrefix(t, "[") {
		return "", false
	}
	i := strings.IndexByte(t, '=')
	if i <= 0 {
		return "", false
	}
	k := strings.TrimSpace(t[:i])
	if k == "" || strings.ContainsAny(k, " \t\"'") {
		return "", false
	}
	return k, true
}

// providerBlock 是那段指向本地代理的配置。
//
// 两处值得解释：
//
//   - `wire_api = "responses"`：codex 0.155（2026-09-21 实测）**不再支持**
//     `wire_api = "chat"`，报错原文是 `wire_api = "chat" is no longer supported`。
//     它说的是 Responses API，而我们的转发是纯字节直通——上游那份中继实测认
//     `/v1/responses`（400 缺模型，不是 404），所以这一条不用做协议转换。
//   - `requires_openai_auth = false`：本地代理根本不校验这个 token（数据面把客户端
//     的 Authorization 一律丢掉、换成上游真 key），所以没必要让用户去管一个环境变量。
//     代价写清楚：**别把这份 TOML 当成能保护代理的东西**，它指向的是一个只听
//     127.0.0.1 的本地端口。
//
// **不要退回 `env_key`**（2026-09-21 实测）：`env_key` 指向一个当时没设的环境变量
// 时，codex 不是报错而是**卡住**——`codex exec` 跑满 90 秒超时（exit 124），
// 输出停在 `Reading additional input from stdin...`。那正是「用户绕过 shim 直接敲
// codex」的常见情形，而症状是一个不说明原因就挂住的 CLI，极难归因。两个都写也救
// 不了（同时给 `env_key` 与 `requires_openai_auth = false`、env 不给，同样超时）。
func providerBlock(port int) []string {
	return []string{
		providerTable,
		`name = "newgate (local proxy)"`,
		`base_url = "http://127.0.0.1:` + strconv.Itoa(port) + `/a/` + ID + `/v1"`,
		`wire_api = "responses"`,
		`requires_openai_auth = false`,
	}
}

// quote 把值写成 TOML 的基本字符串。
func quote(s string) string { return `"` + s + `"` }

// contextWindow 取这个客户端此刻该声明的窗口。
//
// 来源是**活动 profile 的 ContextWindow**（`docs/04-configuration.md`）——那正是
// claude 侧走 CLAUDE_CODE_MAX_CONTEXT_TOKENS 的同一个数。取不到（没配、profile
// 读不出来）就返回 0，也就是**不写这一行**：codex 会用它的兜底元数据并打一句
// 警告，而那句警告是诚实的——比我们瞎猜一个数让它按错误的窗口 compact 强。
//
// 代价说清楚：profile 换了（档位绑到另一家模型）之后这个数不会自己跟着变，
// 要重新 `newgate on codex`。这与 docs/03 §3.3 那条「改了注入内容要重新接管一次」
// 是同一件事，不是新加的坑。
func contextWindow() int {
	snap, err := store.Load()
	if err != nil || snap == nil {
		return 0
	}
	active := snap.State.ActiveFor(ID)
	for _, p := range snap.Profiles {
		if p.Name == active {
			return p.ContextWindow
		}
	}
	return 0
}

// tierOf 取这个客户端此刻该用的档位。
//
// 走的是**槽位那一套**（与 claude 的档位映射同一份知识、同一张卡）：codex 的
// `model` 槽位没有 EnvVar（它的值要写进 TOML，不是注入 env），而 confighook 的
// Slot 允许 EnvVar 为空正是为这种情况留的口子。
func tierOf(f agentapi.AgentFacts) string {
	a := Agent()
	if len(a.Slots) == 0 {
		return "normal"
	}
	return agentapi.TierOf(f, a.Slots[0])
}
