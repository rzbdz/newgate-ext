#!/usr/bin/env bash
# 零 token 端到端：**Codex（Responses 方言）与 Claude（messages 方言）拿到同一份迁移与
# 补丁**。这条 e2e 存在的原因是用户 2026-09-23 的一句话：
#
#   「from now on you must run full codex test, make sure code adaption is good as
#    claude one.」／「including all response request, have same migration as claude
#    anthropic messages request.」
#
# 也就是说：凡是给 messages 方言做过的两手（思考强度不受支持时改写、跨上游接手未闭合
# tool loop 时做有损重建），Responses 方言上必须各有一份等价物，而且**得有人验**。
# 两边各写一遍、只测一边，就是这条 e2e 要防的事——它锁的四件事全部是「不报错」的那
# 一类，绿得毫无信息：
#
#   1  **思考强度那一手**。模型「始终思考」而请求里写着它不收的值（medium/minimal/none）
#      时，`reasoning.effort` 要被改写成 low。改不动的症状不是报错，是**上游把失败
#      塞进事件流的 200 里**（stream:true → `response.failed`），客户端只看到
#      「stream disconnected before completion」——用户 2026-09-22 报的就是这句。
#   2  **重启/换版后的第一发**。quirk 表活内存里，而流式那条路的失败走不到
#      `learnQuirks`（它只看 status>=400，流式是 200）。所以这一手必须同时认
#      probe-capabilities.json 那份落盘缓存，否则每次重启后第一发必然 400、过一会儿
#      自己好——最难复现也最难解释的一类故障。
#   3  **跨上游迁移那一手**。DeepSeek 接手别家未闭合的 reasoning/tool 状态稳定 400
#      （A/B 实测见 modules/deepseek/st-reasoning.go 文件头），要往 input[] 末尾追加
#      一条普通 user 指令。这一手要成立，前面**三处**都得对：观测侧记下 call_id 的
#      出身（thinkcache）、请求侧认出「尾部是 function_call_output」（ContinuationOrigin）、
#      迁移侧真的往 input[] 里加东西（RebaseToolLoop）。少一处的症状都是**静默**：
#      请求原样发出去，客户端拿到上游的 400，而日志里看不出我们本来该插手。
#   4  **不该动的一个字节都不动**。上游收下的强度（low/high/max）、形状认不出的
#      （effort 缺席、`summary:auto`）、出身不明的 call_id，一律放行——「收下的也
#      改写」是替用户降档，「认不出就猜」是给无关请求乱加字段，两种都比不修更糟。
#
# 假上游按路径复用内核的 `core/mock/fake_upstream.py`（不复制：它是逐字节复刻真实
# 上游行为的产物，复制必然漂移，而漂移出来的是「绿的假测试」）。它按 2026-09-22 的
# 实测复刻了 `reasoning.effort` 那两句 400——**包括流式那一支先 200 开流、再把失败
# 塞进 `response.failed`**，那正是这条 e2e 的非流式部分验不到的形状。
#
# 全部在临时沙箱里跑，不碰真实 ~/.config 与 ~/.codex。

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
# 端口与另外几条 e2e 错开：几条可以同时跑。
UP_PORT="${NEWGATE_E2E_UP_PORT:-18099}"
PORT="${NEWGATE_E2E_CODEXRESP_PORT:-18100}"
BIN="$SANDBOX/bin/newgate"

PASS=0; FAIL=0
ok()   { echo "  ✓ $1"; PASS=$((PASS+1)); }
bad()  { echo "  ✗ $1"; FAIL=$((FAIL+1)); }
check(){ if [ "$2" = "$3" ]; then ok "$1"; else bad "$1 (期望 '$3'，实际 '$2')"; fi; }
has()  { if command grep -qF "$2" "$3" 2>/dev/null; then ok "$1"; else bad "$1（$3 里找不到 $2）"; fi; }
hasnt(){ if command grep -qF "$2" "$3" 2>/dev/null; then bad "$1（$3 里不该有 $2）"; else ok "$1"; fi; }

export NEWGATE_HOME="$SANDBOX/ng"
export NEWGATE_LANG=en
# 沙箱要密闭：跑 e2e 的会话自己可能带着 newgate 注入的窗口声明（嵌套启动时父进程
# env 会漏给子进程）。
unset CLAUDE_CODE_MAX_CONTEXT_TOKENS CLAUDE_CODE_AUTO_COMPACT_WINDOW
unset LC_ALL LC_MESSAGES LANG LANGUAGE 2>/dev/null || true
mkdir -p "$NEWGATE_HOME/mappings" "$(dirname "$BIN")"

