// Package distinfo is the leaf package for "which
// distribution this binary is, and where it came from".
//
// Three facts, all settled at build time:
//
//   - which spec was chosen (spec) -- injected by build/build.sh with `-ldflags
//     -X github.com/rzbdz/newgate-ext/manifest/distinfo.spec=<filename>`;
//   - what that spec is called and which GitHub repo it ships from --
//     generated from dist*.json into specs_gen.go by tools/distgen;
//   - where the source tree that built it is (source) -- also injected by build.sh.
//
// # Why it is a separate leaf package
//
// What reads it is **modules** (self-update has to know which artifact to download and which repo
// to build from), while the manifest package imports every module -- a module importing manifest
// would be a compile cycle. This package imports nothing from this repo, so anyone can read it.
//
// # Why the spec injection point is here and not in main
//
// Before 2026-10-29 it was `-X main.spec`: main read it and handed it to manifest.Loader. Modules
// could never read "which spec I am", and could only guess distribution from the version prefix
// -- which is one segment of the artifact name, and guessing wrong downloads the wrong artifact.
// Now there is one injection point and both sides read it (main takes it from here too).
package distinfo

// Injected with -ldflags -X (see build/build.sh). Empty = `go run` / `go test`.
var (
	spec   string
	source string
)

// Info is a spec's identity.
type Info struct {
	// Spec is the spec file name (dist.json / dist-dev.json).
	Spec string
	// Distribution is distribution from the spec: the prefix of the artifact name and version.
	Distribution string
	// GitHub is the release repo (`owner/repo`); empty = this spec does not declare where it ships.
	GitHub string
}

// Spec is the spec file name chosen for this build; when not injected, DefaultSpec.
func Spec() string {
	if spec == "" {
		return DefaultSpec
	}
	return spec
}

// Current is the identity of this binary's spec. When the spec name
// is not in the table (which should not happen: build.sh only injects file names
// that exist), only the file name comes back and the rest is empty -- the caller
// reports "unknown" from that rather than guessing an artifact name to download.
func Current() Info {
	if i, ok := specs[Spec()]; ok {
		return i
	}
	return Info{Spec: Spec()}
}

// Lookup looks up a spec's identity by file name.
func Lookup(name string) (Info, bool) {
	i, ok := specs[name]
	return i, ok
}

// Source is the source tree that built this binary (the
// absolute path of the repo root); empty when not injected.
//
// It is only a **clue**: an artifact built in CI carries the runner's path, which does
// not exist on another machine; a locally built artifact carries the developer's checkout.
// self-update uses it to decide "was the first install on this machine built here",
// and always verifies first that the directory really exists and really is this repo.
func Source() string { return source }
