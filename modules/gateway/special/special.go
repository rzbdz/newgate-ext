// Package special 是 special_treatment 层：一组可插拔的「特殊照顾」插件。
//
// 为什么需要这一层
//
// 语义命名层的理想是「转发一个请求就该是转发」（docs/01-product.md）。但现实里
// 每个上游都有自己的怪癖，同一个 Anthropic 格式的请求，Claude 官方收下了，
// 某个 DeepSeek 网关就回 400。这类问题有三个共同点：
//
//  1. 只针对**某一个上游**，不能变成全局行为；
//  2. 修法是往请求里补一点东西，而不是改变语义；
//  3. 上游哪天修好了，这段代码就该能干净地摘掉。
//
// 所以它们不该散落在转发热路径的 if 里，而应该是一组注册进来的插件：
// 每个插件自己说明「我认哪个上游」（Match）、「我为什么存在」（Why）、
// 「我改了什么」（Apply 返回的 notes）。热路径只负责按顺序问一遍。
//
// 与 schema 修补的关系：tool schema 修补（rewrite/schema）是**所有**严格
// 校验器都需要的、按 JSON Schema 规范语义无操作的修补，所以它独立成层、
// 默认对所有上游生效。这里放的是「只有某家上游才需要」的补丁。
//
// 三条硬规则
//
//	fail-open：插件报错就当它没跑过，请求按原样发出去。宁可上游报错，
//	           也不能因为一个补丁把整条链弄断（docs/05-gateway.md）。
//	不静默：   改了什么必须回报 notes，由调用方写进日志。用户永远能知道
//	           自己的请求被动过哪一笔。
//	纯字节：   一律用 rewrite 包的字节手术，不做整体 JSON 往返。
package special

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/rzbdz/newgate-ext/modules/config/domain"
	"github.com/rzbdz/newgate-ext/modules/gateway/gatewaystate"
	"github.com/rzbdz/newgate-ext/modules/gateway/quirk"
	"github.com/rzbdz/newgate-ext/modules/gateway/rewrite"
	modules "github.com/rzbdz/newgate/component"
	i18n "github.com/rzbdz/newgate/lib/i18n"
)

// Request 是插件能看到的这次转发的上下文。
//
// 故意不含 http.Request：插件只该看请求的**语义归属**（发给谁、什么模型），
// 看不到也改不了头、认证、连接。想动这些的补丁不属于这一层。
//
// **State 不违反上面那条**，两者是不同的东西：http.Request 是**这次连接**（头、
// 认证、重试、响应生命周期——插件碰不得）；State 是**这份配置快照**（哪些开关
// 点被关了、超时多久），Respond 与 Route 本来就已经在单独收它，只有 Apply 与
// RebaseToolLoop 拿不到，于是插件没法查自己的开关点。补上它就是为了抹平这个
// 不一致。约定与前一条相同：**只读**，改了它不会传回热路径，只会让同一发请求
// 里的后续插件看到不一致的世界。
//
// 链上只有一份事实源：body 字节。r.Model 是它在每一步之后的**视图**——
// 插件改写了 body 的 model 字段后，由 Apply 框架自动同步（见
// syncContextModel），排在后面的插件立刻看到新值。插件不需要、也不应该
// 手动维护它：忘了维护就是「上游切了模型、下游拿旧模型做决定」的失真
// （2026-09-09 实抓过一次）。其余字段（Provider/Tier/Stream/Agent…）来自
// 路由和请求形态，不来自 body，链上不变。
//
// 定义放在这里而不是 gateway 根，是因为这个契约属于**插件层**：
// special 的实现要用它，插件作者也只用它。根 api.go 反过来别名它，
// 这样 gateway 根能 import special，而 special 不必回头 import 根（会成环）。
type Request struct {
	InModel  string
	Tier     string
	Model    string
	Provider string
	BaseURL  string
	Protocol string
	Path     string
	Stream   bool
	Agent    string

	// State 是本次请求看到的那份配置快照，插件据此查自己的运行期开关点
	// （modules/pluginmanager 的 Off/On）。它和本次请求的其它字段一样是**只读**的，
	// 而且**允许为 nil**（测试与旧调用面常见）——查开关点的函数都按「nil = 没关」
	// 处理，所以插件不需要自己判空。
	State *domain.State

	// Quirks 是**网关持有的那张「上游毛病」表**（见 modules/gateway/quirk）。
	//
	// 为什么由 Request 带过来、而不是让插件 import 那个包直接调包级函数
	// （2026-09-18 改）：包级 map 是 service locator——thinking 的 st-always 直接
	// 调 quirk.Has 就依赖上了网关数据面写进去的状态，而那条依赖在 Requires、
	// 依赖图、棘轮测试里全都看不见（docs/03-architecture.md §5 明写不许新增
	// service locator）。现在表归 gateway 所有、经它自己的请求契约交出来，插件
	// 拿到的就是**同一次请求**看到的那张表，与 State 是同一个套路。
	//
	// 允许为 nil（测试与旧调用面）：查表的函数按「没有 = 没学到」处理。
	Quirks *quirk.Table
}

