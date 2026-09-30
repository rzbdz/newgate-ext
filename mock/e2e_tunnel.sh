#!/usr/bin/env bash
# ssh-tunnel 的端到端：**真的 sshd、真的密钥、真的握手**。
#
# # 为什么这条必须有，而单元测试替不了它
#
# 这个模块里有一条分界线，两边的验法完全不同：
#
#   - 它**自己的**行为（懒拨、复用、空闲回收、路径改写、两道门）——用假 Dialer
#     就够了，而且跑得快，所以那些在 *_test.go 里，每次提交都跑；
#   - **它和一台真的 OpenSSH 能不能说上话**——假不出来。协议版本、密钥交换、
#     认证方式、direct-tcpip 通道、known_hosts 的格式，每一样都能在「我这边的
#     代码看起来完全正确」的前提下对不上。
#
# # 它为什么不进 CI
#
# 依赖本机的 sshd 与端口（与 e2e_reasoning_affinity.sh 同一条：那条花真钱，
# 这条花本机环境）。CI 里那条进程内的真 SSH 集成测试覆盖了同一件事的骨架
# （见 ssh_integration_test.go），这条是**开发机上**那一遍完整的手工确认。
#
# 用法：bash mock/e2e_tunnel.sh
#      NEWGATE_E2E_KEEP=1   保留沙箱（排查用）
set -u

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
DIST_NAME="${NEWGATE_E2E_DIST:-default}"

case "$(uname -m)" in
  x86_64|amd64) host_arch=amd64 ;;
  aarch64|arm64) host_arch=arm64 ;;
  *) echo "不认识的架构：$(uname -m)" >&2; exit 2 ;;
esac
BIN_SRC="${NEWGATE_E2E_BIN:-$ROOT/dist/newgate-$DIST_NAME-$(uname -s | tr 'A-Z' 'a-z')-$host_arch}"

if [ ! -x "$BIN_SRC" ]; then
  echo "找不到产物：$BIN_SRC" >&2
  echo "先编一份：build/build.sh dist" >&2
  exit 2
fi
for tool in sshd ssh-keygen curl python3; do
  command -v "$tool" >/dev/null 2>&1 || { echo "缺 $tool" >&2; exit 2; }
done

SANDBOX="${NEWGATE_E2E_SANDBOX:-$(mktemp -d /tmp/newgate-tunnel-e2e.XXXXXX)}"
WEB_PORT="${NEWGATE_E2E_WEB_PORT:-18899}"   # 这个脚本自己的端口，别和别的 e2e 抢
SSH_PORT="${NEWGATE_E2E_SSH_PORT:-12222}"
FWD_PORT="${NEWGATE_E2E_FWD_PORT:-19401}"
PASS=0; FAIL=0
ok(){ PASS=$((PASS+1)); printf '  ✓ %s\n' "$1"; }
bad(){ FAIL=$((FAIL+1)); printf '  ✗ %s\n' "$1"; }
check(){ if [ "$2" = "$3" ]; then ok "$1"; else bad "$1（想要 [$3]，实际 [$2]）"; fi; }
contains(){ case "$2" in *"$3"*) ok "$1" ;; *) bad "$1（[$2] 里没有 [$3]）" ;; esac; }
section(){ printf '\n== %s ==\n' "$1"; }

SRV_PID=""
SSHD_PID=""
WEB_PID=""
cleanup(){
  [ -n "$SRV_PID" ] && kill "$SRV_PID" 2>/dev/null
  [ -n "$SSHD_PID" ] && kill "$SSHD_PID" 2>/dev/null
  [ -n "$WEB_PID" ] && kill "$WEB_PID" 2>/dev/null
  wait 2>/dev/null
  if [ -n "${NEWGATE_E2E_KEEP:-}" ]; then echo "沙箱保留在 $SANDBOX"; else rm -rf "$SANDBOX"; fi
}
trap cleanup EXIT

export NEWGATE_HOME="$SANDBOX/ng"
export NEWGATE_LANG=en
unset LC_ALL LC_MESSAGES LANG LANGUAGE

