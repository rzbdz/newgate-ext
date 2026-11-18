#!/usr/bin/env bash
# Zero-token end to end: **the browser's write path** (snapshot -> save -> conflict -> two gates).
#
# It locks down the only piece of safety semantics in this product where "two writers change the same file": the command line and the browser
# can both change config. Every browser save carries **the baseline from load time**, so a file the command line changed
# while you were looking at the page must become a **conflict** (both sides' original text laid out for the user to pick), not be
# overwritten - when this rule was set on 2026-09-20 the user's own words were "all changes go through snapshot plus detection,
# that is the safest".
#
# Three things, one section each:
#   1  the **shape** of the save path: with the right baseline it writes through, and afterwards the file really changed;
#   2  **conflict**: stale baseline -> 409, both sides' text is there, and **not a single byte on disk was overwritten**;
#   3  **two gates**: a write must declare application/json (blocks cross-site forms), and it only answers a loopback Host
#      (blocks DNS rebinding).
#
# No fake upstream: what is verified here is the control plane, and not a single request goes out. So it is faster than the other e2e's,
# and it does not need core/'s fake upstream either (it still needs a **built** binary).
#
# Everything runs in a temporary sandbox, never touching the real ~/.config.

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
SANDBOX="${NEWGATE_E2E_SANDBOX:-$(mktemp -d /tmp/newgate-dash-e2e.XXXXXX)}"
# Ports are spaced away from the other two e2e's: all three can run at once (handy when debugging side by side).
PORT="${NEWGATE_E2E_WEB_PORT:-18898}"
BIN="$SANDBOX/bin/newgate"

PASS=0; FAIL=0
ok()   { echo "  ✓ $1"; PASS=$((PASS+1)); }
bad()  { echo "  ✗ $1"; FAIL=$((FAIL+1)); }
check(){ if [ "$2" = "$3" ]; then ok "$1"; else bad "$1 (expected '$3', actual '$2')"; fi; }

export NEWGATE_HOME="$SANDBOX/ng"
export NEWGATE_LANG=en
unset LC_ALL LC_MESSAGES LANG LANGUAGE 2>/dev/null || true
mkdir -p "$NEWGATE_HOME/mappings" "$(dirname "$BIN")"

cleanup() {
  [ -n "${SRV_PID:-}" ] && kill "$SRV_PID" 2>/dev/null
  if [ -n "${NEWGATE_E2E_KEEP:-}" ]; then
    echo "sandbox kept (NEWGATE_E2E_KEEP): $SANDBOX"
  else
    rm -rf "$SANDBOX"
  fi
}
trap cleanup EXIT

echo "sandbox: $SANDBOX"
[ -x "$BIN_SRC" ] || { echo "binary not found $BIN_SRC - run build/build.sh first"; exit 1; }
cp "$BIN_SRC" "$BIN"

# ---- sandbox config: one provider + one profile ----
cat > "$NEWGATE_HOME/providers.json" <<'JSON'
{"providers":{"demo":{"protocol":"anthropic","base_url":"http://127.0.0.1:9/demo","api_key":"sk-secret-must-not-leak"}}}
JSON
cat > "$NEWGATE_HOME/mappings/demo.json" <<'JSON'
{"description":"e2e profile","roles":{"normal":[{"provider":"demo","model":"before"}]}}
JSON
STATE="$(python3 -c 'import json;print(json.dumps({"default_profile":"demo","port":int("'"$PORT"'")}))')"
printf '%s' "$STATE" > "$NEWGATE_HOME/state.json"

# ---- start the service ----
# No arguments plus only its own flag also counts as claimed (see gateway's serveEntry), but here we use
# `__serve` explicitly: the script verifies the control plane, so do not mix it up with the test for "how the entry is claimed".
"$BIN" __serve --port "$PORT" >"$SANDBOX/serve.log" 2>&1 &
SRV_PID=$!
for _ in $(seq 1 50); do
  curl -sf -o /dev/null "http://127.0.0.1:$PORT/ui/api/snapshot" && break
  sleep 0.1
done
if ! curl -sf -o /dev/null "http://127.0.0.1:$PORT/ui/api/snapshot"; then
  echo "service did not start, log:"; tail -20 "$SANDBOX/serve.log"; exit 1
fi

API="http://127.0.0.1:$PORT/ui/api"
PROFILE_FILE="$NEWGATE_HOME/mappings/demo.json"

