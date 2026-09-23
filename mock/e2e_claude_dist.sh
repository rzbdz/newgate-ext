#!/usr/bin/env bash
# 零 token 端到端：**本发行版那几个模块**的行为——DeepSeek 的推理原文回填、
# 尾部形状修补、跨上游迁移的补丁契约。
#
# 这些章节原来住在内核仓库的 mock/e2e_claude.sh 里，2026-09-20 跟着模块搬过来。
# 理由是**测试跟着拥有者走**：它们断言的是发行版模块的策略，住在内核那边会让
# 内核的测试依赖一个发行版模块——摘掉那个模块，内核就红，而内核本该能自己测干净。
# 内核自己的 e2e 留在 core/mock/e2e_claude.sh，跑的是接管、转发、控制端点、
# 优雅交接那些**机制**。
#
# 验证（章号沿用内核那版，方便两边对账）：
#   7/8/9  思考模式：Claude Code 会把 thinking 块剥掉，代理补回 thinkcache 里那轮
#          的真实原文（tool id 找回）；**查不到就一个字节都不补**（2026-09-18 起：
#          上游自己没给过推理，我们凭什么替它编）。
#   17     形状 400：上游 400 原样透传 + 熔断器只计数、永不摘牌。
#   18     补丁侧的三个契约：不编、措辞最小、原因分类。
#
# 假上游**按路径复用**内核的 core/mock/fake_upstream.py，不复制一份：它是逐字节
# 复刻真实上游行为的产物，复制必然漂移，而漂移出来的是「绿的假测试」。所以跑之前
# core/ 这个 submodule 必须在（`git submodule update --init --recursive`）。
#
# 全部在临时沙箱里跑，不碰真实 ~/.config / ~/.claude / shell rc。
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CORE="${NEWGATE_E2E_CORE:-$ROOT/core}"
# 二进制来自本仓库的构建入口（build/build.sh），文件名里带发行版名与平台。
DIST_NAME="${NEWGATE_E2E_DIST:-default}"
# 产物名里的架构用 **Go 的叫法**（amd64），不是 uname 的叫法（x86_64）——build.sh
# 就是按 Go 的叫法命名的，两边必须一致，否则这里会找不到二进制。映射与 build.sh
# 里那段是同一份判据（那一段的注释写了为什么需要它）。
case "$(uname -m)" in
  x86_64)        host_arch=amd64 ;;
  aarch64|arm64) host_arch=arm64 ;;
  i386|i686)     host_arch=386 ;;
  *)             host_arch=$(uname -m) ;;
esac
BIN_SRC="${NEWGATE_E2E_BIN:-$ROOT/dist/newgate-$DIST_NAME-$(uname -s | tr 'A-Z' 'a-z')-$host_arch}"
# 端口默认值与内核那版错开：两边可以同时跑（对照着调试时很有用）。
SANDBOX="${NEWGATE_E2E_SANDBOX:-$(mktemp -d /tmp/newgate-dist-e2e.XXXXXX)}"
UP_PORT="${NEWGATE_E2E_UP_PORT:-18091}"
PROXY_PORT="${NEWGATE_E2E_PROXY_PORT:-18899}"
FAKEBIN="$SANDBOX/fakebin"
# 二进制是**多调用型**的：argv0 决定这次调用归谁（newgate / claude / opencode …）。
# 发布产物叫 newgate-<发行版>-<平台>-<架构>，直接拿它当 newgate 用会被当成
# 「profile 名」——2026-09-20 实测的报错是
# `同时给了 profile "newgate-default-linux-x86_64" 和 "ds"，不一致`。
# 所以先按正确的名字落一份到沙箱里，再拿它跑。
BIN="$SANDBOX/bin/newgate"

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
[ -x "$BIN_SRC" ] || { echo "找不到二进制 $BIN_SRC——先跑 build/build.sh"; exit 1; }
mkdir -p "$(dirname "$BIN")"
cp "$BIN_SRC" "$BIN"
[ -f "$CORE/mock/fake_upstream.py" ] || {
  echo "找不到内核的假上游 $CORE/mock/fake_upstream.py"
  echo "core/ 是个 submodule，先：git submodule update --init --recursive"
  exit 1
}

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