# HOME 也圈进沙箱。**这是必要的**：accept-new 会往 ~/.ssh/known_hosts 里写一行，
# 而这条脚本要用「那一行真的写下去了」当断言——不圈的话它写进的是开发者真实的
# known_hosts，于是这条测试既污染了那台机器，又验不到自己以为在验的东西。
# sshd 那边不受影响：它的配置里全是绝对路径。
export HOME="$SANDBOX/home"
mkdir -p "$NEWGATE_HOME/mappings" "$HOME/.ssh"
chmod 700 "$HOME/.ssh"

# ── 一台真的 sshd（本机、非特权端口、只认我们这一把一次性密钥） ─────────────
section "搭一台真 sshd"

ssh-keygen -q -t ed25519 -N '' -f "$SANDBOX/host_ed25519"
ssh-keygen -q -t ed25519 -N '' -f "$SANDBOX/id_ed25519"
cat > "$SANDBOX/sshd_config" <<EOF
Port $SSH_PORT
ListenAddress 127.0.0.1
HostKey $SANDBOX/host_ed25519
PidFile $SANDBOX/sshd.pid
AuthorizedKeysFile $SANDBOX/authorized_keys
UsePAM no
PasswordAuthentication no
KbdInteractiveAuthentication no
PubkeyAuthentication yes
PermitRootLogin yes
AllowTcpForwarding yes
StrictModes no
LogLevel ERROR
EOF
cp "$SANDBOX/id_ed25519.pub" "$SANDBOX/authorized_keys"
chmod 600 "$SANDBOX/authorized_keys" "$SANDBOX/host_ed25519" "$SANDBOX/id_ed25519"

# 这个 sshd 跑在**当前用户**下（沙箱里，不碰系统配置）。root 下 sshd 会抱怨
# 权限，用 StrictModes no 绕开——沙箱里那点权限规矩不是这里要验的东西。
"$(command -v sshd)" -f "$SANDBOX/sshd_config" -D -e >"$SANDBOX/sshd.log" 2>&1 &
SSHD_PID=$!
for _ in $(seq 1 50); do
  if (exec 3<>/dev/tcp/127.0.0.1/$SSH_PORT) 2>/dev/null; then exec 3<&- 3>&-; break; fi
  sleep 0.1
done
if ! (exec 3<>/dev/tcp/127.0.0.1/$SSH_PORT) 2>/dev/null; then
  echo "sshd 没起来，看 $SANDBOX/sshd.log"; cat "$SANDBOX/sshd.log"; exit 2
fi
exec 3<&- 3>&- 2>/dev/null
ok "sshd 在 127.0.0.1:$SSH_PORT 上听着"

# ── 远端那台「newgate 界面」：一个普通的 HTTP 服务，路径与真产品同形 ───────
section "假装远端那台机器上有一个 newgate 界面"

python3 - "$SANDBOX/web" <<'PY' &
import http.server, os, sys
root = sys.argv[1]
os.makedirs(root, exist_ok=True)
with open(os.path.join(root, "index.html"), "w") as f:
    # 绝对前缀 /ui/ 是**真产品产物的形状**（vite 的 base）。这一条 e2e 要验的
    # 正是这个前缀在 route 那条路上被改写、在转发那条路上原样保留。
    f.write('<!doctype html><html><head>'
            '<link rel="icon" href="/ui/favicon.svg">'
            '<script type="module" src="/ui/assets/app.js"></script>'
            '</head><body>REMOTE-INDEX</body></html>')

class H(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        if self.path == "/ui/":
            body = open(os.path.join(root, "index.html"), "rb").read()
            self.send_response(200)
            self.send_header("Content-Type", "text/html; charset=utf-8")
            self.send_header("Content-Length", str(len(body)))
            self.end_headers()
            self.wfile.write(body)
            return
        if self.path == "/ui/api/snapshot":
            body = b'{"who":"remote"}'
            self.send_response(200)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(body)))
            self.end_headers()
            self.wfile.write(body)
            return
        self.send_response(404); self.end_headers()
    def log_message(self, *a): pass

http.server.HTTPServer(("127.0.0.1", 18099), H).serve_forever()
PY
WEB_PID=$!
for _ in $(seq 1 50); do
  curl -sf http://127.0.0.1:18099/ui/ >/dev/null 2>&1 && break
  sleep 0.1
