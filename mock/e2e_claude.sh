#!/usr/bin/env bash
# Zero-token end to end: **the kernel's own** behavior -- takeover injection, tier resolution and forwarding, the control endpoint,
# graceful handover, background classifier rerouting, window declarations, runtime switches.
#
# What it verifies (corresponding to this feature/fix):
#   1. The env injected at startup is the **real model name** (the claude UI shows deepseek-chat,
#      not heavy), and the base URL carries /a/claude/p/<profile>.
#   2. The proxy resolves the real model name back to a tier and forwards it to the right upstream.
#   3. count_tokens: Claude Code calls it periodically (the context gauge / auto-compact threshold). If the upstream
#      understands it (native anthropic endpoint), forward it and get the real value, filling in model from the mid chain head;
#      if it does not (an aggregator 404s), the forward layer's lazy probe learns that and falls back to a local rough estimate
#      (covered by unit tests).
#   4. The control endpoint /__newgate/stop: in a multi-user shared deployment, a user who can read the config but cannot send
#      a signal uses it to stop -- wrong token is 403, the right token lets the daemon exit cleanly.
#   5. Graceful handover /__newgate/upgrade (nginx-style zero-downtime upgrade): restart hands the listening
#      socket to the new process, and in-flight SSE streams finish on the old process -- the session developing newgate
#      itself runs through the proxy, so this is the precondition for "being able to keep developing".
#   6. Background requests: the classifier's whole chain reroutes to light, everything else only gets thinking disabled. Claude Code's non-streaming
#      background calls carry no thinking, but domestic models treat absence as "think by default" -> 15-30 seconds, failing in waves with
#      timeouts. The proxy always adds thinking:disabled (add it when missing, rewrite it when present); among them the Bash
#      safety classifier itself (captured traits: the system starts with "You are a security monitor...")
#      reroutes to the light tier **before the chain is built** -- fallback included, so if light is down it moves along the light chain to
#      someone else, never back to mid; other background calls (compact summaries and the like) do not reroute; the main loop's streaming
#      requests are unaffected.
#   7. Window declarations: Claude Code does not recognize the injected model names (glm-4-plus / normal),
#      and assumes a window for an "unknown model". When the profile declares context_window/
#      auto_compact_window, the corresponding CLAUDE_CODE_* env is injected at startup; but
#      the Claude Code client caps the window at 512K and compact at 400K (2026-11-12
#      on site: 1M went to the last cell of the real window, and after 400 even /compact could not be sent).
#      A profile that declares no window gets the cap value, and one that declares no compact leaves it to the client.
#   8. Runtime switches and special-layer switches (chapters 19/20): the probe uses the kernel's own
#      schema-repair, so this layer does not depend on any distribution module.
#
# # Distribution module behavior is not here (split apart 2026-09-20)
#
# The upstream quirk patches (DeepSeek's reasoning echo, tail-shape repair, cross-upstream migration) follow their modules
# and live in the distribution repository, locked by **the distribution's mock/e2e_claude_dist.sh** -- it reuses this directory's
# fake upstream (which is a byte-for-byte replica of real upstream behavior; a copy would inevitably drift). Before the split those
# chapters were in this file, so **the kernel's tests depended on a distribution module**: remove it and the kernel went red. The boundary and
# the reasons are in docs/09-extension-guide.md §8.
#
# Everything runs in a temporary sandbox, touching neither the real ~/.config / ~/.claude / shell rc.
# # Why this script lives in the distribution repository (moved here 2026-09-20)
#
# It used to be the kernel's mock/e2e_claude.sh. All these chapters go through **real client takeover** (newgate
# claude → wrapper → runtime → fake claude), and client takeover is a product trade-off -- those three modules
# (claudecode / opencode / opencodeomo) moved to this repository, so their end-to-end tests moved along.
#
# What is left on the kernel side is the e2e for the **mechanism surface** (curl against the data plane + control endpoint + runtime switches),
# and coverage is not lost: together the two sides are the original 16 chapters, with the split drawn along "who knows the client".

set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
# The binary comes from this repository's build entry point (build/build.sh); **drop a copy named newgate** before using it:
# it is multi-invocation, and argv0 decides the entry point (see the README). The arch in the artifact name uses Go's naming.
CORE="${NEWGATE_E2E_CORE:-$ROOT/core}"
case "$(uname -m)" in
  x86_64)        host_arch=amd64 ;;
  aarch64|arm64) host_arch=arm64 ;;
  i386|i686)     host_arch=386 ;;
  *)             host_arch=$(uname -m) ;;
esac
BIN_SRC="${NEWGATE_E2E_BIN:-$ROOT/dist/newgate-default-$(uname -s | tr 'A-Z' 'a-z')-$host_arch}"
[ -x "$BIN_SRC" ] || { echo "Binary not found: $BIN_SRC -- run build/build.sh first"; exit 1; }
[ -f "$CORE/mock/fake_upstream.py" ] || { echo "No fake upstream in core/: first git submodule update --init"; exit 1; }
SANDBOX="${NEWGATE_E2E_SANDBOX:-$(mktemp -d /tmp/newgate-claude-e2e.XXXXXX)}"
BIN="$SANDBOX/bin/newgate"
mkdir -p "$(dirname "$BIN")"
cp "$BIN_SRC" "$BIN"
UP_PORT="${NEWGATE_E2E_UP_PORT:-18081}"
PROXY_PORT="${NEWGATE_E2E_PROXY_PORT:-18898}"
FAKEBIN="$SANDBOX/fakebin"

PASS=0; FAIL=0
ok()   { echo "  ✓ $1"; PASS=$((PASS+1)); }
bad()  { echo "  ✗ $1"; FAIL=$((FAIL+1)); }
check(){ if [ "$2" = "$3" ]; then ok "$1"; else bad "$1 (expected '$3', got '$2')"; fi; }

export NEWGATE_HOME="$SANDBOX/ng"
mkdir -p "$NEWGATE_HOME/mappings" "$FAKEBIN"
# The sandbox must be sealed: the session running the e2e may itself carry window declarations injected by newgate (when started
# nested the parent's env leaks into child processes); not unsetting them makes the "profile with no declaration"
# case read the parent session's values and fail spuriously.
unset CLAUDE_CODE_MAX_CONTEXT_TOKENS CLAUDE_CODE_AUTO_COMPACT_WINDOW
# The language must be sealed too: the script asserts **source-language text** (English), while the machine running it may be zh-Hans
# (our own machines are). Without pinning it, "the UI says X in English" assertions fail spuriously on a Chinese machine.
export NEWGATE_LANG=en
unset LC_ALL LC_MESSAGES LANG LANGUAGE 2>/dev/null || true

