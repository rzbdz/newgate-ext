package verify

import (
	"context"

	viewapi "github.com/rzbdz/newgate-ext/lib/view"
	cliapi "github.com/rzbdz/newgate-ext/modules/cli/extension"
	upgradeapi "github.com/rzbdz/newgate-ext/modules/upgrade"
	webapi "github.com/rzbdz/newgate-ext/modules/web-dashboard"
	modules "github.com/rzbdz/newgate/component"
	i18n "github.com/rzbdz/newgate/lib/i18n"
)

// New declares this component.
//
// All three dependencies are Optional for one criterion: **is it still there without the UI?**
//
//   - no view: no web section, `newgate verify` as usual;
//   - no web extension endpoint: no button, the command as usual;
//   - no cli (`dist-dashboard.json`, web only): no command, the web "verify" works, the
//     endpoint calls the service directly.
//
// So it installs into any spec without failing for a missing UI.
func New() modules.Component {
	svc := newService()
	var releases []modules.Release
	return modules.Component{
		Name: Name,
		Desc: func() string {
			return i18n.T("verify which model an upstream really serves, by fingerprint (newgate verify)", nil)
		},
		// Same layer as modellist: diagnostics/infrastructure, not a client, not an upstream patch.
		Type: "infra",
		Requires: []modules.Requirement{
			modules.Optional(viewapi.Capability),
			modules.Optional(webapi.ExtensionCapability),
			modules.Optional(cliapi.Capability),
			// The upgrade registry: with it, mount a "refresh reference bank" action; without
			// it, auto-update by TTL only (see detect.go's memoryTTL).
			modules.Optional(upgradeapi.Capability),
		},
		Start: func(_ context.Context, ctx modules.Context) error {
			if v, ok := modules.Get(ctx, viewapi.Capability); ok {
				rel, err := registerView(v, svc)
				if err != nil {
					return err
				}
				releases = append(releases, rel)
			}
			if web, ok := modules.Get(ctx, webapi.ExtensionCapability); ok {
				rel, err := web.Register(Endpoint, svc.handle)
				if err != nil {
					return err
				}
				releases = append(releases, rel)
			}
			if ui, ok := modules.Get(ctx, cliapi.Capability); ok {
				rel, err := ui.RegisterCommand(verifyCommand{svc: svc})
				if err != nil {
					return err
				}
				releases = append(releases, rel)
				// "Fingerprint" is a term this version added; the help glossary is reported by
				// its owner (same as config reporting profile, confighook reporting agent).
				rel, err = ui.RegisterGlossary(verifyGlossary{})
				if err != nil {
					return err
				}
				releases = append(releases, rel)
			}
			if up, ok := modules.Get(ctx, upgradeapi.Capability); ok {
				rel, err := up.Register(upgradeapi.Action{
					ID:    "fingerprint-data",
					Title: func() string { return i18n.T("Fingerprint reference data", nil) },
					Describe: func() string {
						return i18n.T("download the latest model fingerprint reference bank", nil)
					},
					Run: func(ctx context.Context, req upgradeapi.Request) (upgradeapi.Result, error) {
						if req.Check {
							msg, current := svc.UpgradeStatus()
							return upgradeapi.Result{Message: msg, UpToDate: current}, nil
						}
						msg, err := svc.Upgrade(ctx)
						return upgradeapi.Done(msg), err
					},
				})
				if err != nil {
					return err
				}
				releases = append(releases, rel)
			}
			return nil
		},
		Stop: func(context.Context) error { return modules.ReleaseAll(releases) },
	}
}
