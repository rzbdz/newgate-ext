// Package view 是模块向 web 界面贡献控制面时实现的那组接口，以及那份契约的身份。
//
// 它与 modules/cli/extension 是**同一条规矩的两份实现**：模块保留自己的知识，
// 界面只负责编排与渲染。理由也一样——界面（不管是 CLI 还是浏览器）一旦为了显示
// 某样东西去读某个模块的内部，那两样东西就焊在一起了，而「谁的状态谁自己报」是
// 解开它的唯一办法。
//
// # 为什么契约要下沉成叶子包
//
// web 界面是个重包（它 import gateway 的 metrics、config 的 store、HTTP 那一套）。
// 模块若为了拿到「概念」这个类型去 import 它，就把这些全拖进自己的依赖里，于是
// 谁 import 了那个模块就再也无法被 gateway/config 的测试引用——成环。所以契约住
// 这里，箭头是 `modules/* → lib/view → component`。
//
// # 账本在哪
//
// **账本（Registry）由提供界面的那个模块自己的 service 持有**（发行版的
// modules/web-dashboard），与 cli 那边一模一样：贡献者拿到的是 Register，
// 界面在自己的 service 上问它要数据。所以 web-dashboard 不认识任何模块，
// 加一个模块的界面也不需要改它——更不用改前端（前端只认 Kind）。
//
// 登记时除了产出函数还要报一个**栏目名**（Section）：界面把概念按来源分栏，
// 而「这一栏叫什么」是模块自己的事——名字若住在界面里，加一个模块就得改一次
// 界面，那正是上面那条规矩要避免的。名字在**快照那一刻**求值，理由见 Section。
//
// # 一个概念 = 一份文件的全部知识
//
// 概念不只报**数据**，还带 `Apply`：怎么写回磁盘是**拥有那份数据的人**的知识。
// 这一条是这套东西能站住的关键——只要界面自己动手写文件，它就必然要知道文件
// 格式、字段语义、以及哪些字段不能碰，那正是「BFF 认识 config 的内部」。
//
// # 产出是**有人来问的时候**才发生的
//
// 注册（Register）只登记一个产出函数，不读盘、不解析、不算指标。产出函数要等到
// 真的有人来看界面时才被调用。
//
// 这不是性能洁癖，是这套东西的正确性问题：**每一个 `newgate ...` 进程都会跑一遍
// 模块的 Start**，而其中绝大多数（敲一条命令、进一个 shell、跑一次体检）根本
// 没有人会看界面。把「读全部配置、解析每个 profile、解析每个档位绑到哪家 provider」
// 放在注册那一刻，就是让每一次 CLI 调用都白付一遍这笔账——而它买到的只是「一个
// 没有人会打开的快照」。第一版就是那么写的（Start 里直接读出全部数据塞进概念），
// 被删掉了。
//
// 顺带买到的第二件事：界面上的刷新是真的刷新。产出函数每次重跑，CLI 刚建的档位
// 文件、刚改过的 providers.json 都会出现在下一次快照里——装配期数一遍的名单迟早
// 会数错（daemon 是长命的，而文件是 CLI 随时能动的）。
package view

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"

	modules "github.com/rzbdz/newgate/component"
	"github.com/rzbdz/newgate/lib/i18n"
)

// Kind 是渲染方式的名字。**前端只认这几种**：加一个新模块的界面不需要改前端，
// 除非它带来的是一种新的**形状**（那时候两边都要动，这是诚实的代价）。
const (
	// KindMapping 档位/候选这种「列表套列表」的绑定编辑器：每一项是一组有序候选，
	// 顺序就是 fallback 顺序。
	KindMapping = "mapping-editor"
	// KindCode 源文件（带语法高亮的编辑器）。
	KindCode = "code"
	// KindToggles 一组开关 + 选择器（每个都带一句为什么）。
	KindToggles = "toggles"
	// KindSeries 计数器/指标。
	KindSeries = "series"
	// KindTable 只读的表格。
	KindTable = "table"
	// KindRecords 一组**结构化的记录**：每条几个字段，字段自己声明类型。
	//
	// 它和 KindToggles 的分工：toggles 是「一组同构的布尔/单选」，每条就一个值；
	// 这里每条是一张**小表单**（provider 的 base_url、protocol、key…），条数还会
	// 增减。两者都需要，因为把它们塞进一个形状的结果是其中一个从此别扭。
	KindRecords = "records"
	// KindLog 日志流。
	KindLog = "log"
	// KindChains 一屏摊开的候选链：一份 profile 一张卡，卡里一档一行，行里是这条
	// 链此刻的样子。
	//
	// 它和 KindTable 的分工：表是「一行一个事实」，读的人只看；这里每一行**带着
	// 去处**（「这一档现在走这条链，还能换成谁」），而那条去处是本行的数据长出来
	// 的（见 ChainRow.Actions）。与 KindRecords 的分工更明显：records 每条是一张
	// **小表单**（编辑某个字段的值），而这里要的是**现场状态**——「这一档此刻解析
	// 到什么」是 resolve 算出来的结论，不是文件里的某个字面量。
	KindChains = "chains"
	// KindOverview 是**首屏**：按 agent 分栏的路由总览——顶上一条 agent 标签，
	// 下面一叠「一份 profile 一张」的卡，每张卡上是它的链此刻的样子**加上**每一站
	// 的健康。
	//
	// 它和 KindChains 的分工：chains 回答「这一档解析成什么」（纯配置结论，与谁说、
	// 在哪一屏说无关，`newgate tier` 要的是同一份）；overview 回答「我现在该让这个
	// agent 走哪一份」——那要把**两本账**摆在一起（配置算出来的链 + 探活记下来的
	// 健康），还要一个「就用它」的动作。合起来才是「切一条链」这件事。
	//
	// # 为什么健康必须由贡献者合，而不是让前端去 join
	//
	// 链的结论在 config（`configapi.AllChains`），每条 binding 的延迟与探活结论在
	// breaker（它自己的账本）。让前端按 `provider/model` 把两张表拼起来确实能画出
	// 同样的画面，代价是**两件只有拥有者知道的事被抄进了界面**：链卡上哪一格是
	// 「链头」（空链怎么办）、延迟多少算慢（阈值在 breaker/status 里，只有一处）。
	// 这两条都会在各自的模块里改，而前端那份抄件没有任何东西会红。所以 join 发生在
	// **后端**，由一个同时看得见两本账的贡献者产出（见 overview.go 的注释）。
	KindOverview = "overview"
	// KindFingerprints 是一屏**模型指纹**：按 profile 列出每条绑定上一次验到的
	// 「这个上游实际是哪个模型」，并允许现场再验一次。
	//
	// 它为什么不是 Table/Records：一次指纹验证**是一次要花 token、几十秒才有结果
	// 的动作**（上游要吐几百个整数，本地再拿去和参考库比）。表格表达的是一份已经
	// 算好的事实；这里要的是「这行现在可以按一下、按完会变」的现场，而且每个条目
	// 的状态会独立地从 queued 走到 scored——那是**流程**，不是一格数据。
	//
	// 与 Overview 的区别：overview 合并的是「配置算出来的链」与「健康表」；这里
	// 只有一种素材（指纹），但多一个「挑一家上游、挑一个模型、现场要一份指纹」的
	// 自由面板。所以是新的形状，不是某一种的变体。
	KindFingerprints = "fingerprints"
)

// Table 是 KindTable 的数据形状：几列 + 几行。
//
// 形状定在这里、而不是各模块自己拼一个 map 的原因与 Kind 那组常量一样：前端
// 只认这一种摆法。谁的表都长这样，前端就只有一个渲染器。
type Table struct {
	Columns []Column `json:"columns"`
	Rows    []Row    `json:"rows"`
}

// Row 是表里的一行。
//
// 为什么格子是「按列名索引的 map」而不是数组：列的顺序是**展示**的事（前端可以
// 按窄屏重排、将来可以给用户拖动），而格子和列的对应关系是**数据**的事。用
// 数组下标把它们绑死，前端一动列顺序，所有格子就串位了——那种错还会看起来很
// 正常（每一格都有值，只是值不对）。
type Row struct {
	// ID 是**这一行**的机器标记：动作回传、以及前端做 key 都用它。
	// 空 = 这一行没有动作（那就没人需要它稳定）。
	ID string `json:"id,omitempty"`
	// Cells 是「列 ID → 格子」。缺的列渲染成空白，不是错误：一张表的行本来就
	// 可以有稀疏的字段（比如「只计数没摘牌」的行没有冷却时刻）。
	Cells map[string]Cell `json:"cells"`
	// Actions 是这一行上的按钮（形状见 Action）。
	//
	// 与 Concept.Actions 是同一条规矩的第三处：**谁的知识谁自己报**。这里多一层
	// 理由——表里的行是**数据长出来的**（今天哪些 binding 在健康表里，取决于跑过
	// 哪些请求），所以「这一行能做什么」在构造行的时候才定得下来。贡献者给每一行
	// 各构造一份闭包（它捕获的是那一行），界面只把拿到的按钮画出来。
	Actions []Action `json:"actions,omitempty"`
}

// Column 是表头的一列。
type Column struct {
	// ID 是这一列的**身份**：行里的格子按它索引。稳定的机器标记，不翻译。
	ID string `json:"id"`
	// Label 是给人看的表头（可以翻译、可以改）。
	Label string `json:"label"`
	// Align 是 "right" 时右对齐。数字列要它——左对齐的数字没法竖着比大小。
	Align string `json:"align,omitempty"`
}

// Cell 是一格。
type Cell struct {
	Text string `json:"text"`
	// Tone 是这一格的颜色语义，取 cli 那套 ok/warn/bad/skip 里的前三个
	// （没有 "skip"：那是「这一步没跑」的意思，只对流程有意义，表格里用不上）。
	//
	// 空串 = 不着色。**不许**在这里传 ANSI：颜色是渲染层的事，内核只给语义，
	// 否则同一份数据在网页与终端上就得各写一遍转义。
	Tone string `json:"tone,omitempty"`
	// Href 非空时这一格是一个**链接**（界面渲染成 <a>）。
	//
	// 为什么地址由贡献者给：它往往是**算出来的**，而算法只有贡献者知道——「那个
	// 远端界面挂在这个端口的这个前缀下」是拥有它的模块的事，界面既不认识端口也不
	// 认识前缀（见 modules/ssh-tunnel 的路径改写）。让前端去拼，等于把那条知识抄
	// 成第二份，而两份迟早对不上。
	//
	// 新标签页是**定死的**，不是省事：界面是一个**带草稿**的单页应用，当前这一页
	// 可能正躺着一份没保存的编辑，链接把它顶掉就等于把那份草稿丢掉。这条与
	// nav.ts 的 hash 路由是同一条考虑的两面。
	Href string `json:"href,omitempty"`
}

