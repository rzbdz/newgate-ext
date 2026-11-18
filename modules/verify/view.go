package verify

import (
	"github.com/rzbdz/newgate-ext/lib/view"
	modules "github.com/rzbdz/newgate/component"
	i18n "github.com/rzbdz/newgate/lib/i18n"
)

// This file is the UI-facing side: a "fingerprints" section with one KindFingerprints card.
//
// Registration **reads no file**: the producer runs only when someone really opens the UI
// (see lib/view's package comment). This holds for every `newgate verify` command - the CLI
// process assembles modules too, and it never opens the web page.

func registerView(v view.Service, svc *service) (modules.Release, error) {
	section := view.Title(func() string { return i18n.T("Fingerprints", nil) }).Of(view.FieldRoutes)
	return v.Register(Name, section, func() ([]view.Concept, error) {
		return []view.Concept{{
			ID:    "verify.fingerprints",
			Kind:  view.KindFingerprints,
			Title: i18n.T("Verification", nil),
			// Live: this side changes by itself - a terminal `newgate verify` just wrote a cache
			// entry, and the cell on the page should follow. One read is a few profiles plus tens
			// of KB of cache, not the config section's "re-read and re-parse all config".
			Live: true,
			Data: svc.State(),
		}}, nil
	})
}
