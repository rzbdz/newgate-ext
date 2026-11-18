#!/usr/bin/env bash
# Zero-token end to end: the behavior of **this distribution's modules** - DeepSeek's reasoning-original-text backfill,
# tail shape patching, and the patch contract for cross-upstream migration.
#
# These sections used to live in the core repo's mock/e2e_claude.sh; they moved here with the modules on 2026-09-20.
# The reason is **tests follow their owner**: they assert the policy of a distribution module, and living in the core repo would make
# the core's tests depend on a distribution module - remove that module and the core goes red, while the core should be able to test itself cleanly.
# The core's own e2e stays in core/mock/e2e_claude.sh, covering takeover, forwarding, control endpoints,
# and graceful handover - those **mechanisms**.
#
# Verification (section numbers follow the core version, to make reconciling the two sides easy):
#   7/8/9  thinking mode: Claude Code strips the thinking blocks, and the proxy backfills the real original text of that round from the thinkcache
#          (tool id lookup); **if it cannot find it, it adds not one byte** (since 2026-09-18:
#          the upstream itself never gave any reasoning, so what right do we have to make some up for it).
#   17     shape 400: upstream 400 passed through verbatim + breaker only counts, never trips.
#   18     the three contracts on the patch side: never fabricate, minimal wording, cause classification.
#   25     global no-think switch: when on, the thinking the client wrote (the adaptive one in the main loop)
#          is overwritten to disabled, OpenAI-dialect requests are not touched by a single byte, and turning it off restores them.
#
# The fake upstream is **reused by path** from the core's core/mock/fake_upstream.py rather than copied: it is a byte-for-byte
# replica of real upstream behavior; a copy inevitably drifts, and what drifts out is a "green fake test". So before running,
# the core/ submodule must be present (`git submodule update --init --recursive`).
#
# Everything runs in a temporary sandbox, never touching the real ~/.config / ~/.claude / shell rc.
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CORE="${NEWGATE_E2E_CORE:-$ROOT/core}"
# The binary comes from this repo's build entry (build/build.sh), and its filename carries the distribution name and platform.
DIST_NAME="${NEWGATE_E2E_DIST:-default}"
# The architecture in the artifact name uses **Go's naming** (amd64), not uname's (x86_64) - build.sh
# names it the Go way, so the two sides must agree, otherwise this will not find the binary. The mapping is the same rule as
# that section in build.sh (the comment there explains why it is needed).
case "$(uname -m)" in
  x86_64)        host_arch=amd64 ;;
  aarch64|arm64) host_arch=arm64 ;;
  i386|i686)     host_arch=386 ;;
  *)             host_arch=$(uname -m) ;;
esac
BIN_SRC="${NEWGATE_E2E_BIN:-$ROOT/dist/newgate-$DIST_NAME-$(uname -s | tr 'A-Z' 'a-z')-$host_arch}"
# Port defaults are spaced away from the core version: both can run at once (handy when debugging side by side).
SANDBOX="${NEWGATE_E2E_SANDBOX:-$(mktemp -d /tmp/newgate-dist-e2e.XXXXXX)}"
UP_PORT="${NEWGATE_E2E_UP_PORT:-18091}"
PROXY_PORT="${NEWGATE_E2E_PROXY_PORT:-18899}"
# Upstream that is "temporarily unreachable, then recovers": section 26 uses it to build a real circuit break, then proves that after a successful probe
# requests can go **immediately** (the 2026-11-04 incident). It is not started yet; bring it up after the break is established.
REVIVE_PORT="${NEWGATE_E2E_REVIVE_PORT:-18093}"
FAKEBIN="$SANDBOX/fakebin"
# The binary is **multi-call**: argv0 decides who the invocation belongs to (newgate / claude / opencode ...).
# The release artifact is called newgate-<distribution>-<platform>-<arch>, and using it directly as newgate makes it treat that as a
# "profile name" - the error measured on 2026-09-20 was
# `both profile "newgate-default-linux-x86_64" and "ds" given, inconsistent`.
# So first drop a copy under the right name into the sandbox, then run that.
BIN="$SANDBOX/bin/newgate"

PASS=0; FAIL=0
ok()   { echo "  ✓ $1"; PASS=$((PASS+1)); }
bad()  { echo "  ✗ $1"; FAIL=$((FAIL+1)); }
check(){ if [ "$2" = "$3" ]; then ok "$1"; else bad "$1 (expected '$3', actual '$2')"; fi; }

