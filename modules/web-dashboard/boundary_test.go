package webdashboard

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// The UI front end **knows no module**: it gives framework interfaces (Kind, slots, plugin
// contract, host.ext) that modules fill in. A module's front-end knowledge belongs in its
// module (a plugin); combined UI in a wrapper module - not web/src naming module endpoints.
//
// This ratchet guards the most easily crossed, most easily checked kind: front-end source
// **hard-coding** an extension endpoint (`/ext/<name>`). Plugins use host.ext(var) - no match.
//
// When added on 2026-10-29 two crossings already existed: the model dropdown asked the
// modellist and verify endpoints directly; the fingerprint screen was all verify's UI.
// They're listed below, **shrink-only**: migrate one and delete a row; extras or gone = red.
var extLiteralAllowlist = map[string]string{
	"modellist":   "ModelPicker / api.ts: the model picker asks modules/modellist directly",
	"fingerprint": "Fingerprints.svelte / ModelPicker / api.ts: the fingerprint page is modules/verify's UI living in the shell",
}

var extLiteral = regexp.MustCompile(`/ext/([a-z0-9_-]+)`)

func TestShellFrontendNamesNoExtension(t *testing.T) {
	found := map[string][]string{}
	err := filepath.WalkDir("web/src", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !(strings.HasSuffix(p, ".ts") || strings.HasSuffix(p, ".svelte")) {
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		for _, m := range extLiteral.FindAllStringSubmatch(string(b), -1) {
			found[m[1]] = append(found[m[1]], p)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for n := range found {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		if _, ok := extLiteralAllowlist[n]; !ok {
			t.Errorf("web/src names the extension %q (%v): the shell must not know a module — "+
				"ship the UI as a plugin from that module (api.go, \"front-end plugin\"), or aggregate it in a wrapper module",
				n, found[n])
		}
	}
	for n := range extLiteralAllowlist {
		if _, ok := found[n]; !ok {
			t.Errorf("%q is on the allowlist but web/src no longer names it — remove it from extLiteralAllowlist", n)
		}
	}
}