cleanup() {
  "$BIN" stop >/dev/null 2>&1 || true
  [ -n "${UP_PID:-}" ] && kill "$UP_PID" 2>/dev/null
  # For debugging: NEWGATE_E2E_KEEP=1 keeps the sandbox (logs and the bodies the fake upstream received are in there),
  # so a red run does not leave you guessing against a deleted directory.
  if [ -n "${NEWGATE_E2E_KEEP:-}" ]; then
    echo "Sandbox kept (NEWGATE_E2E_KEEP): $SANDBOX"
  else
    rm -rf "$SANDBOX"
  fi
}
trap cleanup EXIT

echo "Sandbox: $SANDBOX"
[ -x "$BIN" ] || { echo "run make build first"; exit 1; }

# ---- fake claude: send requests the way Claude Code does. E2E_SCENARIO picks the shape ----
cat > "$FAKEBIN/claude" <<'PY'
#!/usr/bin/env python3
import json, os, urllib.request, urllib.error

def g(k):
    return os.environ.get(k, "")

scenario = os.environ.get("E2E_SCENARIO", "plain")
base, model = g("ANTHROPIC_BASE_URL"), g("ANTHROPIC_DEFAULT_OPUS_MODEL")

TOOLS = [{"name": "Read", "description": "read a file",
          "input_schema": {"type": "object",
                           "properties": {"path": {"type": "string"}}}}]

def post(url, payload):
    req = urllib.request.Request(url, data=json.dumps(payload).encode(), method="POST")
    req.add_header("Content-Type", "application/json")
    req.add_header("x-api-key", "newgate-local")
    req.add_header("anthropic-version", "2023-06-01")
    op = urllib.request.build_opener(urllib.request.ProxyHandler({}))
    try:
        r = op.open(req, timeout=20)
        return r.status, r.read().decode()
    except urllib.error.HTTPError as e:
        return e.code, e.read().decode()

if scenario == "plain":
    print(f"OPUS_MODEL={model}")
    print(f"SONNET_MODEL={g('ANTHROPIC_DEFAULT_SONNET_MODEL')}")
    print(f"WIN_MAX={g('CLAUDE_CODE_MAX_CONTEXT_TOKENS')}")
    print(f"WIN_COMPACT={g('CLAUDE_CODE_AUTO_COMPACT_WINDOW')}")
    print(f"BASE_URL={base}")
    if base and model:
        code, _ = post(base.rstrip("/") + "/v1/messages",
                       {"model": model, "max_tokens": 16,
                        "messages": [{"role": "user", "content": "ping"}]})
        print(f"HTTP={code}")
elif scenario in ("think1", "think2", "think3"):
    # Claude Code main-loop shape: **streaming** + thinking explicitly on + tools present,
    # and the assistant messages in history have had their thinking blocks stripped by the client -- only tool_use is left.
    # (Streaming is the key: claude-bg only rewrites non-streaming background calls, and leaves the main loop alone.)
    tid = "toolu_mock_1" if scenario == "think2" else "toolu_UNSEEN_999"
    msgs = [{"role": "user", "content": "go"}]
    if scenario != "think1":
        msgs += [{"role": "assistant", "content": [
                      {"type": "tool_use", "id": tid, "name": "Read",
                       "input": {"path": "x"}}]},
                 {"role": "user", "content": [
                      {"type": "tool_result", "tool_use_id": tid,
                       "content": "ok"}]}]
    code, body = post(base.rstrip("/") + "/v1/messages",
                      {"model": model, "max_tokens": 64, "stream": True,
                       "thinking": {"type": "enabled", "budget_tokens": 1024},
                       "tools": TOOLS, "messages": msgs})
    print(f"HTTP={code}")
    if code != 200:
        print(body[:200])
elif scenario == "count_tokens":
    # Claude Code's count_tokens: the shape without a model field must also survive
    code, body = post(base.rstrip("/") + "/v1/messages/count_tokens",
                      {"messages": [{"role": "user", "content": "count the tokens"}],
                       "tools": TOOLS})
    print(f"HTTP={code}")
    try:
        print(f"INPUT_TOKENS={json.loads(body).get('input_tokens')}")
    except Exception:
        print("INPUT_TOKENS=PARSE_FAIL")
elif scenario in ("bg_plain", "bg_adaptive", "bg_other", "bg_realname"):
    # The shape of Claude Code's small background calls (captured 2026-09, cc 2.1.263): non-streaming,
    # model is the tier name mid, no tools, max_tokens 2112.
    #   bg_plain / bg_adaptive / bg_realname = the Bash classifier itself: system ~126KB
    #     starting with "You are a security monitor..." (bg_plain does not even write thinking,
    #     bg_adaptive explicitly asks for adaptive) -> all switch to light; disabled is injected only into
    #     bg_plain, which wrote no thinking; an explicit intent is not overridden.
    #   bg_other = other background calls (compact summaries and the like): the system lacks that self-identifying
    #     line -> stays on mid, only thinking is disabled.
    #   bg_realname = the regression case from the real-model-name era (captured 2026-09-09): the classifier
    #     names the main-loop slot's real name (glm-4-plus), which is bound to both heavy+mid and resolves
    #     back to heavy in Roles order -- the tier-gate version skips it entirely. It must still switch to light.
    payload = {"model": "mid", "max_tokens": 2112,
               "messages": [{"role": "user", "content": "classify this command"},
                            {"role": "user", "content": "and this one"}]}
    if scenario == "bg_realname":
        payload["model"] = "glm-4-plus"
    if scenario != "bg_other":
        payload["system"] = [{"type": "text", "text":
            "You are a security monitor for autonomous AI coding agents."}]
    else:
        payload["system"] = [{"type": "text", "text":
            "Summarize this conversation for context compaction."}]
    if scenario == "bg_adaptive":
        payload["thinking"] = {"type": "adaptive", "budget_tokens": 512}
    code, body = post(base.rstrip("/") + "/v1/messages", payload)
    print(f"HTTP={code}")
    if code != 200:
        print(body[:200])
