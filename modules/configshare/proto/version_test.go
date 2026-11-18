package proto

import (
	"strings"
	"testing"
)

// TestGateTruthTable pins the two levels' **different natures** - this is not one thing
// at two severities, so their behavior must differ:
//
//   - build string mismatch => allow + warn (parseable today, just written by a newer build)
//   - higher schema         => refuse + keep previous (meanings may have changed, silent)
func TestGateTruthTable(t *testing.T) {
	tests := []struct {
		name       string
		meta       *Meta
		cur        string
		wantOK     bool
		wantWarn   bool
		wantRefuse bool
	}{
		{"no meta: interpret by the current schema", nil, "build-1", true, true, false},
		{"same schema: nothing to say", &Meta{SchemaVersion: 1}, "build-1", true, false, false},
		{"higher schema: refuse", &Meta{SchemaVersion: 2}, "build-1", false, false, true},
		{"schema is 0 (not written): allow + remind", &Meta{}, "build-1", true, true, false},
		{"same build string: nothing to say", &Meta{SchemaVersion: 1, MinNewgateVersion: "build-1"}, "build-1", true, false, false},
		{"different build string: allow + warn", &Meta{SchemaVersion: 1, MinNewgateVersion: "build-2"}, "build-1", true, true, false},
		{"a dev build does not warn (otherwise every dev machine gets noise)", &Meta{SchemaVersion: 1, MinNewgateVersion: "build-2"}, "dev", true, false, false},
		{"no local version string: do not warn", &Meta{SchemaVersion: 1, MinNewgateVersion: "build-2"}, "", true, false, false},
		{
			// `git describe` strings are **not sortable**: build-17-... and build-18-...
			// have no numeric ordering (`--dirty` is worse). So the test can only be
			// equality, which does not earn teeth - it drives a warning only.
			"a dirty suffix is just a different string: still allow",
			&Meta{SchemaVersion: 1, MinNewgateVersion: "build-1-gabc-dirty"},
			"build-1-gabc", true, true, false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ok, warn, refuse := Gate(tc.meta, GateInput{CurrentVersion: tc.cur})
			if ok != tc.wantOK {
				t.Fatalf("ok=%v, want %v (warn=%q refuse=%q)", ok, tc.wantOK, warn, refuse)
			}
			if (warn != "") != tc.wantWarn {
				t.Fatalf("warn=%q, want present=%v", warn, tc.wantWarn)
			}
			if (refuse != "") != tc.wantRefuse {
				t.Fatalf("refuse=%q, want present=%v", refuse, tc.wantRefuse)
			}
			if tc.wantRefuse {
				// The refusal text must say why, since users decide whether to upgrade or
				// fix the host from it. "Version unsupported" alone reads as decorative.
				if !strings.Contains(refuse, "schema_version") {
					t.Fatalf("the refusal message should name schema_version: %q", refuse)
				}
				if !strings.Contains(refuse, "previous configuration is kept") {
					t.Fatalf("the refusal message should say the previous config was kept: %q", refuse)
				}
			}
		})
	}
}

func TestParseMeta(t *testing.T) {
	// Missing file = the host declared nothing -> (nil, nil); Gate then uses the current schema.
	meta, err := ParseMeta(map[string][]byte{"providers.json": []byte("{}")})
	if err != nil || meta != nil {
		t.Fatalf("absence should give (nil, nil), got %v / %v", meta, err)
	}
	// A file that is not JSON: the host wrote something broken, **not** "undeclared". It
	// must error, or it would be read as undeclared under the current schema.
	if _, err := ParseMeta(map[string][]byte{MetaFileName: []byte("this is not JSON")}); err == nil {
		t.Fatal("a corrupt meta file must raise an error, not be treated as \"undeclared\"")
	}
	good := map[string][]byte{MetaFileName: metaFile(3, "build-9")}
	meta, err = ParseMeta(good)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if meta.SchemaVersion != 3 || meta.MinNewgateVersion != "build-9" {
		t.Fatalf("wrong parse result: %+v", meta)
	}
}