echo; echo "== 0. 启动假上游（严格 DeepSeek 口径） =="
# 假上游来自**内核**仓库（core/ 这个 submodule）：它是逐字节复刻真实上游行为的
# 产物——判据是尾部形状而不是推理字段，本发行版的补丁正是针对那套口径写的。
# 所以这里**按路径复用**、不复制一份：复制必然漂移，而漂移出来的是「绿的假测试」。
python3 "$CORE/mock/fake_upstream.py" --port "$UP_PORT" >"$SANDBOX/upstream.log" 2>&1 &
UP_PID=$!
for _ in $(seq 30); do
  curl -sf "http://127.0.0.1:$UP_PORT/__mock/requests" >/dev/null && break; sleep 0.1
done
curl -sf "http://127.0.0.1:$UP_PORT/__mock/requests" >/dev/null \
  && ok "假上游在 127.0.0.1:$UP_PORT" || { bad "假上游没起来"; exit 1; }

# PATH 里 fakebin 在最前，newgate claude 会 exec 我们的假 claude（它在头部生成，
# 模拟 Claude Code 的请求形态）。
export PATH="$FAKEBIN:$PATH"

# 清空假上游记下的请求。内核那版把它定义在中段的章节里，而本脚本只搬了末尾几章，
# 所以定义跟着搬过来——它是**这一层的基础设施**（每个用例都从「上游没收到过东西」
# 开始），不是哪一章的私有物。
RESETUP() { curl -sf "http://127.0.0.1:$UP_PORT/__mock/reset" -X POST >/dev/null; }
echo; echo "== 7. 思考模式第一轮（客户端剥块场景的起点） =="
curl -sf "http://127.0.0.1:$UP_PORT/__mock/reset" -X POST >/dev/null
OUT="$(E2E_SCENARIO=think1 "$BIN" claude --profile=ds 2>"$SANDBOX/t1.err")"
echo "$OUT" | sed 's/^/    /'
check "think1 首轮 200" "$(echo "$OUT" | grep '^HTTP=' | cut -d= -f2)" "200"

