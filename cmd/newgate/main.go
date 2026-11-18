// Command newgate is **this distribution's** binary: the kernel's composition root
// plus the modules installed by the spec selected at build time.
//
// It differs from the kernel's own cmd/newgate in exactly one thing: **which graph**.
// The graph comes from the spec at the repository root (dist.json /
// dist-simple-cli.json ...), selected at link time with
// -ldflags -X github.com/rzbdz/newgate-ext/manifest/distinfo.spec=<file>;
// without an injection it falls back to DefaultSpec.
//
// The composition root's logic (trace → start the graph → ask the entry ledger →
// hand off) all lives in the kernel's app.Main. This file only says "which graph,
// which version, and where this product wants its assembly record" — the last part
// is a hook now (app.Options.Trace), which is why the distribution keeps its log
// policy (lib/assembly) and the kernel keeps none.
package main

import (
	"context"
	"os"

	"github.com/rzbdz/newgate-ext/lib/assembly"
	"github.com/rzbdz/newgate-ext/manifest"
	"github.com/rzbdz/newgate-ext/manifest/distinfo"
	"github.com/rzbdz/newgate-ext/modules/config/paths"
	app "github.com/rzbdz/newgate/app"
)

// The version triple is injected with -ldflags, matching the kernel's Makefile.
var (
	version    = "dev"
	buildTime  = "unknown"
	commitTime = "unknown"
)

func main() {
	os.Exit(app.Main(context.Background(), app.Options{
		Loader:     manifest.Loader{Spec: distinfo.Spec()},
		Version:    version,
		BuildTime:  buildTime,
		CommitTime: commitTime,
		// The assembly record: the full graph on change, one fingerprint line when it
		// is the same as last time. See lib/assembly for why it dedupes.
		Trace: func() func() { return assembly.TraceToLogFile(paths.LogFile()) },
	}))
}
