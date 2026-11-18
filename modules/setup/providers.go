package setup

// providers.json's "read raw, change one place, write back".
//
// The kernel's store.LoadProviders resolves api_key_env into plaintext api_key (for
// forwarding), so **its result must not go back to disk** - that turns every env-managed
// key into "the key sits in a file" (the kernel's SaveProviders warns about this).
//
// Yet the wizard must update **one key** in providers.json, leaving the others alone.
// So read the raw JSON here: json.RawMessage keeps every segment verbatim, swap ours only.
// Field tags match the kernel's domain.Provider (the on-disk schema, see core/.../domain).

import (
	"encoding/json"
	"io/ioutil"
	"os"

	"github.com/rzbdz/newgate-ext/modules/config/domain"
	"github.com/rzbdz/newgate-ext/modules/config/paths"
	"github.com/rzbdz/newgate/lib/i18n"
)

// loadProvidersRaw reads providers.json's raw form:
//
//	provs  typed view (the wizard adds/edits one key here)
//	raw    the raw text (which providers use api_key_env - their plaintext must not go back)
//
// A missing file gives an empty table (first bootstrap), not an error.
func loadProvidersRaw() (*domain.Providers, map[string]bool, error) {
	resolved := map[string]bool{}
	provs := &domain.Providers{Providers: map[string]domain.Provider{}}

	b, err := ioutil.ReadFile(paths.ProvidersFile())
	if err != nil {
		if os.IsNotExist(err) {
			return provs, resolved, nil
		}
		return nil, nil, i18n.Ef(err, "cannot read {file}: {err}", i18n.A{"file": paths.ProvidersFile()})
	}
	if len(b) == 0 {
		return provs, resolved, nil
	}
	var doc struct {
		Providers map[string]json.RawMessage `json:"providers"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		return nil, nil, i18n.Ef(err, "{file} is not valid JSON: {err}", i18n.A{"file": paths.ProvidersFile()})
	}
	for name, rawMsg := range doc.Providers {
		var p domain.Provider
		if err := json.Unmarshal(rawMsg, &p); err != nil {
			return nil, nil, i18n.Ef(err, "provider {name} in {file} is malformed: {err}",
				i18n.A{"name": name, "file": paths.ProvidersFile()})
		}
		if p.APIKey == "" && p.APIKeyEnv != "" {
			resolved[name] = true
		}
		provs.Providers[name] = p
	}
	return provs, resolved, nil
}

// stripResolvedKeys erases the plaintext of providers that get their key from the environment.
//
// Why it exists: the doc above decodes JSON without evaluating api_key_env - but if
// LoadProviders touched the same struct **before the call**, plaintext comes along. The
// wizard does not, so this is a gate for "someone later changes this path and misses the
// comment": better lose the plaintext (env still there, behavior unchanged) than write it.
func stripResolvedKeys(p *domain.Providers, resolved map[string]bool) {
	for name := range resolved {
		v, ok := p.Providers[name]
		if !ok {
			continue
		}
		v.APIKey = ""
		p.Providers[name] = v
	}
}
