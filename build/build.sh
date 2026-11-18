#!/usr/bin/env bash
# Build a static binary from this distribution. Can build **several configs** (specs) at once -> several binaries.
#
# This is **the only build entry point**: how to build locally, how CI builds, how a forker builds, all the same command.
# It does four things:
#
#   1. check that the core/ submodule is present -- it is the kernel source, and the target of that
#      `replace github.com/rzbdz/newgate => ./core` line in go.mod;
#   2. generate the assembly manifest (manifest/modules_gen.go, from **all** specs at the repository root);
#   3. build one fully static binary per spec per platform;
#   4. copy them to dist/, with a SHA256SUMS alongside.
#
# Usage
#   build/build.sh [output dir]               by default only builds dist.json (what day-to-day development needs)
#   NEWGATE_DISTS="dist.json dist-hello.json" build several specs at once -> several binaries
#   NEWGATE_ALL=1                             every dist*.json at the repository root
#   NEWGATE_DIST=dist-hello.json              just one (= a NEWGATE_DISTS with only that one)
#   NEWGATE_PLATFORMS="linux/amd64 linux/arm64"  platform matrix (provided by CI at release time)
#
# # Why several (2026-09-20)
#
# `dist-hello.json` is the skeleton config "the whole framework + one hello". When debugging the distribution mechanisms themselves,
# building such a binary is far more useful than switching to the `template` branch: switching branches keeps changes on the main branch
# and the skeleton config from being verified at the same time (and "build once on each side after a change" is exactly the step most easily skipped). One extra
# build costs a few seconds, so for local debugging just do `NEWGATE_DISTS="dist.json dist-hello.json"`.
#
# GitHub Release ships only **the flagship one** (the distribution's main config) -- that is the product; the skeleton is a template
# for people to fork and has no reason to hang off the Release. That is decided by release.yml in CI via NEWGATE_DISTS,
# not hardcoded in this script: the script only answers "how to build"; answering "what to ship" is the pipeline's business.
#
# # Artifact names
#
#   newgate-<distribution in the spec>-<platform>-<arch>
#
# For example `newgate-default-linux-amd64`, `newgate-hello-darwin-arm64`. Note that it is **multi-invocation**
# (argv0 decides the entry point: newgate / claude / opencode...), so before running it, drop a copy under the correct name
# (see the README and CLAUDE.md §3).
#
# # How this differs from the version before 2026-09-20
#
# The old version wrote a temporary Pin and ran `cd core && make static` -- the real assembly machinery lived in the kernel
# repository, at the cost of making the kernel checkout dirty every time a distribution was built (the generated manifest was rewritten into the distribution's
# copy), and of building the distribution's committed state (you had to commit before building).
#
# Now the distribution is **its own Go module**: it builds its own main, and the kernel is just one of its dependencies
# (replaced by the submodule). So three things disappear together: the kernel worktree is no longer rewritten, the build reads the
# worktree (no need to commit first), and the distribution's tests and the kernel's tests each run on their own.
set -euo pipefail

