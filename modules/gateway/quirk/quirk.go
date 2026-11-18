// Package quirk 记「这个上游有什么毛病」，从它自己的报错现场学。
//
// 为什么需要它
//
// special_treatment 的插件靠 Match 认领请求，而 Match 只能看模型名、provider
// 名、URL 这些**静态**信息。有些毛病是静态看不出来的：
//
//	[1210][该模型始终思考，不支持关闭思考；请使用 low、high 或 max。]
//
// 这句话是 glm-5.3 在**带 tools** 的请求上回的。实测（api.rvcompute.com 聚合
// 器 + glm-5.3）：
//
//	tools + 不给 reasoning_effort        → 400 / 1210
//	tools + reasoning_effort=low|high    → 200
//	同样带 tools 的 glm-5.2 / glm-4.7    → 200
//
// 成因基本可以确定：聚合器看见 tools 就自己给上游塞了「关闭思考」（很多模型
// 不支持思考+工具同时用），而 glm-5.3 是始终思考的模型，于是拒掉。我们改不了
// 聚合器，但给一个**显式**的 reasoning_effort 就能盖过它塞的那个值。
//
// 光看模型名猜不出来哪个模型「始终思考」——那是模型版本的属性，会变，也没有
// 任何接口能查。所以这里的策略是：**认签名、记下来、下次带上补丁**。
// 来源有两个，都往同一个注册表里写：
//
//	Learn()  转发时撞上 4xx，认出签名就记一笔（一次失败换永久免疫）
//	Probe()  newgate probe 主动打一发探出来，省掉那一次失败
//
// 只在内存里。进程重启就忘了，重新学一次的代价是一个请求。要它更持久就该
// 落到 providers.json 里，但那是用户的配置文件，我们不该悄悄往里写东西。
package quirk

import (
	"fmt"
	"strings"
	"sync"

	modules "github.com/rzbdz/newgate/component"
	i18n "github.com/rzbdz/newgate/lib/i18n"
)

// Flag 一个已知毛病。用位掩码，一个 (provider, model) 可以同时有好几个。
type Flag uint32

const (
	// NoThinkingDisable 这个模型不接受「关闭思考」，必须给显式的思考强度。
	NoThinkingDisable Flag = 1 << iota
)

func (f Flag) String() string {
	if f&NoThinkingDisable != 0 {
		return i18n.T("thinking cannot be disabled (an explicit reasoning_effort is required)", nil)
	}
	return i18n.T("unknown", nil)
}

// AllFlags 全部已知的毛病位，按定义顺序。
//
// **加一个新的 Flag 常量时必须同时加到这里。** 它有两个消费者，两边都会因为
// 漏掉而静默出错：
//
//   - probe 的能力缓存（cache.go 的 capture）逐位问「这个上游有没有这一条」
//     再落盘。而恢复侧（apply）读的是整张位掩码，是通用的——所以漏一位的
//     症状不是报错，而是**那一位永远存不进去**：重启/重新探活后上游的毛病
//     又得靠撞一次 400 才学回来。
//   - `Flag.String()` 的展示（哪几位要写成人话）。
//
// 2026-09-18 之前 capture 把 NoThinkingDisable 硬编码在一行 if 里，就是这个
// 形状：加第二位的那天，落盘会静默少一位。
func AllFlags() []Flag {
	return []Flag{NoThinkingDisable}
}

// Table 是这张「上游毛病」表。
//
// **它是一个实例，不是包级变量**（2026-09-18 改）：包级 map 是典型的 service
// locator——别的模块（thinking 的 st-always）直接调 `quirk.Has(...)` 就依赖上了
// gateway 数据面写进去的状态，而那条依赖在 `Requires`、依赖图、棘轮测试里都看
// 不见。docs/03-architecture.md §5 明写「不得新增 service locator」，本仓库也为
// breaker 修过一模一样的问题（见 modules/breaker/shape.go 的说明：健康表做成
// table 实例，就是因为它要在测试里起很多份而不互相污染）。
//
// 现在由 gateway 持有唯一的 Default，并**随每次请求交给插件**（见
// modules/gateway/special 的 `Request.Quirks`）——想要它的模块读的是手里那个
// 请求上的字段，也就是网关自己交出来的东西，而不是一个它可以绕过去的全局。
type Table struct {
	mu         sync.RWMutex
	flags      map[string]Flag
	signatures []Signature
}

// Default 是 gateway 自己的那一份（probe / forward / Request 的填充都用它）。
//
// **它是 gateway 的内部约定，不是给别的模块用的**。别的模块要读这张表，走
// `special.Request.Quirks`（网关在每次请求上带过去的那一个）——那是一条**声明过**
// 的依赖（消费方必然已经 Need 了 gateway 的端口），而这里写 `quirk.Default.Has(...)`
// 会重新引入一条依赖图上看不见的 service locator。
//
// 为什么不把它设成不导出：probe 与 forward 是本模块的**兄弟包**，它们要的是同一
// 份状态，而 Go 的 internal/ 会把 gopher 挡在外面却也挡住它们命名这个类型；真正
// 要防的是「别的模块直接调」，那一条由 `special.Request.Quirks` 这条正路 + 下面
// 这段注释管着。
var Default = NewTable()

