#!/usr/bin/env bash
# Zero-token end to end: **the question asked when a client is not installed, and the best-effort install that follows**.
#
# It locks the only place on this path where "we go and change this machine" -- running an external command to install a
# tool. So it has two properties that must not break, one section each:
#
#   1  **Not installing really means not installing**: after answering n, that sentence must carry **the full command** (the user
#      must be able to copy it and run it), and the tool **really must still be absent** -- "asked once, then secretly installed" is worse than
#      not asking;
#   2  **A failed install must be said clearly**: failing to install (no npm / no network / no permission) is not a fault,
#      but it **must not look like it succeeded**. Verify the two failure modes separately: the command fails, and the command succeeds but the
#      process's PATH still cannot find it (the package manager's bin directory is not on that PATH, which is common).
#
# Use a **fake npm**: we never really install anything on this machine. The fake npm only writes into the sandbox.
#
# Everything runs in a temporary sandbox, touching neither the real ~/.config nor the real PATH.

set -u

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
DIST_NAME="${NEWGATE_E2E_DIST:-default}"
case "$(uname -m)" in
  x86_64)        host_arch=amd64 ;;
  aarch64|arm64) host_arch=arm64 ;;
  i386|i686)     host_arch=386 ;;
  *)             host_arch=$(uname -m) ;;
esac
BIN_SRC="${NEWGATE_E2E_BIN:-$ROOT/dist/newgate-$DIST_NAME-$(uname -s | tr 'A-Z' 'a-z')-$host_arch}"
SANDBOX="${NEWGATE_E2E_SANDBOX:-$(mktemp -d /tmp/newgate-install-e2e.XXXXXX)}"
BIN="$SANDBOX/bin/newgate"
FAKEBIN="$SANDBOX/fakebin"     # the fake npm lives here
INSTALLED="$SANDBOX/bin"       # the "installed" codex lands here (on PATH)
OFFPATH="$SANDBOX/not-on-path" # the kind that installs outside PATH

PASS=0; FAIL=0
ok()   { echo "  ✓ $1"; PASS=$((PASS+1)); }
bad()  { echo "  ✗ $1"; FAIL=$((FAIL+1)); }
check(){ if [ "$2" = "$3" ]; then ok "$1"; else bad "$1 (expected '$3', got '$2')"; fi; }

export NEWGATE_HOME="$SANDBOX/ng"
export NEWGATE_LANG=en
unset LC_ALL LC_MESSAGES LANG LANGUAGE 2>/dev/null || true
mkdir -p "$NEWGATE_HOME/mappings" "$(dirname "$BIN")" "$FAKEBIN" "$OFFPATH"
# **PATH is sealed**: only the two sandbox directories plus the two most basic system ones.
#
# Why we cannot just **prepend** our own directory: this development machine has a real codex (in nvm's bin)
# and a real npm, and prepending only guarantees "ours is found first", while hiding "the real one is still there" --
# and the **entire premise** of this e2e is that the tool is not there. The first version was written that way, so
# `Installed()` was true from the start, the install path was never taken even once, and the failure looked like "the real codex
# complains that -y is not recognized" -- an error with nothing to do with installing. Measured (2026-09-21):
# `command -v codex` → `~/.nvm/versions/node/v24.13.0/bin/codex`。
#
# After sealing, the cat / rm / chmod / dirname this script uses itself are still there (all in /usr/bin or
# /bin), and bash is in /bin -- so this is transparent to the script itself.
#
# **But /usr/bin has a real npm** (the system node's copy, with prefix /usr). Measured 2026-11-17:
# without the HOME isolation layer, codex's facts found codex in the real ~/.nvm,
# so the "not installed" path was never taken even once; and one run of the wizard e2e ran the real
# `npm install -g` into /usr/lib/node_modules and installed a
# /usr/bin/codex on this machine. That is something a test should never have been able to do.
#
# So HOME is pointed into the sandbox as well: the client modules' facts look in ~/.nvm / ~/.local/bin
# (that is their knowledge, see modules/codex's facts.go), and "is it on this machine" must be decided by **the test**
# in a test.
export HOME="$SANDBOX/home"
mkdir -p "$HOME"
export PATH="$FAKEBIN:$SANDBOX/bin:/usr/bin:/bin"

cleanup() {
  # **Stop the daemon first, then delete the directory**. Section 5 starts a client, and `newgate <agent>` **brings the proxy up
  # along the way** when it is not running -- that process outlives the script and keeps writing into NEWGATE_HOME,
  # so `rm -rf` hits "directory not empty" (measured 2026-09-21; the first version went red exactly like this).
  # Stop it via `newgate stop`: the pidfile and graceful exit are its own business, more robust than killing a pid.
  "$BIN" stop >/dev/null 2>&1 || true
  if [ -n "${NEWGATE_E2E_KEEP:-}" ]; then
    echo "Sandbox kept (NEWGATE_E2E_KEEP): $SANDBOX"
  else
    rm -rf "$SANDBOX"
  fi
}
trap cleanup EXIT

echo "Sandbox: $SANDBOX"
[ -x "$BIN_SRC" ] || { echo "Binary not found: $BIN_SRC -- run build/build.sh first"; exit 1; }
cp "$BIN_SRC" "$BIN"

cat > "$NEWGATE_HOME/providers.json" <<'JSON'
{"providers":{"demo":{"protocol":"anthropic","base_url":"http://127.0.0.1:9/demo","api_key":"sk-e2e"}}}
JSON
cat > "$NEWGATE_HOME/mappings/demo.json" <<'JSON'
{"description":"install e2e","roles":{"normal":[{"provider":"demo","model":"demo-model"}]}}
JSON
printf '%s' '{"default_profile":"demo"}' > "$NEWGATE_HOME/state.json"

