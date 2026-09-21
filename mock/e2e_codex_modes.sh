#!/usr/bin/env bash
# 零 token 端到端：**codex 的两种接管模式**，以及「codex 自己的模型名认回档位」那条链。
#
# 背景（2026-09-21 实测）：codex 的模型目录**编在它二进制里**（`codex debug models`
# 打出 9 个），它不问服务端要——把 provider 指向一个记账的假上游，`/v1/models`
# 一次都没被请求过（`remote_models` / `models_endpoint` / `env_key` 三条全试了）。
# 所以「让它自己的选择器里出现档位」这条路不存在，只能反过来：**让它照旧用自己的
# 模型名，名字进来我们认**。
#
# 它锁的是四件会**静默**错掉的事：
#
#   1  **模式真的改变了写进 config.toml 的值**。takeover 写档位名（codex 只认一个
#      模型，切不动），rename 写 codex 自己的模型名。写错了不会报错——接管照常
#      「成功」，只有用户在 codex 里换模型时才发现没用；
#   2  **那些名字真的能被解析**。角色没登记上的症状是 404，而 404 的那句话说的是
#      「既不是档位、也没绑在任何 profile 里」——它不会告诉你「是那张表没生效」。
#      所以这里断言的是**上游收到的模型名**：`gpt-5.6-luna` 必须落到 normal 那一档
#      绑的模型上（假上游记下请求体，能验死）；
#   3  **动态**：加一行新模型**不用重启**就生效（表是配置的一部分，改它和改
#      providers.json 应当一样自动重载）。这条是这张表存在的理由——上游出新的
#      模型时，用户加一行就该能用上；
#   4  **不让路的人不受影响**：切回 takeover 之后那些名字**不再**被认成角色。
#      这是刻意的取舍——登记成角色会短路「具体模型名反解回它所属档位」那条路，
#      而那条路上走着别的客户端的请求（本机 smt-codex 的链上就挂着 gpt-5.6-terra）。
#
# 假上游按路径复用内核的 `core/mock/fake_upstream.py`（不复制：它是逐字节复刻真实
# 上游行为的产物，复制必然漂移，而漂移出来的是「绿的假测试」）。
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
SANDBOX="${NEWGATE_E2E_SANDBOX:-$(mktemp -d /tmp/newgate-codexmodes.XXXXXX)}"
# 端口与另外几条 e2e 错开：几条可以同时跑。
UP_PORT="${NEWGATE_E2E_UP_PORT:-18095}"
PORT="${NEWGATE_E2E_CODEX_PORT:-18096}"
BIN="$SANDBOX/bin/newgate"

PASS=0; FAIL=0
ok()   { echo "  ✓ $1"; PASS=$((PASS+1)); }
bad()  { echo "  ✗ $1"; FAIL=$((FAIL+1)); }
check(){ if [ "$2" = "$3" ]; then ok "$1"; else bad "$1 (期望 '$3'，实际 '$2')"; fi; }
has()  { if command grep -q "$2" "$3" 2>/dev/null; then ok "$1"; else bad "$1（$3 里找不到 $2）"; fi; }

export NEWGATE_HOME="$SANDBOX/ng"
export CODEX_HOME="$SANDBOX/codex"
export NEWGATE_LANG=en
unset LC_ALL LC_MESSAGES LANG LANGUAGE 2>/dev/null || true
mkdir -p "$NEWGATE_HOME/mappings" "$CODEX_HOME" "$(dirname "$BIN")"

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

# ---- 配置：三档各绑一个能一眼分清的模型名 ----
cat > "$NEWGATE_HOME/providers.json" <<EOF
{"providers":{"demo":{"protocol":"anthropic","base_url":"http://127.0.0.1:$UP_PORT/v1","api_key":"sk-demo"}}}
EOF
cat > "$NEWGATE_HOME/mappings/demo.json" <<'JSON'
{"description":"codex modes e2e",
 "roles":{"heavy":[{"provider":"demo","model":"mock-heavy"}],
          "normal":[{"provider":"demo","model":"mock-normal"}],
          "light":[{"provider":"demo","model":"mock-light"}]}}
