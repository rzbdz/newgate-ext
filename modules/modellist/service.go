package modellist

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/rzbdz/newgate-ext/modules/config/domain"
	"github.com/rzbdz/newgate-ext/modules/config/store"
	webapi "github.com/rzbdz/newgate-ext/modules/web-dashboard"
	i18n "github.com/rzbdz/newgate/lib/i18n"
)

// This file is the module's implementation: read providers.json, pick an adapter, cache, turn a
// request into one probe.
//
// Why the cache is in memory rather than on disk: it answers "the upstream listing **this process**
// sees right now", the same attitude as the README's "not having the source on a deployed machine
// is not a failure" -- asking again after a restart is cheap, while persisting would introduce the
// extra truth of "who wrote the file, when does it expire". The key carries the provider
// definition's hash, so changing providers.json will not read a stale listing.

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
			// One dropdown must not spin the page for ten minutes. A real upstream lists models in
			// usually <1s; 10s is enough for a slow transoceanic link, and enough to give the answer back
			// to the UI when the other side hangs (it falls back to the declared list).
			Timeout: 10 * time.Second,
		},
		positiveTTL: 10 * time.Minute,
		// The unsupported/error one is much shorter: the user may have just fixed the key, and the next
		// open should ask again.
		negativeTTL: time.Minute,
	}
}

// List implements Service.
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
	// The slice is shared; callers only read it, but copying makes the boundary clearer (and keeps
	// someone from appending to the cached one later).
	cat.Models = append([]Model(nil), e.cat.Models...)
	cat.Groups = append([]string(nil), e.cat.Groups...)
	return &cat, true
}

func (s *service) put(key string, cat Catalog) {
	s.mu.Lock()
	defer s.mu.Unlock()
	// Clear the expired ones while at it: this table's size is the number of providers, a few
	// dozen.
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

// probe tries the adapters in turn, converging "one upstream's answer" into a Catalog.
//
// A failure (network/auth/parse) also returns a Catalog: Supported=false + Note. That route is not
// an HTTP error -- the UI wants "can it list, and why not", not a 500.
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
				// The endpoint is there, but not one model was listed: to the UI this is the same thing as
				// "not supported" -- it has to fall back to what providers.json declares. adapter/endpoint
				// are kept because that reason sentence needs "where we asked".
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
			// A failure on the backup adapter (mostly the wrong auth method) is not evidence that "this
			// upstream is broken": the real main route has already said clearly that it has no such
			// endpoint. Record it in the reason and carry on to the close-out, rather than putting a 401
			// on the UI as the conclusion.
			unsupportedNotes = append(unsupportedNotes, err.Error())
			continue
		}
		// The main route really failed (DNS, 401, parse failure): do not switch adapters -- that only
		// retries the same failure with another auth method and hides the real error.
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

// handle is the BFF extension endpoint's entrance: `POST /ui/api/ext/modellist`.
//
// The request body has only two fields: `{"provider":"…","refresh":false}`. The shape is defined by
// this module, and the BFF passes it through as is (see web-dashboard's ExtensionService).
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