PY
chmod +x "$FAKEBIN/claude"

# ---- config: two providers, ds + glm, pointing at the fake upstream ----
cat > "$NEWGATE_HOME/providers.json" <<EOF
{
  "providers": {
    "ds":  { "base_url": "http://127.0.0.1:$UP_PORT/v1", "api_key": "sk-ds",  "protocol": "anthropic" },
    "glm": { "base_url": "http://127.0.0.1:$UP_PORT/v1", "api_key": "sk-glm", "protocol": "anthropic" }
  }
}
EOF
cat > "$NEWGATE_HOME/mappings/ds.json" <<'EOF'
{ "name": "ds", "priority": 10, "roles": {
    "heavy": "ds/deepseek-chat", "mid": "ds/deepseek-chat",
    "light": "ds/deepseek-chat", "vision": "ds/deepseek-chat" } }
EOF
cat > "$NEWGATE_HOME/mappings/glm.json" <<'EOF'
{ "name": "glm", "priority": 20,
  "context_window": 1000000, "auto_compact_window": 500000,
  "roles": {
    "heavy": "glm/glm-4-plus", "mid": "glm/glm-4-plus",
    "light": "glm/glm-4.5-air", "vision": "glm/glm-4.5-air" } }
EOF
cat > "$NEWGATE_HOME/state.json" <<EOF
{ "default_profile": "ds", "port": $PROXY_PORT }
EOF

echo; echo "== 1. Start the fake upstream (strict DeepSeek semantics) =="
python3 "$CORE/mock/fake_upstream.py" --port "$UP_PORT" >"$SANDBOX/upstream.log" 2>&1 &
UP_PID=$!
for _ in $(seq 30); do
  curl -sf "http://127.0.0.1:$UP_PORT/__mock/requests" >/dev/null && break; sleep 0.1
done
curl -sf "http://127.0.0.1:$UP_PORT/__mock/requests" >/dev/null \
  && ok "fake upstream on 127.0.0.1:$UP_PORT" || { bad "the fake upstream did not start"; exit 1; }

# fakebin is first on PATH, so newgate claude will exec our fake claude.
export PATH="$FAKEBIN:$PATH"

echo; echo "== 2. newgate claude --profile=ds =="
curl -sf "http://127.0.0.1:$UP_PORT/__mock/reset" -X POST >/dev/null
OUT="$("$BIN" claude --profile=ds 2>"$SANDBOX/ds.err")"
echo "$OUT" | sed 's/^/    /'
# The real model name, **bare**: since 2026-11-12 no `[1m]` is appended any more (the 1M declaration pushed the session to
# the last cell of the real window; after a 400 even /compact could not be sent, see modules/claudecode).
# The window is now declared by CLAUDE_CODE_MAX_CONTEXT_TOKENS, capped at 512K. The next line asserts the upstream
# receives exactly this name.
check "the opus tier is injected with the real model name (bare name)" \
  "$(echo "$OUT" | grep '^OPUS_MODEL=' | cut -d= -f2-)" "deepseek-chat"
echo "$OUT" | grep '^BASE_URL=' | grep -q "/a/claude/p/ds" \
  && ok "base URL carries /a/claude/p/ds" \
  || bad "base URL should contain /a/claude/p/ds (actual $(echo "$OUT" | grep '^BASE_URL='))"
GOT=$(curl -s "http://127.0.0.1:$UP_PORT/__mock/requests" | python3 -c 'import json,sys;r=json.load(sys.stdin);print(r[0]["body"]["model"] if r else "NONE")')
check "the upstream received deepseek-chat from ds" "$GOT" "deepseek-chat"

echo; echo "== 3. newgate claude --profile=glm (switch) =="
curl -sf "http://127.0.0.1:$UP_PORT/__mock/reset" -X POST >/dev/null
OUT="$("$BIN" claude --profile=glm 2>"$SANDBOX/glm.err")"
echo "$OUT" | sed 's/^/    /'
check "after switching to glm the opus tier is glm-4-plus (bare name)" \
  "$(echo "$OUT" | grep '^OPUS_MODEL=' | cut -d= -f2-)" "glm-4-plus"
GOT=$(curl -s "http://127.0.0.1:$UP_PORT/__mock/requests" | python3 -c 'import json,sys;r=json.load(sys.stdin);print(r[0]["body"]["model"] if r else "NONE")')
check "the upstream received glm-4-plus from glm" "$GOT" "glm-4-plus"

echo; echo "== 4. Without a profile, use the default (ds): dynamic mode, slot = tier name =="
# In the default (dynamic) mode the slot keeps the tier name -- the session sends the tier name back, and the proxy resolves it
# against the config current at request time, so --set-profile takes effect immediately for running sessions. Window declarations are still injected (the tier name
# is an unknown model for Claude Code too, and MAX_CONTEXT_TOKENS works the same way).
curl -sf "http://127.0.0.1:$UP_PORT/__mock/reset" -X POST >/dev/null
OUT="$("$BIN" claude 2>"$SANDBOX/default.err")"
check "default (dynamic): opus slot = the workhorse tier normal (bare name)" \
  "$(echo "$OUT" | grep '^OPUS_MODEL=' | cut -d= -f2-)" "normal"
check "default (dynamic): window declaration = client cap (ds has none configured -> 512K)" \
  "$(echo "$OUT" | grep '^WIN_MAX=' | cut -d= -f2-)" "512000"

echo; echo "== 5. A nonexistent profile must fail immediately (exit code 65) =="
"$BIN" claude --profile=nope >/dev/null 2>"$SANDBOX/nope.err"
RC=$?
check "exit code 65" "$RC" "65"
grep -q 'No such profile' "$SANDBOX/nope.err" && ok "the error message says the profile does not exist" || bad "the error message does not say the profile does not exist"

