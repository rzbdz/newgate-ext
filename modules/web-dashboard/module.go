// Package webdashboard is the web UI plus its BFF.
//
// It mounts under /ui on the gateway port (8899 by default) - **one shared port** is a
// hard deployment requirement (firewall, reverse proxy, ACLs are per-port), and the
// mechanism is the kernel's porthub: here only "mount my handler"; dispatch is porthub's.
//
// # It knows no module
//
// The BFF reads a **concept ledger** (core/lib/view): each module Optionally depends on
// that capability and registers in its own Start "what this face looks like, how to
// change it". So adding a module's UI changes neither this package nor the front end -
// the same rule as cli and its injection point (modules/cli/extension), implemented twice.
//
// The Service handed out has only Register: the query half is left to the UI (it renders).
package webdashboard

import (
	"context"
	"embed"
	i18n "github.com/rzbdz/newgate/lib/i18n"
	"io/fs"
	"net/http"

	servingapi "github.com/rzbdz/newgate-ext/lib/serving"
	viewapi "github.com/rzbdz/newgate-ext/lib/view"
	cliapi "github.com/rzbdz/newgate-ext/modules/cli/extension"
	porthubapi "github.com/rzbdz/newgate-ext/modules/porthub"
	modules "github.com/rzbdz/newgate/component"
)

// assets is the front-end build output. **Artifacts go in version control** (same as
// manifest/modules_gen.go): `go build` and CI stay fully offline; front-end tooling dev-only.
//
//go:embed all:web/dist
var assets embed.FS

// Name is this module's name in assembly, the ledger and `newgate plugin` (= dir name).
//
// It is also **the concept ledger's source name** (see the Register in Start): the ledger
// groups each section by source, and this section's source is this module.
const Name = "web-dashboard"

const (
	// Prefix is its landing spot on the shared port. Change it, change the front-end base too.
	Prefix = "/ui"
	// AssetDir is the build output directory. **One source of truth**: embed reads it, the
	// front-end build writes it, a future "artifact stale?" check reads it (like CatalogDir).
	AssetDir = "web/dist"
)

