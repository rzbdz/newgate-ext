#!/usr/bin/env bash
# End to end for ssh-tunnel: **a real sshd, real keys, a real handshake**.
#
# # Why this has to exist, and why unit tests cannot stand in for it
#
# There is a dividing line in this module, and the two sides are verified completely differently:
#
#   - **its own** behavior (lazy dial, reuse, idle reaping, path rewrite, two gates) - a fake Dialer
#     is enough, and it runs fast, so those live in *_test.go and run on every commit;
#   - **whether it can talk to a real OpenSSH** - that cannot be faked. The protocol version, key exchange,
#     authentication method, direct-tcpip channel, known_hosts format, every one of them can mismatch on the premise that "the code on my
#     side looks completely correct".
#
# # Why it is not in CI
#
# It depends on the local sshd and port (same as e2e_reasoning_affinity.sh: that one spends real money,
# this one spends the local environment). The in-process real-SSH integration test in CI covers the skeleton of the same thing
# (see ssh_integration_test.go), and this one is the full manual confirmation **on a dev machine**.
#
# Usage: bash mock/e2e_tunnel.sh
#      NEWGATE_E2E_KEEP=1   keep the sandbox (for troubleshooting)
set -u

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
DIST_NAME="${NEWGATE_E2E_DIST:-default}"

case "$(uname -m)" in
  x86_64|amd64) host_arch=amd64 ;;
  aarch64|arm64) host_arch=arm64 ;;
  *) echo "unknown architecture: $(uname -m)" >&2; exit 2 ;;
esac
BIN_SRC="${NEWGATE_E2E_BIN:-$ROOT/dist/newgate-$DIST_NAME-$(uname -s | tr 'A-Z' 'a-z')-$host_arch}"

if [ ! -x "$BIN_SRC" ]; then
  echo "artifact not found: $BIN_SRC" >&2
  echo "build one first: build/build.sh dist" >&2
  exit 2
fi
for tool in sshd ssh-keygen curl python3; do
  command -v "$tool" >/dev/null 2>&1 || { echo "missing $tool" >&2; exit 2; }
done

SANDBOX="${NEWGATE_E2E_SANDBOX:-$(mktemp -d /tmp/newgate-tunnel-e2e.XXXXXX)}"
WEB_PORT="${NEWGATE_E2E_WEB_PORT:-18899}"   # this script's own port, do not fight other e2e's for it
SSH_PORT="${NEWGATE_E2E_SSH_PORT:-12222}"
FWD_PORT="${NEWGATE_E2E_FWD_PORT:-19401}"
PASS=0; FAIL=0
ok(){ PASS=$((PASS+1)); printf '  ✓ %s\n' "$1"; }
bad(){ FAIL=$((FAIL+1)); printf '  ✗ %s\n' "$1"; }
check(){ if [ "$2" = "$3" ]; then ok "$1"; else bad "$1 (wanted [$3], actual [$2])"; fi; }
contains(){ case "$2" in *"$3"*) ok "$1" ;; *) bad "$1 ([$2] does not contain [$3])" ;; esac; }
section(){ printf '\n== %s ==\n' "$1"; }

SRV_PID=""
SSHD_PID=""
WEB_PID=""
cleanup(){
  [ -n "$SRV_PID" ] && kill "$SRV_PID" 2>/dev/null
  [ -n "$SSHD_PID" ] && kill "$SSHD_PID" 2>/dev/null
  [ -n "$WEB_PID" ] && kill "$WEB_PID" 2>/dev/null
  wait 2>/dev/null
  if [ -n "${NEWGATE_E2E_KEEP:-}" ]; then echo "sandbox kept at $SANDBOX"; else rm -rf "$SANDBOX"; fi
}
trap cleanup EXIT

export NEWGATE_HOME="$SANDBOX/ng"
export NEWGATE_LANG=en
unset LC_ALL LC_MESSAGES LANG LANGUAGE

