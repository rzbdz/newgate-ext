package hermes

import (
	"github.com/rzbdz/newgate-ext/modules/config/domain"
	"github.com/rzbdz/newgate-ext/modules/config/store"
	"github.com/rzbdz/newgate-ext/modules/gateway/special"
	i18n "github.com/rzbdz/newgate/lib/i18n"
)

// modelsLister answers `GET …/v1/models` on hermes' behalf (the kernel's
// special.ModelLister).
//
// # Why it is needed (measured on this machine 2026-11-04)
//
// hermes' `/model` asks `<base>/models` once for every named provider, and when it gets an
// answer it **replaces** the model table we wrote into config.yaml with what it got back. The
// proxy's default list is tiers + that batch of omo dynamic-role keys: so the lines pinned to a
// profile (`newgate-<profile>`) end up with tiers only and no concrete models, while the
// "follow us" line grows twenty-odd `omo-*` / `cat-*` entries that mean nothing to hermes.
//
// The first version's workaround was to write `discover_models: false` on every line (the
// picker honors it) -- the price being that hermes' config normalization **does not recognize**
// that key, so every normalization writes one warning line into errors.log, one per provider
// line, several times per round (measured: nine for one `hermes chat`). If the proxy answers
// this list correctly, that key is unnecessary; the side benefit is that the list is
// **answered live**: change a binding in a profile and the next `/model` sees it, with no wait
// for file sync.
//
// What is answered is computed by the **same function** as what is written into the file
// (concreteModels), so the two cannot diverge.
//
// It modifies no request: Match is always false. The name appears in `newgate st`, and turning
// it off (`newgate st off hermes-models`) returns to the default list -- hermes still works,
// only /model shows tiers alone.
type modelsLister struct{}

var (
	_ special.Plugin      = modelsLister{}
	_ special.ModelLister = modelsLister{}
)

func (modelsLister) Name() string { return "hermes-models" }

func (modelsLister) Why() string {
	return i18n.T("hermes' /model replaces its configured model table with whatever <base>/models returns; "+
		"answer the tiers for the follow row and tiers + that profile's bound models for each pinned row", nil)
}

func (modelsLister) Match(*special.Request) bool { return false }

func (modelsLister) Apply(body []byte, _ *special.Request) ([]byte, []string, error) {
	return body, nil, nil
}

func (modelsLister) ListModels(agent, profile string) ([]string, bool) {
	if agent != ID {
		return nil, false
	}
	if profile == "" {
		return append([]string(nil), domain.Roles...), true
	}
	snap, err := store.Load()
	if err != nil || snap == nil {
		return nil, false
	}
	for _, p := range snap.Profiles {
		if p.Name == profile {
			ids, _ := concreteModels(p)
			return append(append([]string(nil), domain.Roles...), ids...), true
		}
	}
	return nil, false
}
