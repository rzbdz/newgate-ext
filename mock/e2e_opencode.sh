#!/usr/bin/env bash
# Zero-token end to end: fake upstream + newgate + fake opencode
# Everything runs in a temporary sandbox, never touching your real ~/.config
# # Why this script lives in the distribution repo (moved here 2026-09-20)
#
# It used to be the core's mock/e2e_claude.sh. These sections all go through **real client takeover** (newgate
# claude -> wrapper -> runtime -> fake claude), and client integration is a product decision - those three modules
# (claudecode / opencode / opencodeomo) moved to this repo, so their end-to-end came along too.
#
# The core side kept the **mechanism-facing** e2e (curl against the data plane + control endpoints + runtime switches),
# so no coverage was lost: together the two sides are the original 16 sections, only split along the line "who knows the client".

set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
# The binary comes from this repo's build entry (build/build.sh); **drop a copy named newgate** before use:
# it is multi-call, argv0 picks the entry (see README). The architecture in the artifact name uses Go's naming.
CORE="${NEWGATE_E2E_CORE:-$ROOT/core}"
case "$(uname -m)" in
  x86_64)        host_arch=amd64 ;;
  aarch64|arm64) host_arch=arm64 ;;
  i386|i686)     host_arch=386 ;;
  *)             host_arch=$(uname -m) ;;
esac
BIN_SRC="${NEWGATE_E2E_BIN:-$ROOT/dist/newgate-default-$(uname -s | tr 'A-Z' 'a-z')-$host_arch}"
[ -x "$BIN_SRC" ] || { echo "binary not found $BIN_SRC - run build/build.sh first"; exit 1; }
[ -f "$CORE/mock/fake_upstream.py" ] || { echo "no fake upstream in core/: first git submodule update --init"; exit 1; }
SANDBOX="${NEWGATE_E2E_SANDBOX:-$(mktemp -d /tmp/newgate-e2e.XXXXXX)}"
BIN="$SANDBOX/bin/newgate"
mkdir -p "$(dirname "$BIN")"
cp "$BIN_SRC" "$BIN"
# Ports can be overridden with env vars: the defaults do not collide, so a human can run several in parallel; in CI, only when
# two run in parallel on the same machine do they need to be spaced out explicitly (otherwise both fight over the same fake upstream port).
UP_PORT="${NEWGATE_E2E_UP_PORT:-18080}"
PROXY_PORT="${NEWGATE_E2E_PROXY_PORT:-18899}"

PASS=0; FAIL=0
ok()   { echo "  ✓ $1"; PASS=$((PASS+1)); }
bad()  { echo "  ✗ $1"; FAIL=$((FAIL+1)); }
check(){ if [ "$2" = "$3" ]; then ok "$1"; else bad "$1 (expected '$3', actual '$2')"; fi; }

export NEWGATE_HOME="$SANDBOX/ng"
# The language must be sealed too: the script asserts on **source-language text** (English), while the machine running it may be zh-Hans
# (our own machines are). Without pinning it, assertions like "the UI is English" go falsely red on Chinese machines.
export NEWGATE_LANG=en
unset LC_ALL LC_MESSAGES LANG LANGUAGE 2>/dev/null || true
export NEWGATE_TARGET_DIR="$SANDBOX/cfg"
mkdir -p "$NEWGATE_HOME" "$NEWGATE_TARGET_DIR"

cleanup() {
  "$BIN" stop >/dev/null 2>&1 || true
  [ -n "${UP_PID:-}" ] && kill "$UP_PID" 2>/dev/null
  rm -rf "$SANDBOX"
}
trap cleanup EXIT

echo "sandbox: $SANDBOX"
[ -x "$BIN" ] || { echo "build first: make build"; exit 1; }

# ---- setup: bring our own fixtures ----
# Deliberately **not** reading the user's real config: tests must be self-contained and reproducible, not depending on any local file.
cat > "$NEWGATE_TARGET_DIR/opencode.json" <<'EOF'
{ "$schema": "https://opencode.ai/config.json",
  "plugin": ["oh-my-openagent@latest"],
  "provider": {
    "upstream-a": { "npm": "@ai-sdk/openai-compatible",
      "options": { "baseURL": "https://example.invalid/v1", "apiKey": "sk-orig-a" },
      "models": { "model-large": {}, "model-small": {} } },
    "upstream-b": { "npm": "@ai-sdk/anthropic",
      "options": { "baseURL": "https://example.invalid/v1", "apiKey": "sk-orig-b" },
      "models": { "model-flagship": {}, "model-vision": {} } }
  } }
