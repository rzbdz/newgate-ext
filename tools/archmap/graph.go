// Package archmap draws the **two sources** of this architecture as one browsable HTML.
//
// # Why the two sources belong together
//
//   - **assembly map (resolve)**: really run the component framework's resolution and
//     get "who needs whom, who provides what". It is **declaration**: Requires/Provides.
//   - **import map (scan)**: scan every package's imports. It is **fact**: what the
//     compiler sees.
//
// Where the maps disagree is the problem - Need declared but no import (usually
// taken via capability, fine), or an import with no dependency edge (**a dependency
// nobody declared**, the best-hidden kind: compiles, tests, review misses it, only a
// change shows it cannot be split). Drawn apart, side by side, that gets a handle.
//
// # Direction
//
// Both maps go **deps below, dependents above**: leaf mechanisms at the very bottom
// (nothing depends on them, they on nothing), dependents above them, root on top; arrows up.
package archmap

import (
	"sort"

	modules "github.com/rzbdz/newgate/component"
)

// Node is a point on the map.
type Node struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	// Group decides color and legend (module type / repo ownership).
	Group string `json:"group"`
	// Note is the small line on hover (who provides it, where it is installed).
	Note string `json:"note"`
	// Missing marks "needed, but not in this assembly" - the usual fate of an Optional
	// dependency; drawing it beats omitting it: it answers "what happens if a module is off".
	Missing bool `json:"missing,omitempty"`

	Layer int     `json:"layer"`
	Order int     `json:"order"`
	X     float64 `json:"x"`
	Y     float64 `json:"y"`
	W     float64 `json:"w"`
	H     float64 `json:"h"`
}

// Edge points from From (dependent, drawn above) to To (depended on, drawn below).
type Edge struct {
	From  string `json:"from"`
	To    string `json:"to"`
	Style string `json:"style"` // solid = Need，dashed = Optional，cross = 跨仓库
	Label string `json:"label"` // 走的是哪个 capability
	// Cycle marks "this edge is inside one strongly connected component" (two dirs
	// reachable from each other).
	//
	// It is **a product of aggregation**, not a design flaw: shared leaves (config/domain,
	// cli/extension, gateway/policy...) may be imported by anyone; by file a DAG,
	// aggregated by directory a cycle. Marked red so reviewers see leaf contracts at work.
	Cycle bool `json:"cycle,omitempty"`
}

// Graph is one drawn map (coordinates computed; the front end only paints).
type Graph struct {
	ID     string  `json:"id"`
	Title  string  `json:"title"`
	Note   string  `json:"note"`
	Nodes  []Node  `json:"nodes"`
	Edges  []Edge  `json:"edges"`
	Width  float64 `json:"width"`
	Height float64 `json:"height"`
	Layers int     `json:"layers"`

	// Compact makes the layout **tighten** after layering (points move toward neighbors).
	//
	// When to use it: when **layer numbers have no physical meaning**, only grouping
	// (the import map: same layer means nothing, seeing connections is the goal).
	// Off for the assembly map: there depth is the story, and tightening flattens it.
	Compact bool `json:"-"`
}

// Report is the whole artifact: several assembly maps (one per spec) + one import map.
type Report struct {
	GeneratedAt string   `json:"generatedAt"`
	Specs       []Graph  `json:"specs"`
	Imports     Graph    `json:"imports"`
	Notes       []string `json:"notes"`
}

// ---------- assembly map ----------

// ResolveGraph really runs component resolution and draws the result as a map.
//
// It goes through the framework's `component.Resolve` (**the blueprint**, not `New`):
// validation and topological sort included, but no Start runs - no ports, no writes,
// no global registry changes. So one process can ask for several maps (one per
// spec), and this is **a capability the framework gives**, not a re-parse here.
func ResolveGraph(id, title, note string, loaders ...modules.Loader) (Graph, error) {
	plan, err := modules.Resolve(loaders...)
	if err != nil {
		return Graph{}, err
	}
	return buildGraph(id, title, note, plan.Components(), plan.Dependencies()), nil
}