// Plugin 是模块可以挂到请求处理链的 typed capability。
type Plugin interface {
	Name() string
	Why() string
	Match(*Request) bool
	Apply(body []byte, request *Request) (out []byte, notes []string, err error)
}

// ToolLoopMigrator 是可选的路由约束：某些上游不能原样接手别家尚未闭合的
// reasoning/tool 状态，但可以在安全候选都失败后做一次显式的有损重建。
// 它属于具体上游的 quirk，不应变成全局 fallback 规则。
type ToolLoopMigrator interface {
	NeedsToolLoopRebase(originProvider, originModel string, candidate *Request) (bool, string)
	RebaseToolLoop(body []byte, candidate *Request) ([]byte, string, error)
}

// Responder 是插件在构链/转发前的短路点：**直接替上游回答**，一个字节都不发
// 出去。返回 (result, true) 表示这条请求由插件定案；返回 false 走正常转发。
//
// 它与 Apply 的区别：Apply 改请求，Responder 造响应 —— 前者让请求「变成」另一
// 份请求，后者让请求「不用发」了。短路器要自己负责响应的**完整 HTTP/协议语义**
// （对 anthropic Messages 请求就要回一个合法的 messages 响应），调用方只负责写
// 头和打日志。
//
// 端点左侧不写死任何上游专有字符串：认不认得出「这是分类器」是具体插件的事
// （见 modules/claudecode/classifier-naked.go）。fail-open：插件 panic 就当它没
// 跑，走正常转发。
type Responder interface {
	Respond(body []byte, request *Request, state *domain.State) (result []byte, ok bool)
}

// NoteProvider 是短路插件的可选搭档：给自己替上游回答的每一发写一句人类可读
// 的说明。热路径把它原样写进日志行，因此**谁短路的、为什么**在日志里始终
// 看得见（「不静默」在短路路径上的落点）。
//
// 为什么不由调用方拼这句话：只有插件自己知道这次判决意味着什么。热路径去
// 猜（比如按插件名 if）会把插件私有知识搬回 core，正是这个仓库明令禁止的。
type NoteProvider interface {
	RespondNote(state *domain.State) string
}

// ModelLister 是插件对 `GET …/v1/models` 的回答：**这个客户端、这个 URL 下面**
// 能点名的模型有哪些。返回 (ids, true) 表示由它定案；false = 交回内核的缺省清单
// （档位 + 模块登记的动态角色键）。
//
// 为什么要有它（2026-11-04 接 hermes）：缺省清单与客户端无关，而有的客户端会
// **拿这份清单替换掉**它自己配置里的模型表——hermes 的 `/model` 对每个具名
// provider 都去问一次 `<base>/models`，问到了就用问到的。钉在某份 profile 上的
// URL（`/a/<agent>/p/<profile>/v1`）下面能点名的不只是档位，还有那份 profile 绑的
// 每个具体模型；缺省清单里没有它们，却有 omo 那批对这个客户端毫无意义的键。
// 「这个客户端该看见哪些」是客户端模块的知识，所以由它答，内核只负责问。
//
// 与 Responder 同一条 fail-open：插件 panic、或者被 `newgate st off` 关掉，就当它
// 没答，回缺省清单。
type ModelLister interface {
	ListModels(agent, profile string) (ids []string, ok bool)
}

