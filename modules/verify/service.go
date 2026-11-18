package verify

import (
	"context"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/rzbdz/newgate-ext/modules/config/store"
	"github.com/rzbdz/newgate-ext/modules/config/targets"
	i18n "github.com/rzbdz/newgate/lib/i18n"
)

// service is this module's implementation: it stitches config, the fingerprint bank and the cache.
//
// It serves three entries - CLI, web extension endpoint, concept ledger. All three know only
// its methods, so "how a binding is verified" has one implementation (like modellist).
type service struct {
	det   detector
	cache *cacheStore
}

func newService() *service {
	c := newCacheStore()
	return &service{det: newFingerprintDetector(c), cache: c}
}

// State produces all the material for this screen. It **returns no error**: unreadable config
// still draws the page (free verification needs no profile), with the reason in State.Error.
// The compromise between "not silent" and "one screen must not blank because another broke".
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

// groupProfiles folds targets' flat rows into "one group per profile".
//
// The input is already sorted by (profile, tier order); this keeps that order and does not
// sort again - tier order is a capability ladder, not alphabetical.
//
// Rows for "this tier is unbound" are skipped: a sparse profile writing only its own tiers
// is **normal**, and this screen answers "who are the bound models actually". Spilling five
// not-bound rows under each profile would bury the bindings that matter. (A missing provider
// is **kept**: it says the config is broken and needs to be seen.)
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

// Run verifies one binding, returns the conclusion and saves it.
//
// An error means only **the request never went out** (no such provider, no key, bank cannot
// load). An upstream that returns 300 integers but bad samples is **a conclusion** (Entry's
// Error), not a handler failure - the UI shows it on that row, in that row's own place,
// not as a global error.
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
			// The bank gives dozens of candidates; the UI needs the top few, not the whole table.
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
	// A failed save does not affect this conclusion, but decides whether the next UI visit sees it.
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

// Upgrade is the action mounted on `newgate upgrade`: force-refresh the reference bank.
//
// It touches only **dynamic data** (the bank in the cache), never the binary - see the first
// version's boundary in modules/upgrade's package comment.
func (s *service) Upgrade(ctx context.Context) (string, error) {
	return s.det.Refresh(ctx)
}

// UpgradeStatus is this item's answer in `newgate upgrade --check`: what is in hand, no network.
//
// "Is there a new one remotely" takes downloading a bundle (the bank has no cheap version
// probe), and --check touches nothing - so this honestly says "which version is in
// hand, when it was last checked".
//
// The second return is "nothing to refresh": with the bank on a local dir (DataEnv), no y/N.
func (s *service) UpgradeStatus() (string, bool) {
	if src := strings.TrimSpace(os.Getenv(DataEnv)); src != "" {
		return i18n.T("reference data is read from the local directory {dir}; nothing to refresh",
			i18n.A{"dir": src}), true
	}
	v := s.cache.dataVersion()
	if v == "" {
		return i18n.T("no reference data cached yet; upgrade would download it", nil), false
	}
	if at, ok := s.cache.dataCheckedAt(); ok {
		return i18n.T("cached {version}, last checked {ago} ago; upgrade would check for a newer bank",
			i18n.A{"version": shortHash(v), "ago": strings.TrimSuffix(time.Since(at).Round(time.Minute).String(), "0s")}), false
	}
	return i18n.T("cached {version}; upgrade would check for a newer bank", i18n.A{"version": shortHash(v)}), false
}

// shortHash trims the bank version (a sha256) to a readable length, the same convention as git.
func shortHash(v string) string {
	if len(v) == 64 {
		return v[:12]
	}
	return v
}

// cacheDoc is the answer of the "cache only" query (used by the model dropdown).
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
