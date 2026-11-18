package locale

import (
	"context"
	"os"

	viewapi "github.com/rzbdz/newgate-ext/lib/view"
	cliapi "github.com/rzbdz/newgate-ext/modules/cli/extension"
	"github.com/rzbdz/newgate-ext/modules/config/paths"
	confighookapi "github.com/rzbdz/newgate-ext/modules/confighook"
	modules "github.com/rzbdz/newgate/component"
	"github.com/rzbdz/newgate/lib/i18n"
)

// TypeInfra is this module's classification tag in the graph (the kernel does not define the
// values; the product fills them in).
const TypeInfra = "infra"

// service is the port's implementation: it holds only "the result resolved this run", not the
// catalogs -- those live in the lib/i18n layer (anyone can look them up, no need to take the port).
type service struct {
	lang      string
	requested string
	source    Source
}

func (s *service) Lang() string           { return s.lang }
func (s *service) Requested() string      { return s.requested }
func (s *service) Source() Source         { return s.source }
func (s *service) Available() []i18n.Info { return i18n.Available() }

// New assembles the language thing.
//
// Dependency direction: **only confighook** (registering a state.json field must go through its
// port, same as plugin-manager), plus one weak dependency on the UI (with a UI, hang `newgate lang`
// and that status line up; without one, skip -- this module's most important job, "install the
// catalogs", depends on nothing).
//
// It must be **removable**: with it gone lib/i18n has no catalogs, every sentence comes out as the
// source-language text, and the process carries on (the removal matrix in `app/matrix_test.go`
// really does remove it once).
func New() modules.Component {
	svc := &service{}
	var releases []modules.Release
	return modules.Component{
		Name: "locale",
		Desc: func() string {
			return i18n.T("which language this process speaks (env > config > system), and installs its tables", nil)
		},
		Type: TypeInfra,
		Requires: []modules.Requirement{
			modules.Optional(cliapi.Capability),
			// Web UI: if present, hang the "language" card up (see view.go); if not, skip.
			// Like the CLI one this is a **weak dependency** -- this module's most important job,
			// "install the catalogs", depends on nothing.
			modules.Optional(viewapi.Capability),
			modules.Need(confighookapi.ConfigHooksCapability),
		},
		Provides: []modules.Provision{modules.Provide(Capability, Service(svc))},
		Start: func(_ context.Context, ctx modules.Context) error {
			hooks := modules.MustGet(ctx, confighookapi.ConfigHooksCapability)
			field, err := hooks.RegisterStateField("locale", StateKey)
			if err != nil {
				return err
			}
			releases = append(releases, field)

			req := resolve(os.Getenv, loadConfigured())
			led, cats, err := i18n.Builtin()
			if err != nil {
				return err
			}
			// Disk overrides: `$NEWGATE_HOME/locale/<tag>.json` can change any built-in translation,
			// without recompiling (the same route as mappings/).
			eff, err := i18n.Install(req.Tag, led, cats, i18n.OverlayDir(paths.Root()))
			if err != nil {
				return err
			}
			svc.lang, svc.requested, svc.source = eff, i18n.Normalize(req.Tag), req.Source

			// Web UI: register that language card (the equivalent of `newgate lang`, see view.go).
			if v, ok := modules.Get(ctx, viewapi.Capability); ok {
				rel, err := registerView(v)
				if err != nil {
					return err
				}
				releases = append(releases, rel)
			}

			cli, ok := modules.Get(ctx, cliapi.Capability)
			if !ok {
				return nil // no UI installed: the language still applies, there is just no entry point
			}
			cmd := &command{svc: svc}
			for _, reg := range []func() (modules.Release, error){
				func() (modules.Release, error) { return cli.RegisterCommand(cmd) },
				func() (modules.Release, error) { return cli.RegisterStatus(cmd) },
				func() (modules.Release, error) { return cli.RegisterDiagnostics(cmd) },
				func() (modules.Release, error) { return cli.RegisterGlossary(cmd) },
			} {
				rel, err := reg()
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