// RoutePlugin 是 special 层在构链前的扩展点。插件只返回路由意图；如何校验
// binding、构造 fallback 链仍由 resolve 负责。
type RoutePlugin interface {
	Route(body []byte, request *Request, state *domain.State) (RouteDecision, bool)
}

// RouteDecision 是路由插件交回主路由器的声明式意图。
// 插件可以建议链头、档位和首字节超时，但不能自行构造或执行 fallback。
type RouteDecision struct {
	Plugin           string
	Tier             string
	Head             *domain.Binding
	FirstByteTimeout time.Duration
	Note             string
	OverrideNote     string
	OverrideFailNote string
	Metric           string
}

// MetricKey 把插件私有指标规范化到 special 命名空间；缺少任一部分就不发指标。
func (d RouteDecision) MetricKey() string {
	if d.Plugin == "" || d.Metric == "" {
		return ""
	}
	return "special." + d.Plugin + "." + d.Metric
}

// StatusItem 是插件贡献给 `newgate status` 的结构化信息。CLI 只负责排版，
// 不知道 classifier、DeepSeek 等具体机制。
type StatusItem struct {
	Label string
	Value string
}

// StatusProvider 允许插件贡献状态项，而不让 CLI 认识具体插件类型。
type StatusProvider interface {
	Status(state *domain.State) []StatusItem
}

// BindingProvider 让路由插件声明自己可能引入、但不在 profile 中的 binding。
// metrics 用它构造完整观测集合，不需要知道任何插件配置字段。
type BindingProvider interface {
	Bindings(state *domain.State) []domain.Binding
}

// MetricInfo 描述指标对应的动作和排障提示，使计数值保留语义。
type MetricInfo struct {
	Action string
	Hint   string
}

// MetricProvider 是插件可选的指标元数据端口。
type MetricProvider interface {
	Metrics() []MetricInfo
}

// ResponseAuditor 让插件只读检查上游响应，用于发现协议异常而不改写响应。
type ResponseAuditor interface {
	AuditResponse(body []byte) string
}

// Egress 改写**上游回来的响应**，一条 SSE 事件一条地改。
//
// 它与请求侧的 Plugin 对称，而且是这个模型里唯一一处「吐出去的字节不再是上游
// 原话」的地方——所以规则写死在这里，不靠调用方自觉：
//
//   - **每一条 event 原样进、原样出。** 调用方按 SSE 的事件边界切，把**这个
//     事件的原始字节**（含 `event:` 行与结尾空行）交给它；返回 nil 就是一字不改。
//     返回的字节是**这条事件的完整替代**，调用方负责写出去。
//   - **只改你认领的那几条。** 没认出来的一律返回 nil——哪怕只是「顺手统一一下
//     格式」，那也是把上游的字节换成了我们的，而这条路上没有版本号能让人发现。
//   - **fail-open：panic 或返回空 slice 都当没改。** 与 Apply 同一条规矩
//     （见包注释）：宁可少改一条事件，不能因为一个补丁把整条流转断。
//
// 它是**接口而不是函数**：响应改写几乎总是带状态（要记住这一发的哪几个 output
// item 被改成了什么），而状态该活在这一发请求上——见 Egressor。
type Egress interface {
	Egress(event []byte) []byte
}

// Egressor 是插件的可选搭档：**这一发的响应**由谁改。
//
// 在请求阶段问一次（插件刚 Apply 完、请求还没发出去），返回非 nil 就表示这一发
// 的响应归它改。每次问都该给一个**新实例**，理由是响应改写的状态只属于这一发：
// 插件实例是进程级单例，而多个请求同时在线上跑，做成每次一个新对象就不需要锁，
// 也不会把 A 那发的判断带到 B 那发。
//
// `original` 是**客户端发来的原始请求字节**（不是 Apply 之后的那一份）。响应侧要
// 改哪几条，判据往往只在原始请求里：比如客户端声明了哪些工具是它自己的 custom
// 类型，而 Apply 已经把那些声明改成上游认的形状了——想从改完的 body 倒推回去是
// 猜，从原文里读是事实。
type Egressor interface {
	// Egress 认领这一发的响应改写；nil = 这一发不归我。
	Egress(req *Request, original []byte) Egress
}

