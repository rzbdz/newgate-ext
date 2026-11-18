package confighook

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/rzbdz/newgate-ext/modules/config/paths"
	modules "github.com/rzbdz/newgate/component"
	i18n "github.com/rzbdz/newgate/lib/i18n"
)

// ConfigHooks 是客户端模块写入配置扩展的所有权端口。
// 每次注册都返回 Release，使模块停止时能精确撤销自己的贡献。
type ConfigHooks interface {
	RegisterAgent(*Agent) (modules.Release, error)
	// RegisterAgentInstaller 交上「我家工具怎么装」（见 AgentInstaller）。
	// 不交 = 这个客户端不提供自助安装，`newgate <agent>` 在没装的机器上照旧报错。
	RegisterAgentInstaller(agentID string, installer AgentInstaller) (modules.Release, error)
	// RegisterAgentFacts 交上「我自己那个客户端此刻怎么样」（见 AgentFacts）。
	//
	// 它是**另一条**注册而不是描述符上的字段：描述符在 Start 里交一次就定了，
	// 而事实每次被问都可能不一样，而且**可以不交**（不交就用缺省判据）。
	RegisterAgentFacts(agentID string, facts AgentFacts) (modules.Release, error)
	BindTakeover(agentID string, takeover ConfigTakeover) (modules.Release, error)
	RegisterStateField(owner, name string) (modules.Release, error)
}

// AgentCatalog 是运行时和 CLI 的只读客户端目录，
// 与 ConfigHooks 分离后，消费者无法借查询能力修改注册表。
type AgentCatalog interface {
	Get(id string) (*Agent, bool)
	Names() []string
	// Facts 拿到某个客户端交上来的运行时事实；nil = 它没交。
	Facts(id string) AgentFacts
	// Installer 拿到某个客户端的安装方式；nil = 它没交（那就不提供自助安装）。
	Installer(id string) AgentInstaller
	// Installed 报告这个客户端在不在**这台机器**上。
	//
	// 有事实就问事实（那是客户端自己的判据），没有就按 Bin 在 PATH 上找（跳过我们
	// 自己的 shim 目录，见 InstalledDefault）。缺省为什么是「找 PATH」而不是
	// 「假定在」：这个判据的用途正是回答「到底有没有」，假定在就回到了它要修的那个 bug。
	Installed(id string) bool
}

// 两个 capability 是同一注册表的读写分面：写端只交给扩展模块，
// 读端交给 runtime 和 CLI，防止查询者获得注册权限。
var (
	ConfigHooksCapability  = modules.NewCapability[ConfigHooks]("config-hooks")
	AgentCatalogCapability = modules.NewCapability[AgentCatalog]("agent-catalog")
)

// Slot 描述客户端的一个模型槽位如何映射到 newgate 语义档位。
// EnvVar 为空的槽位仍可供配置接管使用，但不会参与进程环境注入。
type Slot struct {
	Name string
	// Tier 是这个槽位的**缺省**归属。用户可以在配置里改（见 AgentFacts.SlotTier）
	// ——改了之后生效的是那个，不是这个。所以读「此刻走哪儿」要走 Facts，别直接
	// 读它。
	Tier string
	// Also 是这个槽位**除档位之外**还认的取值——客户端自己的约定，不是我们的档位。
	//
	// 为什么要有它：Claude Code 的 subagent 槽位可以写 `inherit`（「跟父会话走」），
	// 那是**它的**语义，newgate 只是原样注入这个字符串。没有这一格的话，槽位映射
	// 那条可配置的路会把 `inherit` 当成打错的档位名滤掉——界面上设不了，手改的还会
	// 被静默忽略（看着像没生效，而说明里明明写着可以这么写）。
	//
	// 取值归**客户端模块**声明（那是它的知识）；内核与配置层只负责「这些也是合法的」。
	Also []string
	// ModelSuffix 是注入这个槽位时**无条件**拼在模型名后面的后缀——同样也是
	// **客户端自己的能力标记**，不是我们的档位语义。
	//
	// 出现的理由（2026-09-28）：Claude Code 用 `[1m]` 后缀声明「这个模型有 100 万
	// 上下文」。它是客户端侧的一个开关，不是上游模型 id 的一部分——数据面转发前
	// 会把它剥掉（见 gateway/protocol.NormalizeRole），反向解析则靠这一层：剥掉
	// 之后剩下的模型名正是配置里那个名字，所以点名解析照旧命中。
	//
	// 为什么装在槽位上、而不是「整个 agent 一个开关」：槽位之间可以不同。Claude
	// Code 的 subagent 槽位允许取 `inherit`（见 Also），给那个值加后缀会注入一个
	// 它不认识的模型名；开关挂在槽位上，客户端自己按槽位表态。
	//
	// 为什么内核不校验它、也不认识 `[1m]`：后缀是个**字符串**，含义归声明它的
	// 客户端模块。我们绕开内核 import 客户端模块的那条线，于是契约只剩「注入时
	// 拼在值后面」这一件事——两端用的是同一个常量，谁也没在核心里写死 `[1m]`。
	//
	// 空 = 不拼。它**只作用于模型名**，窗口声明那两个变量是数字，永远不沾它
	// （见 Agent.ContextWindowEnv 与 modules/runtime/launch）。
	ModelSuffix string
	EnvVar      string
	Desc        string
}

