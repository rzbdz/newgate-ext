#!/usr/bin/env bash
# Zero-token end to end: **Codex (Responses dialect) and Claude (messages dialect) get the same migration and
# patches**. The reason this e2e exists is one sentence from the user on 2026-09-23:
#
#   「from now on you must run full codex test, make sure code adaption is good as
#    claude one.」／「including all response request, have same migration as claude
#    anthropic messages request.」
#
# That is: every two-handed move done for the messages dialect (rewrite when the thinking effort is unsupported, lossy rebuild
# when taking over an unclosed tool loop across upstreams) must have an equivalent on the Responses dialect, and **someone must verify it**.
# Writing it twice and testing only one side is exactly what this e2e guards against - all four things it locks are in the "does not error"
# class, green with zero information:
#
#   1  **the thinking-effort move**. When the model "always thinks" and the request writes a value it does not accept (medium/minimal/none),
#      `reasoning.effort` must be rewritten to low. The symptom of a failed rewrite is not an error, but **the upstream stuffing the failure
#      into the 200 of the event stream** (stream:true -> `response.failed`), and the client only sees
#      "stream disconnected before completion" - which is exactly the sentence the user reported on 2026-09-22.
#   2  **the first request after a restart/upgrade**. The quirk table lives in memory, and failures on the streaming path never reach
#      `learnQuirks` (it only looks at status>=400, and streaming is 200). So this move must also recognize
#      the on-disk probe-capabilities.json cache, otherwise the first request after every restart is necessarily 400, and it fixes itself
#      later - the hardest class of failure to reproduce and to explain.
#   3  **the cross-upstream migration move**. DeepSeek steadily returns 400 when taking over another vendor's unclosed reasoning/tool state
#      (A/B measurement in the modules/deepseek/st-reasoning.go file header), and it must append a plain user instruction
#      to the end of input[]. For this move to work, **three** things must be right: the observation side records the call_id's
#      origin (thinkcache), the request side recognizes "the tail is function_call_output" (ContinuationOrigin),
#      and the migration side really adds something into input[] (RebaseToolLoop). Missing one, the symptom is **silence**:
#      the request goes out as is, the client gets the upstream's 400, and the log shows no sign that we should have stepped in.
#   4  **do not touch a single byte that should not be touched**. An effort the upstream accepted (low/high/max), an unrecognizable shape
#      (effort missing, `summary:auto`), a call_id of unknown origin - all pass through - "rewriting even what was accepted"
#      lowers the user's tier, and "guess when unrecognizable" adds fields to unrelated requests at random; both are worse than no fix.
#
# The fake upstream reuses the core's `core/mock/fake_upstream.py` by path (not copied: it is a byte-for-byte replica of real
# upstream behavior, a copy inevitably drifts, and what drifts out is a "green fake test"). Based on the 2026-09-22
# measurement it replicates the two `reasoning.effort` 400s - **including the streaming branch opening the stream with 200 first, then stuffing the failure
# into `response.failed`**, which is exactly the shape the non-streaming part of this e2e cannot verify.
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
SANDBOX="${NEWGATE_E2E_SANDBOX:-$(mktemp -d /tmp/newgate-codexresp.XXXXXX)}"
# Ports are spaced away from the other e2e's: several can run at once.
UP_PORT="${NEWGATE_E2E_UP_PORT:-18099}"
PORT="${NEWGATE_E2E_CODEXRESP_PORT:-18100}"
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
  # After graceful handover the port belongs to a new process (pid/lock move to it as well), and it is not in this script's
  # process group, so the two kills above cannot reach it - if it is not collected, the sandbox's port stays occupied forever, and the next
  # run of this e2e turns into "the new process never starts", with the symptom being **a screen full of unrelated failures** (beyond the "port
  # occupied" line in the log there is no clue at all; measured and hit on 2026-09-23).
  if [ -n "${HANDED_OVER:-}" ]; then
    stop_handed_over
  fi
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
# Two upstreams: A (`up`, neither the name nor the base contains deepseek, so the DeepSeek plugin does not claim it) is
# where the tool loop's **origin** is produced; B (`deepseek`) is the one taking over. The first request goes to A, then this chain
# is switched to B - "the same tool loop now served by another upstream" is exactly the scene cross-upstream migration handles.
cat > "$NEWGATE_HOME/providers.json" <<EOF
{"providers":{
  "up":{"protocol":"openai","base_url":"http://127.0.0.1:$UP_PORT/v1","api_key":"sk-demo"},
  "deepseek":{"protocol":"openai","base_url":"http://127.0.0.1:$UP_PORT/v1","api_key":"sk-demo"}}}