export NEWGATE_HOME="$SANDBOX/ng"
mkdir -p "$NEWGATE_HOME/mappings" "$FAKEBIN"
# The sandbox must be sealed: the session running e2e may itself carry newgate-injected window declarations (nested
# startup leaks the parent's env to the child), and not unsetting them makes the "undeclared profile"
# case read the parent session's values and fail falsely.
unset CLAUDE_CODE_MAX_CONTEXT_TOKENS CLAUDE_CODE_AUTO_COMPACT_WINDOW
# The language must be sealed too: the script asserts on **source-language text** (English), while the machine running it may be zh-Hans
# (our own machines are). Without pinning it, assertions like "the UI is English" go falsely red on Chinese machines.
export NEWGATE_LANG=en
unset LC_ALL LC_MESSAGES LANG LANGUAGE 2>/dev/null || true

cleanup() {
  "$BIN" stop >/dev/null 2>&1 || true
  [ -n "${UP_PID:-}" ] && kill "$UP_PID" 2>/dev/null
  [ -n "${REVIVE_PID:-}" ] && kill "$REVIVE_PID" 2>/dev/null
  # For debugging: NEWGATE_E2E_KEEP=1 keeps the sandbox (logs, the bodies the fake upstream received are all in there),
  # so when something goes red you are not guessing at a directory that has been deleted.
  if [ -n "${NEWGATE_E2E_KEEP:-}" ]; then
    echo "sandbox kept (NEWGATE_E2E_KEEP): $SANDBOX"
  else
    rm -rf "$SANDBOX"
  fi
}
trap cleanup EXIT

echo "sandbox: $SANDBOX"
[ -x "$BIN_SRC" ] || { echo "binary not found $BIN_SRC - run build/build.sh first"; exit 1; }
mkdir -p "$(dirname "$BIN")"
cp "$BIN_SRC" "$BIN"
[ -f "$CORE/mock/fake_upstream.py" ] || {
  echo "cannot find the core's fake upstream $CORE/mock/fake_upstream.py"
  echo "core/ is a submodule, first: git submodule update --init --recursive"
  exit 1
}

# ---- fake claude: sends requests the way Claude Code does. E2E_SCENARIO picks the shape ----
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
    # Claude Code main-loop shape: **streaming** + thinking explicitly on + carries tools,
    # the assistant messages in the history have had their thinking blocks stripped by the client - only tool_use is left.
    # (streaming is key: claude-bg only rewrites non-streaming background calls, the main loop is untouched.)
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
    # Claude Code's count_tokens: the shape without a model field must survive too
    code, body = post(base.rstrip("/") + "/v1/messages/count_tokens",
                      {"messages": [{"role": "user", "content": "count the tokens"}],
                       "tools": TOOLS})
    print(f"HTTP={code}")
    try:
        print(f"INPUT_TOKENS={json.loads(body).get('input_tokens')}")
    except Exception:
        print("INPUT_TOKENS=PARSE_FAIL")
elif scenario in ("bg_plain", "bg_adaptive", "bg_other", "bg_realname"):
    # Claude Code's background small-call shape (captured live 2026-09, cc 2.1.263): non-streaming,
    # model is just the tier name mid, no tools, max_tokens 2112.
    #   bg_plain / bg_adaptive / bg_realname = the Bash classifier proper: system ~126KB
    #     starts with "You are a security monitor..." (bg_plain does not even write thinking,
    #     bg_adaptive explicitly asks for adaptive) -> all switch to light; only the one with no thinking written,
    #     bg_plain, gets disabled injected, explicit intent is not overridden.
    #   bg_other = other background calls (compact summarization and the like): the system does not have that self-identifying
    #     line -> keep mid, only disable thinking.
    #   bg_realname = the regression scene from the real-model-name era (captured live 2026-09-09): the classifier
    #     names it with the real name of the main-loop slot (glm-4-plus), which is bound to both heavy+mid and, resolved in
    #     Roles order, comes out as heavy - the tier-gate version would skip it entirely. It must still switch to light.
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

# ---- config: three providers ds + glm + rev, pointing at the fake upstream ----
# rev is the "temporarily unreachable" upstream from section 26: nothing is listening on its port right now, so the probe fails and
# builds a real circuit break; the section starts a fake upstream on that port later.
cat > "$NEWGATE_HOME/providers.json" <<EOF
{
  "providers": {
    "ds":  { "base_url": "http://127.0.0.1:$UP_PORT/v1", "api_key": "sk-ds",  "protocol": "anthropic" },
    "glm": { "base_url": "http://127.0.0.1:$UP_PORT/v1", "api_key": "sk-glm", "protocol": "anthropic" },
    "rev": { "base_url": "http://127.0.0.1:$REVIVE_PORT/v1", "api_key": "sk-rev", "protocol": "anthropic" }
  }
}
EOF
cat > "$NEWGATE_HOME/mappings/ds.json" <<'EOF'
{ "name": "ds", "priority": 10, "roles": {
    "heavy": "ds/deepseek-flash", "mid": "ds/deepseek-flash",
    "light": "ds/deepseek-flash", "vision": "ds/deepseek-flash" } }