# HOME is fenced into the sandbox too. **This is necessary**: accept-new writes a line into ~/.ssh/known_hosts,
# and this script wants to assert "that line really got written" - without fencing, it writes into the developer's real
# known_hosts, so this test both pollutes that machine and fails to verify what it thinks it verifies.
# The sshd side is unaffected: its config is full of absolute paths.
export HOME="$SANDBOX/home"
mkdir -p "$NEWGATE_HOME/mappings" "$HOME/.ssh"
chmod 700 "$HOME/.ssh"

# ── a real sshd (local, unprivileged port, only accepts this one throwaway key) ─────────────
section "set up a real sshd"

ssh-keygen -q -t ed25519 -N '' -f "$SANDBOX/host_ed25519"
ssh-keygen -q -t ed25519 -N '' -f "$SANDBOX/id_ed25519"
cat > "$SANDBOX/sshd_config" <<EOF
Port $SSH_PORT
ListenAddress 127.0.0.1
HostKey $SANDBOX/host_ed25519
PidFile $SANDBOX/sshd.pid
AuthorizedKeysFile $SANDBOX/authorized_keys
UsePAM no
PasswordAuthentication no
KbdInteractiveAuthentication no
PubkeyAuthentication yes
PermitRootLogin yes
AllowTcpForwarding yes
StrictModes no
LogLevel ERROR
EOF
cp "$SANDBOX/id_ed25519.pub" "$SANDBOX/authorized_keys"
chmod 600 "$SANDBOX/authorized_keys" "$SANDBOX/host_ed25519" "$SANDBOX/id_ed25519"

# This sshd runs as the **current user** (in the sandbox, never touching system config). Under root sshd complains
# about permissions; work around it with StrictModes no - the permission rules in the sandbox are not what is verified here.
"$(command -v sshd)" -f "$SANDBOX/sshd_config" -D -e >"$SANDBOX/sshd.log" 2>&1 &
SSHD_PID=$!
for _ in $(seq 1 50); do
  if (exec 3<>/dev/tcp/127.0.0.1/$SSH_PORT) 2>/dev/null; then exec 3<&- 3>&-; break; fi
  sleep 0.1
done
if ! (exec 3<>/dev/tcp/127.0.0.1/$SSH_PORT) 2>/dev/null; then
  echo "sshd did not start, see $SANDBOX/sshd.log"; cat "$SANDBOX/sshd.log"; exit 2
fi
exec 3<&- 3>&- 2>/dev/null
ok "sshd listening on 127.0.0.1:$SSH_PORT"

# ── the remote "newgate UI": a plain HTTP service, with the same path shape as the real product ───────
section "pretend the remote machine has a newgate UI"

python3 - "$SANDBOX/web" <<'PY' &
import http.server, os, sys
root = sys.argv[1]
os.makedirs(root, exist_ok=True)
with open(os.path.join(root, "index.html"), "w") as f:
    # The absolute prefix /ui/ is **the shape of the real product artifact** (vite's base). What this e2e verifies
    # is exactly that this prefix gets rewritten on the route path and kept as is on the forwarding path.
    f.write('<!doctype html><html><head>'
            '<link rel="icon" href="/ui/favicon.svg">'
            '<script type="module" src="/ui/assets/app.js"></script>'
            '</head><body>REMOTE-INDEX</body></html>')

class H(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        if self.path == "/ui/":
            body = open(os.path.join(root, "index.html"), "rb").read()
            self.send_response(200)
            self.send_header("Content-Type", "text/html; charset=utf-8")
            self.send_header("Content-Length", str(len(body)))
            self.end_headers()
            self.wfile.write(body)
            return
        if self.path == "/ui/api/snapshot":
            body = b'{"who":"remote"}'
            self.send_response(200)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(body)))
            self.end_headers()
            self.wfile.write(body)
            return
        self.send_response(404); self.end_headers()
    def log_message(self, *a): pass

http.server.HTTPServer(("127.0.0.1", 18099), H).serve_forever()
PY
WEB_PID=$!
for _ in $(seq 1 50); do
  curl -sf http://127.0.0.1:18099/ui/ >/dev/null 2>&1 && break
  sleep 0.1
