#!/usr/bin/env bash
# 零 token 端到端：**模型清单**（前端下拉背后那条路）。
#
# 锁的是三件事，它们只在这一层（真二进制 + 真 HTTP + 真上游）看得见：
#   1  适配器真的按 provider 的 base 拼出了地址、解析出了模型与分组；
#   2  「不支持」是一个**正常答案**（200 + supported:false + 原因），不是 500；
#   3  BFF 的扩展端点仍在两道门后面（Host / Content-Type），并且缓存能命中、
#      refresh 能绕过。
#
# 不起真上游：`mock/fake_modellist.py` 只实现列模型这一条。全部在临时沙箱里跑，
# 不碰真实 ~/.config；端口与别的 e2e 错开，可以并行跑。

set -u

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
DIST_NAME="${NEWGATE_E2E_DIST:-default}"
case "$(uname -m)" in
  x86_64)        host_arch=amd64 ;;
  aarch64|arm64) host_arch=arm64 ;;
  i386|i686)     host_arch=386 ;;
  *)             host_arch=$(uname -m) ;;
esac
BIN_SRC="${NEWGATE_E2E_BIN:-$ROOT/dist/newgate-$DIST_NAME-$(uname -s | tr 'A-Z' 'a-z')-$host_arch}"
SANDBOX="${NEWGATE_E2E_SANDBOX:-$(mktemp -d /tmp/newgate-modellist-e2e.XXXXXX)}"
PORT="${NEWGATE_E2E_MODELLIST_PORT:-18911}"
UP_PORT="${NEWGATE_E2E_MODELLIST_UP_PORT:-18101}"
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
  [ -n "${SRV_PID:-}" ] && kill "$SRV_PID" 2>/dev/null
  [ -n "${UP_PID:-}" ] && kill "$UP_PID" 2>/dev/null
  if [ -n "${NEWGATE_E2E_KEEP:-}" ]; then
    echo "沙箱保留（NEWGATE_E2E_KEEP）: $SANDBOX"
  else
    rm -rf "$SANDBOX"
  fi
}
trap cleanup EXIT

echo "沙箱: $SANDBOX"
[ -x "$BIN_SRC" ] || { echo "找不到二进制 $BIN_SRC——先跑 build/build.sh"; exit 1; }
cp "$BIN_SRC" "$BIN"

# ---- 假上游：/v1/models 有清单；/none 下 404（演「没有这个接口」）----
python3 "$ROOT/mock/fake_modellist.py" --port "$UP_PORT" >"$SANDBOX/upstream.log" 2>&1 &
UP_PID=$!
for _ in $(seq 1 50); do
  curl -sf -o /dev/null "http://127.0.0.1:$UP_PORT/v1/models" && break
  sleep 0.1
done
if ! curl -sf -o /dev/null "http://127.0.0.1:$UP_PORT/v1/models"; then
  echo "假上游没起来，日志："; tail -20 "$SANDBOX/upstream.log"; exit 1
fi

# ---- 沙箱配置 ----
cat > "$NEWGATE_HOME/providers.json" <<JSON
{"providers":{
  "fake":{"protocol":"openai","base_url":"http://127.0.0.1:$UP_PORT","api_key":"sk-modellist"},
  "none":{"protocol":"openai","base_url":"http://127.0.0.1:$UP_PORT/none","api_key":"sk-modellist"}
}}
JSON
cat > "$NEWGATE_HOME/mappings/demo.json" <<'JSON'
{"description":"modellist e2e","roles":{"normal":[{"provider":"fake","model":"alpha-chat"}]}}
JSON
printf '%s' "{\"default_profile\":\"demo\",\"port\":$PORT}" > "$NEWGATE_HOME/state.json"

# ---- 起服务 ----
"$BIN" __serve --port "$PORT" >"$SANDBOX/serve.log" 2>&1 &
SRV_PID=$!
for _ in $(seq 1 50); do
  curl -sf -o /dev/null "http://127.0.0.1:$PORT/ui/api/snapshot" && break
  sleep 0.1
done
if ! curl -sf -o /dev/null "http://127.0.0.1:$PORT/ui/api/snapshot"; then
  echo "服务没起来，日志："; tail -30 "$SANDBOX/serve.log"; exit 1
fi

API="http://127.0.0.1:$PORT/ui/api"
RESP="$SANDBOX/resp.json"