done
curl -sf http://127.0.0.1:18099/ui/ >/dev/null 2>&1 || { echo "假远端没起来"; exit 2; }
ok "远端界面在 127.0.0.1:18099（从远端那台机器看）"

# ── newgate 起来 ────────────────────────────────────────────────────────────
section "起一个新 gate（沙箱）"

BIN="$SANDBOX/bin/newgate"
mkdir -p "$(dirname "$BIN")"
cp "$BIN_SRC" "$BIN"

# 一份最小的沙箱配置：一个 provider + 一个档位 + state.json。
# **state.json 是必需的**：没有它 `/__newgate/status` 回 400（读不出配置），
# 而下面那个就绪探针用 `curl -sf`，400 会被当成「还没起来」——于是脚本会在一个
# 其实已经跑起来的守护进程上超时（实测踩过）。
cat > "$NEWGATE_HOME/providers.json" <<'JSON'
{"providers":{"demo":{"protocol":"anthropic","base_url":"http://127.0.0.1:9/demo","api_key":"sk-not-used-here"}}}
JSON
cat > "$NEWGATE_HOME/mappings/demo.json" <<'JSON'
{"description":"e2e profile","roles":{"normal":[{"provider":"demo","model":"m"}]}}
JSON
python3 -c 'import json,sys; print(json.dumps({"default_profile":"demo","port":int(sys.argv[1])}))' "$WEB_PORT" \
  > "$NEWGATE_HOME/state.json"
# `__serve`：这条脚本要验的是那条 route 与转发端口，别把它和「入口怎么认领」
# 那条测试混在一起（与 e2e_dashboard.sh 同一条理由）。
"$BIN" __serve --port "$WEB_PORT" >"$SANDBOX/newgate.log" 2>&1 &
SRV_PID=$!
for _ in $(seq 1 80); do
  curl -sf "http://127.0.0.1:$WEB_PORT/__newgate/status" >/dev/null 2>&1 && break
  sleep 0.1
done
curl -sf "http://127.0.0.1:$WEB_PORT/__newgate/status" >/dev/null 2>&1 || {
  echo "newgate 没起来："; tail -30 "$SANDBOX/newgate.log"; exit 2
}
ok "newgate 在 127.0.0.1:$WEB_PORT 上"

# ── 配一条 tunnel ───────────────────────────────────────────────────────────
section "配一条 tunnel 并让它连上"

"$BIN" tunnel add snode1 \
  --host 127.0.0.1 --port "$SSH_PORT" --user "$(id -un)" \
  --identity "$SANDBOX/id_ed25519" \
  --remote-host 127.0.0.1 --remote-port 18099 \
  --local-port "$FWD_PORT" --persistent --host-key accept-new \
  >"$SANDBOX/add.log" 2>&1
check "tunnel add 的退出码" "$?" "0"
contains "add 说出了路线" "$(cat "$SANDBOX/add.log")" "/ui/remote/snode1/"

# 等 daemon 那条后台循环把它连上（循环是 2 秒一轮）。
connected=0
for _ in $(seq 1 60); do
  if curl -sf "http://127.0.0.1:$FWD_PORT/ui/api/snapshot" 2>/dev/null | grep -q remote; then
    connected=1; break
  fi
  sleep 0.2
done
check "常驻的 target 自己连上了（accept-new 也写进了 known_hosts）" "$connected" "1"
# -F：这一行里全是方括号（[127.0.0.1]:12222），当正则写要转义，而当**字面量**
# 写才是这里的意思（本机的 grep 是个 ugrep 包装，转义规则还不一样）。
if grep -qF "]:$SSH_PORT " "$HOME/.ssh/known_hosts" 2>/dev/null; then
  ok "accept-new 把主机密钥记进了 known_hosts"
else
  bad "accept-new 没有写 known_hosts（$(cat "$HOME/.ssh/known_hosts" 2>/dev/null | head -2)）"
fi
contains "accept-new 在日志里报了一句（信任一台机器是一个事件）" \
  "$(cat "$SANDBOX/newgate.log")" "recorded the host key"