EOF
cat > "$NEWGATE_HOME/mappings/glm.json" <<'EOF'
{ "name": "glm", "priority": 20,
  "context_window": 1000000, "auto_compact_window": 500000,
  "roles": {
    "heavy": "glm/glm-4-plus", "mid": "glm/glm-4-plus",
    "light": "glm/glm-4.5-air", "vision": "glm/glm-4.5-air" } }
EOF
# rev-model is **bound only in this one profile**: that is exactly the shape of the 2026-11-04 incident (deepseek-flash
# bound only in ds). When it is unavailable, if the reverse lookup follows the admission filter, it gets misreported as "unknown".
cat > "$NEWGATE_HOME/mappings/rev.json" <<'EOF'
{ "name": "rev", "priority": 30, "roles": { "mid": "rev/rev-model" } }
EOF
cat > "$NEWGATE_HOME/state.json" <<EOF
{ "default_profile": "ds", "port": $PROXY_PORT }
EOF


echo; echo "== 0. start the fake upstream (strict DeepSeek rules) =="
# The fake upstream comes from the **core** repo (the core/ submodule): it is a byte-for-byte replica of real upstream
# behavior - the rule is the tail shape, not the reasoning field, and this distribution's patch is written exactly for those rules.
# So here it is **reused by path**, not copied: a copy inevitably drifts, and what drifts out is a "green fake test".
python3 "$CORE/mock/fake_upstream.py" --port "$UP_PORT" >"$SANDBOX/upstream.log" 2>&1 &
UP_PID=$!
for _ in $(seq 30); do
  curl -sf "http://127.0.0.1:$UP_PORT/__mock/requests" >/dev/null && break; sleep 0.1
done
curl -sf "http://127.0.0.1:$UP_PORT/__mock/requests" >/dev/null \
  && ok "fake upstream at 127.0.0.1:$UP_PORT" || { bad "fake upstream did not start"; exit 1; }

# fakebin is first in PATH, so newgate claude will exec our fake claude (generated at the top of the file,
# which mimics Claude Code's request shape).
export PATH="$FAKEBIN:$PATH"

# Clears the requests recorded by the fake upstream. The core version defines it in the middle sections, while this script only carried over the last few,
# so the definition came along - it is **infrastructure for this layer** (every case starts from "the upstream has received nothing"),
# not the private property of any one section.
RESETUP() { curl -sf "http://127.0.0.1:$UP_PORT/__mock/reset" -X POST >/dev/null; }
echo; echo "== 7. thinking mode round one (the starting point of the client block-stripping scenario) =="
curl -sf "http://127.0.0.1:$UP_PORT/__mock/reset" -X POST >/dev/null
OUT="$(E2E_SCENARIO=think1 "$BIN" claude --profile=ds 2>"$SANDBOX/t1.err")"
echo "$OUT" | sed 's/^/    /'
check "think1 first round 200" "$(echo "$OUT" | grep '^HTTP=' | cut -d= -f2)" "200"