EOF
cat > "$NEWGATE_HOME/mappings/ds.json" <<'JSON'
{"description":"codex responses parity e2e",
 "roles":{"normal":[{"provider":"up","model":"mock-normal"}]}}
JSON
printf '{"default_profile":"ds","port":%s}' "$PORT" > "$NEWGATE_HOME/state.json"

# graceful_handover: hand the socket to a new process via **the upgrade path** (daemon.SpawnHandoff).
#
# Why this e2e cannot do without it: the failure the user reported happened on **the first request after an upgrade**, and an upgrade
# goes through handover (new process starts, old process drains), not "kill and restart". The two paths have exactly the same requirements
# on when to persist, yet the code does not overlap at all - test only one, and an ordering bug on the other (such as "hand off before
# persisting") is undetectable. Measured and hit on 2026-09-23: with that ordering the new process still reads the old cache,
# and the first request after the upgrade is still 400.
graceful_handover() {
  HANDED_OVER=1
  local tok
  tok=$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["control_token"])' "$NEWGATE_HOME/state.json")
  curl -s -X POST -H "Authorization: Bearer $tok" "http://127.0.0.1:$PORT/__newgate/upgrade" >"$SANDBOX/upgrade$1.json"
  # The new process takes a while to come up; SERVE_PID is already dead at this point (the old process os.Exit(0)s, it is **not**
  # our child process anymore - handover moves pid/lock to the new process).
  for _ in $(seq 1 80); do
    curl -sf -o /dev/null "http://127.0.0.1:$PORT/__newgate/status" && return 0
    sleep 0.15
  done
  echo "port did not come up after handover, see $SANDBOX/upgrade$1.json"; return 1
}

# After handover the port belongs to the new process, and the kill in cleanup cannot reach it - collect it by pidfile.
stop_handed_over() {
  local pid
  pid=$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["pid"])' "$NEWGATE_HOME/.newgate.pid" 2>/dev/null) || return 0
  [ -n "$pid" ] && kill "$pid" 2>/dev/null
  HANDED_OVER=""
}

# wait_serve_down stops the __serve it started cleanly.
#
# Why not just `kill` + `wait`: after __serve receives the signal it must first **drain in-flight requests** and then exit,
# and starting a new process right away collides with a port that has not been released yet - the new process fails to bind and dies at once, and the symptom left behind is
# "every section after this is inexplicably broken" (measured 2026-09-23: after deleting the cache the first request was still saved by what was learned,
# making it look like the mechanism did not work, when in fact the new process never started and the old one was still alive).
#
# Here we wait until the port really **refuses to connect**, then hand back to the caller to start the next one. SIGKILL is the fallback: when draining
# hangs, the whole e2e must not be stuck there.
wait_serve_down() {
  [ -n "${SERVE_PID:-}" ] && kill "$SERVE_PID" 2>/dev/null
  # After handover (graceful_handover) the port belongs to **another** process, which is neither in SERVE_PID nor in
  # this script's process group. If it is not collected, the new process started by start_serve below fails to bind and dies at once,
  # and every request after that still talks to the old process - which carries in memory what the previous section learned, so
  # "the first request after deleting the cache" looks like the mechanism did not work (hit this pitfall on 2026-09-23).
  stop_handed_over
  for _ in $(seq 1 60); do
    if ! curl -sf -o /dev/null "http://127.0.0.1:$PORT/__newgate/status"; then
      wait "$SERVE_PID" 2>/dev/null
      return 0
    fi
    sleep 0.1
  done
  kill -9 "$SERVE_PID" 2>/dev/null
  wait "$SERVE_PID" 2>/dev/null
}