cleanup() {
  [ -n "${UP_PID:-}" ] && kill "$UP_PID" 2>/dev/null
  [ -n "${SERVE_PID:-}" ] && kill "$SERVE_PID" 2>/dev/null
  # 优雅交接之后端口归一个新进程（pid/lock 也跟着改到它名下），它不在这个脚本的
  # 进程组里，上面那两个 kill 碰不到——不收的话这个沙箱的端口会一直占着，下一次
  # 跑这条 e2e 就变成「新进程永远起不来」，而症状是**满屏无关的失败**（日志里
  # 「端口被占」那行之外看不出任何线索，2026-09-23 实测踩过）。
  if [ -n "${HANDED_OVER:-}" ]; then
    stop_handed_over
  fi
  if [ -n "${NEWGATE_E2E_KEEP:-}" ]; then
    echo "沙箱保留（NEWGATE_E2E_KEEP）: $SANDBOX"
  else
    rm -rf "$SANDBOX"
  fi
}
trap cleanup EXIT

echo "沙箱: $SANDBOX"
[ -x "$BIN_SRC" ] || { echo "找不到二进制 $BIN_SRC——先跑 build/build.sh"; exit 1; }
[ -f "$CORE/mock/fake_upstream.py" ] || {
  echo "找不到内核的假上游 $CORE/mock/fake_upstream.py"
  echo "core/ 是个 submodule，先：git submodule update --init --recursive"
  exit 1
}
cp "$BIN_SRC" "$BIN"

# ---- 假上游 ----
python3 "$CORE/mock/fake_upstream.py" --port "$UP_PORT" >"$SANDBOX/upstream.log" 2>&1 &
UP_PID=$!
for _ in $(seq 1 50); do
  curl -sf -o /dev/null "http://127.0.0.1:$UP_PORT/__mock/requests" && break
  sleep 0.1
done

# ---- 配置 ----
# 两个上游：A（`up`，名与 base 里都没有 deepseek，所以 DeepSeek 的插件不认领它）是
# tool loop 的**出身**；B（`deepseek`）是接手的那一家。第一次请求走 A，之后把这条链
# 换成 B——「同一轮 tool loop 换了一家上游」正是跨上游迁移要处理的现场。
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

# graceful_handover：走**换版那条路**把 socket 交给一个新进程（daemon.SpawnHandoff）。
#
# 为什么这条 e2e 非有它不可：用户报的那次故障发生在**换版之后的第一发**，而换版
# 走的是交接（新进程起来、老进程排空），不是「杀掉再起」。两条路对落盘的时机要求
# 完全一样、代码却完全不重叠——只测其中一条，另一条上的顺序错误（比如「先交棒再
# 落盘」）是测不出来的。2026-09-23 实测踩过：那种顺序下新进程读到的仍是旧缓存，
# 换版后的第一发照样 400。
graceful_handover() {
  HANDED_OVER=1
  local tok
  tok=$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["control_token"])' "$NEWGATE_HOME/state.json")
  curl -s -X POST -H "Authorization: Bearer $tok" "http://127.0.0.1:$PORT/__newgate/upgrade" >"$SANDBOX/upgrade$1.json"
  # 新进程接上要一会儿；SERVE_PID 此刻已经死了（老进程 os.Exit(0)，它**不是**
  # 我们的子进程了——交接会把 pid/lock 改到新进程名下）。
  for _ in $(seq 1 80); do
    curl -sf -o /dev/null "http://127.0.0.1:$PORT/__newgate/status" && return 0
    sleep 0.15
  done
  echo "交接之后端口没接上，见 $SANDBOX/upgrade$1.json"; return 1
}

# 交接之后端口归新进程，cleanup 里那个 kill 够不着它——按 pidfile 收。
stop_handed_over() {
  local pid
  pid=$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["pid"])' "$NEWGATE_HOME/.newgate.pid" 2>/dev/null) || return 0
  [ -n "$pid" ] && kill "$pid" 2>/dev/null
}

start_serve() {
  "$BIN" __serve --port "$PORT" >"$SANDBOX/serve$1.log" 2>&1 &
  SERVE_PID=$!
  for _ in $(seq 1 50); do
    curl -sf -o /dev/null "http://127.0.0.1:$PORT/__newgate/status" && return 0
    sleep 0.1
  done
  echo "守护进程没起来，见 $SANDBOX/serve$1.log"; return 1
}
start_serve 1 || exit 1
LOG="$NEWGATE_HOME/newgate.log"

