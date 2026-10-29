package webdashboard

import (
	"context"
	"encoding/json"
	"io/fs"
	"net/url"

	modules "github.com/rzbdz/newgate/component"
)

// 本文件是**界面提供出去的那几个端口**：皮肤登记处（theme）与扩展端点登记处
// （extension）。两者形状一样——模块 Optional 依赖、在自己的 Start 里注册、
// Stop 时还回来；界面不认识任何模块。
//
// # 为什么不把皮肤写死在这个包里
//
// 与概念账本（core/lib/view）是同一条规矩的另一份实现：**界面不认识任何皮肤**，
// 它只知道「有一张表，每一条给一份 CSS」。加一套皮肤 = 加一个目录，
// 不改这个包、不改前端。删掉全部皮肤模块时界面照常工作（走 app.css 里那套出厂
// 令牌）——那是「没装任何 ui 时一切照常」那条规矩在**皮肤**这一层的同一句话。
//
// # 方向
//
// 箭头只有「皮肤模块 → 界面」一个方向（皮肤模块 Optional 依赖本端口）。与
// cli/extension 那边一样：界面不能反过来依赖任何一个皮肤，否则「删掉它界面还
// 成立吗」这个判据就答不上来了。

// Theme 是一套皮肤。
type Theme struct {
	// ID 是机器标记，同时也是 `state.json` 里存下来的那个值。不翻译。
	//
	// 约定：皮肤模块的目录名是 `theme-<id>`（`theme-anthropic` → `anthropic`）。
	// 这只是一条约定、不是机制——机制是**目录名由模块自己声明**（见 modules 的
	// 扫描规则），所以一个叫别的名字的模块照样能注册一个 ID 叫做 `foo` 的皮肤。
	ID string
	// Name 是给人看的名字（可以翻译）。
	//
	// 闭包：登记发生在各模块的 Start 里，那时候语言层装好没有是没有保证的
	// （见 lib/view 的 Title——同一个理由）。
	Name func() string
	// Dark 说的是这套皮肤**是不是深色底**。
	//
	// 它必须由皮肤自己声明、走报文给前端，而不是让前端从颜色的亮度里猜：浏览器
	// 那一侧有几样东西不归 CSS 变量管（滚动条、`<input type=checkbox>` 的勾、
	// 自动填充的底色），它们跟着 `color-scheme` 走。猜错的表现是「一套很浅的皮肤
	// 配着一条黑滚动条」，而那种错没人会报成 bug。
	Dark bool
	// CSS 是这套皮肤对**设计令牌**的覆盖，形如
	// `--bg: #000; --panel: #0b0b0b;`。
	//
	// 只有令牌，**不含选择器**：皮肤能改的是那十几个变量的值，不是版面。允许写
	// 选择器就等于允许一套皮肤重排整个界面，而「这行样式是谁加的」在那时候要靠
	// 读一段运行时注入的字符串才能回答。令牌表是 app.css 的 `:root` 那一段，
	// 少给一个不会报错——它会落回出厂的暖色值（半套皮肤很难看，但那是作者看得见
	// 的难看，比静默换一套配色好）。
	CSS string
}

// ThemeService 是皮肤的登记处。
//
// 形状与 view.Service 的 Register 同：交进去、拿回一个放销，Stop 时逆序还回来。
// 注册在各模块的 Start 里做，而**那个顺序是装配图给的**（皮肤模块 Optional 依赖
// 本端口，于是它们一定排在本模块之后）。
type ThemeService interface {
	RegisterTheme(t Theme) (modules.Release, error)
}

// ThemeCapability 是它在装配图上的名字。
var ThemeCapability = modules.NewCapability[ThemeService]("web-theme")

// ---------- 扩展端点 ----------
//
// BFF 的概念账本（core/lib/view）只装**能被画成一张卡**的东西。而有一类功能
// 不是「一张卡」：用户点开模型下拉，要问一句「这家上游此刻提供哪些模型」。它
// 没有可以持久化的形状，也不该在每次快照里都去碰网络（见 config/view.go 里
// profileData 的注释）。
//
// 所以这里开第二条口子：**一个名字 + 一个函数**。模块在自己的 Start 里登记，
// BFF 在 `/api/ext/<名字>` 上原样转交。它仍然**不认识任何模块**——它只认识
// 「有人登记了这个名字」，就像 porthub 只认识「有人挂了这段前缀」。
//
// 方向与皮肤、概念账本完全一样：箭头只有「模块 → 界面」。没装任何扩展时这个
// 端点一律 404，界面自己回落到它能拿到的数据（模型下拉会回落到 providers.json
// 里声明的清单）。