start_serve() {
  "$BIN" __serve --port "$PORT" >"$SANDBOX/serve$1.log" 2>&1 &
  SERVE_PID=$!
  for _ in $(seq 1 50); do
    curl -sf -o /dev/null "http://127.0.0.1:$PORT/__newgate/status" && return 0
    sleep 0.1
  done
  echo "daemon did not start, see $SANDBOX/serve$1.log"; return 1
}
start_serve 1 || exit 1
LOG="$NEWGATE_HOME/newgate.log"

# POST <request body file> <output file> -> HTTP status code. The agent segment cannot be omitted: `special.Request.Agent`
# comes from /a/codex/, and that is the premise for the patch claiming on the codex path (see mock/e2e_codex_tools.sh).
POST() {
  curl -sS -o "$2" -w '%{http_code}' -X POST \
    "http://127.0.0.1:$PORT/a/codex/p/ds/v1/responses" \
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
# After a request, wait for the log to land on disk (note and response are written on the same thread, the log is append-only).
settle() { sleep 1; }

write_req() { # write_req <file> <JSON>
  printf '%s' "$2" > "$1"
}

echo
echo "== 1. cold start: the quirk table is empty -> not a byte is touched, the upstream returns 400 by the real rule =="
# The rule is "this (provider, model) is known to always think" - if we have not learned it, we must not act (guessing at a request
# is worse than not changing it). So the first 400 is **expected**, and it is exactly the source of learning.
write_req "$SANDBOX/req-medium.json" \
  '{"model":"normal","stream":false,"reasoning":{"effort":"medium"},"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}]}'
RESET
check "not learned -> sent as is -> upstream 400" \
  "$(POST "$SANDBOX/req-medium.json" "$SANDBOX/out1.json")" "400"
check "the effort the upstream received is still the medium the client wrote (no guessing without learning)" \
  "$(UPSAW 'body["reasoning"]["effort"]')" "medium"
check "the 400 body is the upstream's own sentence" \
  "$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["error"]["message"])' "$SANDBOX/out1.json")" \
  "该模型始终思考，不支持关闭思考；请使用 low、high 或 max。"
settle
has "the log recorded what was learned from this request (not silent)" "learned up/mock-normal" "$LOG"

echo
echo "== 2. send the same request again: learned -> rewritten in place to low, upstream 200 =="
RESET
check "same request 200 after learning" \
  "$(POST "$SANDBOX/req-medium.json" "$SANDBOX/out2.json")" "200"
check "the effort the upstream received was rewritten to low" \
  "$(UPSAW 'body["reasoning"]["effort"]')" "low"
check "only that one value moved: sibling keys in the same object and every other byte are there" \
  "$(UPSAW 'json.dumps(body["reasoning"], sort_keys=True)')" '{"effort": "low"}'
settle
has "the log reported this rewrite (changing a request must leave a trace)" 'reasoning.effort "medium" → "low"' "$LOG"

echo
echo "== 3. values the upstream accepts are not touched by a single byte (low/high/max) =="
# Accepting only low would rewrite the high/max the user explicitly set to low - that lowers the user's tier, a silent behavior change.
for e in low high max; do
  write_req "$SANDBOX/req-$e.json" \
    "{\"model\":\"normal\",\"stream\":false,\"reasoning\":{\"effort\":\"$e\"},\"input\":[{\"type\":\"message\",\"role\":\"user\",\"content\":[{\"type\":\"input_text\",\"text\":\"hi\"}]}]}"
  RESET
  check "effort=$e still 200" "$(POST "$SANDBOX/req-$e.json" "$SANDBOX/out-$e.json")" "200"
  check "effort=$e sent as is" "$(UPSAW 'body["reasoning"]["effort"]')" "$e"
done

echo
echo "== 4. unrecognizable shape means no guessing (effort missing / summary:auto / effort:null) =="
for name in absent summary null; do
  case "$name" in
    absent)  r='{"model":"normal","stream":false,"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}]}' ;;
    summary) r='{"model":"normal","stream":false,"reasoning":{"summary":"auto"},"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}]}' ;;
    null)    r='{"model":"normal","stream":false,"reasoning":{"effort":null},"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}]}' ;;
  esac
  write_req "$SANDBOX/req-shape-$name.json" "$r"
  RESET
  check "$name still 200" "$(POST "$SANDBOX/req-shape-$name.json" "$SANDBOX/out-shape-$name.json")" "200"
  check "$name the reasoning the upstream received is identical to what the client sent" \
    "$(UPSAW 'json.dumps(body.get("reasoning"), sort_keys=True)')" \
    "$(python3 -c 'import json,sys; print(json.dumps(json.load(open(sys.argv[1])).get("reasoning"), sort_keys=True))' "$SANDBOX/req-shape-$name.json")"