# ── 路线一：本地转发端口（保真） ───────────────────────────────────────────
section "路线一：本地转发端口（一个字节都不动）"

body=$(curl -s "http://127.0.0.1:$FWD_PORT/ui/")
contains "转发端口能拿到远端首页" "$body" "REMOTE-INDEX"
# **这一条是这条路存在的全部理由**：远端产物里写死的绝对地址原样可用。
contains "远端的 /ui/assets 原样保留（没有被改写）" "$body" 'src="/ui/assets/app.js"'

snap=$(curl -s "http://127.0.0.1:$FWD_PORT/ui/api/snapshot")
contains "转发端口上的 API 也通" "$snap" '"who":"remote"'

# ── 路线二：共享端口上的 route（改写） ─────────────────────────────────────
section "路线二：共享端口上的 /ui/remote/<id>/（改写）"

body=$(curl -s "http://127.0.0.1:$WEB_PORT/ui/remote/snode1/")
contains "route 能拿到远端首页" "$body" "REMOTE-INDEX"
contains "正文里的绝对前缀被改写成了这条 route" "$body" 'src="/ui/remote/snode1/assets/app.js"'
contains "favicon 也被改写了" "$body" 'href="/ui/remote/snode1/favicon.svg"'

snap=$(curl -s "http://127.0.0.1:$WEB_PORT/ui/remote/snode1/api/snapshot")
contains "route 上的 API 也通（远端收到的是 /ui/api/snapshot）" "$snap" '"who":"remote"'

# ── 门与边界 ────────────────────────────────────────────────────────────────
section "门与边界"

code=$(curl -s -o /dev/null -w '%{http_code}' -H 'Host: evil.example' "http://127.0.0.1:$WEB_PORT/ui/remote/snode1/")
check "非回环 Host 被拒（DNS rebinding 那道门）" "$code" "403"

code=$(curl -s -o /dev/null -w '%{http_code}' "http://127.0.0.1:$WEB_PORT/ui/remote/nope/")
check "不认识的 id 是 404" "$code" "404"
contains "404 说得出有哪些 id" "$(curl -s "http://127.0.0.1:$WEB_PORT/ui/remote/nope/")" "snode1"

code=$(curl -s -o /dev/null -w '%{http_code}' "http://127.0.0.1:$WEB_PORT/ui/remote/snode1")
check "少一个尾斜杠时是 302" "$code" "302"

code=$(curl -s -o /dev/null -w '%{http_code}' "http://127.0.0.1:$WEB_PORT/ui/")
check "本机自己的界面照常（/ui/ 没被抢）" "$code" "200"

# ── 本机界面能看到它 ────────────────────────────────────────────────────────
section "本机界面上看得见"

snap=$(curl -s "http://127.0.0.1:$WEB_PORT/ui/api/snapshot")
contains "快照里有这一节" "$snap" 'ssh-tunnel'
contains "配置卡里有这条 target" "$snap" 'snode1'
contains "状态表里有这一行" "$snap" 'ssh-tunnel.status'

# ── 命令行 ──────────────────────────────────────────────────────────────────
section "命令行"

"$BIN" tunnel ls >"$SANDBOX/ls.log" 2>&1
contains "tunnel ls 列出了 target" "$(cat "$SANDBOX/ls.log")" "snode1"
contains "tunnel ls 给出了两条路" "$(cat "$SANDBOX/ls.log")" "/ui/remote/snode1/"

"$BIN" tunnel test snode1 >"$SANDBOX/test.log" 2>&1
check "tunnel test 的退出码" "$?" "0"
contains "test 走到了最后一跳" "$(cat "$SANDBOX/test.log")" "accepts connections"

"$BIN" tunnel rm snode1 >"$SANDBOX/rm.log" 2>&1
check "tunnel rm 的退出码" "$?" "0"
"$BIN" tunnel ls >"$SANDBOX/ls2.log" 2>&1
contains "删掉之后 ls 里没有了" "$(cat "$SANDBOX/ls2.log")" "no targets yet"

printf '\n结果: %d 通过, %d 失败\n' "$PASS" "$FAIL"
[ "$FAIL" -eq 0 ] || exit 1
