package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/rzbdz/newgate-ext/lib/view"
	"github.com/rzbdz/newgate-ext/modules/config/domain"
	"github.com/rzbdz/newgate-ext/modules/config/paths"
	"github.com/rzbdz/newgate-ext/modules/config/store"
	modules "github.com/rzbdz/newgate/component"
	i18n "github.com/rzbdz/newgate/lib/i18n"
)

// 配置模块贡献给 web 界面的三样东西：**档位映射**（每个 profile 一张绑定编辑器）、
// **源文件**（硬核用户直接改文件那条 tab）、**全局开关**（默认 profile 这类）。
//
// 为什么由本模块贡献而不是让界面自己读：界面一旦自己读 store，它就必须知道
// profile 文件的字段、mappings 目录的布局、哪些文件带凭据不能写回——那样加一个
// 模块的界面就要改界面。这条规矩与 cli/extension 那边一字不差（见那里关于
// 「status 要显示非出厂态的开关点」的那段），只是这次的服务端是浏览器。
//
// **写的知识也在这里**（每个概念带 Apply）：改档位要「只动 roles 那一段、别的键
// 原样保留」，这件事只有知道 profile 文件长什么样的人做得对。

// registerView 把本模块的概念挂到界面上。没有界面（没装 web-dashboard 或者
// 那是个 CLI 进程）时什么都不做——配置照常工作，只是没有 web 入口。
//
// 登记时**一个文件都不读**：产出函数 `concepts` 要等到真的有人来看界面才跑。
// 理由是这套东西的正确性而不是性能——每条 `newgate …` 命令都会跑到这里，而其中
// 绝大多数没有人会打开界面（见 lib/view 的包注释）。
func registerView(v view.Service) (modules.Release, error) {
	// 「再建一份档位文件」挂在**栏目**上，不是挂在某张卡上（见 view.Section.Actions）：
	// 新建出来的那一份此刻还没有概念，所以没有哪张卡能挂这个按钮。挂在栏目上还有一个
	// 好处——它**永远够得着**，不管你现在正看着哪一张卡。
	create := view.Action{
		ID:    "new-profile",
		Label: func() string { return i18n.T("+ profile", nil) },
		Run:   newProfileFile,
	}
	// 「force apply」：把**每一个**客户端的链头清成「跟随全局缺省」。
	//
	// 它挂在**栏目**上而不是某张卡上，有两个理由，两个都不是排版：
	//
	//   - 它动的不是某一格，而是**所有客户端**（今天三家，将来更多）。挂到某一家的卡
	//     上，那个按钮就会在一个它管不着的地方；挂到「全局设置」那张卡上，读起来又像
	//     「这一格的一个选项」，而它不是——它是一次批量收敛。
	//   - 界面不认识「客户端」这件事（那是 confighook 的词汇）。让顶栏那个通用保存
	//     按钮多一个「force」含义，就等于让界面知道「清掉 active 是什么意思」——
	//     正是这套设计要避免的那种耦合。
	//
	// 用例是一句话：**改完全局默认，想让所有客户端都跟着它**。单独设过的客户端
	// 不跟着走是对的（它们被明确指定过），但「全部收敛回跟随，然后我只维护一个
	// 默认」是常见诉求，而逐个去点一遍要十六次操作。
	applyAll := view.Action{
		ID: "apply-default-all",
		// 它挂在**栏目**上，所以宾语得自己写全：旁边没有哪张卡告诉读的人「应用什么」。
		// （卡片上那一对不一样，见 profileActions——那里的宾语是那张卡。）
		Label: func() string { return i18n.T("apply the default to all clients", nil) },
		Run:   applyDefaultToAll,
	}
	return v.Register("config",
		view.Title(func() string { return i18n.T("Configuration", nil) }).
			Of(view.FieldSettings).
			Does(create).Does(applyAll), concepts)
}

// applyDefaultToAll 清掉所有 per-agent 链头（见 store.ClearAllAgentProfiles）。
//
// 它不改全局默认本身：用户要的是「大家都跟着它」，不是「把它改成别的」。
//
// 一条都不存在时**也算成功**（返回 nil）：那正是用户想要的状态，报一个错只会让人
// 以为没生效。界面做完动作会重读快照，各家的链头那一格会自己显示成「跟随缺省」。
func applyDefaultToAll() (string, error) {
	if _, err := store.ClearAllAgentProfiles(); err != nil {
		return "", i18n.Ef(err, "cannot clear the per-client profiles: {err}", nil)
	}
	return "", nil
}

// newProfileFile 建一份空的档位文件，名字自己挑（第一个没被占用的）。
//
// 为什么名字由**后端**挑（原来是界面挑的）：只有这里知道哪几个名字已经被占了，
// 也知道「一份档位可以叫 .kv 也可以叫 .json」（见 store.ListProfiles 的去重）——
// 界面要挑名字就得把这两条知识各抄一份，而抄来的知识必然漂移。
//
// 为什么用 `WriteIfUnchanged(…, "")`（要求「此刻不存在」）：两个标签页同时按下这个
// 按钮时，后到的那个撞上 StaleError 而不是把前一个建好的覆盖掉——重名撞车是这里
// 唯一真实的并发场景，而它的代价是「我刚建的那份没了」。
func newProfileFile() (string, error) {
	taken := map[string]bool{}
	for _, n := range profileNames() {
		taken[n] = true
	}
	for i := 1; i <= 100; i++ {
		name := "new-profile"
		if i > 1 {
			name = fmt.Sprintf("new-profile-%d", i)
		}
		if taken[name] {
			continue
		}
		file := filepath.Join(paths.Mappings(), name+".kv")
		body := []byte(store.SerializeProfileKV(&domain.Profile{Name: name}))
		_, err := store.WriteIfUnchanged(file, "", body)
		if err == nil {
			// 交回新卡的名字：界面据此切过去（「新建的下一步一定是去填它」）。
			return "config.profile." + name, nil
		}
		var stale *store.StaleError
		if !errors.As(err, &stale) {
			// 写不进去（目录权限之类）——报出来，别拿它当撞名一路重试到 100。
			return "", i18n.Ef(err, "cannot create {file}: {err}", i18n.A{"file": filepath.Base(file)})
		}
		// 名字刚被别人占了：换下一个再试。
	}
	return "", i18n.E("could not find a free name for a new profile file", nil)
}

