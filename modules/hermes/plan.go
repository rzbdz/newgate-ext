package hermes

import (
	"sort"
	"strconv"
	"strings"

	"github.com/rzbdz/newgate/modules/config/domain"
)

// 本文件是**这次接管要写什么**：给定此刻的 newgate 配置，算出 config.yaml 里每一处
// 该是什么值。纯函数（输入是 inputs，输出是 []edit），所以它能被单测直接钉住。
//
// # 写的是哪几处（hermes 0.13，2026-11-04 读源码 + 本机实测）
//
//	model.provider / default        newgate / 档位名（槽位 model）
//	model.base_url / api_key / api_mode   本地代理（见下：为什么在 model 里也写一份）
//	model.context_length            活动 profile 的窗口（知道才写）
//	providers.newgate               「跟着 newgate 走」那一行：/model 里只列档位
//	providers.newgate-<profile>     每份 profile 一行：钉在那份 profile 上（URL 带 /p/），
//	                                列档位 + 它绑定的每个具体模型（provider/model）
//	delegation.provider / model     newgate / 档位（槽位 delegation），base_url/api_key 清空
//	auxiliary.<task>.provider/model newgate / 档位，base_url/api_key 清空
//
// # 几处不显然的取舍
//
//   - **provider 写具名的 `newgate`，不写 `custom`**：具名 provider 走
//     `_resolve_named_custom_runtime`，它的 api_mode、URL 都按我们写的来；/model 的
//     列表里那一行也会被标成「当前」（is_current 比的是 provider 键）。
//   - **model 里再写一份 base_url / api_key / api_mode**：辅助任务在拿不到主循环的
//     运行时时会按 `model.base_url` 解析一个裸的 `custom`（auxiliary_client 的退路），
//     而 `/model <名字>` 不带 `--provider` 时，hermes 只在 base_url 是本机地址时才
//     认定「当前是自定义端点」，否则可能把人换到 OpenRouter 去。两处都靠这一份兜住。
//   - **api_key 写一个占位值而不是留空**：留空时 hermes 会按顺序去拿 `OPENAI_API_KEY`
//     / `OPENROUTER_API_KEY`——用户真的钥匙会被发到代理（虽然代理会丢掉它，但那本不
//     该出门）。占位值要 ≥4 个字符且不是 `dummy` 这类词，否则同样被当成「没给」。
//   - **不写 `discover_models: false`**：/model 会去问 `<base>/models` 并用问到的替换
//     这里的模型表，而那份清单由本模块在代理那一侧答（st-models.go），与这里写的是
//     同一个函数算的。写那个键的代价是 hermes 每归一化一次就往 errors.log 里记一条
//     「unknown config keys ignored」（实测一次 `hermes chat` 九条）。这里的 `models:`
//     仍然要写：每个模型的 context_length 只能从这里来。
//   - **delegation 清掉 base_url**：它有 base_url 时 api_mode 被写死成
//     chat_completions、provider 被忽略（delegate_tool.py）；清掉才走具名 provider。
//   - **auxiliary 清掉 base_url / api_key**：两者都在时它直接当自定义端点用，
//     provider 那一格就不算数了（`_resolve_task_provider_model`）。

// ProviderID 是我们在 config.yaml 的 `providers:` 里占的那个键（也是它的前缀：每份
// profile 一行，叫 `newgate-<profile>`）。前缀下面的键**都归我们**：接管时整块重写，
// 同步时多出来的删掉（见 ownedProvider）。
const ProviderID = "newgate"

// placeholderKey 见文件头「api_key 写一个占位值」。
const placeholderKey = "newgate-local"

// apiMode 是 hermes 发给我们的方言：OpenAI chat completions。
//
// 为什么不是 anthropic_messages（hermes 原来配的那种）：newgate 的转发是**纯字节直通**，
// 客户端的方言必须是上游听得懂的；今天每家 provider 都声明 `protocol: openai`，而
// anthropic 方言只有部分聚合器收。另一条是 hermes 自己的：fallback 与 delegation 的
// 某些路径**不认**具名 provider 的 api_mode、一律按 chat_completions 走——选它，两条
// 路就不会一条走一种方言。
const apiMode = "chat_completions"

