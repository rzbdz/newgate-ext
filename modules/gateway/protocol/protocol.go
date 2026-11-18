// Package protocol deals with API dialects and model name normalization.
package protocol

import "strings"

// OneMMarker: Claude Code uses the [1m] suffix to declare a 1M-token context capability.
// That is a **client-side capability marker**, not part of the upstream model id, and must be
// stripped before forwarding.
const OneMMarker = "[1m]"

// NormalizeRole pulls the tier name out of the model field a client sent.
//
//	"newgate/heavy"  → "heavy"    strip the virtual provider prefix
//	"heavy[1m]"      → "heavy"    strip the client capability marker
func NormalizeRole(model string) string {
	r := strings.TrimSpace(model)
	if i := strings.LastIndex(r, "/"); i >= 0 {
		r = r[i+1:]
	}
	if strings.HasSuffix(strings.ToLower(r), OneMMarker) {
		r = strings.TrimSpace(r[:len(r)-len(OneMMarker)])
	}
	return r
}