// Tone 的取值。前端按这几个词上色，其余一律当没给。
const (
	// 栏目（Section）的 Fields 取值：这一栏**是干什么的**。见 SectionInfo.Fields。
	//
	// 三个名字说的是界面上的三类事，不是模块的类别：「请求走哪条路」是看的那一半，
	// 「能改的东西」是设置，「某一个客户端自己的格子」是第三类。
	//
	// # 词表要**避开模块名**（`config` 尤其不能当取值）
	//
	// 界面那边有一条棘轮：`web/src` 里出现任何模块名就红（见 nav_test.go 的
	// moduleLiterals）。取值 `"config"` 撞上了那个模块名——而棘轮是对的：它无法
	// 分辨「这是模块名」与「这是碰巧同名的词表」，而放它过去就等于给「前端认识模块」
	// 开一个说不清的例外。所以这里叫 `settings`（2026-09-29 从 `config` 改的，
	// 那时它才刚加上一天）。
	//
	// 这三个词与前端那边认的 Kind 名（`overview` / `chains` / …）是同一类东西：
	// **界面自己的词表**，不是任何模块的身份。
	FieldRoutes   = "routes"
	FieldSettings = "settings"
	FieldClients  = "clients"

	ToneOK   = "ok"
	ToneWarn = "warn"
	ToneBad  = "bad"
)

// Records 是 KindRecords 的数据形状：一组记录，每条几个具名字段。
//
// 字段的**类型**（text / select / secret / lines）由贡献者声明：它知道这个字段
// 是一个封闭集合（协议只有两家）、还是一个自由文本、还是不该给浏览器看的凭据。
// 前端只按这四个词渲染，不认识任何具体字段——「加一个模块的面不用改前端」在
// 这里与别处是同一条规矩。
type Records struct {
	// File 是这份记录集**写的是哪一份文件**，相对配置根（空 = 它不对应任何可编辑
	// 的文件）。界面拿它把「控件」与「原文」两半配成一对并排（见 nav.ts 的 fileOf：
	// 所有带这个字段的形状都适用，不是给某一种 Kind 开的口子）。
	File  string   `json:"file,omitempty"`
	Items []Record `json:"items"`
	// Base 是这份文件加载时的基线（内容哈希，见 store.WriteIfUnchanged）。
	// 可写的记录集必须有它：没有基线，保存就无从判断「我手里这份是不是最新的」，
	// 而那正是「命令行刚改了同一个文件」的唯一防线（见 Concept.Apply 的注释）。
	// 只读的记录集留空。
	Base string `json:"base,omitempty"`
	// CanAdd 为 true 时界面给一个「新增一条」的按钮，AddLabel 是它的字。
	// 为什么不是所有 Records 都能加：有的记录集合由别的东西决定（比如「这个构建
	// 装了哪些模块」），给它一个加了没用的按钮就是骗人。
	CanAdd   bool   `json:"can_add,omitempty"`
	AddLabel string `json:"add_label,omitempty"`
	// Blank 是**新增一条时长什么样**：字段的形状（ID / Label / Kind / Options /
	// Why），值一律被忽略（新的一条从空开始）。
	//
	// 为什么这件事需要单独一格：字段的形状长在**每一条记录**上（见 Record.Fields），
	// 于是前端渲染一张新卡片时只能去抄一条现成的——它们全都一样，因为它们是同一
	// 个贡献者按同一份声明造出来的。那个办法在**一条都没有**的时候失效：`Items`
	// 为空、`CanAdd` 为真，点「新增」得到一张**一个输入框都没有的空卡片**，而
	// 用户没有任何办法把内容填进去（`modules/ssh-tunnel` 第一次装配就撞上：一份
	// 还没配过 target 的配置正是它的常态）。
	//
	// 所以形状由贡献者在这里明说。留空是合法的（老贡献者不必改），那时前端退回
	// 去抄第一条——今天的行为不变。
	Blank []Field `json:"blank,omitempty"`
}

// Record 是一条。
type Record struct {
	// ID 是这条**此刻**的身份（改名就是换一条：ID 变了）。空的 = 界面上新加的，
	// 贡献者按「新增」处理。它同时是 Apply 回传时用来对号的东西。
	ID     string  `json:"id"`
	Label  string  `json:"label"`
	Fields []Field `json:"fields"`
	// Removable 为 true 时界面给一个删除按钮。刻意**不是**默认开：删掉一条记录
	// 通常意味着删掉磁盘上的一段配置，那种动作该由贡献者显式说「这条可以删」，
	// 而不是由「它是个 record」推出来。
	Removable bool `json:"removable,omitempty"`
	// Base 是**这一条**代表的那份文件加载时的基线，见 Records.Base。有的记录集
	// 一条对应一份文件（如「这些档位文件」），删这一条就是删那份文件，得用这条
	// 自己的版本来判断「没被别人抢先动过」。界面原样回传，不解释。
	Base string `json:"base,omitempty"`
}

// Field 是一条记录里的一个字段。
type Field struct {
	// ID 是字段名（Apply 回传时的键）。机器标记，不翻译。
	ID string `json:"id"`
	// Label 是给人看的字段名（可以翻译）。
	Label string `json:"label"`
	// Kind 决定怎么渲染：text（普通文本）/ select（Options 里挑一个）/
	// secret（凭据：**值不在快照里**，界面显示占位符，敲了才改）/
	// lines（多行，一行一项，用于「一组名字」那种列表）。
	Kind string `json:"kind"`
	// Value 是当前值。**secret 字段的 Value 恒为空串**——见下面的注释。
	Value   string   `json:"value,omitempty"`
	Options []string `json:"options,omitempty"`
	// Placeholder 是空值时的提示（界面画在输入框里）。
	Placeholder string `json:"placeholder,omitempty"`
	// Why 是一句话说明这个字段影响什么（鼠标悬停时给）。
	Why string `json:"why,omitempty"`
}

// 字段类型的取值。
const (
	FieldText   = "text"
	FieldSelect = "select"
	// FieldSecret 是**凭据**：值不会出现在快照里，界面拿到的是一个空串加一句
	// 占位提示。空串回传 = 「别动它」，敲了新值才是改。
	//
	// 为什么不是「脱敏成 *** 再让界面改」：界面手里的原文已经被换成 *** 了，
	// 它写回来就是把 *** 落盘——那是数据丢失，比不能改严重得多（同一个理由见
	// modules/config/view.go 里 Redacted 那段）。所以这里的做法是**值根本不出去**：
	// 浏览器没有的东西，就不可能被原样写回来。
	//
	// 判据是「这东西泄露了会不会疼」，不是「它是不是密码」：api_key 是，
	// base_url 不是。
	FieldSecret = "secret"
	// FieldLines 是多行文本，一行一项（「这家的模型清单」那种）。
	FieldLines = "lines"
)

// Toggles 是 KindToggles 的数据形状：一组开关 / 单选 / 单行文本，每个带一句为什么。
//
// # 为什么这几个类型住在契约包里，而不是某个使用者的家里
//
// 它们 2026-09-21 之前定义在 `modules/config/view.go`（那张「全局设置」卡）。当时
// 只有一个使用者，所以看不出问题；等到第二个模块要产出同一种 Kind（modules/locale
// 的「语言」卡）就只有两条路：抄一遍这些字段名，或者把 config 拖进自己的依赖里。
// 两条都是这套设计要避免的东西——**形状是 Kind 的事，Kind 定义在这里**，所以形状
// 也定义在这里，与 KindToggles 那条注释里「前端只认这一种摆法」是同一句话。
//
// 前端读的就是这几个 JSON 字段（kinds/Toggles.svelte），改名要让两边一起动。
type Toggles struct {
	// File 是这张卡**写的是哪一份文件**，相对配置根（空 = 它不对应某一份可编辑的
	// 文件）。与 Records.File 同义：界面拿它把「控件」与「原文」两半配成一对并排。
	File string `json:"file,omitempty"`
	// Base 是这份文件加载时的基线（内容哈希，见 store.WriteIfUnchanged）。
	Base  string       `json:"base,omitempty"`
	Items []ToggleItem `json:"items"`
}

// ToggleItem 是一个开关格。
type ToggleItem struct {
	// ID 是这一格的机器标记（Apply 回传时的键）。不翻译。
	ID string `json:"id"`
	// Label 是给人看的名字（可以翻译）。
	Label string `json:"label"`
	// Kind 决定怎么渲染：select（Options 里挑一个）/ switch（On）/ text（单行）。
	Kind string `json:"kind"`
	// Value 是 select/text 格的当前值；On 是 switch 格的状态。
	//
	// **空串也发**（没有 omitempty）：对 select 来说空串是一个**合法取值**，而不是
	// 「没有这一格」——本仓库里就有这样的下拉（语言那格的「跟随系统」是空串，皮肤
	// 那格同）。省掉它，前端拿到的是 undefined，只能自己 `?? ""` 兜一下；那个兜底
	// 恰好对，但它是运气：一个选项表里**不含空串**的下拉会因此显示成第一项，而真实
	// 取值是空——屏幕上说的与实际存的不一样，且没有任何东西会红。
	//
	// switch 格会因此多带一个 `"value": ""`。那点字节换的是「取值永远在报文里」这
	// 一条能靠得住的规矩（与 OverviewAgent 的 ready / own 同一条）。
	Value string `json:"value"`
	On    bool   `json:"on,omitempty"`
	// Options 是 select 格的候选（**机器取值**，不翻译）。
	Options []string `json:"options,omitempty"`
	// OptionLabels 给某个取值换一个**给人看的说法**（取值 → 显示）。没列到的按取值
	// 本身显示，空取值没列到时显示成「—」。
	//
	// 为什么需要它、而不是把说法直接写进 Options：Options 里那些值是**会被存进配置
	// 的**（档位名、profile 名、`inherit`），显示层改一个字的代价是配置里多一个不
	// 认识的值。而有的取值**根本没法直接给用户看**——链头那一格的空取值就是：
	// 它的意思是「跟随全局缺省」，而「缺省是谁」只有后端知道。
	//
	// 更要紧的是它把两件**配置上完全不同**的事分开了：用户选了 `ds`（state.json
	// 里记着 `active: {claude: ds}`）与用户什么都没选、只是此刻全局默认正好是 `ds`
	// （state.json 里没有这一条）。两者在这一格里的**当前值**是同一个字符串，不换
	// 个说法就分不出来——而它们的行为差别是「以后改全局默认时它跟不跟着走」。
	OptionLabels map[string]string `json:"option_labels,omitempty"`
	// Why 是一句话说明这一格影响什么（鼠标悬停时给）。
	Why string `json:"why,omitempty"`
	// Placeholder 是空格子里的提示（比如「空 = 只听回环」）。空值时它比 why
	// 更该被看见：用户对着一个空输入框，第一句话得告诉他空着是什么意思。
	Placeholder string `json:"placeholder,omitempty"`
	// Group 是**卡片内部**再分一层的标题：值一变，界面就在这一格前面插一行小标题
	// （见 kinds/Toggles.svelte）。空 = 不分组。
	//
	// 与 Concept.Group 不是一回事：那个分的是**侧栏里的栏目**，这个分的是**同一张
	// 卡里的若干行**。opencode 的 omo 槽位用它把 agent 与 category 分成两段——
	// 十几行下拉挤在一起时，那两行小标题是唯一能让眼睛停一下的东西。
	//
	// 它在契约里而不是在某一家模块里：形状归 Kind，而「界面上怎么摆」这件事已经
	// 有一份实现（前端那个渲染器），多加一个只有一家认得的字段等于让别家写的卡片
	// 分组静默失效。
	Group string `json:"group,omitempty"`
}

