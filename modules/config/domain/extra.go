package domain

import (
	"strings"
	"sync/atomic"

	"github.com/rzbdz/newgate/lib/i18n"
)

// ExtraRole 框架之外的**动态角色键**——由模块贡献，core 不认识具体是谁。
//
// 为什么需要这一层：档位（heavy/normal/mid/light/vision）是**能力阶梯**，
// 所有客户端共享；但有些客户端自己有更细的槽位体系——opencode + omo 的
// intra-agent（sisyphus / librarian / …）、将来别的工具的自定义 agent。
// 这些键的数量、名字、默认归属都是**那个模块**的知识，不该 hard-code 进
// core。
//
// 所以分工是：
//
//	模块（如 omo 接管）实现 roleprov.Provider，说自己有哪些键、各自缺省
//	  ref 到哪个键；
//	core 只维护一张「键 → 缺省 ref」的表，解析时与内置别名一视同仁。
//
// 键名进 profile 的写法与档位完全一样（`omo-sisyphus = @normal`），
// 因为解析路径是同一条——见 CandidatesFor / resolve.BuildChain。
type ExtraRole struct {
	// Key 键名。命名冲突由贡献方负责（写进 profile 的也是这个名字）。
	Key string `json:"key"`
	// Source 谁贡献的（模块短名，如 "omo" / "builtin"）。只给人看。
	Source string `json:"source"`
	// Default 这个键在 profile 里**没写**时，等价于哪个键（通常是某个档位）。
	// 空 = 没有缺省，没写就真的没有候选（稀疏）。
	Default string `json:"default,omitempty"`
	// Meta 贡献方自己的说明，core 不解释（如 was / variant / suggested）。
	Meta map[string]string `json:"meta,omitempty"`
}

// builtinAliases 内置的角色等价关系。机制与模块贡献的 ExtraRole 完全一样，
// 只是来源写在代码里——这样「四档化的向下兼容」不需要第二条代码路径。
//
// 它的 Meta（给人看的那句「为什么」）不写在这里，由 builtinAliasMeta 现挂：
// 这是**包级变量**，初始化时求值——那会儿 modules/locale 还没把语言装上，
// 写进来会永远停在源语言。
var builtinAliases = []ExtraRole{{
	Key: "normal", Source: "builtin", Default: "mid",
}}

// builtinAliasMeta 内置别名要挂在 Meta 上的说明（没有就返回 nil）。
//
// 只有给人看的那个出口（ExtraRoles）需要它：ExtraRoleOf 在请求热路径上
// （每个带动态角色键的请求都要过一次），那里连一次 map 分配都不该多。
func builtinAliasMeta(key string) map[string]string {
	if key != "normal" {
		return nil
	}
	return map[string]string{"why": i18n.T(
		"four-tier compatibility: a profile that does not write normal behaves like mid", nil)}
}

// extraRoles 模块贡献的动态角色键，**原子换页**。
//
// 它同时被两个协程摸，所以不能是一个裸的包级切片：
//
//	写：daemon 的配置 watcher 协程，每秒一次（store.Reload → store.Load →
//	    roleprov.Refresh → SetExtraRoles）；config/diagnostics.go 也会调。
//	读：请求协程（resolve.BuildChain → CandidatesFor → DefaultBindingFor →
//	    ExtraRoleOf），凡是请求里带了动态角色键的每一发都走这条读路径。
//
// 2026-09-18 之前它就是一句裸赋值，`-race` 实测 12 次命中。而且后果不只是
// 「读到旧值」：slice header 是三个机器字（指针 + len + cap），并发读写会读到
// **撕裂的头**——新指针配旧长度，`range` 于是越界遍历，轻则把垃圾当成已知角色键
// 解析出错误绑定，重则 panic 在请求协程里。这个仓库整个转发路径是 fail-open 的，
// 请求协程里 panic 恰好是最坏的那种失败。
//
// 为什么是 atomic.Pointer 而不是 mutex：读者在转发热路径上，写者每秒才一次。
// 这与 store.Watcher 处理整个配置快照的方式同构（`cur atomic.Value` + 原子换页）
// ——写少读多、读侧零锁零拷贝。
var extraRoles atomic.Pointer[[]ExtraRole]

// SetExtraRoles 灌入模块贡献的动态角色键。启动时与每次配置重载时调（见
// store.Load）；传 nil 回到「只有内置别名」。
//
// 拷一份再存：调用方（roleprov.Refresh）的切片可能被它自己复用，而读者拿到
// 的那份必须永不改写——换页之后谁都不许再碰旧切片，这是无锁读的前提。
func SetExtraRoles(rs []ExtraRole) {
	snapshot := make([]ExtraRole, len(rs))
	copy(snapshot, rs)
	extraRoles.Store(&snapshot)
}