// concepts 是「此刻配置的样子」：state.json 里归本模块的那几个字段、每个 profile
// 一张绑定编辑器、每个源文件一条。
//
// 它每次被调用都重新读盘，所以界面上的刷新是真的刷新——CLI 刚建的档位文件会在
// 下一次快照里出现。单个东西读不出来（文件删了、JSON 坏了）不牵连同组的别人：
// 那一张卡片带 Broken 说明，其余照常。
func concepts() ([]view.Concept, error) {
	var out []view.Concept
	// **顺序是产品决定，不是字母序**（见 view.Concept.Order）：先配上游、再挑档位。
	// 字母序会把 `config.profile.*` 排到 `config.providers` 前面，于是左栏第一屏
	// 全是档位文件、而上游配置在第一屏之外——而「先有上游才选得出绑定」这件事与
	// 操作的先后是同一件事。
	out = append(out, stateConcept())     // Order 0：全局设置
	out = append(out, providersConcept()) // Order 1：上游
	// 一个档位一份文件，一份文件一张卡——**改档位、改继承、改名、删除、新增**全都
	// 在这张卡上（见 applyProfileRoles）。2026-09-20 之前这里还有一张「档位文件」
	// 目录卡：它列的文件与下面这些卡一一对应，是同一件事说两遍（用户的原话是
	// 「这个多余的啊」），而它独有的那点功能（新增/改名/删除）现在都在卡自己身上。
	names := profileNames()
	for _, name := range names {
		out = append(out, profileConcept(name, names))
	}
	out = append(out, fileConcepts()...)
	return out, nil
}

// profileFamily 说一个档位属于哪一族（左栏分组用）。
//
// 规则一条：把名字按 "-" 从左往右切，**切到的那一段本身是一个存在的档位**时，
// 它就是这一族的名字（`claude-cheap` → `claude`，`minimax-fast` → `minimax`）。
// 切不出来（没有 `demo-*` 这种兄弟）就用自己的名字——自己一族。
//
// 为什么要有它：档位是**一族一族**长出来的（先 `claude`，再派生出 `claude-cheap`），
// 而左栏平铺十六行之后，一眼看不出谁是谁的变体。分组的判据必须是「那一族真的存在」
// 而不是「名字里有横杠」：横杠在档位名里很常见（`gpt-4.1-mini`），凭它分组会造出
// 一堆并不存在的家族。
func profileFamily(name string, all []string) string {
	exists := make(map[string]bool, len(all))
	for _, n := range all {
		exists[n] = true
	}
	// 逐个扫描字符，遇到一个 "-" 就试一次它左边那一段。
	//
	// **不要把它写成 `for i := strings.Index(name, "-"); i > 0; i = strings.Index(name[i+1:], "-") + i + 1`**
	// ——那是这一段原来的写法，而它有一个会**把整个 daemon 挂死**的错：`name[i+1:]`
	// 里再也没有 "-" 时 `Index` 回 -1，`-1 + i + 1` 恰好等于 `i`，于是下标原地踏步、
	// 循环永不退出。触发条件平常到不能再平常——界面上的「＋新建档位」造出来的名字
	// 就是 `new-profile`：它在 `new` 之后没有第二个横杠，而 `new` 又不是一个已有的
	// 档位，于是**第一次刷新快照就死循环**（2026-09-20 实测：CPU 打满、`/ui/api/snapshot`
	// 一个字节都不回、浏览器那层「新建之后切到新卡」永远不成立）。
	//
	// 症状之所以难认，是因为挂的是**读快照**这条所有人共用的路：界面整个卡住，
	// 而磁盘上文件建得好好的——看起来像前端没刷新，不像后端在空转。
	for i := 0; i < len(name); i++ {
		if name[i] == '-' && exists[name[:i]] {
			return name[:i]
		}
	}
	return name
}

// brokenConcept 是「这东西现在读不出来」的那张卡片：身份照报（前端才知道少了
// 谁），数据没有，也写不回去。
//
// 为什么不干脆不报这个概念：用户会以为那个档位不存在，然后去别处找。为什么不让
// 整次快照失败：一个坏文件让整个界面白屏，代价远大于它。
func brokenConcept(id, kind, title string, err error) view.Concept {
	return view.Concept{ID: id, Kind: kind, Title: title, Broken: err.Error()}
}

// ---------- 档位映射 ----------

type bindingData struct {
	Provider string `json:"provider,omitempty"`
	Model    string `json:"model,omitempty"`
	Ref      string `json:"ref,omitempty"`
}

type roleData struct {
	ID       string        `json:"id"`
	Bindings []bindingData `json:"bindings"`
}

type providerChoice struct {
	Name     string   `json:"name"`
	Protocol string   `json:"protocol,omitempty"`
	BaseURL  string   `json:"base_url,omitempty"`
	KeyEnv   string   `json:"key_env,omitempty"`
	HasKey   bool     `json:"has_key"`
	Models   []string `json:"models"`
}

// profileData 是绑定编辑器的全部素材。
//
// 带上 providers 是刻意的：**候选是「provider × model」的组合**，界面要让人用
// 下拉框选而不是凭记忆敲字符串。这份清单从配置里来（各家声明过的模型），不问
// 上游——探活是 `newgate probe` 的事，界面加载不该去碰网络。
type profileData struct {
	Profile     string `json:"profile"`
	File        string `json:"file"` // 相对配置根；保存时原样回传
	Base        string `json:"base"` // 基线（内容哈希），见 store.WriteIfUnchanged
	Description string `json:"description,omitempty"`
	Default     bool   `json:"default"`
	Pinned      bool   `json:"pinned,omitempty"`
	Excluded    bool   `json:"excluded,omitempty"`
	Extends     string `json:"extends,omitempty"`
	// ExtendsOptions 是可选的父档位（别的档位文件名）。**它必须由后端给**：
	// 界面不认识「档位文件」这件事，它只知道「这一格有个下拉，选项是这些」。
	ExtendsOptions []string         `json:"extends_options"`
	Roles          []roleData       `json:"roles"`
	Providers      []providerChoice `json:"providers"`
}