// ToggleKind 的取值。
const (
	ToggleSelect = "select"
	ToggleSwitch = "switch"
	ToggleText   = "text"
)

// Chains 是 KindChains 的数据形状：一叠 profile 卡，每张卡里按档位列出此刻的链。
//
// # 为什么一张卡是「一份 profile」
//
// 链不是全局的一条：同一份配置里可以有 `ark`、`kimi` 好几份 profile，各自给
// 每一档配候选，谁是链头由 state.json 说了算（见 docs/04-configuration.md）。
// 所以「一屏看到我这些链」的单位只能是 profile——合成一张大表的话，第一列就得
// 是 profile 名，而那个重复十几遍的字符串是这个页面最不该让人读的东西。
//
// # 为什么行是「一档」
//
// 用户问的问题是「这一发会走谁」，而那一问是按档位问的（`newgate tier normal`）。
// 每一行的 Head 就是那一问的答案，Steps 是答案的展开，Note 解释被跳过的那几位。
type Chains struct {
	Cards []ChainCard `json:"cards"`
}

// ChainCard 一份 profile 的链。
type ChainCard struct {
	// Profile 是这份 profile 的名字（机器取值，不翻译）。
	Profile string `json:"profile"`
	// File 是它写的是哪一份文件，相对配置根。与 Records.File 同义：界面拿它把
	// 「控件」与「原文」两半配成一对并排（见 nav.ts 的 fileOf）。
	File string `json:"file,omitempty"`
	// Default 为 true 表示这份就是此刻生效的那一份（链头）。界面把它画成一个
	// 标记，与 Concept.Note 那条「当前配置」是同一个意思、同一份语气。
	Default bool `json:"default,omitempty"`
	// Roles 按档位列。**顺序由贡献者给**（谁在前谁在后是产品取舍：`newgate
	// status` 与 doctor 都按能力从高到低排，这里照同一条）。
	Roles []ChainRow `json:"roles"`
}

// ChainRow 一份 profile 里**某一档**的链。
type ChainRow struct {
	// ID 是**这一行**的机器标记：动作回传、以及前端做 key 都用它。
	// 空 = 这一行没有动作（那就没人需要它稳定）。
	//
	// 与 Row.ID 是同一条规矩的第五处，理由更硬一点：表里一行是一条 binding，
	// 而这里一行是**一份 profile 的一档**——`Tier` 在整张卡里唯一，在**整个概念
	// 里不唯一**（十份 profile 就有十个 `heavy`）。所以「把这一档换成 X」这件事
	// 光有档位名定位不到，行自己得带一个能对号的东西。
	//
	// 它是机器标记（`demo/heavy` 这种拼法），不翻译，也不该被界面拆开解释。
	ID string `json:"id,omitempty"`
	// Tier 是档位名（机器取值，不翻译）。
	Tier string `json:"tier"`
	// Label 是给人看的名字（可以翻译）。空 = 直接显示 Tier。
	Label string `json:"label,omitempty"`
	// Head 是这一档此刻的第一站（`ark/deepseek-v3`），空 = 这一档没有可用候选。
	//
	// 它单独有一格而不是「Steps[0]」：用户唯一会问的问题是「为什么不是我想的
	// 那个」，而那个答案的眼睛落点就是链头。让界面自己去取第一个，等于把「空链
	// 怎么办」这条判断抄进了前端。
	Head string `json:"head,omitempty"`
	// Steps 是**整条**链，链头在内，按实际尝试顺序排。
	Steps []ChainStep `json:"steps,omitempty"`
	// Note 是链尾那种「还有谁被跳过、为什么」的一句话。空 = 一个都没跳过。
	//
	// 它与 Step.Note 的分工：那里是**某一步**为什么没能排更前，这里是**整条链**
	// 的总结（今天就是 `newgate tier` 链尾那行「跳过 N 个候选」）。两者都要，因为
	// 被跳过的候选常常一步都不在链上——只在 Step 上写说明的话，它们就无处可去。
	Note string `json:"note,omitempty"`
	// Tone 是这一行的语气（取 ToneOK/ToneWarn/ToneBad，空 = 不着色）。
	//
	// 今天只表达「有没有可用的链头」：空链是 bad（这一档此刻没得走），有链是
	// 空的（正常态不着色）。**不表达健康度**——那需要探活，是另一本账。
	Tone string `json:"tone,omitempty"`
	// Actions 是这一行上的按钮（「把这一档换成 X」这类），形状见 Action。
	//
	// 与 Row.Actions 是同一条规矩的第四处：动作是**数据长出来的**（这一档的链上
	// 有谁，取决于配置此刻的样子），所以「这一行能做什么」在构造这一行的时候才
	// 定得下来。界面只把拿到的按钮画出来、把点击转回去。
	Actions []Action `json:"actions,omitempty"`
}

// ChainStep 链上的一站。
type ChainStep struct {
	// Provider / Model 是这一站的绑定（机器取值，不翻译）。
	Provider string `json:"provider"`
	Model    string `json:"model"`
	// Profile 是这一站来自哪一份 profile。
	//
	// 为什么要它：一条链会**跨 profile**（本 profile 的候选之后接着别人的，见
	// resolve.BuildChain 的「profile 为主序」）。不写出来源的话，用户会以为链上
	// 每一站都写在他正在看的这份文件里，然后去找一个不存在的候选。
	Profile string `json:"profile,omitempty"`
	// Note 是这一站的一句话（今天只有 Skip.Reason：被降级/跳过之类的说明）。
	Note string `json:"note,omitempty"`
	// Tone 是这一站的语气（取 ToneOK/ToneWarn/ToneBad，空 = 不着色）。与 Cell.Tone
	// 同一份词汇表，理由也同一条：颜色是渲染层的事，内核只给语义。
	Tone string `json:"tone,omitempty"`
}

// Overview 是 KindOverview 的数据形状：**按 agent 分的路由总览**。
//
// # 为什么是两个轴
//
// 用户要做的决定是「让**这个**客户端走**哪一份**档位」。前者是几台机器上装着的
// 客户端（claude / codex / …，一份机器上通常两三个），后者是配置里那十几份 profile。
// 所以这张页面的形状就是这两个轴：Agents 是顶上的标签，Cards 是下面的卡，每张卡
// 是一份 profile 的链此刻的样子。卡按 Profile 排（贡献者给的顺序，与
// `configapi.AllChains` 一致：生效的那一份在前）。
//
// # 为什么「这个 agent 现在走谁」在 Agent 上而不是在卡上
//
// 因为它是**agent 的属性**，不是 profile 的属性（同一份 profile 可以同时是 claude
// 的链头、而 codex 走别的）。把它写进卡里，一份 profile 就得分身成「for claude 的
// 那张」和「for codex 的那张」，而它们的数据除了那一个布尔以外完全一样。
type Overview struct {
	// Agents 是顶上的标签，**顺序由贡献者给**（今天按 agent 的机器标记排，跨重启
	// 稳定——标签会跳动的界面，用户每次都要重新找一遍）。
	Agents []OverviewAgent `json:"agents"`
	// Auto 是网格最前面那一张**「自动」**卡（见 OverviewCard.Auto）。
	//
	// 它是一个**间接引用**：它自己不是一个 profile，而是「全局默认」这条引用。
	// 它此刻指向哪一份由 Profile 说；它**能指向**哪几份由它自己 Actions 里那组
	// `use:<global>:<profile>` 说（选一个 = 改全局默认）。
	//
	// 为空的场合：这个进程里没有可选的 profile（一份档位都没有）。那时不该画一
	// 张没有任何成员的空卡。
	Auto *OverviewCard `json:"auto,omitempty"`
	// Cards 是一份 profile 一张。
	Cards []OverviewCard `json:"cards"`
}

