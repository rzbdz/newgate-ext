package config

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/rzbdz/newgate-ext/lib/view"
	"github.com/rzbdz/newgate-ext/modules/config/paths"
	"github.com/rzbdz/newgate-ext/modules/config/store"
	"github.com/rzbdz/newgate-ext/testing/testkit"
)

// The source-text column (`kind: code`)'s data **must carry the baseline**.
//
// This is the scene from 2026-09-20: saving a kv source-text edit **always failed**,
// while editing controls worked fine. The root cause was fileData missing the `base`
// field -- the UI handed back an empty string, and the backend read the empty string as
// "it did not exist when I loaded", while the file plainly was on disk, so the CAS judged
// it stale on the spot and reported "this file was changed by someone else after the page
// loaded". The controls half always carried base, so only the source-text half was broken.
//
// One assertion is enough: writing with the base this concept reports itself must succeed.
func TestTheRawHalfCarriesItsBaseline(t *testing.T) {
	testkit.Sandbox(t)
	abs := seedFile(t, "demo.kv", "desc=demo\nnormal=p/m\n")

	var code view.Concept
	for _, c := range fileConcepts() {
		if c.ID == "config.file.mappings/demo.kv" {
			code = c
		}
	}
	if code.ID == "" {
		t.Fatal("the raw-text card for mappings/demo.kv was not reported")
	}
	data, ok := code.Data.(fileData)
	if !ok {
		t.Fatalf("Data is not fileData, it is %T", code.Data)
	}
	if data.Base == "" {
		t.Fatal("the raw-text half carries no baseline -- the UI hands back an empty string, the backend reads that as \"the file does not exist\", and the save is guaranteed to be judged stale")
	}
	if want := store.Revision(abs); data.Base != want {
		t.Errorf("the baseline does not match the one on disk:\n  the card says %s\n  the disk says %s", data.Base, want)
	}
	if code.Apply == nil {
		t.Fatal("the raw-text card should be writable (not every one of them carries a credential)")
	}

	// Write once with the baseline it reports itself -- must succeed. That is the user's action.
	body, _ := json.Marshal(map[string]string{"text": "desc=edited\n"})
	if _, err := code.Apply(body, data.Base); err != nil {
		t.Fatalf("saving the raw text with the baseline the card itself reported failed: %v", err)
	}
	got, _ := os.ReadFile(abs)
	if string(got) != "desc=edited\n" {
		t.Errorf("the raw text was not written: %q", got)
	}
	// After someone else got ahead, it must still be blocked (the baseline is not decoration).
	if _, err := code.Apply(body, data.Base); err == nil {
		t.Error("a stale baseline was allowed to write? two changes would silently overwrite each other")
	}
	_ = paths.Mappings()
}
