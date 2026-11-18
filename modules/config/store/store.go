// Package store 是 Config Store：磁盘上的单一事实源。
//
// 职责边界（docs/03-architecture.md）：
//   - core 只有类型和纯逻辑，一行 IO 都没有
//   - store 负责所有读写、原子替换、密钥的环境变量查找、schema 校验
//   - 上层拿到的是**不可变快照**，避免边解析边读文件带来的竞态
package store

import (
	crand "crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/rzbdz/newgate-ext/modules/config/domain"
	"github.com/rzbdz/newgate-ext/modules/config/paths"
	"github.com/rzbdz/newgate-ext/modules/config/roleprov"
	"github.com/rzbdz/newgate/lib/i18n"
)

// Snapshot 一次性读齐的配置快照。解析阶段只看它，不再回头读盘。
type Snapshot struct {
	Providers *domain.Providers
	Profiles  []*domain.Profile
	State     *domain.State
}

// Load 读取完整快照。坏掉的 profile 文件跳过而不是让整体失败——
// 一个手改坏的文件不该让所有 agent 停摆；doctor 会报出来。
func Load() (*Snapshot, error) {
	// 模块贡献的动态角色键（omo 的 omo-sisyphus / cat-deep …）跟着快照一起
	// 刷新：它们也是配置——键是谁、缺省跟哪一档走，都写在模块自己的文件里。
	// 读失败不挡住加载（失败开放）：那个模块的键退化成「没注册」，
	// 用户在 `newgate omo` / doctor 里能看到原因。
	_ = roleprov.Refresh()

	provs, err := LoadProviders()
	if err != nil {
		return nil, err
	}
	names, err := ListProfiles()
	if err != nil {
		return nil, err
	}
	var ps []*domain.Profile
	for _, n := range names {
		if p, err := LoadProfile(n); err == nil {
			ps = append(ps, p)
		}
	}
	return &Snapshot{Providers: provs, Profiles: ps, State: LoadState()}, nil
}

// ---------- 原子写 ----------

// writeJSON 落一份 JSON 配置：序列化，然后交给 writeFileAtomic 那条唯一的写路径。
//
// **不要在这里自己拼 `path + ".tmp"`**——那是这一段原来的写法，也正是
// writeFileAtomic 当初被改掉的那个 bug：固定临时名会让两个并发写者撞在同一个
// inode 上，一个 rename 走了，另一个 rename 的是一份被对方写了一半的内容。两份
// 实现并存，只会让其中一份慢慢腐坏（这一份当时就腐坏了），所以这里直接调它。
//
// 权限不在参数里了：能到这里的调用方一律要 0660（配置目录是多人共享读的），
// writeFileAtomic 本来就落这个值，多一个参数只会多一种写法。
func writeJSON(path string, v interface{}) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	return writeFileAtomic(path, b)
}

// Write 原子地覆盖一份配置文件，并在替换之前把当前那份抄进历史环。
//
// 给**不走 CAS 的写**用：`newgate profile kv --write` 这类由人当场发起的转换没有
// 「加载时的基线」可比，但它仍然必须走这一条路径——直接 os.WriteFile 会让进程死在
// 半路时留下一份被截断的配置，而且**那一版连备份都没有**（备份恰恰是为一件事存在
// 的）。判据是代价不对称：多一次 rename 换掉「一次截断 = 配置没了」。
func Write(path string, data []byte) error { return writeFileAtomic(path, data) }

func readJSON(path string, v interface{}) error {
	b, err := ioutil.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}

// ---------- providers ----------

// LoadProviders 读 provider 表，并把 api_key_env 指向的环境变量解析成明文。
//
// 这个解析放在 store 而不是 core：读环境变量是 IO，core 必须保持纯净
// （domain.Provider.Key() 因此只返回 APIKey 字段）。
func LoadProviders() (*domain.Providers, error) {
	p := &domain.Providers{Providers: map[string]domain.Provider{}}
	if err := readJSON(paths.ProvidersFile(), p); err != nil {
		return nil, err
	}
	if p.Providers == nil {
		p.Providers = map[string]domain.Provider{}
	}
	for name, prov := range p.Providers {
		if prov.APIKey == "" && prov.APIKeyEnv != "" {
			if v := os.Getenv(prov.APIKeyEnv); v != "" {
				prov.APIKey = v
				p.Providers[name] = prov
			}
		}
	}
	return p, nil
}