// Apply 把要注入这个槽位的值补成最终形态：拼上客户端自己的能力标记。
//
// 动态模式（值 = 档位名）与钉死模式（值 = 真实模型名）都走这里，所以「标记一定
// 在」这条只写一次。空值不拼——一个空的槽位变量注入进去等于告诉客户端「模型叫
// 空字符串」，那是另一个 bug，不该被这里顺手制造出来。
func (s Slot) Apply(model string) string {
	if model == "" || s.ModelSuffix == "" {
		return model
	}
	return model + s.ModelSuffix
}

// Agent 是一个可接管 AI CLI 的完整描述符。
// 它集中声明二进制发现、协议、模型槽位和配置接管，避免这些知识散落在 runtime。
type Agent struct {
	ID      string
	Bin     []string
	Dialect string
	// Icon 是这个客户端在界面上的图标，SVG **path 的 d 那一串**（`24 24` 视口）。
	//
	// 为什么是「一串路径数据」而不是一个文件名或一个 URL：图标是**客户端的知识**
	// （与下面的 ContextWindowEnv 同一条），所以由它自己带；而带成路径数据，界面就
	// 只需要一个 `<svg viewBox="0 0 24 24">` 把它放进去——不必认识任何一套图标库、
	// 不必读盘、也不必联网。`currentColor` 由界面那一侧填，所以一套图标跟着皮肤走。
	//
	// 为什么不是前端按 ID 查一张表：那张表就是「界面认识客户端」，而这一层的规矩
	// 正是它不认识——加一家客户端要改前端，就等于这一格从来没被设计过。
	//
	// 空 = 这个客户端没给图标（界面画它自己的通用记号，不是画一个空白）。
	Icon  string
	Slots []Slot

	BaseURLEnv string
	AuthEnv    string
	UnsetEnv   []string
	Notes      string
	Config     ConfigTakeover

	// ContextWindowEnv / AutoCompactEnv 是「窗口声明」两个环境变量的**名字**：
	// 前者走客户端对「未知模型」的窗口假设，后者是它自己的 compact 触发点。
	//
	// 名字是**客户端的知识**（Claude Code 叫 CLAUDE_CODE_MAX_CONTEXT_TOKENS），
	// 所以由 agent 定义填，内核只负责「配置里声明了就注进去」。
	// 空 = 这个客户端不认识这两种变量（比如 opencode），那就不注入。
	//
	// 2026-09-20 之前这两个名字硬编码在 modules/runtime/launch 里——那是内核在
	// 认识某一家客户端的内部变量名，方向反了。搬到定义上是同一个动作的延续：
	// 客户端接入归发行版（见发行版的 modules/claudecode），内核只提供这张表。
	ContextWindowEnv string
	AutoCompactEnv   string

	// ContextWindowCap / AutoCompactCap 是**客户端自己的安全上限**：无论
	// profile 声明多大，注进去的窗口与 compact 数字都不超过它。0 = 不设上限。
	//
	// 为什么需要（2026-11-12 现场）：Claude Code 的 `[1m]` 名称后缀 + 1M 的
	// 窗口声明会把会话顶到真实窗口的最后一格。一旦超了，上游的 400 连
	// /compact 也一起拒——压缩请求本身也带着整段会话，于是**客户端没有自救
	// 路径**，只能手工删历史。留出余量是唯一能自动恢复的做法；具体数字归
	// 客户端模块（Claude Code 现在是 512K）。
	//
	// ContextWindowCap 在 profile **没声明**窗口时也当声明用：客户端对未知
	// 模型的缺省假设通常更小（Claude Code 是 200k），而这台客户端明确能接受
	// 这个上限。AutoCompactCap 不这样兜底——没声明 compact 就交给客户端按
	// 它自己的窗口算压缩点，避免在窗口很小时注入一个比窗口还大的值。
	ContextWindowCap int
	AutoCompactCap   int
}