echo; echo "== 8. round two: thinkcache hit, backfill that round's real reasoning original text =="
curl -sf "http://127.0.0.1:$UP_PORT/__mock/reset" -X POST >/dev/null
OUT="$(E2E_SCENARIO=think2 "$BIN" claude --profile=ds 2>"$SANDBOX/t2.err")"
echo "$OUT" | sed 's/^/    /'
check "think2 strict upstream lets it through (backfill took effect)" "$(echo "$OUT" | grep '^HTTP=' | cut -d= -f2)" "200"
GOT=$(curl -s "http://127.0.0.1:$UP_PORT/__mock/requests" | python3 -c '
import json,sys
r=json.load(sys.stdin)
m=r[0]["body"]["messages"][1] if r else {}
print(m.get("reasoning_content","MISSING"))')
check "reasoning_content is the original text from the cache" "$GOT" "MOCK-THINKING-ORIGINAL"
GOT=$(curl -s "http://127.0.0.1:$UP_PORT/__mock/requests" | python3 -c '
import json,sys
r=json.load(sys.stdin)
m=r[0]["body"]["messages"][1] if r else {}
c=m.get("content")
print(c[0].get("thinking","") if isinstance(c,list) and c else "NO_BLOCK")')
check "thinking block is also the original text from the cache" "$GOT" "MOCK-THINKING-ORIGINAL"

echo; echo "== 9. cache miss (an old session after a restart): not one byte is added =="
# This section locks the **most basic** logic set on 2026-09-18: the upstream's own round never
# gave any reasoning, so the thing the "must be passed back" requirement asks to pass back does not exist at all, and what right do we
# have to make one up for it. What used to be added here was "No thinking in this round", which was wrong:
# fabricated words go into the upstream, into the conversation history, and burn tokens every round, with zero information content.
#
# The correct action is to **skip this message**, and classify "why there is no original text" into the log
# (notool / nocache / nokey) - skipping is the outcome, a bare count cannot be traced back.
#
# But the **tail shape** move (move 4) still has to act on this request: think3's history happens to be
# "ends with a bare tool_result", and that is exactly the shape the upstream reports this 400 for. The two things do not conflict,
# so here we assert both at once:
#   assistant message: rc absent, no thinking block (no fabrication)
#   tail user turn: one extra `continue` (shape fixed)
curl -sf "http://127.0.0.1:$UP_PORT/__mock/reset" -X POST >/dev/null
OUT="$(E2E_SCENARIO=think3 "$BIN" claude --profile=ds 2>"$SANDBOX/t3.err")"
echo "$OUT" | sed 's/^/    /'
check "think3 strict upstream lets it through (tail shape was fixed)" "$(echo "$OUT" | grep '^HTTP=' | cut -d= -f2)" "200"
GOT=$(curl -s "http://127.0.0.1:$UP_PORT/__mock/requests" | python3 -c '
import json,sys
r=json.load(sys.stdin)
msgs=r[0]["body"]["messages"] if r else []
a=[m for m in msgs if m.get("role")=="assistant"]
c=(a[0].get("content") if a else None) or []
blk=[b.get("type") for b in c if isinstance(b,dict)]
rc="PRESENT" if (a and "reasoning_content" in a[0]) else "ABSENT"
lastu=[m for m in msgs if m.get("role")=="user"][-1].get("content")
tailb=[b.get("type") for b in lastu] if isinstance(lastu,list) else [lastu]
print("rc="+rc+" blocks="+",".join(blk)+" tail="+",".join(tailb))')
check "no placeholder fabricated (rc absent, no thinking block), but the tail shape was fixed" \
  "$GOT" "rc=ABSENT blocks=tool_use tail=tool_result,text"
GOT=$(curl -s "http://127.0.0.1:$UP_PORT/__mock/requests" | python3 -c '
import json,sys
r=json.load(sys.stdin)
lastu=[m for m in r[0]["body"]["messages"] if m.get("role")=="user"][-1]["content"]
print([b.get("text") for b in lastu if isinstance(b,dict) and b.get("type")=="text"][0])')
check "what was appended is literally the word continue (the minimal one)" "$GOT" "continue"
# The reason for skipping must be reported by category (this tool_use id never entered thinkcache => nocache).
LOGTAIL=$(tail -40 "$NEWGATE_HOME/newgate.log")
case "$LOGTAIL" in
  *"left untouched"*"nocache"*) ok "the log classifies no-original-text-to-backfill as nocache and explains the skip" ;;
  *) bad "the log does not explain the skip reason (should have \"left untouched\" + \"nocache\"): $(echo "$LOGTAIL" | tail -2)" ;;
esac

echo; echo "== 17. shape 400: upstream 400 passed through verbatim + breaker only counts, never trips =="
# This body must satisfy two conditions; miss one and the case no longer tests what it is meant to test:
#
#   1. **tail shape violation, and the plugin cannot fix it**. The measured rule is "the last role:user's
#      content[] is all tool_result blocks" (see section 6 and mock/fake_upstream.py),
#      and the deepseek plugin, for the family it **can fix** (that user turn is the end of the array),
#      appends a `continue` to fix it and returns 200 - that is what section 9 locks down.
#      So here we use the family it **cannot fix**: right after the bare tool_result there is **still one more
#      assistant**. The plugin deliberately leaves it alone (stuffing a line whose effect the model cannot see into the user's conversation
#      is worse than a 400), and measured, this family is still 400 3/3 after appending `continue` verbatim.
#      So **every** candidate on the chain gets 400, the chain runs out, and only then does the client get the upstream's original text.
#   2. **Go through the glm profile**, so the glm request eats the 400 first: the deepseek plugin's MatchTarget
#      looks at model/provider/baseURL, and glm/glm-4-plus has no "deepseek" string in any of the three,
#      so the plugin leaves it alone. (This is a redundant safeguard: even if Match gets it wrong, condition 1 catches it.)
#
# The shape rule (modules/deepseek/shape.go) claims it -> classify yields BucketShape ->
# the ledger only bumps ShapeSkips and never enters Open. So "the client gets a 400" and "glm was not tripped"
# must hold **at the same time**: that is exactly the behavior this round of refactoring is after.
RESETUP
SHAPE_BODY='{"model":"glm-4-plus","max_tokens":16,"stream":true,'\
'"thinking":{"type":"enabled","budget_tokens":1024},'\
'"tools":[{"name":"Bash","description":"d","input_schema":{"type":"object","properties":{}}}],'\
'"messages":['\
'{"role":"user","content":[{"type":"text","text":"run it"}]},'\
'{"role":"assistant","content":[{"type":"tool_use","id":"toolu_shape_17","name":"Bash","input":{}}]},'\
'{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_shape_17","content":"ok"}]},'\
'{"role":"assistant","content":[{"type":"text","text":"ok"}]}]}'

# (1) hit the fake upstream directly: prove this rule is really deployed (not secretly altered by the proxy).
CODE=$(curl -s -o "$SANDBOX/shape_direct.out" -w '%{http_code}' -X POST \
  "http://127.0.0.1:$UP_PORT/v1/messages" \
  -H 'Content-Type: application/json' -d "$SHAPE_BODY")
check "direct to upstream: 400 must be passed back" \
  "$( [ "$CODE" = "400" ] && command grep -q 'must be passed back' "$SANDBOX/shape_direct.out" && echo y || echo n)" "y"

# (2) same body through the proxy: every stop on the chain gets 400 -> the client must receive the upstream's original text (no silence).
RESETUP
RES_CODE=$(curl -s -o "$SANDBOX/shape_proxy.out" -w '%{http_code}' -X POST \
  "http://127.0.0.1:$PROXY_PORT/a/claude/p/glm/v1/messages" \
  -H 'Content-Type: application/json' \
  -H 'anthropic-version: 2023-06-01' -H 'x-api-key: e2e' -d "$SHAPE_BODY")
check "through proxy: client still 400" "$RES_CODE" "400"
command grep -q 'must be passed back' "$SANDBOX/shape_proxy.out" \
  && ok "through proxy: upstream original text passed through verbatim (no silence)" \
  || bad "through proxy: swallowed; body=$(head -c 200 "$SANDBOX/shape_proxy.out")"

# The [shape-400] line in the log is the **only** evidence that the rule claimed it (the forwarding path knows no upstream
# proprietary strings, it only reads the Result.Shape name).
LOG="$NEWGATE_HOME/newgate.log"
for _ in $(seq 20); do
  command grep -q '\[shape-400\]' "$LOG" 2>/dev/null && break; sleep 0.1
done
command grep -aq '\[shape-400\].*detector deepseek' "$LOG" \
  && ok "log has [shape-400] rule deepseek (claim recorded)" \
  || bad "log has no [shape-400] rule deepseek"

# (3) the breaker only counts, never trips. `newgate breaker` splits the problem bindings into two sections, and this
#     shape-400 can only appear in the "counted only, not tripped" section.
BRK_OUT="$("$BIN" breaker 2>/dev/null)"
echo "$BRK_OUT" | sed 's/^/    /'
echo "$BRK_OUT" | command grep -q 'counted only, not tripped' \
  && ok "breaker table has a counted-only-not-tripped section (where shape-400 belongs)" \
  || bad "breaker table has no counted-only section"
echo "$BRK_OUT" | command grep -q 'glm/glm-4-plus' \
  && ok "breaker table contains glm/glm-4-plus (it was accounted)" \
  || bad "breaker table does not contain glm/glm-4-plus"
echo "$BRK_OUT" | command grep -qE '· 0 tripped' \
  && ok "no binding was tripped (shape 400 only counts)" \
  || bad "a binding was tripped (shape 400 should not trip): $(echo "$BRK_OUT" | command grep 'tripped' | head)"

# (4) the shape counter on the metrics side must go up.
"$BIN" metrics 2>/dev/null | command grep -q 'breaker.skipped.shape_error' \
  && ok "metrics has breaker.skipped.shape_error" \
  || bad "metrics is missing breaker.skipped.shape_error"

echo; echo "== 18. the three contracts on the patch side: never fabricate, minimal wording, cause classification =="
# This section locks the three things set on 2026-09-18, all on the deepseek plugin's rewrite path.
# Here we hit the **proxy** directly and read the body the fake upstream received: these are pure byte surgery, and what the fake upstream receives
# is exactly the bytes the upstream would receive.
#
# (a) **Never fabricate**. The upstream's round never gave any reasoning, so the thing the "must be passed back" requirement asks for
#     does not exist at all, and what right do we have to make one up - fabricated words go into the upstream, into the conversation
#     history, and burn tokens every round, with zero information content. What used to be added was "No thinking in this round",
#     now it is **skip this message, add not one byte**. The assistant's
#     tool_use id in this request never entered thinkcache => no original text => must stay untouched.
#
# (b) **the tail fix uses minimal wording**. The sentence move 4 appends to the tail goes into the upstream, and also into the user's
#     next round's conversation history, and the longer it is the more it looks like "someone is speaking for me". The user's own words were "use the fewest words,
#     like ("continue") this". So the assertion is that it equals `continue` **word for word**, not "non-empty and short"
#     - the latter would still pass with a long sentence.
#
# (c) **the cause must be reported by category**. Skipping is the **outcome**, a bare count cannot be traced: was it "the upstream never
#     gave it" or "it gave it but we did not store it", and the remedies are exact opposites (the former can only be accepted, the latter requires
#     checking the thinkcache hit rate). Three categories:
#       notool   plain text turn, looked up by body hash, miss
#       nocache  has tool_use, but its id is not found in thinkcache
#       nokey    neither tool_use nor body, the message cannot be identified
#     The tool_use id in this request was never cached, so it must be nocache.
#
# The request explicitly writes reasoning_content "this round already has reasoning, the plugin must not touch it": the plugin only fills in
# **missing** fields, and does not touch a single byte of fields already present (existing contract). So the same request gives two
# samples at once: the first assistant has original text (must not be touched), and the tail user turn is a bare tool_result
# (must be fixed).
PH_BODY='{"model":"deepseek-flash","max_tokens":16,"stream":false,'\
'"thinking":{"type":"enabled"},'\
'"messages":['\
'{"role":"user","content":[{"type":"text","text":"run it"}]},'\
'{"role":"assistant","reasoning_content":"this round already has reasoning, the plugin must not touch it",'\
'"content":[{"type":"text","text":"ok"},{"type":"tool_use","id":"t-never-cached-e2e","name":"Bash","input":{}}]},'\
'{"role":"user","content":[{"type":"tool_result","tool_use_id":"t-never-cached-e2e","content":"ok"}]}]}'

RESETUP
CODE=$(curl -s -o "$SANDBOX/ph_proxy.out" -w '%{http_code}' -X POST \
  "http://127.0.0.1:$PROXY_PORT/a/claude/p/ds/v1/messages" \
  -H 'Content-Type: application/json' \
  -H 'anthropic-version: 2023-06-01' -H "x-api-key: e2e" -d "$PH_BODY")
check "through proxy: thinking on, bare tool_result at tail -> 200 (shape fixed)" "$CODE" "200"

# Turn the contract into key=value lines one by one, then check each one - no read word splitting
# (read -r A B splits on whitespace, and text with spaces in it misaligns), and no substring matching.
PH_SUM=$(curl -s "http://127.0.0.1:$UP_PORT/__mock/requests" | python3 -c '
import json,sys
r=json.load(sys.stdin)
if not r: print("NO_REQUEST"); raise SystemExit
msgs=r[-1]["body"]["messages"]
a=[m for m in msgs if m.get("role")=="assistant"][0]
blocks=a.get("content") or []
if not isinstance(blocks,list): print("NOT_ARRAY"); raise SystemExit
kinds=[b.get("type") for b in blocks if isinstance(b,dict)]
texts=[b.get("text","") for b in blocks if isinstance(b,dict) and b.get("type")=="text"]
lu=[m for m in msgs if m.get("role")=="user"][-1].get("content")
tail=[b.get("type") for b in lu] if isinstance(lu,list) else ["STR"]
tailtext=[b.get("text","") for b in lu if isinstance(b,dict) and b.get("type")=="text"] if isinstance(lu,list) else []
print("thinking_blocks=" + str(kinds.count("thinking")))
print("reasoning_content=" + a.get("reasoning_content","<ABSENT>"))
print("tail=" + ",".join(tail))
print("tail_text=" + (tailtext[0] if tailtext else "<NONE>"))')
echo "$PH_SUM" | sed 's/^/    /'
ph_get() { echo "$PH_SUM" | command grep "^$1=" | cut -d= -f2-; }

# (a) never fabricate: this assistant has no original text => no thinking block inserted, no reasoning_content added.
check "no original text => no thinking block inserted (no placeholder fabricated)" "$(ph_get thinking_blocks)" "0"
check "no original text => the reasoning_content already written stays word for word" \
  "$(ph_get reasoning_content)" "this round already has reasoning, the plugin must not touch it"
# (b) the tail shape was fixed, and what was appended is **word for word** that minimal instruction.
check "tail went from bare tool_result to tool_result+text" "$(ph_get tail)" "tool_result,text"
check "the appended instruction is literally the word continue (fewest words)" "$(ph_get tail_text)" "continue"

# (c) cause classification. The line in the log must state both "skipped" and "why" (nocache).
LOGTAIL=$(tail -60 "$NEWGATE_HOME/newgate.log")
case "$LOGTAIL" in
  *"left untouched"*"nocache"*) ok "the log says it skipped, and classifies the cause as nocache (has tool_use, cache lookup fails)" ;;
  *) bad "the log does not explain the skip reason (should have \"left untouched\" + \"nocache\"): $(echo "$LOGTAIL" | tail -2)" ;;
esac
# There used to be another assertion here: "the log must not contain the word placeholder - the placeholder fabrication path should already be
# deleted". **It was an empty assertion**: the sentence it looked for was left behind by that already-deleted path, so from the day it was written
# it never went red (always true). During the 2026-09-20 migration it was first turned into a positive assertion (the log must say
# "no placeholder fabricated"), and as soon as it ran it turned out that sentence belongs to `newgate deepseek`'s audit output, **which this scenario
# does not go through** - so it became another fake assertion.
#
# No keeping up appearances: the real guarantees are at both ends - the two assertions above (skipping must be explained, the cause must be classified as
# nocache), and modules/deepseek's own unit tests. "Placeholder fabrication" has no assertable
# output to the UI today, and keeping a fake one would only make the next person think someone is watching here.

echo; echo "== 25. global no-think switch (newgate nothink) =="
# This section locks that **the switch really changed the bytes the upstream receives**. It changes the client's **written intent**:
# every Claude Code main-loop request carries thinking:{"type":"adaptive"} (the 2026-09-29
# live dump err-400-req000010: stream=true, 28 tools, model=heavy), so
# the rule "respect the thinking the client wrote" is exactly the opposite here - without overriding it, this switch
# would be as good as off. The rule can only rest on the body the upstream receives, not on "the command did not error".
NK_BODY='{"model":"deepseek-flash","max_tokens":16,"stream":false,'\
'"thinking":{"type":"adaptive"},'\
'"messages":[{"role":"user","content":[{"type":"text","text":"hello"}]}]}'

# nk_thinking fires one Anthropic-dialect request and prints the thinking value **the upstream received**.
nk_thinking() {
  RESETUP
  curl -s -o /dev/null -X POST "http://127.0.0.1:$PROXY_PORT/p/ds/v1/messages" \
    -H 'Content-Type: application/json' -H 'anthropic-version: 2023-06-01' \
    -H 'x-api-key: e2e' -d "$NK_BODY"
  curl -s "http://127.0.0.1:$UP_PORT/__mock/requests" | python3 -c '
import json,sys
r=json.load(sys.stdin)
if not r: print("NO_REQUEST"); raise SystemExit
b=r[-1]["body"]
print("ABSENT" if "thinking" not in b else json.dumps(b["thinking"], sort_keys=True))'
}

check "off (default): the adaptive the client wrote is sent to the upstream as is" "$(nk_thinking)" '{"type": "adaptive"}'

"$BIN" nothink forever >/dev/null 2>&1
# config hot reload: after the command writes state.json, NotifyProxy sends SIGHUP and the watcher reloads.
sleep 0.5
check "newgate nothink forever: adaptive is overwritten to disabled" "$(nk_thinking)" '{"type": "disabled"}'

# Not silent: when it is on, status should show it (it changes model behavior, and the symptom will not say who did it),
# and the log should record what this request changed.
#
# First capture into a variable and then match with case, **do not** write `… | grep -q`: `grep -q` exits on the first hit,
# the upstream process gets SIGPIPE before it finishes writing, and the script runs with pipefail on - so even a hit is judged a failure
# (measured 2026-10-14: this assertion went red exactly this way the first time it ran).
STATUS_OUT="$("$BIN" status 2>&1)"
case "$STATUS_OUT" in
  *Nothink*) ok "status prints this line (not silent)" ;;
  *) bad "status does not mention this switch: $(echo "$STATUS_OUT" | head -6 | tr '\n' ' ')" ;;
