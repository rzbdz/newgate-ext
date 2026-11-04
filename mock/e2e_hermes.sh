#!/usr/bin/env bash
# 零 token 端到端：**hermes 的接管**——真二进制、真接管、真 config.yaml、内核的假上游。
#
# 它锁的是 hermes 那两种并存的用法，以及几件会**静默**错掉的事：
#
#   1  接管是字节手术：用户自己的东西（这里是一个与模型无关的键、和一个混进 auxiliary
#      的标量——本机真实文件里就有）原样留着；
#   2  「跟着 newgate 走」：缺省模型是档位名，落到哪个模型由 profile 决定；
#   3  「在 hermes 里直接点名」：`newgate-<profile>` 那一行的 URL 钉在那份 profile 上，
#      `provider/model` 这种名字进来，上游收到的是那份 profile 绑的模型；
#      而 /model 问到的清单（代理替 hermes 答的 /models）与文件里写的是同一份；
#   4  hermes 对本机 base_url 的探测（/api/tags …）本地 404，不刷失败数；
#   5  守护进程在跑时新加一份 profile，几秒内 /model 的清单就同步进文件——而且
#      **只动清单**：用户在 hermes 里选过的主模型不被改回去；
#   6  `off` 按键还原：接管写过的几格放回原样，接管期间用户改过的别的东西保留。
#
# 假上游按路径复用内核的 `core/mock/fake_upstream.py`。全部在临时沙箱里跑，
# 不碰真实 ~/.config 与 ~/.hermes。

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
SANDBOX="${NEWGATE_E2E_SANDBOX:-$(mktemp -d /tmp/newgate-hermes.XXXXXX)}"
UP_PORT="${NEWGATE_E2E_UP_PORT:-18097}"
PORT="${NEWGATE_E2E_HERMES_PORT:-18098}"
BIN="$SANDBOX/bin/newgate"

PASS=0; FAIL=0
ok()   { echo "  ✓ $1"; PASS=$((PASS+1)); }
bad()  { echo "  ✗ $1"; FAIL=$((FAIL+1)); }
check(){ if [ "$2" = "$3" ]; then ok "$1"; else bad "$1 (期望 '$3'，实际 '$2')"; fi; }
has()  { if command grep -q -- "$2" "$3" 2>/dev/null; then ok "$1"; else bad "$1（$3 里找不到 $2）"; fi; }
hasnt(){ if command grep -q -- "$2" "$3" 2>/dev/null; then bad "$1（$3 里不该有 $2）"; else ok "$1"; fi; }

export NEWGATE_HOME="$SANDBOX/ng"
export HERMES_HOME="$SANDBOX/hermes"
export NEWGATE_LANG=en
unset LC_ALL LC_MESSAGES LANG LANGUAGE 2>/dev/null || true
mkdir -p "$NEWGATE_HOME/mappings" "$HERMES_HOME" "$(dirname "$BIN")"
CFG="$HERMES_HOME/config.yaml"