echo; echo "== 6. Upstream strictness self-check: the tail shape (not the reasoning field) is what decides the 400 =="
# First prove the fake upstream really enforces **the measured criterion** -- otherwise all those later 200s prove nothing.
#
# The criterion was measured cell by cell on 2026-09-18 against the real smt-deepseek/deepseek-flash, and
# reproduced verbatim from **a real Claude Code capture** (dump/err-400-req000412.client-sent.json,
# 764KB, 229 messages, UA claude-cli/2.1.273):
#
#   as is (tail = bare [tool_result])                 -> 400 reasoning_content...
#   the same body, only the last assistant thinking block removed  -> 400 (the reasoning field is irrelevant)
#   the same body, only a text " " added to the tail content[]      -> 200
#   both                                              -> 200
#
# The error text says "reasoning was not passed back", while the real cause is "no new instruction this round" -- the text and the cause
# disagree, which is exactly why this check must be written by **shape**, not by text.
#
# Three cells: a bare tool_result must be 400; the same + a space text, and + a normal text block,
# must both be 200. Both directions together show where the criterion is drawn (testing only the 400s would let an upstream that
# "blocks everything it sees" pass too).
tail_case() {  # $1=the literal for the tail content[]  $2=expected status code  $3=description
  CODE=$(curl -s -o "$SANDBOX/strict.out" -w "%{http_code}" -X POST \
    "http://127.0.0.1:$UP_PORT/v1/messages" -H 'Content-Type: application/json' \
    -d '{"model":"deepseek-chat","max_tokens":16,"thinking":{"type":"enabled","budget_tokens":1024},"tools":[{"name":"Read","input_schema":{"type":"object"}}],"messages":[{"role":"user","content":[{"type":"text","text":"run it"}]},{"role":"assistant","content":[{"type":"tool_use","id":"toolu_x","name":"Read","input":{}}]},{"role":"user","content":'"$1"'}]}')
  check "tail $3 -> $2" "$CODE" "$2"
}
tail_case '[{"type":"tool_result","tool_use_id":"toolu_x","content":"ok"}]' \
  400 "only tool_result (bare)"
tail_case '[{"type":"tool_result","tool_use_id":"toolu_x","content":"ok"},{"type":"text","text":" "}]' \
  200 "tool_result + one space"
tail_case '[{"type":"tool_result","tool_use_id":"toolu_x","content":"ok"},{"type":"text","text":"continue"}]' \
  200 "tool_result + a normal instruction"
# The error text must be the upstream's own sentence (later chapters rely on it to recognize the shape); lock it verbatim.
CODE=$(curl -s -o "$SANDBOX/strict.out" -w "%{http_code}" -X POST \
  "http://127.0.0.1:$UP_PORT/v1/messages" -H 'Content-Type: application/json' \
  -d '{"model":"deepseek-chat","messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"t","content":"ok"}]}]}')
command grep -q "must be passed back" "$SANDBOX/strict.out" \
  && ok "the 400 text is the upstream's own sentence (reasoning field) -- text and real cause disagree, do not judge by text" \
  || bad "wrong 400 text: $(head -c 160 "$SANDBOX/strict.out")"