// OverviewAgent 是顶上的一个标签：一个客户端此刻被指到哪一份 profile。
type OverviewAgent struct {
	// Icon 是这个客户端在界面上的图标：SVG path 的 `d` 那一串，`24 24` 视口。
	// **空 = 它没给**——界面那时画自己的通用记号，不是画一个空白。
	//
	// 由客户端自己声明、原样带到这里（见 confighook.Agent.Icon）：界面不认识任何
	// 客户端，它只把这一串放进一个 `<svg>` 里，颜色走 `currentColor`（于是跟着皮肤）。
	Icon string `json:"icon,omitempty"`
	// ID 是机器标记（`claude` / `codex`，与 `newgate <agent>` 那个名字同一个）：
	// 它同时是**动作 ID 的后半截**（见 OverviewCard.Actions 的 `use:<agent>`），
	// 所以界面在这一栏里要挑哪个按钮，判据就是它。不翻译。
	ID string `json:"id"`
	// Name 是给人看的名字（可以翻译）。
	Name string `json:"name"`
	// Profile 是它此刻从哪一份 profile 起链（机器取值，不翻译）。空 = 没有指定过，
	// 也就是**跟随全局默认**——那不是「没有链」，界面上要说清楚（见 Docs 里的
	// `ActiveFor`）。
	Profile string `json:"profile,omitempty"`
	// Own 为 false = 这一档**没有自己的设置**，上面那个 Profile 是它跟全局默认走的
	// 结果，不是它自己的选择。
	//
	// # 为什么这个区分必须有
	//
	// 「给 claude 固定选 ds」与「claude 跟随全局、而全局恰好是 ds」是**两个不同的
	// 状态**：前者的路由在全局被改掉之后仍然是 ds，后者会跟着变。报文里只有 Profile
	// 的话，这两个状态长得一模一样，界面只能把它们说成同一句话——于是
	//   - 用户看不出「我改全局会不会连带改了这一家」；
	//   - 而且没法表达「把这一家改回自动」（那个动作的前提正是「它现在自己有设置」）。
	//
	// 与 `omitempty` 同一条：这句话两个取值都有意义，省掉一个就等于丢状态。
	Own bool `json:"own"`
	// Ready 为 false 表示这台机器上**没有这个客户端**（判据由报这个 agent 的人给，
	// 今天走 confighook 的「装没装」那一问）。没装的那一栏照常画出来——藏掉的话
	// 用户会以为这个客户端不被支持，而真相是「它在，只是这台机器上没有」。
	//
	// **不能加 `omitempty`**：这句话的两个取值都是有意义的，省掉 `false` 之后
	// 「没装」与「装了」在报文上一模一样（都是字段缺席），界面于是既画不出那一栏的
	// 灰、也画不出「未安装」那个记号——而它不会报任何错，只是那两处**永远不出现**。
	Ready bool `json:"ready"`
}

// OverviewCard 一份 profile 在这个首页上的那张卡。
type OverviewCard struct {
	// Auto 为 true 表示**这一张是「自动」那张卡，不是一份 profile**。
	//
	// 它与别的卡是两类东西，而它们**长得几乎一样**（都是「一个名字 + 一张表 + 一组
	// 动作」），所以这个标记必须有：界面据此决定标题写什么、这一张被选中时意味着
	// 什么（别的卡是「这一家固定用它」，这一张是「这一家跟着全局默认走」）。
	//
	// 「自动」与「固定到某一份」是**两个状态**，哪怕此刻解析出来的 profile 一模一样：
	// 改掉全局默认之后，前者的路由会跟着变，后者不会。把它们合成一张卡，用户就没法
	// 表达「我就是要它」这件事——那正是这一格存在的理由（Clash 里同一个 group 既能
	// 直接选节点、又能引用另一个 group，是同一件事）。
	Auto bool `json:"auto,omitempty"`
	// Profile 是这份 profile 的名字（机器取值，不翻译）。
	//
	// 在「自动」那张卡上它是**此刻指向的那一份**（空 = 一份都没有）。
	Profile string `json:"profile"`
	// File 是它写的是哪一份文件，相对配置根（空 = 取不到）。与 Records.File 同义。
	File string `json:"file,omitempty"`
	// Default 为 true 表示这份就是**全局默认**（没单配过的 agent 都从它起链）。
	Default bool `json:"default,omitempty"`
	// Roles 按档位列。**顺序由贡献者给**（与 KindChains 同一条）。
	Roles []OverviewRow `json:"roles"`
	// Actions 是这张卡上的按钮。今天是一组 `use:<agent>:<profile>`：把这份 profile
	// 指给某个客户端（见 OverviewAgent.ID），`global` 是「所有客户端都用它」那一档。
	//
	// # 为什么是「一个客户端一个动作」而不是一个带参数的「用它」
	//
	// 动作**不接参数**（见 Action.Run）：界面能提供的只有「用户点了这个按钮」，
	// 别的都得由实现者在构造这个闭包的时候定下来。所以「给 claude 用」与「给 codex
	// 用」必然是**两个动作**，各带各的闭包。
	//
	// # 为什么 ID 里还要带 profile
	//
	// 因为同一个 agent 的同一个动作在**十几张卡上各有一个**（每份 profile 一个「用它」
	// 按钮），而账本按概念 ID + 动作 ID 找人——`RunConceptAction` 取的是**第一个匹配**
	// （见 Registry.RunConceptAction）。只写 `use:claude` 的话，那十几个动作里只有
	// 排在最前面的那张卡能被点到，其余的点了会落到**别人**身上：用户想切到 cheap，
	// 实际改的可能是 balanced。加上 profile 之后它在整个概念里唯一，查找才落在对的
	// 那一张卡上。
	//
	// 界面据此挑按钮的判据是 ID 里那两段机器标记（`use:` 后面先 agent 后 profile），
	// 与链卡那边拼行 ID 是同一条规矩：机器标记两边各拼一次，就必须拼成同一个串
	// （见 ChainRow.ID）。所以这个写法是**契约**，不是实现细节；改它要两边一起改。
	//
	// 当前这一栏的 agent 已经用着这份 profile 时，那个动作**不出现**（把它指给
	// 它自己是一件没有意义的事），界面于是只画一句「正在用它」的陈述。
	Actions []Action `json:"actions,omitempty"`
}

// OverviewRow 一份 profile 里**某一档**的链，加上这一档链头的健康。
type OverviewRow struct {
	// ID 是**这一行**的机器标记（动作回传用）。拼法与 ChainRow.ID 完全相同：
	// `profile/tier`。行 ID 在整张卡里唯一、在整个概念里也唯一。
	ID string `json:"id,omitempty"`
	// Tier 是档位名（机器取值，不翻译）。
	Tier string `json:"tier"`
	// Label 是给人看的名字（可以翻译）。空 = 直接显示 Tier。
	Label string `json:"label,omitempty"`
	// Head 是这一档此刻的第一站（`ark/deepseek-v3`），空 = 这一档没有可用候选。
	// 与 ChainRow.Head 同义、同一条理由（空链是一条判断，不该由界面去做）。
	Head string `json:"head,omitempty"`
	// Steps 是**整条**链，链头在内，按实际尝试顺序排。
	Steps []OverviewStep `json:"steps,omitempty"`
	// Note 是链尾那句「还有谁被跳过、为什么」。空 = 一个都没跳过。
	Note string `json:"note,omitempty"`
	// Tone 是这一行的语气（空 = 不着色）。与 ChainRow.Tone 同义：只表达「有没有
	// 可用的链头」，**不表达健康度**——健康度在每一站的 LatencyTone 上，那两者的
	// 判据完全不同（一个是「配置里有没有人」、一个是「这个人此刻快不快」）。
	Tone string `json:"tone,omitempty"`
}

// OverviewStep 链上的一站，外加**这一站此刻的健康**。
//
// 它与 ChainStep 是两份形状（而不是给 ChainStep 加两个字段）是刻意的：那一份是
// **配置的结论**（「解析出来会走谁」），这一份是**配置 + 健康**（「走起来会怎样」）。
// 把它们合成一份，等于让 `newgate tier` 那条纯粹的解析路径也背上一个「探活记过账
// 没有」的参数——而那个参数它永远用不上。
type OverviewStep struct {
	// Provider / Model 是这一站的绑定（机器取值，不翻译）。
	Provider string `json:"provider"`
	Model    string `json:"model"`
	// Profile 是这一站来自哪一份 profile（跨 profile 的链要写出来源，理由同
	// ChainStep.Profile）。
	Profile string `json:"profile,omitempty"`
	// LatencyMs 是最近一次样本的首字节延迟（毫秒）。0 = **没有样本**（从没探过、
	// 也没有真实流量），界面在那种情况下什么都不画——画一个 `0ms` 是在说
	// 「它快得没有延迟」，而真相是「不知道」。
	LatencyMs int64 `json:"latency_ms,omitempty"`
	// LatencyTone 是那个延迟该上的颜色（取 ToneOK/ToneWarn/ToneBad，空 = 不着色）。
	//
	// **阈值不在这里**：快/可用/慢是 breaker 的判据（`breaker/status.Grade`），
	// 只有一处实现。这一格只是把那个判断的**结果**搬过来，界面照着上色，不自己
	// 比数字——把 3000/12000 这两个数抄进前端，等于让改阈值的那一天有两处要改，
	// 而漏掉的那一处不会有任何东西变红。
	LatencyTone string `json:"latency_tone,omitempty"`
	// Probe 是上一次探活的结论（`fluent` / `laggy` / …，机器标记，不翻译）。
	// 空 = 没探过。它与 LatencyMs 是两件事：延迟是「多快」，结论是「通不通」。
	Probe string `json:"probe,omitempty"`
	// Note 是这一站的一句话（今天只有 Skip.Reason：被降级/跳过之类的说明）。
	Note string `json:"note,omitempty"`
}

// Applier 把这个概念的一次修改落盘。
//
// `edit` 的形状由**这个概念自己**定义（每个概念的应用者自己解释），界面不解析
// 它，只负责把前端产出的那坨 JSON 原样递回来。`base` 是加载时拿到的基线
// （内容哈希，见 store.Revision）：实现方必须先比对再写，不一致就返回 *Conflict。
//
// 返回的新基线供前端续着改——保存一次之后不该逼用户刷新页面。
type Applier func(edit json.RawMessage, base string) (newBase string, err error)

// Previewer 是「这份文件还没落盘的草稿长这样，我该显示成什么样」——见
// Concept.Preview。返回的形状必须与 Concept.Data 一致（前端拿它当 data 用）。
type Previewer func(draft []byte) (any, error)

