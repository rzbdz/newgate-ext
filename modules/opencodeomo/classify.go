package opencodeomo

import "strings"

// ClassifyModel classifies a real model name into a capability tier.
//
// This is the tier heuristic described in docs/07-clients-runtime.md. Every hit is recorded so the
// user can check it -- **a silent mismatch is worse than a failure**: running a task that should be
// heavy on the light tier, the user only feels that the model got dumber today, with no way to
// trace it.
func ClassifyModel(s string) (tier string, exact bool) {
	m := strings.ToLower(s)
	if i := strings.LastIndex(m, "/"); i >= 0 {
		m = m[i+1:]
	}
	switch {
	case strings.Contains(m, "fable"):
		return "heavy", true
	case strings.Contains(m, "opus"), strings.Contains(m, "-sol"),
		strings.Contains(m, "flagship"):
		// Four tiers (2026-09-16): opus drops from the top tier to the **main tier** normal, and the
		// top is left for fable. Third-party synonyms for "the vendor's strongest" like "-sol" /
		// "flagship" move with opus -- they were classified by opus's build in the first place, and a
		// tier rename should not change the build.
		return "normal", true
	case strings.Contains(m, "gemini") && strings.Contains(m, "pro"),
		strings.Contains(m, "vision"):
		return "vision", true
	case strings.Contains(m, "sonnet"), strings.Contains(m, "terra"),
		strings.Contains(m, "medium"), strings.Contains(m, "-large"):
		return "mid", true
	case strings.Contains(m, "luna"), strings.Contains(m, "haiku"),
		strings.Contains(m, "flash"), strings.Contains(m, "lite"),
		strings.Contains(m, "mini"), strings.Contains(m, "small"):
		return "light", true
	}
	return "mid", false // cannot tell: fall back to mid, flagged inexact so the caller must log it
}
