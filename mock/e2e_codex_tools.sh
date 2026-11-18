#!/usr/bin/env bash
# Zero-token end to end: **Codex × DeepSeek tool crossover** -- lift/degrade on the request side, turn back on the response side.
#
# Background (2026-09-21, measured cell by cell against the real smt-deepseek/deepseek-flash): each vendor's shape is
# fine on its own; they only break when put together.
#
#   The old Codex hangs the tool tree under `input[0].additional_tools`, while DeepSeek's
#   `/responses` **only reads tools from the top-level `tools`**. Each side follows its own rules, so the model cannot see a single
#   tool -- it falls back to the text format from training to "call tools", the client cannot parse that, and so
#   **not one tool_result comes back** (the customer's words: "never successfully got a tool_result").
#   This reports no error, the request returns 200, the body even looks plausible, so only an end-to-end test can lock it.
#
#   The new Codex puts tools directly at the top level `tools` (including `namespace` grouping and `web_search`).
#   That is what DeepSeek already reads, but it only knows `apply_patch` among `custom` and 400s on everything else;
#   a custom hidden inside `namespace` gets a different 400 message.
#   （`Currently custom tools are not allowed inside a namespace`）。
#
# It locks five things that would go wrong **silently**:
#
#   1  **Old shapes get lifted**. The symptom without lifting is "the model cannot see the tools", not an error -- see above;
#   2  **Both custom sites get degraded** (the top-level one and the one in a namespace). The symptom without degrading is
#      a 400 upstream, and a 400 here happens **not** to be silent: the fake upstream enforces the real criterion, so "still
#      200" is itself the evidence that degrading worked;
#   3  **The response side is turned back**. A degraded tool must become `custom_tool_call` again in the response (plus
#      the `custom_tool_call_input.*` event name family, and the `{"input": ...}` wrapper must be stripped);
#      **ones that were never degraded** (native functions) must not change by a single byte. The symptom without turning back is the client taking
#      a shape it does not recognize and eval'ing it;
#   4  **Not one byte changes when there is no custom**. "Do nothing when there is nothing to fix" is the rule most easily
#      broken by accident on this path, and breaking it is invisible (the request still returns 200);
#   5  **Both switch points are really read**, and they are **two things**. Lifting tools (moving additional_tools to
#      the top level) and degrading (turning an upstream-rejected custom into a function) were crammed under the same
#      switch before 2026-09-23, called `codex-deepseek.lift-tools` -- so the "Codex with Ark" path had no
#      switch to turn off at all, and the same behavior had two names in the same binary. Now they are separate:
#      `codex.lift-tools` (client dialect, modules/codex) and
#      `codex-deepseek.degrade-tools` (upstream quirk, modules/codex_deepseek).
#      Both sides must really be read -- on 2026-09-21 it was measured that a switch reported "off" while requests were still rewritten,
#      which is worse than no switch; and if only one of the two switches is unread, turning it off becomes a no-op,
#      or becomes an asymmetry that breaks the client ("the request was not degraded, but the response was turned into custom_tool_call").
#
# The fake upstream is reused by path from the kernel's `core/mock/fake_upstream.py` (not copied: it is a byte-for-byte replica of real
# upstream behavior, a copy would inevitably drift, and what drifts out is a "green fake test"). Its `/responses`
# branch is likewise written from measurement: tool calls start at output_index 1 (a reasoning item comes first),
# arguments arrive as a series of deltas, and custom tools get a 400 by the real criterion.
#
# Everything runs in a temporary sandbox, touching neither the real ~/.config nor ~/.codex.

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
SANDBOX="${NEWGATE_E2E_SANDBOX:-$(mktemp -d /tmp/newgate-codextools.XXXXXX)}"
# Ports are offset from the other e2e scripts: several can run at once.
UP_PORT="${NEWGATE_E2E_UP_PORT:-18097}"
PORT="${NEWGATE_E2E_CODEXTOOLS_PORT:-18098}"
BIN="$SANDBOX/bin/newgate"

PASS=0; FAIL=0
ok()   { echo "  ✓ $1"; PASS=$((PASS+1)); }
bad()  { echo "  ✗ $1"; FAIL=$((FAIL+1)); }
check(){ if [ "$2" = "$3" ]; then ok "$1"; else bad "$1 (expected '$3', got '$2')"; fi; }
has()  { if command grep -q "$2" "$3" 2>/dev/null; then ok "$1"; else bad "$1 (no $2 in $3)"; fi; }
hasnt(){ if command grep -q "$2" "$3" 2>/dev/null; then bad "$1 (should not be $2 in $3)"; else ok "$1"; fi; }

