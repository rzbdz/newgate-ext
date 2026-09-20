#!/usr/bin/env bash
# 零 token 端到端：假上游 + newgate + 假 opencode
# 全部在临时沙箱里跑，不碰你真实的 ~/.config
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
SANDBOX="${NEWGATE_E2E_SANDBOX:-$(mktemp -d /tmp/newgate-e2e.XXXXXX)}"
BIN="$SANDBOX/bin/newgate"
mkdir -p "$(dirname "$BIN")"
cp "$BIN_SRC" "$BIN"
# 端口可用环境变量覆盖：默认值互不冲突，好让人肉并行跑；CI 里同一台机器上
# 并行跑两份时才需要显式岔开（不然两边抢同一个假上游端口）。
UP_PORT="${NEWGATE_E2E_UP_PORT:-18080}"
PROXY_PORT="${NEWGATE_E2E_PROXY_PORT:-18899}"

PASS=0; FAIL=0
ok()   { echo "  ✓ $1"; PASS=$((PASS+1)); }
bad()  { echo "  ✗ $1"; FAIL=$((FAIL+1)); }
check(){ if [ "$2" = "$3" ]; then ok "$1"; else bad "$1 (期望 '$3'，实际 '$2')"; fi; }

export NEWGATE_HOME="$SANDBOX/ng"
# 语言要密闭：脚本断言的是**源语言原文**（英文），而跑它的机器可能是 zh-Hans
# （我们自己的机器就是）。不钉住的话，「界面上是英文」这类断言在中文机器上假红。
export NEWGATE_LANG=en
unset LC_ALL LC_MESSAGES LANG LANGUAGE 2>/dev/null || true
export NEWGATE_TARGET_DIR="$SANDBOX/cfg"
mkdir -p "$NEWGATE_HOME" "$NEWGATE_TARGET_DIR"

cleanup() {
  "$BIN" stop >/dev/null 2>&1 || true
  [ -n "${UP_PID:-}" ] && kill "$UP_PID" 2>/dev/null
  rm -rf "$SANDBOX"
}
trap cleanup EXIT

echo "沙箱: $SANDBOX"
[ -x "$BIN" ] || { echo "先 make build"; exit 1; }

# ---- 准备：自带 fixture ----
# 刻意**不**读用户真实配置：测试必须自包含、可复现，不依赖任何本机文件。
cat > "$NEWGATE_TARGET_DIR/opencode.json" <<'EOF'
{ "$schema": "https://opencode.ai/config.json",
  "plugin": ["oh-my-openagent@latest"],
  "provider": {
    "upstream-a": { "npm": "@ai-sdk/openai-compatible",
      "options": { "baseURL": "https://example.invalid/v1", "apiKey": "sk-orig-a" },
      "models": { "model-large": {}, "model-small": {} } },
    "upstream-b": { "npm": "@ai-sdk/anthropic",
      "options": { "baseURL": "https://example.invalid/v1", "apiKey": "sk-orig-b" },
      "models": { "model-flagship": {}, "model-vision": {} } }
  } }
EOF
cat > "$NEWGATE_TARGET_DIR/oh-my-openagent.json" <<'EOF'
{ "agents": {
    "worker": { "model": "upstream-b/model-flagship", "variant": "max",
      "fallback_models": [ { "model": "upstream-a/model-large" } ] },
    "looker": { "model": "upstream-b/model-vision" } },
  "categories": {
    "quick": { "model": "upstream-a/model-small" },
    "deep":  { "model": "upstream-b/model-flagship" } } }
EOF

ORIG_SUM=$(md5sum "$NEWGATE_TARGET_DIR/opencode.json" | cut -d' ' -f1)
ORIG_OA_SUM=$(md5sum "$NEWGATE_TARGET_DIR/oh-my-openagent.json" | cut -d' ' -f1)

echo; echo "== 1. 启动假上游 =="
python3 "$CORE/mock/fake_upstream.py" --port "$UP_PORT" >"$SANDBOX/upstream.log" 2>&1 &
UP_PID=$!
for _ in $(seq 30); do
  curl -sf "http://127.0.0.1:$UP_PORT/__mock/requests" >/dev/null && break; sleep 0.1
