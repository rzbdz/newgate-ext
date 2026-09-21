package codex

import (
	"encoding/json"
	"io/ioutil"
	"os"
	"path/filepath"

	i18n "github.com/rzbdz/newgate/lib/i18n"
	"github.com/rzbdz/newgate/modules/config/domain"
	"github.com/rzbdz/newgate/modules/config/paths"
)

// 本文件是 codex 的**模型名 ↔ 我们的档位**那张表，外加接管模式。
//
// # 为什么需要它（2026-09-21 实测的结论）
//
// codex 的模型目录**编在它二进制里**：`codex debug models` 打出 9 个（gpt-6-astra、
// gpt-5.6-sol、gpt-5.6-terra、gpt-5.6-luna、gpt-5.5 …），而它**不问服务端要**。
// 实测把它的 provider 指向一个记账的假上游：`/v1/models` **一次都没被请求过**——
// `--enable remote_models`、provider 表里声明 `models_endpoint`、再给 `env_key`
// 配 API key，三条都试了，假上游始终收到 0 条。那条路（`model-provider/src/
// models_endpoint.rs` 确实在二进制里）在「自定义 provider、无官方 auth」这种形态下
// 不开。
//
// 所以「把档位塞进 `/v1/models`、让它自己的选择器里出现档位」这条路**不存在**。
// 反过来做：**让它照旧用它自己的模型名，名字进来我们认**——用户在 codex 里按
// `/model` 选中谁，就落到我们哪一档。这正是它「里面无法切模型」的解法：今天
// config.toml 里写的是档位名，codex 只认一个模型，切也切不动。
//
// # 机制不是新开的洞
//
// 这些模型名登记成**动态角色**（domain.ExtraRole），与 omo 的 intra-agent 槽位走
// 同一条解析路径——`resolve.ResolveRequest` 的第一句就是
// `if domain.IsKnownRole(model) → BuildChain(...)`。core 完全不知道 codex 有哪几个
// 模型，也不知道「gpt-5.6-luna」是个什么东西。
//
// # 为什么是**文件**而不是代码里的常量
//
// codex 会出新模型（它自己那份目录就是会变的）。写死在代码里意味着「上游加一个
// 模型 → 我们发一版」，而这件事的知识（哪个名字该落哪一档）**用户自己就有**。
// 所以出厂给一张表，落在 `$NEWGATE_HOME/codex-models.json`，加一行就生效——
// 与 omo 的槽位表同一套做法（modules/opencodeomo 的 omo-slots.json）。
//
// 表是**叠加**的：文件里写的盖住同名的出厂值，多出来的就是新增的。所以「加一个
// 新模型」真的只是加一行，不必把出厂那五条抄一遍。

// ModelsFile 是这张表住的地方。放在配置根下，与其它模块自己的表并排。
func ModelsFile() string { return filepath.Join(paths.Config(), "codex-models.json") }

// 接管模式的两个取值。
const (
	// ModeTakeover 把**档位名**写进 config.toml（`model = "normal"`）。
	// 2026-09-21 之前的唯一行为，也是缺省——不变更任何人的现状。
	ModeTakeover = "takeover"
	// ModeRename 写 **codex 自己的模型名**（`model = "gpt-5.6-luna"`），
	// 由下面的表在请求进来时认回档位。用户在 codex 里换模型 = 换档位。
	ModeRename = "rename"
)

// modelTier 是表里的一条。
type modelTier struct {
	// Slug 是 codex 认的那个模型名（`codex debug models` 里的 slug）。机器标记，不翻。
	Slug string `json:"slug"`
	// Tier 是它落到我们哪一档。
	Tier string `json:"tier"`
}

// Models 是这份文件的形状。
type Models struct {
	// Mode 见上面的两个常量；空 = takeover（缺省）。
	Mode string `json:"mode,omitempty"`
	// Models 是要**加**或要**改**的那些条（出场的那五条不必抄进来）。
	Models []modelTier `json:"models,omitempty"`
}

// defaultModels 是出厂那张表：codex 目录里可见的五个 → 我们有的档位。
//
// 按 codex 自己的 priority 排（`codex debug models` 里那个数字，越小越能干）：
// 最能干的给 heavy，最便宜的给 light。2026-09-21 用户定的。
//
// **只登记可见的那五个**（visibility=list）：隐藏的那几个（daybreak-*、
// codex-auto-review）不进选择器，登记了也没人会选。
var defaultModels = []modelTier{
	{Slug: "gpt-6-astra", Tier: "heavy"},
	{Slug: "gpt-5.6-sol", Tier: "normal"},
	{Slug: "gpt-5.6-terra", Tier: "normal"},
	{Slug: "gpt-5.6-luna", Tier: "normal"},
	{Slug: "gpt-5.5", Tier: "light"},
}

// readModels 读这份文件；读不到（还没建过）或读不动时回落到出厂表。
//
// 读不动**不报错也不静默**：坏掉的那份会被忽略（fail-open，接管照常写成缺省的
// 行为），而这件事由 diagnostics 说出来——配置坏掉本身值得知道，但不该让
// `newgate on codex` 因此失败。
func readModels() (Models, string) {
	var m Models
	b, err := ioutil.ReadFile(ModelsFile())
	if err != nil || len(b) == 0 {
		return m, ""
	}
	if err := json.Unmarshal(b, &m); err != nil {
		return Models{}, i18n.T("{file} does not parse ({err}); the built-in table is in use",
			i18n.A{"file": ModelsFile(), "err": err.Error()})
	}
	return m, ""
}

