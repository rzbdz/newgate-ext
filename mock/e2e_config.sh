#!/usr/bin/env bash
# Zero-token end to end: `newgate config`, the shuttle (the wizard).
#
# What it locks is **those three minutes when the user first gets this machine**: pick a client -> one base/key/model ->
# persist -> install + take over -> start the proxy. So it must use the real binary, real terminal semantics (pty), and run in a
# sealed sandbox (HOME / NEWGATE_HOME / PATH all point at temporary directories).
#
# Use a **fake terminal** (python3's pty) rather than a pipe: every screen of the wizard reads keys in raw mode, and feeding it
# through a pipe takes the "cannot interact on this machine" path - that path has its own assertion (section 0).
#
# Fake upstream: a small HTTP service that only answers `GET /v1/models` (a snippet of python3 http.server
# script). That way the "model list" prompt needs no real upstream, and **burns not a single token**.
#
# Everything in temporary directories, never touching the real ~/.config/newgate, and never really installing anything on this machine.

set -u

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
DIST_NAME="${NEWGATE_E2E_DIST:-default}"
case "$(uname -m)" in
  x86_64)        host_arch=amd64 ;;
  aarch64|arm64) host_arch=arm64 ;;
  *)             host_arch=$(uname -m) ;;
esac
BIN_SRC="${NEWGATE_E2E_BIN:-$ROOT/dist/newgate-$DIST_NAME-$(uname -s | tr 'A-Z' 'a-z')-$host_arch}"
SANDBOX="${NEWGATE_E2E_SANDBOX:-$(mktemp -d /tmp/newgate-config-e2e.XXXXXX)}"
BIN="$SANDBOX/bin/newgate"

PASS=0; FAIL=0
ok()   { echo "  ✓ $1"; PASS=$((PASS+1)); }
bad()  { echo "  ✗ $1"; FAIL=$((FAIL+1)); }
check(){ if [ "$2" = "$3" ]; then ok "$1"; else bad "$1 (expected '$3', actual '$2')"; fi; }

export NEWGATE_HOME="$SANDBOX/ng"
export NEWGATE_LANG=en
unset LC_ALL LC_MESSAGES LANG LANGUAGE 2>/dev/null || true
mkdir -p "$NEWGATE_HOME" "$(dirname "$BIN")"
# **Sealed PATH + fake package managers**: the wizard really does install the selected clients, and this dev machine has
# a real codex (in nvm) and a real hermes. Prepending only guarantees "ours is found first", it does not stop "the real one
# is still there" - and what this e2e verifies is exactly "what is not installed gets installed".
#
# The first version ran into this on 2026-11-17: there was a real curl in PATH, so the wizard ran hermes's
# `curl … | bash` line, and the script sat in the pty waiting for input that never came. It installed nothing (the last step
# is exec), but that command should not have been run once.
FAKEBIN="$SANDBOX/fakebin"
mkdir -p "$FAKEBIN"
export PATH="$FAKEBIN:$(dirname "$BIN"):/usr/bin:/bin"
export HOME="$SANDBOX/home"
mkdir -p "$HOME"

# Fake npm: record argv, then drop a script of the same name into "the bin directory that got installed".
# Real npm puts executables into the prefix's bin; mimic that shape here.
cat > "$FAKEBIN/npm" <<'NPM'
#!/usr/bin/env bash
echo "$@" >> "$FAKE_NPM_LOG"
pkg=""
for a in "$@"; do case "$a" in @*|*-ai) pkg="$a" ;; esac; done
# Package name -> binary name: @openai/codex -> codex, @anthropic-ai/claude-code -> claude
bin=$(printf '%s' "$pkg" | sed 's#^@[^/]*/##; s/-code$//')
[ -n "$bin" ] || exit 1
cat > "$NEWGATE_INSTALL_BIN/$bin" <<SH
#!/usr/bin/env bash
echo "fake $bin: \$*"
SH
chmod +x "$NEWGATE_INSTALL_BIN/$bin"
exit 0
NPM

# Fake curl: only to intercept hermes's `curl … | bash` line. Give it an "install script that does nothing",
# so that command succeeds and touches nothing - this e2e does not verify upstream install scripts' behavior.
cat > "$FAKEBIN/curl" <<'CURL'
#!/usr/bin/env bash
out=""
prev=""
for a in "$@"; do [ "$prev" = "-o" ] && out="$a"; prev="$a"; done
[ -n "$out" ] || exit 1
printf '#!/usr/bin/env bash
exit 0
' > "$out"
CURL
chmod +x "$FAKEBIN/npm" "$FAKEBIN/curl"

export NEWGATE_INSTALL_BIN="$SANDBOX/installed-bin"
mkdir -p "$NEWGATE_INSTALL_BIN"
# Put the "installed" binaries on PATH too (that is how real npm's prefix/bin works).
export PATH="$NEWGATE_INSTALL_BIN:$PATH"
export FAKE_NPM_LOG="$SANDBOX/npm-argv.txt"