// SaveProviders 写 provider 表。
//
// 0660 而不是 0600：共享部署模型（同一台机器、developer 组的多用户管理
// 同一个 newgate，见 docs/03）里组员要能读它——`newgate status` / wrapper
// 都要解析 provider。组边界就是信任边界；单用户部署想收紧，chmod 0600
// providers.json 即可，代码不会把它改回去（只在写入时用这个 mode）。
// 注意：不能把 LoadProviders 解析出来的环境变量值写回去，否则密钥就落盘了。
// 调用方必须传入原始形态。
func SaveProviders(p *domain.Providers) error {
	return writeJSON(paths.ProvidersFile(), p)
}

// ---------- profiles ----------

// LoadProfile 读一个 profile，**含 extends 合并**（变体只写差异项，
// 其余从 base 继承，见 domain.Profile.MergeFrom）。
//
// 文件形态两种：`name.json`（canonical，程序写）或 `name.kv`（手写友好，
// 见 kv.go）。并存时 **.kv 赢**——kv 只会被人刻意创建，它出现就是最新的
// 意图；`newgate profile kv --write` 转换时会把旧 json 改名 .bak。
func LoadProfile(name string) (*domain.Profile, error) {
	return loadProfile(name, map[string]bool{})
}

func loadProfile(name string, loading map[string]bool) (*domain.Profile, error) {
	if loading[name] {
		return nil, i18n.E("extends cycle: {name} points back to itself", i18n.A{"name": name})
	}
	pr, err := readProfileFile(name)
	if err != nil {
		return nil, err
	}
	if pr.Name == "" {
		pr.Name = name
	}
	if pr.Extends == "" {
		return pr, nil
	}
	loading[name] = true
	base, err := loadProfile(pr.Extends, loading)
	delete(loading, name)
	if err != nil {
		return nil, i18n.Ef(err, "cannot resolve extends {name}: {err}", i18n.A{"name": pr.Extends})
	}
	pr.MergeFrom(base)
	pr.Extends = "" // 已合并，解析后的视图不再背 extends 声明
	return pr, nil
}

func readProfileFile(name string) (*domain.Profile, error) {
	if b, err := ioutil.ReadFile(filepath.Join(paths.Mappings(), name+".kv")); err == nil {
		return ParseProfileKV(string(b))
	}
	var pr domain.Profile
	if err := readJSON(filepath.Join(paths.Mappings(), name+".json"), &pr); err != nil {
		return nil, err
	}
	return &pr, nil
}

// LoadProfileRaw 读**未合并**的原始 profile（extends 声明原样保留）。
// 给编辑/转换用：对一个紧凑的派生声明改一个字段，不该把它展开成全量。
func LoadProfileRaw(name string) (*domain.Profile, error) {
	pr, err := readProfileFile(name)
	if err != nil {
		return nil, err
	}
	if pr.Name == "" {
		pr.Name = name
	}
	return pr, nil
}

// SaveProfile 写 profile。不含密钥（只有 provider/model 绑定），组内共享读
// 没有暴露面：wrapper 启动（launch.Launch）本就要解析它来注入真实模型名。
func SaveProfile(pr *domain.Profile) error {
	return writeJSON(filepath.Join(paths.Mappings(), pr.Name+".json"), pr)
}