func profileConcept(name string, all []string) view.Concept {
	conceptID := "config.profile." + name
	file, err := profileFile(name)
	if err != nil {
		return brokenConcept(conceptID, view.KindMapping, name, err)
	}
	// 读**未合并**的原文：对一个紧凑的派生声明（extends）改一个字段，不该把它
	// 展开成全量——那会让用户在界面上点一下保存，文件就膨胀十倍。
	pr, err := store.LoadProfileRaw(name)
	if err != nil {
		return brokenConcept(conceptID, view.KindMapping, name, err)
	}
	title := name
	if pr.Description != "" {
		title = name + " — " + pr.Description
	}
	return view.Concept{
		ID: conceptID, Kind: view.KindMapping, Title: title,
		// Order 10：档位文件排在「上游 / 全局设置」之后（见 concepts 的注释）。
		//
		// Group 是**两级**的（见 view.Concept.Group）：大档 `档位`，族由
		// profileFamily 算（`claude-cheap` 缩在 `claude` 底下）。为什么要有大档
		// 那一段：左栏里「全局设置 / 上游」是**装完就要配的两项**，而档位是**一天天
		// 加出来的一堆**——不分开的话，那两项与十六个档位平铺在一起，谁也看不出
		// 这是两类东西（用户的原话：「profile 这里就可以做一个分栏了啊」）。
		//
		// 大档的名字走 i18n（它是给人看的），族名是档位名本身（机器标记，不翻）。
		Order: 10, Group: i18n.T("Profiles", nil) + "/" + profileFamily(name, all),
		Data: profileView(name, file, pr),
		Apply: func(edit json.RawMessage, base string) (string, error) {
			return applyProfileRoles(conceptID, file, edit, base)
		},
		// Preview：原文那一半的草稿长这样时，这张控件卡该显示成什么样（见
		// view.Concept.Preview）。
		//
		// 为什么这张卡需要它：用户粘一整份档位进原文那一栏、再想用一个下拉框调一档
		// ——没有 Preview 的话，控件那一栏手里还是**改之前**那份内容，他那一下编辑
		// 交上去的是整份旧表，刚粘的东西当场没了，而屏幕上一直没显示过它，所以他
		// 不会觉得自己正在覆盖什么（见 App.svelte 里 lastEdit 那段）。
		Preview: func(draft []byte) (any, error) {
			p, err := parseProfileDraft(file, draft)
			if err != nil {
				return nil, err
			}
			return profileView(name, file, p), nil
		},
		Actions: profileActions(name),
		Note:    profileNote(name),
	}
}

// profileNote 说这张档位卡此刻**是不是正在生效的那一份**。
//
// 是 → 一句绿色的「当前配置」，不是 → 不画任何东西。
//
// # 为什么不能只靠「按钮不画」表达这件事（2026-09-21 用户指出）
//
// 第一版就是那样：已经生效的那一张不挂那对按钮（理由见 profileActions），于是
// 十六张档位卡里有十五张长着两个按钮、一张什么都没有。用户的原话是：
//
//	你应该注入的是一个 info only 的绿色：当前配置 / Active profile 的效果啊！
//
// 他说得对，而且指出的是一个**信息缺口**：「这份在生效吗」是他打开这一节最想
// 知道的事，而当时的界面只用一个**缺席**去暗示它——缺席是看不出来的，两张卡
// 长得几乎一样，唯一的差别是少两个按钮。而不画按钮与不画任何东西是两件事。
//
// 措辞按用户给的来：`active profile`（中文「当前配置」）。它是**陈述**不是命令，
// 所以不着重点色（那是按钮用的），走 ToneOK——绿色的意思是「一切正常，这就是
// 现在生效的那一份」，与健康表里 `ok` 那一格是同一个语气。
func profileNote(name string) *view.Note {
	if store.LoadState().DefaultProfile != name {
		return nil
	}
	return &view.Note{Text: i18n.T("active profile", nil), Tone: view.ToneOK}
}

// profileActions 是这张档位卡上的按钮；它**已经就是默认**时返回空。
//
// 为什么已经默认的那一张不挂：「把当前这份设为默认」显示在它自己身上是纯噪音，
// 而卡片上按钮一多，真正有用的那个就会被淹掉。判据只有一条（此刻的 DefaultProfile
// 是不是它），所以它不会骗人。
//
// 注意「不挂按钮」**不等于**「什么都不说」：那一张卡上有一句绿色的 `active profile`
// （见 profileNote）。两件事分开之后，这条函数只回答「该给哪些按钮」。
//
// 为什么是两个而不是一个：改默认 profile 这件事，用户想要的常常不止「默认改成它」，
// 还有「**所有客户端**都跟着它」。单独设过的客户端不跟着走是对的（它们被明确指定
// 过），但要逐个去点一遍就是十六次操作——第二个按钮把「设默认」与「收敛」一次做完，
// 它不多做任何别的事。
//
// # 措辞：`use` / `use globally`（2026-10-26 改，之前是 `apply` / `apply to all`）
//
// 2026-09-21 那一版写的是 `apply` / `apply to all`（中文「应用 / 应用到全部」）。
// 2026-10-26 用户指出它**歧义**：`apply` 在这个界面里也能读成「把这份草稿保存
// 下去」，而这两颗按钮做的是「把它变成当前生效的那一份」。所以动词改成 `use`：
//
//	use           = 从现在起用它（成为全局默认）
//	use globally  = 再往所有客户端推一遍（清掉每个客户端自己的链头）
//
// 两个词仍然是同一个动词、只差作用域——「同一件事的两个范围」该有的样子。
// 宾语不必写出来：按钮就长在**这张档位的卡**上，卡头写着它的名字。
func profileActions(name string) []view.Action {
	if store.LoadState().DefaultProfile == name {
		return nil
	}
	return []view.Action{
		{
			ID:    "set-default",
			Label: func() string { return i18n.T("use", nil) },
			Run:   func() (string, error) { return setDefaultProfile(name, false) },
		},
		{
			ID:    "set-default-all",
			Label: func() string { return i18n.T("use globally", nil) },
			Run:   func() (string, error) { return setDefaultProfile(name, true) },
		},
	}
}

// setDefaultProfile 把一份档位设成全局默认；all 时顺带清掉每个客户端的链头。
//
// 它**不改这一份档位本身**：用户要的是「默认用它」，不是「把它改一改」。所以这个
// 按钮不会让这张卡变脏、也不会产生一份待保存的草稿——它是一次落盘的动作，
// 做完界面重读一遍（见 lib/view 的 Concept.Actions）。
func setDefaultProfile(name string, all bool) (string, error) {
	if err := store.SetDefaultProfile(name, all); err != nil {
		return "", err
	}
	return "", nil
}

// profileView 把一份**已经解析好的**档位拼成编辑器要的全部素材。
//
// 加载路径与预览路径共用它，理由一条：两条路必须给出**一模一样**的形状。各写一份
// 的话，「刚敲完原文」与「保存之后」这两个时刻画出来的会是两张慢慢长歪的表，而那种
// 不一致没有任何测试会红——只有用的人看得出来（两张表里有一张少了某个字段）。
func profileView(name, file string, pr *domain.Profile) profileData {
	// provider 表读不出来不算这张卡片坏掉：档位本身是好的、可以改，只是候选
	// 下拉框没有素材（providers.json 还没建是全新安装的正常状态）。这条以前是
	// 「整个 profile 静默消失」，现在是「照常显示、候选为空」。
	snap, snapErr := store.Load()
	if snapErr != nil {
		snap = &store.Snapshot{}
	}
	// 父档位的可选项 = 别的档位（不含自己：自己继承自己是个环，解析时会报错）。
	// 第一个是空串 = **不继承**，它必须有一个看得见的位置（界面把它画成「—」）。
	extOpts := []string{""}
	for _, n := range profileNames() {
		if n != name {
			extOpts = append(extOpts, n)
		}
	}
	data := profileData{
		Profile: name, File: paths.RelToRoot(file), Base: store.Revision(file),
		Description: pr.Description, Pinned: pr.Pinned, Excluded: pr.Excluded, Extends: pr.Extends,
		Default:        snap.State != nil && snap.State.DefaultProfile == name,
		Providers:      providerChoices(snap),
		ExtendsOptions: extOpts,
	}
	for _, id := range roleOrder(pr) {
		rd := roleData{ID: id}
		for _, b := range pr.Roles[id] {
			rd.Bindings = append(rd.Bindings, bindingData{Provider: b.Provider, Model: b.Model, Ref: b.Ref})
		}
		data.Roles = append(data.Roles, rd)
	}
	return data
}