// contributedRoles 当前模块贡献的键（没有就是空）。热路径读，无锁、无拷贝
// ——返回值是只读的，谁都不许改它（见 SetExtraRoles）。
func contributedRoles() []ExtraRole {
	if p := extraRoles.Load(); p != nil {
		return *p
	}
	return nil
}

// ExtraRoles 当前全部动态角色键（内置别名在前，模块的按 key 排序在后）。
func ExtraRoles() []ExtraRole {
	mods := contributedRoles()
	out := make([]ExtraRole, 0, len(builtinAliases)+len(mods))
	for _, r := range builtinAliases {
		// 说明现挂（见 builtinAliasMeta）：拷一份结构体再挂，绝不写进那个
		// 共享的包级变量——它是只读的。
		r.Meta = builtinAliasMeta(r.Key)
		out = append(out, r)
	}
	// 模块那份已经在 Refresh 里排过序了，这里不再排——但它是共享的只读切片，
	// 所以**必须**拷出来，不能直接 append 给调用方。
	out = append(out, mods...)
	return out
}

// ExtraRoleOf 这个键是不是动态角色键。
func ExtraRoleOf(key string) (ExtraRole, bool) {
	for _, r := range builtinAliases {
		if r.Key == key {
			return r, true
		}
	}
	for _, r := range contributedRoles() {
		if r.Key == key {
			return r, true
		}
	}
	return ExtraRole{}, false
}

// AliasFor 这个键在 profile 里没写时等价于哪个**键**（"" = 不是键到键的等价）。
// 只是给展示/校验用；解析走 DefaultBindingFor。
func AliasFor(key string) string {
	r, ok := ExtraRoleOf(key)
	if !ok {
		return ""
	}
	d := strings.TrimSpace(r.Default)
	d = strings.TrimPrefix(d, "@")
	if d == "" || strings.Contains(d, "/") {
		return "" // 具体绑定（provider/模型）不是键的等价
	}
	return d
}

// DefaultBindingFor 这个键在 profile 里没写时，等价于哪条绑定。
//
// Default 支持三种写法，空 = 没有缺省（稀疏，整层跳过）：
//
//	"@别的键"        引用：就地展开那条链。最常用，也是模块贡献键的默认形态
//	                 （omo-sisyphus = @normal：跟进主力档，档位一动它跟着动）
//	"provider/模型"  具体绑定：钉到某条真实模型（不会随档位漂移）
//	"别的键"         等价于那个键——内置别名 normal→mid 就是这么写的一条记录
//
// 三种都归一成 Binding 再返回，于是**解析路径只有一条**：引用本来就是候选
// 的第一等公民（chainBuilder.expand 展开它、去重、环检测），别名不必再养
// 一套「去别的键里翻候选人」的第二路径。
func DefaultBindingFor(key string) (Binding, bool) {
	r, ok := ExtraRoleOf(key)
	if !ok {
		return Binding{}, false
	}
	d := strings.TrimSpace(r.Default)
	if strings.HasPrefix(d, "@") {
		d = strings.TrimSpace(d[1:])
		if d == "" {
			return Binding{}, false
		}
		return Binding{Ref: d}, true
	}
	if d == "" {
		return Binding{}, false
	}
	if strings.Contains(d, "/") {
		if bd, err := ParseBindingString(d); err == nil {
			return bd, true
		}
		return Binding{}, false
	}
	return Binding{Ref: d}, true
}

// IsKnownRole 客户端发来的 model 字段是不是一个**角色键**（档位或动态角色）。
// 不是的话就按「具体模型名」反查（docs/04-configuration.md）。
func IsKnownRole(s string) bool {
	if IsRole(s) {
		return true
	}
	_, ok := ExtraRoleOf(s)
	return ok
}

// TierLadder 阶梯档由弱到强。vision 是**正交**档（看得见图，不代表更聪明），
// 不在阶梯上，也就不能靠「升一级」从别的档走过来。
var TierLadder = []string{"light", "mid", "normal", "heavy"}

// ShiftTier 在阶梯上挪 n 级（越界夹住）。不是阶梯档（vision / 未知）原样返回。
// 用途：omo 槽位的 `variant`（max/high/low）表达的是「这条槽位调多猛」，
// 算建议档位时把它折进体格（docs/04-configuration.md）。
func ShiftTier(tier string, n int) string {
	if n == 0 {
		return tier
	}
	i := -1
	for j, t := range TierLadder {
		if t == tier {
			i = j
			break
		}
	}
	if i < 0 {
		return tier
	}
	i += n
	if i < 0 {
		i = 0
	}
	if i >= len(TierLadder) {
		i = len(TierLadder) - 1
	}
	return TierLadder[i]
}
