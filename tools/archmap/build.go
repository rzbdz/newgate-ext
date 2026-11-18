package archmap

import (
	"sort"
	"time"

	app "github.com/rzbdz/newgate/app"
	modules "github.com/rzbdz/newgate/component"
)

// Build assembles the whole report: **one assembly map per spec + one import map**.
//
// It was a block in the test (the artifact step); a second caller, `modules/arch-
// diagram`, now mounts it on the shared port for browsers. Two versions would grow
// crooked in concrete ways (spec enumeration, caption, failure degradation) - so one.
//
// **The spec table is a parameter, not an import**: the generated manifest imports
// every module, and a module imports this package (arch-diagram) - importing
// manifest here is a compile cycle, hit in practice; core/app's SpecAware hands it down.
//
// root is the **source tree root** (`go list` runs there). The assembly half does not need it.
func Build(specs map[string]app.Selection, root string) (Report, error) {
	report := Report{GeneratedAt: time.Now().Format(time.RFC3339)}

	names := make([]string, 0, len(specs))
	for name := range specs {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		sel := specs[name]
		// Actually run the resolution (Start nothing): this is the "resolved" map.
		g, err := ResolveGraph(name, name, DistributionNote(sel), sel)
		if err != nil {
			return report, err
		}
		report.Specs = append(report.Specs, g)
	}

	report.Imports = importHalf(root)
	return report, nil
}

// BuildCatalog is the **online** report: a single assembly map - **what this process
// has installed right now** (see CatalogGraph) - plus the import scan.
//
// Why not "one per spec": that table sits above the modules (the manifest imports
// every module), so a module mounted online **cannot see** it - a compile cycle,
// hit in practice. And "who is installed here now" is what this process should
// answer anyway - closer to what is happening than "what else could be assembled".
func BuildCatalog(id, title, note string, comps []modules.Component, root string) Report {
	return Report{
		GeneratedAt: time.Now().Format(time.RFC3339),
		Specs:       []Graph{CatalogGraph(id, title, note, comps)},
		Imports:     importHalf(root),
	}
}

// importHalf is the half both reports share: scan the import statements in the source tree.
func importHalf(root string) Graph {
	if root == "" {
		// No source tree (a deployed binary is like that): **do not guess** the current
		// directory. Guessing makes `go list` run in unrelated directories and emit
		// plausible package names, so the map shows nodes nobody knows - worse than a missing half.
		return Unavailable("no source tree given, so imports were not scanned", nil)
	}
	g, err := ImportGraph(root)
	if err != nil {
		// Not scanning is not "a broken report": the assembly half still matters (what a
		// build installed is answerable anywhere); the import half needs a source tree. So
		// no error: write it as **a sentence** - an empty map would read as "no dependencies".
		return Unavailable("the import scan needs the source tree and the go toolchain", err)
	}
	return g
}

// DistributionNote is the line in hover/overview: what this spec **installed and left off**.
//
// That is an assembly map's most useful context: the same module table is a dozen
// points for a skeleton ("disable": ["*"]) and thirty-odd for the flagship - know which.
func DistributionNote(sel app.Selection) string {
	parts := []string{plural(len(sel.Extra), "own module", "own modules")}
	switch {
	case sel.AllCore:
		parts = append(parts, "all built-in kernel modules disabled")
	case len(sel.Disable) > 0:
		parts = append(parts, "kernel modules disabled: "+join(sel.Disable, ", "))
	default:
		parts = append(parts, "all built-in kernel modules installed")
	}
	return join(parts, " · ")
}

// Unavailable is the empty map for "this half was not drawn because ...".
func Unavailable(why string, err error) Graph {
	note := why
	if err != nil {
		note += ": " + err.Error()
	}
	return Graph{
		ID:    "imports",
		Title: "imports (not scanned)",
		Note:  note,
	}
}
