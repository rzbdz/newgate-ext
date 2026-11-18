package setup

// 本文件是向导的第二幕：**把第一个上游写成一份能跑的全局配置**。
//
// 它只碰三份文件，全部走 store 那唯一的写路径（原子写 + 写前留历史，见内核
// config/store/history.go）：
//
//	providers.json   加（或更新）一个 provider：base_url / api_key / protocol
//	mappings/default.kv  一份叫 default 的 profile，四个档位全指向那一个模型
//	state.json       全局默认链头改成 default
//
// # 为什么写 .kv 而不是 .json
//
// profile 是用户之后最常手改的东西（换模型、加变体），而 .kv 一行一个字段、
// 肉眼可查（见内核 store/kv.go 的文件头）。`.kv` 与 `.json` 并存时 `.kv` 赢，
// 所以写它就是「这份是最新意图」。
//
// # 为什么四个档位都指向同一个模型
//
// 用户在这一步只给了一个 base、一个 key、一个模型名（那正是「开箱即用」的全部
// 要求）。把 four tiers 全指过去，才有**一条完整的链**：主循环（normal）、
// 分类器与 compact（mid）、小活（light）、以及明确点名 heavy 的调用，全都通。
//
// 留空任何一档的后果不是「省了点配置」，而是那一档解析成空链——症状是某类请求
// 突然「没有可用候选」，而用户完全看不出是当初没填。细化档位是**之后**的事
// （`newgate tui` / 网页），不是 bootstrap 的门槛。
//
// vision 也指过去：正交档位不绑的话，任何带图的请求直接没有候选。模型到底看不看
// 得懂图，我们这一步没有便宜的判据——指过去至少让它有机会成功，而收尾的提示里
// 会写明「图形/多模态请求也走这个模型」。

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/rzbdz/newgate-ext/modules/config/domain"
	"github.com/rzbdz/newgate-ext/modules/config/paths"
	"github.com/rzbdz/newgate-ext/modules/config/store"
	"github.com/rzbdz/newgate/lib/i18n"
)

// ProviderName 是 bootstrap 写进 providers.json 的那个键。
const ProviderName = "BootstrapProfile"

// ProfileName 是 bootstrap 写出来的那份 profile 的名字。
const ProfileName = "default"

// Bootstrap 是一份「第一个上游」的输入。
type Bootstrap struct {
	// Provider 是 providers.json 里的键（机器名，界面上的名字）。
	Provider string
	// BaseURL 是上游的 base（不含 /v1 也行，两种写法都常见；见下面 normalizeBase）。
	BaseURL string
	// APIKey 明文写在 providers.json 里（见 VaultHint 的说明）。
	APIKey string
	// Protocol 是 openai / anthropic（转发方言）。空 = openai。
	Protocol string
	// Model 是四个档位都指过去的那个模型名。
	Model string
}

// Profile 是向导要不要覆盖全局配置的决定。
type Profile struct {
	// WriteProfile = 写 mappings/default.kv（false = 只更新 providers）。
	WriteProfile bool
	// SetDefault = 把全局默认链头设成 default。
	SetDefault bool
	// Overwrite = 允许覆盖已经存在的 default.kv。
	Overwrite bool
}

// ApplyResult 是一次落盘的账：改了哪些文件、动了哪些东西。
type ApplyResult struct {
	Files []string
	// Notes 是给人看的逐条说明（「provider 新增」/「profile 覆盖」/「全局默认 → default」）。
	Notes []string
	// Warnings 是「写成了，但你需要知道」的那些。
	Warnings []string
}

// Existing 是向导动手之前看到的现场：有没有可用的配置、default 是否已存在。
type Existing struct {
	// HasProvider = providers.json 里已经有 bootstrap 这个名字。
	HasProvider bool
	// HasProfile = mappings/default.kv（或 .json）已经存在。
	HasProfile bool
	// HasUsable = 当前全局默认那份 profile 解析得出候选、且 provider 有 key。
	//
	// 有它的时候向导**默认不碰**配置：一个已经能跑的人跑 `newgate config`
	// 多半是想装/接管客户端，而不是想换掉自己那份配置。
	HasUsable bool
	// DefaultProfile 是此刻的全局默认名字（给人看）。
	DefaultProfile string
}

// Inspect 看现场。读不出来一律当作「没有」（向导要能在半坏的配置上也跑起来）。
func Inspect() Existing {
	e := Existing{}
	provs, err := store.LoadProviders()
	if err == nil {
		_, e.HasProvider = provs.Providers[ProviderName]
	}
	st := store.LoadState()
	e.DefaultProfile = st.DefaultProfile
	if _, err := os.Stat(filepath.Join(paths.Mappings(), ProfileName+".kv")); err == nil {
		e.HasProfile = true
	}
	if _, err := os.Stat(filepath.Join(paths.Mappings(), ProfileName+".json")); err == nil {
		e.HasProfile = true
	}
	e.HasUsable = usable(st.DefaultProfile)
	return e
}