done

echo
echo "== 5. the first request after an upgrade/restart (streaming): only the on-disk cache can save it =="
# This section is the **verbatim reproduction** of the failure the user reported on 2026-09-22: the quirk table lives in memory and is empty after a restart;
# and a streaming failure is "HTTP 200 + one response.failed in the event stream", while `learnQuirks` only looks at
# status>=400, so it can never learn. Hence "the first request after a restart is necessarily 400 and fixes itself later".
#
# Switch to a real process restart (stop cleanly + start), then hand-write a probe-capabilities.json - exactly
# what one `newgate probe` run leaves behind, and what `probe.LoadCachedCapabilities` reads back
# (see matchTarget in core/modules/thinking/st-always.go).
wait_serve_down
cat > "$NEWGATE_HOME/probe-capabilities.json" <<'JSON'
{"targets":{"up/mock-normal":{"supports":0,"known":0,"quirks":1,"checked_at":"2026-09-23T00:00:00Z"}}}
JSON
start_serve 2 || exit 1
write_req "$SANDBOX/req-stream.json" \
  '{"model":"normal","stream":true,"reasoning":{"effort":"medium"},"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}]}'
RESET
check "the first streaming request after a restart is 200 as usual (the HTTP status code was never the rule on this path)" \
  "$(POST "$SANDBOX/req-stream.json" "$SANDBOX/out-stream.sse")" "200"
has "the stream ends normally (response.completed), not that response.failed" \
  'response.completed' "$SANDBOX/out-stream.sse"
hasnt "the stream has no response.failed (the 'stream disconnected before completion' the user saw is it)" \
  'response.failed' "$SANDBOX/out-stream.sse"
check "the effort the upstream received was rewritten to low (the cache path really connected)" \
  "$(UPSAW 'body["reasoning"]["effort"]')" "low"

echo
echo "== 5b. what was learned survives **an upgrade**: the new process after handover must not hit it on the first request =="
# Section 5 verified "a new process can read back a file on disk", but that file was **hand-written** by the test. This section
# verifies something else, and it is the one the user actually hit: **what is learned gets written down by itself**.
#
# Both paths must persist (the upgrade path flushes before handing off, a normal shutdown after draining), and missing one the symptom is
# "the first request after an upgrade is 400 again" - reproducible every time, with no surface cause (what the user reported on 2026-09-22
# is exactly that sentence: `stream disconnected before completion`).
#
# Start from a clean home directory, so that "something is on disk" can only be something we wrote ourselves.
rm -f "$NEWGATE_HOME/probe-capabilities.json"
wait_serve_down
start_serve 5 || exit 1
check "the first request of a fresh process (empty table): sent as is, upstream 400" \
  "$(POST "$SANDBOX/req-medium.json" "$SANDBOX/out-cold.json")" "400"
RESET
check "the second request 200 (the 400 before it has already been learned)" \
  "$(POST "$SANDBOX/req-medium.json" "$SANDBOX/out-warm.json")" "200"
check "the upstream received low on the second request (what was learned really got used)" \
  "$(UPSAW 'body["reasoning"]["effort"]')" "low"

graceful_handover 5 || exit 1
check "after the upgrade there really is one entry on disk that we learned ourselves (the hand-written one has been deleted)" \
  "$(python3 -c 'import json,sys; d=json.load(open(sys.argv[1])); print(d["targets"]["up/mock-normal"]["quirks"])' "$NEWGATE_HOME/probe-capabilities.json")" "1"
RESET
check "**the first request after the upgrade is 200** (this used to be necessarily 400: the in-memory table was empty and persisting only happened during probes)" \
  "$(POST "$SANDBOX/req-medium.json" "$SANDBOX/out-after-upgrade.json")" "200"