done
curl -sf "http://127.0.0.1:$UP_PORT/__mock/requests" >/dev/null \
  && ok "假上游在 127.0.0.1:$UP_PORT" || { bad "假上游没起来"; exit 1; }

echo; echo "== 2. init + 把 provider 指向假上游 =="
"$BIN" init >/dev/null 2>&1
python3 - "$NEWGATE_HOME/providers.json" "$UP_PORT" <<'PY'
import json,sys
p,port=sys.argv[1],sys.argv[2]
d=json.load(open(p))
for name,v in d["providers"].items():
    v["base_url"]=f"http://127.0.0.1:{port}/v1"
    v["api_key"]=f"sk-fake-{name}"
    v.pop("api_key_env",None)
json.dump(d,open(p,"w"),indent=2)
PY
python3 - "$NEWGATE_HOME/state.json" "$PROXY_PORT" <<'PY'
import json,sys
p=sys.argv[1]; d=json.load(open(p)); d["port"]=int(sys.argv[2])
json.dump(d,open(p,"w"),indent=2)
PY
[ -f "$NEWGATE_HOME/mappings/cheap.json" ] && ok "profile 已铺开" || bad "缺 mappings"

echo; echo "== 3. newgate start（接管配置） =="
"$BIN" start >"$SANDBOX/start.log" 2>&1
sed 's/^/    /' "$SANDBOX/start.log" | head -20
[ -f "$NEWGATE_HOME/.newgate.pid" ]  && ok "pid 文件已建"  || bad "缺 pid 文件"
[ -f "$NEWGATE_HOME/.newgate.lock" ] && ok "lock 文件已建" || bad "缺 lock 文件"
curl -sf "http://127.0.0.1:$PROXY_PORT/__newgate/status" >/dev/null \
  && ok "代理在 127.0.0.1:$PROXY_PORT" || bad "代理不响应"

# 接管后配置应含 newgate 且原 provider 仍在
grep -q '"newgate"' "$NEWGATE_TARGET_DIR/opencode.json" && ok "opencode.json 注入了 newgate provider" || bad "没注入"
grep -q 'upstream-a' "$NEWGATE_TARGET_DIR/opencode.json" && ok "原有 provider 被保留" || bad "原 provider 丢了！"
grep -q 'newgate/' "$NEWGATE_TARGET_DIR/oh-my-openagent.json" && ok "oh-my-openagent 模型已改写" || bad "没改写"
grep -q 'fallback_models' "$NEWGATE_TARGET_DIR/oh-my-openagent.json" && ok "fallback 结构保留" || bad "结构被破坏"

echo; echo "== 4. 假 opencode 发请求（顶层 model = newgate/heavy） =="
curl -sf "http://127.0.0.1:$UP_PORT/__mock/reset" -X POST >/dev/null
python3 "$CORE/mock/opencode_mock.py" --config-dir "$NEWGATE_TARGET_DIR" \
  --model newgate/heavy 2>&1 | sed 's/^/    /'
GOT=$(curl -s "http://127.0.0.1:$UP_PORT/__mock/requests" | python3 -c 'import json,sys;r=json.load(sys.stdin);print(r[0]["body"]["model"] if r else "NONE")')
check "链头 cheap 的 heavy 应解析成 model-large" "$GOT" "model-large"

echo; echo "== 5. 切 profile（不重启，立刻生效） =="
"$BIN" --set-profile top-only 2>&1 | sed 's/^/    /'
curl -sf "http://127.0.0.1:$UP_PORT/__mock/reset" -X POST >/dev/null
python3 "$CORE/mock/opencode_mock.py" --config-dir "$NEWGATE_TARGET_DIR" \
  --model newgate/heavy >/dev/null 2>&1
GOT=$(curl -s "http://127.0.0.1:$UP_PORT/__mock/requests" | python3 -c 'import json,sys;r=json.load(sys.stdin);print(r[0]["body"]["model"] if r else "NONE")')
check "切到 top-only 后 heavy 应是 model-flagship（稀疏层生效）" "$GOT" "model-flagship"
KEY=$(curl -s "http://127.0.0.1:$UP_PORT/__mock/requests" | python3 -c 'import json,sys;r=json.load(sys.stdin);print(r[0]["headers"].get("authorization","")) if r else print("")')
check "key 应换成 upstream-b 那把" "$KEY" "Bearer sk-fake-upstream-b"

