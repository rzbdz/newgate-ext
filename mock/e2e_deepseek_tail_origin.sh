#!/usr/bin/env bash
# Zero-token end to end: the **origin rule** of the DeepSeek tail shape fix (move 4, messages dialect;
# move 5, Responses dialect).
#
# Background (two rounds, 2026-09-18 and 2026-09-22, measured cell by cell against real smt-deepseek/deepseek-flash):
# whether a "bare tail" (the last user turn in messages has only tool_result; the Responses input[] ends with
# function_call_output) is rejected by the upstream **does not depend on shape alone** - it also depends on who produced
# that tool call. The aggregator (the one newgate goes through) remembers the ids it issued, and uses
# that memory to put the reasoning back in the continuation round; for an id it did not produce it cannot, and the gap leaks all the way to DeepSeek, which then reports
# "must be passed back". The full derivation is in core/docs/06-reasoning.md §1.1.
#
# So the rule is **two-dimensional** (shape + origin), and the second dimension is the one most easily wrong silently at this layer:
#
#   A  **the self-produced family must not be under-recognized**. When several tool calls are issued in parallel in one round, the aggregator numbers them
#      `call_00_`, `call_01_`, ... in order - recognizing only `call_00_` judges "two tools in one round",
#      the most common normal round, as foreign, and so **even healthy chains get a sentence stuffed in**
#      (5060 injections all day on 2026-09-21, about a third of them this kind).
#   B  **an unrecognizable shape must be treated as foreign**. The consequence of a missed fix is not an error but **a silent shift down the chain**
#      (the client gets the next vendor's 200, and the log shows no sign that the middle vendor rejected it).
#
# The fake upstream reuses the core's `core/mock/fake_upstream.py` by path (not copied: it is a byte-for-byte replica of
# real upstream behavior, a copy inevitably drifts, and what drifts out is a "green fake test"). It **does not simulate**
# the aggregator's memory of "does it remember this id", so this e2e asserts on **the bytes we send out**
# (`/__mock/requests`), not the upstream's 200/400 - the real contract is locked on demand by
# a script that hits the real upstream, like `mock/e2e_reasoning_affinity.sh`.
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
SANDBOX="${NEWGATE_E2E_SANDBOX:-$(mktemp -d /tmp/newgate-tailorigin.XXXXXX)}"
# Ports are spaced away from the other e2e's: several can run at once.
UP_PORT="${NEWGATE_E2E_UP_PORT:-18095}"
PORT="${NEWGATE_E2E_TAILORIGIN_PORT:-18096}"
BIN="$SANDBOX/bin/newgate"

PASS=0; FAIL=0
ok()   { echo "  ✓ $1"; PASS=$((PASS+1)); }
bad()  { echo "  ✗ $1"; FAIL=$((FAIL+1)); }
check(){ if [ "$2" = "$3" ]; then ok "$1"; else bad "$1 (expected '$3', actual '$2')"; fi; }
has()  { if command grep -qF "$2" "$3" 2>/dev/null; then ok "$1"; else bad "$1 ($3 does not contain $2)"; fi; }
hasnt(){ if command grep -qF "$2" "$3" 2>/dev/null; then bad "$1 ($3 must not contain $2)"; else ok "$1"; fi; }

export NEWGATE_HOME="$SANDBOX/ng"
export NEWGATE_LANG=en
# The sandbox must be sealed: the session running e2e may itself carry newgate-injected window declarations (nested startup leaks the parent's
# env to the child).
unset CLAUDE_CODE_MAX_CONTEXT_TOKENS CLAUDE_CODE_AUTO_COMPACT_WINDOW
unset LC_ALL LC_MESSAGES LANG LANGUAGE 2>/dev/null || true
mkdir -p "$NEWGATE_HOME/mappings" "$(dirname "$BIN")"

