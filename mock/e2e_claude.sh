#!/usr/bin/env bash
# 零 token 端到端：**内核自己**的行为——接管注入、档位解析与转发、控制端点、
# 优雅交接、后台分类器改道、窗口声明、运行期开关。
#
# 验证（对应本次特性/修复）：
#   1. 启动时注入的 env 是**真实模型名**（claude 界面显示 deepseek-chat，
#      而不是 heavy），且 base URL 带上 /a/claude/p/<profile>。
#   2. 代理把真实模型名反解回档位，转发给正确的上游。
#   3. count_tokens：Claude Code 周期性调用（水位条/自动压缩阈值）。上游
#      听得懂（原生 anthropic 端点）就转发拿真值、model 按 mid 链头补上；
#      听不懂的（聚合器 404）由 forward 层 lazy probe 学下来退回本地粗估
#      （单测覆盖）。
#   4. 控制端点 /__newgate/stop：多用户共享部署下，读得到配置却发不出
#      信号的用户靠它停机——错令牌 403，对令牌让 daemon 退干净。
#   5. 优雅交接 /__newgate/upgrade（nginx 式零停机升级）：restart 把监听
#      socket 移交给新进程，在途 SSE 流由旧进程流完为止——开发 newgate
#      的会话本身就穿行在代理里，这是「能持续开发」的前提。
#   6. 后台请求：分类器整条链改走 light、其余只禁思考。Claude Code 的非流
#      式后台调用不带 thinking，国模却把缺省当默认思考 → 15-30 秒、成波超
#      时。代理一律补 thinking:disabled（缺就补、带了也改写）；其中 Bash
#      安全分类器本体（实抓特征：system 开头 "You are a security monitor…"）
#      在**建链之前**改道 light 档——含 fallback，light 挂了沿 light 链换
#      人，不回 mid；其他后台调用（compact 总结这类）不改道；主循环的流式
#      请求不受影响。
#   7. 窗口声明：Claude Code 不认识注入的真实模型名（glm-4-plus），按
#      「未知模型」默认 200k 窗口提前 compact。profile 里声明了
#      context_window/auto_compact_window 就在启动时注入对应的
#      CLAUDE_CODE_* env；没声明的 profile 一个都不注入。
#   8. 运行期开关与 special 层开关（第 19/20 章）：探针用内核自己的
#      schema-repair，这样这一层不依赖任何发行版模块。
#
# # 发行版模块的行为不在这里（2026-09-20 拆开）
#
# 上游怪癖补丁（DeepSeek 的推理原文回填、尾部形状修补、跨上游迁移）跟着模块
# 住在发行版仓库，由 **发行版的 mock/e2e_claude_dist.sh** 锁——它复用本目录的
# 假上游（那是逐字节复刻真实上游行为的产物，复制一份必然漂移）。拆开之前那几
# 章在本文件里，于是**内核的测试依赖一个发行版模块**：摘掉它内核就红。边界与
# 理由见 docs/09-extension-guide.md §8。
#
# 全部在临时沙箱里跑，不碰真实 ~/.config / ~/.claude / shell rc。
# # 这份脚本为什么在发行版仓库里（2026-09-20 搬来）
#
# 它原来是内核的 mock/e2e_claude.sh。这些章节全部经由**真客户端接管**（newgate
# claude → wrapper → runtime → 假 claude），而客户端接入是产品取舍——那三个模块
# （claudecode / opencode / opencodeomo）搬到了本仓库，所以它们的端到端也跟过来。
#
# 内核那一侧留下了**机制面**的 e2e（curl 打数据面 + 控制端点 + 运行期开关），
# 覆盖没有丢：两边合起来是原来那 16 章，只是分家分在「谁认识客户端」这条线上。

set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
# 二进制来自本仓库的构建入口（build/build.sh），**按 newgate 落一份**再用：
# 它是多调用型的，argv0 决定入口（见 README）。产物名里的架构用 Go 的叫法。
CORE="${NEWGATE_E2E_CORE:-$ROOT/core}"
case "$(uname -m)" in
  x86_64)        host_arch=amd64 ;;
  aarch64|arm64) host_arch=arm64 ;;
  i386|i686)     host_arch=386 ;;
  *)             host_arch=$(uname -m) ;;
esac
BIN_SRC="${NEWGATE_E2E_BIN:-$ROOT/dist/newgate-default-$(uname -s | tr 'A-Z' 'a-z')-$host_arch}"
[ -x "$BIN_SRC" ] || { echo "找不到二进制 $BIN_SRC——先跑 build/build.sh"; exit 1; }
[ -f "$CORE/mock/fake_upstream.py" ] || { echo "core/ 里没有假上游：先 git submodule update --init"; exit 1; }
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
check(){ if [ "$2" = "$3" ]; then ok "$1"; else bad "$1 (期望 '$3'，实际 '$2')"; fi; }