cleanup() {
  [ -n "${SERVE_PID:-}" ] && kill "$SERVE_PID" 2>/dev/null
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
[ -f "$CORE/mock/fake_upstream.py" ] || {
  echo "找不到内核的假上游 $CORE/mock/fake_upstream.py（git submodule update --init --recursive）"
  exit 1
}
cp "$BIN_SRC" "$BIN"

python3 "$CORE/mock/fake_upstream.py" --port "$UP_PORT" >"$SANDBOX/upstream.log" 2>&1 &
UP_PID=$!
for _ in $(seq 1 50); do
  curl -sf -o /dev/null "http://127.0.0.1:$UP_PORT/__mock/requests" && break
  sleep 0.1
done

cat > "$NEWGATE_HOME/providers.json" <<EOF
{"providers":{"demo":{"protocol":"openai","base_url":"http://127.0.0.1:$UP_PORT/v1","api_key":"sk-demo"}}}
EOF
cat > "$NEWGATE_HOME/mappings/demo.json" <<'JSON'
{"description":"hermes e2e","context_window":400000,
 "roles":{"heavy":[{"provider":"demo","model":"mock-heavy"}],
          "normal":[{"provider":"demo","model":"mock-normal"}],
          "light":[{"provider":"demo","model":"mock-light"}]}}
JSON
cat > "$NEWGATE_HOME/mappings/alt.json" <<'JSON'
{"description":"pinned row","roles":{"normal":[{"provider":"demo","model":"mock-alt"}]}}
JSON
printf '{"default_profile":"demo","port":%s}' "$PORT" > "$NEWGATE_HOME/state.json"

# hermes 0.13 自己落盘的形状（节选）。`compact: false` 混在 auxiliary 下面——本机真实
# 文件就是这样（多半是一次迁移写错了缩进），接管不能被它挡住，也不能碰它。
cat > "$CFG" <<'YAML'
model:
  provider: custom
  model: claude-opus-5
  default: claude-opus-5
  base_url: https://api.example.com/
  api_key: sk-user-real-key
  api_mode: anthropic_messages
providers: {}
agent:
  max_turns: 90
auxiliary:
  vision:
    provider: smt-vision
    model: gpt-4o
    base_url: ""
    api_key: ""
    timeout: 120
  compact: false
delegation:
  model: claude-sonnet-4.6
  provider: ""
  base_url: https://api.example.com/
  api_key: sk-user-real-key
  max_iterations: 50
YAML
cp "$CFG" "$SANDBOX/original.yaml"

LASTUP() {
  curl -s "http://127.0.0.1:$UP_PORT/__mock/requests" | python3 -c '
import json,sys
d=json.load(sys.stdin)
print(d[-1]["body"].get("model","none") if d else "none")'
}
ASK() { # ASK <path 前缀> <模型名> → HTTP 状态码
  curl -s -o /dev/null -w '%{http_code}' -X POST "http://127.0.0.1:$PORT$1/chat/completions" \
    -H 'Content-Type: application/json' -H 'Authorization: Bearer newgate-local' \
    -d "{\"model\":\"$2\",\"max_tokens\":8,\"messages\":[{\"role\":\"user\",\"content\":\"hi\"}]}"
}
YAMLOK() { # 有 PyYAML 就让它解析一遍（没有就跳过：CI 机器不一定装了）
  if python3 -c 'import yaml' 2>/dev/null; then
    if python3 -c 'import sys,yaml; yaml.safe_load(open(sys.argv[1]))' "$CFG" 2>/dev/null; then
      ok "$1"
    else
      bad "$1（PyYAML 解析失败）"
    fi
  fi
}

echo
echo "== 1. newgate on hermes：字节手术，别人的东西不动 =="
"$BIN" on hermes >"$SANDBOX/on.log" 2>&1
check "接管成功" "$?" "0"
has "主循环指到 newgate" '^  provider: newgate$' "$CFG"
has "缺省模型是档位名" '^  default: normal$' "$CFG"
has "跟着走的那一行在" '^  newgate:$' "$CFG"
has "每份 profile 一行（钉在 /p/alt 上）" 'a/hermes/p/alt/v1' "$CFG"
has "钉住那一行列出具体模型" 'demo/mock-alt:' "$CFG"
has "profile 声明的窗口写进 per-model 那一格" 'context_length: 400000' "$CFG"
has "delegation 走具名 provider" '^  model: mid$' "$CFG"
hasnt "用户的真钥匙不再出现在主循环里" 'sk-user-real-key' "$CFG"
has "无关的键原样留着" '^  max_turns: 90$' "$CFG"
has "混进 auxiliary 的标量原样留着" '^  compact: false$' "$CFG"
YAMLOK "改完的文件 PyYAML 照样读得出来"
"$BIN" on hermes >"$SANDBOX/on2.log" 2>&1
cp "$CFG" "$SANDBOX/once.yaml"
"$BIN" on hermes >/dev/null 2>&1
if cmp -s "$CFG" "$SANDBOX/once.yaml"; then ok "幂等：再接管一次文件不变"; else bad "再接管一次改了文件"; fi

echo
echo "== 2. 两种用法都通 =="
"$BIN" __serve --port "$PORT" >"$SANDBOX/serve.log" 2>&1 &
SERVE_PID=$!
for _ in $(seq 1 50); do
  curl -sf -o /dev/null "http://127.0.0.1:$PORT/__newgate/status" && break
  sleep 0.1
done
curl -s -X POST "http://127.0.0.1:$UP_PORT/__mock/reset" >/dev/null
check "档位名 normal（跟着 newgate 走）" "$(ASK /a/hermes/v1 normal)" "200"
check "落到活动 profile 的 normal" "$(LASTUP)" "mock-normal"
check "点名 demo/mock-alt（钉在 alt 上）" "$(ASK /a/hermes/p/alt/v1 demo/mock-alt)" "200"
check "上游收到的是 alt 绑的模型（前缀被剥掉）" "$(LASTUP)" "mock-alt"
check "钉住那一行的档位名走那份 profile" "$(ASK /a/hermes/p/alt/v1 normal)" "200"
check "确实是 alt 的 normal" "$(LASTUP)" "mock-alt"

modelids() { curl -s "http://127.0.0.1:$PORT$1/models" | python3 -c 'import json,sys;print(",".join(m["id"] for m in json.load(sys.stdin)["data"]))'; }
check "/model 问到的跟着走那一行：只有档位" "$(modelids /a/hermes/v1)" "heavy,normal,mid,light,vision"
check "/model 问到的钉住 alt 那一行：档位 + 它绑的模型" "$(modelids /a/hermes/p/alt/v1)" "heavy,normal,mid,light,vision,demo/mock-alt"
hasnt "不写 discover_models（hermes 的归一化不认它，每轮一条 warning）" 'discover_models' "$CFG"

echo
echo "== 3. hermes 的本机服务探测：404，不算失败 =="
before=$(curl -s "http://127.0.0.1:$PORT/__newgate/status" | python3 -c 'import json,sys;print(json.load(sys.stdin).get("failures",0))' 2>/dev/null || echo 0)
check "/api/tags 404" "$(curl -s -o /dev/null -w '%{http_code}' "http://127.0.0.1:$PORT/a/hermes/api/tags")" "404"
check "/v1/props 404" "$(curl -s -o /dev/null -w '%{http_code}' "http://127.0.0.1:$PORT/a/hermes/v1/props")" "404"
after=$(curl -s "http://127.0.0.1:$PORT/__newgate/status" | python3 -c 'import json,sys;print(json.load(sys.stdin).get("failures",0))' 2>/dev/null || echo 0)
check "失败计数不动" "$after" "$before"

echo
echo "== 4. 守护进程在跑时加一份 profile：清单自动同步，用户选的主模型不被改回去 =="
# 用户在 hermes 里 `/model demo/mock-alt --provider newgate-alt --global`：
sed -i 's/^  provider: newgate$/  provider: newgate-alt/; s/^  default: normal$/  default: demo\/mock-alt/' "$CFG"
cat > "$NEWGATE_HOME/mappings/third.json" <<'JSON'
{"description":"added later","roles":{"normal":[{"provider":"demo","model":"mock-third"}]}}
JSON
synced=""
for _ in $(seq 1 40); do
  if command grep -q 'demo/mock-third:' "$CFG"; then synced=1; break; fi
  sleep 0.5
done
check "新 profile 那一行同步进来了" "${synced:-no}" "1"
has "用户选的 provider 没被改回去" '^  provider: newgate-alt$' "$CFG"
has "用户选的模型没被改回去" '^  default: demo/mock-alt$' "$CFG"
YAMLOK "同步之后 PyYAML 照样读得出来"

echo
echo "== 5. newgate off hermes：按键还原，用户期间的改动保留 =="
kill "$SERVE_PID" 2>/dev/null; SERVE_PID=""
sed -i 's/^  max_turns: 90$/  max_turns: 120/' "$CFG"
"$BIN" off hermes >"$SANDBOX/off.log" 2>&1
check "还原成功" "$?" "0"
hasnt "我们那几行都没了" 'newgate' "$CFG"
has "主循环回到原来的 provider" '^  provider: custom$' "$CFG"
has "原来的钥匙放回去了" 'sk-user-real-key' "$CFG"
has "用户接管期间改的那一格保留" '^  max_turns: 120$' "$CFG"
has "providers 回到空映射" '^providers: {}$' "$CFG"
YAMLOK "还原之后 PyYAML 照样读得出来"

echo
echo "结果: $PASS 通过, $FAIL 失败"
[ "$FAIL" = "0" ]
