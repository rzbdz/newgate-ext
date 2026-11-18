// Package targets resolves profile config into "the (provider, model) to hit this round".
//
// # Why a separate package
//
// Two diagnostics do the same thing: `newgate probe` (connectivity/dialect) and
// `newgate verify` (fingerprint). Their **conclusions** are completely different, but
// the knowledge "which profiles, which tiers, and after dedup which upstreams" exists
// in only one copy. Before 2026-10 it was inlined in gateway/probe; the distribution's
// verify needs the same list, and the kernel's probe implementation package should not
// be imported by the distribution (that is an internal implementation). Lowered here,
// both share this one copy, and profile resolution rules drift in only one place.
//
// It depends only on config's shared leaves (domain/store), does just
// disk reading and resolution, and knows neither probe nor verify
// -- so the arrows are `probe -> targets` and `verify -> targets`.
package targets

import (
	"sort"
	"strconv"
	"strings"

	"github.com/rzbdz/newgate-ext/modules/config/domain"
	"github.com/rzbdz/newgate-ext/modules/config/store"
	i18n "github.com/rzbdz/newgate/lib/i18n"
)

// Target is the deduped minimal identity: profile/role are just config views that **reference** it.
type Target struct {
	Provider string
	Model    string
}

// String returns a stable provider/model identifier, for sorting and diagnostics.
func (t Target) String() string { return t.Provider + "/" + t.Model }

// Row is one config view: which binding some profile's some tier resolved to.
//
// The same Target can appear in many Rows (one binding shared by several
// profiles' tiers), and the caller dedups by Target before hitting requests.
type Row struct {
	Profile  string
	Role     string
	Provider string
	Model    string
	// Err non-empty = this tier has no hittable target (unbound, or the
	// provider is not declared). It is still a row: the user needs to
	// know "this tier is empty", not to have it vanish from the table.
	Err string
}

// Options controls which slice of config to resolve.
type Options struct {
	// Only is one profile name; empty = all profiles.
	Only string
	// OnlyTarget names one binding; empty = go by Only / all profiles.
	//
	// The difference from Only is "what to find by": Only is a profile name (under
	// which are a chain's several candidates), while this is one specific binding.
	// Set it and **profiles are not consulted**: the target is already named, and
	// walking the config again would make "hit this one" also hit a string of others.
	OnlyTarget *Target
}

// Enumerate resolves the config, returning the config
// views (Rows) and the deduped targets (Targets).
//
// Targets are sorted by String; Rows by (profile, tier order). When a profile file
// is broken it is **skipped** (same as store.Load: one hand-broken file should
// not stall all diagnostics); which file is broken is reported by doctor, and here
// only "the named profile does not exist" is reported -- that is an error blocking
// the whole round, and silence would make the caller think the config is empty.
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
		// One was named: do not walk profiles (for the reason see Options.OnlyTarget).
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