export NEWGATE_HOME="$SANDBOX/ng"
export NEWGATE_LANG=en
# The sandbox must be sealed: the session running the e2e may itself carry window declarations injected by newgate (when started nested, the parent's
# env leaks into child processes).
unset CLAUDE_CODE_MAX_CONTEXT_TOKENS CLAUDE_CODE_AUTO_COMPACT_WINDOW
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
  curl -sf -o /dev/null "http://127.0.0.1:$UP_PORT/__mock/requests" && break
  sleep 0.1
done

# ---- config ----
# The provider name contains deepseek, which is what DeepSeek's model-family MatchTarget recognizes (it looks at any
# of model name / provider name / base URL). This is the precondition for the **crossover plugin claiming** this request,
# and also why this e2e does not care which upstream it is.
cat > "$NEWGATE_HOME/providers.json" <<EOF
{"providers":{"deepseek":{"protocol":"openai","base_url":"http://127.0.0.1:$UP_PORT/v1","api_key":"sk-demo"}}}
EOF
cat > "$NEWGATE_HOME/mappings/ds.json" <<'JSON'
{"description":"codex tools e2e",
 "roles":{"normal":[{"provider":"deepseek","model":"mock-normal"}]}}
JSON
printf '{"default_profile":"ds","port":%s}' "$PORT" > "$NEWGATE_HOME/state.json"

"$BIN" __serve --port "$PORT" >"$SANDBOX/serve.log" 2>&1 &
for _ in $(seq 1 50); do
  curl -sf -o /dev/null "http://127.0.0.1:$PORT/__newgate/status" && break
  sleep 0.1
done
LOG="$NEWGATE_HOME/newgate.log"

# POST <request body file> <output file> → HTTP status code.
# codex's real path carries /a/codex/ -- `special.Request.Agent` comes from that segment, and without it
# the crossover plugin does not claim it at all (measured 2026-09-21: going through /p/<profile>/ sends the request
# as is, which looks like "the patch did not take effect").
POST() {
  curl -sS -o "$2" -w '%{http_code}' -X POST \
    "http://127.0.0.1:$PORT/a/codex/p/ds/v1/responses" \
    -H 'Content-Type: application/json' --data-binary @"$1"
}
RESET() { curl -s -X POST "http://127.0.0.1:$UP_PORT/__mock/reset" >/dev/null; }
# UPSAW <expression> -- evaluate against the **last** request body the upstream received (body is that one inside the expression).
UPSAW() {
  curl -s "http://127.0.0.1:$UP_PORT/__mock/requests" | python3 -c '
import json, sys
d = json.load(sys.stdin)
body = d[-1]["body"] if d else {}
print(eval(sys.argv[1], {"json": json}, {"body": body}))
' "$1"
}
# A shape summary of one tool table: [(type, name), ...], top-level order.
SHAPE='json.dumps([[t.get("type"), t.get("name")] for t in (body.get("tools") or [])])'

