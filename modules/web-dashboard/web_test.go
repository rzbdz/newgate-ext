package webdashboard

import (
	"bytes"
	"encoding/xml"
	"io"
	"io/fs"
	"regexp"
	"strings"
	"testing"
)

// Front-end artifacts **go in version control** (same as manifest/modules_gen.go): `go
// build` and CI stay fully offline, front-end tooling only on dev machines. The cost is
// that "forgot pnpm build" is not caught by the Go compiler - this file guards that cost.
//
// It can only check "is the embedded copy self-consistent", not "is it current" (needs
// node, CI must be offline). But it stops both common wrecks: dist emptied (no artifact)
// and index.html pointing at a hash file that no longer exists (built then not committed).
//
// The other half of "forgot pnpm build" is guarded by i18n_test.go's bundle test: a string
// added without a rebuild is in the source dictionary but not the bundle; that one names it.
// What stays unchecked (style, layout, logic) stays unchecked - a human must look.

var assetRef = regexp.MustCompile(`/ui/assets/[A-Za-z0-9._-]+`)

func TestEmbeddedFrontendIsSelfConsistent(t *testing.T) {
	sub, err := fs.Sub(assets, AssetDir)
	if err != nil {
		t.Fatalf("cannot read the embedded front-end directory (does %s exist?): %v", AssetDir, err)
	}
	index, err := fs.ReadFile(sub, "index.html")
	if err != nil {
		t.Fatalf("there is no index.html in the output -- was the front end committed without being built?: %v", err)
	}
	html := string(index)
	if strings.Contains(html, "placeholder") {
		t.Fatal("what ships is still that placeholder -- web/dist is build output, run pnpm build first")
	}
	refs := assetRef.FindAllString(html, -1)
	if len(refs) == 0 {
		t.Fatal("index.html references not a single asset -- was this build empty?")
	}
	for _, ref := range refs {
		name := strings.TrimPrefix(ref, Prefix+"/")
		if _, err := fs.ReadFile(sub, name); err != nil {
			t.Errorf("index.html references %s, but it is not in the output --"+
				"after changing the front end run pnpm build again and commit (file names carry a content hash; a mismatch means stale)", name)
		}
	}
}

// TestEmbeddedSVGsParse asserts every SVG in the artifact is **valid XML**.
//
// # Why this exists (measured 2026-09-21)
//
// The favicon had never shown in a browser since added, yet "looked" normal everywhere:
// the file present, `index.html` referring to it, a 200, Content-Type image/svg+xml,
// byte count identical to the repo copy. curl verified all green - **it would not draw**.
//
// The root cause was invisible on its face: the comment body contained the CSS variable
// name `--bg`, and XML comments **may not contain two consecutive hyphens**. Chromium
// fails to parse it (`parsererror`, root to html): **nothing drawn** - no error, no placeholder.
//
// That is exactly the class this gate should stop: not "is the artifact current" (needs
// node), not "is the file there" (checked above), but **"can the browser draw these
// bytes"**. Go's encoding/xml judges it: offline, zero deps, **the same rule** as the
// browser - that `--` is what it reports first.
//
// SVG only: `.js`/`.css` are not XML; `.html` trips on Svelte's template syntax (another
// parser; browsers tolerate it, XML standards would only yield false alarms).
func TestEmbeddedSVGsParse(t *testing.T) {
	sub, err := fs.Sub(assets, AssetDir)
	if err != nil {
		t.Fatalf("cannot read the embedded front-end directory (does %s exist?): %v", AssetDir, err)
	}
	checked := 0
	err = fs.WalkDir(sub, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".svg") {
			return err
		}
		b, err := fs.ReadFile(sub, path)
		if err != nil {
			return err
		}
		checked++
		dec := xml.NewDecoder(bytes.NewReader(b))
		for {
			if _, err := dec.Token(); err == io.EOF {
				return nil
			} else if err != nil {
				t.Errorf("%s in the output is not valid XML: %v\n"+
					"  a browser given such a file draws **nothing** (no error, no placeholder),"+
					"  so it can only be caught at this layer. A common trap: two consecutive hyphens in comment text (XML forbids it),"+
					"  for example a CSS variable name `--bg` written straight into a comment.", path, err)
				return nil
			}
		}
	})
	if err != nil {
		t.Fatalf("walking the embedded output failed: %v", err)
	}
	// An empty-set guard so the loop above cannot spin green: the favicon lives here, and no
	// SVG at all means the artifact changed or the scan broke.
	if checked == 0 {
		t.Fatal("not a single SVG was scanned in the embedded output -- where is the favicon? (the loop spins empty, this test checks nothing)")
	}
}