export NEWGATE_HOME="$SANDBOX/ng"
mkdir -p "$NEWGATE_HOME/mappings" "$FAKEBIN"
# 沙箱要密闭：跑 e2e 的会话自己可能带着 newgate 注入的窗口声明（嵌套
# 启动时父进程 env 会漏给子进程），不 unset 会让「没声明的 profile」
# 用例读到父会话的值、假失败。
unset CLAUDE_CODE_MAX_CONTEXT_TOKENS CLAUDE_CODE_AUTO_COMPACT_WINDOW
# 语言要密闭：脚本断言的是**源语言原文**（英文），而跑它的机器可能是 zh-Hans
# （我们自己的机器就是）。不钉住的话，「界面上是英文」这类断言在中文机器上假红。
export NEWGATE_LANG=en
unset LC_ALL LC_MESSAGES LANG LANGUAGE 2>/dev/null || true

cleanup() {
  "$BIN" stop >/dev/null 2>&1 || true
  [ -n "${UP_PID:-}" ] && kill "$UP_PID" 2>/dev/null
  # 调试用：NEWGATE_E2E_KEEP=1 保留沙箱（日志、假上游收到的 body 都在里面），
  # 出红的时候不至于对着一个删掉的目录猜。
  if [ -n "${NEWGATE_E2E_KEEP:-}" ]; then
    echo "沙箱保留（NEWGATE_E2E_KEEP）: $SANDBOX"
  else
    rm -rf "$SANDBOX"
  fi
}
trap cleanup EXIT

echo "沙箱: $SANDBOX"
[ -x "$BIN" ] || { echo "先 make build"; exit 1; }

# ---- 假 claude：像 Claude Code 那样发请求。E2E_SCENARIO 选形态 ----
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
    # Claude Code 主循环形态：**流式** + thinking 显式开着 + 带 tools，
    # 历史里的 assistant 消息被客户端剥掉了 thinking 块——只剩 tool_use。
    # （流式是关键：claude-bg 只改写非流式的后台调用，主循环不碰。）
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
    # Claude Code 的 count_tokens：不带 model 字段的形态也要能活
    code, body = post(base.rstrip("/") + "/v1/messages/count_tokens",
                      {"messages": [{"role": "user", "content": "数一下 token"}],
                       "tools": TOOLS})
    print(f"HTTP={code}")
    try:
        print(f"INPUT_TOKENS={json.loads(body).get('input_tokens')}")
    except Exception:
        print("INPUT_TOKENS=PARSE_FAIL")
elif scenario in ("bg_plain", "bg_adaptive", "bg_other", "bg_realname"):
    # Claude Code 后台小调用的形态（实抓 2026-09，cc 2.1.263）：非流式、
    # model 就是档位名 mid、不带 tools、max_tokens 2112。
    #   bg_plain / bg_adaptive / bg_realname = Bash 分类器本体：system ~126KB
    #     开头是 "You are a security monitor…"（bg_plain 连 thinking 都没写，
    #     bg_adaptive 显式要求 adaptive）→ 都切 light；只给没写 thinking 的
    #     bg_plain 注入 disabled，显式意图不覆盖。
    #   bg_other = 其他后台调用（compact 总结这类）：system 没有那句自报
    #     家门 → 保留 mid，只禁思考。
    #   bg_realname = 真实模型名时代的回归现场（2026-09-09 实抓）：分类器
    #     用主循环槽位的真实名（glm-4-plus）点名，它同时绑 heavy+mid、按
    #     Roles 顺序反解成 heavy——tier 闸门版本会整个跳过。仍要切 light。
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

# ---- 配置：ds + glm 两个 provider，指向假上游 ----
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

echo; echo "== 1. 启动假上游（严格 DeepSeek 口径） =="
python3 "$CORE/mock/fake_upstream.py" --port "$UP_PORT" >"$SANDBOX/upstream.log" 2>&1 &
UP_PID=$!
for _ in $(seq 30); do
  curl -sf "http://127.0.0.1:$UP_PORT/__mock/requests" >/dev/null && break; sleep 0.1
done
curl -sf "http://127.0.0.1:$UP_PORT/__mock/requests" >/dev/null \
  && ok "假上游在 127.0.0.1:$UP_PORT" || { bad "假上游没起来"; exit 1; }

# PATH 里 fakebin 在最前，newgate claude 会 exec 我们的假 claude。
export PATH="$FAKEBIN:$PATH"

