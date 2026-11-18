// Package durarg handles one thing: durations typed by the user -- parsing and echoing.
//
// Why a separate package instead of leaving it inside some command: `newgate
// naked 30s` and `newgate plugin <path> off 2m` are commands from **different modules**,
// but the time formats they accept must be the same set -- otherwise a user learns one,
// gets rejected on the other, and thinks they misremembered. Placing it under lib/ is
// the only spot that satisfies both "both sides use the same copy" and "neither knows the
// other": it is a pure utility with no newgate business concept and no module dependency.
//
// Not a module (does not violate everything is module): it has no lifecycle,
// no dependencies, no state, and is not pluggable; it is just a function library.
// The assembly manifest scans `modules/`, and this was never on that graph anyway.
package durarg

import (
	"fmt"
	"strings"
	"time"
)

// Parse turns user input into a time.Duration.
//
// Besides what time.ParseDuration accepts (30s / 2m / 1h), it tolerates two colloquial suffixes:
// "2min" / "5mins". Same reason as above -- users write it that way, and rejecting it buys nothing.
func Parse(s string) (time.Duration, error) {
	if d, err := time.ParseDuration(s); err == nil {
		return d, nil
	}
	norm := s
	switch {
	case strings.HasSuffix(norm, "mins"):
		norm = norm[:len(norm)-3]
	case strings.HasSuffix(norm, "min"):
		norm = norm[:len(norm)-2]
	}
	return time.ParseDuration(norm)
}

// Format renders seconds as a short human string (`45s` / `2m30s` / `1h20m`).
//
// The cap is hours: these durations are all "wait a bit and it recovers" switch
// windows, nothing spans days, and forcing a `d` in would only blow up the width.
func Format(sec int) string {
	d := time.Duration(sec) * time.Second
	if d < time.Minute {
		return d.String()
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm%ds", int(d.Minutes()), int(d.Seconds())%60)
	}
	return fmt.Sprintf("%dh%dm", int(d.Hours()), int(d.Minutes())%60)
}