// Registry 持有当前网关的插件集合、稳定执行顺序和注册所有权 token。
type Registry struct {
	mu      sync.RWMutex
	plugins []Plugin
	tokens  map[string]uint64
	next    uint64
}

var (
	defaultMu       sync.RWMutex
	defaultRegistry = &Registry{}
)

// NewRegistry 创建与其他应用实例隔离的空插件注册表。
func NewRegistry() *Registry { return &Registry{} }

// SetDefault 设置旧调用面使用的注册表；新组件装配应优先使用有所有权的 InstallDefault。
func SetDefault(registry *Registry) {
	if registry == nil {
		panic("special: nil default registry")
	}
	defaultMu.Lock()
	defaultRegistry = registry
	defaultMu.Unlock()
}

// InstallDefault 安装带所有权的兼容注册表并返回恢复函数。
// 恢复时会核对当前实例，避免旧组件回滚覆盖后来启动的新 owner。
func InstallDefault(registry *Registry) func() {
	if registry == nil {
		panic("special: nil default registry")
	}
	defaultMu.Lock()
	previous := defaultRegistry
	defaultRegistry = registry
	defaultMu.Unlock()
	return func() {
		defaultMu.Lock()
		if defaultRegistry == registry {
			defaultRegistry = previous
		}
		defaultMu.Unlock()
	}
}

func currentRegistry() *Registry {
	defaultMu.RLock()
	registry := defaultRegistry
	defaultMu.RUnlock()
	return registry
}

// Ordered 用插件名声明执行先后约束。这是一张依赖图而不是数字优先级；
// 新增无关插件不会悄悄改变既有插件的相对顺序。
type Ordered interface {
	Before() []string
	After() []string
}

// Register 是兼容调用面；生产装配由 Gateway 组件持有 Registry，并把注册端口
// 注入其他组件。执行顺序由 Ordered 的具名依赖图决定，不依赖加载顺序。
func Register(p Plugin) {
	if _, err := currentRegistry().Register(p); err != nil {
		panic(err)
	}
}

// Register 把插件加入实例注册表，并返回只属于本次注册的撤销句柄。
func (r *Registry) Register(p Plugin) (modules.Release, error) {
	if p == nil || p.Name() == "" {
		return nil, fmt.Errorf("special: plugin name is required")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, existing := range r.plugins {
		if existing.Name() == p.Name() {
			return nil, fmt.Errorf("special: duplicate plugin %s", p.Name())
		}
	}
	next := append(append([]Plugin(nil), r.plugins...), p)
	var orderErr error
	func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				orderErr = fmt.Errorf("%v", recovered)
			}
		}()
		next = orderPlugins(next)
	}()
	if orderErr != nil {
		return nil, orderErr
	}
	if r.tokens == nil {
		r.tokens = make(map[string]uint64)
	}
	r.next++
	token := r.next
	// tokens 这张表**不是**为了容忍重复注册（那已经在上面被拒了），而是为了让
	// **陈旧 Release 无害**：注销之后再注册同一个键会拿到新 token，那个旧
	// Release 一旦晚到就会把新注册删掉。比对 token 拦的正是这一种。
	// （看代码时容易以为这条分支不可达——那是漏掉了「注销 → 再注册」。）
	r.tokens[p.Name()] = token
	r.plugins = next
	return func() error {
		r.mu.Lock()
		defer r.mu.Unlock()
		if r.tokens[p.Name()] != token {
			return nil
		}
		delete(r.tokens, p.Name())
		for i, existing := range r.plugins {
			if existing.Name() == p.Name() {
				r.plugins = append(r.plugins[:i], r.plugins[i+1:]...)
				break
			}
		}
		return nil
	}, nil
}