"$BIN" --set-profile expensive >/dev/null 2>&1
curl -sf "http://127.0.0.1:$UP_PORT/__mock/reset" -X POST >/dev/null
python3 "$CORE/mock/opencode_mock.py" --config-dir "$NEWGATE_TARGET_DIR" \
  --model newgate/heavy >/dev/null 2>&1
GOT=$(curl -s "http://127.0.0.1:$UP_PORT/__mock/requests" | python3 -c 'import json,sys;r=json.load(sys.stdin);print(r[0]["body"]["model"] if r else "NONE")')
check "切到 expensive 后 heavy 应是 model-flagship" "$GOT" "model-flagship"

echo; echo "== 6. 档位隔离：light 应走小模型 =="
"$BIN" --set-profile cheap >/dev/null 2>&1
curl -sf "http://127.0.0.1:$UP_PORT/__mock/reset" -X POST >/dev/null
python3 "$CORE/mock/opencode_mock.py" --config-dir "$NEWGATE_TARGET_DIR" --slot small_model >/dev/null 2>&1
GOT=$(curl -s "http://127.0.0.1:$UP_PORT/__mock/requests" | python3 -c 'import json,sys;r=json.load(sys.stdin);print(r[0]["body"]["model"] if r else "NONE")')
check "small_model(light) 应是 model-small" "$GOT" "model-small"

echo; echo "== 7. 流式逐块（不缓冲） =="
python3 "$CORE/mock/opencode_mock.py" --config-dir "$NEWGATE_TARGET_DIR" --stream 2>&1 | sed 's/^/    /' | tail -6

echo; echo "== 8. 未知档位不猜（fail 而非静默回落） =="
OUT=$(python3 "$CORE/mock/opencode_mock.py" --config-dir "$NEWGATE_TARGET_DIR" --model newgate/nonexistent 2>&1)
echo "$OUT" | grep -q '404' && ok "未知档位返回 404 且不猜" || bad "未知档位没有正确报错"
echo "$OUT" | grep -q 'newgate' && ok "错误信息带 newgate 标识" || bad "错误信息没有 newgate 标识"

echo; echo "== 9. 跑遍所有 agent 槽位 =="
curl -sf "http://127.0.0.1:$UP_PORT/__mock/reset" -X POST >/dev/null
python3 "$CORE/mock/opencode_mock.py" --config-dir "$NEWGATE_TARGET_DIR" --all 2>&1 | tail -3 | sed 's/^/    /'
N=$(curl -s "http://127.0.0.1:$UP_PORT/__mock/requests" | python3 -c 'import json,sys;print(len(json.load(sys.stdin)))')
[ "$N" -ge 2 ] && ok "所有 agent 槽位都能路由（$N 个去重后的请求）" || bad "agent 槽位路由失败（只有 $N 个）"

echo; echo "== 10. newgate stop（逃生舱：逐字节还原） =="
"$BIN" stop 2>&1 | sed 's/^/    /'
NEW_SUM=$(md5sum "$NEWGATE_TARGET_DIR/opencode.json" | cut -d' ' -f1)
NEW_OA_SUM=$(md5sum "$NEWGATE_TARGET_DIR/oh-my-openagent.json" | cut -d' ' -f1)
check "opencode.json 逐字节还原" "$NEW_SUM" "$ORIG_SUM"
check "oh-my-openagent.json 逐字节还原" "$NEW_OA_SUM" "$ORIG_OA_SUM"
[ ! -f "$NEWGATE_HOME/.newgate.pid" ]  && ok "pid 文件已清"  || bad "pid 文件残留"
[ ! -f "$NEWGATE_HOME/.newgate.lock" ] && ok "lock 文件已清" || bad "lock 文件残留"
curl -sf "http://127.0.0.1:$PROXY_PORT/__newgate/status" >/dev/null 2>&1 \
  && bad "代理还在跑" || ok "代理已停"

echo; echo "=============================="
echo " $PASS 通过, $FAIL 失败"
echo "=============================="
[ "$FAIL" -eq 0 ] || exit 1
