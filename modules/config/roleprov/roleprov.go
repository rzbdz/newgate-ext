// Package roleprov 汇总**模块贡献的动态角色键**。
//
// 为什么要有这一层：档位（heavy/normal/mid/light/vision）是框架的能力阶梯，
// 每个客户端共享；但客户端插件常常自带更细的槽位体系——opencode 的
// oh-my-openagent 有 sisyphus / librarian / … 十几个 intra-agent，将来别的
// 插件还会有别的。这些键**叫什么、有几个、缺省跟谁走**是那个模块的知识，
// 不该 hard-code 进 core（docs/04-configuration.md）。
//
// 分工：
//
//	模块实现 RoleProvider，经 Config 组件的 capability 注册；
//	框架（store 每次装快照、CLI 启动）调 Refresh，把结果灌进 domain。
//
// core 只认「键 → 缺省绑定」这张表，解析时与内置别名一视同仁。
package roleprov

import (
	"fmt"
	"sort"
	"sync"

	"github.com/rzbdz/newgate-ext/modules/config/domain"
	modules "github.com/rzbdz/newgate/component"
	"github.com/rzbdz/newgate/lib/i18n"
)

// RoleProvider 允许模块向配置层贡献动态档位，同时保留来源供冲突诊断。
//
// 契约定义在这里而不是 config 根：角色注册表的实现就是本包，定义放这里
// config 根才能 import 本包而不成环（见 docs/03-architecture.md 的分层）。
type RoleProvider interface {
	Source() string
	Roles() ([]domain.ExtraRole, error)
}

// RoleWatchProvider 是 RoleProvider 的可选能力：声明哪些文件变化后需要重载。
type RoleWatchProvider interface {
	WatchFiles() []string
}

// Registry 聚合动态档位提供者，并用 token 维护每次注册的精确所有权。
type Registry struct {
	mu        sync.RWMutex
	providers []RoleProvider
	tokens    map[string]uint64
	next      uint64
}

var (
	defaultMu       sync.RWMutex
	defaultRegistry = &Registry{}
)

// NewRegistry 创建隔离注册表，供每个 Config 组件实例独立拥有。
func NewRegistry() *Registry { return &Registry{} }

// SetDefault 设置旧数据面使用的默认注册表；新装配应使用 InstallDefault。
func SetDefault(registry *Registry) {
	if registry == nil {
		panic("roleprov: nil default registry")
	}
	defaultMu.Lock()
	defaultRegistry = registry
	defaultMu.Unlock()
}

// InstallDefault 暂时接管兼容全局，并返回不会覆盖新 owner 的恢复函数。
func InstallDefault(registry *Registry) func() {
	if registry == nil {
		panic("roleprov: nil default registry")
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

// Register 是旧数据面的兼容入口；生产装配通过 Config capability 注册。
func Register(p RoleProvider) {
	if _, err := currentRegistry().Register(p); err != nil {
		panic(err)
	}
}

// Register 加入一个来源唯一的动态档位提供者，并返回所有权 Release。
func (r *Registry) Register(p RoleProvider) (modules.Release, error) {
	if p == nil || p.Source() == "" {
		return nil, fmt.Errorf("roleprov: provider source is required")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, q := range r.providers {
		if q.Source() == p.Source() {
			return nil, fmt.Errorf("roleprov: duplicate provider %s", p.Source())
		}
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
	r.tokens[p.Source()] = token
	r.providers = append(r.providers, p)
	return func() error {
		r.mu.Lock()
		defer r.mu.Unlock()
		if r.tokens[p.Source()] != token {
			return nil
		}
		delete(r.tokens, p.Source())
		for i, provider := range r.providers {
			if provider.Source() == p.Source() {
				r.providers = append(r.providers[:i], r.providers[i+1:]...)
				break
			}
		}
		return nil
	}, nil
}

// Refresh 重新问一遍每个已登记的 provider，把结果灌进 domain。
//
// 调用点：store.Load（每次装配置快照）——角色键是配置的一部分，改了
// omo-slots.json 应当和改 providers.json 一样自动生效，不需要重启。
//
// 单个 provider 读文件失败不影响别的：记一条错误继续（失败开放），
// 那个模块的键退化成「没注册」，用户看到的是一句可查的报错而不是全线停摆。
func Refresh() []error {
	registry := currentRegistry()
	registry.mu.RLock()
	ps := append([]RoleProvider(nil), registry.providers...)
	registry.mu.RUnlock()

	var (
		out  []domain.ExtraRole
		errs []error
		seen = map[string]string{} // key -> 谁登记的
	)
	for _, p := range ps {
		roles, err := p.Roles()
		if err != nil {
			errs = append(errs, i18n.Ef(err, "cannot read the role keys of module {module}: {err}",
				i18n.A{"module": p.Source()}))
			continue
		}
		for _, r := range roles {
			if r.Key == "" {
				continue
			}
			// 阶梯档位是框架的，模块不许覆盖——那会让「heavy 是什么意思」
			// 取决于装了哪个插件，排查时无从下手。
			if domain.IsRole(r.Key) {
				errs = append(errs, i18n.E("module {module} tried to register {key}, which is a built-in role name; ignored",
					i18n.A{"module": p.Source(), "key": r.Key}))
				continue
			}
			if who, dup := seen[r.Key]; dup {
				errs = append(errs, i18n.E("key {key} is registered by both {first} and {second}; keeping the first",
					i18n.A{"key": r.Key, "first": who, "second": p.Source()}))
				continue
			}
			seen[r.Key] = p.Source()
			if r.Source == "" {
				r.Source = p.Source()
			}
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	domain.SetExtraRoles(out)
	return errs
}

// Sources 已登记的模块短名（doctor / `newgate omo` 用）。
func Sources() []string {
	registry := currentRegistry()
	registry.mu.RLock()
	defer registry.mu.RUnlock()
	out := make([]string, 0, len(registry.providers))
	for _, p := range registry.providers {
		out = append(out, p.Source())
	}
	sort.Strings(out)
	return out
}

// WatchFiles 汇总所有支持热更新的 provider 文件，并稳定排序。
func WatchFiles() []string {
	registry := currentRegistry()
	registry.mu.RLock()
	defer registry.mu.RUnlock()
	var files []string
	for _, provider := range registry.providers {
		if watcher, ok := provider.(RoleWatchProvider); ok {
			files = append(files, watcher.WatchFiles()...)
		}
	}
	sort.Strings(files)
	return files
}