echo
echo "== 5c. learn **from the stream only**: the upstream stuffs the rejection into the 200, and the status-code path cannot reach it =="
# Sections 5 and 5b both use non-streaming requests, so that 400 goes through the HTTP status code and `learnQuirks` can learn it.
# But the scene the user reported is **streaming only**: Codex always sends `stream:true`, the upstream opens the stream with 200 first, then stuffs the rejection
# into the event stream (`response.failed`). A rule written against status codes never fires on this path - the table stays
# empty forever, the patch never takes effect, and what the user sees is that sentence "stream disconnected before completion".
#
# This section is its verbatim reproduction: starting from a clean home directory, **the very first request is streaming**, and nobody may hand-write a cache.
# The order is **stop first, delete after**; the other way round tests nothing: the shutdown step itself writes back to disk the problems learned along the way
# (that is exactly the mechanism 5b verifies), so what was deleted immediately grows back - the first request gets saved by what the previous section learned,
# and this section becomes a fake test that is always green.
wait_serve_down
rm -f "$NEWGATE_HOME/probe-capabilities.json"
start_serve 6 || exit 1
write_req "$SANDBOX/req-stream-medium.json" \
  '{"model":"normal","stream":true,"reasoning":{"effort":"medium"},"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}]}'
RESET
check "cold-start streaming first request: HTTP **200** (the status code says nothing on this path)" \
  "$(POST "$SANDBOX/req-stream-medium.json" "$SANDBOX/out-stream-cold.sse")" "200"
has "and the stream has that failure (that is what the client sees)" 'response.failed' "$SANDBOX/out-stream-cold.sse"
check "the effort the upstream received is still medium (no guessing without learning)" \
  "$(UPSAW 'body["reasoning"]["effort"]')" "medium"
settle
has "the log left a line saying the upstream reported a failure in the stream (not silent: the user must be able to look it up afterwards)" \
  'reported a failure inside the event stream' "$LOG"
has "and it recorded what was learned from this request" "learned up/mock-normal" "$LOG"

RESET
check "the streaming request right after: 200" \
  "$(POST "$SANDBOX/req-stream-medium.json" "$SANDBOX/out-stream-warm.sse")" "200"
hasnt "the second stream has no such failure (the learned patch really got used)" \
  'response.failed' "$SANDBOX/out-stream-warm.sse"
has "the second request ends normally" 'response.completed' "$SANDBOX/out-stream-warm.sse"
check "the effort the upstream received was rewritten to low" \
  "$(UPSAW 'body["reasoning"]["effort"]')" "low"

# what is learned must survive an upgrade - **on this path** especially, because even the "hit a 400" learning opportunity
# only shows up the first time: from the second request on the upstream accepts it.
graceful_handover 6 || exit 1
RESET
check "the first request after the upgrade is 200 (this used to reliably produce 'stream disconnected before completion')" \
  "$(POST "$SANDBOX/req-stream-medium.json" "$SANDBOX/out-stream-after.sse")" "200"
hasnt "the stream after the upgrade has no response.failed either" 'response.failed' "$SANDBOX/out-stream-after.sse"

echo
echo "== 6. cross-upstream migration: the same tool loop moves to DeepSeek taking over =="
# Step one: the tool loop's **origin** is produced at A (`up`). This request must really carry tools for the fake upstream to
# return a function_call - thinkcache records the origin with its call_id (CommitWithOrigin).
write_req "$SANDBOX/req-turn1.json" \
  '{"model":"normal","stream":false,
    "input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"go"}]}],
    "tools":[{"type":"function","name":"exec_command","description":"run","parameters":{"type":"object","properties":{"cmd":{"type":"string"}}}}]}'
RESET
check "first round 200" "$(POST "$SANDBOX/req-turn1.json" "$SANDBOX/out-turn1.json")" "200"
# The call_id the fake upstream returns is **predictable** here (call_mock_<n>), so the next round can copy it.
check "the function_call coming back from the upstream carries a call_id (the next round copies it)" \
  "$(python3 -c 'import json,sys; o=json.load(open(sys.argv[1]))["output"]; print([i["call_id"] for i in o if i["type"]=="function_call"][0])' "$SANDBOX/out-turn1.json")" \
  "call_mock_1"