echo; echo "== 2. newgate claude --profile=ds =="
curl -sf "http://127.0.0.1:$UP_PORT/__mock/reset" -X POST >/dev/null
OUT="$("$BIN" claude --profile=ds 2>"$SANDBOX/ds.err")"
echo "$OUT" | sed 's/^/    /'
check "opus 档注入的是真实模型名" \
  "$(echo "$OUT" | grep '^OPUS_MODEL=' | cut -d= -f2-)" "deepseek-chat"
echo "$OUT" | grep '^BASE_URL=' | grep -q "/a/claude/p/ds" \
  && ok "base URL 带上 /a/claude/p/ds" \
  || bad "base URL 应含 /a/claude/p/ds（实际 $(echo "$OUT" | grep '^BASE_URL=')）"
GOT=$(curl -s "http://127.0.0.1:$UP_PORT/__mock/requests" | python3 -c 'import json,sys;r=json.load(sys.stdin);print(r[0]["body"]["model"] if r else "NONE")')
check "上游收到 ds 的 deepseek-chat" "$GOT" "deepseek-chat"

echo; echo "== 3. newgate claude --profile=glm（切换） =="
curl -sf "http://127.0.0.1:$UP_PORT/__mock/reset" -X POST >/dev/null
OUT="$("$BIN" claude --profile=glm 2>"$SANDBOX/glm.err")"
echo "$OUT" | sed 's/^/    /'
check "切到 glm 后 opus 档是 glm-4-plus" \
  "$(echo "$OUT" | grep '^OPUS_MODEL=' | cut -d= -f2-)" "glm-4-plus"
GOT=$(curl -s "http://127.0.0.1:$UP_PORT/__mock/requests" | python3 -c 'import json,sys;r=json.load(sys.stdin);print(r[0]["body"]["model"] if r else "NONE")')
check "上游收到 glm 的 glm-4-plus" "$GOT" "glm-4-plus"

echo; echo "== 4. 不带 profile 用默认（ds）：动态模式，槽位 = 档位名 =="
# 默认（动态）模式槽位保持档位名——会话发档位名回来，代理按请求时的当前
# 配置解析，--set-profile 对跑着的会话立刻生效。窗口声明照注入（档位名
# 对 Claude Code 也是未知模型，MAX_CONTEXT_TOKENS 一样生效）。
curl -sf "http://127.0.0.1:$UP_PORT/__mock/reset" -X POST >/dev/null
OUT="$("$BIN" claude 2>"$SANDBOX/default.err")"
check "默认（动态）：opus 槽 = 主力档 normal" \
  "$(echo "$OUT" | grep '^OPUS_MODEL=' | cut -d= -f2-)" "normal"
check "默认（动态）：窗口声明照注入（ds 没配 → 空）" \
  "$(echo "$OUT" | grep '^WIN_MAX=' | cut -d= -f2-)" ""

echo; echo "== 5. 不存在的 profile 要立刻报错（退出码 65） =="
"$BIN" claude --profile=nope >/dev/null 2>"$SANDBOX/nope.err"
RC=$?
check "退出码 65" "$RC" "65"
grep -q 'No such profile' "$SANDBOX/nope.err" && ok "报错信息说明了 profile 不存在" || bad "报错信息没说 profile 不存在"

echo; echo "== 6. 上游严格性自检：尾部形状（不是推理字段）才决定 400 =="
# 先证明假上游真的在执行**实测口径**——否则后面那些 200 不能说明任何问题。
#
# 判据是 2026-09-18 打真实 smt-deepseek/deepseek-flash 逐格实测出来的，而且
# 是拿**真实 Claude Code 抓包**（dump/err-400-req000412.client-sent.json，
# 764KB、229 条消息、UA claude-cli/2.1.273）原样复现的：
#
#   原样（尾 = 裸 [tool_result]）                     → 400 reasoning_content…
#   同一个请求体，只去掉最后 assistant 的 thinking 块  → 400（推理字段无关）
#   同一个请求体，只在尾部 content[] 里加 text " "     → 200
#   两个都做                                          → 200
#
# 报错文案说「推理没回传」，真实原因却是「这一轮没有新指令」——文案与原因
# 不一致，正是这条检查必须按**形状**写、不能按文案写的原因。
#
# 三格：裸 tool_result 要 400；同一个 + 一个空格 text、以及 + 一个普通文字块
# 都要 200。上下两格一起才说明判据画在哪条线上（只测 400 会让「见谁都拦」
# 的假上游也过）。
tail_case() {  # $1=尾部 content[] 的字面量  $2=期望状态码  $3=说明
  CODE=$(curl -s -o "$SANDBOX/strict.out" -w "%{http_code}" -X POST \
    "http://127.0.0.1:$UP_PORT/v1/messages" -H 'Content-Type: application/json' \
    -d '{"model":"deepseek-chat","max_tokens":16,"thinking":{"type":"enabled","budget_tokens":1024},"tools":[{"name":"Read","input_schema":{"type":"object"}}],"messages":[{"role":"user","content":[{"type":"text","text":"跑一下"}]},{"role":"assistant","content":[{"type":"tool_use","id":"toolu_x","name":"Read","input":{}}]},{"role":"user","content":'"$1"'}]}')
  check "尾部 $3 → $2" "$CODE" "$2"
}
tail_case '[{"type":"tool_result","tool_use_id":"toolu_x","content":"ok"}]' \
  400 "只有 tool_result（裸）"
