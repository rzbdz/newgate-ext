#!/usr/bin/env bash
# 零 token 端到端：DeepSeek 尾部形状修复的**出身判据**（第 4 手 messages 方言、
# 第 5 手 Responses 方言）。
#
# 背景（2026-09-18 与 2026-09-22 两轮，打真实 smt-deepseek/deepseek-flash 逐格实测）：
# 「裸尾」（messages 的最后一条 user 轮只有 tool_result；Responses 的 input[] 以
# function_call_output 收尾）到底会不会被上游拒，**不只看形状**——还要看那条
# tool call 是谁产的。聚合器（newgate 走的那家）记得自己发过的 id，续写那一轮靠
# 这份记忆把推理补回去；不是我产的 id 它补不上，一路漏到 DeepSeek，才报那句
# 「must be passed back」。完整推导见 core/docs/06-reasoning.md §1.1。
#
# 判据因此是**两维**（形状 + 出身），而这一层最容易静默错掉的就是第二维：
#
#   A  **自产的那一族不能少认**。一轮里并行发几个 tool call，聚合器就按
#      `call_00_`、`call_01_`、… 依次编号——只认 `call_00_` 会把「一轮发了两个
#      工具」这种最常见的正常轮次判成外来，于是**健康链路也被塞一句话**
#      （2026-09-21 全天 5060 次注入，约三分之一是这种）。
#   B  **认不出的形态必须按外来处理**。漏补的后果不是报错而是**静默沿链转移**
#      （客户端拿到下一家的 200，日志里看不出中间那家拒过）。
#
# 假上游按路径复用内核的 `core/mock/fake_upstream.py`（不复制：它是逐字节复刻
# 真实上游行为的产物，复制必然漂移，而漂移出来的是「绿的假测试」）。它**不模拟**
# 聚合器那份「记不记得这个 id」的记忆，所以这条 e2e 断言的是**我们发出去的字节**
# （`/__mock/requests`），而不是上游的 200/400——真正那条契约由
# `mock/e2e_reasoning_affinity.sh` 那种打真实上游的脚本按需锁。
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
SANDBOX="${NEWGATE_E2E_SANDBOX:-$(mktemp -d /tmp/newgate-tailorigin.XXXXXX)}"
# 端口与另外几条 e2e 错开：几条可以同时跑。
UP_PORT="${NEWGATE_E2E_UP_PORT:-18095}"
PORT="${NEWGATE_E2E_TAILORIGIN_PORT:-18096}"
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
# provider 名里带 deepseek，DeepSeek 那个模型家族的 MatchTarget 才认（它看的是
# 模型名 / provider 名 / base URL 任一处）。这是这一次请求会被认领的前提。
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