// Concept 是一个模块贡献给界面的东西。
type Concept struct {
	// ID 是**稳定身份**：排序、持久化、以及「这次修改针对谁」都用它。
	//
	// 不用标题当身份：标题是给人看的，会翻译、会改措辞，而身份一变，前端的
	// 本地状态（展开的卡片、正在编辑的草稿）就全部对不上了。
	ID string
	// Kind 决定前端怎么渲染（见上面的常量）。
	Kind string
	// Title 是给人看的名字（可以翻译、可以改）。
	Title string
	// Source 是谁贡献的（模块名）。**由账本填写**，贡献者不用自己报——报错了
	// 就是替别人背锅，而账本本来就知道这条是谁登记的。
	Source string
	// Data 是给前端渲染的结构化数据，形状由 Kind 约定。
	Data any
	// Broken 非空表示这个概念**此刻读不出来**（文件被删了、JSON 坏了）。
	//
	// 界面照常渲染一张卡片，上面写这句话。为什么不直接把它从快照里去掉、
	// 或者让整次快照失败：前者是静默丢功能（用户以为那个档位不存在），后者是
	// 一个坏文件让整个界面白屏。两种都比「这个东西坏了，原因是 X」差。
	Broken string
	// Live 表示**这一面的数据会自己变**，界面该每隔几秒重新问它一次。
	//
	// 由贡献者声明，不由 Kind 推断（2026-09-20 之前是后者：界面把 series 与 log
	// 当成「便宜、可以常问」的同义词）。那条推断有两个毛病：一是把**成本**这个
	// 贡献者才知道的事，写成了渲染形状的附庸；二是没给别的 Kind 留口子——一张
	// 显示「还有多久自动关闭」的表，页面开着不动它就永远停在那个数字上。
	//
	// 声明的判据是**读一次贵不贵**，不是数据变不变：计数器、日志尾巴、几个开关
	// 的当前值都是内存里读一把，可以常问；config 那一面要重读并重新解析每一份
	// profile 与每一个源文件，就不行（见 Registry.Snapshot 的注释）。
	//
	// **粒度是源，不是这一条概念**：界面刷的是「有谁声明了 Live」的那些**源**，
	// 而问一个源就是问它全部的贡献者。所以同一个源里的概念得一起便宜——一条
	// 便宜的声明会把同源那几位一起拖进几秒一次的轮询里。今天只有 gateway 与
	// claudecode 两个源声明，各自的贡献者都是内存里读一把。
	Live bool
	// Apply 为 nil 表示这个概念**只读**（比如带凭据的文件：它的内容是脱敏过的，
	// 写回去就是把 *** 落盘）。
	Apply Applier
	// Locked 非空 = 这张卡**此刻没有意义**，给界面一个把它整张锁灰的理由（那句话
	// 就是理由，显示在卡片上）。
	//
	// 与只读（Apply 为 nil）的区别是「为什么不能动」：只读说的是「这个贡献者没有
	// 提供写回的方式」，锁死说的是「**它管的那件事此刻不存在**」——今天唯一的用处
	// 是「这家客户端没装在这台机器上」：给 claude 配槽位、调分类器，在这台机器上
	// 一个字节都不会生效，而卡片照常可编辑的话，用户会改完才发现白改。
	//
	// 为什么由**贡献者**说：它才知道自己的工具在不在（判据在它自己那儿，今天走
	// confighook.Agent.OnPath）。界面不理解「装没装」这件事，也不该理解——它只把
	// 拿到的理由画出来。
	//
	// 空串 = 不锁。**不要说「未安装」以外的理由**：这一格是「整张卡无意义」，不是
	// 「某一格填错了」，后者属于 Broken。
	Locked string
	// Preview 是「我这份文件**还没落盘的草稿**长这样时，我该显示成什么样」。
	//
	// 一份文件常常有两半：结构化的一半（控件）与原文的一半（`code`）。它们说的是
	// 同一份文件，而用户可以在任一半上动手。改了原文之后，控件那一半手里还是
	// 「改之前那份盘上内容」——他看不到自己刚写的东西，接着去动一下控件就会把它
	// **整个盖回去**（控件的编辑载荷是整份文件，不是那一格）。有了 Preview，界面
	// 就能拿原文的草稿来问一句，两半于是始终说的是同一份内容。
	//
	// 参数是这份文件草稿的**全部字节**（界面从同一份文件的另一半那儿拿到的），
	// 返回的形状与 Data 完全一样（就是快照里那一个）。可选：返回 error 时界面保持
	// 原样——**正敲着的那一行本来就解析不了**，那是打字途中的常态，不是故障。
	//
	// 为什么放在这里而不是让界面自己解析：那份文件的格式是**贡献者的知识**（KV
	// 怎么切、哪些键是档位、哪些字段不能碰），界面认识它就等于认识那个模块。
	// 这也正是 BFF 那条「它不认识任何模块」的规矩在这里的落点。
	Preview Previewer
	// Order 是**同一节里谁排前面**（小的在前，同值按 ID 排）。
	//
	// 为什么由贡献者说，而不是按 ID 字母序：字母序会把「一次装完就要配的基本项」
	// （上游、全局设置）排到「一天天加出来的那一堆」后面——`config.profile.*` 字母
	// 序在 `config.providers` 之前，于是左栏第一屏全是档位文件，而上游配置在第一屏
	// 之外。那不是排版偏好：先配上游才选得出档位绑定，顺序与操作的先后是同一件事。
	Order int
	// Group 是**导航栏里归到哪个标题下**，可以有两级，用 "/" 分开。
	//
	// 前一段是**大档**（`档位`那一栏——把「一天天加出来的那一堆」与「装完就要
	// 配的那两项」在版面上分开），后一段是**族**（`claude` 底下缩着
	// `claude-cheap`）。单段（`claude`）就是只有族、没有大档。
	//
	// 空串 = 不归任何一档（排在最上面、不缩进）。它只影响排列，不影响任何身份
	// ——分组变了不会让谁的 ID 跟着变，路由与草稿都按 ID 走。
	//
	// **标题画不画由界面按「这一段底下有没有 ≥2 张卡」决定**（见 TabStrip 的
	// rows）：一个人的族加一行标题是纯噪音。所以贡献者可以放心地给每一张卡都写
	// 上分组，不必自己先数一遍。
	Group string
	// Actions 是**这张卡上**的动作（见 Section.Actions，形状完全一样）。
	//
	// 与栏目动作的分工只有一条：**这件事是不是针对某一张卡**。
	//
	//	「再建一份档位文件」   栏目动作：新建出来的东西此刻还没有概念，
	//	                       没有哪张卡挂得住它。
	//	「把当前这份设为默认」  卡片动作：它说的是**这一张**（哪一份 profile），
	//	                       而那是只有这张卡自己知道的事。
	//
	// Run 依然是**不接参数**的，而这次它接不到参数是**对**的：界面能提供的只有
	// 「用户点了这个按钮」，而「点的是哪一张卡」在贡献者构造这个 Action 的时候
	// 就已经定下来了（闭包）。让界面回传一个 id 反而危险——它手里那份快照可能
	// 已经旧了，而一个按名字走的动作会作用到一个**已经不存在**的档位上。
	//
	// 与栏目动作另一条相同：它**不开事务、不碰快照**，跑完界面自己重读一遍。
	Actions []Action
	// Note 是**一句状态说明**：这张卡此刻是什么（今天唯一的用处是「这一份就是
	// 当前配置」）。它不是按钮——点了没有任何事发生，界面把它画成一个静态的标记。
	//
	// # 为什么要有它（而不是「什么都不显示」）
	//
	// 这件事的第一版是**不显示**：`profileActions` 判到「它已经是默认了」就返回空，
	// 理由是「把当前这份设为默认」显示在它自己身上是噪音。前半句对，后半句错——
	// 两张卡于是长得**一模一样**：一张是当前配置，一张不是。用户看不出哪份在生效，
	// 而这恰恰是他打开这一节最想知道的那件事（2026-09-21 用户：「你应该注入的是一个
	// info only 的绿色：当前配置的效果」）。
	//
	// 「不画噪音」的落点应该是**不画那个按钮**，而不是不画任何东西：按钮消失了，
	// 位置就被一句陈述接管。
	//
	// # 它为什么不是 Locked / Broken
	//
	// 那两位说的都是「这张卡有问题」——Broken 是读不出来，Locked 是此刻没意义。
	// 这张卡两样都不是：它读得出来、可以照常改，只是**它此刻正好是生效的那一份**。
	// 那是事实，不是故障。所以它是个独立的字段，而不是借某一个已有的口子说话。
	Note *Note
}

// Note 是一句带语气色彩的状态说明（见 Concept.Note）。
//
// Tone 取下面那三个 Tone 常量；空串 = 不着色。这里是**信息**，所以界面不该把它画成
// 警告或错误的样子——今天唯一的用处配的是 ToneOK。
type Note struct {
	Text string `json:"text"`
	Tone string `json:"tone,omitempty"`
}

// Conflict 是「你手里那份已经不是最新的了」。
//
// 它带**两边的原文**，因为只有摆出来用户才能决定怎么办：他的改动与别人的改动
// 可能只是碰巧撞上了同一段，也可能南辕北辙。「保存失败，请重试」把这件事交给
// 用户去猜，而猜错的代价是丢数据。
type Conflict struct {
	Concept string
	Path    string
	Base    string // 你加载时那份
	Current string // 磁盘上现在这份
	Yours   string // 你这次要写进去的
	Theirs  string // 磁盘上现在这份的原文（等于 Current 那份的内容，给人看）
}

func (c *Conflict) Error() string {
	return fmt.Sprintf("view: %s changed on disk since it was loaded (%s)", c.Path, c.Concept)
}

// Contributor 是「这个模块此刻贡献什么」这件事本身。
//
// 它被调用的时机只有一个：有人来问界面要数据。返回值里的每个概念都该是**当下**
// 的样子（该读的文件现在读、该算的指标现在算）。
//
// 返回 error 是给「整组都产不出来」用的（比如配置目录整个读不了）。单个概念
// 自己的毛病不要走这里——那会连带把别人的面也弄没，用 Concept.Broken。
type Contributor func() ([]Concept, error)