// auxTasks 是 hermes 0.13 代码里真正会调用的辅助任务（`auxiliary.<task>`）。
//
// 文件里出现、但不在这张表里的任务（`flush_memories` 是一个：没有代码读它）也照样接管
// ——接管是「文件里写着的模型都指到我们」，不是「我们认识的才算」。表的用处是反过来：
// 文件里**没写**的那几个也补上，否则它们走 `auto`、跟着主循环用 normal 档，
// 槽位卡上设的 light 对它们不生效，而卡片上看不出来。
var auxTasks = []string{
	"vision", "web_extract", "compression", "session_search", "skills_hub",
	"approval", "mcp", "title_generation", "triage_specifier", "curator", "goal_judge",
}

// auxSlot 某个辅助任务归哪个槽位（见 agent.go 的槽位表）。
func auxSlot(task string) string {
	switch task {
	case "vision":
		return SlotVision
	case "compression":
		return SlotCompression
	}
	return SlotAuxiliary
}

// inputs 是算一次接管要用到的全部事实——都在调用方那一刻现取（见 takeover.go 的
// currentInputs），这里不读盘。
type inputs struct {
	Port int
	// Tiers 槽位名 → 此刻该走的档位。
	Tiers map[string]string
	// Active 是 hermes 此刻跟随的那份 profile（state.json 的 ActiveFor("hermes")）。
	Active string
	// Profiles 是全部 profile（按名字排好）。
	Profiles []*domain.Profile
	// Unreadable 有文件、但这一刻读不出来的 profile（手改坏了）。它们那一行不动。
	Unreadable []string
}

func (in inputs) baseURL(profile string) string {
	u := "http://127.0.0.1:" + strconv.Itoa(in.Port) + "/a/" + ID
	if profile != "" {
		u += "/p/" + profile
	}
	return u + "/v1"
}

func (in inputs) profile(name string) *domain.Profile {
	for _, p := range in.Profiles {
		if p.Name == name {
			return p
		}
	}
	return nil
}

// window 一份 profile 声明的上下文窗口；没声明返回 0（= 不写，理由同 codex 的
// contextWindow：猜一个数比不写更糟）。
func window(p *domain.Profile) int {
	if p == nil {
		return 0
	}
	return p.ContextWindow
}

// providerKey 某份 profile 那一行的键。
func providerKey(profile string) string { return ProviderID + "-" + profile }

// ownedProvider 这个键是不是我们的（`newgate` 或 `newgate-*`）。
func ownedProvider(key string) bool {
	return key == ProviderID || strings.HasPrefix(key, ProviderID+"-")
}