// New declares the web UI component.
//
// All dependencies are Optional:
//   - porthub present -> mount on the shared port (the normal shape);
//   - porthub absent, serving present -> fallback: listen on a port of our own (standalone.go).
//     It comes up **only in the serving process** - registers on serving's ledger and waits
//     for whoever owns the port to say "I am about to serve". Why not decide here: a
//     module's Start runs on every `newgate …` command (the modules/i18n lesson), so
//     listening here means one `newgate status` opens a server; and reading os.Args to
//     tell "am I the serving process" is explicitly forbidden in the kernel.
//   - cliapi present -> mount `newgate web` (tells the user where to find the UI).
func New() modules.Component {
	views := viewapi.NewRegistry()
	// Theme registry (see api.go and theme.go): same path as the concept ledger - the UI
	// offers the port and theme modules register in their own Start. Adding a theme needs no
	// change here, and with every theme module gone the UI still works (factory tokens).
	themes := newThemeRegistry()
	// Extension endpoint registry (see api.go and extension.go): same path as the concept
	// ledger - the UI offers the port, modules register a name and a function in their Start.
	// With no extensions installed endpoints 404, and the UI falls back to its own data.
	extensions := newExtensionRegistry()
	plugins := newPluginRegistry()
	// self is this assembly's fact: mounted or not, and how to undo the mount. The command
	// uses it to answer "where is the UI" truthfully - having this module **is not** having an
	// entry (absent porthub there is none); let the actual mount speak, not "am I installed".
	self := &instance{views: views}
	return modules.Component{
		Name: Name,
		Desc: func() string { return i18n.T("the web configuration interface: a BFF and the front-end bundle", nil) },
		Type: "cli", // the established value for this "UI shell" kind (tui / simple-cli use it too)
		Requires: []modules.Requirement{
			// Shared port (the norm).
			modules.Optional(porthubapi.Capability),
			// Serve-time callback (fallback: own port when porthub is absent). Used **in exactly
			// one place** - the criterion is not "who am I" but "has someone started serving".
			modules.Optional(servingapi.Capability),
			modules.Optional(cliapi.Capability),
		},
		// This ledger is **what it provides**: other modules Optionally depend on it and
		// register concepts in their own Start. So it must exist at Bind time (built in New),
		// not in Start - else modules that Start earlier could not register.
		Provides: []modules.Provision{
			modules.Provide(viewapi.Capability, viewapi.Service(views)),
			modules.Provide(ThemeCapability, ThemeService(themes)),
			modules.Provide(ExtensionCapability, ExtensionService(extensions)),
			modules.Provide(PluginCapability, PluginService(plugins)),
		},
		Start: func(_ context.Context, ctx modules.Context) error {
			sub, err := fs.Sub(assets, AssetDir)
			if err != nil {
				return err
			}
			// The theme card (see themeConcept in theme.go): registered into **the ledger this
			// module itself provides** - the same path other modules use, except contributor and
			// ledger are one module. It registers **unconditionally**, not per "is there an
			// entry this time": the cell is one of this module's settings; an entry-less assembly
			// (no porthub, no serving) is a rare fallback; a branch adds an unrun state.
			//
			// The read function is **lazy**: evaluated at snapshot time, so theme modules (they
			// Start after this one) have registered by then - the dropdown loses no options.
			if _, err := views.Register(Name,
				// The message must be a **literal** (the i18n scanner only sees those - a concatenated
				// sentence cannot be translated, see that error's source). So no constant here.
				viewapi.Title(func() string { return i18n.T("Interface", nil) }).Of(viewapi.FieldSettings),
				func() ([]viewapi.Concept, error) {
					return []viewapi.Concept{themeConcept(themes)}, nil
				}); err != nil {
				return err
			}
			handler := NewHandler(sub, views, themes, extensions, plugins)
			// Two paths, only one taken, and each asks first "does this path exist":
			//
			//   - **porthub present**: mount on the shared port (normal). Mounting is an in-process
			//     registration (no socket, no goroutine), so even in a CLI process it has no side
			//     effect - that process has no HTTP server reading this table.
			//   - **porthub absent, serving present**: fallback, own port. But **only in the serving
			//     process** (see the serving package): register a callback, wait for whoever owns the
			//     port to say "I am about to serve".
			//   - neither: this assembly's UI has no entry (`newgate web` will say so).
			hub, hasHub := modules.Get(ctx, porthubapi.Capability)
			listeners, hasServing := modules.Get(ctx, servingapi.Capability)
			switch {
			case hasHub:
				// Mounted on Prefix (not Prefix+"/"): porthub's matches accepts "exactly the prefix"
				// and "prefix + /", both caught by this mount (see lib/porthub). So when `/ui`
				// arrives, StripPrefix hands over an **empty path**.
				//
				// An empty path renders fine (static treats it as index.html), but **the browser
				// resolves relative URLs by truncating at the document path**: from `/ui`,
				// `api/snapshot` becomes `/api/snapshot` - nobody claims it, it lands in the data
				// plane's catch-all as a forward request of unknown shape (400, counted as data
				// traffic). So an empty path redirects to the version with a trailing slash.
				//
				// 302, not 301: permanent redirects are cached almost forever, so after a Prefix
				// change a machine that saw 301 can never be corrected (same in standalone.go).
				stem := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path == "" {
						http.Redirect(w, r, Prefix+"/", http.StatusFound)
						return
					}
					handler.ServeHTTP(w, r)
				})
				rel, err := hub.Mount(Prefix, "web-dashboard",
					http.StripPrefix(Prefix, stem))
				if err != nil {
					return err
				}
				// Mount's release is func(), OnServe's is component.Release (func() error) -
				// normalize to the latter, so the shutdown path knows one shape only.
				self.mode = modeShared
				self.release = func() error { rel(); return nil }
			case hasServing:
				rel, err := listeners.OnServe("web-dashboard", func() (func(), error) {
					return serveStandalone(handler)
				})
				if err != nil {
					return err
				}
				self.mode, self.release = modeStandalone, rel
			default:
				self.mode = modeNone
			}
			// The command is a **weak dependency**: with no UI installed nothing is lost (concepts
			// still register, the module still works), just nobody can type `newgate web`.
			if ui, ok := modules.Get(ctx, cliapi.Capability); ok {
				rel, err := ui.RegisterCommand(&webCommand{self: self})
				if err != nil {
					return err
				}
				self.releases = append(self.releases, rel)
			}
			return nil
		},
		Stop: func(context.Context) error {
			for _, rel := range self.releases {
				_ = rel()
			}
			self.releases = nil
			if self.release != nil {
				_ = self.release()
				self.release = nil
			}
			self.mode = modeNone
			return nil
		},
	}
}

// instance is this module's fact in this assembly; Start and the command share it.
//
// Why the command cannot judge "is porthub there" itself: that is an **assembly-time**
// fact, and the command runs in "this call" - same process (every command assembles
// once), but asking porthub means repeating the assembly-time judgement, and it
// answers wrong when "mount failed at assembly, looks present at command time".
type instance struct {
	views    *viewapi.Registry
	mode     string
	release  func() error
	releases []func() error
}

// The three shapes of the UI's **entry**. The command speaks by it: installed is not entry.
const (
	// modeShared: mounted on the shared port (porthub present).
	modeShared = "shared"
	// modeStandalone: own port (porthub absent, serving present), and it really comes up
	// only in the serving process.
	modeStandalone = "standalone"
	// modeNone: no entry at all in this assembly (both absent).
	modeNone = "none"
)