EOF
cat > "$NEWGATE_TARGET_DIR/oh-my-openagent.json" <<'EOF'
{ "agents": {
    "worker": { "model": "upstream-b/model-flagship", "variant": "max",
      "fallback_models": [ { "model": "upstream-a/model-large" } ] },
    "looker": { "model": "upstream-b/model-vision" } },
  "categories": {
    "quick": { "model": "upstream-a/model-small" },
    "deep":  { "model": "upstream-b/model-flagship" } } }
EOF

ORIG_SUM=$(md5sum "$NEWGATE_TARGET_DIR/opencode.json" | cut -d' ' -f1)
ORIG_OA_SUM=$(md5sum "$NEWGATE_TARGET_DIR/oh-my-openagent.json" | cut -d' ' -f1)

echo; echo "== 1. start the fake upstream =="
python3 "$CORE/mock/fake_upstream.py" --port "$UP_PORT" >"$SANDBOX/upstream.log" 2>&1 &
UP_PID=$!
for _ in $(seq 30); do
  curl -sf "http://127.0.0.1:$UP_PORT/__mock/requests" >/dev/null && break; sleep 0.1
done
curl -sf "http://127.0.0.1:$UP_PORT/__mock/requests" >/dev/null \
  && ok "fake upstream at 127.0.0.1:$UP_PORT" || { bad "fake upstream did not start"; exit 1; }

echo; echo "== 2. init + point the provider at the fake upstream =="
"$BIN" init >/dev/null 2>&1
python3 - "$NEWGATE_HOME/providers.json" "$UP_PORT" <<'PY'
import json,sys
p,port=sys.argv[1],sys.argv[2]
d=json.load(open(p))
for name,v in d["providers"].items():
    v["base_url"]=f"http://127.0.0.1:{port}/v1"
    v["api_key"]=f"sk-fake-{name}"
    v.pop("api_key_env",None)
json.dump(d,open(p,"w"),indent=2)
PY
python3 - "$NEWGATE_HOME/state.json" "$PROXY_PORT" <<'PY'
import json,sys
p=sys.argv[1]; d=json.load(open(p)); d["port"]=int(sys.argv[2])
json.dump(d,open(p,"w"),indent=2)
PY
[ -f "$NEWGATE_HOME/mappings/cheap.json" ] && ok "profile laid out" || bad "missing mappings"

echo; echo "== 3. newgate start (takeover config) =="
"$BIN" start >"$SANDBOX/start.log" 2>&1
sed 's/^/    /' "$SANDBOX/start.log" | head -20
[ -f "$NEWGATE_HOME/.newgate.pid" ]  && ok "pid file created"  || bad "missing pid file"
[ -f "$NEWGATE_HOME/.newgate.lock" ] && ok "lock file created" || bad "missing lock file"
curl -sf "http://127.0.0.1:$PROXY_PORT/__newgate/status" >/dev/null \
  && ok "proxy at 127.0.0.1:$PROXY_PORT" || bad "proxy not responding"

# After takeover the config should contain newgate and the original provider should still be there
grep -q '"newgate"' "$NEWGATE_TARGET_DIR/opencode.json" && ok "opencode.json got the newgate provider injected" || bad "not injected"
grep -q 'upstream-a' "$NEWGATE_TARGET_DIR/opencode.json" && ok "the original provider was kept" || bad "the original provider is gone!"
grep -q 'newgate/' "$NEWGATE_TARGET_DIR/oh-my-openagent.json" && ok "oh-my-openagent models rewritten" || bad "not rewritten"
grep -q 'fallback_models' "$NEWGATE_TARGET_DIR/oh-my-openagent.json" && ok "fallback structure kept" || bad "structure broken"

echo; echo "== 4. fake opencode sends a request (top-level model = newgate/heavy) =="
curl -sf "http://127.0.0.1:$UP_PORT/__mock/reset" -X POST >/dev/null
python3 "$CORE/mock/opencode_mock.py" --config-dir "$NEWGATE_TARGET_DIR" \
  --model newgate/heavy 2>&1 | sed 's/^/    /'
GOT=$(curl -s "http://127.0.0.1:$UP_PORT/__mock/requests" | python3 -c 'import json,sys;r=json.load(sys.stdin);print(r[0]["body"]["model"] if r else "NONE")')
check "the chain head cheap's heavy should resolve to model-large" "$GOT" "model-large"

echo; echo "== 5. switch profile (no restart, takes effect at once) =="
"$BIN" --set-profile top-only 2>&1 | sed 's/^/    /'
curl -sf "http://127.0.0.1:$UP_PORT/__mock/reset" -X POST >/dev/null
python3 "$CORE/mock/opencode_mock.py" --config-dir "$NEWGATE_TARGET_DIR" \
  --model newgate/heavy >/dev/null 2>&1
