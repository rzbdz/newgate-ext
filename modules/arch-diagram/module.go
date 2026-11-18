// Package archdiagram mounts the architecture diagram on
// the gateway's port (`/arch`), for development viewing.
//
// What it serves is the diagram `tools/archmap` generates -- the same
// `Build`, the same renderer, only turned from "run a test that writes
// into dist/" into "one click in the browser gives this moment's".
//
// # Why a module rather than a test incidentally starting an http server
//
// Because what it needs is exactly what porthub offers: **a path on a shared port**. As a
// module, it is assembled, removed, and listed by `newgate plugin` like any other service; when
// not wanted, do not install it in the spec (see dist-dev.json) and it vanishes from the product
// entirely -- rather than leaving "please run this script by hand during development" somewhere.
//
// # It is a **development-time** thing; two consequences are written here
//
//  1. **The import half needs a source tree**: `go list` must run in a tree with
//     a go.mod. When it cannot run it does not error; the assembly half is still drawn and
//     the import half is written as an explanation (see archmap.Unavailable) -- a deployed
//     binary has no source, so there it has only the assembly diagram, and that is useful.
//  2. **It knows the distribution's own manifest**: the spec table (`manifest.Specs()`) is compiled
//     into the binary, so "what this build installed" can be answered on any machine. This is also
//     why it must live in the distribution repo -- the kernel does not know which specs exist.
package archdiagram

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"sync"

	cliapi "github.com/rzbdz/newgate-ext/modules/cli/extension"
	porthubapi "github.com/rzbdz/newgate-ext/modules/porthub"
	modules "github.com/rzbdz/newgate/component"
)

// Prefix is where it sits on the shared port.
//
// Not `/a` (a data-plane prefix; porthub refuses it on the spot), nor the
// `__newgate` family (the control-plane namespace). `/arch` is open ground.
const Prefix = "/arch"

// SrcEnv points at the root of the source tree: the import half runs `go list` there.
//
// When not given, guess the current working directory (on a dev box the command was most
// likely typed inside the repo). If neither can be obtained, or the tree found is not
// valid, draw only the assembly diagram -- **do not error**: half a diagram beats a 500.
const SrcEnv = "NEWGATE_ARCH_SRC"

// service is this assembly's facts: where the source tree is,
// and the component list the kernel hands over after assembly.
type service struct {
	root string

	mu    sync.Mutex
	comps []modules.Component
}

// SetCatalog is handed once by the kernel **after all Starts** (component.CatalogAware).
//
// The online diagram can only get the list this way: it must answer "who this process has installed
// right now", and the kernel already owns that list. It is **not** reading the distribution's spec
// table -- that table sits on top of the modules (the generated manifest imports every module),
// and a module looking up at it is a compile cycle, hit for real on one dist-dev.json build:
//
//	manifest → arch-diagram → tools/archmap → manifest
//
// Losing a dropdown of "which other assemblies can be built" buys zero
// kernel changes, and a diagram closer to what is actually happening
// (the offline one, `go test ./tools/archmap`, still draws all specs).
func (s *service) SetCatalog(comps []modules.Component) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.comps = comps
}

func (s *service) Components() []modules.Component {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.comps
}

// Compile-time assertion: the kernel finds people through this interface (component.CatalogAware).
var _ modules.CatalogAware = (*service)(nil)

// Service is the face this module **exposes**.
//
// It has no other consumer; its only reason to exist is that the kernel's channel
// for delivering the component list only recognizes "a value already Provided" (see the comment
// in component.deliverCatalog) -- without exposing it, SetCatalog would never be called and
// the diagram would have zero nodes (hit once for real). This is not ceremony: that rule buys a
// kernel that knows no module, at the price of "to be handed something, hand yourself out first".
type Service interface {
	// Components is the list the kernel hands over after assembly (may be empty: not delivered yet).
	Components() []modules.Component
}

// Capability is the identity of this face.
var Capability = modules.NewCapability[Service]("arch-diagram")

func New() modules.Component {
	svc := &service{root: sourceRoot()}
	var release func()
	return modules.Component{
		Name: "arch-diagram",
		// "Other": it is not a kernel, gateway, UI, client, or model, nor a
		// bridge between them. A development-time observation tool jammed into
		// any of those categories would make `newgate plugin`'s grouping lie.
		Type: "others",
		Requires: []modules.Requirement{
			modules.Optional(porthubapi.Capability),
			modules.Optional(cliapi.Capability),
		},
		// Exposed only so the kernel can deliver that component list (see the note on Service).
		Provides: []modules.Provision{modules.Provide(Capability, Service(svc))},
		Start: func(_ context.Context, ctx modules.Context) error {
			// Mounting is an in-process registration (no socket), so mounting once in a CLI process is
			// harmless -- no HTTP server in that process is reading this table (same as web-dashboard).
			if hub, ok := modules.Get(ctx, porthubapi.Capability); ok {
				rel, err := hub.Mount(Prefix, "arch-diagram",
					http.StripPrefix(Prefix, NewHandler(svc)))
				if err != nil {
					return err
				}
				release = rel
			}
			if ui, ok := modules.Get(ctx, cliapi.Capability); ok {
				if _, err := ui.RegisterCommand(archCommand{svc: svc}); err != nil {
					return err
				}
			}
			return nil
		},
		Stop: func(context.Context) error {
			if release != nil {
				release()
				release = nil
			}
			return nil
		},
	}
}

// sourceRoot is where this run scans imports.
//
// The test is **whether that tree is valid** (has go.mod and dist.json), not
// "was the env var given": with the wrong directory, `go list` runs against an
// unrelated tree and yields some plausible-looking package names, so the diagram
// sprouts a pile of nodes nobody recognizes. Better to say "cannot scan".
func sourceRoot() string {
	candidates := []string{}
	if v := os.Getenv(SrcEnv); v != "" {
		candidates = append(candidates, v)
	}
	if wd, err := os.Getwd(); err == nil {
		candidates = append(candidates, wd)
	}
	for _, dir := range candidates {
		if looksLikeSourceTree(dir) {
			return dir
		}
	}
	return ""
}

func looksLikeSourceTree(dir string) bool {
	if dir == "" {
		return false
	}
	for _, name := range []string{"go.mod", "dist.json"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			return false
		}
	}
	return true
}