JSON
printf '{"default_profile":"demo","port":%s}' "$PORT" > "$NEWGATE_HOME/state.json"

# 一份**还没被接管过**的 codex 配置：带注释、带它自己长出来的段。
# 已经带着 `[model_providers.newgate]` 的样本会让接管拒绝执行（那是对的：
# 没有原始备份就接管等于把用户的原始配置弄丢），而那是测试自己的坑。
cat > "$CODEX_HOME/config.toml" <<'TOML'
# 我自己写的注释
model = "gpt-5.6-terra"
personality = "pragmatic"

[projects."/root"]
trust_level = "trusted"
TOML

# 上游收到的最后一条请求里的 model——用来看**代理实际发出去的是谁**。
LASTUP() {
  curl -s "http://127.0.0.1:$UP_PORT/__mock/requests" | python3 -c '
import json,sys
d=json.load(sys.stdin)
print(d[-1]["body"].get("model","none") if d else "none")'
}
ASK() { # ASK <模型名> → HTTP 状态码
  curl -s -o /dev/null -w '%{http_code}' -X POST "http://127.0.0.1:$PORT/v1/messages" \
    -H 'Content-Type: application/json' \
    -d "{\"model\":\"$1\",\"max_tokens\":8,\"messages\":[{\"role\":\"user\",\"content\":\"hi\"}]}"
}

echo
echo "== 1. 缺省是 takeover：config.toml 里写档位名 =="
"$BIN" on codex >"$SANDBOX/on1.log" 2>&1
has "model 写的是档位名" '^model = "normal"' "$CODEX_HOME/config.toml"
has "用户自己的注释还在（接管是字节手术，不是 TOML 往返）" '我自己写的注释' "$CODEX_HOME/config.toml"

echo
echo "== 2. 切到 rename：写 codex 自己的模型名 =="
printf '{"mode":"rename"}' > "$NEWGATE_HOME/codex-models.json"
"$BIN" on codex >"$SANDBOX/on2.log" 2>&1
has "normal 档写成了 codex 的模型名（gpt-5.6-sol）" '^model = "gpt-5.6-sol"' "$CODEX_HOME/config.toml"

echo
echo "== 3. rename 模式下，codex 发自己的模型名能被认回档位 =="
"$BIN" __serve --port "$PORT" >"$SANDBOX/serve.log" 2>&1 &
for _ in $(seq 1 50); do
  curl -sf -o /dev/null "http://127.0.0.1:$PORT/__newgate/status" && break
  sleep 0.1
done
curl -s -X POST "http://127.0.0.1:$UP_PORT/__mock/reset" >/dev/null
check "gpt-5.6-luna 认得出（HTTP 200）" "$(ASK gpt-5.6-luna)" "200"
check "它落到 normal 那一档绑的模型上" "$(LASTUP)" "mock-normal"

echo
echo "== 4. 动态：加一行新模型，不用重启 =="
printf '{"mode":"rename","models":[{"slug":"gpt-7-nova","tier":"heavy"},{"slug":"gpt-5.6-luna","tier":"light"}]}' \
  > "$NEWGATE_HOME/codex-models.json"
sleep 1.5   # 等一次配置重载（角色表是配置的一部分，与 providers.json 同一条路）
curl -s -X POST "http://127.0.0.1:$UP_PORT/__mock/reset" >/dev/null
check "新加的 gpt-7-nova 立刻认得出" "$(ASK gpt-7-nova)" "200"
check "它落到 heavy" "$(LASTUP)" "mock-heavy"
check "把 luna 那一行改成 light：立刻改道" "$(ASK gpt-5.6-luna)" "200"
check "确实走了 light" "$(LASTUP)" "mock-light"

echo
echo "== 5. 切回 takeover：那些名字不再被认成角色（别人不受影响） =="
printf '{"mode":"takeover"}' > "$NEWGATE_HOME/codex-models.json"
sleep 1.5
check "gpt-7-nova 不再被认成角色（404）" "$(ASK gpt-7-nova)" "404"
check "档位名照旧好用（404 只是那些 codex 名字的事）" "$(ASK normal)" "200"

echo
echo "结果: $PASS 通过, $FAIL 失败"
[ "$FAIL" = "0" ]