done
curl -sf http://127.0.0.1:18099/ui/ >/dev/null 2>&1 || { echo "fake remote did not start"; exit 2; }
ok "remote UI at 127.0.0.1:18099 (as seen from the remote machine)"

# ── start newgate ────────────────────────────────────────────────────────────
section "start a new gate (sandbox)"

BIN="$SANDBOX/bin/newgate"
mkdir -p "$(dirname "$BIN")"
cp "$BIN_SRC" "$BIN"

# A minimal sandbox config: one provider + one tier + state.json.
# **state.json is required**: without it `/__newgate/status` returns 400 (it cannot read the config),
# and the readiness probe below uses `curl -sf`, so a 400 is taken as "not up yet" - and the script would time out
# on a daemon that has actually been running for a while (measured, hit this).
cat > "$NEWGATE_HOME/providers.json" <<'JSON'
{"providers":{"demo":{"protocol":"anthropic","base_url":"http://127.0.0.1:9/demo","api_key":"sk-not-used-here"}}}
JSON
cat > "$NEWGATE_HOME/mappings/demo.json" <<'JSON'
{"description":"e2e profile","roles":{"normal":[{"provider":"demo","model":"m"}]}}
JSON
python3 -c 'import json,sys; print(json.dumps({"default_profile":"demo","port":int(sys.argv[1])}))' "$WEB_PORT" \
  > "$NEWGATE_HOME/state.json"
# `__serve`: what this script verifies is that route and the forwarding port, so do not mix it up with the test of "how the entry is claimed"
# (same reason as e2e_dashboard.sh).
"$BIN" __serve --port "$WEB_PORT" >"$SANDBOX/newgate.log" 2>&1 &
SRV_PID=$!
for _ in $(seq 1 80); do
  curl -sf "http://127.0.0.1:$WEB_PORT/__newgate/status" >/dev/null 2>&1 && break
  sleep 0.1
done
curl -sf "http://127.0.0.1:$WEB_PORT/__newgate/status" >/dev/null 2>&1 || {
  echo "newgate did not start:"; tail -30 "$SANDBOX/newgate.log"; exit 2
}
ok "newgate on 127.0.0.1:$WEB_PORT"

# ── configure one tunnel ───────────────────────────────────────────────────────────
section "configure a tunnel and get it connected"

"$BIN" tunnel add snode1 \
  --host 127.0.0.1 --port "$SSH_PORT" --user "$(id -un)" \
  --identity "$SANDBOX/id_ed25519" \
  --remote-host 127.0.0.1 --remote-port 18099 \
  --local-port "$FWD_PORT" --persistent --host-key accept-new \
  >"$SANDBOX/add.log" 2>&1
check "exit code of tunnel add" "$?" "0"
contains "add stated the route" "$(cat "$SANDBOX/add.log")" "/ui/remote/snode1/"

# Wait for the daemon's background loop to connect it (the loop is one round every 2 seconds).
connected=0
for _ in $(seq 1 60); do
  if curl -sf "http://127.0.0.1:$FWD_PORT/ui/api/snapshot" 2>/dev/null | grep -q remote; then
    connected=1; break
  fi
  sleep 0.2
done
check "the persistent target connected itself (accept-new also wrote into known_hosts)" "$connected" "1"
# -F: this line is all square brackets ([127.0.0.1]:12222); as a regex it needs escaping, but as a **literal**
# is what is meant here (the local grep is a ugrep wrapper, with different escaping rules too).
if grep -qF "]:$SSH_PORT " "$HOME/.ssh/known_hosts" 2>/dev/null; then
  ok "accept-new recorded the host key into known_hosts"
else
  bad "accept-new did not write known_hosts ($(cat "$HOME/.ssh/known_hosts" 2>/dev/null | head -2))"
fi
contains "accept-new reported a line in the log (trusting a machine is an event)" \
  "$(cat "$SANDBOX/newgate.log")" "recorded the host key"

