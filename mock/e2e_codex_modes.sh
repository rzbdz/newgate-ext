#!/usr/bin/env bash
# Zero-token end to end: **codex's two takeover modes**, and the chain that "maps codex's own model names back to a tier".
#
# Background (measured 2026-09-21): codex's model catalog is **compiled into its binary** (`codex debug models`
# prints 9), it does not ask the server - point the provider at a recording fake upstream, and `/v1/models`
# is never requested once (tried all three: `remote_models` / `models_endpoint` / `env_key`).
# So the path of "making tiers appear in its own picker" does not exist, and the only way is the reverse: **let it keep using its own
# model names, and recognize the names when they come in**.
#
# It locks four things that can go wrong **silently**:
#
#   1  **the mode really changes the value written into config.toml**. takeover writes tier names (codex only accepts one
#      model, it cannot switch), rename writes codex's own model names. Getting it wrong does not error - the takeover is still
#      "successful", and only when the user switches models in codex do they find it does nothing;
#   2  **those names really resolve**. The symptom of a role not being registered is 404, and that 404 says
#      "neither a tier nor bound in any profile" - it will not tell you "that table did not take effect".
#      So the assertion here is **the model name the upstream received**: `gpt-5.6-luna` must land on the model bound to the normal
#      tier (the fake upstream records the request body, so this can be pinned down);
#   3  **dynamic**: adding a new model line takes effect **without a restart** (the table is part of the config, and changing it and changing
#      providers.json should reload automatically the same way). This is the reason the table exists - when the upstream ships a new
#      model, the user adds one line and should be able to use it;
#   4  **those who do not opt in are unaffected**: after switching back to takeover those names are **no longer** recognized as roles.
#      This is a deliberate tradeoff - registering a name as a role short-circuits the path "a concrete model name maps back to its tier",
#      and other clients' requests travel that path (the local smt-codex chain carries gpt-5.6-terra).
#
# The fake upstream reuses the core's `core/mock/fake_upstream.py` by path (not copied: it is a byte-for-byte replica of real
# upstream behavior, a copy inevitably drifts, and what drifts out is a "green fake test").
#
# Everything runs in a temporary sandbox, never touching the real ~/.config or ~/.codex.

set -u

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CORE="$ROOT/core"
DIST_NAME="${NEWGATE_E2E_DIST:-default}"
case "$(uname -m)" in
  x86_64)        host_arch=amd64 ;;
  aarch64|arm64) host_arch=arm64 ;;
  i386|i686)     host_arch=386 ;;
  *)             host_arch=$(uname -m) ;;
esac
BIN_SRC="${NEWGATE_E2E_BIN:-$ROOT/dist/newgate-$DIST_NAME-$(uname -s | tr 'A-Z' 'a-z')-$host_arch}"
SANDBOX="${NEWGATE_E2E_SANDBOX:-$(mktemp -d /tmp/newgate-codexmodes.XXXXXX)}"
# Ports are spaced away from the other e2e's: several can run at once.
UP_PORT="${NEWGATE_E2E_UP_PORT:-18095}"
PORT="${NEWGATE_E2E_CODEX_PORT:-18096}"
BIN="$SANDBOX/bin/newgate"

PASS=0; FAIL=0
ok()   { echo "  ✓ $1"; PASS=$((PASS+1)); }
bad()  { echo "  ✗ $1"; FAIL=$((FAIL+1)); }
check(){ if [ "$2" = "$3" ]; then ok "$1"; else bad "$1 (expected '$3', actual '$2')"; fi; }
has()  { if command grep -q "$2" "$3" 2>/dev/null; then ok "$1"; else bad "$1 ($3 does not contain $2)"; fi; }

export NEWGATE_HOME="$SANDBOX/ng"
export CODEX_HOME="$SANDBOX/codex"
export NEWGATE_LANG=en
unset LC_ALL LC_MESSAGES LANG LANGUAGE 2>/dev/null || true
mkdir -p "$NEWGATE_HOME/mappings" "$CODEX_HOME" "$(dirname "$BIN")"

cleanup() {
  [ -n "${UP_PID:-}" ] && kill "$UP_PID" 2>/dev/null
  "$BIN" stop >/dev/null 2>&1 || true
  if [ -n "${NEWGATE_E2E_KEEP:-}" ]; then
    echo "sandbox kept (NEWGATE_E2E_KEEP): $SANDBOX"
  else
    rm -rf "$SANDBOX"
  fi
}
trap cleanup EXIT

echo "sandbox: $SANDBOX"
[ -x "$BIN_SRC" ] || { echo "binary not found $BIN_SRC - run build/build.sh first"; exit 1; }
[ -f "$CORE/mock/fake_upstream.py" ] || {
  echo "cannot find the core's fake upstream $CORE/mock/fake_upstream.py"
  echo "core/ is a submodule, first: git submodule update --init --recursive"
  exit 1
}
cp "$BIN_SRC" "$BIN"