// Mode 是这一刻的接管模式（缺省 takeover）。
func Mode() string {
	m, _ := readModels()
	if m.Mode == ModeRename {
		return ModeRename
	}
	return ModeTakeover
}

// SetMode 写模式。**只动这一格**：表里其余的部分原样保留（用户手写的东西不能因为
// 我们切一下开关就没了）。文件不存在时建一份最小的。
func SetMode(mode string) error {
	if mode != ModeRename {
		mode = ModeTakeover
	}
	m, _ := readModels()
	m.Mode = mode
	return writeModels(m)
}

func writeModels(m Models) error {
	if err := os.MkdirAll(filepath.Dir(ModelsFile()), 0o770); err != nil {
		return err
	}
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return ioutil.WriteFile(ModelsFile(), append(b, '\n'), 0o660)
}

// effectiveModels 是出厂表叠加文件之后的那张表。
//
// # 顺序是有意义的：**文件里写的排在前面**
//
// 反查（modelFor：rename 模式该把哪一个模型名写进 config.toml）取的是**第一条**
// 同档位的记录。出厂值排前面的话，「新加一个模型」这件事只做对了一半——它认得出
// （角色登记了）却**永远写不到**（写进文件的还是出厂那一条）。而这张表的用法本来
// 就是「上游出了新模型，我加一行」。
//
// 所以用户写的那几条优先：他刚刚加的那个模型就是他想用的那个。要换回来也简单
// ——把出厂那条改成别的档位，或者把新加的那条删掉。
//
// 稳定性：同一份文件两次装配得到同一张表（map 只用来查重，顺序由两个切片决定），
// 所以界面与接管两次问出来的答案一致。
func effectiveModels() []modelTier {
	m, _ := readModels()
	bySlug := map[string]string{}
	order := make([]string, 0, len(defaultModels)+len(m.Models))
	add := func(slug, tier string) {
		if slug == "" || tier == "" {
			return
		}
		// **先来的赢**：文件在前、出厂在后，所以已经记过就跳过——否则出厂值会把
		// 用户刚写的那一条盖回去，而「叠加」这件事在两边都像是对的（值都在表里，
		// 只是赢的不是写它的那个人）。
		if _, seen := bySlug[slug]; seen {
			return
		}
		order = append(order, slug)
		bySlug[slug] = tier
	}
	for _, e := range m.Models {
		add(e.Slug, e.Tier)
	}
	for _, d := range defaultModels {
		add(d.Slug, d.Tier)
	}
	out := make([]modelTier, 0, len(order))
	for _, slug := range order {
		out = append(out, modelTier{Slug: slug, Tier: bySlug[slug]})
	}
	return out
}

// modelFor 反过来问：这一档该写 codex 的哪个模型名（rename 模式用）。
//
// 找不到就回空串——调用方据此回落到档位名（见 takeover.go 的 slotValue），
// 也就是与 takeover 模式一模一样的行为。**回落而不是报错**：一条表被改窄了不该
// 让接管失败，那只是「这一档没有对应的 codex 模型」而已。
func modelFor(tier string) string {
	if tier == "" {
		return ""
	}
	for _, e := range effectiveModels() {
		if e.Tier == tier {
			return e.Slug
		}
	}
	return ""
}

// ---------- 动态角色 ----------

// rolesProvider 把这张表交给 core 的解析路径（roleprov.RoleProvider）。
//
// **只在 rename 模式下贡献**（2026-09-21 的一个取舍，理由是实测出来的）：
// 这些名字一旦登记成「角色」，`IsKnownRole` 就为真，于是**反解那条路被短路**——
// 而反解（「具体模型名反解回它所属档位，并把点名的模型放最前」）正是别的客户端
// 点名 `gpt-5.6-terra` 时依赖的东西（本机 smt-codex 的链上就有这个名字）。
// 换句话说：无条件登记会悄悄改掉**没让路的人**的行为。
//
// 所以它跟着模式走——rename 模式是用户**明确要过**的形态，那时候他要的就是
// 「codex 的模型名 = 档位」；takeover 模式下这份表对解析毫无用处（codex 只发
// 档位名），登记它纯属副作用。
type rolesProvider struct{}

func (rolesProvider) Source() string { return "codex" }

// WatchFiles：这份表改了要重新灌角色（含模式——两个都在同一个文件里）。
func (rolesProvider) WatchFiles() []string { return []string{ModelsFile()} }

func (rolesProvider) Roles() ([]domain.ExtraRole, error) {
	if Mode() != ModeRename {
		return nil, nil
	}
	models := effectiveModels()
	out := make([]domain.ExtraRole, 0, len(models))
	for _, e := range models {
		out = append(out, domain.ExtraRole{
			Key:     e.Slug,
			Source:  "codex",
			Default: e.Tier,
			// Meta 只给人看（core 不解释）：这一条是哪个客户端要的、落到哪一档。
			Meta: map[string]string{"client": "codex", "tier": e.Tier},
		})
	}
	return out, nil
}