// AgentFacts 是贡献者对自己那个客户端的**运行时事实**：这一刻它装没装、某个槽位
// 走哪个档位。
//
// # 为什么是一个端口，而不是描述符上的两个函数字段
//
// Agent 是**描述符**：Bin、方言、槽位表、环境变量名——那些是「这个客户端是什么」，
// 不变的、可以打印出来给人看的。而这两个问题的答案随磁盘与配置变（今天卸了、
// 明天装了；用户改了槽位映射）。把「是什么」与「此刻怎么样」塞进同一个 struct，
// 依赖注入的那一侧就没了：谁提供、能不能替换、测试里怎么塞一个假的，全都无从谈起
// ——内核只是捡到一个恰好被赋了值的函数指针。
//
// 端口这一侧是完整的：贡献者**注册**进来（拿得到 Release）、消费者**问**端口、
// 没注册时有一份写死的缺省（见 AgentCatalog.Installed）。与「注册型 capability
// 必须返回 Release，consumer 在 Stop 中逆序释放」那条规矩同源。
type AgentFacts interface {
	// Installed 报告这台机器上有没有这个工具。实现里通常就一句
	// `Agent().OnPath()`——那条已经跳过了我们自己的 shim 目录。
	Installed() bool
	// SlotTier 报告某个槽位此刻走哪个档位；空串 = 用描述符里的缺省。
	SlotTier(slot Slot) string
}

// TierOf 是一个槽位此刻实际走的档位：贡献者交了事实就问它，否则用描述符里的缺省。
//
// 它是**纯函数**（喂事实进来），所以「没交事实时用缺省」这条规则只有一份实现，
// 谁调用都一样。
func TierOf(facts AgentFacts, slot Slot) string {
	if facts != nil {
		if t := facts.SlotTier(slot); t != "" {
			return t
		}
	}
	return slot.Tier
}

// InstalledDefault 是「这个客户端在不在这台机器上」的**唯一一份判据**：客户端交了
// 事实就问事实（它有它的知识），没交才在 PATH 上找它的 Bin——且**排除 skipDirs**
// （我们自己的 shim 目录，由调用方给：shim 放在哪是 runtime 的知识）。
//
// 为什么是自由函数而不是各处自己写一遍：实现 AgentCatalog 的地方不止一处（真注册表、
// 测试里的假目录），而「装没装」出现两种答案是这类判据最坏的失效方式——一处说装了、
// 一处说没装，而两边看起来都对。
func InstalledDefault(a *Agent, facts AgentFacts) bool {
	if facts != nil {
		return facts.Installed()
	}
	if a == nil {
		return false
	}
	return a.OnPath()
}

// TakeoverReport 记录一次配置接管实际改了什么；接管不能静默成功。
type TakeoverReport struct {
	File     string
	Rewrites []string
	Fuzzy    int
	Skipped  string
}