cleanup() {
  [ -n "${UP_PID:-}" ] && kill "$UP_PID" 2>/dev/null
  [ -n "${SERVE_PID:-}" ] && kill "$SERVE_PID" 2>/dev/null
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

# ---- config ----
# The provider name contains deepseek, which is what the DeepSeek model family's MatchTarget recognizes (it looks at
# any of model name / provider name / base URL). That is the premise for this request being claimed.
cat > "$NEWGATE_HOME/providers.json" <<EOF
{"providers":{"deepseek":{"protocol":"openai","base_url":"http://127.0.0.1:$UP_PORT/v1","api_key":"sk-demo"}}}
EOF
cat > "$NEWGATE_HOME/mappings/ds.json" <<'JSON'
{"description":"deepseek tail origin e2e",
 "roles":{"normal":[{"provider":"deepseek","model":"mock-normal"}]}}
JSON
printf '{"default_profile":"ds","port":%s}' "$PORT" > "$NEWGATE_HOME/state.json"

"$BIN" __serve --port "$PORT" >"$SANDBOX/serve.log" 2>&1 &
SERVE_PID=$!
for _ in $(seq 1 50); do
  curl -sf -o /dev/null "http://127.0.0.1:$PORT/__newgate/status" && break
  sleep 0.1
done
LOG="$NEWGATE_HOME/newgate.log"

# POST <request body file> <output file> <path> -> HTTP status code.
# The agent segment cannot be omitted: `special.Request.Agent` comes from there, and without it the cross plugin does not claim
# (see the same comment in mock/e2e_codex_tools.sh).
POST() {
  curl -sS -o "$2" -w '%{http_code}' -X POST \
    "http://127.0.0.1:$PORT/a/codex/p/ds$3" \
    -H 'Content-Type: application/json' --data-binary @"$1"
}
RESET() { curl -s -X POST "http://127.0.0.1:$UP_PORT/__mock/reset" >/dev/null; }
# UPSAW <expression> - evaluate against the **last** request body the upstream received (body is it inside the expression).
UPSAW() {
  curl -s "http://127.0.0.1:$UP_PORT/__mock/requests" | python3 -c '
import json, sys
d = json.load(sys.stdin)
body = d[-1]["body"] if d else {}
print(eval(sys.argv[1], {"json": json}, {"body": body}))
' "$1"
}
# Whether it injected anything: is that `continue` in the last message/item.
INJ_TAIL='json.dumps(body[end][textkey])'
NOTE4="the trailing user turn holds only tool_result"
NOTE5="the Responses input[] ends on a function_call_output"

# After a request, wait for the log to land on disk: note and response are written on the same thread, but the log is append-only,
# and reading a step too early reads the previous request (other e2e's handle it the same way).
settle() { sleep 1; }

echo
echo "== 1. Responses: foreign call_id at the tail -> append a plain user message (move 5 acts) =="
cat > "$SANDBOX/req-resp-foreign.json" <<'JSON'
{"model":"normal","stream":false,"input":[
 {"type":"message","role":"user","content":[{"type":"input_text","text":"go"}]},
 {"type":"function_call","call_id":"call_faedac561213487ebeea7731","name":"echo","arguments":"{}"},
 {"type":"function_call_output","call_id":"call_faedac561213487ebeea7731","output":"x"}]}
JSON
RESET
check "200 as usual (the fake upstream does not simulate that memory, so the status code is not asserted here)" \
  "$(POST "$SANDBOX/req-resp-foreign.json" "$SANDBOX/out1.json" /v1/responses)" "200"
settle
check "the input[] the upstream received has one more item, its body word for word continue" \
  "$(UPSAW 'json.dumps([i for i in body["input"] if i.get("type")=="message"][-1])')" \
  '{"type": "message", "role": "user", "content": [{"type": "input_text", "text": "continue"}]}'
check "the extra item is **at the end** (the rule looks at the last item)" \
  "$(UPSAW 'body["input"][-1]["content"][0]["text"]')" "continue"
has "the log reported move 5 acting" "$NOTE5" "$LOG"

echo
echo "== 2. Responses: all self-produced (call_00_ + call_01_, two tools in one round) -> not a byte is touched =="
cat > "$SANDBOX/req-resp-self.json" <<'JSON'
{"model":"normal","stream":false,"input":[
 {"type":"message","role":"user","content":[{"type":"input_text","text":"go"}]},
 {"type":"function_call","call_id":"call_00_aaa111","name":"echo","arguments":"{}"},
 {"type":"function_call","call_id":"call_01_bbb222","name":"echo","arguments":"{}"},
 {"type":"function_call_output","call_id":"call_00_aaa111","output":"x"},
 {"type":"function_call_output","call_id":"call_01_bbb222","output":"y"}]}
JSON
RESET
POST "$SANDBOX/req-resp-self.json" "$SANDBOX/out2.json" /v1/responses >/dev/null
settle
check "the input[] the upstream received has neither more nor fewer items (5 items, as is)" \
  "$(UPSAW 'len(body["input"])')" "5"
# Compare input[] rather than the whole body: the model segment was rewritten by routing (normal -> mock-normal),
# That is a **routing** matter, not this move's.
check "the input[] sent out is byte-for-byte identical to what the client sent" \
  "$(UPSAW 'json.dumps(body["input"], sort_keys=True, ensure_ascii=False)')" \
  "$(python3 -c 'import json,sys; print(json.dumps(json.load(open(sys.argv[1]))["input"], sort_keys=True, ensure_ascii=False))' "$SANDBOX/req-resp-self.json")"
NOTE_COUNT_BEFORE="$(command grep -cF "$NOTE5" "$LOG" 2>/dev/null || echo 0)"

echo
echo "== 3. Responses: unrecognizable shape (the sequence number has one digit) -> treated as foreign, backfilled as before =="
sed 's/call_00_aaa111/call_0_aaa111/g' "$SANDBOX/req-resp-self.json" > "$SANDBOX/req-resp-odd.json"
RESET
POST "$SANDBOX/req-resp-odd.json" "$SANDBOX/out3.json" /v1/responses >/dev/null
settle
check "the input[] the upstream received became 6 items (the continue was added)" \
  "$(UPSAW 'len(body["input"])')" "6"
NOTE_COUNT_AFTER="$(command grep -cF "$NOTE5" "$LOG" 2>/dev/null || echo 0)"
check "move 5 count in the log +1" "$((NOTE_COUNT_AFTER - NOTE_COUNT_BEFORE))" "1"

echo
echo "== 4. messages: bare tool_result tail + all self-produced -> **no injection** (the healthy-chain regression point) =="
cat > "$SANDBOX/req-msg-self.json" <<'JSON'
{"model":"normal","stream":false,"messages":[
 {"role":"user","content":[{"type":"text","text":"go"}]},
 {"role":"assistant","content":[{"type":"tool_use","id":"call_00_aaa111","name":"Read","input":{}},
                               {"type":"tool_use","id":"call_01_bbb222","name":"Read","input":{}}]},
 {"role":"user","content":[{"type":"tool_result","tool_use_id":"call_00_aaa111","content":"ok"},
                           {"type":"tool_result","tool_use_id":"call_01_bbb222","content":"ok"}]}]}
JSON
RESET
POST "$SANDBOX/req-msg-self.json" "$SANDBOX/out4.json" /v1/messages >/dev/null
settle
check "the tail turn the upstream received still has two tool_results, no extra text block" \
  "$(UPSAW 'json.dumps([b.get("type") for b in body["messages"][-1]["content"]])')" \
  '["tool_result", "tool_result"]'
hasnt "the log has no trace of move 4 acting" "$NOTE4" "$LOG"

echo
echo "== 5. messages: bare tail + foreign id -> inject, body word for word continue =="
cat > "$SANDBOX/req-msg-foreign.json" <<'JSON'
{"model":"normal","stream":false,"messages":[
 {"role":"user","content":[{"type":"text","text":"go"}]},
 {"role":"assistant","content":[{"type":"tool_use","id":"call_faedac561213487ebeea7731","name":"Read","input":{}}]},
 {"role":"user","content":[{"type":"tool_result","tool_use_id":"call_faedac561213487ebeea7731","content":"ok"}]}]}
JSON
RESET
POST "$SANDBOX/req-msg-foreign.json" "$SANDBOX/out5.json" /v1/messages >/dev/null
settle
check "the tail turn became tool_result + text" \
  "$(UPSAW 'json.dumps([b.get("type") for b in body["messages"][-1]["content"]])')" \
  '["tool_result", "text"]'
check "the sentence appended is word for word continue (minimal wording, not a long sentence)" \
  "$(UPSAW 'body["messages"][-1]["content"][-1]["text"]')" "continue"
has "the log reported move 4 acting" "$NOTE4" "$LOG"

echo
echo "== 6. switch point: with deepseek.tail-shape-responses off, move 5 stops acting =="
"$BIN" plugin deepseek.tail-shape-responses off 5m >"$SANDBOX/off.log" 2>&1
sleep 1.5   # wait for one config reload (the switch and providers.json go the same way)
RESET
POST "$SANDBOX/req-resp-foreign.json" "$SANDBOX/out6.json" /v1/responses >/dev/null
settle
check "the upstream received neither more nor fewer input[] items (3 items, it stopped acting)" \
  "$(UPSAW 'len(body["input"])')" "3"
"$BIN" plugin deepseek.tail-shape-responses on >"$SANDBOX/on.log" 2>&1
sleep 1.5
RESET
POST "$SANDBOX/req-resp-foreign.json" "$SANDBOX/out7.json" /v1/responses >/dev/null
settle
check "turned back on, it appends again (4 items)" "$(UPSAW 'len(body["input"])')" "4"

echo
echo "result: $PASS passed, $FAIL failed"
[ "$FAIL" = "0" ]