func orderPlugins(plugins []Plugin) []Plugin {
	byName := make(map[string]Plugin, len(plugins))
	index := make(map[string]int, len(plugins))
	edges := make(map[string]map[string]bool, len(plugins))
	indegree := make(map[string]int, len(plugins))
	for i, plugin := range plugins {
		byName[plugin.Name()] = plugin
		index[plugin.Name()] = i
		edges[plugin.Name()] = map[string]bool{}
	}
	addEdge := func(from, to string) {
		if _, ok := byName[from]; !ok {
			return
		}
		if _, ok := byName[to]; !ok || edges[from][to] {
			return
		}
		edges[from][to] = true
		indegree[to]++
	}
	for _, plugin := range plugins {
		ordered, ok := plugin.(Ordered)
		if !ok {
			continue
		}
		for _, before := range ordered.Before() {
			addEdge(plugin.Name(), before)
		}
		for _, after := range ordered.After() {
			addEdge(after, plugin.Name())
		}
	}

	var ready []string
	for name := range byName {
		if indegree[name] == 0 {
			ready = append(ready, name)
		}
	}
	sort.Slice(ready, func(i, j int) bool { return index[ready[i]] < index[ready[j]] })
	out := make([]Plugin, 0, len(plugins))
	for len(ready) > 0 {
		name := ready[0]
		ready = ready[1:]
		out = append(out, byName[name])
		for next := range edges[name] {
			indegree[next]--
			if indegree[next] == 0 {
				ready = append(ready, next)
				sort.Slice(ready, func(i, j int) bool {
					return index[ready[i]] < index[ready[j]]
				})
			}
		}
	}
	if len(out) != len(plugins) {
		panic(fmt.Sprintf("special: plugin ordering cycle among %v", pluginNames(plugins)))
	}
	return out
}

func pluginNames(plugins []Plugin) []string {
	names := make([]string, 0, len(plugins))
	for _, plugin := range plugins {
		names = append(names, plugin.Name())
	}
	return names
}

// Plugins 已注册的插件（副本，调用方改不坏注册表）。
func Plugins() []Plugin {
	registry := currentRegistry()
	registry.mu.RLock()
	defer registry.mu.RUnlock()
	out := make([]Plugin, len(registry.plugins))
	copy(out, registry.plugins)
	return out
}

// ListModels 按注册顺序问 ModelLister；第一个定案的生效。返回 (ids, 插件名, ok)。
func ListModels(agent, profile string, state *domain.State) ([]string, string, bool) {
	if state == nil || !gatewaystate.SpecialEnabled(state) {
		return nil, "", false
	}
	for _, p := range Plugins() {
		if gatewaystate.PluginOff(state, p.Name()) {
			continue
		}
		lister, is := p.(ModelLister)
		if !is {
			continue
		}
		var ids []string
		var ok bool
		func() {
			defer func() { _ = recover() }()
			ids, ok = lister.ListModels(agent, profile)
		}()
		if ok {
			return ids, p.Name(), true
		}
	}
	return nil, "", false
}

// Respond 按注册顺序询问短路插件；第一个能直接替上游回答的生效。
// 返回 (响应体, 生效插件名, ok)。
func Respond(body []byte, request *Request, state *domain.State) ([]byte, string, bool) {
	if state == nil || !gatewaystate.SpecialEnabled(state) {
		return nil, "", false
	}
	for _, p := range Plugins() {
		if gatewaystate.PluginOff(state, p.Name()) {
			continue
		}
		responder, is := p.(Responder)
		if !is {
			continue
		}
		var out []byte
		var matched bool
		func() {
			// fail-open：插件 panic 就当它没跑。短路器替用户做决定，绝不能让
			// 一个坏插件把整条链的回答也夺走。
			defer func() { _ = recover() }()
			out, matched = responder.Respond(body, request, state)
		}()
		if matched {
			return out, p.Name(), true
		}
	}
	return nil, "", false
}

// RespondNote 问刚生效的短路插件「这一发意味着什么」，供调用方写进日志。
// 插件没实现 NoteProvider（或已注销）就回空串，调用方照常打自己的日志。
func RespondNote(name string, state *domain.State) string {
	for _, p := range Plugins() {
		if p.Name() != name {
			continue
		}
		if provider, ok := p.(NoteProvider); ok {
			return provider.RespondNote(state)
		}
		return ""
	}
	return ""
}