// profileKeyOK 这份 profile 的名字能不能当一个 provider 键。
//
// profile 名来自文件名，理论上什么字符都有；而这个键要同时活在 YAML（裸键）、
// hermes 的 slug（`custom_provider_slug` 会小写化、换掉空格）与 URL 路径（`/p/<名字>`）
// 里。三处都安全的只有这一小撮字符；不安全的那份**跳过并报出来**，不硬塞。
func profileKeyOK(name string) bool {
	if name == "" {
		return false
	}
	for _, c := range name {
		ok := c == '-' || c == '_' || c == '.' ||
			(c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
		if !ok {
			return false
		}
	}
	return true
}

// concreteModels 一份 profile 绑定的每个具体模型，写成 `provider/model`。
//
// 为什么带 provider 前缀：/model 的列表就是用户看见的全部——同一个模型名在两家
// provider 上都有时，前缀是唯一能让人分清的东西。数据面会把前缀剥掉
// （gateway/protocol.NormalizeRole），剥完剩下的正是配置里那个模型名，所以在这一
// 行（URL 钉在这份 profile 上）里点名必然能被反查回来。
//
// **跳过模型名本身带 `/` 的绑定**（OpenRouter 那种 `google/gemini-…`）：NormalizeRole
// 取的是最后一个 `/` 之后的部分，它在任何客户端那里都点不中——列出来就是一个选了
// 必 404 的条目。跳过的那些交回给调用方报出来。
//
// 顺序：档位按 domain.Roles 的能力顺序，其余键按名字；同一个模型只列第一次出现。
func concreteModels(p *domain.Profile) (ids []string, skipped []string) {
	if p == nil {
		return nil, nil
	}
	var keys []string
	seenKey := map[string]bool{}
	for _, r := range domain.Roles {
		if _, ok := p.Roles[r]; ok {
			keys = append(keys, r)
			seenKey[r] = true
		}
	}
	var rest []string
	for k := range p.Roles {
		if !seenKey[k] {
			rest = append(rest, k)
		}
	}
	sort.Strings(rest)
	keys = append(keys, rest...)

	var bindings []domain.Binding
	for _, k := range keys {
		bindings = append(bindings, p.Roles[k]...)
	}
	if p.Fallback != nil {
		bindings = append(bindings, *p.Fallback)
	}
	seen := map[string]bool{}
	for _, b := range bindings {
		if b.Ref != "" || b.Model == "" || b.Provider == "" {
			continue
		}
		id := b.Provider + "/" + b.Model
		if seen[id] {
			continue
		}
		seen[id] = true
		if strings.Contains(b.Model, "/") {
			skipped = append(skipped, id)
			continue
		}
		ids = append(ids, id)
	}
	return ids, skipped
}

// providerEntry 是 `providers:` 下面的一行。
func providerEntry(key, url string, ids []string, win int) omap {
	models := make(omap, 0, len(ids))
	for _, id := range ids {
		meta := omap{}
		if win > 0 {
			// 按**模型**声明，不只写 model.context_length：后者是全局的一格，hermes 在
			// /model 换模型时会把它清掉（run_agent.py），之后的新模型就只能靠它自己去
			// 猜（猜不到按 256K）。per-model 那一格按 (base_url, 模型名) 精确匹配，
			// 换到哪个都有。
			meta = omap{{"context_length", win}}
		}
		models = append(models, kv{id, meta})
	}
	return omap{
		{"name", key},
		{"base_url", url},
		{"api_key", placeholderKey},
		{"api_mode", apiMode},
		{"default_model", "normal"},
		{"models", models},
	}
}

// catalog 是 `providers:` 下我们那几行，以及没能列出来的东西（要报给人看）。
type catalog struct {
	Entries []kv
	// Skipped 名字不能当键的 profile、模型名带 `/` 的绑定——都写成给人看的一句。
	Skipped []string
}

// buildCatalog 算出「跟着走」那一行 + 每份 profile 一行。
func buildCatalog(in inputs) catalog {
	var c catalog
	c.Entries = append(c.Entries, kv{ProviderID,
		providerEntry(ProviderID, in.baseURL(""), domain.Roles, window(in.profile(in.Active)))})
	for _, p := range in.Profiles {
		if !profileKeyOK(p.Name) {
			c.Skipped = append(c.Skipped, "profile "+strconv.Quote(p.Name))
			continue
		}
		ids, skipped := concreteModels(p)
		for _, s := range skipped {
			c.Skipped = append(c.Skipped, p.Name+": "+s)
		}
		all := append(append([]string(nil), domain.Roles...), ids...)
		key := providerKey(p.Name)
		c.Entries = append(c.Entries, kv{key, providerEntry(key, in.baseURL(p.Name), all, window(p))})
	}
	return c
}

// catalogEdits 是「只动模型清单」的那一组改动：我们那几行写成此刻的样子，多出来的
// （profile 删了）删掉。
//
// 例外：**此刻还被引用着**的那一行不删——用户在 hermes 里 `/model … --global` 把
// 主模型钉到了 `newgate-old` 上，而 old 这份 profile 刚被删掉。删了那一行，hermes
// 下一轮就解析不出 provider，整个消息网关停摆；留着它，最坏是请求被 newgate 404
// 回去（profile 不存在），而那句报错是说得清楚的。留下的那一行报出来。
func catalogEdits(d *doc, in inputs) ([]edit, catalog) {
	c := buildCatalog(in)
	var edits []edit
	want := map[string]bool{}
	for _, e := range c.Entries {
		want[e.K] = true
		edits = append(edits, edit{Path: []string{"providers", e.K}, Value: e.V})
	}
	referenced := referencedProviders(d)
	for _, n := range in.Unreadable {
		want[providerKey(n)] = true // 读不出来 ≠ 删掉了：原样留着
		c.Skipped = append(c.Skipped, "profile "+strconv.Quote(n)+" (unreadable, row left as is)")
	}
	for _, k := range d.mapChildren("providers") {
		if !ownedProvider(k) || want[k] {
			continue
		}
		if referenced[k] {
			c.Skipped = append(c.Skipped, k+" (still referenced, kept)")
			continue
		}
		edits = append(edits, edit{Path: []string{"providers", k}})
	}
	return edits, c
}

// referencedProviders 文件里此刻有哪些 provider 键被用着（主循环、delegation、各辅助任务）。
func referencedProviders(d *doc) map[string]bool {
	out := map[string]bool{}
	add := func(path ...string) {
		if v, ok := d.get(path...); ok && v != "" {
			out[v] = true
		}
	}
	add("model", "provider")
	add("delegation", "provider")
	for _, t := range d.mapChildren("auxiliary") {
		add("auxiliary", t, "provider")
	}
	return out
}

// fullEdits 是一次完整接管：模型清单 + 每个槽位指到我们。
func fullEdits(d *doc, in inputs) ([]edit, catalog) {
	edits, c := catalogEdits(d, in)
	url := in.baseURL("")
	m := func(k string) []string { return []string{"model", k} }
	edits = append(edits,
		edit{Path: m("provider"), Value: ProviderID},
		edit{Path: m("default"), Value: in.Tiers[SlotModel]},
		edit{Path: m("base_url"), Value: url},
		edit{Path: m("api_key"), Value: placeholderKey},
		edit{Path: m("api_mode"), Value: apiMode},
	)
	// `model.model` 是 `default` 的旧别名（只在 default 缺席时被读）。文件里有它就写成
	// 同一个值：留着旧的模型名，读的人会以为 hermes 还在用它。
	if _, ok := d.lookup("model", "model"); ok {
		edits = append(edits, edit{Path: m("model"), Value: in.Tiers[SlotModel]})
	}
	if w := window(in.profile(in.Active)); w > 0 {
		edits = append(edits, edit{Path: m("context_length"), Value: w})
	}

	dl := func(k string) []string { return []string{"delegation", k} }
	edits = append(edits,
		edit{Path: dl("provider"), Value: ProviderID},
		edit{Path: dl("model"), Value: in.Tiers[SlotDelegation]},
		edit{Path: dl("base_url"), Value: ""},
		edit{Path: dl("api_key"), Value: ""},
	)

	tasks := append([]string(nil), auxTasks...)
	known := map[string]bool{}
	for _, t := range tasks {
		known[t] = true
	}
	for _, t := range d.mapChildren("auxiliary") {
		if !known[t] {
			tasks = append(tasks, t)
		}
	}
	for _, t := range tasks {
		a := func(k string) []string { return []string{"auxiliary", t, k} }
		edits = append(edits,
			edit{Path: a("provider"), Value: ProviderID},
			edit{Path: a("model"), Value: in.Tiers[auxSlot(t)]},
			edit{Path: a("base_url"), Value: ""},
			edit{Path: a("api_key"), Value: ""},
		)
	}
	return edits, c
}

// takenOverPaths 接管会碰的全部路径——还原时逐个放回原件里的样子（见 Restore）。
//
// 由「此刻的文件」与「原件」两份一起决定：原件里有、此刻没有的辅助任务也要放回去；
// 此刻有、原件里没有的（我们补的）要删掉。
func takenOverPaths(cur, orig *doc) [][]string {
	var out [][]string
	seen := map[string]bool{}
	add := func(p ...string) {
		k := strings.Join(p, "\x00")
		if !seen[k] {
			seen[k] = true
			out = append(out, p)
		}
	}
	for _, k := range []string{"provider", "default", "model", "base_url", "api_key", "api_mode", "context_length"} {
		add("model", k)
	}
	for _, k := range []string{"provider", "model", "base_url", "api_key"} {
		add("delegation", k)
	}
	tasks := map[string]bool{}
	for _, t := range auxTasks {
		tasks[t] = true
	}
	for _, d := range []*doc{cur, orig} {
		for _, t := range d.mapChildren("auxiliary") {
			tasks[t] = true
		}
	}
	var names []string
	for t := range tasks {
		names = append(names, t)
	}
	sort.Strings(names)
	for _, t := range names {
		for _, k := range []string{"provider", "model", "base_url", "api_key"} {
			add("auxiliary", t, k)
		}
	}
	for _, d := range []*doc{cur, orig} {
		for _, k := range d.mapChildren("providers") {
			if ownedProvider(k) {
				add("providers", k)
			}
		}
	}
	return out
}