// parseProfileDraft 把一份**草稿的字节**按那份文件的格式解析成档位。
//
// 与 store.readProfileFile 是同一套分支（.kv 走 KV 解析、.json 走 JSON），但输入
// 是内存里的草稿而不是磁盘上的文件——预览要的正是这个：**还没落盘**的那一份。
func parseProfileDraft(file string, draft []byte) (*domain.Profile, error) {
	if strings.HasSuffix(file, ".kv") {
		return store.ParseProfileKV(string(draft))
	}
	var pr domain.Profile
	if err := json.Unmarshal(draft, &pr); err != nil {
		return nil, err
	}
	if pr.Roles == nil {
		pr.Roles = map[string]domain.Candidates{}
	}
	return &pr, nil
}

// roleOrder 是档位的展示顺序：四个标准档位按阶梯先后（重→轻），其余（含 "*" 与
// 客户端注册的动态键）按名字排在后面。
//
// 顺序不是排版洁癖：**档位是一条阶梯**，界面上按 heavy→light 排，用户一眼能看出
// 「这条链在往下走」；按字母序排出来的是 h/l/m/n，看的人得自己拼。
func roleOrder(pr *domain.Profile) []string {
	var out []string
	seen := map[string]bool{}
	for _, id := range domain.Roles {
		if _, ok := pr.Roles[id]; ok {
			out = append(out, id)
			seen[id] = true
		}
	}
	var rest []string
	for id := range pr.Roles {
		if !seen[id] {
			rest = append(rest, id)
		}
	}
	sort.Strings(rest)
	return append(out, rest...)
}

func providerChoices(snap *store.Snapshot) []providerChoice {
	if snap.Providers == nil {
		return nil
	}
	// 模型清单从**各 profile 的绑定**里收集：那是「这家用过的模型」，比 providers.json
	// 里声明的 models 更贴近实际（而且不依赖那个字段存不存在）。
	used := map[string]map[string]bool{}
	for _, p := range snap.Profiles {
		for _, cands := range p.Roles {
			for _, b := range cands {
				if b.Provider == "" || b.Model == "" {
					continue
				}
				if used[b.Provider] == nil {
					used[b.Provider] = map[string]bool{}
				}
				used[b.Provider][b.Model] = true
			}
		}
	}
	names := make([]string, 0, len(snap.Providers.Providers))
	for name := range snap.Providers.Providers {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]providerChoice, 0, len(names))
	for _, name := range names {
		p := snap.Providers.Providers[name]
		pc := providerChoice{
			Name: name, Protocol: p.Protocol, BaseURL: p.BaseURL, KeyEnv: p.APIKeyEnv,
			HasKey: p.APIKey != "" || p.APIKeyEnv != "",
		}
		for m := range used[name] {
			pc.Models = append(pc.Models, m)
		}
		sort.Strings(pc.Models)
		out = append(out, pc)
	}
	return out
}