// Route 按注册顺序询问路由插件；第一个明确认领请求的决定生效。
func Route(body []byte, request *Request, state *domain.State) (RouteDecision, bool) {
	if state == nil || !gatewaystate.SpecialEnabled(state) {
		return RouteDecision{}, false
	}
	for _, p := range Plugins() {
		if gatewaystate.PluginOff(state, p.Name()) {
			continue
		}
		router, ok := p.(RoutePlugin)
		if !ok {
			continue
		}
		if decision, matched := router.Route(body, request, state); matched {
			decision.Plugin = p.Name()
			return decision, true
		}
	}
	return RouteDecision{}, false
}

// Statuses 汇总所有启用插件贡献的状态行。
func Statuses(state *domain.State) []StatusItem {
	if state == nil || !gatewaystate.SpecialEnabled(state) {
		return nil
	}
	var out []StatusItem
	for _, p := range Plugins() {
		if gatewaystate.PluginOff(state, p.Name()) {
			continue
		}
		if reporter, ok := p.(StatusProvider); ok {
			out = append(out, reporter.Status(state)...)
		}
	}
	return out
}

func Bindings(state *domain.State) []domain.Binding {
	if state == nil || !gatewaystate.SpecialEnabled(state) {
		return nil
	}
	var out []domain.Binding
	for _, p := range Plugins() {
		if gatewaystate.PluginOff(state, p.Name()) {
			continue
		}
		if provider, ok := p.(BindingProvider); ok {
			out = append(out, provider.Bindings(state)...)
		}
	}
	return out
}

// MetricHint 让指标说明跟着产生指标的插件走，避免 CLI 维护插件名 switch。
func MetricHint(key string) (string, bool) {
	for _, p := range Plugins() {
		provider, ok := p.(MetricProvider)
		if !ok {
			continue
		}
		for _, metric := range provider.Metrics() {
			if key == "special."+p.Name()+"."+metric.Action {
				return metric.Hint, true
			}
		}
	}
	return "", false
}

func AuditResponse(body []byte) string {
	var reports []string
	for _, plugin := range Plugins() {
		if auditor, ok := plugin.(ResponseAuditor); ok {
			if report := auditor.AuditResponse(body); report != "" {
				reports = append(reports, plugin.Name()+":\n"+report)
			}
		}
	}
	return strings.Join(reports, "\n")
}

// ClaimEgress 问一遍插件：**这一发的响应**归谁改。
//
// 在请求刚被 Apply 完、还没发出去时问一次，拿到的实例跟着这一发走到底。
// 返回 nil 表示没人认领，调用方走原来那条逐字节直通的转发。
//
// 为什么要先问（而不是让每个事件都过一遍插件）：改响应要把 SSE 拆成事件，而拆帧
// 让「一个字节都不动」这条承诺多了一层。没有认领者时调用方必须一眼看得出可以走
// 原来的快路——所以判断放在**发请求之前**，与真正执行时用的是同一份名单。
//
// 只认第一个（按注册顺序问，与 Respond / Route 同一条）：响应只有一个出口，两家
// 各改各的拼出来的是谁都没写过的东西。
//
// `original` 原样转给认领者（见 Egressor 的说明：它要的是客户端发来的那一份）。
func ClaimEgress(req *Request, original []byte, off func(name string) bool) Egress {
	for _, p := range Plugins() {
		if off != nil && off(p.Name()) {
			continue
		}
		claimer, ok := p.(Egressor)
		if !ok {
			continue
		}
		var egress Egress
		func() {
			// fail-open：认领器 panic 就当它没认领，这一发照原样转发。
			defer func() { _ = recover() }()
			egress = claimer.Egress(req, original)
		}()
		if egress != nil {
			return egress
		}
	}
	return nil
}

// RewriteEvent 把**一条 SSE 事件**交给认领者改写；它说不动就返回 nil。
//
// panic 当没改（fail-open，与 Apply 同）：宁可少改一条事件，也不能因为一个补丁
// 把整条流转断。
func RewriteEvent(e Egress, event []byte) []byte {
	if e == nil {
		return nil
	}
	var out []byte
	func() {
		defer func() { _ = recover() }()
		out = e.Egress(event)
	}()
	if len(out) == 0 {
		return nil
	}
	return out
}