echo; echo "== 10. count_tokens: if the upstream understands it, forward and get the real value =="
# Claude Code calls count_tokens periodically to compute context usage (OpenAI-dialect upstreams have no such
# endpoint). The fake upstream implements it (native anthropic shape) -> the proxy must forward: fill model from
# the mid tier's chain head (count_tokens requests carry no model), and the client gets the upstream's real value 42,
# not a local bytes/4 estimate. The local estimate fallback (upstream 404 -> learn -> stop wasting requests)
# is covered by unit tests (count_tokens_test.go).
curl -sf "http://127.0.0.1:$UP_PORT/__mock/reset" -X POST >/dev/null
OUT="$(E2E_SCENARIO=count_tokens "$BIN" claude --profile=ds 2>"$SANDBOX/ct.err")"
echo "$OUT" | sed 's/^/    /'
check "count_tokens 200" "$(echo "$OUT" | grep '^HTTP=' | cut -d= -f2)" "200"
check "got the upstream's real value 42 (not a local estimate)" "$(echo "$OUT" | grep '^INPUT_TOKENS=' | cut -d= -f2)" "42"
GOT=$(curl -s "http://127.0.0.1:$UP_PORT/__mock/requests" | python3 -c '
import json,sys
r=json.load(sys.stdin)
ct=[x for x in r if x["path"].endswith("/count_tokens")]
if not ct:
    print("NOT_FORWARDED")
else:
    print(str(ct[0]["body"].get("model","NONE"))+"@"+ct[0]["path"])')
check "forwarded to the upstream, with model filled from the mid chain head" "$GOT" "deepseek-chat@/v1/messages/count_tokens"

echo; echo "== 11. Control endpoint: cross-user shutdown (/__newgate/stop + token) =="
# Multi-user deployment: the claude user can read the shared config but has no
# kill() permission on a daemon started as root -- so stopping can only go through this endpoint. A wrong token must be 403; the right token 200 and
# the daemon exits cleanly itself (both pid and lock cleaned up).
PIDFILE="$NEWGATE_HOME/.newgate.pid"
if [ -f "$PIDFILE" ]; then
  PORT=$(python3 -c 'import json;print(json.load(open("'"$NEWGATE_HOME"'/state.json"))["port"])')
  TOK=$(python3 -c 'import json;print(json.load(open("'"$NEWGATE_HOME"'/state.json")).get("control_token",""))')
  if [ -n "$TOK" ]; then
    ok "a control token was generated in state.json"
  else
    bad "no control token was generated (cross-user shutdown would degrade to asking root to stop it)"
  fi
  CODE=$(curl -s -o /dev/null -w '%{http_code}' -X POST \
    "http://127.0.0.1:$PORT/__newgate/stop" -H 'Authorization: Bearer wrong-token')
  check "wrong token -> 403" "$CODE" "403"
  CODE=$(curl -s -o /dev/null -w '%{http_code}' -X POST \
    "http://127.0.0.1:$PORT/__newgate/stop" -H "Authorization: Bearer $TOK")
  check "right token -> 200" "$CODE" "200"
  # What we wait for is "both pid and lock are gone" -- those are the two things asserted, so the wait condition must match.
  # Waiting only on the pidfile gets preempted between RemovePid() and RemoveLock(): pid gone, lock
  # still there for a few microseconds, and a poll landing exactly in that window falsely reports "did not exit cleanly" (intermittent, 2026-09-17).
  for _ in $(seq 40); do
    [ ! -f "$PIDFILE" ] && [ ! -f "$NEWGATE_HOME/.newgate.lock" ] && break
    sleep 0.1
  done
  if [ ! -f "$PIDFILE" ] && [ ! -f "$NEWGATE_HOME/.newgate.lock" ]; then
    ok "the daemon exited after the command, pid/lock are both gone"
  else
    bad "the daemon did not exit cleanly (pid or lock still there)"
  fi
  curl -sf "http://127.0.0.1:$PORT/__newgate/status" >/dev/null 2>&1 \
    && bad "the port is still listening?" || ok "the port is released"
else
  bad "the sandbox daemon's pid file is missing (did some earlier step fail to start the daemon?)"
fi

echo; echo "== 12. Graceful handover: restart does not cut in-flight streams (nginx-style zero-downtime upgrade) =="
# The previous chapter stopped the daemon; bring it back through the lazy-start path first.
OUT="$(E2E_SCENARIO=plain "$BIN" claude --profile=ds 2>/dev/null)"
check "lazy start brought the daemon back" "$(echo "$OUT" | grep '^HTTP=' | cut -d= -f2)" "200"
PIDFILE="$NEWGATE_HOME/.newgate.pid"
read_pid() { python3 -c 'import json,sys;print(json.load(open(sys.argv[1]))["pid"])' "$PIDFILE" 2>/dev/null; }
OLD_PID=$(read_pid)
if [ -z "$OLD_PID" ]; then
  bad "cannot read the pid file, so the handover cannot be verified"
else
  # A ~2.5s slow SSE stream, with a restart in the middle: the stream must finish completely
  curl -sN -o "$SANDBOX/sse.out" -w '%{http_code}' -X POST \
    "http://127.0.0.1:$PROXY_PORT/a/claude/p/ds/v1/messages" \
    -H 'Content-Type: application/json' -H 'x-api-key: newgate-local' \
    -H 'anthropic-version: 2023-06-01' \
    -d '{"model":"deepseek-chat","max_tokens":64,"stream":true,"mock_slow":true,"messages":[{"role":"user","content":"go"}]}' \
    > "$SANDBOX/sse.code" &
  CURL_PID=$!
  sleep 0.8  # the stream is already running, in flight
  OUT="$("$BIN" restart 2>&1)"
  echo "$OUT" | sed 's/^/    /'
  echo "$OUT" | command grep -q 'gracefully restarted' \
    && ok "restart did a graceful handover (socket handover)" \
    || bad "restart did not hand over: $(echo "$OUT" | head -2)"
  wait "$CURL_PID"
  check "SSE stream 200 (the restart happened mid-stream)" "$(cat "$SANDBOX/sse.code")" "200"
  command grep -q 'message_stop' "$SANDBOX/sse.out" \
    && ok "the stream finished completely (message_stop received)" || bad "the in-flight stream was cut off by the restart"
  RCVD=$(cat "$SANDBOX/sse.out" | command grep -c 'content_block_delta')
  [ "$RCVD" -ge 4 ] && ok "all delta chunks arrived ($RCVD)" || bad "delta chunks lost (only $RCVD)"
  NEW_PID=$(read_pid)
  if [ -n "$NEW_PID" ] && [ "$NEW_PID" != "$OLD_PID" ]; then
    ok "pid changed ($OLD_PID -> $NEW_PID)"
  else
    bad "pid did not change (old $OLD_PID, new '$NEW_PID')"
  fi
  # A new request after the handover must land on the new process (it may not, but it must succeed)
  OUT="$(E2E_SCENARIO=plain "$BIN" claude --profile=ds 2>/dev/null)"
  check "new request after the handover returns 200" "$(echo "$OUT" | grep '^HTTP=' | cut -d= -f2)" "200"
fi

echo; echo "== 13. Background requests: the classifier switches to the light tier, everything else only gets thinking disabled =="
# Captured (2026-09, glm-5.3): the Bash classifier = non-streaming, model=mid, the system starts with
# "You are a security monitor...". The proxy must switch the classifier (bg_plain/bg_adaptive)
# to light; bg_plain, which wrote no thinking, gets disabled injected, and an explicit adaptive must be
# preserved. Other background calls (bg_other, compact summaries) keep mid + disabled.
for SC in bg_plain bg_adaptive bg_other bg_realname; do
  curl -sf "http://127.0.0.1:$UP_PORT/__mock/reset" -X POST >/dev/null
  OUT="$(E2E_SCENARIO=$SC "$BIN" claude --profile=glm 2>"$SANDBOX/$SC.err")"
  echo "$OUT" | sed 's/^/    /'
  check "$SC request returns 200" "$(echo "$OUT" | grep '^HTTP=' | cut -d= -f2)" "200"
  WANT="glm-4-plus|disabled"          # bg_other: no marker -> stays on mid
  [ "$SC" != "bg_other" ] && WANT="glm-4.5-air|disabled"  # classifier -> light
  [ "$SC" = "bg_adaptive" ] && WANT="glm-4.5-air|adaptive" # an explicit thinking is not overridden
  GOT=$(curl -s "http://127.0.0.1:$UP_PORT/__mock/requests" | python3 -c '
import json,sys
r=json.load(sys.stdin)
b=r[0]["body"] if r else {}
th=(b.get("thinking") or {}).get("type","MISSING")
print(str(b.get("model","NONE"))+"|"+th)')
  check "$SC: the upstream received $WANT" "$GOT" "$WANT"
done

echo; echo "== 13b. Naming a profile uses only that one: the chain tail is cut off (no more switching) =="
# Naming a profile means "use this profile for this call": launch only writes /p/<name> into the base URL when the user names one,
# and the proxy truncates the chain **to its head** based on that. So if glm is down, it reports glm's error truthfully,
# and never quietly switches to someone else -- this assertion was originally written the other way round (expecting a switch along the chain to ds for a 200),
# because that was the behavior then: the config said glm, the UI said glm, and what actually ran was ds.
#
# Rerouting is still a routing decision (before the chain is built): this request hits the classifier (bg_plain -> light tier),
# so the whole chain is light, and the tail that got cut is **the next profile's light**
# (ds:ds/deepseek-chat), not mid's glm-4-plus.
LOG="$NEWGATE_HOME/newgate.log"
curl -sf "http://127.0.0.1:$UP_PORT/__mock/reset" -X POST >/dev/null
curl -sf "http://127.0.0.1:$UP_PORT/__mock/fail?code=500" >/dev/null
OUT="$(E2E_SCENARIO=bg_plain "$BIN" claude --profile=glm 2>"$SANDBOX/bgfo.err")"
echo "$OUT" | sed 's/^/    /'
check "a named profile that is down is reported truthfully as 500 (no switching)" "$(echo "$OUT" | grep '^HTTP=' | cut -d= -f2)" "500"
GOT=$(curl -s "http://127.0.0.1:$UP_PORT/__mock/requests" | python3 -c '
import json,sys
r=json.load(sys.stdin)
models=[x["body"].get("model") for x in r if x["path"].endswith("/messages")]
print("|".join(dict.fromkeys(models)))')
# The criterion is the **de-duplicated** model sequence: the breaker, near its threshold, first fires a "pre-gate diagnosis"
# ([probe] pre-gate diagnosis, counter breaker.spared), and that request also goes through /messages
# and is also recorded by the fake upstream. So hitting the same stop twice is normal, and "did it switch" is about **which stops
# appeared** -- only after de-duplication is it not polluted by duplicates from the diagnosis (measured 2026-09-28:
# a raw join gave deepseek-chat|deepseek-chat on 13c, which looked like a switch).
check "only glm's chain head was hit: glm-4.5-air" "$GOT" "glm-4.5-air"
# Who got cut must be said out loud (not silently). How the prefix is rendered is mechanism, not a promise (measured, that line is
# `[proxy] #13 profile glm is pinned, ...`, with reqID expanded), so anchor only that tail,
# do not match the prefix.
for _ in $(seq 20); do
  command grep -aq 'profile glm is pinned, dropping 1 fallback step: ds:ds/deepseek-chat' "$LOG" 2>/dev/null && break
  sleep 0.1
done
command grep -aq 'profile glm is pinned, dropping 1 fallback step: ds:ds/deepseek-chat' "$LOG" \
  && ok "the log says which step was cut (profile ... is pinned)" \
  || bad "the log does not record who got cut ($LOG)"
# The 4xx finalization branch does not write X-Newgate-Failover, so **cannot** use it here as a criterion (its absence is certain,
# a no-op); the two criteria above are the ones that matter: the status code comes from the chain head, and the upstream was hit only once.

echo; echo "== 13c. newgate fallback off: globally turn off switching (all gateway paths) =="
# 13b is the truncation when a profile is **named**; this section is the global switch. The criterion is on
# gateway.fallback in state.json and covers every request that names no profile (the web UI, curl, other agents
# all go through here). Both share the same truncation point and the same result; the only difference is "why it was cut" -- the log
# wording differs, so the two paths can be verified separately.
#
# The shape is "first prove the chain is alive, then turn it off, then turn it back on", not just "no switching": a chain that was never connected
# can also make "the second stop was not hit" true, which is exactly how the version before 13b passed while doing nothing.
#
# `newgate fallback` has no "report the current state when given no arguments" usage (no args prints usage and exits with 64,
# the same house rule as schema-repair); see the current state with `newgate status`.
LOG="$NEWGATE_HOME/newgate.log"
RESETUP() { curl -sf "http://127.0.0.1:$UP_PORT/__mock/reset" -X POST >/dev/null; }
MODELS() { curl -s "http://127.0.0.1:$UP_PORT/__mock/requests" | python3 -c '
import json,sys
r=json.load(sys.stdin)
print("|".join(dict.fromkeys(x["body"].get("model") for x in r if x["path"].endswith("/messages"))))'; }

# (1) Control: on by default, the same request must switch along the chain. Without this one, "only one hit after turning it off"
#     could also just mean the chain was not connected; it also records chain.failover (chapter 15 reads it).
#     MODELS is de-duplicated (see the note in 13b): what is wanted here is "which stops were visited".
RESETUP; curl -sf "http://127.0.0.1:$UP_PORT/__mock/fail?code=500" >/dev/null
OUT="$(E2E_SCENARIO=bg_plain "$BIN" claude 2>"$SANDBOX/fbon1.err")"
check "control: fallback on => still 200 when the chain head is down (switch along the light chain)" \
  "$(echo "$OUT" | grep '^HTTP=' | cut -d= -f2)" "200"
check "control: two stops hit (ds's light -> glm's light)" "$(MODELS)" "deepseek-chat|glm-4.5-air"

# (2) Turned off: the same request must hit the chain head and report the upstream's own text as is.
"$BIN" fallback off >/dev/null 2>&1
RESETUP; curl -sf "http://127.0.0.1:$UP_PORT/__mock/fail?code=500" >/dev/null
OUT="$(E2E_SCENARIO=bg_plain "$BIN" claude 2>"$SANDBOX/fboff.err")"
check "fallback off => the chain head's 500 is reported as is" \
  "$(echo "$OUT" | grep '^HTTP=' | cut -d= -f2)" "500"
check "fallback off => only the chain head was hit (no switching)" "$(MODELS)" "deepseek-chat"
for _ in $(seq 20); do
  command grep -aq 'fallback is off, dropping 1 chain step: glm:glm/glm-4.5-air' "$LOG" 2>/dev/null && break
  sleep 0.1
done
command grep -aq 'fallback is off, dropping 1 chain step: glm:glm/glm-4.5-air' "$LOG" \
  && ok "the log says which step was cut (fallback is off)" \
  || bad "the log does not record who got cut ($LOG)"
# This line is the user's only clue: normally requests return 200 as usual, and it only shows when some upstream really is down;
# having forgotten you flipped this switch, you would misread "no fallback" as "nobody on the chain to switch to".
check "status reports fallback=off" \
  "$("$BIN" status 2>&1 | command grep -c 'fallback=off')" "1"

# (3) Flip it back: the switch is symmetric, and the status line follows cleanly.
"$BIN" fallback on >/dev/null 2>&1
RESETUP; curl -sf "http://127.0.0.1:$UP_PORT/__mock/fail?code=500" >/dev/null
OUT="$(E2E_SCENARIO=bg_plain "$BIN" claude 2>"$SANDBOX/fbon2.err")"
check "fallback back on => switching along the chain again" \
  "$(echo "$OUT" | grep '^HTTP=' | cut -d= -f2)" "200"
check "fallback back on => status no longer reports fallback=off" \
  "$("$BIN" status 2>&1 | command grep -c 'fallback=off')" "0"

echo; echo "== 15. newgate metrics: everything on the path is accounted =="
# The counters live in daemon memory (/__newgate/metrics), and the CLI reads them over HTTP; the restart in section 12
# already cleared them once, so manufacture a few here first and then check. count_tokens
# goes straight to the proxy with curl (the fake upstream has that endpoint -> forward and get the real value).
CT_CODE=$(curl -s -o "$SANDBOX/ct2.out" -w '%{http_code}' -X POST \
  "http://127.0.0.1:$PROXY_PORT/a/claude/p/glm/v1/messages/count_tokens" \
  -H 'Content-Type: application/json' -H 'x-api-key: newgate-local' \
  -d '{"messages":[{"role":"user","content":"count me"}]}')
check "metrics precondition: count_tokens 200" "$CT_CODE" "200"
MOUT="$("$BIN" metrics 2>"$SANDBOX/metrics.err")"
echo "$MOUT" | sed 's/^/    /'
for KEY in "special.claude-bg.route_light" "count_tokens.forwarded" "count_tokens.total" "chain.step_failed" "chain.failover"; do
  echo "$MOUT" | command grep -q "$KEY" \
    && ok "metrics has $KEY" \
    || bad "metrics is missing $KEY (output: $(echo "$MOUT" | head -3))"
done

echo; echo "== 14. Window declarations: follow the profile, but capped at 512K/400K =="
# Claude Code does not recognize the model names we inject and assumes a window for an "unknown model". glm declares
# 1M/500k -> the client's caps squeeze it to 512K/400K (2026-11-12 on site: 1M hit the last cell,
# and after 400 even /compact could not be sent); ds declares none -> the window uses the cap 512K, and compact
# is left to the client to compute from the window.
OUT="$(E2E_SCENARIO=plain "$BIN" claude --profile=glm 2>/dev/null)"
check "glm: MAX_CONTEXT_TOKENS capped at 512000" \
  "$(echo "$OUT" | grep '^WIN_MAX=' | cut -d= -f2-)" "512000"
check "glm: AUTO_COMPACT_WINDOW capped at 400000" \
  "$(echo "$OUT" | grep '^WIN_COMPACT=' | cut -d= -f2-)" "400000"
OUT="$(E2E_SCENARIO=plain "$BIN" claude --profile=ds 2>/dev/null)"
check "ds (nothing declared): MAX_CONTEXT_TOKENS=512000 (uses the cap)" \
  "$(echo "$OUT" | grep '^WIN_MAX=' | cut -d= -f2-)" "512000"
check "ds (nothing declared): AUTO_COMPACT_WINDOW empty (the client computes it)" \
  "$(echo "$OUT" | grep '^WIN_COMPACT=' | cut -d= -f2-)" ""

echo; echo "== 16. naked: the Bash classifier is short-circuited, the upstream receives not one request =="
# Going naked is "our own security gate": while on, Bash classifier requests are approved directly and never reach the upstream. The criterion
# is the same as the previous chapter (bg_plain = the small background call with "You are a security monitor...").
#   off: the request really reaches the upstream (glm's light tier);
#   on: the request is short-circuited, the upstream's /__mock/requests count is 0, and the short-circuit counter goes up.
UPCOUNT() { curl -s "http://127.0.0.1:$UP_PORT/__mock/requests" | python3 -c 'import json,sys;print(len(json.load(sys.stdin)))'; }
RESETUP() { curl -sf "http://127.0.0.1:$UP_PORT/__mock/reset" -X POST >/dev/null; }

RESETUP
OUT="$(E2E_SCENARIO=bg_plain "$BIN" claude --profile=glm 2>"$SANDBOX/nk_off.err")"
echo "$OUT" | sed 's/^/    /'
check "naked off: bg_plain 200" "$(echo "$OUT" | grep '^HTTP=' | cut -d= -f2)" "200"
check "naked off: the upstream received 1 classifier request" "$(UPCOUNT)" "1"
GOT=$(curl -s "http://127.0.0.1:$UP_PORT/__mock/requests" | python3 -c '
import json,sys
r=json.load(sys.stdin)
print(r[0]["body"].get("model","NONE") if r else "NONE")')
check "naked off: the classifier still switches to light (glm-4.5-air)" "$GOT" "glm-4.5-air"

"$BIN" naked on >/dev/null 2>&1
RESETUP
OUT="$(E2E_SCENARIO=bg_plain "$BIN" claude --profile=glm 2>"$SANDBOX/nk_on.err")"
echo "$OUT" | sed 's/^/    /'
check "naked on: bg_plain still 200 (approved via the short circuit)" "$(echo "$OUT" | grep '^HTTP=' | cut -d= -f2)" "200"
check "naked on: the upstream received 0 requests (really short-circuited)" "$(UPCOUNT)" "0"
MOUT="$("$BIN" metrics 2>/dev/null)"
# Assert **machine facts**: the counter name (an identifier; the table guarantees it is never wrapped -- see the column-width
# floor in the kernel's lib/style) and the first few words of the description.
#
# **Do not match a whole sentence of prose verbatim**: the description is prose, wrapped to the column width, and broken **per character**
# (the first line hard-breaks at `naked: the classi`). Matching a whole sentence verbatim turns the assertion into
# "how wide is the terminal". Flattening the newlines does not save it either -- continuation lines carry the column indent, and words are separated by multiple spaces.
# Before 2026-09-20 this was a line-by-line grep: the Chinese description was short and happened not to wrap, and a long English one wraps,
# and the failure looked like "the counter is gone".
FLAT="$(echo "$MOUT" | tr '\n' ' ' | tr -s ' ')"
case "$FLAT" in
  *"classifier-naked.shortcircuit"*"naked:"*)
    ok "metrics has special.classifier-naked.shortcircuit (short circuit counted)" ;;
  *) bad "metrics is missing the naked short-circuit counter (output: $(echo "$MOUT" | command grep -a classifier-naked))" ;;
esac
"$BIN" naked off >/dev/null 2>&1
check "naked finally off (leave no dirty sandbox state)" "$("$BIN" naked 2>&1 | command grep -c 'naked is off')" "1"

echo; echo "== 19. Runtime switches: modules report themselves, commands register themselves =="
# This layer is the user interface of "everything is a module": modules report their switch points
# to plugin-manager in Start (RegisterSelf), and the commands are registered into cli by **each module itself**
# (gateway's st/schema-repair, claudecode's naked, plugin-manager's plugin).
#
# What is asserted here is the foundation of the list: **the enumeration source must be the component graph**, not "who reported" -- otherwise
# "this module has no switches" and "this module forgot to register" look exactly the same, and the user sees the same
# empty list. The "turning a point off really changes the hot path" half is locked by schema-repair in chapter 20.
#
# Since 2026-09-20 this chapter only looks at **the kernel's own** modules: the upstream quirk modules (the deepseek switch set
# used to be the best example of this layer) moved with the distribution, and their switch points are locked by the distribution's own e2e.
PLUGIN_OUT="$("$BIN" plugin 2>&1)"
case "$PLUGIN_OUT" in
  *"breaker"*"plugin-manager"*"gateway"*)
    ok "plugin: lists every module (including ones outside the switch system)" ;;
  *) bad "plugin did not list every module: $(echo "$PLUGIN_OUT" | head -6 | tr '\n' ' ')" ;;
esac
case "$PLUGIN_OUT" in
  *"infra"*"gateway"*"model"*) ok "plugin: grouped by category" ;;
  *) bad "plugin is not grouped by category" ;;