here=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
out=${1:-$here/dist}
# Turn the output directory **into an absolute path first**: the script does `cd "$gomod"` partway through, and relative paths after that would point elsewhere.
# Measured in CI on 2026-09-20: `build/build.sh dist` wrote artifacts into `dist/`, while the end-to-end script
# looked for them at the repository root and reported "binary not found" -- the build job was green, so the red appeared one step over.
# Relative paths are interpreted **relative to the caller's current working directory** (not the repository root, and not the script's own directory).
case "$out" in
  /*) : ;;
  *)  out="$(pwd)/$out" ;;
esac
core=$here/core
gomod=$here

# Which ones to build. Priority of the three switches: all > a named few > just one (dist.json by default).
if [ -n "${NEWGATE_ALL:-}" ]; then
  specs=$(cd "$here" && ls dist*.json)
elif [ -n "${NEWGATE_DISTS:-}" ]; then
  specs=${NEWGATE_DISTS}
else
  specs=${NEWGATE_DIST:-dist.json}
fi

if [ ! -f "$core/go.mod" ]; then
  echo "No kernel source in core/. It is a submodule; first:" >&2
  echo "  git submodule update --init --recursive" >&2
  exit 1
fi
for s in $specs; do
  [ -f "$here/$s" ] || { echo "Spec not found: $here/$s" >&2; exit 1; }
done

# The kernel commit **only counts once it is pushed**. The submodule is normally detached: you change things in core/,
# commit, and forget `git push`, and the binary you build is based on a commit that exists only on your machine -- everyone else
# (including CI) gets a pointer to a commit that does not exist, and the distribution's pipeline dies **wholesale**
# at the checkout step: `fatal: remote error: upload-pack: not our ref` (measured 2026-09-20).
#
# Warn only, do not fail: during local development, building from "a kernel commit not yet pushed" is reasonable (that is exactly what you want to try),
# but it must be said -- nobody else can reproduce that artifact, and CI will say so for you the moment it is pushed.
if core_head=$(git -C "$core" rev-parse --verify -q HEAD); then
  if [ -z "$(git -C "$core" for-each-ref --contains "$core_head" --format='%(refname)' refs/remotes/)" ]; then
    {
      echo "Warning: core/ HEAD ${core_head:0:7} is not on any **fetched** remote branch."
      echo "  Either you forgot to push (git -C core push origin main), or this machine has not fetched"
      echo "  (git -C core fetch). Nobody else can reproduce this artifact, and CI will not get past checkout."
    } >&2
  fi
fi

# Put the worktree state into the version. The old version only **warned** when the worktree was dirty, while the binary's version still recorded
# that commit id -- the two stories disagreed. Now it is written into the binary directly: `newgate version` itself says
# "this is dirty", and it cannot lie.
rev=$(git -C "$here" describe --tags --always --dirty 2>/dev/null || echo unknown)
if [ -n "$(git -C "$here" status --porcelain -- go 'dist*.json')" ]; then
  echo "Warning: the distribution has uncommitted changes: the version carries -dirty, and the contents match no commit exactly" >&2
fi

echo "-- kernel: $core @ $(git -C "$core" rev-parse --short HEAD)"
echo "-- distribution specs: $specs"

# The manifest is generated once: distgen scans every spec at the repository root, writing them all in one pass (which one is used is decided at link time).
cd "$gomod"
go run ./tools/distgen

# This distribution's own catalog tables must also be compiled into the one the runtime reads directly (see the kernel's lib/i18n/bundle.go):
# go:embed wants the .bin, so this step must come before compilation. The kernel's copy is handled by the kernel's `make generate`
# (build.sh has already run make above); what is handled here is the handful of tables this distribution ships itself.
#
# extract then bundle (2026-09-21): the ledger is the product of a **purely static scan**, the same kind of thing as the manifest,
# yet it used to be the only one that a human had to remember to regenerate -- forgetting turned the build red, and the red message ("the ledger is stale")
# had nothing to do with the change in hand. Now the build rebuilds it itself and nobody has to remember. The `extract -check`
# in `check-i18n` still catches "regenerated but not committed".
go run github.com/rzbdz/newgate/tools/i18n extract -root . -catalogs modules/i18n/catalogs
go run github.com/rzbdz/newgate/tools/i18n bundle -root . -catalogs modules/i18n/catalogs

mkdir -p "$out"
build_time=$(date '+%Y-%m-%d_%H:%M:%S%z')
commit_time=$(git -C "$here" log -1 --date=format:'%Y-%m-%d_%H:%M:%S%z' --format=%cd 2>/dev/null || echo unknown)

# Build one platform. Linux additionally requires fully static (cross-machine deployment depends on it: snode1 has an old glibc,
# and a dynamically linked binary simply will not start). Darwin does not require it -- "fully static" does not exist on macOS,
# CGO_ENABLED=0 is that guarantee.
build_one() {
  local goos=$1 goarch=$2 artifact=$3 ldflags=$4
  CGO_ENABLED=0 GOOS="$goos" GOARCH="$goarch" go build -trimpath -tags netgo,osusergo \
    -ldflags "$ldflags" -o "$artifact" ./cmd/newgate
}

# Platform matrix: by default only build the local one (what day-to-day development needs). At release time CI supplies a list,
# for example NEWGATE_PLATFORMS="linux/amd64 linux/arm64 darwin/arm64".
host_os=$(uname -s | tr 'A-Z' 'a-z')
# uname -m gives the machine's own naming, Go uses another set: x86_64 → amd64, aarch64 → arm64.
# Without the mapping, **the default path** (no NEWGATE_PLATFORMS) fails outright:
#   go: unsupported GOOS/GOARCH pair linux/x86_64
# measured on 2026-09-20 -- the previous script only used uname -m in file names, so it never showed up.
case "$(uname -m)" in
  x86_64)        host_arch=amd64 ;;
  aarch64|arm64) host_arch=arm64 ;;
  i386|i686)     host_arch=386 ;;
  *)             host_arch=$(uname -m) ;;
esac
platforms=${NEWGATE_PLATFORMS:-"$host_os/$host_arch"}

failed=""
host_artifacts=""
for spec in $specs; do
  dist=$(python3 -c 'import json,sys;print(json.load(open(sys.argv[1]))["distribution"])' "$here/$spec")
  # The distribution in the spec is both **the version prefix** and that segment of the artifact name. They are the same
  # fact, so both are read from the same place -- hand-assembling an artifact name to run the e2e is where they most easily disagree.
  ldflags_common="-s -w -X main.version=$dist-$rev -X main.buildTime=$build_time"
  # The spec name and the source tree are injected into distinfo (not main): modules read them too -- self-update
  # uses them to decide which artifact to download and whether the first install goes through the Release or the local repository (see that package's comments).
  ldflags_common="$ldflags_common -X main.commitTime=$commit_time"
  ldflags_common="$ldflags_common -X github.com/rzbdz/newgate-ext/manifest/distinfo.spec=$spec"
  ldflags_common="$ldflags_common -X github.com/rzbdz/newgate-ext/manifest/distinfo.source=$here"
  for p in $platforms; do
    goos=${p%/*}; goarch=${p#*/}
    artifact="$out/newgate-$dist-$goos-$goarch"
    ldflags="$ldflags_common"
    if [ "$goos" = linux ]; then
      ldflags="$ldflags -extldflags '-static'"
    fi
    if build_one "$goos" "$goarch" "$artifact" "$ldflags"; then
      echo "  ok  $spec  $goos/$goarch  $artifact"
    else
      echo "  --  $spec  $goos/$goarch  cannot build (toolchain does not support this target?)" >&2
      failed="$failed $spec:$goos/$goarch"
      rm -f "$artifact"
    fi
    if [ "$goos" = "$host_os" ] && [ "$goarch" = "$host_arch" ]; then
      host_artifacts="$host_artifacts $artifact"
    fi
  done
