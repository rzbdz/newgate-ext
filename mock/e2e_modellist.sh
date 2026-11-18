#!/usr/bin/env bash
# Zero-token end to end: **model list** (the path behind the frontend dropdown).
#
# It locks three things that are only visible at this layer (real binary + real HTTP + real upstream):
#   1  the adapter really built the address from the provider's base and parsed the models and groups;
#   2  "not supported" is a **normal answer** (200 + supported:false + reason), not a 500;
#   3  the BFF's extension endpoint is still behind the two gates (Host / Content-Type), the cache can hit,
#      and refresh can bypass it.
#
# No real upstream: `mock/fake_modellist.py` implements only list-models. Everything runs in a temporary sandbox,
# never touching the real ~/.config; ports are spaced away from other e2e's, so they can run in parallel.

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
SANDBOX="${NEWGATE_E2E_SANDBOX:-$(mktemp -d /tmp/newgate-modellist-e2e.XXXXXX)}"
PORT="${NEWGATE_E2E_MODELLIST_PORT:-18911}"
UP_PORT="${NEWGATE_E2E_MODELLIST_UP_PORT:-18101}"
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
  [ -n "${UP_PID:-}" ] && kill "$UP_PID" 2>/dev/null
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

# ---- fake upstream: /v1/models has a list; /none gives 404 (plays "no such endpoint") ----
python3 "$ROOT/mock/fake_modellist.py" --port "$UP_PORT" >"$SANDBOX/upstream.log" 2>&1 &
UP_PID=$!
for _ in $(seq 1 50); do
  curl -sf -o /dev/null "http://127.0.0.1:$UP_PORT/v1/models" && break
  sleep 0.1
done
if ! curl -sf -o /dev/null "http://127.0.0.1:$UP_PORT/v1/models"; then
  echo "fake upstream did not start, log:"; tail -20 "$SANDBOX/upstream.log"; exit 1
fi

# ---- sandbox config ----
cat > "$NEWGATE_HOME/providers.json" <<JSON
{"providers":{
  "fake":{"protocol":"openai","base_url":"http://127.0.0.1:$UP_PORT","api_key":"sk-modellist"},
  "none":{"protocol":"openai","base_url":"http://127.0.0.1:$UP_PORT/none","api_key":"sk-modellist"}
}}
JSON
cat > "$NEWGATE_HOME/mappings/demo.json" <<'JSON'
{"description":"modellist e2e","roles":{"normal":[{"provider":"fake","model":"alpha-chat"}]}}
JSON
printf '%s' "{\"default_profile\":\"demo\",\"port\":$PORT}" > "$NEWGATE_HOME/state.json"

# ---- start the service ----
"$BIN" __serve --port "$PORT" >"$SANDBOX/serve.log" 2>&1 &
SRV_PID=$!
for _ in $(seq 1 50); do
  curl -sf -o /dev/null "http://127.0.0.1:$PORT/ui/api/snapshot" && break
  sleep 0.1
done
if ! curl -sf -o /dev/null "http://127.0.0.1:$PORT/ui/api/snapshot"; then
  echo "service did not start, log:"; tail -30 "$SANDBOX/serve.log"; exit 1
fi

API="http://127.0.0.1:$PORT/ui/api"
RESP="$SANDBOX/resp.json"

# ---- 1. upstream has a list: models, groups, adapter ----
echo
echo "== 1. has a list =="
CODE=$(curl -s -o "$RESP" -w '%{http_code}' -X POST "$API/ext/modellist" \
  -H 'Content-Type: application/json' -d '{"provider":"fake"}')
