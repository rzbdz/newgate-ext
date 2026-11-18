package verify

import "strings"

// This file compares "who the fingerprint says" with "who the binding says".
//
// # Why strings cannot be compared directly
//
// Bank IDs use dotted versions (`claude-haiku-4.5`, `gpt-5.6-terra`) while upstream model
// names use hyphens (`claude-haiku-4-5`) and often carry a date snapshot suffix
// (`deepseek-v4.1-flash-1017`). Direct comparison reports the same thing as a mismatch - and
// once a warning cries wolf often enough, people learn to ignore it, including a real swap.
//
// So normalize **conservatively** first: letters and digits only, drop separators. The rest
// is graded by "strength of identity" (see api.go's Match):
//
//	identical           match
//	only a 4+ digit extra dated (a date/snapshot build of the same model)
//	same family         family
//	otherwise           mismatch
//
// Requiring at least 4 digits for a date suffix is deliberate: `gpt-5` and `gpt-5.5` normalize
// to `gpt5` and `gpt55`, one extra digit - that is **another model**, not a snapshot build.
// Treating one digit as a date would quietly turn "5.5 posing as 5" into a match.

// Classify gives the relation between requested and predicted. Empty predicted = no ranking.
func Classify(requested, predicted, family string) Match {
	if strings.TrimSpace(predicted) == "" {
		return MatchUnknown
	}
	r, p := normalize(requested), normalize(predicted)
	if r == "" || p == "" {
		return MatchUnknown
	}
	if r == p {
		return MatchOK
	}
	if datedVariant(r, p) {
		return MatchDated
	}
	f := normalize(family)
	// Check only whether **requested** belongs to this family: predicted of course belongs to
	// its own (else the bank would not give the family), so comparing it means "anything matches".
	if f != "" && strings.HasPrefix(r, f) {
		return MatchFamily
	}
	return MatchMismatch
}

// normalize keeps letters and digits only, lowercased.
func normalize(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(s)) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// datedVariant: the shorter is a prefix of the longer, and the extra part is all digits, 4+.
func datedVariant(a, b string) bool {
	short, long := a, b
	if len(short) > len(long) {
		short, long = long, short
	}
	if !strings.HasPrefix(long, short) {
		return false
	}
	extra := long[len(short):]
	if len(extra) < 4 {
		return false
	}
	for _, r := range extra {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
