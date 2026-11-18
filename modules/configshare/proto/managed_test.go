package proto

import (
	paths "github.com/rzbdz/newgate-ext/modules/config/paths"
	"testing"
)

// TestManagedPathAllowlist pins the "single contract" table.
//
// It is the criterion shared by three places (host snapshot, replica validation, drift
// enumeration), so every entry here has a provenance, none is arbitrary.
func TestManagedPathAllowlist(t *testing.T) {
	tests := []struct {
		rel  string
		want bool
		why  string
	}{
		{"providers.json", true, "the core of the managed set"},
		{"newgate-config.json", true, "schema/version declaration, keep a copy too"},
		{"mappings/prod.json", true, "tier"},
		{"mappings/prod.kv", true, "the hand-editable tier form, also managed"},
		{"mappings/a-b_c.1.json", true, "tier names may contain hyphens/underscores/dots"},

		// Pitfall 1: profile kv --write renames the old json to .bak. Outside the managed
		// set, or every machine accumulates junk that is unread and counts as a local edit.
		{"mappings/prod.json.bak", false, "a backup left by the kv conversion, not config"},

		// Pitfall 2: omo-slots.json is watched by roleprov and machine-generated; don't share.
		{"omo-slots.json", false, "a local takeover artifact"},
		{"state.json", false, "machine-local state (including it fights with the watcher)"},
		{"health.json", false, "the breaker health table is runtime data"},
		{"secrets.json", false, "goes over the secrets channel (separate bookkeeping/generation/permissions)"},
		{"thinkcache.bin", false, "reasoning cache"},
		{"probe-capabilities.json", false, "local probe results"},
		{"dump/x.json", false, "wire evidence, never leaves the machine"},
		{"backups/20260917/providers.json", false, "backup directory"},

		// Temp files from atomic writes (ours, or left by others).
		{"providers.json.tmp", false, "write temp files"},
		{".providers.json.tmp", false, "write temp files"},
		{"mappings/.prod.json.tmp", false, "write temp files"},
		{"mappings/.hidden.json", false, "dotfiles are left alone"},

		// Path safety: the payload is from the network; names must not escape the config dir.
		{"../providers.json", false, "escapes the config directory"},
		{"mappings/../../etc/passwd", false, "escapes the config directory"},
		{"/etc/passwd", false, "absolute path"},
		{"mappings/sub/x.json", false, "no more subdirectories under mappings"},
		{"mappings//prod.json", false, "// makes two records look alike in the list"},
		{"mappings/", false, "empty name"},
		{"", false, "empty path"},
		{`mappings\prod.json`, false, "backslash is not a separator on Linux"},
		{"providers.json/", false, "trailing slash"},
	}
	for _, tc := range tests {
		t.Run(tc.rel, func(t *testing.T) {
			got, ok := ManagedPath(tc.rel)
			if ok != tc.want {
				t.Fatalf("ManagedPath(%q) = %v, want %v (%s)", tc.rel, ok, tc.want, tc.why)
			}
			if ok && got != tc.rel {
				t.Fatalf("ManagedPath(%q) normalized to %q - the contract is a slash-relative path, it must not be rewritten", tc.rel, got)
			}
		})
	}
}

func TestListManagedFiles(t *testing.T) {
	sandbox(t)
	// Make a batch of files, half should be listed, half not.
	for _, rel := range []string{
		"providers.json",
		"state.json",     // 不该
		"omo-slots.json", // 不该
		"mappings/prod.json",
		"mappings/prod.json.bak", // 不该
		"mappings/other.kv",
		"mappings/.tmp.json", // 不该
	} {
		writeLive(t, rel, []byte("{}"))
	}

	got, err := ListManagedFiles(paths.Config())
	if err != nil {
		t.Fatalf("enumeration failed: %v", err)
	}
	want := []string{"mappings/other.kv", "mappings/prod.json", "providers.json"}
	if len(got) != len(want) {
		t.Fatalf("enumeration gave %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("enumeration gave %v, want %v (must be sorted so the report is stable)", got, want)
		}
	}
}
