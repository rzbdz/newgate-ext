#!/usr/bin/env bash
# Zero-token end to end: **hermes takeover** -- real binary, real takeover, real config.yaml, the kernel's fake upstream.
#
# It locks hermes's two coexisting usages, plus several things that would go wrong **silently**:
#
#   1  Takeover is byte surgery: the user's own things (here a model-independent key, and a scalar mixed into auxiliary
#      -- the real file on this machine has one) stay as they were;
#   2  "Follow newgate": the default model is the tier name, and which model it lands on is decided by the profile;
#   3  "Name it directly inside hermes": the URL on the `newgate-<profile>` line is pinned to that profile,
#      and when a `provider/model`-style name comes in, what the upstream receives is the model that profile binds;
#      and the list `/model` asks for (the /models the proxy answers for hermes) is the same one written in the file;
#   4  hermes's probing of the local base_url (/api/tags ...) 404s locally and does not bump the failure count;
#   5  While the daemon is running, adding a profile syncs the /model list into the file within seconds -- and
#      **only the list**: the main model the user picked in hermes is not changed back;
#   6  The `off` key restores: the few cells takeover wrote go back as they were, and anything else the user changed during takeover is kept.
#
# The fake upstream is reused by path from the kernel's `core/mock/fake_upstream.py`. Everything runs in a temporary sandbox,
# touching neither the real ~/.config nor ~/.hermes.

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
SANDBOX="${NEWGATE_E2E_SANDBOX:-$(mktemp -d /tmp/newgate-hermes.XXXXXX)}"
UP_PORT="${NEWGATE_E2E_UP_PORT:-18097}"
PORT="${NEWGATE_E2E_HERMES_PORT:-18098}"
BIN="$SANDBOX/bin/newgate"

PASS=0; FAIL=0
ok()   { echo "  ✓ $1"; PASS=$((PASS+1)); }
bad()  { echo "  ✗ $1"; FAIL=$((FAIL+1)); }
check(){ if [ "$2" = "$3" ]; then ok "$1"; else bad "$1 (expected '$3', got '$2')"; fi; }
has()  { if command grep -q -- "$2" "$3" 2>/dev/null; then ok "$1"; else bad "$1 (no $2 in $3)"; fi; }
hasnt(){ if command grep -q -- "$2" "$3" 2>/dev/null; then bad "$1 (should not be $2 in $3)"; else ok "$1"; fi; }

export NEWGATE_HOME="$SANDBOX/ng"
export HERMES_HOME="$SANDBOX/hermes"
export NEWGATE_LANG=en
unset LC_ALL LC_MESSAGES LANG LANGUAGE 2>/dev/null || true
mkdir -p "$NEWGATE_HOME/mappings" "$HERMES_HOME" "$(dirname "$BIN")"
CFG="$HERMES_HOME/config.yaml"

cleanup() {
  [ -n "${SERVE_PID:-}" ] && kill "$SERVE_PID" 2>/dev/null
  [ -n "${UP_PID:-}" ] && kill "$UP_PID" 2>/dev/null
  if [ -n "${NEWGATE_E2E_KEEP:-}" ]; then
    echo "Sandbox kept (NEWGATE_E2E_KEEP): $SANDBOX"
  else
    rm -rf "$SANDBOX"
  fi
}
trap cleanup EXIT

echo "Sandbox: $SANDBOX"
[ -x "$BIN_SRC" ] || { echo "Binary not found: $BIN_SRC -- run build/build.sh first"; exit 1; }
[ -f "$CORE/mock/fake_upstream.py" ] || {
  echo "The kernel's fake upstream was not found: $CORE/mock/fake_upstream.py (git submodule update --init --recursive)"
  exit 1
}
cp "$BIN_SRC" "$BIN"

python3 "$CORE/mock/fake_upstream.py" --port "$UP_PORT" >"$SANDBOX/upstream.log" 2>&1 &
UP_PID=$!
for _ in $(seq 1 50); do
  curl -sf -o /dev/null "http://127.0.0.1:$UP_PORT/__mock/requests" && break
  sleep 0.1
done