[ -x "$BIN_SRC" ] || { echo "binary not found $BIN_SRC - run build/build.sh first"; exit 1; }
cp "$BIN_SRC" "$BIN"

cleanup() {
  "$BIN" stop >/dev/null 2>&1 || true
  [ -n "${FAKE_MODELS_PID:-}" ] && kill "$FAKE_MODELS_PID" 2>/dev/null
  if [ -n "${NEWGATE_E2E_KEEP:-}" ]; then
    echo "sandbox kept (NEWGATE_E2E_KEEP): $SANDBOX"
  else
    rm -rf "$SANDBOX"
  fi
}
trap cleanup EXIT

# ---- fake upstream: only answers GET /v1/models ----
cat > "$SANDBOX/fake_models.py" <<'PY'
import json, sys
from http.server import BaseHTTPRequestHandler, HTTPServer

class H(BaseHTTPRequestHandler):
    def do_GET(self):
        if self.path.rstrip('/').endswith('/models'):
            body = json.dumps({"object": "list", "data": [
                {"id": "e2e-small-model"}, {"id": "e2e-big-model"}]}).encode()
            self.send_response(200)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(body)))
            self.end_headers()
            self.wfile.write(body)
            return
        self.send_response(404); self.end_headers()
    def log_message(self, *a): pass

srv = HTTPServer(("127.0.0.1", int(sys.argv[1])), H)
print(srv.server_address[1], flush=True)
srv.serve_forever()
PY
FAKE_PORT=$(python3 - "$SANDBOX" <<'PY'
import socket, subprocess, sys, os
s = socket.socket(); s.bind(("127.0.0.1", 0)); port = s.getsockname()[1]; s.close()
p = subprocess.Popen([sys.executable, os.path.join(sys.argv[1], "fake_models.py"), str(port)],
                     stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
open(os.path.join(sys.argv[1], "fake_models.pid"), "w").write(str(p.pid))
print(port)
PY
)
FAKE_MODELS_PID="$(cat "$SANDBOX/fake_models.pid")"
for _ in 1 2 3 4 5 6 7 8 9 10; do
  curl -fsS "http://127.0.0.1:$FAKE_PORT/v1/models" >/dev/null 2>&1 && break
  sleep 0.2
done

echo
echo "== 0. when not a terminal: change nothing, explain each point =="
# Only look at **config** files: newgate.log is the assembly trace every process writes (the first thing the core's app.Main
# does), it is not "what the wizard changed", and counting it would only make this assertion look like a failure.
cfg_snapshot() { find "$NEWGATE_HOME" -type f ! -name '*.log' | sort | xargs -r md5sum 2>/dev/null; }
before="$(cfg_snapshot)"
out=$(printf '' | "$BIN" config 2>&1); rc=$?
check "no terminal exits with 64" "$rc" "64"
case "$out" in
  *"interactive terminal"*) ok "it says an interactive terminal is needed";;
  *) bad "it does not say why it cannot run: $out";;
esac
case "$out" in
  *"newgate on"*) ok "it gives the manual steps";;
  *) bad "it does not give the manual steps: $out";;
esac
after="$(cfg_snapshot)"
check "not a single file was written without a terminal" "$after" "$before"

echo
echo "== 1. real wizard (pty): pick a client -> one upstream -> persist =="
# Scripted keystrokes:
#   - the multi-select screen: by default it checks "installed, not taken over". This sandbox's PATH is sealed,
#     no client is installed -> nothing is checked by default -> move to claude, check it with space, then Enter.
#     **Check only one**: this e2e verifies "not installed gets installed, installed gets taken over", and checking more turns the assertion
#     from "claude's shim is there" into "is some row on a screen right", and the latter is already locked by unit tests.
#     The order is claude/codex/hermes/opencode: one move gets to claude.
#   - the three upstream fields: base / key / name (the address points at the fake upstream, so the model list can be asked).
#   - model list: press Enter to pick the first one.
read -r -d '' KEYS_SCRIPT <<'PY' || true
import os, pty, select, sys, time
binp, base, key, name = sys.argv[1:5]
pid, fd = pty.fork()
if pid == 0:
    os.execvpe(binp, [binp, "config", "--no-start"], dict(os.environ, NEWGATE_LANG="en"))
def drain(t):
    end = time.time() + t
    while time.time() < end:
        r, _, _ = select.select([fd], [], [], 0.1)
        if r:
            try: os.read(fd, 65536)
            except OSError: return
drain(1.2)
for chunk, wait in [(" ", 0.4), ("\r", 0.6), (base + "\r", 0.5), (key + "\r", 0.5),
                    (name + "\r", 0.5), ("\r", 1.2)]:
    try: os.write(fd, chunk.encode())
    except OSError: break
    drain(wait)
try:
    os.write(fd, b"\x03")
except OSError:
    pass
drain(0.5)
try: os.waitpid(pid, 0)
except ChildProcessError: pass
PY
out=$(python3 -c "$KEYS_SCRIPT" "$BIN" "http://127.0.0.1:$FAKE_PORT/v1" "sk-e2e" "e2e-upstream" 2>&1); rc=$?