# ---- 1. 上游有清单：模型、分组、适配器 ----
echo
echo "== 1. 有清单 =="
CODE=$(curl -s -o "$RESP" -w '%{http_code}' -X POST "$API/ext/modellist" \
  -H 'Content-Type: application/json' -d '{"provider":"fake"}')
check "扩展端点返回 200" "$CODE" "200"
read -r SUPPORTED N ADAPTER GROUP_LIST <<EOF
$(python3 - "$RESP" <<'PY'
import json,sys
d=json.load(open(sys.argv[1]))
print("yes" if d.get("supported") else "no", len(d.get("models") or []),
      d.get("adapter",""), ",".join(d.get("groups") or []))
PY
)
EOF
check "supported=true" "$SUPPORTED" "yes"
check "列出了 3 个模型" "$N" "3"
check "命中的是 openai 适配器" "$ADAPTER" "openai"
check "分组按第一次出现排序" "$GROUP_LIST" "alpha,beta"

# ---- 2. 第二次命中缓存，refresh 绕过 ----
echo
echo "== 2. 缓存 =="
curl -s -o "$RESP" -X POST "$API/ext/modellist" -H 'Content-Type: application/json' \
  -d '{"provider":"fake"}' >/dev/null
CACHED=$(python3 -c 'import json,sys;print(json.load(open(sys.argv[1])).get("cached",False))' "$RESP")
check "第二次标注 cached" "$CACHED" "True"
curl -s -o "$RESP" -X POST "$API/ext/modellist" -H 'Content-Type: application/json' \
  -d '{"provider":"fake","refresh":true}' >/dev/null
CACHED=$(python3 -c 'import json,sys;print(json.load(open(sys.argv[1])).get("cached",False))' "$RESP")
check "refresh 绕过缓存" "$CACHED" "False"

# ---- 3. 没有列表接口：200 + supported:false + 原因 ----
echo
echo "== 3. 不支持 =="
CODE=$(curl -s -o "$RESP" -w '%{http_code}' -X POST "$API/ext/modellist" \
  -H 'Content-Type: application/json' -d '{"provider":"none"}')
check "不支持也是 200（不是故障）" "$CODE" "200"
read -r SUPPORTED NOTE <<EOF
$(python3 - "$RESP" <<'PY'
import json,sys
d=json.load(open(sys.argv[1]))
print("yes" if d.get("supported") else "no", d.get("note",""))
PY
)
EOF
check "supported=false" "$SUPPORTED" "no"
case "$NOTE" in
  *"no model list"*) ok "原因里说了「没有模型清单接口」" ;;
  *) bad "原因没说清（$NOTE）" ;;
esac

# ---- 4. 名字不存在是错误（调用方问错了）----
echo
echo "== 4. 名字不存在 =="
CODE=$(curl -s -o "$RESP" -w '%{http_code}' -X POST "$API/ext/modellist" \
  -H 'Content-Type: application/json' -d '{"provider":"typo"}')
check "未知 provider 返回 400" "$CODE" "400"
if grep -q 'no provider named' "$RESP"; then ok "报错点名了那个名字"; else bad "报错没说清: $(cat "$RESP")"; fi

# ---- 5. BFF 的两道门 + 方法 ----
echo
echo "== 5. 两道门与动词 =="
CODE=$(curl -s -o /dev/null -w '%{http_code}' -X POST "$API/ext/modellist" \
  -H 'Content-Type: text/plain' -d '{"provider":"fake"}')
check "没声明 application/json → 415" "$CODE" "415"
CODE=$(curl -s -o /dev/null -w '%{http_code}' -X POST "$API/ext/modellist" \
  -H 'Host: evil.com' -H 'Content-Type: application/json' -d '{"provider":"fake"}')
check "Host 不是本机 → 403（挡 DNS rebinding）" "$CODE" "403"
CODE=$(curl -s -o /dev/null -w '%{http_code}' "$API/ext/modellist")
check "GET → 405" "$CODE" "405"
CODE=$(curl -s -o /dev/null -w '%{http_code}' -X POST "$API/ext/modellist" \
  -H 'Content-Type: application/json' -d '{')
check "坏 JSON → 400" "$CODE" "400"
CODE=$(curl -s -o /dev/null -w '%{http_code}' -X POST "$API/ext/nope" \
  -H 'Content-Type: application/json' -d '{}')
check "没登记的扩展 → 404" "$CODE" "404"

echo
echo "== 结果：$PASS 通过，$FAIL 失败 =="
[ "$FAIL" -eq 0 ]