cat > "$NEWGATE_HOME/providers.json" <<EOF
{"providers":{"demo":{"protocol":"openai","base_url":"http://127.0.0.1:$UP_PORT/v1","api_key":"sk-demo"}}}
EOF
cat > "$NEWGATE_HOME/mappings/demo.json" <<'JSON'
{"description":"hermes e2e","context_window":400000,
 "roles":{"heavy":[{"provider":"demo","model":"mock-heavy"}],
          "normal":[{"provider":"demo","model":"mock-normal"}],
          "light":[{"provider":"demo","model":"mock-light"}]}}
JSON
cat > "$NEWGATE_HOME/mappings/alt.json" <<'JSON'
{"description":"pinned row","roles":{"normal":[{"provider":"demo","model":"mock-alt"}]}}
JSON
printf '{"default_profile":"demo","port":%s}' "$PORT" > "$NEWGATE_HOME/state.json"

# The shape hermes 0.13 writes itself (excerpt). `compact: false` mixed under auxiliary -- the real file on
# this machine looks like that (probably a migration wrote the wrong indentation); takeover must not be blocked by it, nor touch it.
cat > "$CFG" <<'YAML'
model:
  provider: custom
  model: claude-opus-5
  default: claude-opus-5
  base_url: https://api.example.com/
  api_key: sk-user-real-key
  api_mode: anthropic_messages
providers: {}
agent:
  max_turns: 90
auxiliary:
  vision:
    provider: smt-vision
    model: gpt-4o
    base_url: ""
    api_key: ""
    timeout: 120
  compact: false
delegation:
  model: claude-sonnet-4.6
  provider: ""
  base_url: https://api.example.com/
  api_key: sk-user-real-key
  max_iterations: 50
YAML
cp "$CFG" "$SANDBOX/original.yaml"

LASTUP() {
  curl -s "http://127.0.0.1:$UP_PORT/__mock/requests" | python3 -c '
import json,sys
d=json.load(sys.stdin)
print(d[-1]["body"].get("model","none") if d else "none")'
}
ASK() { # ASK <path prefix> <model name> → HTTP status code
  curl -s -o /dev/null -w '%{http_code}' -X POST "http://127.0.0.1:$PORT$1/chat/completions" \
    -H 'Content-Type: application/json' -H 'Authorization: Bearer newgate-local' \
    -d "{\"model\":\"$2\",\"max_tokens\":8,\"messages\":[{\"role\":\"user\",\"content\":\"hi\"}]}"
}
YAMLOK() { # run PyYAML over it when available (skip otherwise: CI machines do not necessarily have it)
  if python3 -c 'import yaml' 2>/dev/null; then
    if python3 -c 'import sys,yaml; yaml.safe_load(open(sys.argv[1]))' "$CFG" 2>/dev/null; then
      ok "$1"
    else
      bad "$1 (PyYAML failed to parse)"
    fi
  fi
}

echo
echo "== 1. newgate on hermes: byte surgery, other people's things stay put =="
"$BIN" on hermes >"$SANDBOX/on.log" 2>&1
check "takeover succeeded" "$?" "0"
has "the main loop points at newgate" '^  provider: newgate$' "$CFG"
has "the default model is the tier name" '^  default: normal$' "$CFG"
has "the follow line is there" '^  newgate:$' "$CFG"
has "one line per profile (pinned to /p/alt)" 'a/hermes/p/alt/v1' "$CFG"
has "the pinned line lists a concrete model" 'demo/mock-alt:' "$CFG"
has "the window the profile declares goes into the per-model cell" 'context_length: 400000' "$CFG"
has "delegation goes through the named provider" '^  model: mid$' "$CFG"
hasnt "the user's real key no longer appears in the main loop" 'sk-user-real-key' "$CFG"
has "an unrelated key stays as it was" '^  max_turns: 90$' "$CFG"
has "the scalar mixed into auxiliary stays as it was" '^  compact: false$' "$CFG"
YAMLOK "PyYAML can still read the edited file"
"$BIN" on hermes >"$SANDBOX/on2.log" 2>&1
cp "$CFG" "$SANDBOX/once.yaml"
"$BIN" on hermes >/dev/null 2>&1
if cmp -s "$CFG" "$SANDBOX/once.yaml"; then ok "idempotent: taking over again leaves the file unchanged"; else bad "taking over again changed the file"; fi

echo
echo "== 2. Both usages work =="
"$BIN" __serve --port "$PORT" >"$SANDBOX/serve.log" 2>&1 &
SERVE_PID=$!
for _ in $(seq 1 50); do
  curl -sf -o /dev/null "http://127.0.0.1:$PORT/__newgate/status" && break
  sleep 0.1
