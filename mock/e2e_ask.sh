#!/usr/bin/env bash
# Zero-token end to end: **`newgate ask`, the consumer path** -- one sentence in, the body out.
#
# It locks three things that would go wrong **silently**:
#
#   1  **The body and the chain of thought stay separable**. What `answer=$(newgate ask ...)` gets must be the clean body;
#      in a reasoning model's answer the chain of thought is often several times longer than the body (see docs/06-reasoning.md),
#      and if it leaks in, what the script gets is unusable. The fake upstream emits a thinking block first and then the
#      body for **streaming** requests, so this one can be verified conclusively here;
#   2  **The question actually sent is that sentence**. `cliapi.Positional` does not skip option **values**,
#      so `ask --tier normal <question>` easily turns into sending "normal <question>" -- it reports no error, goes red nowhere, and just answers
#      nonsense. Assert against the request body the fake upstream recorded;
#   3  **`--profile` is a single-call override** that does not touch the global default.
#
# The fake upstream is reused by path from the kernel's `core/mock/fake_upstream.py` (not copied: it is a byte-for-byte replica of real
# upstream behavior, a copy would inevitably drift, and what drifts out is a "green fake test").
#
# Everything runs in a temporary sandbox, touching neither the real ~/.config.

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
SANDBOX="${NEWGATE_E2E_SANDBOX:-$(mktemp -d /tmp/newgate-ask-e2e.XXXXXX)}"
# Ports are offset from the other e2e scripts: several can run at once.
UP_PORT="${NEWGATE_E2E_UP_PORT:-18093}"
PORT="${NEWGATE_E2E_ASK_PORT:-18094}"
BIN="$SANDBOX/bin/newgate"

PASS=0; FAIL=0
ok()   { echo "  ✓ $1"; PASS=$((PASS+1)); }
bad()  { echo "  ✗ $1"; FAIL=$((FAIL+1)); }
check(){ if [ "$2" = "$3" ]; then ok "$1"; else bad "$1 (expected '$3', got '$2')"; fi; }

export NEWGATE_HOME="$SANDBOX/ng"
export NEWGATE_LANG=en
unset LC_ALL LC_MESSAGES LANG LANGUAGE 2>/dev/null || true
mkdir -p "$NEWGATE_HOME/mappings" "$(dirname "$BIN")"

cleanup() {
  [ -n "${UP_PID:-}" ] && kill "$UP_PID" 2>/dev/null
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
[ -f "$CORE/mock/fake_upstream.py" ] || {
  echo "The kernel's fake upstream was not found: $CORE/mock/fake_upstream.py"
  echo "core/ is a submodule; first: git submodule update --init --recursive"
  exit 1
}
cp "$BIN_SRC" "$BIN"

# ---- fake upstream ----
python3 "$CORE/mock/fake_upstream.py" --port "$UP_PORT" >"$SANDBOX/upstream.log" 2>&1 &
UP_PID=$!
for _ in $(seq 1 50); do
  curl -sf "http://127.0.0.1:$UP_PORT/__mock/requests" >/dev/null && break
  sleep 0.1
done
curl -sf "http://127.0.0.1:$UP_PORT/__mock/requests" >/dev/null \
  && ok "fake upstream on 127.0.0.1:$UP_PORT" || { bad "the fake upstream did not start"; exit 1; }

# ---- config: one chain hitting the fake upstream; another profile for single-call override ----
cat > "$NEWGATE_HOME/providers.json" <<EOF
{"providers":{"demo":{"protocol":"anthropic","base_url":"http://127.0.0.1:$UP_PORT/v1","api_key":"sk-demo"}}}
EOF
cat > "$NEWGATE_HOME/mappings/demo.json" <<'JSON'
{"description":"ask e2e","roles":{"light":[{"provider":"demo","model":"mock-model"}],
                                  "normal":[{"provider":"demo","model":"mock-model"}]}}
JSON
cat > "$NEWGATE_HOME/mappings/alt.json" <<'JSON'
{"description":"ask e2e, another chain","roles":{"normal":[{"provider":"demo","model":"other-model"}]}}
JSON
printf '{"default_profile":"demo","port":%s}' "$PORT" > "$NEWGATE_HOME/state.json"

"$BIN" __serve --port "$PORT" >"$SANDBOX/serve.log" 2>&1 &
for _ in $(seq 1 50); do
  curl -sf -o /dev/null "http://127.0.0.1:$PORT/__newgate/status" && break
  sleep 0.1
done

RESETUP() { curl -sf "http://127.0.0.1:$UP_PORT/__mock/reset" -X POST >/dev/null; }
# The user content in the first request the upstream received -- used to verify "what was really sent is that sentence".
SENT() { curl -s "http://127.0.0.1:$UP_PORT/__mock/requests" | python3 -c '
import json,sys
r = json.load(sys.stdin)
if not r: print(""); raise SystemExit
m = r[0]["body"].get("messages") or []
c = m[0].get("content") if m else ""
print(c if isinstance(c, str) else json.dumps(c, ensure_ascii=False))'; }

echo
echo "== 1. The body goes to stdout, the chain of thought to stderr =="
RESETUP
out=$("$BIN" ask --tier normal "answer in one sentence" 2>"$SANDBOX/think.txt")
check "stdout is the body (the sentence the fake upstream streamed back)" "$out" "MOCK-STREAM mock-model"
err=$(cat "$SANDBOX/think.txt")
case "$err" in
  *MOCK-THINKING-ORIGINAL*) ok "the chain of thought is on stderr (a human sees it, the script cannot capture it)";;
  *) bad "the chain of thought did not go to stderr, actual: $(echo "$err" | head -c 120)";;