// applyProfileRoles 只改 roles 那一段，其余字段原样保留。
//
// 为什么不是「把整个 profile 序列化回去」：界面展示的是**概念**（档位、候选），
// 而文件里还有它不认识的东西（extends、window、compact、将来加的字段）。整份重写
// 等于拿界面的知识覆盖文件，用户手写的字段会在他没碰过的地方消失。
func applyProfileRoles(conceptID, file string, edit json.RawMessage, base string) (string, error) {
	var patch struct {
		Roles map[string][]bindingData `json:"roles"`
		// Extends / Name / Delete 与 roles **同一张卡**交上来：一张 kv 卡就是那一份
		// 文件的全部（档位、继承、改名、删除）。分成两张卡的做法试过了——用户的原话
		// 是「这些要收进各个 kv 内部」，而且分成两张时「档位文件」那张卡与每个 kv 的
		// 卡说的是同一件事，是重复的。
		Extends *string `json:"extends"`
		Name    *string `json:"name"`
		Delete  bool    `json:"delete"`
	}
	if err := json.Unmarshal(edit, &patch); err != nil {
		return "", i18n.Ef(err, "the tier bindings in this request are not readable: {err}", i18n.A{"err": err})
	}

	if patch.Delete {
		// 删的是整份文件。CAS 用 base（加载时那一版）——别人刚改过就不删，报冲突。
		if err := store.RemoveIfUnchanged(file, base); err != nil {
			return "", profileErr("remove", file, err)
		}
		return "", nil
	}

	// roles 为 nil 表示**这一次没动档位**（只改了继承或名字）：保持文件里现有的。
	var roles map[string]domain.Candidates
	if patch.Roles != nil {
		roles = map[string]domain.Candidates{}
		for id, list := range patch.Roles {
			// 空列表**保留**（写进去一个空档位），不当作「删掉这个键」：界面把一个档位
			// 的候选全删光，意思是「这一档没有候选」——那与「没写这一档」（于是往上
			// 继承）是两件事，替用户选后者等于改了他没碰过的语义。
			cands := make(domain.Candidates, 0, len(list))
			for _, b := range list {
				cands = append(cands, domain.Binding{Provider: b.Provider, Model: b.Model, Ref: b.Ref})
			}
			roles[id] = cands
		}
	}

	// 改名 = 建新文件 + 删旧文件（两次独立的 CAS）。名字必须是干净的**文件词干**：
	// 带路径分隔符或 ".." 的名字能把文件写到 mappings/ 外面去。
	newName := ""
	if patch.Name != nil {
		newName = strings.TrimSpace(*patch.Name)
		if newName == "" {
			return "", i18n.E("a profile must have a name — there is nothing to save it as", nil)
		}
		if strings.ContainsAny(newName, `/\`) || newName == "." || newName == ".." || strings.Contains(newName, "..") {
			return "", i18n.E("a profile name must not contain path separators or \"..\": {name}", i18n.A{"name": newName})
		}
	}
	curStem := strings.TrimSuffix(filepath.Base(file), filepath.Ext(file))
	if newName == "" || newName == curStem {
		newName = "" // 没改名
	}

	if strings.HasSuffix(file, ".kv") {
		b, err := os.ReadFile(file)
		if err != nil {
			return "", err
		}
		pr, err := store.ParseProfileKV(string(b))
		if err != nil {
			return "", err
		}
		if roles != nil {
			pr.Roles = roles
		}
		if patch.Extends != nil {
			pr.Extends = strings.TrimSpace(*patch.Extends)
		}
		if newName != "" {
			// .kv 里**没有名字这个字段**——名字就是文件名。所以改名 = 内容原样搬到
			// 新文件名下（内容里不含名字，不存在「路径与内容不一致」的问题）。
			return renameProfile(conceptID, file, newName, base, []byte(store.SerializeProfileKV(pr)))
		}
		return writeThrough(conceptID, file, base, []byte(store.SerializeProfileKV(pr)))
	}

	b, err := os.ReadFile(file)
	if err != nil {
		return "", err
	}
	// map[string]json.RawMessage：只替换动过的那几个键，别的键连**解析都不解析**，
	// 于是它们不可能被这次保存改变形状（数字的写法、键的顺序、未知字段）。
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(b, &doc); err != nil {
		return "", i18n.Ef(err, "{file} is not valid JSON", i18n.A{"file": filepath.Base(file)})
	}
	if doc == nil {
		doc = map[string]json.RawMessage{}
	}
	if roles != nil {
		raw, err := json.Marshal(roles)
		if err != nil {
			return "", err
		}
		doc["roles"] = raw
	}
	if patch.Extends != nil {
		ext := strings.TrimSpace(*patch.Extends)
		if ext == "" {
			delete(doc, "extends")
		} else {
			raw, err := json.Marshal(ext)
			if err != nil {
				return "", err
			}
			doc["extends"] = raw
		}
	}
	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return "", err
	}
	if newName != "" {
		return renameProfile(conceptID, file, newName, base, append(out, '\n'))
	}
	return writeThrough(conceptID, file, base, append(out, '\n'))
}

// renameProfile 把一份档位文件改名：按新名字写一份（内容原样），再删掉旧的。
//
// 两次独立的 CAS：新文件要求**不存在**（base=""），旧文件要求**还是加载时那一版**。
// 中间任何一步失败都如实报出来——半途改名（两份都在、或者旧的没了新的没建）是
// 用户最不愿意看到的状态，所以宁可在错误里说清楚停在哪一步。
func renameProfile(conceptID, oldFile, newName, base string, content []byte) (string, error) {
	newFile := filepath.Join(paths.Mappings(), newName+filepath.Ext(oldFile))
	if newFile == oldFile {
		return writeThrough(conceptID, oldFile, base, content)
	}
	if err := profileNameTaken(newName); err != nil {
		return "", err
	}
	if err := writeProfileNewFile(newFile, content); err != nil {
		return "", profileErr("create", newFile, err)
	}
	if err := store.RemoveIfUnchanged(oldFile, base); err != nil {
		return "", profileErr("remove old", oldFile, err)
	}
	return "", nil
}

// writeThrough 是三个应用者共用的出口：落盘，并把「基线对不上」翻译成**界面的
// 冲突形状**（两边原文都在）。
//
// 翻译放在这里而不是 store 里：store 是文件层，它只该说「你手里那份过期了」，
// 而「过期了该怎么办」是界面与用户之间的事（把两份摊开让人选）。中间这一层
// 是拥有这些文件的那位——它知道文件叫什么、概念是谁。
func writeThrough(conceptID, file, base string, data []byte) (string, error) {
	rev, err := store.WriteIfUnchanged(file, base, data)
	var stale *store.StaleError
	if errors.As(err, &stale) {
		return "", &view.Conflict{
			Concept: conceptID, Path: paths.RelToRoot(file),
			Base: stale.Base, Current: stale.Current,
			Yours: string(data), Theirs: string(stale.Disk),
		}
	}
	return rev, err
}

// ---------- 全局开关 ----------

// toggleItem / stateData 的形状住在契约包里（`lib/view` 的 Toggles / ToggleItem）：
// 形状是 **Kind 的事**，而 Kind 定义在那里。2026-09-21 之前它们长在这里，只因为
// 当时只有这一张卡用 KindToggles；等 modules/locale 也要产出一张语言卡，留在
// 这里就只能被抄一遍。
type toggleItem = view.ToggleItem
type stateData = view.Toggles

// stateConcept 是 state.json 里**归 config 的那几个字段**。
//
// 只报自己的字段（别的模块的段各有其人，见 confighook 的登记表）：一个模块把
// state.json 整个端上去让用户改，等于让界面替所有模块做决定。
func stateConcept() view.Concept {
	file := paths.StateFile()
	snap, _ := store.Load()
	var st *domain.State
	if snap != nil {
		st = snap.State
	}
	return view.Concept{
		ID: "config.state", Kind: view.KindToggles, Title: i18n.T("Global settings", nil),
		Data: stateView(file, st),
		Apply: func(edit json.RawMessage, base string) (string, error) {
			return applyStateDefaultProfile("config.state", file, edit, base)
		},
		// Preview：见 view.Concept.Preview。这一对（`config.state` 是控件半、
		// `config.file.state.json` 是原文半）与档位那一对是同一件事，所以少了它
		// 就会长出同一个 bug：在原文里改完、再拨一下开关，整份盖回去。
		Preview: func(draft []byte) (any, error) {
			return stateView(file, parseStateDraft(draft)), nil
		},
	}
}

// stateView 把一份 state 拼成开关卡要的素材。加载与预览共用（理由同 profileView：
// 两条路必须给出同一个形状）。
func stateView(file string, st *domain.State) stateData {
	active, host := "", ""
	if st != nil {
		active, host = st.DefaultProfile, st.Host
	}
	return stateData{
		File: paths.RelToRoot(file), Base: store.Revision(file),
		Items: []toggleItem{{
			ID: "default_profile", Kind: "select", Value: active, Options: profileNames(),
			Label: i18n.T("Default profile", nil),
			Why:   i18n.T("Which profile the chain starts from when no agent-specific one is set.", nil),
		}, {
			// 监听地址：默认只绑回环，因为 8899 能改配置、能拨开关。想从 tailnet /
			// 局域网打开界面的人才需要放开它（放开之后 web 界面的 Host 门也认得
			// Tailscale 网段，见 web-dashboard 的 isTailscale）。
			ID: "host", Kind: "text", Value: host,
			Label:       i18n.T("Bind address", nil),
			Placeholder: i18n.T("empty = loopback only (127.0.0.1)", nil),
			Why: i18n.T("An IP (e.g. a Tailscale 100.x address) or 0.0.0.0 for every interface. "+
				"Empty = loopback only. Changing it takes effect after a restart.", nil),
		}},
	}
}

// parseStateDraft 把一份 state.json 的草稿解析成 State。
//
// 与 store.LoadState 同一套规则（读不出来就零值 + Normalize——那是刻意的 fail-open，
// 一个手改坏的文件不该让所有 agent 停摆），只是输入是**内存里的草稿**：预览要的正是
// 还没落盘的那一份。
func parseStateDraft(draft []byte) *domain.State {
	s := &domain.State{}
	_ = json.Unmarshal(draft, s)
	s.Normalize()
	return s
}

func applyStateDefaultProfile(conceptID, file string, edit json.RawMessage, base string) (string, error) {
	var patch struct {
		DefaultProfile *string `json:"default_profile"`
		// Host 是监听地址（空串 = 回环）。它在界面上也能改，但**改完要重启**
		// 才生效：换监听地址要重新 bind，那不是热路径能做的事（见 forward.New 的
		// 注释）。所以这一格保存之后，界面显示的仍是「配置成什么」，而此刻真正
		// 在听的是哪一个是另一回事——那句话写在 why 里。
		Host *string `json:"host"`
	}
	if err := json.Unmarshal(edit, &patch); err != nil {
		return "", i18n.Ef(err, "the settings in this request are not readable: {err}", i18n.A{"err": err})
	}
	b, err := os.ReadFile(file)
	if err != nil {
		return "", err
	}
	// RawMessage 的理由同 applyProfileRoles：state.json 里住着好几个模块各自的段，
	// 整份重写会拿这一处的知识覆盖别人的。
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(b, &doc); err != nil {
		return "", i18n.Ef(err, "{file} is not valid JSON", i18n.A{"file": filepath.Base(file)})
	}
	if patch.DefaultProfile != nil {
		raw, err := json.Marshal(*patch.DefaultProfile)
		if err != nil {
			return "", err
		}
		doc["default_profile"] = raw
	}
	if patch.Host != nil {
		h := strings.TrimSpace(*patch.Host)
		if h == "" {
			// 空 = 回环，那是默认值——把键删掉而不是写一个空串，文件里少一行噪音，
			// 语义与「没配过」完全一样（见 domain.State.BindHost）。
			delete(doc, "host")
		} else {
			if net.ParseIP(h) == nil {
				return "", i18n.E("the bind address must be an IP (or empty for loopback only), got {value}",
					i18n.A{"value": h})
			}
			raw, err := json.Marshal(h)
			if err != nil {
				return "", err
			}
			doc["host"] = raw
		}
	}
	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return "", err
	}
	return writeThrough(conceptID, file, base, append(out, '\n'))
}

// ---------- provider 表 ----------

// providerFields 是这个概念管得着的键。**不在这个名单里的键原样保留**——名字里
// 带个日期、写着备注、或者将来 core 新加的字段，都不该因为界面没露出来就消失
// （同一个理由见 applyProfileRoles）。
var providerFields = []string{"protocol", "base_url", "anthropic_url", "api_key", "api_key_env", "models"}

// providersConcept 是 providers.json 的**结构化编辑器**。
//
// 为什么不能靠原文那张卡：那份文件里有 api_key，出 daemon 之前整份被脱敏（见
// redact），而原文卡的 Apply 写回的是整份文本——写回去就是把 *** 落盘。所以
// `config.file.providers.json` 一直是只读的，而「加一家 provider」恰恰是装完
// newgate 之后第一个要做的事，界面不该在这件事上缺席。
//
// 做法是把值**留在 daemon 里**：api_key 那一格不进快照（见 view.FieldSecret），
// 界面显示占位提示，敲了才改、不敲就不动。于是浏览器永远拿不到凭据，而新增、
// 改名、删除、换 base 这些操作都是完整的。
func providersConcept() view.Concept {
	file := paths.ProvidersFile()
	// 读**原始形态**，不走 store.LoadProviders：那一个会把 api_key_env 解析成明文
	// 填进 APIKey，拿它的结果去写盘就是把密钥落盘（见 store.SaveProviders 的注释）。
	// 「界面拿不到凭据」这条承诺，在这里是「连我自己的读路径都不带明文」。
	raw, err := readProvidersRaw(file)
	if err != nil {
		return brokenConcept("config.providers", view.KindRecords, i18n.T("Providers", nil), err)
	}
	names := make([]string, 0, len(raw))
	for name := range raw {
		names = append(names, name)
	}
	sort.Strings(names)

	items := make([]view.Record, 0, len(names))
	for _, name := range names {
		items = append(items, providerRecord(name, raw[name]))
	}
	return view.Concept{
		ID: "config.providers", Kind: view.KindRecords,
		Title: i18n.T("Providers", nil),
		// Order 1：排在全局设置（0）之后、档位文件（10）之前——「先配上游，才选得出
		// 档位绑定」，这个顺序与操作的先后是同一件事。
		Order: 1,
		Data: view.Records{
			File:     paths.RelToRoot(file),
			Items:    items,
			Base:     store.Revision(file),
			CanAdd:   true,
			AddLabel: i18n.T("+ provider", nil),
		},
		Apply: func(edit json.RawMessage, base string) (string, error) {
			return applyProviders(file, edit, base)
		},
	}
}

// providerRecord 把一个 provider 摆成一张小表单。
//
// 顺序是**从上到下读一遍就能配出一家上游**的顺序：先认人（名字、协议、地址），
// 再给凭据，最后是它提供哪些模型。
func providerRecord(name string, fields map[string]json.RawMessage) view.Record {
	str := func(k string) string {
		var s string
		if raw, ok := fields[k]; ok {
			_ = json.Unmarshal(raw, &s)
		}
		return s
	}
	var models []string
	if raw, ok := fields["models"]; ok {
		_ = json.Unmarshal(raw, &models)
	}
	sort.Strings(models)

	// 凭据那一格的提示要说清**它现在是什么状态**，因为值本身永远不出来：
	// 「已设置（留空 = 不改）」与「还没设」是用户唯一能看到的区别。
	keyHint := i18n.T("not set — paste the key here", nil)
	if str("api_key") != "" || str("api_key_env") != "" {
		keyHint = i18n.T("set — leave empty to keep it", nil)
	}
	return view.Record{
		ID: name, Label: name, Removable: true,
		Fields: []view.Field{
			{ID: "name", Label: i18n.T("name", nil), Kind: view.FieldText, Value: name,
				Why: i18n.T("The name the tier bindings refer to. Renaming is a new provider: bindings that used the old name keep pointing at it.", nil)},
			{ID: "protocol", Label: i18n.T("protocol", nil), Kind: view.FieldSelect,
				Value: str("protocol"), Options: []string{"", "openai", "anthropic"},
				Why: i18n.T("Which dialect this upstream speaks. Empty means openai.", nil)},
			{ID: "base_url", Label: i18n.T("base URL", nil), Kind: view.FieldText, Value: str("base_url"),
				Placeholder: i18n.T("https://…", nil),
				Why:         i18n.T("Where requests go. The path is appended by newgate.", nil)},
			{ID: "anthropic_url", Label: i18n.T("Anthropic base URL", nil), Kind: view.FieldText, Value: str("anthropic_url"),
				Placeholder: i18n.T("only if the two dialects live on different bases", nil),
				Why:         i18n.T("Some upstreams serve the Anthropic dialect on a different base and do not forward between them (ARK is the case this exists for).", nil)},
			{ID: "api_key", Label: i18n.T("API key", nil), Kind: view.FieldSecret,
				Placeholder: keyHint,
				Why:         i18n.T("Never sent to the browser: this field arrives empty, and writing a value is the only way to change it.", nil)},
			{ID: "api_key_env", Label: i18n.T("API key from env", nil), Kind: view.FieldText, Value: str("api_key_env"),
				Placeholder: i18n.T("e.g. DEEPSEEK_API_KEY", nil),
				Why:         i18n.T("Reads the key from an environment variable instead of the file. Takes precedence over the field above.", nil)},
			{ID: "models", Label: i18n.T("models", nil), Kind: view.FieldLines, Value: strings.Join(models, "\n"),
				Placeholder: i18n.T("one per line", nil),
				Why:         i18n.T("Model names this provider serves, used to pick candidates for an explicit model request.", nil)},
		},
	}
}

// applyProviders 把界面上那份 provider 表写回 providers.json。
//
// 语义与开关那张表一致：界面交回来的是**它手里的全部记录**，没交的就是删掉。
// 两处例外，都是「界面不可能知道」的东西：
//
//   - api_key 空串 = **别动它**（值从来没给过界面，它无从回传）。
//   - 不在 providerFields 里的键原样保留。
func applyProviders(file string, edit json.RawMessage, base string) (string, error) {
	var patch struct {
		Items []struct {
			ID     string            `json:"id"`
			Values map[string]string `json:"values"`
		} `json:"items"`
	}
	if err := json.Unmarshal(edit, &patch); err != nil {
		return "", i18n.Ef(err, "the providers in this request are not readable: {err}", i18n.A{"err": err})
	}

	raw, err := readProvidersRaw(file)
	if err != nil {
		return "", err
	}

	out := map[string]map[string]json.RawMessage{}
	for _, it := range patch.Items {
		name := strings.TrimSpace(it.Values["name"])
		if name == "" {
			return "", i18n.E("a provider has no name — the tier bindings would have nothing to refer to", nil)
		}
		if _, dup := out[name]; dup {
			// 两条记录写同一个名字：静默留一条的结果是用户改的那家不见了。
			return "", i18n.E("two providers are both named \"{name}\"", i18n.A{"name": name})
		}
		// 从**它原来那条**起手（界面点开的是 id 那条，名字可以改），未知键与没露
		// 出来的字段于是原样带过去。
		next := map[string]json.RawMessage{}
		for k, v := range raw[it.ID] {
			next[k] = v
		}
		for _, k := range providerFields {
			v, given := it.Values[k]
			if !given {
				continue // 界面上没有这一格：不碰
			}
			if k == "api_key" {
				if v == "" {
					continue // 空 = 别动它，见上面那段
				}
			}
			if k == "models" {
				list := splitLines(v)
				if len(list) == 0 {
					delete(next, k)
					continue
				}
				b, err := json.Marshal(list)
				if err != nil {
					return "", err
				}
				next[k] = b
				continue
			}
			if strings.TrimSpace(v) == "" {
				delete(next, k)
				continue
			}
			next[k] = mustJSON(v)
		}
		out[name] = next
	}

	// 顶层文档的其他键也要留着（将来加的段），所以按原文**只改 providers 那一段**。
	// 文件不存在（全新安装还没建它）从空文档起手：那不是错误，见 readProvidersRaw。
	b, err := os.ReadFile(file)
	if err != nil && !os.IsNotExist(err) {
		return "", err
	}
	doc := map[string]json.RawMessage{}
	// 空文件（不存在 / 刚被清空）从空文档起手：`json.Unmarshal(nil, …)` 报的是
	// 「unexpected end of JSON input」，而那不是用户能看懂的东西。
	if len(strings.TrimSpace(string(b))) > 0 {
		if err := json.Unmarshal(b, &doc); err != nil {
			return "", i18n.Ef(err, "{file} is not valid JSON", i18n.A{"file": filepath.Base(file)})
		}
	}
	body, err := json.Marshal(out)
	if err != nil {
		return "", err
	}
	doc["providers"] = body

	text, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return "", err
	}
	return writeThrough("config.providers", file, base, append(text, '\n'))
}

// readProvidersRaw 读 providers.json 的 providers 那一段，**每个 provider 保持
// 原始 JSON**（键序、未知键都不动）。
//
// 文件不存在当空表：全新安装还没建它——那不是错误，界面该能照着它加第一家。
func readProvidersRaw(file string) (map[string]map[string]json.RawMessage, error) {
	b, err := os.ReadFile(file)
	if os.IsNotExist(err) {
		return map[string]map[string]json.RawMessage{}, nil
	}
	if err != nil {
		return nil, err
	}
	var doc struct {
		Providers map[string]map[string]json.RawMessage `json:"providers"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		return nil, i18n.Ef(err, "{file} is not valid JSON", i18n.A{"file": filepath.Base(file)})
	}
	if doc.Providers == nil {
		doc.Providers = map[string]map[string]json.RawMessage{}
	}
	return doc.Providers, nil
}

// splitLines 把「一行一项」的多行文本拆成列表：空行与首尾空格丢掉。
func splitLines(s string) []string {
	var out []string
	for _, line := range strings.Split(s, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			out = append(out, line)
		}
	}
	return out
}

