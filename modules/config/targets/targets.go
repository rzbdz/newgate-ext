// Package targets 把 profile 配置解析成「这一轮要打的 (provider, model)」。
//
// # 为什么单独一个包
//
// 两条诊断走的是同一件事：`newgate probe`（连通性/方言）与 `newgate verify`
// （指纹）。它们的**结论**完全不同，但「哪些 profile、哪些档位、去重之后是哪几个
// 上游」这条知识只有一份。2026-10 之前它内联在 gateway/probe 里；发行版的 verify
// 需要同一份名单，而内核的 probe 实现包不该被发行版 import（那是内部实现）。
// 下沉到这里之后，两边共用这一份，profile 的解析规则也只有一处会漂移。
//
// 它只依赖 config 的共享叶子（domain/store），只做读盘与解析，也不认识
// probe/verify 任何一方——所以箭头是 `probe → targets` 与 `verify → targets`。
package targets

import (
	"sort"
	"strconv"
	"strings"

	"github.com/rzbdz/newgate-ext/modules/config/domain"
	"github.com/rzbdz/newgate-ext/modules/config/store"
	i18n "github.com/rzbdz/newgate/lib/i18n"
)

// Target 是去重后的最小身份：profile/role 只是**引用**它的配置视图。
type Target struct {
	Provider string
	Model    string
}

// String 返回稳定的 provider/model 标识，用于排序与诊断。
func (t Target) String() string { return t.Provider + "/" + t.Model }

// Row 是一条配置视图：某个 profile 的某个档位解析到了哪一条绑定。
//
// 同一条 Target 可以出现在很多 Row 里（一份 binding 被好几个 profile 的档位
// 共用），调用方按 Target 去重之后再打请求。
type Row struct {
	Profile  string
	Role     string
	Provider string
	Model    string
	// Err 非空 = 这一档没有可打的目标（没绑、或 provider 没声明）。它仍然是一行：
	// 用户要知道「这一档是空的」，而不是从表里消失。
	Err string
}

// Options 控制解析哪一段配置。
type Options struct {
	// Only 是一个 profile 名；空 = 全部 profile。
	Only string
	// OnlyTarget 点名一条 binding，空 = 按 Only/全部 profile 走。
	//
	// 与 Only 的区别是「按什么找」：Only 是一个 profile 名（那下面是整条链的
	// 几个候选），而这个是某一条具体的 binding。设了它就**不看 profile**：
	// 目标已经点名了，再去遍历配置只会让「点一下这条」顺带打一串别的。
	OnlyTarget *Target
}

// Enumerate 解析配置，返回配置视图（Rows）与去重后的目标（Targets）。
//
// Targets 按 String 排序；Rows 按（profile, 档位顺序）排序。profile 文件坏掉时
// **跳过它**（与 store.Load 同一条：一个手改坏的文件不该让全部诊断停摆）；
// 哪个文件坏了由 doctor 报，这里只报「点名的 profile 不存在」——那是整轮无法
// 进行的错误，沉默会让调用方以为配置是空的。
func Enumerate(o Options) (rows []Row, uniq []Target, err error) {
	provs, err := store.LoadProviders()
	if err != nil {
		return nil, nil, err
	}
	names, err := store.ListProfiles()
	if err != nil {
		return nil, nil, err
	}
	if o.Only != "" {
		found := false
		for _, n := range names {
			if n == o.Only {
				found = true
			}
		}
		if !found {
			return nil, nil, i18n.E("profile {profile} does not exist (available: {names})", i18n.A{
				"profile": strconv.Quote(o.Only),
				"names":   strings.Join(names, ", "),
			})
		}
		names = []string{o.Only}
	}

	seen := map[Target]bool{}
	add := func(t Target) {
		if !seen[t] {
			seen[t] = true
			uniq = append(uniq, t)
		}
	}

	if o.OnlyTarget != nil {
		// 点名了一条：不遍历 profile（理由见 Options.OnlyTarget）。
		add(*o.OnlyTarget)
		sort.Slice(uniq, func(i, j int) bool { return uniq[i].String() < uniq[j].String() })
		return nil, uniq, nil
	}

	for _, n := range names {
		pr, err := store.LoadProfile(n)
		if err != nil {
			continue
		}
		for _, role := range domain.Roles {
			b, ok := pr.Resolve(role)
			if !ok {
				rows = append(rows, Row{Profile: n, Role: role, Err: i18n.T("not bound", nil)})
				continue
			}
			r := Row{Profile: n, Role: role, Provider: b.Provider, Model: b.Model}
			if _, ok := provs.Providers[b.Provider]; !ok {
				r.Err = i18n.T("provider undefined", nil)
				rows = append(rows, r)
				continue
			}
			add(Target{Provider: b.Provider, Model: b.Model})
			rows = append(rows, r)
		}
	}
	sort.Slice(uniq, func(i, j int) bool { return uniq[i].String() < uniq[j].String() })
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].Profile != rows[j].Profile {
			return rows[i].Profile < rows[j].Profile
		}
		return roleIdx(rows[i].Role) < roleIdx(rows[j].Role)
	})
	return rows, uniq, nil
}

func roleIdx(r string) int {
	for i, x := range domain.Roles {
		if x == r {
			return i
		}
	}
	return 99
}
