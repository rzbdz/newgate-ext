package store

import (
	"os"
	"path/filepath"

	"github.com/rzbdz/newgate-ext/modules/config/domain"
	"github.com/rzbdz/newgate-ext/modules/config/paths"
)

// The repo ships no real gateway address, provider naming, or model names.
// Those belong to the user's own config, which lives only in
// ~/.config/newgate/ on their machine and is never committed.
//
// What follows is a TEMPLATE. After `newgate init` you replace the provider
// names, endpoint, and model names with your own. The placeholders are
// deliberately obvious so nobody mistakes them for a working config.
const placeholderBase = "https://your-gateway.example.com/v1"

// SeedProviders writes placeholder providers on first init.
// Keys are read from the environment first (so they never hit disk); the
// inline api_key in providers.json is the fallback.
func SeedProviders() *domain.Providers {
	return &domain.Providers{Providers: map[string]domain.Provider{
		"upstream-a": {
			BaseURL: placeholderBase, Protocol: "openai",
			APIKeyEnv: "NEWGATE_KEY_UPSTREAM_A",
		},
		"upstream-b": {
			BaseURL: placeholderBase, Protocol: "openai",
			APIKeyEnv: "NEWGATE_KEY_UPSTREAM_B",
		},
	}}
}

func prio(n int) *int { return &n }

func one(provider, model string) domain.Candidates {
	return domain.Candidates{{Provider: provider, Model: model}}
}

func list(pairs ...string) domain.Candidates {
	out := make(domain.Candidates, 0, len(pairs))
	for _, p := range pairs {
		if b, err := domain.ParseBindingString(p); err == nil {
			out = append(out, b)
		}
	}
	return out
}

// SeedProfiles ships three demo profiles. Their purpose is to show every
// capability of the fallback chain in as little text as possible — not to
// give you a config you can actually run:
//
//	priority   lower goes first; absent means DefaultPriority (50)
//	list       several candidates per role, tried in order within the profile
//	sparse     define only the roles you care about; the rest fall through
//	pinned     when I am the chain head, the chain stops at me
//	excluded   never fall into me automatically; explicit selection only
func SeedProfiles() []*domain.Profile {
	return []*domain.Profile{
		{
			Name: "cheap", Priority: prio(20),
			Description: "cost first, may escalate (demonstrates list)",
			Roles: map[string]domain.Candidates{
				"heavy":  list("upstream-a/model-large", "upstream-b/model-large"),
				"mid":    list("upstream-a/model-medium", "upstream-b/model-medium"),
				"light":  list("upstream-a/model-small", "upstream-b/model-small"),
				"vision": list("upstream-b/model-vision"),
			},
		},
		{
			Name: "expensive", Priority: prio(10),
			Description: "best first (demonstrates pinned+excluded)",
			Pinned:      true,
			Excluded:    true,
			Roles: map[string]domain.Candidates{
				"heavy": list("upstream-b/model-flagship", "upstream-a/model-large"),
				"mid":   list("upstream-b/model-large"),
			},
		},
		{
			Name: "top-only", Priority: prio(5),
			Description: "sparse layer: covers heavy only, transparent elsewhere",
			Roles: map[string]domain.Candidates{
				"heavy": one("upstream-b", "model-flagship"),
			},
		},
	}
}

// Init lays down defaults idempotently. Existing files are NEVER overwritten,
// so running init on an already-configured machine is safe.
func Init(force bool) ([]string, error) {
	if err := paths.EnsureDirs(); err != nil {
		return nil, err
	}
	var created []string

	if _, err := os.Stat(paths.ProvidersFile()); force || os.IsNotExist(err) {
		if err := SaveProviders(SeedProviders()); err != nil {
			return created, err
		}
		created = append(created, paths.ProvidersFile())
	}

	for _, pr := range SeedProfiles() {
		p := filepath.Join(paths.Mappings(), pr.Name+".json")
		if _, err := os.Stat(p); force || os.IsNotExist(err) {
			if err := SaveProfile(pr); err != nil {
				return created, err
			}
			created = append(created, p)
		}
	}

	if _, err := os.Stat(paths.StateFile()); force || os.IsNotExist(err) {
		if err := SaveState(&domain.State{DefaultProfile: "cheap", Port: domain.ProxyPort}); err != nil {
			return created, err
		}
		created = append(created, paths.StateFile())
	}
	return created, nil
}
