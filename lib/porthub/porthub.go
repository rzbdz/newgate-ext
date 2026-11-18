// Package porthub 是「一个端口上挂多个服务」的机制：一张 **前缀 → http.Handler**
// 的表，谁想在这个端口上再开一个 service，就往表里挂一条。
//
// 为什么做成机制：一个 gateway 只监听一个端口，这是部署上的硬约束（防火墙、反代、
// Tailscale ACL 都按端口配）。web 界面、将来别的服务都想住在这个端口上，而「往同一
// 张表里挂东西」这件事跟卖什么产品无关。
//
// 它做四件事：记住谁挂了什么、按**最长前缀**查、拒绝抢保留前缀、**交出这个端口的
// 根 handler**（`Root`：挂载表优先，没人认领的落回兜底服务——兜底就是数据面）。
//
// **它不监听**：socket 属于守护进程（fd 优雅交接、排空、pidfile/lock 都是进程级
// 的事，porthub 关掉时它们得照旧工作）。所以这里的挂载是进程内的注册：不产生
// socket、不产生 goroutine，因此在每条 `newgate …` 命令里被调用也是安全的
// （模块的 Start 每条命令都会跑，这条约束见 modules/i18n 的注释）。
//
// 方向（2026-09-20 定）：**数据面不认识这张表**。守护进程入口把数据面的 handler
// 交给 `Root`，再把拿到的根 handler 交给 http.Server——所以「谁应答」是这张表
// 说了算，而 gateway/forward 里连 porthub 这个名字都不出现。
//
// 与 pluginmanager 的开关点不同：那是「要不要这个功能」，这里是「这个 service 挂在
// 哪个路径上」。关掉 porthub 模块的后果是**消费者拿不到这张表**（capability 不在），
// 于是它们按 fallback 自己监听一个端口——语义干净，不需要在分派处写特判。
package porthub

import (
	"net/http"
	"sort"
	"strings"
	"sync"

	i18n "github.com/rzbdz/newgate/lib/i18n"
)

// Mount 是一条已挂载的记录，给诊断与 status 用。
type Mount struct {
	Prefix string
	Owner  string // 谁挂的（模块名）；冲突时报错要点名
}

// Registry 是挂载表。零值不可用，用 NewRegistry 或 Default。
type Registry struct {
	mu     sync.RWMutex
	mounts map[string]mount
	// fallback 是「没人认领的请求」的归宿：在这个进程里就是数据面。它不是一条
	// 挂在 "/" 上的记录——"/" 是所有前缀的公共前缀，写成记录会让「谁挂了 /」和
	// 「谁是兜底」变成两个可以同时存在、且互相不知道对方的东西。
	fallback http.Handler
	fbOwner  string
}

type mount struct {
	handler http.Handler
	owner   string
}

// reserved 是不能被抢的前缀。
//
// 理由不是「不好看」：`/a` 与 `/v1` 是数据面（转发路径），`/__newgate` 是控制面
// 命名空间。被抢的症状是**本该发给本机控制面的请求被当数据面转发给上游**——
// forward.go 里那句「绝不盲发 /__newgate/upgrade（那会被 catch-all 转发到上游）」
// 记的就是这个坑。所以这里当场报错，而不是让后来者悄悄覆盖。
var reserved = []string{"/v1", "/a", "/__newgate"}

// NewRegistry 造一张空表。
func NewRegistry() *Registry {
	return &Registry{mounts: map[string]mount{}}
}

var (
	defaultOnce sync.Once
	defaultReg  *Registry
)

// Default 是进程级的那张表。
//
// 为什么这里允许一个全局：它和 metrics.Default 是同一类东西——除去「谁挂了什么」
// 没有别的状态，而这件事在一个进程里天然只有一份（那个 http.Server 也只有一份）。
// 有生命周期的东西仍然走 capability（modules/porthub），全局只解决「分派处怎么
// 拿到表」这一个问题。
func Default() *Registry {
	defaultOnce.Do(func() { defaultReg = NewRegistry() })
	return defaultReg
}