// ConfigTakeover 定义可逆的持久配置接管。
// Apply 与 Restore 成对，Targets/IsTakenOver 则让 CLI 能在执行前解释当前状态。
type ConfigTakeover interface {
	Targets() []string
	IsTakenOver(target string) bool
	Apply(port int) ([]*TakeoverReport, error)
	Restore() ([]string, error)
}

// EnvSlots 返回需要注入环境变量的槽位，同时保留原始 Slots 供配置型客户端使用。
func (a *Agent) EnvSlots() []Slot {
	var out []Slot
	for _, slot := range a.Slots {
		if slot.EnvVar != "" {
			out = append(out, slot)
		}
	}
	return out
}

// BuildEnv 生成启动客户端所需的最小环境覆盖，不复制当前进程的完整环境。
//
// facts 是「这个客户端此刻怎么样」（见 AgentFacts），由调用方从目录端口取来交给它
// ——本函数不认识它是怎么算出来的，也不该认识。nil = 用描述符里的缺省。
func (a *Agent) BuildEnv(port int, authToken string, facts AgentFacts) map[string]string {
	env := map[string]string{}
	if a.BaseURLEnv != "" {
		env[a.BaseURLEnv] = fmt.Sprintf("http://127.0.0.1:%d/a/%s", port, a.ID)
	}
	if a.AuthEnv != "" {
		env[a.AuthEnv] = authToken
	}
	for _, slot := range a.EnvSlots() {
		// Apply：客户端的**能力标记**（`[1m]` 那类）在这里拼上去，两种模式
		// 共用同一次拼接——标记只写一遍，见 Slot.ModelSuffix。
		env[slot.EnvVar] = slot.Apply(TierOf(facts, slot))
	}
	return env
}

// FindReal 跳过 newgate 自己的 shim 查找真实客户端，防止接管后递归启动自身。
func (a *Agent) FindReal(skipDir string) (string, error) {
	if p := a.realBin(); p != "" {
		return p, nil
	}
	return "", i18n.E("cannot find {names} in PATH (skipped the shim directory {dir})",
		i18n.A{"names": strings.Join(a.Bin, "/"), "dir": skipDir})
}

// OnPath 报告这家客户端的**真实**可执行文件在不在 PATH 上——**排除我们自己的 shim**。
//
// 「排除」是这条判据的全部要点：接管就是往 PATH 前面的目录里放一个同名的链接，所以
// 一次天真的 PATH 查找**永远**能找到它，而那个文件正是我们放的那个。不排除的话，
// 判据会回答「我们做过接管」而不是「这台机器上有这个工具」——而那两件事在「装过、
// 后来卸了」的时候是分开的（2026-09-21 实测：`which opencode` 找不到，而 status 报 ✓，
// 因为 opencode 走 config 机制，「已接管」判的是我们改过的配置文件在不在）。
//
// 要排除的目录是**写死的一条**（paths.ShimDir），不是调用方传进来的参数：需要跳过的
// 永远是同一个目录，让每个调用方各传一次只会多出「传错了/忘了传」这一种故障，而那种
// 故障的表现正是这条判据又开始撒谎。
func (a *Agent) OnPath() bool { return a.realBin() != "" }

// realBin 是上面两条共用的那一次查找。
func (a *Agent) realBin() string {
	skipDir := filepath.Clean(paths.ShimDir())
	skipped := func(dir string) bool { return filepath.Clean(dir) == skipDir }
	for _, name := range a.Bin {
		for _, dir := range filepath.SplitList(os.Getenv("PATH")) {
			if dir == "" || skipped(filepath.Clean(dir)) {
				continue
			}
			path := filepath.Join(dir, name)
			if !isExec(path) {
				continue
			}
			// 解析链接之后还指着 newgate 的也不算（有人会把 shim 复制到别处）。
			if real, err := filepath.EvalSymlinks(path); err == nil {
				if skipped(filepath.Dir(real)) ||
					strings.HasSuffix(filepath.Base(real), "newgate") {
					continue
				}
			}
			return path
		}
	}
	return ""
}

func isExec(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir() && info.Mode()&0o111 != 0
}