done

# Static assertion for the local platform: one broken quote degrades it to dynamic linking, and that symptom only shows up **on another
# machine** -- so go red here instead. (Cross-compiled ones cannot be checked with ldd, so skip those.)
#
# Capture the output first and then decide; do not write `ldd ... | grep -q`: the script runs with pipefail, and ldd on a static
# binary **returns non-zero** ("not a dynamic executable" is its failure output), so the pipeline as a whole
# counts as failed -- judging the only successful cell as a failure. Measured 2026-09-20.
if [ "$host_os" = linux ] && command -v ldd >/dev/null; then
  for artifact in $host_artifacts; do
    [ -x "$artifact" ] || continue
    ldd_out=$(ldd "$artifact" 2>&1 || true)
    case "$ldd_out" in
      *"not a dynamic executable"*|*"statically linked"*) : ;;
      *)
        echo "The local artifact $artifact is not a static binary (it will not start on another machine):" >&2
        echo "$ldd_out" >&2
        exit 1
        ;;
    esac
  done
fi

if [ -n "$failed" ]; then
  echo "These combinations were not built:$failed" >&2
  exit 1
fi

( cd "$out" && sha256sum newgate-* > SHA256SUMS 2>/dev/null || shasum -a 256 newgate-* > SHA256SUMS )
ls -l "$out"