# POST <请求体文件> <输出文件> <路径> → HTTP 状态码。
# agent 那一段不能省：`special.Request.Agent` 从那里来，少了它交叉插件不认领
# （见 mock/e2e_codex_tools.sh 的同一处注释）。
POST() {
  curl -sS -o "$2" -w '%{http_code}' -X POST \
    "http://127.0.0.1:$PORT/a/codex/p/ds$3" \
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
# 注入了没有：最后一条消息/项里有没有那句 `continue`。
INJ_TAIL='json.dumps(body[end][textkey])'
NOTE4="the trailing user turn holds only tool_result"
NOTE5="the Responses input[] ends on a function_call_output"

# 一次请求之后等日志落盘：note 与响应是同一条线程写的，但日志是追加写，
# 慢一步读会读到上一发（其它 e2e 也这么处理）。
settle() { sleep 1; }

echo
echo "== 1. Responses：外来 call_id 收尾 → 追加一条普通 user 消息（第 5 手动手） =="
cat > "$SANDBOX/req-resp-foreign.json" <<'JSON'
{"model":"normal","stream":false,"input":[
 {"type":"message","role":"user","content":[{"type":"input_text","text":"go"}]},
 {"type":"function_call","call_id":"call_faedac561213487ebeea7731","name":"echo","arguments":"{}"},
 {"type":"function_call_output","call_id":"call_faedac561213487ebeea7731","output":"x"}]}
JSON
RESET
check "照常 200（假上游不模拟那份记忆，所以状态码不在这里断言）" \
  "$(POST "$SANDBOX/req-resp-foreign.json" "$SANDBOX/out1.json" /v1/responses)" "200"
settle
check "上游收到的 input[] 多了一项，正文逐字是 continue" \
  "$(UPSAW 'json.dumps([i for i in body["input"] if i.get("type")=="message"][-1])')" \
  '{"type": "message", "role": "user", "content": [{"type": "input_text", "text": "continue"}]}'
check "多出来的那一项在**末尾**（判据看的就是最后一项）" \
  "$(UPSAW 'body["input"][-1]["content"][0]["text"]')" "continue"
has "日志里报了第 5 手动手" "$NOTE5" "$LOG"

echo
echo "== 2. Responses：全自产（call_00_ + call_01_，一轮两个工具）→ 一个字节都不动 =="
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
check "上游收到的 input[] 一项不多一项不少（5 项，原样）" \
  "$(UPSAW 'len(body["input"])')" "5"
# 比 input[] 而不是整个 body：model 那一段被路由改写过（normal → mock-normal），
# 那是**路由**的事，不是这一手的事。
check "发出去的 input[] 与客户端发的逐字节相同" \
  "$(UPSAW 'json.dumps(body["input"], sort_keys=True, ensure_ascii=False)')" \
  "$(python3 -c 'import json,sys; print(json.dumps(json.load(open(sys.argv[1]))["input"], sort_keys=True, ensure_ascii=False))' "$SANDBOX/req-resp-self.json")"
NOTE_COUNT_BEFORE="$(command grep -cF "$NOTE5" "$LOG" 2>/dev/null || echo 0)"

echo
echo "== 3. Responses：认不出的形态（序号只有一位）→ 按外来处理，照旧补 =="
sed 's/call_00_aaa111/call_0_aaa111/g' "$SANDBOX/req-resp-self.json" > "$SANDBOX/req-resp-odd.json"
RESET
POST "$SANDBOX/req-resp-odd.json" "$SANDBOX/out3.json" /v1/responses >/dev/null
settle
check "上游收到的 input[] 变成 6 项（多出那条 continue）" \
  "$(UPSAW 'len(body["input"])')" "6"
NOTE_COUNT_AFTER="$(command grep -cF "$NOTE5" "$LOG" 2>/dev/null || echo 0)"
check "日志里第 5 手的次数 +1" "$((NOTE_COUNT_AFTER - NOTE_COUNT_BEFORE))" "1"

echo
echo "== 4. messages：裸 tool_result 尾 + 全自产 → **不注入**（健康链路的回归点） =="
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
check "上游收到的尾部那一轮还是两个 tool_result，没有多出 text 块" \
  "$(UPSAW 'json.dumps([b.get("type") for b in body["messages"][-1]["content"]])')" \
  '["tool_result", "tool_result"]'
hasnt "日志里没有第 4 手动手的痕迹" "$NOTE4" "$LOG"

echo
echo "== 5. messages：裸尾 + 外来 id → 注入，正文逐字 continue =="
cat > "$SANDBOX/req-msg-foreign.json" <<'JSON'
{"model":"normal","stream":false,"messages":[
 {"role":"user","content":[{"type":"text","text":"go"}]},
 {"role":"assistant","content":[{"type":"tool_use","id":"call_faedac561213487ebeea7731","name":"Read","input":{}}]},
 {"role":"user","content":[{"type":"tool_result","tool_use_id":"call_faedac561213487ebeea7731","content":"ok"}]}]}
JSON
RESET
POST "$SANDBOX/req-msg-foreign.json" "$SANDBOX/out5.json" /v1/messages >/dev/null
settle
check "尾部那一轮变成 tool_result + text" \
  "$(UPSAW 'json.dumps([b.get("type") for b in body["messages"][-1]["content"]])')" \
  '["tool_result", "text"]'
check "补进去的那句话逐字是 continue（措辞最小、不是长句）" \
  "$(UPSAW 'body["messages"][-1]["content"][-1]["text"]')" "continue"
has "日志里报了第 4 手动手" "$NOTE4" "$LOG"

echo
echo "== 6. 开关点：deepseek.tail-shape-responses 关掉之后第 5 手停手 =="
"$BIN" plugin deepseek.tail-shape-responses off 5m >"$SANDBOX/off.log" 2>&1
sleep 1.5   # 等一次配置重载（开关与 providers.json 走同一条路）
RESET
POST "$SANDBOX/req-resp-foreign.json" "$SANDBOX/out6.json" /v1/responses >/dev/null
settle
check "上游收到的 input[] 一项不多（3 项，停手了）" \
  "$(UPSAW 'len(body["input"])')" "3"
"$BIN" plugin deepseek.tail-shape-responses on >"$SANDBOX/on.log" 2>&1
sleep 1.5
RESET
POST "$SANDBOX/req-resp-foreign.json" "$SANDBOX/out7.json" /v1/responses >/dev/null
settle
check "打开之后又补上了（4 项）" "$(UPSAW 'len(body["input"])')" "4"

echo
echo "结果: $PASS 通过, $FAIL 失败"
[ "$FAIL" = "0" ]