// mustJSON 把一个字符串编码成 JSON 值。字符串不会有编码错误，所以这里不返回错误
// ——让调用点少一条永远走不到的 if。
func mustJSON(s string) json.RawMessage {
	b, err := json.Marshal(s)
	if err != nil {
		return json.RawMessage(`""`)
	}
	return b
}

// writeProfileNewFile 写一份「此刻不存在的」新文件：要求 base="" 让
// WriteIfUnchanged（它还要返回新基线）把并发新建直接拒。
func writeProfileNewFile(path string, data []byte) error {
	_, err := store.WriteIfUnchanged(path, "", data)
	return err
}

// profileErr 把三类失败包成一句可读的、按文件定位的报错——这是「不静默」的
// 那条规矩（出错了要带上下文），也是「按文件定位」（这条路径同时动多份文件，
// 用户必须知道是哪份坏了）。
func profileErr(verb, file string, err error) error {
	name := filepath.Base(file)
	if sc, ok := err.(*store.StaleError); ok {
		return i18n.E("{verb} {file} failed: {err} (someone else changed it since you opened this page — reload and try again)",
			i18n.A{"verb": verb, "file": name, "err": sc.Error()})
	}
	return i18n.Ef(err, "{verb} {file} failed: {err}",
		i18n.A{"verb": verb, "file": name})
}