func ListProfiles() ([]string, error) {
	ents, err := ioutil.ReadDir(paths.Mappings())
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var out []string
	for _, e := range ents {
		if e.IsDir() {
			continue
		}
		var n string
		switch {
		case strings.HasSuffix(e.Name(), ".json"):
			n = strings.TrimSuffix(e.Name(), ".json")
		case strings.HasSuffix(e.Name(), ".kv"):
			n = strings.TrimSuffix(e.Name(), ".kv")
		default:
			continue
		}
		// 同名 json/kv 只出一个名字（谁赢见 LoadProfile）
		if !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	sort.Strings(out)
	return out, nil
}

// ---------- state ----------

// ValidateState 只回答一件事：state.json **读得出来吗**。
//
// 为什么需要它（2026-09-18）：LoadState 是 fail-open 的（读不出来就给零值），
// 那是对的——一个手改坏的文件不该让所有 agent 停摆。但零值 State 里有两个字段
// 要命：`DefaultProfile == ""` 让链头是个不存在的 profile（每个请求「无可用候选」），
// `Port == 0` 让 daemon 监听随机端口（CLI 与客户端按配置里的端口找它，于是
// 「代理在跑但连不上」）。而 doctor 的「文件」那一项原来只 os.Stat——**存在即 ok**，
// 于是一个半截 JSON 会被列进 ok 里，doctor 打「全部通过」。
//
// 用户手上于是是一组自相矛盾的现象：配置件件都在、doctor 全绿、但没有候选 /
// 连不上。这一项就该在他会去看的地方报出来。
func ValidateState() error {
	b, err := ioutil.ReadFile(paths.StateFile())
	if err != nil {
		return err
	}
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(b, &probe); err != nil {
		return err
	}
	if len(probe) == 0 {
		return i18n.E("the file is empty", nil)
	}
	// 顶层能解析还不够：真正要的是它能填进 domain.State（字段类型对得上）。
	var st domain.State
	return json.Unmarshal(b, &st)
}

func LoadState() *domain.State {
	s := &domain.State{}
	if b, err := ioutil.ReadFile(paths.StateFile()); err == nil {
		if json.Unmarshal(b, s) == nil {
			var fields map[string]json.RawMessage
			if json.Unmarshal(b, &fields) == nil {
				known := stateJSONFields()
				s.ModuleConfig = make(map[string][]byte)
				for name, raw := range fields {
					if !known[name] {
						s.ModuleConfig[name] = append([]byte(nil), raw...)
					}
				}
			}
		}
	}
	s.Normalize()
	return s
}

func stateJSONFields() map[string]bool {
	typ := reflect.TypeOf(domain.State{})
	fields := make(map[string]bool, typ.NumField())
	for i := 0; i < typ.NumField(); i++ {
		tag := typ.Field(i).Tag.Get("json")
		name := strings.SplitN(tag, ",", 2)[0]
		if name != "" && name != "-" {
			fields[name] = true
		}
	}
	return fields
}

func SaveState(s *domain.State) error {
	b, err := StateBytes(s)
	if err != nil {
		return err
	}
	// 临时文件名随机、同目录（见 writeFileAtomic）：固定名 "<path>.tmp" 是两个
	// 并发写者会撞上的名字，撞上就是「你 rename 了别人的字节」。
	return writeFileAtomic(paths.StateFile(), b)
}

// StateBytes 是 SaveState 会写下去的那份字节：已知字段 + 各模块自己那段原文。
//
// 导出它是因为**写这份文件的路径不止一条**（命令行改档位/改开关点，浏览器改同一个
// 开关点），而安全的那条是「读基线 → 比对 → 原子写」（WriteIfUnchanged）。只给一个
// SaveState 的话，第二个写者要么绕开基线检查直接写（把别人的改动盖掉，而且是静默
// 的），要么自己拼一遍序列化——拼漏了 ModuleConfig，别人的模块段就整段消失。
//
// 序列化的规则只在这里有一份：哪些键是已知字段、哪些是别人寄存的原文。
func StateBytes(s *domain.State) ([]byte, error) {
	typed, err := json.Marshal(s)
	if err != nil {
		return nil, err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(typed, &fields); err != nil {
		return nil, err
	}
	for name, raw := range s.ModuleConfig {
		if _, owned := fields[name]; !owned {
			fields[name] = append(json.RawMessage(nil), raw...)
		}
	}
	b, err := json.MarshalIndent(fields, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

// NewControlToken 生成控制端点令牌（crypto/rand，48 位十六进制）。
func NewControlToken() string {
	b := make([]byte, 24)
	if _, err := crand.Read(b); err != nil {
		// rand 失败几乎只在早期 boot 的虚拟机里发生；退化到时间熵也比
		// 空令牌（= 端点永远 403）好
		return fmt.Sprintf("%x", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}

// EnsureControlToken 幂等地保证 state.json 里有控制令牌，返回最新 state。
//
// 必须在 daemon.Spawn **之前**调用（cmdStart / launch.Launch / Serve 各自
// 兜一次底）：令牌先落盘，daemon 起来时 watcher 的初次加载就能读到，
// 不存在「daemon 拿着空令牌跑着」的窗口。之后所有 LoadState→SaveState
// 的调用方（--set-profile 等）都从盘上重新读，令牌不会被冲掉。
// 写盘失败**必须报出去**：令牌写不出去时 /__newgate/stop 与 /__newgate/upgrade
// 会拒掉所有请求（跨用户停机就没了），而调用方原来拿不到任何信号。与
// CLAUDE.md §3.1 记的那个 umask 权限坑是同一条现场。
func EnsureControlToken() (*domain.State, error) {
	s := LoadState()
	if s.ControlToken != "" {
		return s, nil
	}
	s.ControlToken = NewControlToken()
	if err := SaveState(s); err != nil {
		return s, i18n.Ef(err, "cannot write the control token: {err}", nil)
	}
	return s, nil
}

// SetActiveProfile 设置某个 agent 的链头。agent 为空 = 设全局默认。
//
// 校验存在后才写，**绝不静默回落**——那正是「切了没生效」这类故障的来源。
func SetActiveProfile(agent, profile string) error {
	if _, err := LoadProfile(profile); err != nil {
		avail, _ := ListProfiles()
		return i18n.E("no such profile: {name} (available: {list})",
			i18n.A{"name": profile, "list": strings.Join(avail, ", ")})
	}
	s := LoadState()
	if agent == "" {
		s.DefaultProfile = profile
	} else {
		if s.Active == nil {
			s.Active = map[string]string{}
		}
		s.Active[agent] = profile
	}
	return SaveState(s)
}

// ClearActiveProfile 让某个 agent 回落到全局默认。
func ClearActiveProfile(agent string) error {
	s := LoadState()
	delete(s.Active, agent)
	return SaveState(s)
}

// SetDefaultProfile 改全局默认；clearAgents 为真时顺带把每个 agent 的链头清掉。
//
// 两件事**一次写完**（一次 Load + 一次 Save）：分两次的话，中间那一刻 state.json
// 是半成品——默认已经改了、客户端还没收敛——而另一次编辑恰好落在那一刻就会被
// 后一次 Save 整个盖掉。这与「保存是 CAS」那条防的是同一类事故。
func SetDefaultProfile(profile string, clearAgents bool) error {
	if _, err := LoadProfile(profile); err != nil {
		avail, _ := ListProfiles()
		return i18n.E("no such profile: {name} (available: {list})",
			i18n.A{"name": profile, "list": strings.Join(avail, ", ")})
	}
	s := LoadState()
	s.DefaultProfile = profile
	if clearAgents {
		s.Active = nil
	}
	return SaveState(s)
}

// ClearAllAgentProfiles 让**每一个** agent 都回落到全局默认，返回清掉了几条。
//
// 它是「force apply」那件事的落点：改全局默认 profile 时，单独设过的那些 agent
// 不会跟着走——那是**对的**（它们是被明确指定过的），但用户常常想要的是「全部
// 收敛回跟随，然后我只要维护一个默认」。逐个客户端去点一遍是十六次操作，而这个
// 动作就是那件事本身。
//
// 返回条数是为了让调用方能说一句「清了 N 条」：一个把十六行配置删掉的动作，
// 如果屏幕上什么都不说，用户没法确认它到底做了没有。
func ClearAllAgentProfiles() (int, error) {
	s := LoadState()
	n := len(s.Active)
	if n == 0 {
		return 0, nil
	}
	s.Active = nil
	return n, SaveState(s)
}

// SetTakeoverWanted 记下「用户要不要接管这个 agent」（期望态）。
// 只改期望态，不碰磁盘——具体怎么接管是 runtime/takeover 的事。
func SetTakeoverWanted(agent string, want bool) error {
	s := LoadState()
	if s.Takeover == nil {
		s.Takeover = map[string]bool{}
	}
	if want {
		// 默认就是想接管，所以「想要」用删除条目表示，state.json 保持干净
		delete(s.Takeover, agent)
	} else {
		s.Takeover[agent] = false
	}
	if len(s.Takeover) == 0 {
		s.Takeover = nil
	}
	return SaveState(s)
}

// ---------- 校验 ----------

// Validate 检查所有 profile 引用的 provider 都存在、都有 key。
func Validate() []string {
	var problems []string
	provs, err := LoadProviders()
	if err != nil {
		return []string{i18n.T("cannot read providers.json: {err}", i18n.A{"err": err.Error()})}
	}
	names, _ := ListProfiles()
	for _, n := range names {
		pr, err := LoadProfile(n)
		if err != nil {
			problems = append(problems,
				i18n.T("profile {name}: {err}", i18n.A{"name": n, "err": err.Error()}))
			continue
		}
		for tier, cands := range pr.Roles {
			for _, b := range cands {
				p, ok := provs.Providers[b.Provider]
				if !ok {
					problems = append(problems, i18n.T(
						"profile {profile} role {role}: provider {provider} is not defined",
						i18n.A{"profile": n, "role": tier, "provider": b.Provider}))
					continue
				}
				if p.Key() == "" {
					hint := i18n.T("set api_key in providers.json", nil)
					if p.APIKeyEnv != "" {
						hint = i18n.T("set the environment variable {env} or fill in api_key",
							i18n.A{"env": p.APIKeyEnv})
					}
					problems = append(problems, i18n.T("provider {provider} has no key → {hint}",
						i18n.A{"provider": b.Provider, "hint": hint}))
				}
				if b.Model == "" {
					problems = append(problems,
						i18n.T("profile {profile} role {role}: model is empty",
							i18n.A{"profile": n, "role": tier}))
				}
			}
		}
	}
	return problems
}