echo
echo "== 1. Old shape: tools hang under input[0].additional_tools, nothing at the top level =="
cat > "$SANDBOX/req-old.json" <<'JSON'
{"model":"normal","stream":true,"tool_choice":"auto",
 "input":[{"type":"additional_tools","id":"at_1","role":"developer","tools":[
    {"type":"namespace","name":"functions","description":"","tools":[
      {"type":"custom","name":"exec","description":"Run JS"},
      {"type":"function","name":"wait","description":"Wait","parameters":{"type":"object","properties":{"cell_id":{"type":"string"}},"required":["cell_id"]}}]}]},
   {"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}]}
JSON
RESET
check "old shape returns 200 (unlifted, the model cannot see tools, but it does **not** error)" \
  "$(POST "$SANDBOX/req-old.json" "$SANDBOX/out-old.sse")" "200"
check "the upstream received top-level tools: two, one function and one degraded from custom" \
  "$(UPSAW "$SHAPE")" '[["function", "exec"], ["function", "wait"]]'
check "the degraded one carries parameters (the upstream's function requires this field)" \
  "$(UPSAW 'json.dumps(body["tools"][0].get("parameters"))')" \
  '{"type": "object", "properties": {"input": {"type": "string"}}, "required": ["input"]}'
has "the log reports 2 lifted" "lifted 2 tool declarations" "$LOG"
has "the log reports 1 degraded" "degraded 1 custom tool to a standard function" "$LOG"

echo
echo "== 2. Response side: the degraded one comes back as custom_tool_call, the native one changes not one byte =="
# The upstream answers one function_call per tool (arguments arrive as a series of deltas, matching the real shape),
# and the outbound rewrite is accounted by output_index: exec should become custom_tool_call, wait should stay as is.
has "the degraded one is custom_tool_call in the stream" '"type":"custom_tool_call"' "$SANDBOX/out-old.sse"
has "its arguments event name changed to custom_tool_call_input" \
  '"type":"response.custom_tool_call_input.delta"' "$SANDBOX/out-old.sse"
has "the closing item is renamed too" '"type":"response.custom_tool_call_input.done"' "$SANDBOX/out-old.sse"
has "the native one is still function_call (not changed along the way)" '"type":"function_call"' "$SANDBOX/out-old.sse"
has "the outer {\"input\": ...} wrapper is stripped (codex wants the raw string inside)" \
  '"input":"MOCK-TOOL-exec"' "$SANDBOX/out-old.sse"
hasnt "after unwrapping, input should not still wrap a JSON object" '"input":"{\\"input\\"' "$SANDBOX/out-old.sse"

echo
echo "== 3. New shape: a custom mixed into the top-level tools =="
cat > "$SANDBOX/req-top.json" <<'JSON'
{"model":"normal","stream":false,
 "input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}],
 "tools":[{"type":"function","name":"exec_command","description":"run","parameters":{"type":"object","properties":{"cmd":{"type":"string"}}}},
          {"type":"custom","name":"exec","description":"Run JS"}]}
JSON
RESET
check "a custom at the top level also returns 200 (without degrading, the upstream 400s Unsupported custom tool)" \
  "$(POST "$SANDBOX/req-top.json" "$SANDBOX/out-top.json")" "200"
check "the top-level custom was degraded to a function, the native one untouched" \
  "$(UPSAW "$SHAPE")" '[["function", "exec_command"], ["function", "exec"]]'
has "the log reports this degradation" "degraded 1 custom tool to a standard function" "$LOG"

echo
echo "== 4. A custom hidden inside a namespace (the upstream's 400 is a different sentence) =="
cat > "$SANDBOX/req-ns.json" <<'JSON'
{"model":"normal","stream":false,
 "input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}],
 "tools":[{"type":"namespace","name":"functions","description":"","tools":[
    {"type":"function","name":"wait","description":"w","parameters":{"type":"object","properties":{}}},
    {"type":"custom","name":"exec","description":"Run JS"}]}]}
JSON
RESET
check "a custom inside a namespace also returns 200 (miss it and you get Currently custom tools are not allowed inside a namespace)" \
  "$(POST "$SANDBOX/req-ns.json" "$SANDBOX/out-ns.json")" "200"
check "the namespace shell is still there (DeepSeek really uses it as a grouping), and both entries inside are functions" \
  "$(UPSAW 'json.dumps([body["tools"][0]["type"], [[t.get("type"), t.get("name")] for t in body["tools"][0]["tools"]]])')" \
  '["namespace", [["function", "wait"], ["function", "exec"]]]'

echo
echo "== 5. Not one byte changes when there is no custom =="
cat > "$SANDBOX/req-clean.json" <<'JSON'
{"model":"normal","stream":false,
 "input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}],
 "tools":[{"type":"function","name":"exec_command","description":"run","parameters":{"type":"object","properties":{}}},
          {"type":"namespace","name":"multi_agent_v1","tools":[{"type":"function","name":"spawn_agent","description":"s","parameters":{"type":"object","properties":{}}}]},
          {"type":"web_search","external_web_access":false}]}
JSON
# This request should produce **not one note**. The log is shared by the whole e2e, so first record the current line count,
# then compare "no new lifted/degraded lines between these requests" -- grabbing the log by file name would catch the wrong one.
BEFORE=$(command grep -c 'lifted \|degraded ' "$LOG" 2>/dev/null || echo 0)
RESET
check "clean top-level tools also return 200" "$(POST "$SANDBOX/req-clean.json" "$SANDBOX/out-clean.json")" "200"
AFTER=$(command grep -c 'lifted \|degraded ' "$LOG" 2>/dev/null || echo 0)
check "not one lift/degrade log line for this request (do nothing when there is nothing to fix)" "$AFTER" "$BEFORE"
# Control: what the upstream received must be **the same shape** as what the client sent (including namespace and
# web_search). Compare the sort_keys shape rather than bytes: the fake upstream records the parsed object.
check "what the upstream received is identical to what the client sent" \
  "$(UPSAW 'json.dumps(body["tools"], sort_keys=True)')" \
  "$(python3 -c 'import json,sys; print(json.dumps(json.load(open(sys.argv[1]))["tools"], sort_keys=True))' "$SANDBOX/req-clean.json")"
# Nothing should move on the response side either: something never degraded should not be turned into custom_tool_call.
hasnt "no custom_tool_call in the clean stream (the response side did not claim it)" "custom_tool_call" "$SANDBOX/out-clean.json"

echo
echo "== 6. The degrade switch: with it off, custom goes out as is and the upstream blocks it by the real criterion =="
# This one stops only **the downstream** half: lifting still happens (that is the client's business, see section 7), but a lifted custom
# is no longer degraded to a function -- so the upstream 400s by its real criterion. That is "the symptom of turning degrade off",
# and being able to write it down is what proves the switch is really read.
"$BIN" plugin codex-deepseek.degrade-tools off 5m >"$SANDBOX/off-degrade.log" 2>&1
sleep 1.5   # wait for one config reload (switches and providers.json take the same path)
RESET
check "with degrade off the upstream 400s (that custom went out as is)" \
  "$(POST "$SANDBOX/req-old.json" "$SANDBOX/out-off-degrade.json")" "400"
has "the 400 body is the upstream's own Unsupported custom tool (not a line we made up)" \
  "Unsupported custom tool" "$SANDBOX/out-off-degrade.json"
check "lifting did not stop: the upstream still received top-level tools, they just still contain that custom" \
  "$(UPSAW "$SHAPE")" '[["custom", "exec"], ["function", "wait"]]'
hasnt "the response side did not claim it (a request that was not degraded but a response turned into custom_tool_call is the asymmetry that breaks clients)" \
  "custom_tool_call" "$SANDBOX/out-off-degrade.json"
"$BIN" plugin codex-deepseek.degrade-tools on >"$SANDBOX/on-degrade.log" 2>&1
sleep 1.5
RESET
check "turning it back on gives 200 again" "$(POST "$SANDBOX/req-old.json" "$SANDBOX/out-on-degrade.sse")" "200"
check "both are functions now" \
  "$(UPSAW "$SHAPE")" '[["function", "exec"], ["function", "wait"]]'

echo
echo "== 7. The lift switch: with it off the tools do not go up at all, and both sides stop =="
# Orthogonal to section 6: this one stops "lifting from input[0] to the top level" itself (unrelated to the upstream,
# so it is in modules/codex's hands). Once off, DeepSeek's fallback branch must stop too --
# otherwise "turn codex.lift-tools off" is a no-op on this path, and a switch that announces itself but is unread is worse than no
# switch at all (the same family of problem was measured on live on 2026-09-21).
"$BIN" plugin codex.lift-tools off 5m >"$SANDBOX/off-lift.log" 2>&1
sleep 1.5
BEFORE=$(command grep -c 'lifted ' "$LOG" 2>/dev/null || echo 0)
RESET
check "with it off, still 200 as usual" "$(POST "$SANDBOX/req-old.json" "$SANDBOX/out-off-lift.sse")" "200"
check "the upstream **did not** receive top-level tools (lifting really stopped)" \
  "$(UPSAW 'json.dumps(body.get("tools"))')" "null"
hasnt "the response was not rewritten either (stopping only one side breaks the client)" "custom_tool_call" "$SANDBOX/out-off-lift.sse"
AFTER=$(command grep -c 'lifted ' "$LOG" 2>/dev/null || echo 0)
check "not one lift log line for this request" "$AFTER" "$BEFORE"
"$BIN" plugin codex.lift-tools on >"$SANDBOX/on-lift.log" 2>&1
sleep 1.5
RESET
check "turning it back on lifts as usual" "$(POST "$SANDBOX/req-old.json" "$SANDBOX/out-on-lift.sse")" "200"
check "top-level tools are visible again" \
  "$(UPSAW "$SHAPE")" '[["function", "exec"], ["function", "wait"]]'

echo
echo "Result: $PASS passed, $FAIL failed"
[ "$FAIL" = "0" ]