settle
has "the log reported that this round recorded the reasoning and its origin" "recorded" "$LOG"

# Step two: switch this tier to DeepSeek - the scene is "this same tool loop is now served by another vendor".
# Changing mappings takes effect immediately (config hot reload), no restart needed.
cat > "$NEWGATE_HOME/mappings/ds.json" <<'JSON'
{"description":"codex responses parity e2e (now deepseek)",
 "roles":{"normal":[{"provider":"deepseek","model":"mock-normal"}]}}
JSON
sleep 1.5

write_req "$SANDBOX/req-turn2.json" \
  '{"model":"normal","stream":false,
    "input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"go"}]},
             {"type":"function_call","call_id":"call_mock_1","name":"exec_command","arguments":"{}"},
             {"type":"function_call_output","call_id":"call_mock_1","output":"ok"}]}'
RESET
check "second round 200" "$(POST "$SANDBOX/req-turn2.json" "$SANDBOX/out-turn2.json")" "200"
check "the input[] the upstream received has one more item (the lossy rebuild really acted)" \
  "$(UPSAW 'len(body["input"])')" "4"
check "the extra item is **at the end**, is a plain user message, and its body is word for word continue" \
  "$(UPSAW 'json.dumps(body["input"][-1], sort_keys=True)')" \
  '{"content": [{"text": "continue", "type": "input_text"}], "role": "user", "type": "message"}'
check "the original three items are not changed by a single character (all the old reasoning/tool stays as visible context)" \
  "$(UPSAW 'json.dumps(body["input"][:3], sort_keys=True)')" \
  "$(python3 -c 'import json,sys; print(json.dumps(json.load(open(sys.argv[1]))["input"], sort_keys=True))' "$SANDBOX/req-turn2.json")"
settle
has "the log reported this lossy rebuild (changing a request must leave a trace)" \
  "lossily rebuilt a foreign tool loop" "$LOG"

echo
echo "== 7. unknown origin means do not touch: when that call_id has not been seen, migration must not add a single byte =="
# The premise of migration is that **we know** who produced this round's tool call. After a restart thinkcache is empty,
# and "unrecognized origin" can only be handled conservatively at that point - better to let the upstream error as is than to guess and stuff a sentence
# into the user's history (that continue would live in the conversation history forever).
#
# This section must turn **move 5** (deepseek.tail-shape-responses) off on its own, otherwise it does not verify precisely:
# it does the same thing as migration (no new instruction at the tail -> append a continue), but its rule is different - it
# only looks at the **shape** of the origin (any id other than `call_NN_…` counts as foreign), and `call_mock_9`, which we have never seen,
# is exactly foreign in its eyes, so it would append one too. The two added together give "two appended", and what this section
# asks is "does the migration move flail around when there is no origin". With move 5 off, the only thing that can add
# anything to input[] is migration, and only then does the count mean something.
wait_serve_down
start_serve 3 || exit 1
"$BIN" plugin deepseek.tail-shape-responses off 5m >"$SANDBOX/off-tail5.log" 2>&1
sleep 1.5   # wait for one config reload (the switch and providers.json go the same way)
write_req "$SANDBOX/req-turn3.json" \
  '{"model":"normal","stream":false,
    "input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"go"}]},
             {"type":"function_call","call_id":"call_mock_9","name":"exec_command","arguments":"{}"},
             {"type":"function_call_output","call_id":"call_mock_9","output":"ok"}]}'
RESET
POST "$SANDBOX/req-turn3.json" "$SANDBOX/out-turn3.json" >/dev/null
settle
check "the upstream received three items, not one more (unknown origin -> migration does not trigger)" \
  "$(UPSAW 'len(body["input"])')" "3"
BEFORE=$(command grep -cF "lossily rebuilt a foreign tool loop" "$LOG" 2>/dev/null || echo 0)
check "the log does not show an extra migration either (none beyond the one in section 6)" "$BEFORE" "1"
"$BIN" plugin deepseek.tail-shape-responses on >"$SANDBOX/on-tail5.log" 2>&1
sleep 1.5

echo
echo "result: $PASS passed, $FAIL failed"
[ "$FAIL" = "0" ]