echo; echo "== 8. 第二轮：thinkcache 命中，回填那轮真实推理原文 =="
curl -sf "http://127.0.0.1:$UP_PORT/__mock/reset" -X POST >/dev/null
OUT="$(E2E_SCENARIO=think2 "$BIN" claude --profile=ds 2>"$SANDBOX/t2.err")"
echo "$OUT" | sed 's/^/    /'
check "think2 严格上游放行（回填生效）" "$(echo "$OUT" | grep '^HTTP=' | cut -d= -f2)" "200"
GOT=$(curl -s "http://127.0.0.1:$UP_PORT/__mock/requests" | python3 -c '
import json,sys
r=json.load(sys.stdin)
m=r[0]["body"]["messages"][1] if r else {}
print(m.get("reasoning_content","MISSING"))')
check "reasoning_content 是缓存里的原文" "$GOT" "MOCK-THINKING-ORIGINAL"
GOT=$(curl -s "http://127.0.0.1:$UP_PORT/__mock/requests" | python3 -c '
import json,sys
r=json.load(sys.stdin)
m=r[0]["body"]["messages"][1] if r else {}
c=m.get("content")
print(c[0].get("thinking","") if isinstance(c,list) and c else "NO_BLOCK")')
check "thinking 块也是缓存里的原文" "$GOT" "MOCK-THINKING-ORIGINAL"

echo; echo "== 9. 缓存未命中（模拟重启后的旧会话）：一个字节都不补 =="
# 这一章锁的是 2026-09-18 定下来的那条**最基本**的逻辑：上游自己那一轮就没
# 给过推理，那「must be passed back」要求回传的东西根本不存在，我们凭什么
# 替它编一个。原来这里补的是 "No thinking in this round"，是错的：
# 编出来的字会进上游、进对话历史、每轮烧 token，而信息量是零。
#
# 正确动作是**跳过这条消息**，并且把「为什么没有原文」分好类报进日志
# （notool / nocache / nokey）——跳过是结果，光报个数没法反查。
#
# 但**尾部形状**那一手（第 4 手）在这一发上照样要动：think3 的历史正好是
# 「裸 tool_result 收尾」，上游对那个形状报的正是这条 400。两件事互不冲突，
# 所以这里同时断言：
#   assistant 消息：rc 缺席、没有 thinking 块（不编）
#   尾部 user 轮：多了一条 `continue`（修形状）
curl -sf "http://127.0.0.1:$UP_PORT/__mock/reset" -X POST >/dev/null
OUT="$(E2E_SCENARIO=think3 "$BIN" claude --profile=ds 2>"$SANDBOX/t3.err")"
echo "$OUT" | sed 's/^/    /'
check "think3 严格上游放行（尾部形状被修好）" "$(echo "$OUT" | grep '^HTTP=' | cut -d= -f2)" "200"
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
check "不编占位符（rc 缺席、无 thinking 块），但尾部形状被修" \
  "$GOT" "rc=ABSENT blocks=tool_use tail=tool_result,text"
GOT=$(curl -s "http://127.0.0.1:$UP_PORT/__mock/requests" | python3 -c '
import json,sys
r=json.load(sys.stdin)
lastu=[m for m in r[0]["body"]["messages"] if m.get("role")=="user"][-1]["content"]
print([b.get("text") for b in lastu if isinstance(b,dict) and b.get("type")=="text"][0])')
check "追加的就是最简那句「continue」" "$GOT" "continue"
# 跳过的原因必须分类报出来（这条 tool_use id 从没进过 thinkcache ⇒ nocache）。
LOGTAIL=$(tail -40 "$NEWGATE_HOME/newgate.log")
case "$LOGTAIL" in
  *"left untouched"*"nocache"*) ok "日志把「没有原文可补」的原因分成 nocache 并说明跳过" ;;
  *) bad "日志没说清跳过原因（该有 \"left untouched\" + \"nocache\"）：$(echo "$LOGTAIL" | tail -2)" ;;
esac

echo; echo "== 17. 形状 400：上游 400 原样透传 + 熔断器只计数、永不摘牌 =="
# 这份 body 要满足两个条件，缺一条这个用例就不是它要测的东西：
#
#   1. **尾部形状违规、而且插件修不了**。实测判据是「最后一条 role:user 的
#      content[] 里全是 tool_result 块」（详见第 6 章与 mock/fake_upstream.py），
#      而 deepseek 插件对其中**修得好**的那一族（那个 user 轮就是数组末尾）
#      会追加一句 `continue` 把它修掉、返回 200——那是第 9 章在锁的事。
#      所以这里用的是**修不好**的那一族：裸 tool_result 之后**还有一条
#      assistant**。插件故意不碰它（往用户的对话里塞一句模型看不见效果的
#      噪音比 400 更糟），实测这一族在原样追加 `continue` 之后 3/3 还是 400。
#      于是链上**每一个**候选都 400，链走到头，客户端才拿得到上游原文。
#   2. **走 glm profile**，让 glm 那一发先吃 400：deepseek 插件的 MatchTarget
#      看 model/provider/baseURL，glm/glm-4-plus 三处都没有「deepseek」字样，
#      于是插件不去碰它。（这一点是冗余保险：就算 Match 判错，条件 1 也兜住了。）
#
# 形状判据（modules/deepseek/shape.go）认领它 → classify 判 BucketShape →
# 账本只涨 ShapeSkips、永不进 Open。所以「客户端拿到 400」和「glm 没被摘牌」
# 必须**同时**成立：这正是这轮重构要的那个行为。
RESETUP
SHAPE_BODY='{"model":"glm-4-plus","max_tokens":16,"stream":true,'\
'"thinking":{"type":"enabled","budget_tokens":1024},'\
'"tools":[{"name":"Bash","description":"d","input_schema":{"type":"object","properties":{}}}],'\
'"messages":['\
'{"role":"user","content":[{"type":"text","text":"跑一下"}]},'\
'{"role":"assistant","content":[{"type":"tool_use","id":"toolu_shape_17","name":"Bash","input":{}}]},'\
'{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_shape_17","content":"ok"}]},'\
'{"role":"assistant","content":[{"type":"text","text":"好"}]}]}'

# (1) 直打假上游：证明这条规则真的部署到位（不是被代理偷偷改过）。
CODE=$(curl -s -o "$SANDBOX/shape_direct.out" -w '%{http_code}' -X POST \
  "http://127.0.0.1:$UP_PORT/v1/messages" \
  -H 'Content-Type: application/json' -d "$SHAPE_BODY")
check "直打上游：400 must be passed back" \
  "$( [ "$CODE" = "400" ] && command grep -q 'must be passed back' "$SANDBOX/shape_direct.out" && echo y || echo n)" "y"

# (2) 同 body 经代理：链上每一站都 400 → 客户端必须收到上游原文（不静默）。
RESETUP
RES_CODE=$(curl -s -o "$SANDBOX/shape_proxy.out" -w '%{http_code}' -X POST \
  "http://127.0.0.1:$PROXY_PORT/a/claude/p/glm/v1/messages" \
  -H 'Content-Type: application/json' \
  -H 'anthropic-version: 2023-06-01' -H 'x-api-key: e2e' -d "$SHAPE_BODY")
check "经代理：客户端仍 400" "$RES_CODE" "400"
command grep -q 'must be passed back' "$SANDBOX/shape_proxy.out" \
  && ok "经代理：上游原文原样透传（不静默）" \
  || bad "经代理：被吞了；body=$(head -c 200 "$SANDBOX/shape_proxy.out")"

# 日志里那行 [shape-400] 是判据认领的**唯一**证据（转发路径不认识任何上游
# 专有字符串，它只读 Result.Shape 那个名字）。
LOG="$NEWGATE_HOME/newgate.log"
for _ in $(seq 20); do
  command grep -q '\[shape-400\]' "$LOG" 2>/dev/null && break; sleep 0.1
done
command grep -aq '\[shape-400\].*detector deepseek' "$LOG" \
  && ok "日志有 [shape-400] 判据 deepseek（认领留痕）" \
  || bad "日志里没有 [shape-400] 判据 deepseek"

# (3) 熔断器只计数、绝不摘牌。`newgate breaker` 把问题 binding 分两段，这条
#     shape-400 只能出现在「只计数、没摘牌」那一段。
BRK_OUT="$("$BIN" breaker 2>/dev/null)"
echo "$BRK_OUT" | sed 's/^/    /'
echo "$BRK_OUT" | command grep -q 'counted only, not tripped' \
  && ok "breaker 表里有「只计数、没摘牌」段（shape-400 的归属）" \
  || bad "breaker 表里找不到 counted-only 段"
echo "$BRK_OUT" | command grep -q 'glm/glm-4-plus' \
  && ok "breaker 表里能找到 glm/glm-4-plus（被记账了）" \
  || bad "breaker 表里找不到 glm/glm-4-plus"
echo "$BRK_OUT" | command grep -qE '· 0 tripped' \
  && ok "没有任何 binding 被摘牌（形状 400 只计数）" \
  || bad "有 binding 被摘牌了（形状 400 不该摘牌）：$(echo "$BRK_OUT" | command grep 'tripped' | head)"

# (4) metrics 端的形状计数要涨。
"$BIN" metrics 2>/dev/null | command grep -q 'breaker.skipped.shape_error' \
  && ok "metrics 有 breaker.skipped.shape_error" \
  || bad "metrics 缺 breaker.skipped.shape_error"

echo; echo "== 18. 补丁侧的三个契约：不编、措辞最小、原因分类 =="
# 这一章锁 2026-09-18 定下来的三件事，全在 deepseek 插件的改写路径上。
# 这里直打**代理**、读假上游收到的 body：这些是纯字节手术，假上游收到的
# 就是上游会收到的字节。
#
# (a) **绝不编**。上游那一轮没给过推理，那「must be passed back」要求回传的
#     东西根本不存在，我们凭什么替它编一个——编出来的字会进上游、进对话
#     历史、每轮烧 token，信息量是零。原来补的是 "No thinking in this round"，
#     现在是**跳过这条消息，一个字节都不加**。这条请求里那条 assistant 的
#     tool_use id 从没进过 thinkcache ⇒ 没有原文 ⇒ 必须原样不动。
#
# (b) **尾部修复的措辞最小**。第 4 手往尾部追加的那句话会进上游、也会进用户
#     下一轮的对话历史，越长越像「有人在替我说话」。用户的原话是「只用最少字，
#     比如（"继续"）这种」。所以断言的是**逐字**等于 `continue`，不是「非空且短」
#     ——后者换个长句子照样能过。
#
# (c) **原因必须分类报出来**。跳过是**结果**，光报个数没法反查：是「上游本来就
#     没给」还是「给了但我们没存住」，处置完全相反（前者只能接受，后者要去
#     查 thinkcache 的命中率）。分三类：
#       notool   纯文本轮，靠正文哈希找回，miss
#       nocache  有 tool_use，但它的 id 在 thinkcache 里查不到
#       nokey    既无 tool_use 也无正文，认不出这条消息
#     这条请求的 tool_use id 从没被缓存过，所以必然是 nocache。
#
# 请求里显式写 reasoning_content「这一轮本来就有推理，插件不许碰」：插件只补
# **缺**的字段，已经有了的一个字节都不碰（既有契约）。所以同一条请求同时给出
# 两个样本：第一条 assistant 有原文（不许碰）、尾部那个 user 轮是裸 tool_result
# （必须修）。
PH_BODY='{"model":"deepseek-chat","max_tokens":16,"stream":false,'\
'"thinking":{"type":"enabled"},'\
'"messages":['\
'{"role":"user","content":[{"type":"text","text":"跑一下"}]},'\
'{"role":"assistant","reasoning_content":"这一轮本来就有推理，插件不许碰",'\
'"content":[{"type":"text","text":"好"},{"type":"tool_use","id":"t-never-cached-e2e","name":"Bash","input":{}}]},'\
'{"role":"user","content":[{"type":"tool_result","tool_use_id":"t-never-cached-e2e","content":"ok"}]}]}'

RESETUP
CODE=$(curl -s -o "$SANDBOX/ph_proxy.out" -w '%{http_code}' -X POST \
  "http://127.0.0.1:$PROXY_PORT/a/claude/p/ds/v1/messages" \
  -H 'Content-Type: application/json' \
  -H 'anthropic-version: 2023-06-01' -H "x-api-key: e2e" -d "$PH_BODY")
check "经代理：思考开着、尾部裸 tool_result → 200（形状修好了）" "$CODE" "200"

# 逐条把契约打成一行一行的 key=value，再一条条 check——不用 read 拆词
# （read -r A B 是按空白切分，正文里带空格就错位），也不做子串匹配。
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

# (a) 绝不编：这条 assistant 没有原文 ⇒ 不插 thinking 块、不加 reasoning_content。
check "没有原文 ⇒ 不插 thinking 块（不编占位符）" "$(ph_get thinking_blocks)" "0"
check "没有原文 ⇒ 已经写着的 reasoning_content 逐字不动" \
  "$(ph_get reasoning_content)" "这一轮本来就有推理，插件不许碰"
# (b) 尾部形状被修好，而且追加的是**逐字**那句最简指令。
check "尾部从裸 tool_result 变成 tool_result+text" "$(ph_get tail)" "tool_result,text"
check "追加的指令逐字是「continue」（最少字）" "$(ph_get tail_text)" "continue"

# (c) 原因分类。日志里那句必须同时说清「跳过了」和「为什么」（nocache）。
LOGTAIL=$(tail -60 "$NEWGATE_HOME/newgate.log")
case "$LOGTAIL" in
  *"left untouched"*"nocache"*) ok "日志说明跳过、且把原因分成 nocache（有 tool_use、缓存查不到）" ;;
  *) bad "日志没说清跳过原因（该有 \"left untouched\" + \"nocache\"）：$(echo "$LOGTAIL" | tail -2)" ;;
esac
# 这里原本还有一条断言：「日志里不许出现『占位符』字样——编占位符那条路应该已经
# 删掉了」。**它是一条空断言**：它查的那句话是那条已被删掉的路留下的，所以它从写
# 下的那天起就没有红过（恒真）。2026-09-20 迁移时先把它换成正面断言（日志要明说
# 「不编占位符」），一跑就发现那句属于 `newgate deepseek` 的审计输出，**这个场景
# 不走**——于是它变成另一条假断言。
#
# 不留样子：真正的保证在两头——这条脚本上面那两条（跳过要说清、原因要分成
# nocache），以及 modules/deepseek 自己的单测。「编占位符」这件事今天没有可断言
# 的界面输出，硬留一条只会让下一个人以为这里有人看着。

echo
echo "结果: $PASS 通过, $FAIL 失败"
[ "$FAIL" -eq 0 ]
