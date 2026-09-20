#!/usr/bin/env bash
# 零 token 端到端：**浏览器那条写路径**（快照 → 保存 → 冲突 → 两道门）。
#
# 它锁的是这个产品里唯一一处「两个写者改同一份文件」的安全语义：命令行与浏览器
# 都能改配置。浏览器的每一次保存都带着**加载时那份基线**，所以命令行在你看着
# 页面时改过的文件必须变成一次**冲突**（两边原文都摆出来让人选），而不是被
# 覆盖——2026-09-20 定这条时用户的原话是「所有改动都走 snapshot 加检测的方式，
# 这样最安全」。
#
# 三件事各验一段：
#   1  保存这条路的**形状**：基线对了就写得进去，写完之后文件真的变了；
#   2  **冲突**：基线过期 → 409，两边原文都在，而且磁盘**一个字节都没被覆盖**；
#   3  **两道门**：写操作必须声明 application/json（挡跨站表单），只答本机 Host
#      （挡 DNS rebinding）。
#
# 不起假上游：这里验的是控制面，请求一个都不发出去。所以它比别的 e2e 快，
# 也不需要 core/ 的假上游（仍然需要一个**装出来的**二进制）。
#
# 全部在临时沙箱里跑，不碰真实 ~/.config。

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
SANDBOX="${NEWGATE_E2E_SANDBOX:-$(mktemp -d /tmp/newgate-dash-e2e.XXXXXX)}"
# 端口与另外两条 e2e 错开：三条可以同时跑（对照着调试时很有用）。
PORT="${NEWGATE_E2E_WEB_PORT:-18898}"
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

# ---- 沙箱配置：一个 provider + 一个 profile ----
cat > "$NEWGATE_HOME/providers.json" <<'JSON'
{"providers":{"demo":{"protocol":"anthropic","base_url":"http://127.0.0.1:9/demo","api_key":"sk-secret-must-not-leak"}}}
JSON
cat > "$NEWGATE_HOME/mappings/demo.json" <<'JSON'
{"description":"e2e profile","roles":{"normal":[{"provider":"demo","model":"before"}]}}
JSON
STATE="$(python3 -c 'import json;print(json.dumps({"default_profile":"demo","port":int("'"$PORT"'")}))')"
printf '%s' "$STATE" > "$NEWGATE_HOME/state.json"

# ---- 起服务 ----
# 无参数 + 只有自己的 flag 也算认领（见 gateway 的 serveEntry），但这里显式用
# `__serve`：脚本要验的是控制面，别把它和「入口怎么认领」那条测试混在一起。
"$BIN" __serve --port "$PORT" >"$SANDBOX/serve.log" 2>&1 &
SRV_PID=$!
for _ in $(seq 1 50); do
  curl -sf -o /dev/null "http://127.0.0.1:$PORT/ui/api/snapshot" && break
  sleep 0.1
done
if ! curl -sf -o /dev/null "http://127.0.0.1:$PORT/ui/api/snapshot"; then
  echo "服务没起来，日志："; tail -20 "$SANDBOX/serve.log"; exit 1
fi

API="http://127.0.0.1:$PORT/ui/api"
PROFILE_FILE="$NEWGATE_HOME/mappings/demo.json"

# ---- 1. 快照：可写的那张卡片带着基线 ----
echo
echo "== 1. 快照 =="
SNAP="$SANDBOX/snapshot.json"
curl -sf "$API/snapshot" > "$SNAP"
read -r BASE1 HAS_PROFILE <<EOF
$(python3 - "$SNAP" <<'PY'
import json,sys
d=json.load(open(sys.argv[1]))
c=[x for x in d["concepts"] if x["id"]=="config.profile.demo"]
print((c[0]["data"]["base"] if c else ""), "yes" if c else "no")
PY
)
EOF
check "快照里有可写的 profile 卡片" "$HAS_PROFILE" "yes"
[ -n "$BASE1" ] && ok "卡片带着基线（$BASE1）" || bad "卡片没带基线——没有基线就没有冲突检测"