// ---------- 源文件 ----------

type fileData struct {
	Path     string `json:"path"`
	Language string `json:"language"`
	Text     string `json:"text"`
	// Base 是这份文件加载时的基线（内容哈希，见 store.WriteIfUnchanged）。
	//
	// **少了它，原文那一栏根本存不进去**：界面保存时把这份基线一起交回来，没有
	// 基线就成了「我以为它不存在」，而磁盘上它是存在的——CAS 当场判过期，报
	// 「这个文件在页面加载之后被别人改过」。用户看到的是「改原文点保存没反应，
	// 改控件却好好的」（2026-09-20 实测）。
	Base string `json:"base"`
	// Redacted：这份内容是**脱敏过**的（凭据被换成了 ***），所以它只读——
	// 写回去就是把 *** 落盘，那是数据丢失，比不能编辑严重得多。
	Redacted bool `json:"redacted,omitempty"`
}

func fileConcepts() []view.Concept {
	var out []view.Concept
	add := func(path, language string) {
		rel := paths.RelToRoot(path)
		b, err := os.ReadFile(path)
		if err != nil {
			// 读不出来就报一张坏卡片（而不是不报）：源文件那条 tab 少一个文件名，
			// 用户以为这文件不存在，而它可能只是权限不对。
			out = append(out, brokenConcept("config.file."+rel, view.KindCode, rel, err))
			return
		}
		text, redacted := redact(string(b), language)
		c := view.Concept{
			ID: "config.file." + rel, Kind: view.KindCode, Title: rel,
			// Order 20：原文那一半排在所有控件卡之后。它在左栏里本来就不占位
			// （有控件半时被折掉，见 App 的 navUnits），这个 Order 是给「没有
			// 控件半、自己单独露脸」的那种文件用的（比如 providers.json 之外的
			// 边角文件）。
			Order: 20,
			Data: fileData{
				Path: rel, Language: language, Text: text, Redacted: redacted,
				// 基线取自**磁盘上那份字节**（不是上面这段 text——text 可能被脱敏
				// 改写过，用它算出来的哈希与盘上对不上，保存照样会被判过期）。
				Base: store.Revision(path),
			},
		}
		if !redacted {
			c.Apply = func(edit json.RawMessage, base string) (string, error) {
				var patch struct {
					Text string `json:"text"`
				}
				if err := json.Unmarshal(edit, &patch); err != nil {
					return "", i18n.Ef(err, "the file content in this request is not readable: {err}", i18n.A{"err": err})
				}
				return writeThrough(c.ID, path, base, []byte(patch.Text))
			}
		}
		out = append(out, c)
	}
	add(paths.ProvidersFile(), "json")
	add(paths.StateFile(), "json")
	if ents, err := os.ReadDir(paths.Mappings()); err == nil {
		for _, e := range ents {
			if e.IsDir() {
				continue
			}
			name := e.Name()
			switch {
			case strings.HasSuffix(name, ".kv"):
				add(filepath.Join(paths.Mappings(), name), "kv")
			case strings.HasSuffix(name, ".json"):
				add(filepath.Join(paths.Mappings(), name), "json")
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// secretKeys 是**绝不外发**的字段名。
//
// 显式列而不是「反正只挑我认识的字段」：源文件那条 tab 是把整个文件发给浏览器的，
// 而 providers.json 里的 api_key、state.json 里的 control_token 都是凭据。界面
// 监听在 loopback 上，但 loopback 边界意味着**本机任何进程**都能读——凭据不该
// 只靠这一点保护。
var secretKeys = map[string]bool{
	"api_key": true, "control_token": true, "root_key": true, "token": true,
}

func redact(text, language string) (string, bool) {
	if language == "json" {
		var doc map[string]any
		if json.Unmarshal([]byte(text), &doc) != nil {
			return text, false // 解析不了就原样给出去（只读视图，不写回）
		}
		if !scrub(doc) {
			return text, false
		}
		b, err := json.MarshalIndent(doc, "", "  ")
		if err != nil {
			return text, true
		}
		return string(b) + "\n", true
	}
	found := false
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		key, _, ok := strings.Cut(line, "=")
		if ok && secretKeys[strings.TrimSpace(key)] {
			lines[i] = key + "=***"
			found = true
		}
	}
	return strings.Join(lines, "\n"), found
}

func scrub(v any) bool {
	touched := false
	switch node := v.(type) {
	case map[string]any:
		for k, val := range node {
			if secretKeys[k] {
				node[k] = "***"
				touched = true
				continue
			}
			if scrub(val) {
				touched = true
			}
		}
	case []any:
		for _, item := range node {
			if scrub(item) {
				touched = true
			}
		}
	}
	return touched
}

// ---------- 小工具 ----------

func profileNames() []string {
	names, err := store.ListProfiles()
	if err != nil {
		return nil
	}
	return names
}

// profileNameTaken 说这个**名字**是不是已经有一份档位文件了（`.kv` / `.json` 都算）。
//
// 为什么按名字判、而不是按「我正要写的那条路径」判：两种后缀并存时 `.kv` 赢（见
// store.LoadProfile），所以 `demo.json` 还在的情况下写出一份 `demo.kv`，等于把那份
// json **静默压掉**——它在文件系统里好好的，但谁也不会再读到它（用户在界面上看到
// 的只是「我新建的那个档位怎么是空的」）。按名字判把这个洞一起堵上。
//
// 为什么这条要放在 CAS **之前**：CAS 拦得住写入，但它的报错是「(loaded (absent),
// on disk sha256:…)（页面打开后别人改过——重载后再试）」——而这里根本没有人改过
// 东西，重载再试一万次还是同一个结果（2026-09-21 实测：把 aaa 改名成已存在的 bbb
// 报的就是这句，用户会照着它去重载，然后卡住）。存在性是一个**确定的事实**，
// 按事实说。
func profileNameTaken(name string) error {
	if slices.Contains(profileNames(), name) {
		return i18n.E("a profile named {name} already exists — pick another name, or delete that one first",
			i18n.A{"name": name})
	}
	return nil
}

// profileFile 是这个 profile 在磁盘上的真身（.kv 优先，与 store 的读取次序一致）。
func profileFile(name string) (string, error) {
	kv := filepath.Join(paths.Mappings(), name+".kv")
	if _, err := os.Stat(kv); err == nil {
		return kv, nil
	}
	js := filepath.Join(paths.Mappings(), name+".json")
	if _, err := os.Stat(js); err == nil {
		return js, nil
	}
	return "", fmt.Errorf("profile %s has no file", name)
}