// ToolLoopNeedsRebase 询问匹配 candidate 的插件是否需要先有损重建，才能接手
// origin 产生的未闭合 tool loop。
func ToolLoopNeedsRebase(originProvider, originModel string, candidate *Request,
	off func(name string) bool) (bool, string) {
	for _, p := range Plugins() {
		if off != nil && off(p.Name()) {
			continue
		}
		migrator, ok := p.(ToolLoopMigrator)
		if !ok || !p.Match(candidate) {
			continue
		}
		if needed, why := migrator.NeedsToolLoopRebase(
			originProvider, originModel, candidate); needed {
			return true, p.Name() + ": " + why
		}
	}
	return false, ""
}

// RebaseToolLoop 让声明需要 rebase 的插件改写请求。失败开放：插件出错时返回
// 原 body 和一条可见 note，由上游给出真实错误，不让补丁本身切断整条链。
func RebaseToolLoop(body []byte, originProvider, originModel string, candidate *Request,
	off func(name string) bool) Result {
	res := Result{Body: body}
	for _, p := range Plugins() {
		if off != nil && off(p.Name()) {
			continue
		}
		migrator, ok := p.(ToolLoopMigrator)
		if !ok || !p.Match(candidate) {
			continue
		}
		needed, _ := migrator.NeedsToolLoopRebase(originProvider, originModel, candidate)
		if !needed {
			continue
		}
		out, note, err := migrator.RebaseToolLoop(body, candidate)
		if err != nil {
			note := i18n.T("lossy tool loop rebase skipped ({err})", i18n.A{"err": err.Error()})
			res.Notes = []string{p.Name() + ": " + note}
			return res
		}
		if note == "" || out == nil {
			return res
		}
		res.Body, res.Changed = out, true
		res.Notes = []string{p.Name() + ": " + note}
		res.Events = []Event{{Plugin: p.Name(), Action: "tool_loop_rebase", Note: note}}
		return res
	}
	return res
}

// Result 一次 special_treatment 的结果。
type Result struct {
	Body    []byte   // Changed 为 false 时等于传进来的 body
	Notes   []string // 形如 "deepseek: 注入 thinking…"，逐条写日志
	Events  []Event
	Changed bool
}

type Event struct {
	Plugin string
	Action string
	Note   string
}

func (e Event) MetricKey() string {
	if e.Plugin == "" {
		return ""
	}
	if e.Action == "" || e.Action == "rewrite" {
		return "special." + e.Plugin
	}
	return "special." + e.Plugin + "." + e.Action
}

// Apply 按注册顺序跑一遍所有匹配的插件，串联改写。
//
// 装饰器链的完整语义：body 一路传（res.Body = out），上下文跟着 body 走
// （每步后 syncContextModel）——下一个插件拿到的 body 和 r.Model 都是上
// 一个插件改过的最新版，不存在「body 已被改、上下文还是旧的」的窗口。
//
// off 用来跳过被用户单独关掉的插件（可以传 nil）。
// 任何一个插件报错都只影响它自己：记一条 note，body 保持上一个插件的结果。
func Apply(body []byte, r *Request, off func(name string) bool) Result {
	res := Result{Body: body}
	for _, p := range Plugins() {
		if off != nil && off(p.Name()) {
			continue
		}
		if !p.Match(r) {
			continue
		}
		out, notes, err := p.Apply(res.Body, r)
		if err != nil {
			note := i18n.T("skipped ({err}); sending the request as-is", i18n.A{"err": err.Error()})
			res.Notes = append(res.Notes, p.Name()+": "+note)
			continue
		}
		if len(notes) == 0 || out == nil {
			continue // 这个插件这次没什么要补的
		}
		res.Body = out
		res.Changed = true
		for _, n := range notes {
			res.Notes = append(res.Notes, p.Name()+": "+n)
			res.Events = append(res.Events, Event{Plugin: p.Name(), Action: "rewrite", Note: n})
		}
		syncContextModel(res.Body, r)
	}
	return res
}

// syncContextModel 让上下文跟上 body：从 body 读回顶层 model 写进 r.Model。
// 这是链上唯一需要维护的派生字段（其余字段不来自 body）。读不出就保持
// 原值——绝不让一个改坏了的 body 把上下文也带坏。
func syncContextModel(body []byte, r *Request) {
	if m, has := rewrite.TopLevelString(body, "model"); has && m != "" && m != r.Model {
		r.Model = m
	}
}