esac
case "$PLUGIN_OUT" in
  *"cannot be toggled at runtime (v1)"*)
    ok "plugin: modules that report no switch point are labeled explicitly (not silently omitted)" ;;
  *) bad "plugin does not label modules that cannot be toggled at runtime" ;;
esac

# When a switch point cannot be recognized it must be **an error**, not silently treated as on/off.
"$BIN" plugin no-such-module off >/dev/null 2>&1
check "plugin: a nonexistent target exits non-zero" "$([ $? -ne 0 ] && echo yes || echo no)" "yes"

echo; echo "== 20. special_treatment (whole layer / single plugin) and schema-repair =="
# These switches do not live in plugin-manager's ledger but are typed fields on domain.State
# (special_treatment / special_treatment_off / schema_repair), hence their own chapter.
# The assertion is the same thing: **once off, the hot path really stops doing it** -- written into state.json but read by nobody
# is just a pretty switch.
#
# The probe uses schema-repair (a special plugin of the kernel's gateway itself): it adds
# "required": [], which is a no-op semantically, but **whether that key is in the bytes the upstream receives is visible**,
# which makes it a perfect probe for "is this layer still running". Before 2026-09-20 the probe here was the distribution's
# tail-shape patch (deepseek's 4th move) -- easier to use, but it moved to the distribution with its owner.
SCHEMA_BODY='{"model":"normal","max_tokens":16,"messages":[{"role":"user","content":"hi"}],'\
'"tools":[{"type":"function","function":{"name":"t","parameters":{"type":"object","properties":{}}}}]}'
has_required() {
  curl -s "http://127.0.0.1:$UP_PORT/__mock/requests" | python3 -c '
import json,sys
try:
    r=json.load(sys.stdin)
    prm=r[0]["body"]["tools"][0]["function"]["parameters"]
    print("yes" if "required" in prm else "no")
except Exception:
    print("NOUP")' 2>/dev/null
}
post_schema() {
  RESETUP
  curl -s -o /dev/null -X POST "http://127.0.0.1:$PROXY_PORT/p/ds/v1/chat/completions" \
    -H 'Content-Type: application/json' -d "$SCHEMA_BODY"
}