esac
case "$(tail -40 "$NEWGATE_HOME/newgate.log")" in
  *"nothink:"*"disabled"*) ok "the log states what this request changed (not silent)" ;;
  *) bad "no nothink note in the log (a rewrite must leave a trace)" ;;
esac

# OpenAI dialect **has no thinking field to write** (opencode / codex go that way): even when it is on
# we must not stuff in a field it does not know - the scene where we added disabled for them was "deepseek no
# longer thinks" (2026-09-15, see modules/claudecode_glm). So the switch claims by **dialect**.
RESETUP
curl -s -o /dev/null -X POST "http://127.0.0.1:$PROXY_PORT/p/ds/v1/chat/completions" \
  -H 'Content-Type: application/json' \
  -d '{"model":"deepseek-flash","max_tokens":16,"messages":[{"role":"user","content":"hello"}]}'
GOT=$(curl -s "http://127.0.0.1:$UP_PORT/__mock/requests" | python3 -c '
import json,sys
r=json.load(sys.stdin)
if not r: print("NO_REQUEST")
else: print("ABSENT" if "thinking" not in r[-1]["body"] else "PRESENT")')
check "OpenAI-dialect requests are not touched by a single byte" "$GOT" "ABSENT"

"$BIN" nothink off >/dev/null 2>&1
sleep 0.5
check "newgate nothink off: back to the adaptive the client wrote" "$(nk_thinking)" '{"type": "adaptive"}'

echo; echo "== 26. named profile and breaker recovery (the 2026-11-04 scene) =="
# Scene shape: the global profile is not ds, and the only ds binding bound to deepseek-flash has been
# tripped, and the client sends the real model name ds injected. Old behavior got all three things wrong:
#   - the reverse lookup followed the admission filter -> the model was removed from the chain -> 404 "neither a tier nor in any
#     profile's bindings", while it was clearly bound in ds;
#   - naming (--profile) only truncated after resolution -> when every candidate of the named profile is gone, the chain head is already
#     someone else's profile, and the truncation leaves a silent profile switch;
#   - after a successful probe one still had to wait out the 60s isolation before returning to the chain -> the user's only recovery action became a placebo.
# This section uses an upstream that is **down first, up later** (rev, nothing serving on its port) to verify the three things separately.
check_port_busy() { curl -sf "http://127.0.0.1:$REVIVE_PORT/__mock/requests" >/dev/null 2>&1; }
if check_port_busy; then
  bad "something is already listening on port $REVIVE_PORT, the premise of this section fails (change NEWGATE_E2E_REVIVE_PORT)"
else
  ok "the rev upstream is unreachable right now (nothing is listening on port $REVIVE_PORT)"
fi

# One failed active probe is enough to trip a binding (probes are not bound by the consecutive-failure threshold).
"$BIN" probe rev >/dev/null 2>&1 || true
BRK="$("$BIN" breaker 2>&1)"
case "$BRK" in
  *"rev/rev-model"*) ok "the failed probe tripped rev/rev-model on the spot" ;;
  *) bad "rev/rev-model not found in the breaker table after the failed probe" ;;