tail_case '[{"type":"tool_result","tool_use_id":"toolu_x","content":"ok"},{"type":"text","text":" "}]' \
  200 "tool_result + 一个空格"
tail_case '[{"type":"tool_result","tool_use_id":"toolu_x","content":"ok"},{"type":"text","text":"继续"}]' \
  200 "tool_result + 普通指令"
# 报错原文必须是上游那句（后面几章靠它认形状），逐字锁住。
CODE=$(curl -s -o "$SANDBOX/strict.out" -w "%{http_code}" -X POST \
  "http://127.0.0.1:$UP_PORT/v1/messages" -H 'Content-Type: application/json' \
  -d '{"model":"deepseek-chat","messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"t","content":"ok"}]}]}')
command grep -q "must be passed back" "$SANDBOX/strict.out" \
  && ok "400 的文案是上游那句（推理字段）——文案与真实原因不一致，别按文案判" \
  || bad "400 文案不对：$(head -c 160 "$SANDBOX/strict.out")"

echo; echo "== 10. count_tokens：上游听得懂就转发拿真值 =="
# Claude Code 周期性调 count_tokens 算上下文水位（OpenAI 方言上游没有这个
# 端点）。假上游实现了它（原生 anthropic 形态）→ 代理必须转发：model 按
# mid 档链头补上（count_tokens 请求不带 model），客户端拿到上游真值 42，
# 而不是本地字节数/4 粗估。本地粗估兜底（上游 404 → 学习 → 不再白跑）
# 由单测盖着（count_tokens_test.go）。
curl -sf "http://127.0.0.1:$UP_PORT/__mock/reset" -X POST >/dev/null
OUT="$(E2E_SCENARIO=count_tokens "$BIN" claude --profile=ds 2>"$SANDBOX/ct.err")"
echo "$OUT" | sed 's/^/    /'
check "count_tokens 200" "$(echo "$OUT" | grep '^HTTP=' | cut -d= -f2)" "200"
check "拿到上游真值 42（不是本地粗估）" "$(echo "$OUT" | grep '^INPUT_TOKENS=' | cut -d= -f2)" "42"
GOT=$(curl -s "http://127.0.0.1:$UP_PORT/__mock/requests" | python3 -c '
import json,sys
r=json.load(sys.stdin)
ct=[x for x in r if x["path"].endswith("/count_tokens")]
if not ct:
    print("NOT_FORWARDED")
else:
    print(str(ct[0]["body"].get("model","NONE"))+"@"+ct[0]["path"])')
check "转发到了上游、model 按 mid 链头补上" "$GOT" "deepseek-chat@/v1/messages/count_tokens"

echo; echo "== 11. 控制端点：跨用户停机（/__newgate/stop + 令牌） =="
# 多用户部署：claude 用户读得到共享配置，却对 root 起的 daemon 没有
# kill() 权限——停机只能靠这个端点。错令牌必须 403；对令牌 200 且
# daemon 自己退干净（pid/lock 都清掉）。
PIDFILE="$NEWGATE_HOME/.newgate.pid"
if [ -f "$PIDFILE" ]; then
  PORT=$(python3 -c 'import json;print(json.load(open("'"$NEWGATE_HOME"'/state.json"))["port"])')
  TOK=$(python3 -c 'import json;print(json.load(open("'"$NEWGATE_HOME"'/state.json")).get("control_token",""))')
  if [ -n "$TOK" ]; then
    ok "state.json 里生成了控制令牌"
  else
    bad "控制令牌没生成（跨用户停机会退化成「请让 root 来停」）"
  fi
  CODE=$(curl -s -o /dev/null -w '%{http_code}' -X POST \
    "http://127.0.0.1:$PORT/__newgate/stop" -H 'Authorization: Bearer wrong-token')
  check "错令牌 → 403" "$CODE" "403"
  CODE=$(curl -s -o /dev/null -w '%{http_code}' -X POST \
    "http://127.0.0.1:$PORT/__newgate/stop" -H "Authorization: Bearer $TOK")
  check "对令牌 → 200" "$CODE" "200"
  # 等的是「pid 和 lock 都清了」——断言的就是这两件事，等待条件必须一致。
  # 只等 pidfile 会在 RemovePid() 与 RemoveLock() 之间被抢占：pid 没了、lock
  # 还剩几微秒，轮询恰好落进这个窗口就误报「没退干净」（2026-09-17 偶发）。
  for _ in $(seq 40); do
    [ ! -f "$PIDFILE" ] && [ ! -f "$NEWGATE_HOME/.newgate.lock" ] && break
    sleep 0.1
  done
  if [ ! -f "$PIDFILE" ] && [ ! -f "$NEWGATE_HOME/.newgate.lock" ]; then
    ok "daemon 收到指令后退出，pid/lock 都清了"
  else
    bad "daemon 没退干净（pid 或 lock 还在）"
  fi
  curl -sf "http://127.0.0.1:$PORT/__newgate/status" >/dev/null 2>&1 \
    && bad "端口还在听？" || ok "端口已释放"
else
  bad "沙箱 daemon 的 pid 文件不在（前面哪一步没起 daemon？）"
fi

echo; echo "== 12. 优雅交接：restart 不掐在途流（nginx 式零停机升级） =="
# 上一章把 daemon 停了，先经懒启动路径拉回来。
OUT="$(E2E_SCENARIO=plain "$BIN" claude --profile=ds 2>/dev/null)"
check "懒启动拉回 daemon" "$(echo "$OUT" | grep '^HTTP=' | cut -d= -f2)" "200"
PIDFILE="$NEWGATE_HOME/.newgate.pid"
read_pid() { python3 -c 'import json,sys;print(json.load(open(sys.argv[1]))["pid"])' "$PIDFILE" 2>/dev/null; }
OLD_PID=$(read_pid)
if [ -z "$OLD_PID" ]; then
  bad "pid 文件读不出，没法验证换血"
else
  # 一条 ~2.5s 的慢 SSE 流，中途 restart：流必须完整流完
  curl -sN -o "$SANDBOX/sse.out" -w '%{http_code}' -X POST \
    "http://127.0.0.1:$PROXY_PORT/a/claude/p/ds/v1/messages" \
    -H 'Content-Type: application/json' -H 'x-api-key: newgate-local' \
    -H 'anthropic-version: 2023-06-01' \
    -d '{"model":"deepseek-chat","max_tokens":64,"stream":true,"mock_slow":true,"messages":[{"role":"user","content":"go"}]}' \
    > "$SANDBOX/sse.code" &
  CURL_PID=$!
  sleep 0.8  # 流已经跑起来，正在途中
  OUT="$("$BIN" restart 2>&1)"
  echo "$OUT" | sed 's/^/    /'
  echo "$OUT" | command grep -q 'gracefully restarted' \
    && ok "restart 走了优雅交接（socket 移交）" \
    || bad "restart 没走交接: $(echo "$OUT" | head -2)"
  wait "$CURL_PID"
  check "SSE 流 200（restart 就发生在流中途）" "$(cat "$SANDBOX/sse.code")" "200"
  command grep -q 'message_stop' "$SANDBOX/sse.out" \
    && ok "流完整流完（收到 message_stop）" || bad "在途流被 restart 掐断了"
  RCVD=$(cat "$SANDBOX/sse.out" | command grep -c 'content_block_delta')
  [ "$RCVD" -ge 4 ] && ok "增量块都到了（$RCVD）" || bad "增量块丢了（只有 $RCVD）"
  NEW_PID=$(read_pid)
  if [ -n "$NEW_PID" ] && [ "$NEW_PID" != "$OLD_PID" ]; then
    ok "pid 已换血（$OLD_PID → $NEW_PID）"
  else
    bad "pid 没换血（旧 $OLD_PID，新 '$NEW_PID'）"
  fi
  # 交接后的新请求要落在（也可能没落在，但必须成功）新进程上
  OUT="$(E2E_SCENARIO=plain "$BIN" claude --profile=ds 2>/dev/null)"
  check "交接后新请求 200" "$(echo "$OUT" | grep '^HTTP=' | cut -d= -f2)" "200"
fi

echo; echo "== 13. 后台请求：分类器切轻档，其余只禁思考 =="
# 实抓（2026-09，glm-5.3）：Bash 分类器 = 非流式、model=mid、system 开头
# "You are a security monitor…"。代理必须把分类器（bg_plain/bg_adaptive）
# 切到 light；没写 thinking 的 bg_plain 注入 disabled，显式 adaptive 必须
# 保留。其他后台调用（bg_other，compact 总结）保留 mid + disabled。
for SC in bg_plain bg_adaptive bg_other bg_realname; do
  curl -sf "http://127.0.0.1:$UP_PORT/__mock/reset" -X POST >/dev/null
  OUT="$(E2E_SCENARIO=$SC "$BIN" claude --profile=glm 2>"$SANDBOX/$SC.err")"
  echo "$OUT" | sed 's/^/    /'
  check "$SC 请求 200" "$(echo "$OUT" | grep '^HTTP=' | cut -d= -f2)" "200"
  WANT="glm-4-plus|disabled"          # bg_other：无标记 → 留在 mid
  [ "$SC" != "bg_other" ] && WANT="glm-4.5-air|disabled"  # 分类器 → light
  [ "$SC" = "bg_adaptive" ] && WANT="glm-4.5-air|adaptive" # 显式 thinking 不覆盖
  GOT=$(curl -s "http://127.0.0.1:$UP_PORT/__mock/requests" | python3 -c '
import json,sys
r=json.load(sys.stdin)
b=r[0]["body"] if r else {}
th=(b.get("thinking") or {}).get("type","MISSING")
print(str(b.get("model","NONE"))+"|"+th)')
  check "$SC 上游收到 $WANT" "$GOT" "$WANT"
done

echo; echo "== 13b. 分类器改道后，fallback 沿 light 链走（不回 mid） =="
# 改道是路由决策（建链之前）：分类器整条链都是 light。武装一发 500 打掉
# light 头（glm-4.5-air），下一站必须是下一个 profile 的 light
# （ds/deepseek-chat），绝不能掉回 mid 的 glm-4-plus——只换链头、尾巴
# 还是 mid 的旧实现就是这个错。
curl -sf "http://127.0.0.1:$UP_PORT/__mock/reset" -X POST >/dev/null
curl -sf "http://127.0.0.1:$UP_PORT/__mock/fail?code=500" >/dev/null
OUT="$(E2E_SCENARIO=bg_plain "$BIN" claude --profile=glm 2>"$SANDBOX/bgfo.err")"
echo "$OUT" | sed 's/^/    /'
check "分类器 light 头挂了仍 200（沿链换人）" "$(echo "$OUT" | grep '^HTTP=' | cut -d= -f2)" "200"
GOT=$(curl -s "http://127.0.0.1:$UP_PORT/__mock/requests" | python3 -c '
import json,sys
r=json.load(sys.stdin)
models=[x["body"].get("model") for x in r if x["path"].endswith("/messages")]
print("|".join(models))')
check "沿 light 链：glm-4.5-air → deepseek-chat（不回 mid）" "$GOT" "glm-4.5-air|deepseek-chat"

echo; echo "== 15. newgate metrics：路径上的操作全记账 =="
# 计数器在 daemon 内存里（/__newgate/metrics），CLI 经 HTTP 读；第 12 节
# 的 restart 已经清过一次零，所以这里自己先制造几笔再验。count_tokens
# 直接 curl 代理（假上游有这个端点 → 转发拿真值）。
CT_CODE=$(curl -s -o "$SANDBOX/ct2.out" -w '%{http_code}' -X POST \
  "http://127.0.0.1:$PROXY_PORT/a/claude/p/glm/v1/messages/count_tokens" \
  -H 'Content-Type: application/json' -H 'x-api-key: newgate-local' \
  -d '{"messages":[{"role":"user","content":"count me"}]}')
check "metrics 前置：count_tokens 200" "$CT_CODE" "200"
MOUT="$("$BIN" metrics 2>"$SANDBOX/metrics.err")"
echo "$MOUT" | sed 's/^/    /'
for KEY in "special.claude-bg.route_light" "count_tokens.forwarded" "count_tokens.total" "chain.step_failed" "chain.failover"; do
  echo "$MOUT" | command grep -q "$KEY" \
    && ok "metrics 有 $KEY" \
    || bad "metrics 缺 $KEY（输出：$(echo "$MOUT" | head -3)）"
done

echo; echo "== 14. 窗口声明：声明了才注入，跟被选中的 profile 走 =="
# Claude Code 不认识我们注入的真实模型名，按「未知模型」默认 200k 窗口
# 提前 compact（2026-09 实测 glm-5.3 会话 125k 就在 compact）。glm 声明
# 了 1M/500k → 两个 env 都注入；ds 没声明 → 一个都不注入。
OUT="$(E2E_SCENARIO=plain "$BIN" claude --profile=glm 2>/dev/null)"
check "glm: MAX_CONTEXT_TOKENS=1000000" \
  "$(echo "$OUT" | grep '^WIN_MAX=' | cut -d= -f2-)" "1000000"
check "glm: AUTO_COMPACT_WINDOW=500000" \
  "$(echo "$OUT" | grep '^WIN_COMPACT=' | cut -d= -f2-)" "500000"
OUT="$(E2E_SCENARIO=plain "$BIN" claude --profile=ds 2>/dev/null)"
check "ds（没声明）: MAX_CONTEXT_TOKENS 为空" \
  "$(echo "$OUT" | grep '^WIN_MAX=' | cut -d= -f2-)" ""
check "ds（没声明）: AUTO_COMPACT_WINDOW 为空" \
  "$(echo "$OUT" | grep '^WIN_COMPACT=' | cut -d= -f2-)" ""

echo; echo "== 16. naked：Bash 分类器被短路，上游一个请求都收不到 =="
# 裸奔是「自家安全门」：开着时 Bash 分类器请求被直接批准，不经过上游。判据
# 跟上章节同一个（bg_plain = "You are a security monitor…" 的后台小调用）。
#   关着：请求真的打到上游（glm 的 light 档）；
#   开着：请求被短路，上游 /__mock/requests 数 0，短路口计数器涨。
UPCOUNT() { curl -s "http://127.0.0.1:$UP_PORT/__mock/requests" | python3 -c 'import json,sys;print(len(json.load(sys.stdin)))'; }
RESETUP() { curl -sf "http://127.0.0.1:$UP_PORT/__mock/reset" -X POST >/dev/null; }

RESETUP
OUT="$(E2E_SCENARIO=bg_plain "$BIN" claude --profile=glm 2>"$SANDBOX/nk_off.err")"
echo "$OUT" | sed 's/^/    /'
check "裸奔关：bg_plain 200" "$(echo "$OUT" | grep '^HTTP=' | cut -d= -f2)" "200"
check "裸奔关：上游收到 1 个分类器请求" "$(UPCOUNT)" "1"
GOT=$(curl -s "http://127.0.0.1:$UP_PORT/__mock/requests" | python3 -c '
import json,sys
r=json.load(sys.stdin)
print(r[0]["body"].get("model","NONE") if r else "NONE")')
check "裸奔关：分类器照常切到 light（glm-4.5-air）" "$GOT" "glm-4.5-air"

"$BIN" naked on >/dev/null 2>&1
RESETUP
OUT="$(E2E_SCENARIO=bg_plain "$BIN" claude --profile=glm 2>"$SANDBOX/nk_on.err")"
echo "$OUT" | sed 's/^/    /'
check "裸奔开：bg_plain 仍 200（被短路批准）" "$(echo "$OUT" | grep '^HTTP=' | cut -d= -f2)" "200"
check "裸奔开：上游收到 0 个请求（真的被短路了）" "$(UPCOUNT)" "0"
MOUT="$("$BIN" metrics 2>/dev/null)"
# 断言**机器事实**：计数器名（标识符，表格保证不折断——见内核 lib/style 的列宽
# 下限）与说明的开头那几个字。
#
# **不逐字匹配整句人话**：说明是散文，按列宽折行、而且是**按字符**断的
# （第一行以 `naked: the classi` 硬断）。逐字去 match 一整句，断言的就变成了
# 「终端多宽」。换行压平也救不了——续行带着列缩进，词与词之间是多空格。
# 2026-09-20 之前这里是逐行 grep：中文说明短、恰好没被折开，英文一长就折了，
# 落空的样子还像「计数没了」。
FLAT="$(echo "$MOUT" | tr '\n' ' ' | tr -s ' ')"
case "$FLAT" in
  *"classifier-naked.shortcircuit"*"naked:"*)
    ok "metrics 有 special.classifier-naked.shortcircuit（短路已计数）" ;;
  *) bad "metrics 缺 naked 短路计数（输出：$(echo "$MOUT" | command grep -a classifier-naked)）" ;;
esac
"$BIN" naked off >/dev/null 2>&1
check "裸奔终于关掉（不留沙箱脏状态）" "$("$BIN" naked 2>&1 | command grep -c 'naked is off')" "1"

echo; echo "== 19. 运行期开关：模块自己上报、命令自己注册 =="
# 这一层是「everything is module」的用户界面：模块在 Start 里把自己的开关点
# 上报给 plugin-manager（RegisterSelf），命令由**各模块自己**注册进 cli
# （gateway 的 st/schema-repair、claudecode 的 naked、plugin-manager 的 plugin）。
#
# 这里断言的是列表的地基：**枚举源必须是组件图**，不是「谁上报过」——否则
# 「这个模块没有开关」和「这个模块忘了注册」长得一模一样，用户看到的是同一个
# 空列表。「关掉一个点真的改变热路径」那一半由第 20 章的 schema-repair 锁。
#
# 2026-09-20 起这一章只看**内核自带**的模块：上游怪癖模块（deepseek 那套开关点
# 曾经是这一层最好的例子）跟着发行版走了，它的开关点由发行版自己的 e2e 锁。
PLUGIN_OUT="$("$BIN" plugin 2>&1)"
case "$PLUGIN_OUT" in
  *"breaker"*"plugin-manager"*"gateway"*)
    ok "plugin：列出全部模块（含没参与开关体系的）" ;;
  *) bad "plugin 没列全模块：$(echo "$PLUGIN_OUT" | head -6 | tr '\n' ' ')" ;;