// ExtensionRequest 是一次扩展调用拿到的全部输入。
//
// Body 是**原样**的 JSON：形状由登记的那个模块解释，BFF 不解析它——与
// apply 那条 `edit` 是同一条规矩。Context 来自这次 HTTP 请求，扩展做网络调用
// 时要沿着它超时/取消（客户端关掉页面，上游请求就该停）。
type ExtensionRequest struct {
	Context context.Context
	Method  string
	Query   url.Values
	Body    json.RawMessage
}

// ExtensionHandler 回答一次扩展调用。返回的值原样写成 JSON（200）；返回 error
// 时 BFF 回 400 并把那句话给界面看（**不包装**：那是扩展对自己数据的说法）。
type ExtensionHandler func(ExtensionRequest) (any, error)

// ExtensionService 是扩展端点的登记处。
//
// 形状与 ThemeService 同：交进去、拿回撤销，Stop 时还回来。重名当场报错——
// 两个模块抢同一个名字时只有一个能收到请求，而那件事在运行期表现为「另一个
// 模块的按钮点了没反应」，没人能从界面上看出来。
type ExtensionService interface {
	Register(name string, h ExtensionHandler) (modules.Release, error)
}

// ExtensionCapability 是它在装配图上的名字。
var ExtensionCapability = modules.NewCapability[ExtensionService]("web-extension")

// ---------- 前端插件 ----------
//
// 概念账本（core/lib/view）给的是**数据 + 一种已知的画法**（Kind）；扩展端点给的是
// **数据**。两者都要求前端事先认识那种形状——于是一个模块想要一块「前端不认识」的
// 界面时，过去唯一的办法是往前端里加一个组件、让前端认识它（指纹那一屏就是这么
// 进去的，见 README 的「谁越界了」）。那条路的代价是：前端目录里长出了模块的知识，
// 摘掉模块，前端里那块代码还在。
//
// 这一口让模块**自带前端代码**：模块把一个 ES module（和它要的别的静态文件）嵌进
// 自己的二进制段里，登记一个名字 + 它要填的**槽位**。BFF 在 `/plugins/<名字>/…`
// 上原样发这些文件，在 `/api/plugins` 上发一张清单；界面按槽位把每个插件挂上去。
// 界面认识的只有槽位（它自己的版面）和「插件长什么样」这份契约，不认识任何一个
// 插件——与 Kind 是同一条判据：**前端提供框架接口，模块往里填**。
//
// # 插件的 JS 契约（版本 1）
//
//	export function mount(el, host) { …; return () => { /* 卸载 */ } }
//
//   - el：界面给的一个空容器，插件在里面随便画；
//   - host.ext(name, body)：POST `/api/ext/<name>`（与模块自己登记的扩展端点配对），
//     回 JSON；非 2xx 时 reject 一个 Error（message 是 BFF/扩展给的那句话）；
//   - host.asset(path)：本插件自己的静态文件地址；
//   - host.lang：界面当前的语言 tag。
//
// 样式用界面的设计令牌（`var(--panel)`、`var(--ink)`、`var(--ok)`…，见 app.css 的
// `:root`），皮肤换了插件自己就跟着换。文案**由插件自己的扩展端点给**（后端走
// i18n.T，与 CLI 同一份目录表）：前端的消息目录是界面的，不是模块的。
//
// 一个插件挂不上（加载失败、mount 抛错）只影响它自己那一格：界面照常（fail-open）。

// PluginSlot 是界面版面上一处可以挂插件的位置。
type PluginSlot string

const (
	// SlotTopbar 是顶栏右侧、皮肤按钮左边的一小格：放一颗按钮/徽标，
	// 点开的面板由插件自己画（相对这一格定位）。
	SlotTopbar PluginSlot = "topbar"
)

// pluginSlots 是今天界面认识的全部槽位。登记一个不在这里的槽位当场报错：
// 静默接受的话，插件在界面上哪儿都不出现，而作者看不出是拼错了还是没加载。
var pluginSlots = map[PluginSlot]bool{SlotTopbar: true}

// Plugin 是一个模块自带的前端代码。
type Plugin struct {
	// Name 是机器标记，也是 URL 的一段（`/plugins/<Name>/…`）。字符集同扩展端点。
	Name string
	// Assets 是插件的文件（通常是模块里的 `//go:embed web`）。
	Assets fs.FS
	// Entry 是 Assets 里那个 ES module 的路径（导出 mount）。
	Entry string
	// Slots 是它要填的槽位。
	Slots []PluginSlot
}

// PluginService 是前端插件的登记处。形状与 ExtensionService 同。
type PluginService interface {
	RegisterPlugin(p Plugin) (modules.Release, error)
}

// PluginCapability 是它在装配图上的名字。
var PluginCapability = modules.NewCapability[PluginService]("web-plugin")
