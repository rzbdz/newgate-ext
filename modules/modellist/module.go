// Package modellist lets the web UI ask "which models does this upstream offer right now".
//
// # The thing it solves
//
// `models` in providers.json is a list **the user wrote by hand**: when an upstream ships a new
// model, the user has to add a row on the provider card first, then come back to the tier to pick
// it. Yet most upstreams actually have `GET /models` -- able to list what is really available right
// now, some with grouping.
//
// This module does three things:
//
//  1. **Probe**: pick an adapter by the provider's `protocol` (the OpenAI / Anthropic dialects) and
//     ask the upstream once. Not getting an answer (no such endpoint, key not allowed, network
//     down) is **not an error**: return `supported:false` + the reason, and the UI falls back to
//     the declared list.
//  2. **Converge**: different upstreams have different field names (`id` / `slug`, `owned_by` /
//     `group` / `provider`); converge them into `Model{id,name,group}` -- the front end knows only
//     one shape.
//  3. **Cache**: in process, bucketed by the hash of the provider definition (a new key/base is
//     another bucket), 10 minutes for a positive result and 1 minute for a negative one; the ↻ next
//     to the dropdown skips the cache.
//
// # Why it lives in the distribution
//
// It is a **product trade-off**, not a kernel mechanism: with or without it, the kernel works the
// same. Its mechanism has only two public mouths -- `Service` (other modules can ask for the
// listing) and web-dashboard's extension endpoint (the UI can ask for the listing). Both directions
// are "this module → the UI"; the UI does not know it.
package modellist

import (
	"context"

	webapi "github.com/rzbdz/newgate-ext/modules/web-dashboard"
	modules "github.com/rzbdz/newgate/component"
	i18n "github.com/rzbdz/newgate/lib/i18n"
)

// New declares this component.
//
// There is only one dependency, and it is Optional: with web-dashboard installed, register the
// extension endpoint; without it (`dist-simple-cli.json`-style assemblies with only a terminal UI)
// this module installs as usual and `Service` works as usual, there is just no browser entrance.
// That is exactly the rule "a business module depends on no ui".
func New() modules.Component {
	svc := newService()
	var releases []modules.Release
	return modules.Component{
		Name: Name,
		Desc: func() string {
			return i18n.T("ask a provider which models it serves, for the model picker in the web interface", nil)
		},
		// Same layer as the kernel's config/gateway: it is infrastructure, not a client, not an upstream
		// patch.
		Type: "infra",
		Requires: []modules.Requirement{
			modules.Optional(webapi.ExtensionCapability),
		},
		Provides: []modules.Provision{
			modules.Provide(Capability, Service(svc)),
		},
		Start: func(_ context.Context, ctx modules.Context) error {
			// With no UI installed nothing is registered: the feature (Service) is still there, just with
			// no entrance.
			web, ok := modules.Get(ctx, webapi.ExtensionCapability)
			if !ok {
				return nil
			}
			rel, err := web.Register(Endpoint, svc.handle)
			if err != nil {
				return err
			}
			releases = append(releases, rel)
			return nil
		},
		Stop: func(context.Context) error { return modules.ReleaseAll(releases) },
	}
}