# ---- 1. Snapshot: the writable card carries a baseline ----
echo
echo "== 1. snapshot =="
SNAP="$SANDBOX/snapshot.json"
curl -sf "$API/snapshot" > "$SNAP"
read -r BASE1 HAS_PROFILE <<EOF
$(python3 - "$SNAP" <<'PY'
import json,sys
d=json.load(open(sys.argv[1]))
c=[x for x in d["concepts"] if x["id"]=="config.profile.demo"]
print((c[0]["data"]["base"] if c else ""), "yes" if c else "no")
PY
)
EOF
check "snapshot has a writable profile card" "$HAS_PROFILE" "yes"
[ -n "$BASE1" ] && ok "card carries a baseline ($BASE1)" || bad "card has no baseline - no baseline means no conflict detection"

# ---- 2. It writes through, and the file really changed ----
echo
echo "== 2. save (correct baseline) =="
CODE=$(curl -s -o "$SANDBOX/apply1.json" -w '%{http_code}' -X POST "$API/apply" \
  -H 'Content-Type: application/json' \
  -d "{\"id\":\"config.profile.demo\",\"base\":\"$BASE1\",\"edit\":{\"roles\":{\"normal\":[{\"provider\":\"demo\",\"model\":\"after\"}]}}}")
check "save returned 200" "$CODE" "200"
BASE2=$(python3 -c 'import json;print(json.load(open("'"$SANDBOX/apply1.json"'")).get("base",""))')
[ -n "$BASE2" ] && [ "$BASE2" != "$BASE1" ] && ok "returned a new baseline (the frontend can keep editing without reloading)" \
  || bad "no new baseline returned (or it is the same as the old one)"
if grep -q '"after"' "$PROFILE_FILE"; then ok "the file on disk really changed"; else bad "file did not change"; fi

# ---- 3. Stale baseline = conflict, and **not one byte is overwritten** ----
echo
echo "== 3. conflict (write again with the old baseline) =="
CODE=$(curl -s -o "$SANDBOX/apply2.json" -w '%{http_code}' -X POST "$API/apply" \
  -H 'Content-Type: application/json' \
  -d "{\"id\":\"config.profile.demo\",\"base\":\"$BASE1\",\"edit\":{\"roles\":{\"normal\":[{\"provider\":\"demo\",\"model\":\"clobbered\"}]}}}")
check "stale baseline returns 409" "$CODE" "409"
read -r C_PATH C_YOURS C_THEIRS <<EOF
$(python3 - "$SANDBOX/apply2.json" <<'PY'
import json,sys
c=json.load(open(sys.argv[1])).get("conflict") or {}
print(c.get("path",""), "y" if c.get("yours") else "", "t" if c.get("theirs") else "")
PY
)
EOF
[ -n "$C_PATH" ] && ok "the conflict names the file ($C_PATH)" || bad "the conflict does not say which file"
[ -n "$C_YOURS" ] && [ -n "$C_THEIRS" ] && ok "the conflict carries both sides' text" \
  || bad "conflict is missing original text - the user cannot tell what they are about to overwrite"
if grep -q '"clobbered"' "$PROFILE_FILE"; then
  bad "the rejected write **overwrote the disk** - this is the worst outcome"
else
  ok "disk was not overwritten (the rejected write never landed)"
fi

# ---- 4. Read-only concepts cannot be written ----
echo
echo "== 4. read-only (file with credentials) =="
CODE=$(curl -s -o /dev/null -w '%{http_code}' -X POST "$API/apply" \
  -H 'Content-Type: application/json' \
  -d '{"id":"config.file.providers.json","base":"","edit":{}}')
check "providers.json (with credentials) refuses writes" "$CODE" "409"

# ---- 5. Two gates ----
echo
echo "== 5. two gates =="
CODE=$(curl -s -o /dev/null -w '%{http_code}' -X POST "$API/apply" \
  -H 'Content-Type: text/plain' -d '{"id":"config.profile.demo","base":"","edit":{}}')
check "write does not declare application/json -> 415" "$CODE" "415"
CODE=$(curl -s -o /dev/null -w '%{http_code}' -X POST "$API/apply" \
  -H 'Host: evil.com' -H 'Content-Type: application/json' \
  -d '{"id":"config.profile.demo","base":"","edit":{}}')
check "Host is not loopback -> 403 (blocks DNS rebinding)" "$CODE" "403"
CODE=$(curl -s -o /dev/null -w '%{http_code}' -H 'Host: evil.com' "$API/snapshot")
check "reads go through this gate too -> 403" "$CODE" "403"

# ---- 6. Credentials do not leak ----
echo
echo "== 6. credential redaction =="
if grep -q 'sk-secret-must-not-leak' "$SNAP"; then
  bad "plaintext api_key appeared in the snapshot"
else
  ok "no plaintext credentials in the snapshot"
fi

echo
echo "result: $PASS passed, $FAIL failed"
[ "$FAIL" -eq 0 ] || exit 1
