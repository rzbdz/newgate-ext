package verify

import (
	"context"
	"sort"
	"strings"
	"time"

	i18n "github.com/rzbdz/newgate/lib/i18n"
	"github.com/rzbdz/newgate/modules/config/store"
	"github.com/rzbdz/newgate/modules/config/targets"
)

// service 是本模块的实现：把配置、指纹库与缓存缝在一起。
//
// 它同时服务三条入口——CLI、web 扩展端点、概念账本。三条入口都只认这里的方法，
// 所以「一条 binding 怎么验」只有一份实现（与 modellist 的 Service 同一条）。
type service struct {
	det   detector
	cache *cacheStore
}

func newService() *service {
	c := newCacheStore()
	return &service{det: newFingerprintDetector(c), cache: c}
}

// State 产出这一屏的全部素材。它**不返回 error**：配置读不出来时也把页面画
// 出来（自由验证那半不依赖任何 profile），原因写在 State.Error 里。这是
// 「不静默」与「一屏不能因为另一屏坏了而白屏」的折中。
func (s *service) State() State {
	st := State{}
	var problems []string

	if rows, _, err := targets.Enumerate(targets.Options{}); err != nil {
		problems = append(problems, err.Error())
	} else {
		st.Profiles = groupProfiles(rows)
	}

	if provs, err := store.LoadProviders(); err != nil {
		problems = append(problems, i18n.Ef(err, "cannot read providers.json: {err}",
			i18n.A{"err": err}).Error())
	} else {
		names := make([]string, 0, len(provs.Providers))
		for name := range provs.Providers {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			p := provs.Providers[name]
			protocol := p.Protocol
			if protocol == "" {
				protocol = "openai"
			}
			st.Providers = append(st.Providers, ProviderState{
				Name: name, Protocol: protocol, BaseURL: p.BaseURL,
				HasKey: p.Key() != "", Models: append([]string(nil), p.Models...),
			})
		}
	}

	if entries, err := s.cache.list(); err != nil {
		problems = append(problems, err.Error())
	} else {
		st.Entries = entries
	}

	st.Build, st.Loaded = s.det.Loaded()
	st.DataCached = s.cache.dataReady()
	if at, ok := s.cache.dataCheckedAt(); ok {
		st.DataCheckedAt = &at
	}
	st.Error = strings.Join(problems, " · ")
	return st
}

// groupProfiles 把 targets 的扁平行折成「一份 profile 一组」。
//
// 输入已经按（profile, 档位顺序）排好；这里保持那个顺序，不再排序——档位顺序
// 是能力阶梯，不是字母序。
//
// 跳过「这一档没绑」的行：稀疏 profile 只写自己关心的档位是**正常形态**，而这一
// 屏回答的是「已绑定的那些模型实际是谁」。把五行 not bound 摊在每份 profile 下面
// 只会让真正要看的 binding 沉下去。（provider 没声明的那种错**保留**：它说明
// 配置坏了，需要人看见。）
func groupProfiles(rows []targets.Row) []ProfileState {
	var out []ProfileState
	index := map[string]int{}
	for _, r := range rows {
		if r.Provider == "" && r.Model == "" {
			continue
		}
		i, ok := index[r.Profile]
		if !ok {
			i = len(out)
			index[r.Profile] = i
			out = append(out, ProfileState{Name: r.Profile})
		}
		out[i].Roles = append(out[i].Roles, RoleState{
			Role: r.Role, Provider: r.Provider, Model: r.Model, Err: r.Err,
		})
	}
	return out
}

