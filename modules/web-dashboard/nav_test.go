package webdashboard

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// A few ratchets for the layout layer. All **text-level** checks (CI must be offline, no
// front-end test runner), same trade-off as kinds_test.go / web_test.go: they see "is it
// there", not "does it look good" - the latter needs a human to open and look.

// frontendSources reads every front-end source under web/src (subdirs included, kinds/ too).
//
// Only src/, not dist/: artifacts are minified; a regex finds no structure and misfires.
func frontendSources(t *testing.T) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir("web/src", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		switch filepath.Ext(p) {
		case ".svelte", ".ts":
		default:
			return nil
		}
		b, rerr := os.ReadFile(p)
		if rerr != nil {
			return rerr
		}
		out[p] = string(b)
		return nil
	})
	if err != nil {
		t.Fatalf("cannot read the front-end source: %v", err)
	}
	if len(out) == 0 {
		t.Fatal("not a single .svelte/.ts file was read under web/src -- did the path change? this assertion runs nothing")
	}
	return out
}

// moduleLiterals are the modules' **source names** (the machine tag used when registering view).
//
// Their appearance in the front end means the UI started knowing modules. Not fastidiousness:
// that is the whole promise of "adding a module's UI needs no front-end change", and breaking
// it takes no resolve - one `if (c.source === "config")` runs and nothing goes red.
var moduleLiterals = []string{
	"config", "gateway", "breaker", "plugin-manager", "runtime", "claudecode", "opencode-omo",
}

func TestFrontendDoesNotKnowModuleNames(t *testing.T) {
	// The criterion is **string literals in code**, not the word appearing in the text: an ID
	// cited in a comment (`config.file.mappings/demo.json`) explains a format, not a module -
	// and comments are what this repo writes most; counting them would force people to
	// delete that sentence. So strip comments first (comments only, strings untouched).
	var patterns []*regexp.Regexp
	for _, m := range moduleLiterals {
		patterns = append(patterns,
			regexp.MustCompile(`["'\x60]`+regexp.QuoteMeta(m)+`["'\x60]`))
	}
	for path, src := range frontendSources(t) {
		code := stripComments(src)
		for i, re := range patterns {
			if loc := re.FindStringIndex(code); loc != nil {
				t.Errorf("%s: module name %q appears -- the front end must not know any module."+
					"each module reports its column name at registration (backend view.Sections); the UI just draws it;"+
					"to filter by source, compare against the source string you were given, do not hardcode a name",
					path, moduleLiterals[i])
			}
		}
	}
}

// stripComments removes `//` and `/* */` comments, **leaving strings and template strings**.
//
// Why write these thirty lines: a regex would be fooled by `//` inside a string (a URL,
// a slash in a string), cutting the rest of the line as a comment - and the miss would be
// real code. A state machine knows quotes, so it will not.
func stripComments(s string) string {
	var b strings.Builder
	runes := []rune(s)
	for i := 0; i < len(runes); i++ {
		c := runes[i]
		switch {
		case c == '/' && i+1 < len(runes) && runes[i+1] == '/':
			for i < len(runes) && runes[i] != '\n' {
				i++
			}
			if i < len(runes) {
				b.WriteRune('\n')
			}
		case c == '/' && i+1 < len(runes) && runes[i+1] == '*':
			i += 2
			for i+1 < len(runes) && !(runes[i] == '*' && runes[i+1] == '/') {
				i++
			}
			i++ // 停在 '/' 上，循环的 i++ 跳过它
		case c == '"' || c == '\'' || c == '`':
			b.WriteRune(c)
			for i++; i < len(runes); i++ {
				b.WriteRune(runes[i])
				if runes[i] == '\\' && c != '`' && i+1 < len(runes) {
					i++
					b.WriteRune(runes[i])
					continue
				}
				if runes[i] == c {
					break
				}
			}
		default:
			b.WriteRune(c)
		}
	}
	return b.String()
}

// TestSectionTitlesComeFromTheWire: sidebar section titles must **come from the backend**.
//
// When this link breaks nothing errors - api.ts's `doc.sections ??= []` fills the missing
// field with an empty array and the front end falls back to source names, so the sidebar
// shows machine tags like config / plugin-manager. It looks like "not translated yet",
// not a bug. So an assertion watches "this field is really read", not who spots it later.
func TestSectionTitlesComeFromTheWire(t *testing.T) {
	src := frontendSources(t)

	api, ok := src["web/src/api.ts"]
	if !ok {
		t.Fatal("web/src/api.ts is gone")
	}
	for _, want := range []string{"sections", "Section"} {
		if !strings.Contains(api, want) {
			t.Errorf("api.ts has no %q -- the snapshot column table needs a type", want)
		}
	}

	// App must really keep doc.sections and hand it to the sidebar: declaring the type
	// without reading it still draws source names, and the test above would pass.
	app, ok := src["web/src/App.svelte"]
	if !ok {
		t.Fatal("web/src/App.svelte is gone")
	}
	if !strings.Contains(app, "doc.sections") {
		t.Error("App.svelte does not read sections from the snapshot -- the sidebar can only use the source name as the title")
	}
	if !strings.Contains(app, "Sidebar") {
		t.Error("App.svelte does not pass the column table to the sidebar")
	}
}

// TestRoutingIsHashBased pins the decision that "location lives in the hash".
//
// Why pin it: switching to path routing **would work** (the BFF's SPA fallback accepts
// extensionless GETs), so someone changing it casually would turn nothing red - and the
// cost appears only when a relative address resolves wrong again (see nav.ts's header: the
// /ui bug was this class). The assertion reminds: runtime paths follow the document path.
func TestRoutingIsHashBased(t *testing.T) {
	src := frontendSources(t)
	nav, ok := src["web/src/nav.ts"]
	if !ok {
		t.Fatal("web/src/nav.ts is gone (routing parse and generation live there)")
	}
	if !strings.Contains(nav, "parseHash") {
		t.Error("nav.ts has no parseHash")
	}
	for path, s := range src {
		if strings.Contains(s, "history.pushState") {
			t.Errorf("%s: uses history.pushState -- if the location used paths,"+
				"the runtime address would follow the doc path (that is exactly the shape of the /ui bug)", path)
		}
	}
}

// TestNoFlatWaterfall: one waterfall must not come back.
//
// It used to group every card by source and lay them all downward (measured 4303px, ~4.8
// screens); step one of "change a switch" was scrolling three screens. This checks that
// shape itself: `{#each sources as …}` plus `<h2>`. The shortest sentence of this redesign.
func TestNoFlatWaterfall(t *testing.T) {
	app := frontendSources(t)["web/src/App.svelte"]
	if strings.Contains(app, "srch") {
		t.Error("App.svelte still has that group heading (.srch) -- did the waterfall come back?")
	}
	if regexp.MustCompile(`#each\s+sources\s+as`).MatchString(app) {
		t.Error("App.svelte still walks sources as a whole and lays out cards -- that is a waterfall, not a workbench with navigation")
	}
	if !strings.Contains(app, "TabStrip") || !strings.Contains(app, "Sidebar") {
		t.Error("App.svelte has no sidebar or tab bar -- the layout is tiled again")
	}
}