# POST <请求体文件> <输出文件> → HTTP 状态码。agent 那一段不能省：`special.Request.Agent`
# 从 /a/codex/ 来，那是 codex 这条路上补丁认领的前提（见 mock/e2e_codex_tools.sh）。
POST() {
  curl -sS -o "$2" -w '%{http_code}' -X POST \
    "http://127.0.0.1:$PORT/a/codex/p/ds/v1/responses" \
    -H 'Content-Type: application/json' --data-binary @"$1"
}
RESET() { curl -s -X POST "http://127.0.0.1:$UP_PORT/__mock/reset" >/dev/null; }
# UPSAW <表达式> — 对上游收到的**最后一条**请求体求值（表达式里 body 就是它）。
UPSAW() {
  curl -s "http://127.0.0.1:$UP_PORT/__mock/requests" | python3 -c '
import json, sys
d = json.load(sys.stdin)
body = d[-1]["body"] if d else {}
print(eval(sys.argv[1], {"json": json}, {"body": body}))
' "$1"
}
# 一次请求之后等日志落盘（note 与响应同线程写，日志是追加写）。
settle() { sleep 1; }

write_req() { # write_req <文件> <JSON>
  printf '%s' "$2" > "$1"
}

echo
echo "== 1. 冷启动：quirk 表是空的 → 一个字节都不动，上游按真实判据 400 =="
# 判据是「这个 (provider, model) 已知始终思考」——没学过就不许动手（猜着改请求比
# 不改更糟）。所以第一发的 400 是**预期**的，而且它正是学习的来源。
write_req "$SANDBOX/req-medium.json" \
  '{"model":"normal","stream":false,"reasoning":{"effort":"medium"},"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}]}'
RESET
check "没学过 → 原样发出去 → 上游 400" \
  "$(POST "$SANDBOX/req-medium.json" "$SANDBOX/out1.json")" "400"
check "上游收到的 effort 仍是客户端写的 medium（没学就不许猜）" \
  "$(UPSAW 'body["reasoning"]["effort"]')" "medium"
check "400 的正文是上游那句原文" \
  "$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["error"]["message"])' "$SANDBOX/out1.json")" \
  "该模型始终思考，不支持关闭思考；请使用 low、high 或 max。"
settle
has "日志里记下了从这一发学到的东西（不静默）" "learned up/mock-normal" "$LOG"

echo
echo "== 2. 同一发再来一次：学过了 → 就地改成 low，上游 200 =="
RESET
check "学过之后同一发 200" \
  "$(POST "$SANDBOX/req-medium.json" "$SANDBOX/out2.json")" "200"
check "上游收到的 effort 被改成了 low" \
  "$(UPSAW 'body["reasoning"]["effort"]')" "low"
check "只动了那一个值：同一对象里的兄弟键与其余字节都在" \
  "$(UPSAW 'json.dumps(body["reasoning"], sort_keys=True)')" '{"effort": "low"}'
settle
has "日志里报了这一次改写（改请求就必须留痕）" 'reasoning.effort "medium" → "low"' "$LOG"

echo
echo "== 3. 上游收下的值一个字节都不动（low/high/max） =="
# 只认 low 会把用户明确设的 high/max 改成 low——那是替用户降档，属于静默改变行为。
for e in low high max; do
  write_req "$SANDBOX/req-$e.json" \
    "{\"model\":\"normal\",\"stream\":false,\"reasoning\":{\"effort\":\"$e\"},\"input\":[{\"type\":\"message\",\"role\":\"user\",\"content\":[{\"type\":\"input_text\",\"text\":\"hi\"}]}]}"
  RESET
  check "effort=$e 照常 200" "$(POST "$SANDBOX/req-$e.json" "$SANDBOX/out-$e.json")" "200"
  check "effort=$e 原样发出去" "$(UPSAW 'body["reasoning"]["effort"]')" "$e"
done

echo
echo "== 4. 形状认不出就不猜（effort 缺席 / summary:auto / effort:null） =="
for name in absent summary null; do
  case "$name" in
    absent)  r='{"model":"normal","stream":false,"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}]}' ;;
    summary) r='{"model":"normal","stream":false,"reasoning":{"summary":"auto"},"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}]}' ;;
    null)    r='{"model":"normal","stream":false,"reasoning":{"effort":null},"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}]}' ;;
  esac
  write_req "$SANDBOX/req-shape-$name.json" "$r"
  RESET
  check "$name 照常 200" "$(POST "$SANDBOX/req-shape-$name.json" "$SANDBOX/out-shape-$name.json")" "200"
  check "$name 上游收到的 reasoning 与客户端发的一模一样" \
    "$(UPSAW 'json.dumps(body.get("reasoning"), sort_keys=True)')" \
    "$(python3 -c 'import json,sys; print(json.dumps(json.load(open(sys.argv[1])).get("reasoning"), sort_keys=True))' "$SANDBOX/req-shape-$name.json")"
