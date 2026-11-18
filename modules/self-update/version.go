package selfupdate

import (
	"regexp"
	"strconv"
	"strings"
)

// Shape from build/build.sh: `<distribution>-<git describe --tags --always --dirty>`,
// e.g. `default-v0.8.0-40-g59f5b3c-dirty`. A Release tag is the bare `v0.8.0`.
//
// Comparison answers only "swap or not": swap only if the Release is newer (no rollback
// without --force). A dev binary is often **newer than the latest Release**
// (`v0.8.0-40-g...` is 40 commits after v0.8.0), so the describe commit count must
// count - comparing only v0.8.0 calls them "same" and swaps the dev machine back.

var (
	describeTail = regexp.MustCompile(`-(\d+)-g([0-9a-f]{4,})$`)
	semverCore   = regexp.MustCompile(`^v?(\d+)\.(\d+)\.(\d+)(?:-([0-9A-Za-z.-]+))?$`)
)

type version struct {
	major, minor, patch int
	pre                 string // the rc1 of v0.9.0-rc1
	ahead               int    // commit count from describe (how many commits past the tag)
	hash                string
	dirty               bool
}

// parseVersion parses a tag or describe string (optionally with a distribution prefix).
// When unrecognizable (`dev`, a commit-only describe) ok is false: no comparing that.
func parseVersion(s, distribution string) (version, bool) {
	var v version
	if distribution != "" {
		s = strings.TrimPrefix(s, distribution+"-")
	}
	if strings.HasSuffix(s, "-dirty") {
		v.dirty = true
		s = strings.TrimSuffix(s, "-dirty")
	}
	if m := describeTail.FindStringSubmatch(s); m != nil {
		v.ahead, _ = strconv.Atoi(m[1])
		v.hash = m[2]
		s = s[:len(s)-len(m[0])]
	}
	m := semverCore.FindStringSubmatch(s)
	if m == nil {
		return v, false
	}
	v.major, _ = strconv.Atoi(m[1])
	v.minor, _ = strconv.Atoi(m[2])
	v.patch, _ = strconv.Atoi(m[3])
	v.pre = m[4]
	return v, true
}

// compare returns -1/0/1. A prerelease sorts below the same-numbered release (semver);
// after one tag, more commits is newer. dirty does not count: it says "tree changed".
func compare(a, b version) int {
	for _, d := range []int{a.major - b.major, a.minor - b.minor, a.patch - b.patch} {
		if d != 0 {
			return sign(d)
		}
	}
	switch {
	case a.pre == "" && b.pre != "":
		return 1
	case a.pre != "" && b.pre == "":
		return -1
	case a.pre != b.pre:
		if a.pre < b.pre {
			return -1
		}
		return 1
	}
	return sign(a.ahead - b.ahead)
}

func sign(d int) int {
	switch {
	case d < 0:
		return -1
	case d > 0:
		return 1
	}
	return 0
}