esac
case "$out" in
  *MOCK-THINKING*) bad "the chain of thought leaked into stdout -- what \$(newgate ask ...) gets is no longer clean";;
  *) ok "not one byte of chain of thought in stdout";;
esac

echo
echo "== 2. The question really sent is that sentence (option values are not part of it) =="
RESETUP
"$BIN" ask --tier normal "what is the weather today" >/dev/null 2>&1
check "the user content the upstream received" "$(SENT)" "what is the weather today"

echo
echo "== 3. --profile is a single-call override and does not touch the global default =="
RESETUP
out=$("$BIN" ask --profile alt --tier normal "switch chains" 2>/dev/null)
check "this request took alt's chain" "$out" "MOCK-STREAM other-model"
check "the global default was not touched" "$(python3 -c 'import json;print(json.load(open("'"$NEWGATE_HOME"'/state.json"))["default_profile"])')" "demo"

echo
echo "== 4. Fed in through a pipe =="
RESETUP
out=$(printf 'a question from the pipe' | "$BIN" ask --tier light 2>/dev/null)
check "the piped request was answered too" "$out" "MOCK-STREAM mock-model"
check "the upstream received the sentence from the pipe" "$(SENT)" "a question from the pipe"

echo
echo "== 5. No question given: say so, do not send an empty request =="
out=$("$BIN" ask </dev/null 2>&1); rc=$?
case "$rc" in
  0) bad "exited 0 with no question given";;
  *) ok "non-zero exit ($rc)";;
esac
case "$out" in
  *"nothing to ask"*) ok "it said clearly that there is nothing to ask";;
  *) bad "did not say clearly, actual: $out";;
esac

echo
echo "== 6. The proxy is not running: say what to run, instead of starting it yourself =="
"$BIN" stop >/dev/null 2>&1
sleep 0.3
out=$("$BIN" ask "hi" 2>&1); rc=$?
case "$rc" in
  0) bad "proxy is not running but exited 0";;
  *) ok "non-zero exit ($rc)";;
esac
case "$out" in
  *"proxy is not running"*) ok "named 'proxy is not running'";;
  *) bad "not clear enough, actual: $out";;
esac
if "$BIN" status >/dev/null 2>&1 && curl -sf -o /dev/null "http://127.0.0.1:$PORT/__newgate/status"; then
  bad "ask brought the proxy up - a question should not become a state change"
else
  ok "ask did not secretly bring the proxy up"
fi

echo
echo "result: $PASS passed, $FAIL failed"
[ "$FAIL" = "0" ]