# ── route one: local forwarding port (faithful) ───────────────────────────────────────────
section "route one: local forwarding port (not a byte is changed)"

body=$(curl -s "http://127.0.0.1:$FWD_PORT/ui/")
contains "the forwarding port gets the remote index" "$body" "REMOTE-INDEX"
# **This one is the whole reason this path exists**: the absolute addresses hard-coded in the remote artifact work as is.
contains "the remote /ui/assets is kept as is (not rewritten)" "$body" 'src="/ui/assets/app.js"'

snap=$(curl -s "http://127.0.0.1:$FWD_PORT/ui/api/snapshot")
contains "the API on the forwarding port works too" "$snap" '"who":"remote"'

# ── route two: the route on the shared port (rewritten) ─────────────────────────────────────
section "route two: /ui/remote/<id>/ on the shared port (rewritten)"

body=$(curl -s "http://127.0.0.1:$WEB_PORT/ui/remote/snode1/")
contains "the route gets the remote index" "$body" "REMOTE-INDEX"
contains "the absolute prefix in the body was rewritten to this route" "$body" 'src="/ui/remote/snode1/assets/app.js"'
contains "favicon was rewritten too" "$body" 'href="/ui/remote/snode1/favicon.svg"'

snap=$(curl -s "http://127.0.0.1:$WEB_PORT/ui/remote/snode1/api/snapshot")
contains "the API on the route works too (the remote receives /ui/api/snapshot)" "$snap" '"who":"remote"'

# ── gates and boundaries ────────────────────────────────────────────────────────────────
section "gates and boundaries"

code=$(curl -s -o /dev/null -w '%{http_code}' -H 'Host: evil.example' "http://127.0.0.1:$WEB_PORT/ui/remote/snode1/")
check "a non-loopback Host is rejected (the DNS rebinding gate)" "$code" "403"

code=$(curl -s -o /dev/null -w '%{http_code}' "http://127.0.0.1:$WEB_PORT/ui/remote/nope/")
check "an unknown id is 404" "$code" "404"
contains "the 404 can say which ids exist" "$(curl -s "http://127.0.0.1:$WEB_PORT/ui/remote/nope/")" "snode1"

code=$(curl -s -o /dev/null -w '%{http_code}' "http://127.0.0.1:$WEB_PORT/ui/remote/snode1")
check "a missing trailing slash is a 302" "$code" "302"

code=$(curl -s -o /dev/null -w '%{http_code}' "http://127.0.0.1:$WEB_PORT/ui/")
check "the local UI is as usual (/ui/ was not stolen)" "$code" "200"

# ── visible on the local UI ────────────────────────────────────────────────────────
section "visible on the local UI"

snap=$(curl -s "http://127.0.0.1:$WEB_PORT/ui/api/snapshot")
contains "the snapshot has this section" "$snap" 'ssh-tunnel'
contains "the config card has this target" "$snap" 'snode1'
contains "the status table has this row" "$snap" 'ssh-tunnel.status'

# ── command line ──────────────────────────────────────────────────────────────────
section "command line"

"$BIN" tunnel ls >"$SANDBOX/ls.log" 2>&1
contains "tunnel ls lists the target" "$(cat "$SANDBOX/ls.log")" "snode1"
contains "tunnel ls gives the two routes" "$(cat "$SANDBOX/ls.log")" "/ui/remote/snode1/"

"$BIN" tunnel test snode1 >"$SANDBOX/test.log" 2>&1
check "exit code of tunnel test" "$?" "0"
contains "test reached the last hop" "$(cat "$SANDBOX/test.log")" "accepts connections"

"$BIN" tunnel rm snode1 >"$SANDBOX/rm.log" 2>&1
check "exit code of tunnel rm" "$?" "0"
"$BIN" tunnel ls >"$SANDBOX/ls2.log" 2>&1
contains "after removal it is gone from ls" "$(cat "$SANDBOX/ls2.log")" "no targets yet"

printf '\nresult: %d passed, %d failed\n' "$PASS" "$FAIL"
[ "$FAIL" -eq 0 ] || exit 1
