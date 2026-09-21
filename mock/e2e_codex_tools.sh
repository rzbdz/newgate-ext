#!/usr/bin/env bash
# 零 token 端到端：**Codex × DeepSeek 的工具交叉**——请求侧抬/降级，响应侧改回。
#
# 背景（2026-09-21，打真实 smt-deepseek/deepseek-flash 逐格实测）：两家的形状各自
# 都对，只有放在一起才坏。
#
#   Codex 旧版把工具树挂在 `input[0].additional_tools` 里，而 DeepSeek 的
#   `/responses` **只从顶层 `tools` 读工具**。两边各按自己的规矩办事，结果模型一个
#   工具都看不见——它会退回训练里的文本格式去「调工具」，客户端解析不出来，于是
#   **一个 tool_result 都没有**（现场原话：「从来没成功获取 tool_result」）。
#   这条不报错、请求 200、正文看着也像模像样，所以只能靠端到端锁。
#
#   新版 Codex 把工具直接放顶层 `tools`（含 `namespace` 分组与 `web_search`）。
#   那份是 DeepSeek 已经在读的，但它对 `custom` 只认 `apply_patch` 一个，其余一律
#   400；藏在 `namespace` 里的 custom 还会换一句 400
#   （`Currently custom tools are not allowed inside a namespace`）。
#
# 它锁的是五件会**静默**错掉的事：
#
#   1  **旧版形状会被抬上来**。不抬的症状是「模型看不见工具」，而不是报错——见上；
#   2  **两处 custom 都会被降级**（顶层那条、namespace 里那条）。降不掉的症状是
#      上游 400，而 400 在这里恰好**不会**静默：假上游按真实判据拦，所以「仍然
#      200」本身就是降级生效的证据；
#   3  **响应侧改得回来**。降级过的工具在响应里必须变回 `custom_tool_call`（外加
#      `custom_tool_call_input.*` 那套事件名，以及 `{"input": …}` 那层壳要拆掉）；
#      **没降级过的**（原生 function）一个字节都不许动。改不回来的症状是客户端拿
#      一段它认不出的形状去 eval；
#   4  **没有 custom 时一个字节都不动**。「没有要修的就不动手」是这条路上最容易
#      被顺手破坏的规矩，而破坏了看不出来（请求照样 200）；
#   5  **开关点真的被读**。`newgate plugin codex-deepseek.lift-tools off` 报了
#      「已关闭」而请求照改不误——2026-09-21 在 live 上实测到这个，它比没有开关
#      更糟。关掉之后请求侧与响应侧都必须停手（只停一边会把客户端弄坏）。
#
# 假上游按路径复用内核的 `core/mock/fake_upstream.py`（不复制：它是逐字节复刻真实
# 上游行为的产物，复制必然漂移，而漂移出来的是「绿的假测试」）。它对 `/responses`
# 的那一支同样按实测写：工具调用从 output_index 1 起（前面有个 reasoning item）、
# 参数是一串 delta、custom 工具按真实判据 400。
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
SANDBOX="${NEWGATE_E2E_SANDBOX:-$(mktemp -d /tmp/newgate-codextools.XXXXXX)}"
# 端口与另外几条 e2e 错开：几条可以同时跑。
UP_PORT="${NEWGATE_E2E_UP_PORT:-18097}"
PORT="${NEWGATE_E2E_CODEXTOOLS_PORT:-18098}"
BIN="$SANDBOX/bin/newgate"

PASS=0; FAIL=0
ok()   { echo "  ✓ $1"; PASS=$((PASS+1)); }
bad()  { echo "  ✗ $1"; FAIL=$((FAIL+1)); }
check(){ if [ "$2" = "$3" ]; then ok "$1"; else bad "$1 (期望 '$3'，实际 '$2')"; fi; }
has()  { if command grep -q "$2" "$3" 2>/dev/null; then ok "$1"; else bad "$1（$3 里找不到 $2）"; fi; }
hasnt(){ if command grep -q "$2" "$3" 2>/dev/null; then bad "$1（$3 里不该有 $2）"; else ok "$1"; fi; }

export NEWGATE_HOME="$SANDBOX/ng"
export NEWGATE_LANG=en
# 沙箱要密闭：跑 e2e 的会话自己可能带着 newgate 注入的窗口声明（嵌套启动时父进程
# env 会漏给子进程）。
unset CLAUDE_CODE_MAX_CONTEXT_TOKENS CLAUDE_CODE_AUTO_COMPACT_WINDOW
unset LC_ALL LC_MESSAGES LANG LANGUAGE 2>/dev/null || true
mkdir -p "$NEWGATE_HOME/mappings" "$(dirname "$BIN")"