# Fake npm: record argv (below we assert "the command that ran is the one it said"), then
# decide where to install based on FAKE_NPM_MODE. The real npm's install puts executables into the prefix's
# bin, so mimic that shape here.
cat > "$FAKEBIN/npm" <<'NPM'
#!/usr/bin/env bash
echo "$@" > "$FAKE_NPM_LOG"
case "${FAKE_NPM_MODE:-ok}" in
  ok)
    cat > "$FAKE_NPM_TARGET/codex" <<'CODEX'
#!/usr/bin/env bash
echo "fake codex ran with: $*"
CODEX
    chmod +x "$FAKE_NPM_TARGET/codex"
    ;;
  fail)
    echo "npm ERR! network timeout" >&2
    exit 1
    ;;
  offpath)
    cat > "$FAKE_NPM_TARGET/codex" <<'CODEX'
#!/usr/bin/env bash
echo "fake codex ran with: $*"
CODEX
    chmod +x "$FAKE_NPM_TARGET/codex"
    ;;
esac
exit 0
NPM
chmod +x "$FAKEBIN/npm"
export FAKE_NPM_LOG="$SANDBOX/npm-argv.txt"

echo
echo "== 1. Answer n: say it clearly, but **really do not install** =="
out=$(printf 'n\n' | "$BIN" codex 2>&1); rc=$?
check "exits 0 when not installed (the user's own choice, not an error)" "$rc" "0"
case "$out" in
  *"npm install --global @openai/codex"*) ok "on refusal it gave the full command (ready to copy and run)";;
  *) bad "on refusal it did not say what to run: $out";;
esac
if [ -x "$INSTALLED/codex" ]; then bad "answered n, but codex was installed anyway"; else ok "answered n, codex really was not installed"; fi
if [ -f "$FAKE_NPM_LOG" ]; then bad "answered n, but the fake npm was still called"; else ok "answered n, the install command never ran"; fi

echo
echo "== 2. -y: really install, and carry on afterwards =="
export FAKE_NPM_MODE=ok FAKE_NPM_TARGET="$INSTALLED"
out=$("$BIN" codex -y 2>&1); rc=$?
check "the command that ran is the one it said" "$(cat "$FAKE_NPM_LOG" 2>/dev/null)" "install --global @openai/codex"
if [ -x "$INSTALLED/codex" ]; then ok "the installed codex is on PATH"; else bad "codex was not installed"; fi
case "$out" in
  *"running: npm install --global @openai/codex"*) ok "it said what it was about to run before installing (no silent external commands)";;
  *) bad "it ran without saying what it would run: $out";;
esac
# `-y` is **our** flag and must not be passed through to the client.
case "$out" in
  *"-y"*) bad "-y was passed through to the client: $out";;
  *) ok "-y was stripped and not passed through to the client";;
esac

echo
echo "== 3. Install fails (the command itself fails): report it, exit code means unavailable =="
rm -f "$INSTALLED/codex" "$FAKE_NPM_LOG"
export FAKE_NPM_MODE=fail FAKE_NPM_TARGET="$INSTALLED"
out=$("$BIN" codex -y 2>&1); rc=$?
check "install failure exits 69" "$rc" "69"
case "$out" in
  *"install failed"*"npm install --global @openai/codex"*) ok "the failure names which command it was";;
  *) bad "the install failure did not say which command: $out";;
esac
# The install command's **own** output must be visible too. It is printed live to stderr (bypassing us),
# so the criterion is "visible when combined", not "stuffed into the error string" -- the first version was written the latter
# way, so it went red even though the text was visible.
case "$out" in
  *"npm ERR! network timeout"*) ok "the install command's own error is visible as is (not swallowed)";;
  *) bad "the install command's error was swallowed: $out";;
esac
if [ -x "$INSTALLED/codex" ]; then bad "the install failed but there is a codex sitting there"; else ok "the install failed, and codex really is not there"; fi

echo
echo "== 4. Installed but not on PATH: **do not pretend it succeeded** =="
rm -f "$FAKE_NPM_LOG"
export FAKE_NPM_MODE=offpath FAKE_NPM_TARGET="$OFFPATH"
out=$("$BIN" codex -y 2>&1); rc=$?
check "exits 69" "$rc" "69"
case "$out" in
  *"still not on PATH"*) ok "it said clearly: installed, but this shell cannot find it, open a new shell";;
  *) bad "it did not say clearly when installing outside PATH: $out";;
esac

echo
echo "== 5. Already installed: **do not touch one word of its command line** =="
#
# This is the other half of section 2, and the real boundary of this design (see the runLaunch comment in launch.go):
# newgate only intervenes when "this tool is not on this machine" -- ask once, install, then start as usual. When the tool
# **is** there it is a transparent launcher, and argv passes through as is.
#
# Why this deserves its own section: `-y` is **our** flag, but it is only stripped on the "not installed" path.
# When it is already installed, it is handed to the client as is (measured 2026-09-21: the real codex reports
# `unexpected argument '-y' found`). It looks like a bug, but it is a **corollary** of the rule above:
# "do not touch its command line" and "strip our flags" cannot both hold. So this pins the behavior
# rather than changing it: changing it means changing the rule, and that starts with working out "which flags are newgate's".
cat > "$INSTALLED/codex" <<'CODEX'
#!/usr/bin/env bash
echo "codex argv: $*"
CODEX
chmod +x "$INSTALLED/codex"
out=$("$BIN" codex -y --model gpt-5 2>&1)
case "$out" in
  *"codex argv: -y --model gpt-5"*) ok "when the tool is there, argv is handed over as is (-y too)";;
  *) bad "argv was changed by us: $out";;
esac

echo
echo "Result: $PASS passed, $FAIL failed"
[ "$FAIL" = "0" ]