# ---- 2. 写得进去，而且文件真的变了 ----
echo
echo "== 2. 保存（基线正确）=="
CODE=$(curl -s -o "$SANDBOX/apply1.json" -w '%{http_code}' -X POST "$API/apply" \
  -H 'Content-Type: application/json' \
  -d "{\"id\":\"config.profile.demo\",\"base\":\"$BASE1\",\"edit\":{\"roles\":{\"normal\":[{\"provider\":\"demo\",\"model\":\"after\"}]}}}")
check "保存返回 200" "$CODE" "200"
BASE2=$(python3 -c 'import json;print(json.load(open("'"$SANDBOX/apply1.json"'")).get("base",""))')
[ -n "$BASE2" ] && [ "$BASE2" != "$BASE1" ] && ok "返回了新基线（前端续着改不用刷新）" \
  || bad "没返回新基线（或与旧的一样）"
if grep -q '"after"' "$PROFILE_FILE"; then ok "磁盘上的文件真的改了"; else bad "文件没改"; fi

# ---- 3. 基线过期 = 冲突，而且**一个字节都不覆盖** ----
echo
echo "== 3. 冲突（拿旧基线再写一次）=="
CODE=$(curl -s -o "$SANDBOX/apply2.json" -w '%{http_code}' -X POST "$API/apply" \
  -H 'Content-Type: application/json' \
  -d "{\"id\":\"config.profile.demo\",\"base\":\"$BASE1\",\"edit\":{\"roles\":{\"normal\":[{\"provider\":\"demo\",\"model\":\"clobbered\"}]}}}")
check "过期基线返回 409" "$CODE" "409"
read -r C_PATH C_YOURS C_THEIRS <<EOF
$(python3 - "$SANDBOX/apply2.json" <<'PY'
import json,sys
c=json.load(open(sys.argv[1])).get("conflict") or {}
print(c.get("path",""), "y" if c.get("yours") else "", "t" if c.get("theirs") else "")
PY
)
EOF
[ -n "$C_PATH" ] && ok "冲突点名了文件（$C_PATH）" || bad "冲突没说是哪份文件"
[ -n "$C_YOURS" ] && [ -n "$C_THEIRS" ] && ok "冲突把两边原文都带上了" \
  || bad "冲突缺原文——用户不知道自己要覆盖掉什么"
if grep -q '"clobbered"' "$PROFILE_FILE"; then
  bad "被拒的那次写**覆盖了磁盘**——这是最坏的结果"
else
  ok "磁盘没被覆盖（被拒的写没有落盘）"
fi

# ---- 4. 只读的概念不能写 ----
echo
echo "== 4. 只读（带凭据的文件）=="
CODE=$(curl -s -o /dev/null -w '%{http_code}' -X POST "$API/apply" \
  -H 'Content-Type: application/json' \
  -d '{"id":"config.file.providers.json","base":"","edit":{}}')
check "providers.json（带凭据）拒绝写入" "$CODE" "409"

# ---- 5. 两道门 ----
echo
echo "== 5. 两道门 =="
CODE=$(curl -s -o /dev/null -w '%{http_code}' -X POST "$API/apply" \
  -H 'Content-Type: text/plain' -d '{"id":"config.profile.demo","base":"","edit":{}}')
check "写操作没声明 application/json → 415" "$CODE" "415"
CODE=$(curl -s -o /dev/null -w '%{http_code}' -X POST "$API/apply" \
  -H 'Host: evil.com' -H 'Content-Type: application/json' \
  -d '{"id":"config.profile.demo","base":"","edit":{}}')
check "Host 不是本机 → 403（挡 DNS rebinding）" "$CODE" "403"
CODE=$(curl -s -o /dev/null -w '%{http_code}' -H 'Host: evil.com' "$API/snapshot")
check "读也认这道门 → 403" "$CODE" "403"

# ---- 6. 凭据不出门 ----
echo
echo "== 6. 凭据脱敏 =="
if grep -q 'sk-secret-must-not-leak' "$SNAP"; then
  bad "快照里出现了明文 api_key"
else
  ok "快照里没有明文凭据"
fi

echo
echo "结果: $PASS 通过, $FAIL 失败"
[ "$FAIL" -eq 0 ] || exit 1