// Section 是「这一栏」的展示面：它的名字。
//
// 名字为什么是个函数，而不是登记时算好的一个字符串：**登记发生在各模块的 Start
// 里，而那时候语言层装好了没有是没有保证的**。业务模块不依赖 modules/locale
// （依赖它的只有发行版的 modules/i18n，它要把自带的目录追加进内核已经装好的
// 那一份），所以「谁先 Start」不归我们管；在 Start 里求值的标题会冻在源语言上，
// 而**没有任何东西会因此变红**——中文界面上那一栏就一直叫 Configuration。
//
// 求值挪到快照那一刻（Registry.Sections），就与概念标题同一时机、同一门语言：
// 概念标题一直是那么做的（见 modules/config/view.go 里 `Title: i18n.T(...)`
// 都在产出函数里），这条只是把栏名拉齐到同一个规矩上。
type Section struct {
	Title func() string
	// Group 是这一栏在侧栏里归到哪个标题下。**空 = 不归任何一档**，排在最上面。
	//
	// 为什么要它：栏目是各模块自己报的（今天七位），而侧栏此前只能按来源名字母序
	// 平铺——于是「熔断」排在第一位、「配置」夹在中间，而那份顺序**没有任何含义**
	// （它只是 `breaker` < `config`）。分组把「这一栏是干什么的」变成看得见的：
	// 配一次就基本不动的那一节在最上面，下面是按用途归拢的几类。
	//
	// 与 Concept.Group 那条一样，**名字由贡献者给**：内核不认识任何模块名，界面
	// 更不认识（它只把拿到的标题画出来）。也同样是**只影响排列**，不参与任何身份
	// ——哪一栏在哪个组里变了，URL、草稿、路由都不受影响（那些按 Source 走）。
	Group func() string
	// Actions 是这一栏上的**动作**（可以没有，也可以好几个）。
	//
	// 为什么动作挂在栏目上、而不是挂在某一概念上：**新建出来的东西此刻还没有概念**
	// ——「再加一份档位文件」建出来的那份要等下一次快照才存在，所以没有哪张卡能挂
	// 这个按钮。它属于**这一节**：那一批数据的拥有者知道「这一节还能长出什么来」，
	// 而界面不知道（它不认识任何模块）。
	//
	// 形状是 `mkdir` 那一类：点一下、干一件事、然后重读快照。所以它**不接参数**
	// ——界面能提供的只有「用户点了这个按钮」，别的都得由实现者自己看盘上有什么决定
	// （新档位该叫什么名字就是这样：只有后端知道哪几个名字已经被占了）。
	Actions []Action
	// Default 是「没有位置时落在这里」——界面的首屏落在这一栏上。
	//
	// 为什么要它：首屏此前是**谁的名字排在前面**这个偶然——`Sections` 按 Source
	// 字母序排（跨重启稳定是必须的，但字母序本身没有含义），于是第一个有卡片的那
	// 一栏成了默认落点，而那一栏是 `breaker`（字母最前）。想让用户先看见「一屏
	// 全能」那一节，唯一的办法是把 source 起成 `aaa-home` 之类来插队——那是**把
	// 巧合当机制**，字母表一变（或者多一个字母更靠前的模块）它就不成立了，而且
	// 看代码的人完全不知道那个名字是为什么那么起的。
	//
	// 所以拆成两件事：**内核说「谁可以被声明为落点」**（这一格，加上下面那条
	// 确定性规则），**谁是落点是产品取舍**（由模块自己声明）。加一个模块仍然不用
	// 改界面——这是这条规矩一直的样子。
	//
	// 它与 Group / Actions 同一条：**只影响界面怎么摆**，不参与任何身份（Source
	// 才是身份：URL、草稿、路由都按它走）。所以「这一栏不再是落点」不搬任何东西。
	// Fields 见 SectionInfo.Fields（声明用 Of）。
	Fields  string
	Default bool
}

// Action 是栏目上的一个动作。
type Action struct {
	// ID 是这一节里唯一的机器标记。前端回传它，**不翻译**。
	ID string
	// Label 是按钮上的字（可以翻译、可以改措辞）。与 Title 同一条：闭包，在快照
	// 那一刻求值——登记发生在各模块的 Start 里，那时候语言层装好没有是没有保证的。
	Label func() string
	// Run 干活。成功时返回**该切到哪个概念**（空串 = 留在原地），失败时返回一句
	// 给人看的话（会原样显示在界面上）。
	//
	// 为什么连「切到哪儿」也要交回来：新建出来的东西在快照里叫什么**只有实现者
	// 知道**（名字是它挑的）。界面拿到之后再去猜一次，就等于把「新档位叫什么名字」
	// 这条知识抄成了两份，而抄来的那份迟早会漂移。空串是常态（多数动作不产出某个
	// 具体的东西，比如「重载配置」）。
	//
	// 它**不开事务、不碰快照**：写完之后界面自己会重读一遍（与保存那条路一样）。
	Run func() (focus string, err error)
	// Tone 说这颗按钮**此刻是按下的**（空 = 平时的样子）。取值与 Note.Tone 同一套。
	//
	// 它存在的理由是有一类开关**是状态本身**：fallback 那个按钮说的不是「去关掉它」，
	// 而是「它现在关着」——不把状态交出来的话，界面只能从 label 的字面去猜（现在这
	// 句是「禁用」还是「启用」），而那是在解析一句给人看的、会翻译的话。
	//
	// # 它是**闭包**，不是当场取的值
	//
	// 与 Label 同一条理由（见上面那句「登记发生在各模块的 Start 里」），但更硬：
	// Label 是文案，取了就取了；Tone 是**状态**，而动作在 Start 里登记一次、快照每次
	// 请求重算一次——当场取值的话，颜色会永远停在进程启动时那一刻。实测踩过
	// （2026-09-30）：`newgate fallback off` 之后按钮还是绿的，看起来像界面在骗人。
	Tone func() string
}

// MarshalJSON 让动作能跟着**数据**一起端出去（表里那些行就带着动作）。
//
// 为什么需要它：Action 有一个**函数字段**（Run），默认的编码会直接拒绝
// （`json: unsupported type: func()`）。而这套东西里「概念的数据」是原样序列化的
// （BFF 拿到的就是 `any`，它不认识任何 Kind，也就无从替表里的行挑出该端什么）。
//
// 端出去的是界面要的那两个字段；Label 在**序列化这一刻**求值——那正是快照那一刻，
// 与 SectionInfo / Concept 的标题同一个时机、同一门语言。Run 端不出去，它在进程里
// 等着被调用（见 Registry.RunRowAction）。
func (a Action) MarshalJSON() ([]byte, error) {
	label := ""
	if a.Label != nil {
		label = a.Label()
	}
	tone := ""
	if a.Tone != nil {
		tone = a.Tone()
	}
	return json.Marshal(ActionInfo{ID: a.ID, Label: label, Tone: tone})
}

// Does 给这一栏挂一个动作（可以链多个）。
//
//	v.Register("config", view.Title(...).Does(view.Action{ID: "new-profile", …}), concepts)
func (s Section) Does(a Action) Section {
	s.Actions = append(s.Actions, a)
	return s
}

// In 给这一栏指定它在侧栏里的分组（可选，不写就是不归任何一档）。
//
//	v.Register("gateway", view.Title(...).In(func() string { return i18n.T("Data plane", nil) }), concepts)
//
// 为什么是链在 Title 后面的方法、而不是 Register 多一个参数：调用点有七处，而
// **绝大多数栏目不需要分组**——多一个必填参数会让那七处都写一个空占位，看的人
// 还得判断「这个空是没想好还是真的没有」。链式调用让「不分组」保持原样一行不动。
func (s Section) In(group func() string) Section {
	s.Group = group
	return s
}

// Title 造一个栏目名。传进来的是个**闭包**，不是译好的字符串：
//
//	v.Register("config", view.Title(func() string { return i18n.T("Configuration", nil) }), concepts)
//
// 这个形状有两个好处，都不是洁癖：
//
//   - 类型上就写不出「登记时把标题翻好」那种错（想传字符串编译不过），而那个
//     错是静默的——英文标题在中文界面上看着只像「还没翻」，不像一条 bug；
//   - i18n 的扫描器照常看得见那句 msgid。它认的是源码里的 `i18n.T(...)` 调用，
//     而闭包体里的调用同样在这棵 AST 里，所以**不需要教扫描器认识 view.Title**。
//     反过来若把 msgid 当字符串收进来（`view.Title("Configuration")`），那句
//     英文就谁也扫不到：账本里没有它、check 全绿、中文目录里永远缺一条。
func Title(f func() string) Section { return Section{Title: f} }

// Landing 把这一栏声明成界面的落点（没位置时落在它上）。
//
//	v.Register("home", view.Title(...).Landing(), concepts)
//
// 为什么是链在 Title 后面的方法（与 In / Does 同一个形状）：调用点有七处，而**只有
// 一处**需要它——多一个必填参数会让那七处都写一个零值，看的人还得判断「这个 false
// 是没想好还是真的不是落点」。链式调用让其余六处一行不动。
//
// 多个人声明时的规则见 Sections：**按 Source 取最小的那个**。不定这条，谁被选中就
// 取决于 map 遍历顺序，而那种「重启一次换个首屏」的错没有任何东西会红。
func (s Section) Landing() Section {
	s.Default = true
	return s
}

// Of 声明这一栏**是干什么的**（见 SectionInfo.Fields 与 Field* 那几个常量）。
//
//	v.Register("home", view.Title(...).Landing().Of(view.FieldRoutes), concepts)
//
// 与 Landing / In / Does 同一个形状，理由也同一条：调用点有七八处，只有一部分需要
// 说这件事，链式让其余的原地不动。
func (s Section) Of(field string) Section {
	s.Fields = field
	return s
}

// SectionInfo 是栏目列表里的一行。
//
// Source 是机器标记（前端拿它跟概念对上、也拿它写进 URL），Title 是给人看的。
// 与概念一样：标题可以翻译、可以改措辞，而身份不能。
type SectionInfo struct {
	Source string `json:"source"`
	Title  string `json:"title"`
	// Group 是这一栏归到哪个标题下（空 = 不归，排在最上面）。见 Section.Group。
	Group string `json:"group,omitempty"`
	// Actions 是这一栏上的动作（见 Section.Actions）。Run 是**函数**，端不出去，
	// 所以这里只给「有哪些按钮」：ID 回传时用，Label 是按钮上的字。
	Actions []ActionInfo `json:"actions,omitempty"`
	// Default 是「没有位置时落在这里」（见 Section.Default）。
	//
	// 端给界面的原因与 Group 一样：**落点是界面在用的东西**，而界面不认识任何模块
	// （它只知道 Source 这个机器标记）。让前端自己按字母序取第一个，就是把这条产品
	// 取舍抄进前端——而前端那份抄件没有模块能改。
	//
	// `omitempty` 是有意的：绝大多数栏目不是落点，那几行 JSON 不必带一个 false。
	Default bool `json:"default,omitempty"`
	// Fields 说这一栏「是干什么的」——界面据它在目录里分组。
	//
	// # 为什么需要它
	//
	// 侧栏原来把栏目按**贡献者的分组**聚在一起（「客户端」底下是 claude / codex /
	// opencode）。那是贡献者自己知道的事，但它回答不了「**这一栏该归到哪一类**」：
	// 一个模块注册栏目时想说的是「这是配置」「这是客户端的设置」，而它手上只有
	// `In("Clients")` 这种自由文本——两个发行版写 `Clients` 与 `客户端` 就分不到一起，
	// 而界面也没有任何机器可判的东西可依。
	//
	// 取值：`view.FieldRoutes`（请求走哪条路）、`FieldSettings`（能改的东西）、
	// `FieldClients`（客户端的设置）。空 = 还没归类，界面排在最后。
	Fields string `json:"fields,omitempty"`
}

