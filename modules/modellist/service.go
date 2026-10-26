package modellist

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	webapi "github.com/rzbdz/newgate-ext/modules/web-dashboard"
	i18n "github.com/rzbdz/newgate/lib/i18n"
	"github.com/rzbdz/newgate/modules/config/domain"
	"github.com/rzbdz/newgate/modules/config/store"
)

// 本文件是模块的实现：读 providers.json、挑适配器、缓存、把请求翻译成一次探测。
//
// 缓存为什么在内存里而不是落盘：它回答的是「**这个进程**此刻看到的上游清单」，
// 与 README 里那条「部署出去的机器上没有源码不是故障」同一个态度——进程重启后
// 再问一次是很便宜的，而落盘会引入「文件是谁写的、什么时候过期」这套额外的
// 真相。键里带着 provider 定义的哈希，所以改了 providers.json 不会读到旧清单。

type service struct {
	mu     sync.Mutex
	cache  map[string]cacheEntry
	now    func() time.Time
	client *http.Client

	positiveTTL time.Duration
	negativeTTL time.Duration
}

type cacheEntry struct {
	cat Catalog
	at  time.Time
}

func newService() *service {
	return &service{
		cache: map[string]cacheEntry{},
		now:   time.Now,
		client: &http.Client{
			// 一次下拉不该让页面转十分钟。真实上游列模型通常 <1s；10s 足够跨洋
			// 的慢连接，也足够在对方挂住时把答案还给界面（它会回落到声明清单）。
			Timeout: 10 * time.Second,
		},
		positiveTTL: 10 * time.Minute,
		// 不支持/出错的那一份短很多：用户可能刚把 key 改对，下一次点开就该重问。
		negativeTTL: time.Minute,
	}
}

// List 实现 Service。
func (s *service) List(ctx context.Context, provider string, opts Options) (*Catalog, error) {
	provider = strings.TrimSpace(provider)
	if provider == "" {
		return nil, i18n.E("a model-list request needs a provider name", nil)
	}
	provs, err := store.LoadProviders()
	if err != nil {
		return nil, i18n.Ef(err, "cannot read providers.json: {err}", i18n.A{"err": err})
	}
	p, ok := provs.Providers[provider]
	if !ok {
		return nil, i18n.E("there is no provider named {name} in providers.json", i18n.A{"name": provider})
	}

	key := cacheKey(provider, p)
	if !opts.Refresh {
		if cat, ok := s.cached(key); ok {
			return cat, nil
		}
	}

	cat := s.probe(ctx, provider, p)
	s.put(key, *cat)
	return cat, nil
}

func (s *service) cached(key string) (*Catalog, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.cache[key]
	if !ok {
		return nil, false
	}
	if s.now().Sub(e.at) >= s.ttlFor(e.cat) {
		delete(s.cache, key)
		return nil, false
	}
	cat := e.cat
	cat.Cached = true
	// 切片是共享的；调用方只读，但复制一份边界更清楚（也避免将来有人 append 到
	// 缓存里的那一份）。
	cat.Models = append([]Model(nil), e.cat.Models...)
	cat.Groups = append([]string(nil), e.cat.Groups...)
	return &cat, true
}

func (s *service) put(key string, cat Catalog) {
	s.mu.Lock()
	defer s.mu.Unlock()
	// 顺手清掉过期的：这张表的规模就是 providers 的数量，几十条而已。
	now := s.now()
	for k, e := range s.cache {
		if now.Sub(e.at) >= s.ttlFor(e.cat) {
			delete(s.cache, k)
		}
	}
	s.cache[key] = cacheEntry{cat: cat, at: now}
}

func (s *service) ttlFor(cat Catalog) time.Duration {
	if cat.Supported {
		return s.positiveTTL
	}
	return s.negativeTTL
}

// probe 依次试适配器，把一个「上游的回答」收敛成 Catalog。
//
// 失败（网络/认证/解析）也回一份 Catalog：Supported=false + Note。那条路不是
// HTTP 错误——界面要的是「能不能列、为什么不能」，而不是一个 500。
func (s *service) probe(ctx context.Context, provider string, p domain.Provider) *Catalog {
	cat := &Catalog{
		Provider:  provider,
		Models:    []Model{},
		FetchedAt: s.now().UTC(),
	}
	var unsupportedNotes []string
	for i, a := range attemptsFor(p) {
		res, err := a.list(ctx, s.client, p)
		if err == nil {
			cat.Adapter = a.name
			cat.Endpoint = res.endpoint
			if res.models == nil {
				res.models = []Model{}
			}
			cat.Models = res.models
			cat.Groups = res.groups
			if len(res.models) == 0 {
				// 接口在、但一个模型都没列：对界面来说这与「不支持」是同一件事
				// ——它得回落到 providers.json 声明的那份。保留 adapter/endpoint，
				// 因为那句原因里要有「问过哪儿」。
				cat.Supported = false
				cat.Note = i18n.T("the provider answered with an empty model list", nil)
				return cat
			}
			cat.Supported = true
			return cat
		}
		var un *errUnsupported
		if errors.As(err, &un) {
			unsupportedNotes = append(unsupportedNotes, un.Error())
			continue
		}
		if i > 0 {
			// 备用适配器上的失败（多半是认证方式不对）不是「这个上游坏了」的
			// 证据：真正的主路已经明确说过它没有这个接口。把它记进原因，继续
			// 收尾，而不是把一个 401 当成结论摆到界面上。
			unsupportedNotes = append(unsupportedNotes, err.Error())
			continue
		}
		// 主路真的坏了（DNS、401、解析失败）：不再换适配器——那只会把同一个失败
		// 用另一种认证再试一遍，并把真正的错藏起来。
		cat.Note = err.Error()
		return cat
	}
	cat.Adapter = ""
	if len(unsupportedNotes) > 0 {
		cat.Note = strings.Join(unsupportedNotes, " · ")
	} else {
		cat.Note = i18n.T("this provider does not expose a model list", nil)
	}
	return cat
}

// handle 是 BFF 扩展端点的入口：`POST /ui/api/ext/modellist`。
//
// 请求体只有两个字段：`{"provider":"…","refresh":false}`。形状由本模块定义，
// BFF 原样转交（见 web-dashboard 的 ExtensionService）。
func (s *service) handle(r webapi.ExtensionRequest) (any, error) {
	var req struct {
		Provider string `json:"provider"`
		Refresh  bool   `json:"refresh"`
	}
	if len(strings.TrimSpace(string(r.Body))) == 0 {
		return nil, i18n.E("a model-list request needs {\"provider\": \"…\"}", nil)
	}
	if err := json.Unmarshal(r.Body, &req); err != nil {
		return nil, i18n.Ef(err, "the model-list request is not valid JSON: {err}", i18n.A{"err": err})
	}
	ctx := r.Context
	if ctx == nil {
		ctx = context.Background()
	}
	return s.List(ctx, req.Provider, Options{Refresh: req.Refresh})
}