GOT=$(curl -s "http://127.0.0.1:$UP_PORT/__mock/requests" | python3 -c 'import json,sys;r=json.load(sys.stdin);print(r[0]["body"]["model"] if r else "NONE")')
check "after switching to top-only, heavy should be model-flagship (sparse layer takes effect)" "$GOT" "model-flagship"
KEY=$(curl -s "http://127.0.0.1:$UP_PORT/__mock/requests" | python3 -c 'import json,sys;r=json.load(sys.stdin);print(r[0]["headers"].get("authorization","")) if r else print("")')
check "key should switch to the upstream-b one" "$KEY" "Bearer sk-fake-upstream-b"

"$BIN" --set-profile expensive >/dev/null 2>&1
curl -sf "http://127.0.0.1:$UP_PORT/__mock/reset" -X POST >/dev/null
python3 "$CORE/mock/opencode_mock.py" --config-dir "$NEWGATE_TARGET_DIR" \
  --model newgate/heavy >/dev/null 2>&1
GOT=$(curl -s "http://127.0.0.1:$UP_PORT/__mock/requests" | python3 -c 'import json,sys;r=json.load(sys.stdin);print(r[0]["body"]["model"] if r else "NONE")')
check "after switching to expensive, heavy should be model-flagship" "$GOT" "model-flagship"

echo; echo "== 6. tier isolation: light should go to the small model =="
"$BIN" --set-profile cheap >/dev/null 2>&1
curl -sf "http://127.0.0.1:$UP_PORT/__mock/reset" -X POST >/dev/null
python3 "$CORE/mock/opencode_mock.py" --config-dir "$NEWGATE_TARGET_DIR" --slot small_model >/dev/null 2>&1
GOT=$(curl -s "http://127.0.0.1:$UP_PORT/__mock/requests" | python3 -c 'import json,sys;r=json.load(sys.stdin);print(r[0]["body"]["model"] if r else "NONE")')
check "small_model(light) should be model-small" "$GOT" "model-small"

echo; echo "== 7. streaming block by block (not buffered) =="
python3 "$CORE/mock/opencode_mock.py" --config-dir "$NEWGATE_TARGET_DIR" --stream 2>&1 | sed 's/^/    /' | tail -6

echo; echo "== 8. unknown tier is not guessed (fails rather than silently falling back) =="
OUT=$(python3 "$CORE/mock/opencode_mock.py" --config-dir "$NEWGATE_TARGET_DIR" --model newgate/nonexistent 2>&1)
echo "$OUT" | grep -q '404' && ok "unknown tier returns 404 and does not guess" || bad "unknown tier did not error correctly"
echo "$OUT" | grep -q 'newgate' && ok "the error carries the newgate marker" || bad "the error has no newgate marker"

echo; echo "== 9. run through every agent slot =="
curl -sf "http://127.0.0.1:$UP_PORT/__mock/reset" -X POST >/dev/null
python3 "$CORE/mock/opencode_mock.py" --config-dir "$NEWGATE_TARGET_DIR" --all 2>&1 | tail -3 | sed 's/^/    /'
N=$(curl -s "http://127.0.0.1:$UP_PORT/__mock/requests" | python3 -c 'import json,sys;print(len(json.load(sys.stdin)))')
[ "$N" -ge 2 ] && ok "all agent slots route ($N deduplicated requests)" || bad "agent slot routing failed (only $N)"

echo; echo "== 10. newgate stop (escape hatch: byte-for-byte restore) =="
"$BIN" stop 2>&1 | sed 's/^/    /'
NEW_SUM=$(md5sum "$NEWGATE_TARGET_DIR/opencode.json" | cut -d' ' -f1)
NEW_OA_SUM=$(md5sum "$NEWGATE_TARGET_DIR/oh-my-openagent.json" | cut -d' ' -f1)
check "opencode.json restored byte for byte" "$NEW_SUM" "$ORIG_SUM"
check "oh-my-openagent.json restored byte for byte" "$NEW_OA_SUM" "$ORIG_OA_SUM"
[ ! -f "$NEWGATE_HOME/.newgate.pid" ]  && ok "pid file cleared"  || bad "pid file left behind"
[ ! -f "$NEWGATE_HOME/.newgate.lock" ] && ok "lock file cleared" || bad "lock file left behind"
curl -sf "http://127.0.0.1:$PROXY_PORT/__newgate/status" >/dev/null 2>&1 \
  && bad "proxy is still running" || ok "proxy stopped"

echo; echo "=============================="
echo " $PASS passed, $FAIL failed"
echo "=============================="
[ "$FAIL" -eq 0 ] || exit 1