// ActionInfo 是栏目动作给界面的那一面（见 Section.Actions）。
type ActionInfo struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	// Tone 是「这颗按钮此刻是按下的」（见 Action.Tone）。空 = 平时的样子，所以
	// 这里可以省；不省是因为它是**状态**，而状态在报文里缺席过一次就够写一屏
	// 猜谜的代码了（同 OverviewAgent.ready 那条）——省掉的是绝大部分动作。
	Tone string `json:"tone,omitempty"`
}

// Service 是贡献者看到的那一面：登记一个产出函数。
//
// 查询那半边不给出去——它属于界面自己（谁渲染谁读账本）。与 porthub 的 Service
// 只给 Mount 是同一条：给出去的面积越小，能长出来的耦合越少。
type Service interface {
	// Register 登记 source（模块名，报错与分组时点名用）与它的栏目名，外加产出
	// 函数。返回撤销。
	Register(source string, section Section, read Contributor) (modules.Release, error)
}

// Capability 是「这个界面在这个进程里」这件事本身。
//
// 提供者是**界面模块**（发行版的 modules/web-dashboard）；贡献者是各业务模块
// （config 报档位与文件、gateway 报计数器……），它们 Optional 依赖它：界面在
// 就注册进去，界面不在就跳过——模块功能一个都不少，只是没有 web 入口。
var Capability = modules.NewCapability[Service]("view")

// ---------- 账本 ----------

// Registry 是贡献者的账本。零值不可用，用 NewRegistry。
//
// 它住在叶子包里（而不是界面模块内部）只有一个理由：**它能被单测**。账本的行为
// （重名、撤销、并发）与渲染无关，而渲染那半边的测试要拖起 HTTP 与嵌入资源。
type Registry struct {
	mu      sync.RWMutex
	next    int
	sources []source
}

type source struct {
	id     int
	name   string
	title  func() string // Register 拒绝 nil，所以这里一定非空
	group  func() string // 可空：没写就是不分组
	def    bool          // 见 Section.Default：界面的落点
	fields string        // 见 Section.Fields：这一栏是干什么的
	acts   []Action
	read   Contributor
}

func NewRegistry() *Registry {
	return &Registry{}
}

// Register 收下一个贡献者，返回撤销。
//
// 这一步**不调用产出函数**：账本在装配期只知道「谁来报」，不知道「报什么」。
func (r *Registry) Register(name string, section Section, read Contributor) (modules.Release, error) {
	if name == "" {
		return nil, i18n.E("view: a contributor registered with no source name", nil)
	}
	if section.Title == nil {
		// 报错而不是兜个名字：栏目名缺失是**界面上一栏没有标题**，而那一刻离这里
		// 隔着一次 HTTP 与一次渲染，谁也不会顺着摸回来。这里红一下最便宜。
		return nil, i18n.E("view: {source} registered with no section title — the sidebar would have "+
			"nothing to label its column with (use view.Title)", i18n.A{"source": name})
	}
	if read == nil {
		return nil, i18n.E("view: {source} registered a nil contributor — the interface would show nothing for it",
			i18n.A{"source": name})
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	s := source{id: r.next, name: name, title: section.Title, group: section.Group,
		def: section.Default, fields: section.Fields, acts: section.Actions, read: read}
	r.next++
	r.sources = append(r.sources, s)
	return func() error {
		r.mu.Lock()
		defer r.mu.Unlock()
		// 撤销只撤自己那一条（按登记的序号认人，不按名字）：同一个模块名重复登记
		// 是允许的，而「后来者也叫这个名字」不该被前一个的撤销带走。
		for i, cur := range r.sources {
			if cur.id == s.id {
				r.sources = append(r.sources[:i], r.sources[i+1:]...)
				break
			}
		}
		return nil
	}, nil
}

// Sections 列出**登记过的全部栏目**（不只是此刻有概念的），按 Source 排序。
//
// 为什么不复用「从概念里取 source」：一个源此刻可能一条概念都产不出来——配置
// 目录整个读不了、omo 还没接管过、插件一个开关点都没上报。那些栏目仍然该在
// 侧栏里有一个位置：它从列表里消失，用户会以为那个模块不存在，然后去别处找。
//
// 它**不调用任何产出函数**（只取登记的栏目名），所以随时问都很便宜，界面可以
// 在每次快照里捎上它。栏名在这里求值——快照这一刻的语言，而不是登记那一刻的
// （见 Section 的注释）。
//
// 排序跨重启稳定（按 Source，机器标记，不随语言变）：界面顺序变来变去会让用户
// 每次都要重找一遍。**落点不靠这个顺序表达**——见 Section.Default。
func (r *Registry) Sections() []SectionInfo {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]SectionInfo, 0, len(r.sources))
	for _, s := range r.sources {
		title := ""
		if s.title != nil {
			title = s.title()
		}
		if strings.TrimSpace(title) == "" {
			// 兜底而不是报错：栏名是一句展示文案，为它让整次快照失败，代价是
			// 整个界面白屏——比一栏的标题写成 source 名严重得多。
			title = s.name
		}
		// 分组名与栏名同一个时机求值（快照这一刻的语言）：它也是给人看的字，
		// 在 Start 里求值会冻在源语言上（见 Title 的注释）。
		group := ""
		if s.group != nil {
			group = s.group()
		}
		var acts []ActionInfo
		for _, a := range s.acts {
			label := ""
			if a.Label != nil {
				label = a.Label()
			}
			tone := ""
			if a.Tone != nil {
				tone = a.Tone()
			}
			acts = append(acts, ActionInfo{ID: a.ID, Label: label, Tone: tone})
		}
		out = append(out, SectionInfo{Source: s.name, Title: title, Group: group, Actions: acts, Default: s.def, Fields: s.fields})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Source < out[j].Source })
	// 落点**最多一个**。多个人声明时按 Source 取最小的那个：不定这条的话，被选中
	// 的那个就取决于登记顺序（也就是各模块 Start 的先后），而那种「装法一变、首屏
	// 就换地方」的错没有任何东西会红。取最小与上面的排序用的是同一把尺子，所以
	// 「谁排在最前面」与「谁是落点」看起来是一致的。
	if n := countDefaults(out); n > 1 {
		for i := range out {
			if out[i].Default && !isFirstDefault(out, i) {
				out[i].Default = false
			}
		}
	}
	return out
}

func countDefaults(s []SectionInfo) int {
	n := 0
	for _, x := range s {
		if x.Default {
			n++
		}
	}
	return n
}

// isFirstDefault 说 out[i] 是不是最小的那个 Default（排序之后 = 最靠前的）。
func isFirstDefault(out []SectionInfo, i int) bool {
	for j := 0; j < i; j++ {
		if out[j].Default {
			return false
		}
	}
	return true
}

// RunAction 跑一个栏目动作（见 Section.Actions）。
//
// 找不到那一栏、或者那一栏没有这个动作时返回 ErrUnknownAction——界面手里的快照
// 可能已经过期（模块被关掉了），说清楚比装作跑过了强。
//
// **它不开事务、不动快照**：跑完由调用方（BFF）告诉界面重读一次。动作与「保存」
// 走的是两条路，因为它们的形状本来就不同：保存是「把这个概念的这份改动落盘」，
// 动作是「干一件事，具体是什么只有那一位知道」。
func (r *Registry) RunAction(source, id string) (string, error) {
	r.mu.RLock()
	var found *Action
	for i := range r.sources {
		if r.sources[i].name != source {
			continue
		}
		for j := range r.sources[i].acts {
			if r.sources[i].acts[j].ID == id {
				found = &r.sources[i].acts[j]
				break
			}
		}
		if found != nil {
			break
		}
	}
	r.mu.RUnlock()
	if found == nil {
		return "", i18n.E("{source} has no action called {action} — the page is probably stale, reload it",
			i18n.A{"source": source, "action": id})
	}
	if found.Run == nil {
		return "", i18n.E("{source}.{action} has nothing to run", i18n.A{"source": source, "action": id})
	}
	return found.Run()
}

// RunConceptAction 跑某个概念上的一个动作（见 Concept.Actions）。
//
// 与 RunAction 只有一处不同：**它得先问一遍贡献者**。栏目动作在登记时就躺在账本
// 里，而概念动作是产出函数每次现造出来的（见 lib/view 的包注释：概念是「有人来问
// 的时候」才算的）——所以「这个 ID 是哪一张卡」这件事，账本自己不知道。
//
// 代价是这一次点击要跑一遍产出函数（配置那一位会重读全部档位）。**可接受**：它是
// 一次用户点击，不是热路径；而换来的是这套东西的一条性质——动作的作用对象就是
// **用户此刻在屏幕上看到的那一张卡**，不是账本里某个可能已经过期的注册。
//
// 找不到就报错，不装作跑过：界面手里的快照可能已经旧了（那一份档位刚被删掉），
// 而一个按名字走的动作会作用到一个不存在的东西上。
func (r *Registry) RunConceptAction(conceptID, actionID string) (string, error) {
	r.mu.RLock()
	reads := make([]Contributor, 0, len(r.sources))
	for _, s := range r.sources {
		reads = append(reads, s.read)
	}
	r.mu.RUnlock()

	for _, read := range reads {
		cs, err := read()
		if err != nil {
			continue // 这一位整个产不出来：它本来就不可能是那张卡的主人
		}
		for _, c := range cs {
			if c.ID != conceptID {
				continue
			}
			acts, err := conceptActions(c)
			if err != nil {
				return "", err
			}
			for _, a := range acts {
				if a.ID != actionID {
					continue
				}
				if a.Run == nil {
					return "", i18n.E("{concept}.{action} has nothing to run",
						i18n.A{"concept": conceptID, "action": actionID})
				}
				return a.Run()
			}
			return "", i18n.E("{concept} has no action called {action} — the page is probably stale, reload it",
				i18n.A{"concept": conceptID, "action": actionID})
		}
	}
	return "", i18n.E("no view contributed a concept called {concept} — the page is probably stale, reload it",
		i18n.A{"concept": conceptID})
}