esac
case "$PLUGIN_OUT" in
  *"infra"*"gateway"*"model"*) ok "plugin：按分类分组展示" ;;
  *) bad "plugin 没有按分类分组" ;;
esac
case "$PLUGIN_OUT" in
  *"cannot be toggled at runtime (v1)"*)
    ok "plugin：没上报开关点的模块显式标注（不是静默省略）" ;;
  *) bad "plugin 没标注「无法 runtime 开关」" ;;
esac

# 开关点认不出来时必须是**报错**，不是静默当成 on/off。
"$BIN" plugin 根本没有这个模块 off >/dev/null 2>&1
check "plugin：不存在的目标以非零退出" "$([ $? -ne 0 ] && echo yes || echo no)" "yes"

echo; echo "== 20. special_treatment（整层 / 单插件）与 schema-repair =="
# 这些开关不住在 plugin-manager 的账本里，而是 domain.State 上的 typed 字段
# （special_treatment / special_treatment_off / schema_repair），所以单开一章。
# 断言的是同一件事：**关掉之后热路径真的不做了**——只写进 state.json 而没人读，
# 等于一个好看的开关。
#
# 探针用 schema-repair（内核 gateway 自己的一个 special 插件）：它补的是
# "required": []，语义无操作，但**上游收到的字节里有没有那个键是看得见的**，
# 正好当「这一层还在不在跑」的探针。2026-09-20 之前这里的探针是发行版那支
# 尾部形状补丁（deepseek 第 4 手）——它更好用，但它跟着拥有者搬去发行版了。
SCHEMA_BODY='{"model":"normal","max_tokens":16,"messages":[{"role":"user","content":"hi"}],'\
'"tools":[{"type":"function","function":{"name":"t","parameters":{"type":"object","properties":{}}}}]}'
has_required() {
  curl -s "http://127.0.0.1:$UP_PORT/__mock/requests" | python3 -c '
import json,sys
try:
    r=json.load(sys.stdin)
    prm=r[0]["body"]["tools"][0]["function"]["parameters"]
    print("有" if "required" in prm else "无")
except Exception:
    print("NOUP")' 2>/dev/null
}
post_schema() {
  RESETUP
  curl -s -o /dev/null -X POST "http://127.0.0.1:$PROXY_PORT/p/ds/v1/chat/completions" \
    -H 'Content-Type: application/json' -d "$SCHEMA_BODY"
}

