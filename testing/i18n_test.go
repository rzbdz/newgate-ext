package disttesting

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	i18n "github.com/rzbdz/newgate/lib/i18n"
	"github.com/rzbdz/newgate/tools/i18n/check"
)

// TestLocalizationStaysConsistent is the distribution-side localization ratchet.
//
// **Two entrances to one criterion** with the kernel's (core/app/i18n_test.go): one
// implementation (core/tools/i18n/check, same yardstick via replace), only paths differ -
// `modules/i18n/catalogs` not the kernel's `lib/i18n/catalogs`.
//
// Why the distribution needs it: the kernel's ratchet sees only its own tree (the scanner
// stops at nested Go modules, see core/tools/i18n/scan). What these modules say - client
// takeover, upstream quirk patches, the UI - the kernel knows nothing about. Without this,
// the distribution grows hard-coded Chinese nobody owns, while the kernel stays green.
func TestLocalizationStaysConsistent(t *testing.T) {
	root, err := filepath.Abs("..")
	if err != nil {
		t.Fatalf("locating the repo root: %v", err)
	}
	findings, err := check.Run(check.Options{
		Root:       root,
		CatalogDir: "modules/i18n/catalogs",
		Allowlist:  "tools/i18n-allowlist.json",
	})
	if err != nil {
		t.Fatalf("cannot run the localization check: %v", err)
	}
	for _, f := range findings {
		if f.Level == "error" {
			t.Errorf("%s", f.String())
		}
	}

	// Degeneration guard: the criterion breaking must go red, not "report nothing" = pass.
	// A moved directory, an emptied ledger, a scanner finding nothing - none of these speak.
	cats, cerr := check.LoadCatalogs(filepath.Join(root, "modules/i18n/catalogs"))
	if cerr != nil {
		t.Fatalf("cannot read the translation directory -- the check has degraded: %v", cerr)
	}
	if len(cats) == 0 {
		t.Fatal("no translations at all -- the check has degraded (did the directory move? is only the ledger left in catalogs?)")
	}
	led, lerr := check.LoadLedger(filepath.Join(root, "modules/i18n/catalogs"))
	if lerr != nil {
		t.Fatalf("cannot read the ledger -- the check has degraded: %v", lerr)
	}
	if len(led.Messages) == 0 {
		t.Fatal("the ledger is empty -- the scanner is broken, or the distribution source has no i18n.T left at all")
	}
}

// TestTheBundlesMatchTheJSON pins "edited JSON, forgot the bundle".
//
// The distribution is **another Go module** with its own catalog table (`modules/i18n/
// catalogs`), while runtime reads the compile-time `.bin` (see lib/i18n/bundle.go: JSON
// parsing is nearly half of each `newgate …` command's assembly cost). So it gains a
// **silent failure mode**: `.bin` lags `.json`, the UI runs fine minus new strings, no error.
//
// So the criterion lands on content: unpack the embedded copy, compare with disk JSON.
func TestTheBundlesMatchTheJSON(t *testing.T) {
	root, err := filepath.Abs("..")
	if err != nil {
		t.Fatalf("locating the repo root: %v", err)
	}
	dir := filepath.Join(root, "modules/i18n/catalogs")

	// Ledger.
	ledRaw, err := os.ReadFile(filepath.Join(dir, i18n.LedgerBundleName))
	if err != nil {
		t.Fatalf("cannot read the ledger bundle: %v", err)
	}
	fromBin, err := i18n.DecodeLedger(ledRaw)
	if err != nil {
		t.Fatalf("cannot decode the ledger bundle: %v", err)
	}
	jsonRaw, err := os.ReadFile(filepath.Join(dir, i18n.LedgerName))
	if err != nil {
		t.Fatalf("cannot read the ledger JSON: %v", err)
	}
	fromJSON, err := i18n.ParseLedger(jsonRaw)
	if err != nil {
		t.Fatalf("cannot decode the ledger JSON: %v", err)
	}
	if len(fromBin.Messages) != len(fromJSON.Messages) {
		t.Fatalf("ledger entry count: bundle %d, JSON %d -- did you change the JSON without running `tools/i18n bundle`?",
			len(fromBin.Messages), len(fromJSON.Messages))
	}
	for id, want := range fromJSON.Messages {
		got, ok := fromBin.Messages[id]
		if !ok || got.One != want.One ||
			got.Other != want.Other || got.Note != want.Note ||
			!slices.Equal(got.Args, want.Args) {
			t.Errorf("ledger %q does not match (bundle behind JSON?)\n  want %+v\n  got %+v", id, want, got)
		}
	}

	// Translations.
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	langs := 0
	for _, e := range ents {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".json") || name == i18n.LedgerName {
			continue
		}
		langs++
		binRaw, err := os.ReadFile(filepath.Join(dir, strings.TrimSuffix(name, ".json")+".bin"))
		if err != nil {
			t.Fatalf("cannot read the %s bundle: %v", name, err)
		}
		binCat, err := i18n.DecodeCatalog(binRaw)
		if err != nil {
			t.Fatalf("cannot decode the %s bundle: %v", name, err)
		}
		raw, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		jsonCat, err := i18n.ParseCatalog(raw)
		if err != nil {
			t.Fatalf("cannot decode %s: %v", name, err)
		}
		if len(binCat.Messages) != len(jsonCat.Messages) {
			t.Fatalf("%s entry count: bundle %d, JSON %d -- did you change the JSON without running `tools/i18n bundle`?",
				name, len(binCat.Messages), len(jsonCat.Messages))
		}
		for id, want := range jsonCat.Messages {
			got, ok := binCat.Messages[id]
			if !ok || (got != want && !(got.Empty() && want.Empty())) {
				t.Errorf("%s / %q does not match (bundle behind JSON?)\n  want %+v\n  got %+v",
					name, id, want, got)
			}
		}
	}
	if langs == 0 {
		t.Fatal("no translations at all -- the check has degraded")
	}
}
