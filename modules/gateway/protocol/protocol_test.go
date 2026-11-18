package protocol

import "testing"

func TestNormalizeRoleStripsPrefixAndMarker(t *testing.T) {
	for in, want := range map[string]string{
		"heavy":             "heavy",
		"newgate/heavy":     "heavy",
		"heavy[1m]":         "heavy",
		"newgate/heavy[1m]": "heavy",
		"heavy[1M]":         "heavy",
		"opus[1m]":          "opus",
	} {
		if got := NormalizeRole(in); got != want {
			t.Errorf("NormalizeRole(%q) = %q, want %q", in, got, want)
		}
	}
}