# (1) Single-plugin switch: this cell is the opposite of "written into state.json but read by nobody".
"$BIN" schema-repair on >/dev/null 2>&1
post_schema
check "schema-repair on => the upstream receives the repaired required" "$(has_required)" "yes"

"$BIN" schema-repair off >/dev/null 2>&1
post_schema
check "schema-repair off => bytes as is, required not added" "$(has_required)" "no"

# (2) Whole layer off: special_treatment off => every **plugin** in this layer stops running.
#
# The probe must pick a plugin that really lives in this layer: the kernel's own claude-bg (the claudecode module) adds
# thinking:disabled to non-streaming background calls, and the upstream receiving no such field means "it did not run".
# Note that schema-repair is **not in this layer** (it is a separate field on gatewaystate), so it
# cannot probe the whole-layer switch -- measured 2026-09-20: after st off, required was still added.
bg_thinking() {
  RESETUP
  E2E_SCENARIO=bg_other "$BIN" claude --profile=glm >/dev/null 2>&1
  curl -s "http://127.0.0.1:$UP_PORT/__mock/requests" | python3 -c '
import json,sys
r=json.load(sys.stdin)
b=r[0]["body"] if r else {}
print((b.get("thinking") or {}).get("type","MISSING"))' 2>/dev/null
}
"$BIN" st on >/dev/null 2>&1
GOT_BG="$(bg_thinking)"
check "st on (whole layer) => the special plugins run (background calls get thinking:disabled)" "$GOT_BG" "disabled"

"$BIN" st off >/dev/null 2>&1
GOT_BG="$(bg_thinking)"
check "st off (whole layer) => the layer does not run (background calls no longer get thinking)" "$GOT_BG" "MISSING"

# (3) Whole layer back on: the same request must get it added again (the switch is symmetric).
"$BIN" st on >/dev/null 2>&1
GOT_BG="$(bg_thinking)"
check "st on (whole layer) => the plugins run again (switch is symmetric)" "$GOT_BG" "disabled"

# Wrap-up: leave no non-factory state (the sandbox is deleted anyway, but dirty state makes the status shown while debugging lie).
# fallback counts too: 13c flipped it back off, and missing this cell would leave a sandbox that "forgot to flip it back"
# looking exactly like a clean one in status -- and its symptom only shows when some upstream really goes down.
"$BIN" schema-repair on >/dev/null 2>&1
"$BIN" st on >/dev/null 2>&1
"$BIN" fallback on >/dev/null 2>&1
check "wrap-up: all switches are back to factory state" \
  "$("$BIN" status 2>&1 | command grep -c 'special off:\|schema-repair=off\|fallback=off')" "0"


echo
echo "Result: $PASS passed, $FAIL failed"
[ "$FAIL" -eq 0 ]
