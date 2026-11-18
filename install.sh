#!/usr/bin/env bash
# One-stop install for newgate: download the static build for this platform from the Release, place it at ~/.local/bin/newgate,
# then tell the user what to type next.
#
#   curl -fsSL https://raw.githubusercontent.com/rzbdz/newgate-ext/main/install.sh | bash
#
# # Why a script (instead of telling users to run gh release download themselves)
#
# "What to do after installing" is the real hurdle: someone handed a fresh machine wants **one command** to finish the job,
# not to first figure out the platform name, then assemble the artifact name, then chmod. The script does three things:
#
#   1. detect platform/arch and pick the matching artifact;
#   2. download + verify against the SHA256SUMS in the Release (**a failed check counts as not downloaded**);
#   3. atomically swap it into the install dir (mv, not overwriting with cp -- a running old process will not be
#      stuck with ETXTBSY), then print a "next step" line.
#
# # Deliberately not done
#
#   - no sudo, no writing to /usr/local/bin (that is a system directory; putting things there needs a human decision);
#   - never edit shell rc files: on most distros ~/.local/bin is already on PATH; when it is not we **say so clearly**
#     and say which line to add, instead of editing a startup script for them (rc edits are the kind of thing that
#     takes effect long after install, and nobody remembers who changed it);
#   - delete nothing (move a same-name file aside as .prev before overwriting it).
#
# Environment variables (for tests and offline environments):
#   NEWGATE_VERSION   the tag to install (default latest)
#   NEWGATE_INSTALL_DIR  install directory (default ~/.local/bin)
#   NEWGATE_REPO      repository (default rzbdz/newgate-ext)
#   NEWGATE_BIN       install this local file directly (skip the download; the e2e uses it)

set -euo pipefail

REPO="${NEWGATE_REPO:-rzbdz/newgate-ext}"
VERSION="${NEWGATE_VERSION:-latest}"
INSTALL_DIR="${NEWGATE_INSTALL_DIR:-$HOME/.local/bin}"
DISTRIBUTION="default"

say()  { printf '%s\n' "$*"; }
die()  { printf 'newgate install: %s\n' "$*" >&2; exit 1; }

need() { command -v "$1" >/dev/null 2>&1 || die "this needs $1 on PATH"; }
need uname
need mkdir

# ---- 1. Platform and architecture ----
case "$(uname -s)" in
  Linux)  os=linux ;;
  Darwin) os=darwin ;;
  *) die "unsupported OS $(uname -s) (newgate ships linux and darwin builds; on Windows use WSL)" ;;
esac
case "$(uname -m)" in
  x86_64|amd64)   arch=amd64 ;;
  aarch64|arm64)  arch=arm64 ;;
  riscv64)        arch=riscv64 ;;
  *) die "unsupported architecture $(uname -m)" ;;
esac
asset="newgate-${DISTRIBUTION}-${os}-${arch}"
say "newgate installer · ${os}/${arch} · ${REPO} · ${VERSION}"

# ---- 2. Fetch the artifact ----
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

if [ -n "${NEWGATE_BIN:-}" ]; then
  [ -f "$NEWGATE_BIN" ] || die "NEWGATE_BIN=$NEWGATE_BIN does not exist"
  cp "$NEWGATE_BIN" "$tmp/$asset"
  say "using local binary $NEWGATE_BIN"
else
  need curl
  if [ "$VERSION" = latest ]; then
    base="https://github.com/${REPO}/releases/latest/download"
  else
    base="https://github.com/${REPO}/releases/download/${VERSION}"
  fi
  say "downloading $asset"
  curl -fsSL --retry 3 --retry-delay 1 -o "$tmp/$asset" "$base/$asset" \
    || die "cannot download $base/$asset (does that release have a $os/$arch build?)"

  # The checksum is a **protocol** (newgate upgrade reads the same file): without it we refuse to install.
  # Why, in one sentence: a binary installed onto PATH whose provenance cannot be verified amounts to handing someone
  # execution rights on a machine; and this check costs a few dozen KB.
  if curl -fsSL --retry 3 -o "$tmp/SHA256SUMS" "$base/SHA256SUMS"; then
    want="$(awk -v f="$asset" '$2 == f { print $1 }' "$tmp/SHA256SUMS")"
    [ -n "$want" ] || die "SHA256SUMS has no entry for $asset"
    if command -v sha256sum >/dev/null 2>&1; then
      got="$(sha256sum "$tmp/$asset" | awk '{print $1}')"
    else
      got="$(shasum -a 256 "$tmp/$asset" | awk '{print $1}')"
    fi
    [ "$got" = "$want" ] || die "checksum mismatch for $asset (got $got, want $want) — refusing to install"
    say "checksum ok"
  else
    die "this release has no SHA256SUMS; refusing to install an unverifiable binary"
  fi
fi

# ---- 3. Install ----
mkdir -p "$INSTALL_DIR"
chmod +x "$tmp/$asset"
if [ -f "$INSTALL_DIR/newgate" ]; then
  mv -f "$INSTALL_DIR/newgate" "$INSTALL_DIR/newgate.prev" || true
  say "previous binary kept at $INSTALL_DIR/newgate.prev"
fi
# mv (rename) rather than cp: a running process holds the inode, and overwriting it gives ETXTBSY.
mv -f "$tmp/$asset" "$INSTALL_DIR/newgate"
say "installed $INSTALL_DIR/newgate"

if ! printf '%s' ":$PATH:" | grep -q ":$INSTALL_DIR:"; then
  say ""
  say "$INSTALL_DIR is not on this shell's PATH. Add it (pick one line for your shell):"
  say "  echo 'export PATH=\"\$HOME/.local/bin:\$PATH\"' >> ~/.bashrc   # bash"
  say "  echo 'export PATH=\"\$HOME/.local/bin:\$PATH\"' >> ~/.zshrc    # zsh"
  say "  fish_add_path \$HOME/.local/bin                                # fish"
  say "Then open a new shell."
fi

say ""
say "Next:"
say "  newgate config     # pick a client, configure the first upstream, all in one go"