// usable 判断一份 profile 此刻真的能用：解析得出至少一个档位，且用到的每个
// provider 都有 key。
//
// 它比「文件存在」严格得多，而向导要的正是这个：一个 `newgate init` 铺出来的
// 占位配置**文件都在**，但它跑不起来——把它当成「已经有配置了」会让向导跳过
// 唯一能让人跑起来的那一步。
func usable(profile string) bool {
	if profile == "" {
		return false
	}
	pr, err := store.LoadProfile(profile)
	if err != nil || len(pr.Roles) == 0 {
		return false
	}
	provs, err := store.LoadProviders()
	if err != nil {
		return false
	}
	used := map[string]bool{}
	for _, cands := range pr.Roles {
		for _, b := range cands {
			if b.Ref != "" || b.Provider == "" || b.Model == "" {
				return false
			}
			used[b.Provider] = true
		}
	}
	if len(used) == 0 {
		return false
	}
	for name := range used {
		p, ok := provs.Providers[name]
		if !ok || p.Key() == "" {
			return false
		}
	}
	return true
}

// Apply 把这份输入写成能跑的配置。
func Apply(in Bootstrap, opt Profile, v ApplyResult) (ApplyResult, error) {
	res := v
	if strings.TrimSpace(in.Provider) == "" || strings.TrimSpace(in.BaseURL) == "" ||
		strings.TrimSpace(in.Model) == "" {
		return res, i18n.E("a provider name, a base URL and a model are all required", nil)
	}
	if err := paths.EnsureDirs(); err != nil {
		return res, err
	}

	// ---- providers.json ----
	//
	// 先读原文再改一处：SaveProviders 会把整份表写回去，所以**必须**从
	// LoadProviders 的结果出发——否则用户别的 provider 会被一次 bootstrap
	// 抹掉。但 LoadProviders 会把 api_key_env 解析成明文（它给转发层用），
	// 用它的结果写盘就是把密钥落盘（内核 SaveProviders 的注释专门警告过这条）。
	// 所以这里读的是**原始形态**：凡是没提到我们那个键的，原样保留；
	// 只有被 api_key_env 解析过的那几个 provider 的明文 key 不能带回去。
	provs, raw, err := loadProvidersRaw()
	if err != nil {
		return res, err
	}
	if _, existed := provs.Providers[in.Provider]; existed {
		res.Notes = append(res.Notes, i18n.T("updated provider {name}", i18n.A{"name": in.Provider}))
	} else {
		res.Notes = append(res.Notes, i18n.T("added provider {name}", i18n.A{"name": in.Provider}))
	}
	provs.Providers[in.Provider] = domain.Provider{
		BaseURL:  normalizeBase(in.BaseURL),
		APIKey:   strings.TrimSpace(in.APIKey),
		Protocol: protocolOr(in.Protocol),
	}
	stripResolvedKeys(provs, raw)
	if err := store.SaveProviders(provs); err != nil {
		return res, err
	}
	res.Files = append(res.Files, paths.ProvidersFile())
	if in.APIKey != "" {
		res.Warnings = append(res.Warnings, VaultHint(in.Provider))
	}

	// ---- mappings/default.kv ----
	if opt.WriteProfile {
		kv := filepath.Join(paths.Mappings(), ProfileName+".kv")
		if _, err := os.Stat(kv); err == nil && !opt.Overwrite {
			res.Warnings = append(res.Warnings, i18n.T(
				"{file} already exists and was left alone; models were not re-pointed",
				i18n.A{"file": kv}))
		} else {
			text := ProfileKV(in)
			if err := store.Write(kv, []byte(text)); err != nil {
				return res, err
			}
			// 同名 .json 并存时 **.kv 赢**（见 store 的读取次序）。留着它就是一个
			// 陷阱：用户下次去看 .json，读到的是一份不起作用的旧绑定。改名成 .bak
			// 与内核 `profile kv --write` 是同一种做法。
			if old := filepath.Join(paths.Mappings(), ProfileName+".json"); fileExists(old) {
				bak := old + ".bak"
				if _, err := os.Stat(bak); err == nil {
					bak = fmt.Sprintf("%s.%d.bak", old, len(res.Files))
				}
				if err := os.Rename(old, bak); err == nil {
					res.Notes = append(res.Notes, i18n.T("moved {old} aside to {bak} (the .kv wins)",
						i18n.A{"old": old, "bak": bak}))
				}
			}
			res.Files = append(res.Files, kv)
			res.Notes = append(res.Notes, i18n.T("wrote profile {name} ({n} tiers → {model})",
				i18n.A{"name": ProfileName, "n": len(domain.Roles), "model": in.Model}))
		}
	}

	// ---- state.json ----
	if opt.SetDefault {
		if err := store.SetDefaultProfile(ProfileName, false); err != nil {
			return res, err
		}
		res.Files = append(res.Files, paths.StateFile())
		res.Notes = append(res.Notes, i18n.T("global default → {name}", i18n.A{"name": ProfileName}))
	}
	res.Files = dedupe(res.Files)
	return res, nil
}