cleanup() {
  [ -n "${UP_PID:-}" ] && kill "$UP_PID" 2>/dev/null
  "$BIN" stop >/dev/null 2>&1 || true
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
# 模型名 / provider 名 / base URL 任一处）。这是**交叉插件会认领**这一次请求的
# 前提，也是这条 e2e 与「上游是谁」无关的原因。
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

# POST <请求体文件> <输出文件> → HTTP 状态码。
# codex 的真实路径带上 /a/codex/——`special.Request.Agent` 就是从那一段来的，
# 少了它交叉插件根本不认领（2026-09-21 实测踩过：走 /p/<profile>/ 时请求原样
# 发出去，看起来像「补丁没生效」）。
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
# 一份工具表的形状摘要：[(type, name), …]，顶层顺序。
SHAPE='json.dumps([[t.get("type"), t.get("name")] for t in (body.get("tools") or [])])'

echo
echo "== 1. 旧版形状：工具挂在 input[0].additional_tools 里，顶层一个都没有 =="
cat > "$SANDBOX/req-old.json" <<'JSON'
{"model":"normal","stream":true,"tool_choice":"auto",
 "input":[{"type":"additional_tools","id":"at_1","role":"developer","tools":[
    {"type":"namespace","name":"functions","description":"","tools":[
      {"type":"custom","name":"exec","description":"Run JS"},
      {"type":"function","name":"wait","description":"Wait","parameters":{"type":"object","properties":{"cell_id":{"type":"string"}},"required":["cell_id"]}}]}]},
   {"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}]}
JSON
RESET
check "旧版形状 200（不抬的话模型看不见工具，但**不会**报错）" \
  "$(POST "$SANDBOX/req-old.json" "$SANDBOX/out-old.sse")" "200"
check "上游收到了顶层 tools：两条，一条 function 一条从 custom 降下来的" \
  "$(UPSAW "$SHAPE")" '[["function", "exec"], ["function", "wait"]]'
check "降级出来的那条带着 parameters（上游的 function 要求这个字段）" \
  "$(UPSAW 'json.dumps(body["tools"][0].get("parameters"))')" \
  '{"type": "object", "properties": {"input": {"type": "string"}}, "required": ["input"]}'
has "日志里报了抬了 2 条" "lifted 2 tool declarations" "$LOG"
has "日志里报了降级 1 条" "degraded 1 custom tool to a standard function" "$LOG"

echo
echo "== 2. 响应侧：降级过的改回 custom_tool_call，原生的一个字节不动 =="
# 上游给两个工具各回一条 function_call（参数是一串 delta 吐的，与真实形态一致），
# 出站改写按 output_index 记账：exec 该变成 custom_tool_call，wait 该原样。
has "被降级的那条在流里是 custom_tool_call" '"type":"custom_tool_call"' "$SANDBOX/out-old.sse"
has "它的 arguments 事件名换成了 custom_tool_call_input" \
  '"type":"response.custom_tool_call_input.delta"' "$SANDBOX/out-old.sse"
has "收尾那条 item 也换名了" '"type":"response.custom_tool_call_input.done"' "$SANDBOX/out-old.sse"
has "原生那条仍是 function_call（没有被顺手改掉）" '"type":"function_call"' "$SANDBOX/out-old.sse"
has "参数外面那层 {\"input\": …} 的壳被拆掉了（codex 要的是里面那串原文）" \
  '"input":"MOCK-TOOL-exec"' "$SANDBOX/out-old.sse"
hasnt "拆壳之后 input 不该还包着 JSON 对象" '"input":"{\\"input\\"' "$SANDBOX/out-old.sse"

echo
echo "== 3. 新版形状：顶层 tools 里夹着一条 custom =="
cat > "$SANDBOX/req-top.json" <<'JSON'
{"model":"normal","stream":false,
 "input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}],
 "tools":[{"type":"function","name":"exec_command","description":"run","parameters":{"type":"object","properties":{"cmd":{"type":"string"}}}},
          {"type":"custom","name":"exec","description":"Run JS"}]}
JSON
RESET
check "顶层夹 custom 也 200（不降级的话上游 400 Unsupported custom tool）" \
  "$(POST "$SANDBOX/req-top.json" "$SANDBOX/out-top.json")" "200"
check "顶层那条 custom 被降成了 function，原生那条一字不动" \
  "$(UPSAW "$SHAPE")" '[["function", "exec_command"], ["function", "exec"]]'
has "日志里报了这次降级" "degraded 1 custom tool to a standard function" "$LOG"

echo
echo "== 4. 藏在 namespace 里的 custom（上游那句 400 是另一句） =="
cat > "$SANDBOX/req-ns.json" <<'JSON'
{"model":"normal","stream":false,
 "input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}],
 "tools":[{"type":"namespace","name":"functions","description":"","tools":[
    {"type":"function","name":"wait","description":"w","parameters":{"type":"object","properties":{}}},
    {"type":"custom","name":"exec","description":"Run JS"}]}]}
JSON
RESET
check "namespace 里的 custom 也 200（漏掉就是 Currently custom tools are not allowed inside a namespace）" \
  "$(POST "$SANDBOX/req-ns.json" "$SANDBOX/out-ns.json")" "200"
check "namespace 的外壳还在（DeepSeek 真的把它当分组用），里面两条都是 function" \
  "$(UPSAW 'json.dumps([body["tools"][0]["type"], [[t.get("type"), t.get("name")] for t in body["tools"][0]["tools"]]])')" \
  '["namespace", [["function", "wait"], ["function", "exec"]]]'

echo
echo "== 5. 没有 custom 时一个字节都不动 =="
cat > "$SANDBOX/req-clean.json" <<'JSON'
{"model":"normal","stream":false,
 "input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}],
 "tools":[{"type":"function","name":"exec_command","description":"run","parameters":{"type":"object","properties":{}}},
          {"type":"namespace","name":"multi_agent_v1","tools":[{"type":"function","name":"spawn_agent","description":"s","parameters":{"type":"object","properties":{}}}]},
          {"type":"web_search","external_web_access":false}]}
JSON
# 这一发该**一条 note 都没有**。日志是整条 e2e 共用的，所以先记下当下的行数，
# 再比「这几发之间没有新的 lifted/degraded 行」——按文件名抓日志会抓错人。
BEFORE=$(command grep -c 'lifted \|degraded ' "$LOG" 2>/dev/null || echo 0)
RESET
check "干净的顶层 tools 也 200" "$(POST "$SANDBOX/req-clean.json" "$SANDBOX/out-clean.json")" "200"
AFTER=$(command grep -c 'lifted \|degraded ' "$LOG" 2>/dev/null || echo 0)
check "这一发一条「抬/降级」的日志都没有（没有要修的就不动手）" "$AFTER" "$BEFORE"
# 对照：上游收到的那份必须与客户端发出去的那一份**同形**（含 namespace 与
# web_search）。比 sort_keys 的形态而不是字节：假上游记的是解析后的对象。
check "上游收到的那份与客户端发的一模一样" \
  "$(UPSAW 'json.dumps(body["tools"], sort_keys=True)')" \
  "$(python3 -c 'import json,sys; print(json.dumps(json.load(open(sys.argv[1]))["tools"], sort_keys=True))' "$SANDBOX/req-clean.json")"
# 响应侧也一个字都不该动：没有降级过的东西，就不该被改成 custom_tool_call。
hasnt "干净的流里没有 custom_tool_call（响应侧没被认领）" "custom_tool_call" "$SANDBOX/out-clean.json"

echo
echo "== 6. 开关点：关掉之后请求侧与响应侧都停手 =="
"$BIN" plugin codex-deepseek.lift-tools off 5m >"$SANDBOX/off.log" 2>&1
sleep 1.5   # 等一次配置重载（开关与 providers.json 走同一条路）
RESET
check "关掉之后照常 200" "$(POST "$SANDBOX/req-old.json" "$SANDBOX/out-off.sse")" "200"
check "上游**没有**收到顶层 tools（抬这一手真的停了）" \
  "$(UPSAW 'json.dumps(body.get("tools"))')" "null"
hasnt "响应也没被改写（只停一边会把客户端弄坏）" "custom_tool_call" "$SANDBOX/out-off.sse"
"$BIN" plugin codex-deepseek.lift-tools on >"$SANDBOX/on.log" 2>&1
sleep 1.5
RESET
check "打开之后照常抬上来" "$(POST "$SANDBOX/req-old.json" "$SANDBOX/out-on.sse")" "200"
check "又看见了顶层 tools" \
  "$(UPSAW "$SHAPE")" '[["function", "exec"], ["function", "wait"]]'

echo
echo "结果: $PASS 通过, $FAIL 失败"
[ "$FAIL" = "0" ]