done

echo
echo "== 5. 换版/重启后的第一发（流式）：只有落盘缓存能救它 =="
# 这一节是用户 2026-09-22 报的那个故障的**逐字复现**：quirk 表活在内存里，重启就空；
# 而流式的失败是「HTTP 200 + 事件流里一条 response.failed」，`learnQuirks` 只看
# status>=400，永远学不到。于是「重启后第一发必 400、过一会儿自己好」。
#
# 换成真进程重启（kill + 起），再手工写一份 probe-capabilities.json——那正是
# `newgate probe` 探过一次会留下的东西，也是 `probe.LoadCachedCapabilities` 读回的
# 那份（见 core/modules/thinking/st-always.go 的 matchTarget）。
kill "$SERVE_PID" 2>/dev/null
wait "$SERVE_PID" 2>/dev/null
cat > "$NEWGATE_HOME/probe-capabilities.json" <<'JSON'
{"targets":{"up/mock-normal":{"supports":0,"known":0,"quirks":1,"checked_at":"2026-09-23T00:00:00Z"}}}
JSON
start_serve 2 || exit 1
write_req "$SANDBOX/req-stream.json" \
  '{"model":"normal","stream":true,"reasoning":{"effort":"medium"},"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}]}'
RESET
check "重启后第一发流式请求照常 200（HTTP 状态码在这一路上本来就不是判据）" \
  "$(POST "$SANDBOX/req-stream.json" "$SANDBOX/out-stream.sse")" "200"
has "流里是正常收尾（response.completed），不是那条 response.failed" \
  'response.completed' "$SANDBOX/out-stream.sse"
hasnt "流里没有 response.failed（用户看到的「stream disconnected before completion」就是它）" \
  'response.failed' "$SANDBOX/out-stream.sse"
check "上游收到的 effort 已被改成 low（缓存那一路真的接上了）" \
  "$(UPSAW 'body["reasoning"]["effort"]')" "low"

echo
echo "== 5b. 学到的东西活过**换版**：交接之后的新进程，第一发就不许再撞 =="
# 5 那一节验的是「盘上有一份时新进程能读回来」，可那一份是测试**手写**的。这一节
# 验的是另一件事，也是用户实际踩的那一次：**学到的东西自己写下去**。
#
# 两手都要落盘（换版路径 flush 在交棒之前、普通停机在排空之后），少一处的症状都是
# 「换了版之后第一发又 400」——必现，而且没有任何表面原因（用户 2026-09-22 报的
# 就是这一句：`stream disconnected before completion`）。
#
# 从干净的家目录开始，这样「盘上有东西」只可能是我们自己写下去的。
rm -f "$NEWGATE_HOME/probe-capabilities.json"
kill "$SERVE_PID" 2>/dev/null
wait "$SERVE_PID" 2>/dev/null
start_serve 5 || exit 1
check "全新进程的第一发（表是空的）：原样发出去，上游 400" \
  "$(POST "$SANDBOX/req-medium.json" "$SANDBOX/out-cold.json")" "400"
RESET
check "第二发 200（这一发之前那一条 400 已经被学到）" \
  "$(POST "$SANDBOX/req-medium.json" "$SANDBOX/out-warm.json")" "200"
check "第二发上游收到的是 low（学到的东西真的用上了）" \
  "$(UPSAW 'body["reasoning"]["effort"]')" "low"

graceful_handover 5 || exit 1
check "换版之后盘上确实有一笔我们自己学的（手写的那份已经被删掉了）" \
  "$(python3 -c 'import json,sys; d=json.load(open(sys.argv[1])); print(d["targets"]["up/mock-normal"]["quirks"])' "$NEWGATE_HOME/probe-capabilities.json")" "1"
RESET
check "**换版后的第一发就 200**（这里以前必 400：内存表空了，而落盘只发生在探活里）" \
  "$(POST "$SANDBOX/req-medium.json" "$SANDBOX/out-after-upgrade.json")" "200"

echo
echo "== 6. 跨上游迁移：同一轮 tool loop 换到 DeepSeek 接手 =="
# 第一步：tool loop 的**出身**在 A（`up`）那里产生。这一发要真的带 tools，假上游才会
# 回一条 function_call——thinkcache 就是拿它的 call_id 记出身的（CommitWithOrigin）。
write_req "$SANDBOX/req-turn1.json" \
  '{"model":"normal","stream":false,
    "input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"go"}]}],
    "tools":[{"type":"function","name":"exec_command","description":"run","parameters":{"type":"object","properties":{"cmd":{"type":"string"}}}}]}'