// ProfileKV 是写进 default.kv 的那段文本。
//
// 注释是**给下一个人看的**（往往就是用户自己，或者他让 AI 去改配置）——所以它
// 写清楚「这四个档位为什么是同一个模型」以及怎么细化，而不是只留一句
// 「generated」。生成物也要能被人读懂，否则用户的第一步就是去改它却不知道能改哪里。
func ProfileKV(in Bootstrap) string {
	var b strings.Builder
	b.WriteString("# newgate bootstrap profile — written by `newgate config`.\n")
	b.WriteString("# Every tier points at the same model so that the whole chain works out of the box.\n")
	b.WriteString("# Later, point tiers at different models (or providers) one line at a time:\n")
	b.WriteString("#   normal = other-provider/a-strong-model\n")
	b.WriteString("#   mid    = other-provider/a-cheap-model\n")
	b.WriteString("# `newgate tui` edits the same file interactively.\n")
	b.WriteString("desc=" + i18n.T("bootstrap chain written by newgate config", nil) + "\n")
	binding := in.Provider + "/" + in.Model
	for _, tier := range domain.Roles {
		b.WriteString(tier + "=" + binding + "\n")
	}
	return b.String()
}

// VaultHint 是「key 已经写到磁盘上了」那句提醒。
func VaultHint(provider string) string {
	return i18n.T("the key is stored in providers.json (mode 0660, readable by your group); to keep it out of the file, put it in the environment as {env} and set api_key_env instead",
		i18n.A{"env": envName(provider)})
}

// envName 是「这个 provider 的 key 环境变量」的约定名（与内核 init 的提示一致：
// NEWGATE_KEY_<PROVIDER 大写，- 换 _>）。
func envName(provider string) string {
	up := strings.ToUpper(provider)
	up = strings.NewReplacer("-", "_", ".", "_", " ", "_").Replace(up)
	return "NEWGATE_KEY_" + up
}

// SuggestedProvider 从 base URL 推出一个干净的名字（用户可以改）。
func SuggestedProvider(base string, taken map[string]bool) string {
	host := base
	if i := strings.Index(host, "://"); i >= 0 {
		host = host[i+3:]
	}
	if i := strings.IndexAny(host, "/?#"); i >= 0 {
		host = host[:i]
	}
	if i := strings.IndexByte(host, '@'); i >= 0 {
		host = host[i+1:]
	}
	if i := strings.IndexByte(host, ':'); i >= 0 { // 端口
		host = host[:i]
	}
	parts := strings.Split(host, ".")
	// 纯数字（IPv4）整条留着：`127.0.0.1` 拆成 `127` 是个看不懂的名字。
	allNumeric := len(parts) >= 2
	for _, p := range parts {
		for _, r := range p {
			if r < '0' || r > '9' {
				allNumeric = false
				break
			}
		}
	}
	if allNumeric {
		return sanitize(strings.ReplaceAll(host, ".", "-"))
	}
	// 跳过 www / api 这类没有信息量的标签（`api.rvcompute.com` → `rvcompute`）。
	i := 0
	for i < len(parts)-1 && (parts[i] == "www" || parts[i] == "api" || parts[i] == "gateway" || parts[i] == "llm") {
		i++
	}
	name := sanitize(parts[i])
	if name == "" {
		name = "provider"
	}
	// 撞名就加序号——静默覆盖别人的 provider 是这里最不该发生的事。
	cand := name
	for n := 2; taken != nil && taken[cand]; n++ {
		cand = fmt.Sprintf("%s-%d", name, n)
	}
	return cand
}

func sanitize(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '-' || r == '_' || r == '.':
			b.WriteRune('-')
		}
	}
	return strings.Trim(b.String(), "-")
}

// normalizeBase 把用户贴进来的地址收敛成我们存的那种写法。
//
// 用户从各种文档里复制：有的带 `/v1`，有的不带，有的带尾斜杠，有的把整个
// `…/v1/chat/completions` 都带上了。forward 那一层按「base + 后缀」拼，
// 所以多余的尾斜杠与末尾的端点必须在这里去掉——否则转发会打到
// `…/chat/completions/chat/completions`。
func normalizeBase(s string) string {
	s = strings.TrimSpace(s)
	for _, tail := range []string{"/chat/completions", "/messages", "/responses", "/models"} {
		s = strings.TrimSuffix(s, tail)
	}
	return strings.TrimRight(s, "/")
}

func protocolOr(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "anthropic":
		return "anthropic"
	default:
		return "openai"
	}
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

func dedupe(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

// portOf 把「配置里写的端口」收敛成一个能用的值（0/负数 = 默认 8899）。
//
// 单独成函数是为了让向导的那几条判断能在测试里喂一个端口进去，而不必先写一份
// state.json——那几个测试要验的是分支，不是 store 的读写。
func portOf(port int) int {
	if port > 0 {
		return port
	}
	return domain.ProxyPort
}