// Run 验一条 binding，返回结论并落盘。
//
// 返回 error 只表示**这次请求根本没打成**（没这个 provider、没有 key、参考库
// 加载不了）。上游把 300 个整数吐回来但样本不合格，是**一条结论**（Entry 里
// 的 Error），不是 handler 的失败——界面要把它显示在那一行上，而不是弹一个
// 全局错误。
func (s *service) Run(ctx context.Context, req RunRequest) (*Entry, error) {
	provider := strings.TrimSpace(req.Provider)
	model := strings.TrimSpace(req.Model)
	if provider == "" || model == "" {
		return nil, i18n.E("verify needs both a provider and a model", nil)
	}
	api, err := resolveAPI(req.API)
	if err != nil {
		return nil, err
	}
	count := req.Count
	if count == 0 {
		count = 3
	}
	if count < 1 || count > 3 {
		return nil, i18n.E("count must be 1, 2 or 3 (it is the number of challenges per round)", nil)
	}

	provs, err := store.LoadProviders()
	if err != nil {
		return nil, i18n.Ef(err, "cannot read providers.json: {err}", i18n.A{"err": err})
	}
	p, ok := provs.Providers[provider]
	if !ok {
		return nil, i18n.E("there is no provider named {name} in providers.json",
			i18n.A{"name": provider})
	}

	res, build, apiName, err := s.det.Detect(ctx, p, model, api, count)
	if err != nil {
		return nil, err
	}

	e := &Entry{
		Provider:  provider,
		Model:     model,
		API:       apiName,
		CheckedAt: time.Now().UTC(),
		Build:     build,
		Match:     MatchUnknown,
	}
	if res != nil {
		if res.Error != nil {
			e.Error = strings.TrimSpace(*res.Error)
		}
		if a := res.Analysis; a != nil {
			e.Decision = a.Decision
			e.Predicted = a.Prediction
			e.PredictedName = a.PredictionName
			e.Family = a.FamilyPrediction
			e.FamilyName = a.FamilyPredictionName
			e.Probability = a.Probability
			e.UsedOutputs = a.UsedOutputs
			e.Match = Classify(model, a.Prediction, a.FamilyPrediction)
			// 参考库通常给几十个候选；界面只需要前几名，报文不必背整张表。
			for i, c := range a.Results {
				if i >= 5 {
					break
				}
				e.Results = append(e.Results, Candidate{
					Model: c.Model, DisplayName: c.DisplayName,
					Family: c.Family, FamilyName: c.FamilyName,
					Score: c.Score, Probability: c.Probability,
				})
			}
		}
	}
	if e.Error == "" && e.Predicted == "" {
		e.Error = i18n.T("the fingerprint round produced no ranking", nil)
	}
	// 落盘失败不影响这次结论，但决定下次界面还看不看得到它。
	if err := s.cache.put(*e); err != nil {
		e.CacheError = err.Error()
	}
	return e, nil
}

func resolveAPI(api string) (string, error) {
	switch strings.TrimSpace(api) {
	case "", "auto":
		return "auto", nil
	case "responses", "chat_completions", "messages":
		return strings.TrimSpace(api), nil
	default:
		return "", i18n.E("unknown fingerprint api {api} (use auto, responses, chat_completions or messages)",
			i18n.A{"api": api})
	}
}

// Upgrade 是挂到 `newgate upgrade` 上的动作：强制刷新参考库。
//
// 它只碰**动态数据**（缓存里的参考库），不碰二进制——第一版 upgrade 的边界见
// modules/upgrade 的包注释。
func (s *service) Upgrade(ctx context.Context) (string, error) {
	return s.det.Refresh(ctx)
}

// cacheDoc 是「只看缓存」那条查询的回答（模型下拉用）。
type cacheDoc struct {
	Entries []Entry `json:"entries"`
}

func (s *service) cached(provider, model string) (*cacheDoc, error) {
	all, err := s.cache.list()
	if err != nil {
		return nil, err
	}
	out := &cacheDoc{Entries: []Entry{}}
	for _, e := range all {
		if provider != "" && e.Provider != provider {
			continue
		}
		if model != "" && e.Model != model {
			continue
		}
		out.Entries = append(out.Entries, e)
	}
	return out, nil
}