RESET
check "第一轮 200" "$(POST "$SANDBOX/req-turn1.json" "$SANDBOX/out-turn1.json")" "200"
# 假上游回的 call_id 在这里是**可预期**的（call_mock_<n>），所以下一轮能照抄。
check "上游回来的 function_call 带着 call_id（下一轮照抄它）" \
  "$(python3 -c 'import json,sys; o=json.load(open(sys.argv[1]))["output"]; print([i["call_id"] for i in o if i["type"]=="function_call"][0])' "$SANDBOX/out-turn1.json")" \
  "call_mock_1"
settle
has "日志里报了这一轮记下了推理与它的出身" "recorded" "$LOG"

# 第二步：把这一档换到 DeepSeek——现场就是「同一轮 tool loop 现在由别家接手」。
# 改 mappings 即生效（配置热重载），不必重启。
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
check "第二轮 200" "$(POST "$SANDBOX/req-turn2.json" "$SANDBOX/out-turn2.json")" "200"
check "上游收到的 input[] 多了一项（有损重建真的动手了）" \
  "$(UPSAW 'len(body["input"])')" "4"
check "多出来的那一项在**末尾**，是普通 user 消息，正文逐字是 continue" \
  "$(UPSAW 'json.dumps(body["input"][-1], sort_keys=True)')" \
  '{"content": [{"text": "continue", "type": "input_text"}], "role": "user", "type": "message"}'
check "原来的三项一字不动（旧 reasoning/tool 全部留作可见上下文）" \
  "$(UPSAW 'json.dumps(body["input"][:3], sort_keys=True)')" \
  "$(python3 -c 'import json,sys; print(json.dumps(json.load(open(sys.argv[1]))["input"], sort_keys=True))' "$SANDBOX/req-turn2.json")"
settle
has "日志里报了这一次有损重建（改请求就必须留痕）" \
  "lossily rebuilt a foreign tool loop" "$LOG"

echo
echo "== 7. 出身不明就不动：没见过那个 call_id 时迁移一个字节都不许加 =="
# 迁移的前提是**我们知道**这一轮的 tool call 是谁产的。重启之后 thinkcache 是空的，
# 此时「认不出出身」只能保守处理——宁可让上游按原样报错，也不能凭猜往用户的历史里
# 塞一句话（那条 continue 会长住在对话历史里）。
#
# 这一节必须把**第 5 手**（deepseek.tail-shape-responses）单独关掉，否则验不准：
# 它和迁移做的是同一件事（尾部没有新指令 → 追加一条 continue），判据却不同——它
# 只看出身**形状**（`call_NN_…` 之外的 id 一律算外来），我们没见过的 `call_mock_9`
# 在它眼里正是外来，于是它照样会补一条。两条相加的结果是「补了两条」，而这一节
# 要问的是「迁移那一手在没有出身时会不会瞎动手」。关掉第 5 手之后，唯一能往
# input[] 里加东西的只剩迁移，数出来的条数才有意义。
kill "$SERVE_PID" 2>/dev/null
wait "$SERVE_PID" 2>/dev/null
start_serve 3 || exit 1
"$BIN" plugin deepseek.tail-shape-responses off 5m >"$SANDBOX/off-tail5.log" 2>&1
sleep 1.5   # 等一次配置重载（开关与 providers.json 走同一条路）
write_req "$SANDBOX/req-turn3.json" \
  '{"model":"normal","stream":false,
    "input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"go"}]},
             {"type":"function_call","call_id":"call_mock_9","name":"exec_command","arguments":"{}"},
             {"type":"function_call_output","call_id":"call_mock_9","output":"ok"}]}'
RESET
POST "$SANDBOX/req-turn3.json" "$SANDBOX/out-turn3.json" >/dev/null
settle
check "上游收到三项，一项都不多（出身不明 → 迁移不触发）" \
  "$(UPSAW 'len(body["input"])')" "3"
BEFORE=$(command grep -cF "lossily rebuilt a foreign tool loop" "$LOG" 2>/dev/null || echo 0)
check "日志里也没有多出一次迁移（第 6 节那一次之外没有新的）" "$BEFORE" "1"
"$BIN" plugin deepseek.tail-shape-responses on >"$SANDBOX/on-tail5.log" 2>&1
sleep 1.5

echo
echo "结果: $PASS 通过, $FAIL 失败"
[ "$FAIL" = "0" ]
