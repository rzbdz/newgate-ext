module github.com/rzbdz/newgate-ext

go 1.27

require (
	github.com/Ikaleio/lm-detector/sdk/go v0.2.0
	github.com/klauspost/compress v1.18.4
	github.com/rzbdz/newgate v0.0.0
	golang.org/x/crypto v0.57.0
	golang.org/x/sys v0.48.0
)

// The kernel is **an ordinary dependency of this repository**, pinned to the commit the
// core/ submodule points at.
//
// Why replace instead of letting Go download a version number: the submodule's gitlink is
// already the single truth of "which kernel", and writing a version number too would record
// the same fact twice -- the two eventually disagree, and the symptom is "what I build
// locally is not what CI builds".
//
// Two more benefits happen to be what this pass wants:
//   - the kernel's tests run inside the tree (that is where the distribution pipeline's
//     core-test step comes from);
//   - the build reads the **working tree**, so the "commit before you can build" friction is gone.
//
// The cost is written down in docs/09-extension-guide.md §8: the only difference between a
// local and a CI build is "is the submodule at that commit", and git guarantees that itself.
replace github.com/rzbdz/newgate => ./core