esac

# 1) name rev: when rev has no candidates we **must not** fall back to ds's.
RESETUP
CODE=$(curl -s -o "$SANDBOX/pin_unavailable.out" -w '%{http_code}' -X POST \
  "http://127.0.0.1:$PROXY_PORT/a/claude/p/rev/v1/messages" \
  -H 'Content-Type: application/json' \
  -d '{"model":"rev-model","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}')
check "name rev, rev is tripped: 404" "$CODE" "404"
case "$(cat "$SANDBOX/pin_unavailable.out")" in
  *"bound to tier mid"*) ok "the 404 recognizes the model's tier (it no longer says the model is in no profile's bindings)" ;;
  *) bad "the 404 text does not recognize the tier: $(cat "$SANDBOX/pin_unavailable.out")" ;;
esac
N_UP=$(curl -s "http://127.0.0.1:$UP_PORT/__mock/requests" | python3 -c 'import json,sys; print(len(json.load(sys.stdin)))')
check "naming rev did not silently switch to ds's upstream" "$N_UP" "0"

# 2) no naming: the model name is still recognized, only that binding is unavailable -> fall back on the same-tier chain.
RESETUP
CODE=$(curl -s -o "$SANDBOX/fallback.out" -w '%{http_code}' -X POST \
  "http://127.0.0.1:$PROXY_PORT/v1/messages" \
  -H 'Content-Type: application/json' \
  -d '{"model":"rev-model","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}')