check "the extension endpoint returns 200" "$CODE" "200"
read -r SUPPORTED N ADAPTER GROUP_LIST <<EOF
$(python3 - "$RESP" <<'PY'
import json,sys
d=json.load(open(sys.argv[1]))
print("yes" if d.get("supported") else "no", len(d.get("models") or []),
      d.get("adapter",""), ",".join(d.get("groups") or []))
PY
)
EOF
check "supported=true" "$SUPPORTED" "yes"
check "3 models listed" "$N" "3"
check "the openai adapter matched" "$ADAPTER" "openai"
check "groups are ordered by first appearance" "$GROUP_LIST" "alpha,beta"

# ---- 2. second call hits the cache, refresh bypasses ----
echo
echo "== 2. cache =="
curl -s -o "$RESP" -X POST "$API/ext/modellist" -H 'Content-Type: application/json' \
  -d '{"provider":"fake"}' >/dev/null
CACHED=$(python3 -c 'import json,sys;print(json.load(open(sys.argv[1])).get("cached",False))' "$RESP")
check "the second call is marked cached" "$CACHED" "True"
curl -s -o "$RESP" -X POST "$API/ext/modellist" -H 'Content-Type: application/json' \
  -d '{"provider":"fake","refresh":true}' >/dev/null
CACHED=$(python3 -c 'import json,sys;print(json.load(open(sys.argv[1])).get("cached",False))' "$RESP")
check "refresh bypasses the cache" "$CACHED" "False"

# ---- 3. no list endpoint: 200 + supported:false + reason ----
echo
echo "== 3. unsupported =="
CODE=$(curl -s -o "$RESP" -w '%{http_code}' -X POST "$API/ext/modellist" \
  -H 'Content-Type: application/json' -d '{"provider":"none"}')
check "unsupported is also 200 (not a failure)" "$CODE" "200"
read -r SUPPORTED NOTE <<EOF
$(python3 - "$RESP" <<'PY'
import json,sys
d=json.load(open(sys.argv[1]))
print("yes" if d.get("supported") else "no", d.get("note",""))
PY
)
EOF
check "supported=false" "$SUPPORTED" "no"
case "$NOTE" in
  *"no model list"*) ok "the reason says there is no model list endpoint" ;;
  *) bad "the reason is not clear ($NOTE)" ;;
esac

# ---- 4. a name that does not exist is an error (the caller asked wrong) ----
echo
echo "== 4. name does not exist =="
CODE=$(curl -s -o "$RESP" -w '%{http_code}' -X POST "$API/ext/modellist" \
  -H 'Content-Type: application/json' -d '{"provider":"typo"}')
check "unknown provider returns 400" "$CODE" "400"
if grep -q 'no provider named' "$RESP"; then ok "the error names that name"; else bad "the error is not clear: $(cat "$RESP")"; fi

# ---- 5. the BFF's two gates + verbs ----
echo
echo "== 5. two gates and verbs =="
CODE=$(curl -s -o /dev/null -w '%{http_code}' -X POST "$API/ext/modellist" \
  -H 'Content-Type: text/plain' -d '{"provider":"fake"}')
check "application/json not declared -> 415" "$CODE" "415"
CODE=$(curl -s -o /dev/null -w '%{http_code}' -X POST "$API/ext/modellist" \
  -H 'Host: evil.com' -H 'Content-Type: application/json' -d '{"provider":"fake"}')
check "Host is not loopback -> 403 (blocks DNS rebinding)" "$CODE" "403"
CODE=$(curl -s -o /dev/null -w '%{http_code}' "$API/ext/modellist")
check "GET → 405" "$CODE" "405"
CODE=$(curl -s -o /dev/null -w '%{http_code}' -X POST "$API/ext/modellist" \
  -H 'Content-Type: application/json' -d '{')
check "bad JSON -> 400" "$CODE" "400"
CODE=$(curl -s -o /dev/null -w '%{http_code}' -X POST "$API/ext/nope" \
  -H 'Content-Type: application/json' -d '{}')
check "unregistered extension -> 404" "$CODE" "404"

echo
echo "== result: $PASS passed, $FAIL failed =="
[ "$FAIL" -eq 0 ]