// conceptActions 是「这个概念上能点的按钮」。
//
// 绝大多数 Kind 就是 Concept.Actions 本身（贡献者在产出那张卡时构造的那一份）。有
// 一种例外：**数据本身是一叠子对象的 Kind**——那些按钮长在子对象上（一个 profile
// 一个「用它」），因为它们说的是「这一份」，而概念级那一栏装不下这个身份。
//
// 判据与 rowActions 那条一样：按 Kind 分派，不用「能不能转成某一种类型」试探——
// 形状与 Kind 的对应关系是契约，试探写法会在两种形状恰好都能转成功时安静地选错。
//
// 为什么这些按钮仍然走**概念动作**这条路（而不是行动作）：它们改的是**这一屏之外**
// 的东西（state.json 里那个客户端的链头），卡片本身一个字都不变，跑完重读一遍就对
// ——那正是概念动作的语义（见 Concept.Actions 与 RunRowAction 的分工）。
func conceptActions(c Concept) ([]Action, error) {
	if c.Kind != KindOverview {
		return c.Actions, nil
	}
	ov, ok := c.Data.(Overview)
	if !ok {
		return nil, i18n.E("{concept} is an overview but its data is not an Overview", i18n.A{"concept": c.ID})
	}
	out := make([]Action, 0, len(c.Actions)+len(ov.Cards)+1)
	out = append(out, c.Actions...)
	// 「自动」那张卡上的动作**也必须在这一摞里**：它的成员（`use:global:<profile>`）
	// 与「这一家跟着全局走」（`auto:<agent>`）都只在它身上，漏掉它等于那张卡上每个
	// 按钮点了都没反应——而 `RunConceptAction` 找不到时会报「没有这个动作」，
	// 界面上的表现是「点了弹一句看不懂的话」。
	if ov.Auto != nil {
		out = append(out, ov.Auto.Actions...)
	}
	for _, card := range ov.Cards {
		out = append(out, card.Actions...)
	}
	return out, nil
}

// RunRowAction 跑**一行**上的一个动作（见 Row.Actions 与 ChainRow.Actions）。
//
// 与 RunConceptAction 同一套：先问一遍贡献者把那张卡重新造出来（行是**数据长出来
// 的**，账本手里没有它们），再在那一行里找那个动作。
//
// 两步定位——概念 ID 然后是行 ID——不是啰嗦：行是一条独立的 binding（表里），或者
// 一份 profile 的一档（链卡里）。少了行这一层，「换成 X」就不知道该动谁。
//
// # 为什么它不写死 KindTable（2026-09-22 加的 chains）
//
// 第一版只认 Table，于是「一行上的动作」这件事被**形状**绑住了：链卡的行与表里的
// 行说的是同一件事（「这一行的去处」），却因为渲染方式不同而无处可挂。分派按 Kind
// 走之后，加一种带行的 Kind 不需要再动这个函数——它只多一个 case，而且漏了的那种
// 情况会在**调用那一刻**报出一句说得通的话（下面那个 default）。
func (r *Registry) RunRowAction(conceptID, rowID, actionID string) (string, error) {
	r.mu.RLock()
	reads := make([]Contributor, 0, len(r.sources))
	for _, s := range r.sources {
		reads = append(reads, s.read)
	}
	r.mu.RUnlock()

	for _, read := range reads {
		cs, err := read()
		if err != nil {
			continue
		}
		for _, c := range cs {
			if c.ID != conceptID {
				continue
			}
			acts, err := rowActions(c, rowID)
			if err != nil {
				return "", err
			}
			for _, a := range acts {
				if a.ID != actionID {
					continue
				}
				if a.Run == nil {
					return "", i18n.E("{row}.{action} has nothing to run",
						i18n.A{"row": rowID, "action": actionID})
				}
				return a.Run()
			}
			return "", i18n.E("row {row} of {concept} has no action called {action} — "+
				"the page is probably stale, reload it",
				i18n.A{"row": rowID, "concept": conceptID, "action": actionID})
		}
	}
	return "", i18n.E("no view contributed a concept called {concept} — the page is probably stale, reload it",
		i18n.A{"concept": conceptID})
}

// rowActions 从一张卡里按行 ID 取出那一行挂着的动作。
//
// 按 Kind 分派，不是按「能不能转成某一种类型」试探：形状与 Kind 的对应关系是**契约**
// （前端也是按 Kind 挑渲染器），试探写法会在两种形状恰好都能转成功时安静地选错一个。
func rowActions(c Concept, rowID string) ([]Action, error) {
	switch c.Kind {
	case KindTable:
		tbl, ok := c.Data.(Table)
		if !ok {
			return nil, i18n.E("{concept} is a table but its data is not a Table", i18n.A{"concept": c.ID})
		}
		for _, row := range tbl.Rows {
			if row.ID == rowID {
				return row.Actions, nil
			}
		}
		return nil, i18n.E("{concept} has no row called {row} — the page is probably stale, reload it",
			i18n.A{"concept": c.ID, "row": rowID})
	case KindChains:
		ch, ok := c.Data.(Chains)
		if !ok {
			return nil, i18n.E("{concept} is a chain card but its data is not a Chains", i18n.A{"concept": c.ID})
		}
		// 行 ID 在一张链卡里是**全概念唯一**的（`demo/heavy`）：行是「一份 profile 的
		// 一档」，而档位名单独一个在十份 profile 里会出现十次（见 ChainRow.ID）。
		// 所以这里全卡扫一遍，不需要先挑 profile。
		for _, card := range ch.Cards {
			for _, role := range card.Roles {
				if role.ID == rowID {
					return role.Actions, nil
				}
			}
		}
		return nil, i18n.E("{concept} has no row called {row} — the page is probably stale, reload it",
			i18n.A{"concept": c.ID, "row": rowID})
	// KindOverview **故意没有分支**：总览那张卡的行上没有动作。这一屏的探活是
	// 卡片级与节级的（见 OverviewCard.Actions），行级要再开一层身份（哪一张卡的
	// 哪一档），而链上真的要探的是链头——卡片级的那个按钮已经覆盖了它。落到下面
	// 那句会如实说出「这一种 Kind 的行不带动作」，比一个悄悄返回空表的空分支强。
	default:
		return nil, i18n.E("{concept} is a {kind}, whose rows carry no actions "+
			"(row actions live on tables and chain cards)",
			i18n.A{"concept": c.ID, "kind": c.Kind})
	}
}

// Snapshot 问一遍贡献者，返回这一刻的概念（按 Source, ID 排序）。
//
// 不带参数 = 问全部。带名字 = **只问这几位**——这条不是优化洁癖，是刷新语义的
// 一部分：界面每几秒刷一次的是计数器与日志（那几位产出很便宜），而配置那一位要
// 重读并重新解析每一份 profile、每一个源文件。让「刷一下计数器」顺带把整个配置
// 目录重读一遍，是把成本花在没人看的地方。
//
// 名字不认识时**不报错**（那位没装、或者刚被关掉）：刷新是后台行为，为一个已经
// 不在的贡献者让整次刷新失败，界面会停在上一帧。
//
// 排序是给**诊断输出**用的：同一份装配跑两次，界面上的卡片顺序必须一样，否则
// 用户会以为东西变了。版面顺序由前端决定，而它今天是**按 Source 分组**（每个源
// 一个小标题，组内按 ID）——Kind 只决定一张卡片怎么画，不决定它排在哪儿。
// （2026-09-20 更正：这里原来写的是「按 Kind 分组」，前端从来不是那么做的。）
func (r *Registry) Snapshot(only ...string) ([]Concept, error) {
	r.mu.RLock()
	sources := append([]source(nil), r.sources...)
	r.mu.RUnlock()
	if len(only) > 0 {
		want := make(map[string]bool, len(only))
		for _, name := range only {
			want[name] = true
		}
		kept := sources[:0:0]
		for _, s := range sources {
			if want[s.name] {
				kept = append(kept, s)
			}
		}
		sources = kept
	}

	var out []Concept
	owner := map[string]string{}
	for _, s := range sources {
		concepts, err := s.read()
		if err != nil {
			return nil, i18n.Ef(err, "interface: {source} could not produce its view: {err}",
				i18n.A{"source": s.name, "err": err})
		}
		for _, c := range concepts {
			c.Source = s.name
			// 重名当场报错，不做「后来者覆盖」：两个模块各自以为自己在报同一个 ID
			// 的时候，界面上只会显示其中一个，而另一个的修改点永远点不到——那是
			// 静默丢功能，跟前缀冲突（porthub）是同一类坑。
			//
			// 这条以前发生在注册那一刻，现在只能在这里——因为「有哪几个概念」本身
			// 就是产出函数说了算的。代价是它从装配期挪到了第一次有人看界面时；
			// 换来的是装配期一次盘都不读（见包注释）。
			if prev, dup := owner[c.ID]; dup {
				return nil, i18n.E("view: concept {id} is contributed by both {a} and {b} — "+
					"two concepts on one id means only one of them is ever reachable from the interface",
					i18n.A{"id": c.ID, "a": prev, "b": s.name})
			}
			if c.ID == "" {
				return nil, i18n.E("view: {source} contributed a concept with no id", i18n.A{"source": s.name})
			}
			if c.Kind == "" {
				return nil, i18n.E("view: concept {id} ({source}) has no kind — "+
					"the interface would not know how to render it",
					i18n.A{"id": c.ID, "source": s.name})
			}
			owner[c.ID] = s.name
			out = append(out, c)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Source != out[j].Source {
			return out[i].Source < out[j].Source
		}
		// Order 在前，ID 兜底：没声明 Order 的贡献者（今天大多数）行为与以前
		// 一模一样（全是 0，于是按 ID 排），声明了的就能把自己排到前面去。
		if out[i].Order != out[j].Order {
			return out[i].Order < out[j].Order
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}

// ErrUnknownConcept 表示「这个 ID 现在没人报」。
//
// 界面拿它区分 404（前端手里那份快照过期了，重载就好）与 500（某种真的坏了）。
var ErrUnknownConcept = errors.New("view: no such concept")

// Get 取一个概念（界面按 ID 找「这次修改针对谁」）。
//
// 它走的是 Snapshot：产出函数会**再跑一遍**。这是有意的——Apply 闭包由产出函数
// 现场构造（它得知道文件现在的路径与基线），缓存一份旧的等于拿过期知识去写盘。
func (r *Registry) Get(id string) (Concept, error) {
	all, err := r.Snapshot()
	if err != nil {
		return Concept{}, err
	}
	for _, c := range all {
		if c.ID == id {
			return c, nil
		}
	}
	return Concept{}, fmt.Errorf("%w: %s", ErrUnknownConcept, id)
}