check "model unavailable when not named: falls back on the mid chain and returns 200" "$CODE" "200"
GOT=$(curl -s "http://127.0.0.1:$UP_PORT/__mock/requests" | python3 -c '
import json,sys
r=json.load(sys.stdin)
print(r[-1]["body"].get("model") if r else "NO_REQUEST")')
check "fallback lands on ds's mid candidate" "$GOT" "deepseek-flash"

# 3) upstream recovers + one successful probe -> the named request can go **immediately** (no waiting for the 60s isolation).
python3 "$CORE/mock/fake_upstream.py" --port "$REVIVE_PORT" >"$SANDBOX/revive_upstream.log" 2>&1 &
REVIVE_PID=$!
for _ in $(seq 30); do
  check_port_busy && break; sleep 0.1
done
check_port_busy || { bad "the fake upstream for recovery did not come up on $REVIVE_PORT"; }
# The probe is authoritative evidence: success closes the breaker. The cooldown (60s) has not passed, so this line goes green only if the probe
# can lift the isolation early - exactly the 2026-11-04 regression of "still 404 after the probe".
"$BIN" probe rev >/dev/null 2>&1 || true
curl -sf "http://127.0.0.1:$REVIVE_PORT/__mock/reset" -X POST >/dev/null 2>&1 || true
CODE=$(curl -s -o "$SANDBOX/pin_recovered.out" -w '%{http_code}' -X POST \
  "http://127.0.0.1:$PROXY_PORT/a/claude/p/rev/v1/messages" \
  -H 'Content-Type: application/json' \
  -d '{"model":"rev-model","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}')
check "after a successful probe the named request immediately returns 200 (no waiting 60s)" "$CODE" "200"
GOT=$(curl -s "http://127.0.0.1:$REVIVE_PORT/__mock/requests" | python3 -c '
import json,sys
r=json.load(sys.stdin)
print(r[-1]["body"].get("model") if r else "NO_REQUEST")')
check "after recovery it goes through the binding of the named profile" "$GOT" "rev-model"

echo
echo "result: $PASS passed, $FAIL failed"
[ "$FAIL" -eq 0 ]