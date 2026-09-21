#!/usr/bin/env bash
# 零 token 端到端：**`newgate ask` 这条消费侧的路**——一句话进，正文出。
#
# 它锁的是三件会**静默**错掉的事：
#
#   1  **正文与思维链分得开**。`answer=$(newgate ask …)` 拿到的必须是干净的正文；
#      reasoning 模型的一发回答里思维链常常比正文长好几倍（见 docs/06-reasoning.md），
#      混进去的话脚本拿到的东西没法用。假上游对**流式**请求先吐 thinking 块再吐
#      正文，所以这一条在这里能验死；
#   2  **真正发出去的问题就是那句话**。`cliapi.Positional` 不跳过选项的**值**，
#      于是 `ask --tier normal 今天天气` 很容易变成把 "normal 今天天气" 发出去——
#      它不报错、不红，只答得莫名其妙。用假上游记下的请求体断言；
#   3  **`--profile` 是单次覆盖**，不动全局默认。
#
# 假上游按路径复用内核的 `core/mock/fake_upstream.py`（不复制：它是逐字节复刻真实
# 上游行为的产物，复制必然漂移，而漂移出来的是「绿的假测试」）。
#
# 全部在临时沙箱里跑，不碰真实 ~/.config。

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
SANDBOX="${NEWGATE_E2E_SANDBOX:-$(mktemp -d /tmp/newgate-ask-e2e.XXXXXX)}"
# 端口与另外几条 e2e 错开：几条可以同时跑。
UP_PORT="${NEWGATE_E2E_UP_PORT:-18093}"
PORT="${NEWGATE_E2E_ASK_PORT:-18094}"
BIN="$SANDBOX/bin/newgate"

PASS=0; FAIL=0
ok()   { echo "  ✓ $1"; PASS=$((PASS+1)); }
bad()  { echo "  ✗ $1"; FAIL=$((FAIL+1)); }
check(){ if [ "$2" = "$3" ]; then ok "$1"; else bad "$1 (期望 '$3'，实际 '$2')"; fi; }

export NEWGATE_HOME="$SANDBOX/ng"
export NEWGATE_LANG=en
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
  curl -sf "http://127.0.0.1:$UP_PORT/__mock/requests" >/dev/null && break
  sleep 0.1
done
curl -sf "http://127.0.0.1:$UP_PORT/__mock/requests" >/dev/null \
  && ok "假上游在 127.0.0.1:$UP_PORT" || { bad "假上游没起来"; exit 1; }

# ---- 配置：一条链打假上游；另有一条 profile 用来验单次覆盖 ----
cat > "$NEWGATE_HOME/providers.json" <<EOF
{"providers":{"demo":{"protocol":"anthropic","base_url":"http://127.0.0.1:$UP_PORT/v1","api_key":"sk-demo"}}}
EOF
cat > "$NEWGATE_HOME/mappings/demo.json" <<'JSON'
{"description":"ask e2e","roles":{"light":[{"provider":"demo","model":"mock-model"}],
                                  "normal":[{"provider":"demo","model":"mock-model"}]}}
JSON
cat > "$NEWGATE_HOME/mappings/alt.json" <<'JSON'
{"description":"ask e2e, another chain","roles":{"normal":[{"provider":"demo","model":"other-model"}]}}
JSON
printf '{"default_profile":"demo","port":%s}' "$PORT" > "$NEWGATE_HOME/state.json"

"$BIN" __serve --port "$PORT" >"$SANDBOX/serve.log" 2>&1 &
for _ in $(seq 1 50); do
  curl -sf -o /dev/null "http://127.0.0.1:$PORT/__newgate/status" && break
  sleep 0.1
done

RESETUP() { curl -sf "http://127.0.0.1:$UP_PORT/__mock/reset" -X POST >/dev/null; }
# 上游收到的第一条请求里的 user 内容——用来验「真正发出去的是不是那句话」。
SENT() { curl -s "http://127.0.0.1:$UP_PORT/__mock/requests" | python3 -c '
import json,sys
r = json.load(sys.stdin)
if not r: print(""); raise SystemExit
m = r[0]["body"].get("messages") or []
c = m[0].get("content") if m else ""
print(c if isinstance(c, str) else json.dumps(c, ensure_ascii=False))'; }

echo
echo "== 1. 正文进 stdout、思维链进 stderr =="
RESETUP
out=$("$BIN" ask --tier normal "只回一句话" 2>"$SANDBOX/think.txt")
check "stdout 就是正文（假上游流式回的那句）" "$out" "MOCK-STREAM mock-model"
err=$(cat "$SANDBOX/think.txt")
case "$err" in
  *MOCK-THINKING-ORIGINAL*) ok "思维链在 stderr（人看得见，脚本捕获不到）";;
  *) bad "思维链没走 stderr，实际: $(echo "$err" | head -c 120)";;
esac
case "$out" in
  *MOCK-THINKING*) bad "思维链混进了 stdout——\$(newgate ask …) 拿到的就不干净了";;
  *) ok "stdout 里一个字节的思维链都没有";;
esac

echo
echo "== 2. 真正发出去的问题就是那句话（选项的值不算问题） =="
RESETUP
"$BIN" ask --tier normal "今天天气如何" >/dev/null 2>&1
check "上游收到的 user 内容" "$(SENT)" "今天天气如何"

echo
echo "== 3. --profile 是单次覆盖，不动全局默认 =="
RESETUP
out=$("$BIN" ask --profile alt --tier normal "换个链" 2>/dev/null)
check "这一发走了 alt 那条链" "$out" "MOCK-STREAM other-model"
check "全局默认没被动过" "$(python3 -c 'import json;print(json.load(open("'"$NEWGATE_HOME"'/state.json"))["default_profile"])')" "demo"

echo
echo "== 4. 管道喂进来 =="
RESETUP
out=$(printf '从管道来的问题' | "$BIN" ask --tier light 2>/dev/null)
check "管道那一发也答了" "$out" "MOCK-STREAM mock-model"
check "上游收到的是管道里那句话" "$(SENT)" "从管道来的问题"

echo
echo "== 5. 没给问题：说清楚，不是发一个空请求 =="
out=$("$BIN" ask </dev/null 2>&1); rc=$?
case "$rc" in
  0) bad "没给问题却以 0 退出";;
  *) ok "非零退出（$rc）";;
esac
case "$out" in
  *"nothing to ask"*) ok "说清了「没有要问的东西」";;
  *) bad "没说清，实际: $out";;
esac

echo
echo "== 6. 代理没在跑：说清楚要跑什么，而不是自己把它拉起来 =="
"$BIN" stop >/dev/null 2>&1
sleep 0.3
out=$("$BIN" ask "hi" 2>&1); rc=$?
case "$rc" in
  0) bad "代理没在跑却以 0 退出";;
  *) ok "非零退出（$rc）";;
esac
case "$out" in
  *"proxy is not running"*) ok "点名了「代理没在跑」";;
  *) bad "没说清，实际: $out";;
esac
if "$BIN" status >/dev/null 2>&1 && curl -sf -o /dev/null "http://127.0.0.1:$PORT/__newgate/status"; then
  bad "ask 把代理拉起来了——一次提问不该变成一次状态改变"
else
  ok "ask 没有偷偷把代理拉起来"
fi

echo
echo "结果: $PASS 通过, $FAIL 失败"
[ "$FAIL" = "0" ]