// CatalogGraph draws "**what this process has installed right now**".
//
// It differs from ResolveGraph only in the **source**: that one runs Resolve (the
// blueprint: per the loader's declarations), this one uses the live component list
// (the fact the kernel hands over via CatalogAware, as modules/pluginmanager does).
// The online map can only take this road - it has no loader, and its question is
// "who is installed here now". Dependency edges come from both sources: they are
// what Requires/Provides say, and need no resolver.
func CatalogGraph(id, title, note string, comps []modules.Component) Graph {
	return buildGraph(id, title, note, comps, dependenciesOf(comps))
}

// dependenciesOf extracts dependency edges from the **installed components**, same
// shape as Plan.Dependencies.
//
// One criterion: a Requires port name that appears in someone's Provides. Nobody ->
// empty (dotted point, see buildGraph). Plan validates; here we draw a **succeeded** assembly.
func dependenciesOf(comps []modules.Component) map[string][]modules.Dependency {
	owner := map[string]string{}
	for _, c := range comps {
		for _, p := range c.Provides {
			if _, taken := owner[p.Name()]; !taken {
				owner[p.Name()] = c.Name
			}
		}
	}
	out := make(map[string][]modules.Dependency, len(comps))
	for _, c := range comps {
		for _, r := range c.Requires {
			out[c.Name] = append(out[c.Name], modules.Dependency{
				Capability: r.Name(),
				ProvidedBy: owner[r.Name()],
				Optional:   r.Optional(),
			})
		}
	}
	return out
}

// buildGraph is the half both sources share: names + dependency edges -> a drawn map.
func buildGraph(id, title, note string, comps []modules.Component, deps map[string][]modules.Dependency) Graph {
	g := Graph{ID: id, Title: title, Note: note}
	names := make([]string, 0, len(comps))
	for _, c := range comps {
		names = append(names, c.Name)
	}
	sort.Strings(names)
	for _, name := range names {
		g.Nodes = append(g.Nodes, Node{
			ID: name, Label: name, Group: groupOf(comps, name), Note: noteOf(comps, name),
		})
	}

	seen := map[string]bool{}
	for _, c := range comps {
		for _, d := range deps[c.Name] {
			to := d.ProvidedBy
			if to == "" {
				// Nobody provides it: the usual fate of an Optional (this distribution turned it
				// off). Draw a dotted point so "what this build left out" is visible - as useful
				// as a solid edge when reviewing an assembly.
				to = "?" + d.Capability
				if !seen[to] {
					seen[to] = true
					g.Nodes = append(g.Nodes, Node{
						ID: to, Label: d.Capability, Group: "missing", Missing: true,
						Note: "needed by " + c.Name + ", not installed in this distribution",
					})
				}
			}
			style := "solid"
			if d.Optional {
				style = "dashed"
			}
			g.Edges = append(g.Edges, Edge{From: c.Name, To: to, Style: style, Label: d.Capability})
		}
	}
	Layout(&g)
	return g
}

func groupOf(comps []modules.Component, name string) string {
	for _, c := range comps {
		if c.Name == name {
			if c.Type == "" {
				return "module"
			}
			return string(c.Type)
		}
	}
	return "module"
}

func noteOf(comps []modules.Component, name string) string {
	for _, c := range comps {
		if c.Name != name {
			continue
		}
		var provided []string
		for _, p := range c.Provides {
			provided = append(provided, p.Name())
		}
		sort.Strings(provided)
		note := "type: " + string(c.Type)
		if len(provided) > 0 {
			note += " · provides: " + join(provided, ", ")
		}
		return note
	}
	return ""
}

func join(items []string, sep string) string {
	out := ""
	for i, s := range items {
		if i > 0 {
			out += sep
		}
		out += s
	}
	return out
}