# (1) 单插件开关：这一格就是「只写进 state.json 而没人读」的反面。
"$BIN" schema-repair on >/dev/null 2>&1
post_schema
check "schema-repair on ⇒ 上游收到补好的 required" "$(has_required)" "有"

"$BIN" schema-repair off >/dev/null 2>&1
post_schema
check "schema-repair off ⇒ 字节原样，不补 required" "$(has_required)" "无"

# (2) 整层关：special_treatment off ⇒ 这一层里所有**插件**都不跑。
#
# 探针得选一个真住在这层里的插件：内核自己的 claude-bg（claudecode 模块）给
# 非流式后台调用补 thinking:disabled，上游收到没有这个字段就是「它没跑」。
# 注意 schema-repair **不在这层里**（它是 gatewaystate 上的独立字段），所以它
# 当不了整层开关的探针——2026-09-20 实测：st off 之后 required 照样被补。
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
check "st on（整层）⇒ special 插件在跑（后台调用被补 thinking:disabled）" "$GOT_BG" "disabled"

"$BIN" st off >/dev/null 2>&1
GOT_BG="$(bg_thinking)"
check "st off（整层）⇒ 这一层不跑了（后台调用不再被补 thinking）" "$GOT_BG" "MISSING"

# (3) 整层开回来：同一发必须又被补上（开关是对称的）。
"$BIN" st on >/dev/null 2>&1
GOT_BG="$(bg_thinking)"
check "st on（整层）⇒ 插件又跑起来了（开关对称）" "$GOT_BG" "disabled"

# 收尾：不留非出厂态（沙箱虽然会删，但脏状态会让调试时看到的现状骗人）。
"$BIN" schema-repair on >/dev/null 2>&1
"$BIN" st on >/dev/null 2>&1
check "收尾：开关都回到出厂态" \
  "$("$BIN" status 2>&1 | command grep -c 'special off:\|schema-repair=off')" "0"


echo
echo "结果: $PASS 通过, $FAIL 失败"
[ "$FAIL" -eq 0 ]