# ---- fake upstream ----
python3 "$CORE/mock/fake_upstream.py" --port "$UP_PORT" >"$SANDBOX/upstream.log" 2>&1 &
UP_PID=$!
for _ in $(seq 1 50); do
  curl -sf -o /dev/null "http://127.0.0.1:$UP_PORT/__mock/requests" && break
  sleep 0.1
done

# ---- config: each of the three tiers is bound to a model name you can tell apart at a glance ----
cat > "$NEWGATE_HOME/providers.json" <<EOF
{"providers":{"demo":{"protocol":"anthropic","base_url":"http://127.0.0.1:$UP_PORT/v1","api_key":"sk-demo"}}}
EOF
cat > "$NEWGATE_HOME/mappings/demo.json" <<'JSON'
{"description":"codex modes e2e",
 "roles":{"heavy":[{"provider":"demo","model":"mock-heavy"}],
          "normal":[{"provider":"demo","model":"mock-normal"}],
          "light":[{"provider":"demo","model":"mock-light"}]}}
JSON
printf '{"default_profile":"demo","port":%s}' "$PORT" > "$NEWGATE_HOME/state.json"

# A codex config that has **not been taken over yet**: with comments, with sections it grew on its own.
# A sample that already carries `[model_providers.newgate]` makes takeover refuse to run (and rightly so:
# taking over without an original backup means losing the user's original config), and that is the test's own pitfall.
cat > "$CODEX_HOME/config.toml" <<'TOML'
# A comment I wrote myself
model = "gpt-5.6-terra"
personality = "pragmatic"

[projects."/root"]
trust_level = "trusted"
TOML

# the model in the last request the upstream received - used to see **who the proxy actually sent**.
LASTUP() {
  curl -s "http://127.0.0.1:$UP_PORT/__mock/requests" | python3 -c '
import json,sys
d=json.load(sys.stdin)
print(d[-1]["body"].get("model","none") if d else "none")'
}
ASK() { # ASK <model name> -> HTTP status code
  curl -s -o /dev/null -w '%{http_code}' -X POST "http://127.0.0.1:$PORT/v1/messages" \
    -H 'Content-Type: application/json' \
    -d "{\"model\":\"$1\",\"max_tokens\":8,\"messages\":[{\"role\":\"user\",\"content\":\"hi\"}]}"
}

echo
echo "== 1. takeover is the default: config.toml gets the tier name =="
"$BIN" on codex >"$SANDBOX/on1.log" 2>&1
has "model is written as the tier name" '^model = "normal"' "$CODEX_HOME/config.toml"
has "the user's own comment is still there (takeover is byte surgery, not a TOML round trip)" 'A comment I wrote myself' "$CODEX_HOME/config.toml"

echo
echo "== 2. switch to rename: write codex's own model name =="
printf '{"mode":"rename"}' > "$NEWGATE_HOME/codex-models.json"
"$BIN" on codex >"$SANDBOX/on2.log" 2>&1
has "the normal tier is written as codex's model name (gpt-5.6-sol)" '^model = "gpt-5.6-sol"' "$CODEX_HOME/config.toml"

echo
echo "== 3. in rename mode, codex sending its own model name maps back to a tier =="
"$BIN" __serve --port "$PORT" >"$SANDBOX/serve.log" 2>&1 &
for _ in $(seq 1 50); do
  curl -sf -o /dev/null "http://127.0.0.1:$PORT/__newgate/status" && break
  sleep 0.1
done
curl -s -X POST "http://127.0.0.1:$UP_PORT/__mock/reset" >/dev/null
check "gpt-5.6-luna is recognized (HTTP 200)" "$(ASK gpt-5.6-luna)" "200"
check "it lands on the model bound to the normal tier" "$(LASTUP)" "mock-normal"

echo
echo "== 4. dynamic: add a new model line, no restart =="
printf '{"mode":"rename","models":[{"slug":"gpt-7-nova","tier":"heavy"},{"slug":"gpt-5.6-luna","tier":"light"}]}' \
  > "$NEWGATE_HOME/codex-models.json"
sleep 1.5   # wait for one config reload (the role table is part of the config, same path as providers.json)
curl -s -X POST "http://127.0.0.1:$UP_PORT/__mock/reset" >/dev/null
check "the newly added gpt-7-nova is recognized at once" "$(ASK gpt-7-nova)" "200"
check "it lands on heavy" "$(LASTUP)" "mock-heavy"
check "change the luna line to light: rerouted at once" "$(ASK gpt-5.6-luna)" "200"
check "it really went to light" "$(LASTUP)" "mock-light"

echo
echo "== 5. switch back to takeover: those names are no longer recognized as roles (others unaffected) =="
printf '{"mode":"takeover"}' > "$NEWGATE_HOME/codex-models.json"
sleep 1.5
check "gpt-7-nova is no longer recognized as a role (404)" "$(ASK gpt-7-nova)" "404"
check "tier names still work as usual (the 404 is only about those codex names)" "$(ASK normal)" "200"

echo
echo "result: $PASS passed, $FAIL failed"
[ "$FAIL" = "0" ]