// Mount 把 h 挂到 prefix 下。owner 是挂载者的名字，只用于报错与诊断。
//
// 返回的 Release 撤销这次挂载，幂等；Stop 里必须调用（没有 Release 就没有生命
// 周期，见 modules/gateway/module.go 里那段反例分析）。
func (r *Registry) Mount(prefix, owner string, h http.Handler) (func(), error) {
	p, err := normalize(prefix)
	if err != nil {
		return nil, err
	}
	if h == nil {
		return nil, i18n.E("porthub: the handler {owner} wants to mount at {prefix} is nil",
			i18n.A{"owner": owner, "prefix": p})
	}
	for _, res := range reserved {
		if p == res || strings.HasPrefix(p, res+"/") || strings.HasPrefix(res, p+"/") {
			return nil, i18n.E("porthub: prefix {prefix} (mounted by {owner}) collides with the "+
				"reserved prefix {reserved}, which belongs to the data plane / control plane "+
				"namespace — claiming it would send requests meant for this machine upstream "+
				"as if they were data-plane traffic", i18n.A{"prefix": p, "owner": owner, "reserved": res})
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if prev, dup := r.mounts[p]; dup {
		return nil, i18n.E("porthub: prefix {prefix} is already mounted by {owner} — two services "+
			"on one path means only one of them ever sees a request, so this errors instead of "+
			"letting the later one win silently", i18n.A{"prefix": p, "owner": prev.owner})
	}
	r.mounts[p] = mount{handler: h, owner: owner}
	return func() {
		r.mu.Lock()
		defer r.mu.Unlock()
		if m, ok := r.mounts[p]; ok && m.owner == owner {
			delete(r.mounts, p)
		}
	}, nil
}

// Lookup 按最长前缀查。第二个返回值为 false 表示这张表不管这个路径，
// 调用方该走自己的路（gateway 那边就是落回转发）。
func (r *Registry) Lookup(path string) (http.Handler, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	best := ""
	var found mount
	for p, m := range r.mounts {
		if !matches(p, path) {
			continue
		}
		if len(p) > len(best) {
			best, found = p, m
		}
	}
	if best == "" {
		return nil, false
	}
	return found.handler, true
}

// Root 把 fallback 登记为这个端口的兜底服务，并交出**该端口的根 handler**。
//
// 这就是「端口归谁」这件事的落点：调用方（守护进程入口）不再自己决定谁应答，
// 它把数据面交进来，拿到一个 handler 交给 http.Server——于是
//   - 挂载表里的服务（web 界面这类）优先；
//   - 没人认领的落回数据面；
//   - 数据面**不认识这张表**（它只是被交进来的一个 handler），
//     这张表也不认识数据面（它只知道自己有个兜底）。
//
// 后一条是这次改动的全部理由：让数据面 import 一个「web 服务怎么共享端口」的
// 机制，等于把部署形态写进热路径。
func (r *Registry) Root(owner string, fallback http.Handler) (http.Handler, error) {
	if fallback == nil {
		return nil, i18n.E("porthub: {owner} wants to serve the port with a nil fallback handler",
			i18n.A{"owner": owner})
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.fallback, r.fbOwner = fallback, owner
	return http.HandlerFunc(r.serve), nil
}

// serve 是根 handler：先问挂载表，问不到交给兜底。
//
// **每请求现查**，不把表抄进 mux：模块的 Start 顺序由依赖图决定，不保证挂东西的
// 模块排在服务端前面；抄一份还会让 Stop 之后的撤销失效（那时 handler 早就建好
// 了）。读写由 RWMutex 保护——这就是「CLI 与 dashboard 同时跑」的 race 保护点。
func (r *Registry) serve(w http.ResponseWriter, req *http.Request) {
	if h, ok := r.Lookup(req.URL.Path); ok {
		h.ServeHTTP(w, req)
		return
	}
	r.mu.RLock()
	fb := r.fallback
	r.mu.RUnlock()
	// 这里不判 nil：Root 拒绝 nil 兜底，所以这个 handler 一旦在外面，fb 就必然
	// 非 nil。写成「以防万一的 404」是本仓库最不想要的那种代码——读的人会以为
	// 那条路真的会发生，于是照着它推理。
	fb.ServeHTTP(w, req)
}

// FallbackOwner 是谁在兜底（诊断用）。
func (r *Registry) FallbackOwner() string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.fbOwner
}

// Mounts 返回快照，按前缀排序（诊断、`newgate status` 用）。
//
// 兜底服务在这里表现为一条前缀 `/` 的记录——这是实话：它接住的就是「所有没被
// 更长的前缀认领的路径」。诊断输出因此能完整回答「这个端口上都在跑什么」。
func (r *Registry) Mounts() []Mount {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Mount, 0, len(r.mounts)+1)
	if r.fallback != nil {
		out = append(out, Mount{Prefix: "/", Owner: r.fbOwner})
	}
	for p, m := range r.mounts {
		out = append(out, Mount{Prefix: p, Owner: m.owner})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Prefix < out[j].Prefix })
	return out
}

// matches 是前缀匹配的判据：`/ui` 命中 `/ui` 与 `/ui/...`，但**不**命中 `/uix`
// ——按字节前缀匹配会在这里悄悄多认一个 service。
func matches(prefix, path string) bool {
	return path == prefix || strings.HasPrefix(path, prefix+"/")
}

// normalize 收敛写法：必须以 / 开头、不能是 /（那是 catch-all）、结尾的 / 去掉。
func normalize(prefix string) (string, error) {
	if prefix == "" || prefix[0] != '/' {
		return "", i18n.E("porthub: a prefix must start with /, got {prefix}",
			i18n.A{"prefix": prefix})
	}
	p := strings.TrimSuffix(prefix, "/")
	if p == "" {
		return "", i18n.E("porthub: the prefix cannot be / — that is the whole port. Give a path "+
			"prefix instead, e.g. /ui", nil)
	}
	return p, nil
}