done
curl -s -X POST "http://127.0.0.1:$UP_PORT/__mock/reset" >/dev/null
check "tier name normal (follow newgate)" "$(ASK /a/hermes/v1 normal)" "200"
check "lands on the active profile's normal" "$(LASTUP)" "mock-normal"
check "naming demo/mock-alt (pinned to alt)" "$(ASK /a/hermes/p/alt/v1 demo/mock-alt)" "200"
check "the upstream received the model alt binds (prefix stripped)" "$(LASTUP)" "mock-alt"
check "the tier name on the pinned line uses that profile" "$(ASK /a/hermes/p/alt/v1 normal)" "200"
check "it really is alt's normal" "$(LASTUP)" "mock-alt"

modelids() { curl -s "http://127.0.0.1:$PORT$1/models" | python3 -c 'import json,sys;print(",".join(m["id"] for m in json.load(sys.stdin)["data"]))'; }
check "/model on the follow line: tiers only" "$(modelids /a/hermes/v1)" "heavy,normal,mid,light,vision"
check "/model on the pinned alt line: tiers + the model it binds" "$(modelids /a/hermes/p/alt/v1)" "heavy,normal,mid,light,vision,demo/mock-alt"
hasnt "does not write discover_models (hermes's normalization does not accept it; one warning per round)" 'discover_models' "$CFG"

echo
echo "== 3. hermes's local service probe: 404, not counted as a failure =="
before=$(curl -s "http://127.0.0.1:$PORT/__newgate/status" | python3 -c 'import json,sys;print(json.load(sys.stdin).get("failures",0))' 2>/dev/null || echo 0)
check "/api/tags 404" "$(curl -s -o /dev/null -w '%{http_code}' "http://127.0.0.1:$PORT/a/hermes/api/tags")" "404"
check "/v1/props 404" "$(curl -s -o /dev/null -w '%{http_code}' "http://127.0.0.1:$PORT/a/hermes/v1/props")" "404"
after=$(curl -s "http://127.0.0.1:$PORT/__newgate/status" | python3 -c 'import json,sys;print(json.load(sys.stdin).get("failures",0))' 2>/dev/null || echo 0)
check "the failure count does not move" "$after" "$before"

echo
echo "== 4. Adding a profile while the daemon runs: the list syncs automatically, the user's chosen main model is not changed back =="
# The user runs `/model demo/mock-alt --provider newgate-alt --global` inside hermes:
sed -i 's/^  provider: newgate$/  provider: newgate-alt/; s/^  default: normal$/  default: demo\/mock-alt/' "$CFG"
cat > "$NEWGATE_HOME/mappings/third.json" <<'JSON'
{"description":"added later","roles":{"normal":[{"provider":"demo","model":"mock-third"}]}}
JSON
synced=""
for _ in $(seq 1 40); do
  if command grep -q 'demo/mock-third:' "$CFG"; then synced=1; break; fi
  sleep 0.5
done
check "the new profile's line synced in" "${synced:-no}" "1"
has "the provider the user chose was not changed back" '^  provider: newgate-alt$' "$CFG"
has "the model the user chose was not changed back" '^  default: demo/mock-alt$' "$CFG"
YAMLOK "PyYAML can still read the file after the sync"

echo
echo "== 5. newgate off hermes: keys restored, changes the user made in the meantime kept =="
kill "$SERVE_PID" 2>/dev/null; SERVE_PID=""
sed -i 's/^  max_turns: 90$/  max_turns: 120/' "$CFG"
"$BIN" off hermes >"$SANDBOX/off.log" 2>&1
check "restore succeeded" "$?" "0"
hasnt "all of our lines are gone" 'newgate' "$CFG"
has "the main loop is back on the original provider" '^  provider: custom$' "$CFG"
has "the original key is back" 'sk-user-real-key' "$CFG"
has "the cell the user changed during takeover is kept" '^  max_turns: 120$' "$CFG"
has "providers is back to an empty mapping" '^providers: {}$' "$CFG"
YAMLOK "PyYAML can still read the file after the restore"

echo
echo "Result: $PASS passed, $FAIL failed"
[ "$FAIL" = "0" ]