if [ -f "$NEWGATE_HOME/providers.json" ]; then ok "providers.json was written"; else bad "no providers.json (output: $out)"; fi
python3 - "$NEWGATE_HOME" <<'PY'
import json, sys, os
home = sys.argv[1]
provs = json.load(open(os.path.join(home, "providers.json")))["providers"]
p = provs.get("e2e-upstream", {})
assert p.get("base_url", "").startswith("http://127.0.0.1"), p
assert p.get("api_key") == "sk-e2e", p
kv = open(os.path.join(home, "mappings", "default.kv")).read()
for tier in ("heavy", "normal", "mid", "light", "vision"):
    line = [l for l in kv.splitlines() if l.startswith(tier + "=")]
    assert line == [tier + "=e2e-upstream/e2e-big-model"], (tier, line)
state = json.load(open(os.path.join(home, "state.json")))
assert state["default_profile"] == "default", state
PY
check "providers / default.kv / state.json all agree" "$?" "0"

echo
echo "== 2. the model picked from the list really got written =="
case "$(cat "$NEWGATE_HOME/mappings/default.kv")" in
  *"normal=e2e-upstream/e2e-big-model"*) ok "uses the first model in the list (e2e-big-model)";;
  *) bad "the tiers do not point at the model in the list: $(cat "$NEWGATE_HOME/mappings/default.kv")";;
esac
case "$(cat "$NEWGATE_HOME/mappings/default.kv")" in
  *"# Later, point tiers"*) ok "the tier file carries a comment on how to split them later (for humans and AI)";;
  *) bad "the tier file does not explain how to change it";;
esac

echo
echo "== 2b. really installed + really taken over (the ones selected) =="
# The rule is readlink: the shim is a **symlink** pointing at newgate, and `cat` on a link
# reads the binary it points at (so that command substitution is full of ELF bytes).
case "$(readlink "$NEWGATE_HOME/bin/claude" 2>/dev/null)" in
  *newgate*) ok "claude's PATH shim is installed (points at newgate)";;
  *) bad "claude has no shim";;
esac
if [ -f "$NEWGATE_INSTALL_BIN/claude" ]; then ok "the fake npm was called to install claude (this one verifies install-if-not-installed)"; else bad "the install command did not run"; fi
# codex was **not** selected this time, so not a byte of it may be touched - including the fact that its config file
# should not be created at all (codex itself writes it on its first run).
if [ -f "$HOME/.codex/config.toml" ]; then
  bad "codex was not selected, but its config.toml was written/rewritten"
else
  ok "the unselected codex was not touched by a single byte (config.toml was not even created)"
fi
if [ -f "$NEWGATE_HOME/.newgate.pid" ]; then bad "--no-start but the proxy was started anyway"; else ok "--no-start: the proxy was not started"; fi

echo
echo "== 3. idempotent: a second run does not re-ask for the existing config, nor change it =="
before="$(md5sum "$NEWGATE_HOME/mappings/default.kv" | awk '{print $1}')"
# This time no keystroke is given at all (the script Ctrl-Cs immediately). That it finishes means it did **not** ask the upstream screen.
python3 - "$BIN" <<'PY' >/dev/null 2>&1 || true
import os, pty, select, sys, time
pid, fd = pty.fork()
if pid == 0:
    os.execvpe(sys.argv[1], [sys.argv[1], "config", "--no-start"], dict(os.environ, NEWGATE_LANG="en"))
time.sleep(1.0)
try:
    os.write(fd, b"\x03")
except OSError:
    pass
try: os.waitpid(pid, 0)
except ChildProcessError: pass
PY
after="$(md5sum "$NEWGATE_HOME/mappings/default.kv" | awk '{print $1}')"
check "the second run did not touch default.kv" "$after" "$before"

echo
echo "== 4. with a working config already present, the wizard no longer asks for the upstream =="
# Rule: that run above went through without a single keystroke (if it had asked for the upstream, Ctrl-C would have made it do nothing,
# but it would have drawn the "Base URL" screen first). Here we use stiffer evidence on one line: exit code and files unchanged.
python3 - "$BIN" <<'PY'
import os, pty, select, sys, time
pid, fd = pty.fork()
if pid == 0:
    os.execvpe(sys.argv[1], [sys.argv[1], "config", "--no-start"], dict(os.environ, NEWGATE_LANG="en"))
out = b""
end = time.time() + 1.5
while time.time() < end:
    r, _, _ = select.select([fd], [], [], 0.1)
    if r:
        try: out += os.read(fd, 65536)
        except OSError: break
try: os.write(fd, b"\x03")
except OSError: pass
try: os.waitpid(pid, 0)
except ChildProcessError: pass
assert b"Base URL" not in out, out[-800:]
PY
check "the Base URL screen did not appear" "$?" "0"

echo
echo "result: $PASS passed, $FAIL failed"
[ "$FAIL" = "0" ]