// NewTable 建一张空表（测试里要隔离时用）。
func NewTable() *Table { return &Table{flags: map[string]Flag{}} }

func key(provider, model string) string { return provider + "/" + model }

// Mark 记一笔。返回 true 表示这是**新学到的**——调用方据此决定要不要打日志，
// 免得同一件事每个请求都刷一行。
func (t *Table) Mark(provider, model string, f Flag) bool {
	k := key(provider, model)
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.flags[k]&f == f {
		return false
	}
	t.flags[k] |= f
	return true
}

// RegisterSignature 注册一条判据。撞名（同一个 Label）当场报错，不静默先到先得
// ——与 gateway/special、breaker 的注册口同一个形状。
func (t *Table) RegisterSignature(sig Signature) (modules.Release, error) {
	if sig.Label == "" {
		return nil, fmt.Errorf("quirk: signature label is required")
	}
	if len(sig.Any) == 0 {
		return nil, fmt.Errorf("quirk: signature %s has no patterns", sig.Label)
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, existing := range t.signatures {
		if existing.Label == sig.Label {
			return nil, fmt.Errorf("quirk: signature %s already registered", sig.Label)
		}
	}
	t.signatures = append(t.signatures, sig)
	return func() error {
		t.mu.Lock()
		defer t.mu.Unlock()
		// 按 label 找回来删（下标会随别人的注销变化，索引不可靠）。
		for i, existing := range t.signatures {
			if existing.Label == sig.Label {
				t.signatures = append(t.signatures[:i], t.signatures[i+1:]...)
				return nil
			}
		}
		return nil
	}, nil
}

// Has 查这个 (provider, model) 有没有某个毛病。
func (t *Table) Has(provider, model string, f Flag) bool {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.flags[key(provider, model)]&f != 0
}

// Entry 给 newgate status / doctor 展示。
type Entry struct {
	Provider string
	Model    string
	Flags    Flag
}

func (t *Table) Snapshot() []Entry {
	t.mu.RLock()
	defer t.mu.RUnlock()
	var out []Entry
	for k, f := range t.flags {
		i := strings.LastIndexByte(k, '/')
		if i < 0 {
			continue
		}
		out = append(out, Entry{Provider: k[:i], Model: k[i+1:], Flags: f})
	}
	return out
}

// Reset 清空（测试隔离用；生产代码不该调它——这张表是「一次失败换永久免疫」，
// 跨装配保留是**预期语义**，不是污染）。
func (t *Table) Reset() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.flags = map[string]Flag{}
}

// Signature 一条「报错长这样 → 说明有这个毛病」的规则。
//
// **它由拥有那个补丁的模块注册进来**（`RegisterSignature`），quirk 自己一条都
// 不认识（2026-09-18 改）。这一条与 `gateway/special` 的 st-<上游>.go、以及
// `breaker.RegisterShapeDetector` 是同一条规矩，理由写在 modules/deepseek/shape.go：
//
//	「判据待在知道真相的模块里，它才能被这个模块自己测试、自己演进，而 core 里
//	 不再出现任何上游专有字符串。」
//
// 这里原本硬编码着 GLM / DeepSeek / Kimi 三家的报错原文，于是「加一家新上游」
// 的唯一办法是改 gateway 的文件——而代价已经付过一次：kimi 那句措辞在补进来之前
// 「一条签名都不匹配，quirk 永远学不到它」，每一次后台调用都重新撞一遍 400。
//
// 加新规则的门槛（写给注册方）：必须是**实测复现过**的报错原文，而且补丁得是
// 语义上说得过去的。猜的规则会让我们给一堆无关请求乱加字段，比不修更糟。
type Signature struct {
	// Flag 命中之后要记的毛病位（位定义在 quirk，语义由注册方解释）。
	Flag Flag
	// Any 报错原文里出现任意一条即命中（小写比较）。
	Any []string
	// Label 学到之后写进日志的人话。
	Label string
}

// Learn 从一次失败的转发里学。只看 4xx——5xx 是上游自己挂了，跟请求形状无关。
//
// 返回学到的东西（人话，可以直接写日志）；什么都没学到就返回 nil。
// 认不出的报错一律不猜：宁可让用户看到原始报错，也不能瞎加字段。
func (t *Table) Learn(provider, model string, status int, body []byte) []string {
	if status < 400 || status >= 500 || len(body) == 0 {
		return nil
	}
	low := strings.ToLower(string(body))
	t.mu.RLock()
	sigs := append([]Signature(nil), t.signatures...)
	t.mu.RUnlock()
	var learned []string
	for _, sg := range sigs {
		hit := false
		for _, pat := range sg.Any {
			if strings.Contains(low, strings.ToLower(pat)) {
				hit = true
				break
			}
		}
		if hit && t.Mark(provider, model, sg.Flag) {
			learned = append(learned, sg.Label)
		}
	}
	return learned
}
