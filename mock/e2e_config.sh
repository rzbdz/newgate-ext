#!/usr/bin/env bash
# 零 token 端到端：`newgate config` 那把梭（向导）。
#
# 它锁的是**用户第一次拿到这台机器时那三分钟**:选客户端 → 一个 base/key/模型 →
# 落盘 → 装 + 接管 → 起代理。所以它必须用真二进制、真终端语义（pty），并在一个
# 密闭沙箱里跑（HOME / NEWGATE_HOME / PATH 全部指向临时目录）。
#
# 用**假终端**（python3 的 pty）而不是 pipe：向导的每一屏都在 raw mode 里读按键，
# 用管道喂它会走到「这台机器上没法交互」那条路——那条路另有断言（第 0 节）。
#
# 假上游：一个只会答 `GET /v1/models` 的小 HTTP 服务（python3 http.server 的一段
# 脚本）。这样「模型清单」那句提示不需要任何真实上游，也**不花一个 token**。
#
# 全部在临时目录里，不碰真实 ~/.config/newgate，也不在这台机器上真装任何东西。

set -u

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
DIST_NAME="${NEWGATE_E2E_DIST:-default}"
case "$(uname -m)" in
  x86_64)        host_arch=amd64 ;;
  aarch64|arm64) host_arch=arm64 ;;
  *)             host_arch=$(uname -m) ;;
esac
BIN_SRC="${NEWGATE_E2E_BIN:-$ROOT/dist/newgate-$DIST_NAME-$(uname -s | tr 'A-Z' 'a-z')-$host_arch}"
SANDBOX="${NEWGATE_E2E_SANDBOX:-$(mktemp -d /tmp/newgate-config-e2e.XXXXXX)}"
BIN="$SANDBOX/bin/newgate"

PASS=0; FAIL=0
ok()   { echo "  ✓ $1"; PASS=$((PASS+1)); }
bad()  { echo "  ✗ $1"; FAIL=$((FAIL+1)); }
check(){ if [ "$2" = "$3" ]; then ok "$1"; else bad "$1 (期望 '$3'，实际 '$2')"; fi; }

export NEWGATE_HOME="$SANDBOX/ng"
export NEWGATE_LANG=en
unset LC_ALL LC_MESSAGES LANG LANGUAGE 2>/dev/null || true
mkdir -p "$NEWGATE_HOME" "$(dirname "$BIN")"
# **密闭 PATH + 假包管理器**：向导真的会去装选中的客户端，而这台开发机上装着
# 真 codex（nvm 里）和真 hermes。插在前面只保证「我们的先被找到」，挡不住「真的
# 还在那儿」——而这条 e2e 要验的恰恰是「没装的会被装上」。
#
# 2026-11-17 第一版吃过这个亏：PATH 里有真 curl，于是向导去跑了 hermes 的那行
# `curl … | bash`，脚本在 pty 里等一个永远不会来的输入。它什么都没装上（最后一步
# 是 exec），但那条命令不该被跑一次。
FAKEBIN="$SANDBOX/fakebin"
mkdir -p "$FAKEBIN"
export PATH="$FAKEBIN:$(dirname "$BIN"):/usr/bin:/bin"
export HOME="$SANDBOX/home"
mkdir -p "$HOME"

# 假 npm：把 argv 记下来，然后在「装出来的 bin 目录」里放一个同名脚本。
# 真 npm 会把可执行文件放进 prefix 的 bin，这里照那个形状做。
cat > "$FAKEBIN/npm" <<'NPM'
#!/usr/bin/env bash
echo "$@" >> "$FAKE_NPM_LOG"
pkg=""
for a in "$@"; do case "$a" in @*|*-ai) pkg="$a" ;; esac; done
# 包名 → 二进制名：@openai/codex → codex，@anthropic-ai/claude-code → claude
bin=$(printf '%s' "$pkg" | sed 's#^@[^/]*/##; s/-code$//')
[ -n "$bin" ] || exit 1
cat > "$NEWGATE_INSTALL_BIN/$bin" <<SH
#!/usr/bin/env bash
echo "fake $bin: \$*"
SH
chmod +x "$NEWGATE_INSTALL_BIN/$bin"
exit 0
NPM

# 假 curl：只为拦 hermes 那行 `curl … | bash`。给它一份「什么都不做的安装脚本」，
# 于是那条命令会成功、并且什么都不碰——这条 e2e 不验上游安装脚本的行为。
cat > "$FAKEBIN/curl" <<'CURL'
#!/usr/bin/env bash
out=""
prev=""
for a in "$@"; do [ "$prev" = "-o" ] && out="$a"; prev="$a"; done
[ -n "$out" ] || exit 1
printf '#!/usr/bin/env bash
exit 0
' > "$out"
CURL
chmod +x "$FAKEBIN/npm" "$FAKEBIN/curl"

export NEWGATE_INSTALL_BIN="$SANDBOX/installed-bin"
mkdir -p "$NEWGATE_INSTALL_BIN"
# 让「装出来的」二进制也在 PATH 上（真 npm 的 prefix/bin 就是这样）。
export PATH="$NEWGATE_INSTALL_BIN:$PATH"
export FAKE_NPM_LOG="$SANDBOX/npm-argv.txt"

[ -x "$BIN_SRC" ] || { echo "找不到二进制 $BIN_SRC——先跑 build/build.sh"; exit 1; }
cp "$BIN_SRC" "$BIN"

cleanup() {
  "$BIN" stop >/dev/null 2>&1 || true
  [ -n "${FAKE_MODELS_PID:-}" ] && kill "$FAKE_MODELS_PID" 2>/dev/null
  if [ -n "${NEWGATE_E2E_KEEP:-}" ]; then
    echo "沙箱保留（NEWGATE_E2E_KEEP）: $SANDBOX"
  else
    rm -rf "$SANDBOX"
  fi
}
trap cleanup EXIT

# ---- 假上游：只答 GET /v1/models ----
cat > "$SANDBOX/fake_models.py" <<'PY'
import json, sys
from http.server import BaseHTTPRequestHandler, HTTPServer

class H(BaseHTTPRequestHandler):
    def do_GET(self):
        if self.path.rstrip('/').endswith('/models'):
            body = json.dumps({"object": "list", "data": [
                {"id": "e2e-small-model"}, {"id": "e2e-big-model"}]}).encode()
            self.send_response(200)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(body)))
            self.end_headers()
            self.wfile.write(body)
            return
        self.send_response(404); self.end_headers()
    def log_message(self, *a): pass

srv = HTTPServer(("127.0.0.1", int(sys.argv[1])), H)
print(srv.server_address[1], flush=True)
srv.serve_forever()
PY
FAKE_PORT=$(python3 - "$SANDBOX" <<'PY'
import socket, subprocess, sys, os
s = socket.socket(); s.bind(("127.0.0.1", 0)); port = s.getsockname()[1]; s.close()
p = subprocess.Popen([sys.executable, os.path.join(sys.argv[1], "fake_models.py"), str(port)],
                     stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
open(os.path.join(sys.argv[1], "fake_models.pid"), "w").write(str(p.pid))
print(port)
PY
)
FAKE_MODELS_PID="$(cat "$SANDBOX/fake_models.pid")"
for _ in 1 2 3 4 5 6 7 8 9 10; do
  curl -fsS "http://127.0.0.1:$FAKE_PORT/v1/models" >/dev/null 2>&1 && break
  sleep 0.2
done

echo
echo "== 0. 不是终端时：什么都不改，逐条说清楚 =="
# 只看**配置**文件：newgate.log 是每个进程都会写的装配留痕（内核 app.Main 的第一
# 件事），它不是「向导改了什么」，把它算进来只会让这条断言看起来失败。
cfg_snapshot() { find "$NEWGATE_HOME" -type f ! -name '*.log' | sort | xargs -r md5sum 2>/dev/null; }
before="$(cfg_snapshot)"
out=$(printf '' | "$BIN" config 2>&1); rc=$?
check "没有终端以 64 退出" "$rc" "64"
case "$out" in
  *"interactive terminal"*) ok "说清了需要交互终端";;
  *) bad "没说清为什么不能跑：$out";;
esac
case "$out" in
  *"newgate on"*) ok "给出了手动那几步";;
  *) bad "没给出手动那几步：$out";;
esac
after="$(cfg_snapshot)"
check "没有终端时一个文件都没写" "$after" "$before"

echo
echo "== 1. 真向导（pty）：选客户端 → 一个上游 → 落盘 =="
# 脚本化按键：
#   - 多选那一屏：默认勾的是「已装、没接管」的。这个沙箱的 PATH 是密闭的，
#     一个客户端都没装 → 默认一个不勾 → 调到 claude，用空格勾上，再回车。
#     **只勾一个**：这条 e2e 要验「没装就装、装上就接管」，勾多了会让断言
#     从「claude 的 shim 在」变成「一屏里某一行对不对」，而后者已经由单测锁了。
#     顺序是 claude/codex/hermes/opencode：调一下就到 claude。
#   - 上游三格：base / key / 名字（地址指向假上游，所以模型清单问得到）。
#   - 模型清单：回车选第一个。
read -r -d '' KEYS_SCRIPT <<'PY' || true
import os, pty, select, sys, time
binp, base, key, name = sys.argv[1:5]
pid, fd = pty.fork()
if pid == 0:
    os.execvpe(binp, [binp, "config", "--no-start"], dict(os.environ, NEWGATE_LANG="en"))
def drain(t):
    end = time.time() + t
    while time.time() < end:
        r, _, _ = select.select([fd], [], [], 0.1)
        if r:
            try: os.read(fd, 65536)
            except OSError: return
drain(1.2)
for chunk, wait in [(" ", 0.4), ("\r", 0.6), (base + "\r", 0.5), (key + "\r", 0.5),
                    (name + "\r", 0.5), ("\r", 1.2)]:
    try: os.write(fd, chunk.encode())
    except OSError: break
    drain(wait)
try:
    os.write(fd, b"\x03")
except OSError:
    pass
drain(0.5)
try: os.waitpid(pid, 0)
except ChildProcessError: pass
PY
out=$(python3 -c "$KEYS_SCRIPT" "$BIN" "http://127.0.0.1:$FAKE_PORT/v1" "sk-e2e" "e2e-upstream" 2>&1); rc=$?

if [ -f "$NEWGATE_HOME/providers.json" ]; then ok "写出了 providers.json"; else bad "没有 providers.json（输出：$out）"; fi
python3 - "$NEWGATE_HOME" <<'PY'
import json, sys, os
home = sys.argv[1]
provs = json.load(open(os.path.join(home, "providers.json")))["providers"]
p = provs.get("e2e-upstream", {})
assert p.get("base_url", "").startswith("http://127.0.0.1"), p
assert p.get("api_key") == "sk-e2e", p
kv = open(os.path.join(home, "mappings", "default.kv")).read()
for tier in ("heavy", "normal", "mid", "light", "vision"):
    line = [l for l in kv.splitlines() if l.startswith(tier + "=")]
    assert line == [tier + "=e2e-upstream/e2e-big-model"], (tier, line)
state = json.load(open(os.path.join(home, "state.json")))
assert state["default_profile"] == "default", state
PY
check "providers / default.kv / state.json 三者都对得上" "$?" "0"

echo
echo "== 2. 模型清单里挑的那个真的被写进去了 =="
case "$(cat "$NEWGATE_HOME/mappings/default.kv")" in
  *"normal=e2e-upstream/e2e-big-model"*) ok "使用清单里的第一个模型（e2e-big-model）";;
  *) bad "档位没有指向清单里的模型：$(cat "$NEWGATE_HOME/mappings/default.kv")";;
esac
case "$(cat "$NEWGATE_HOME/mappings/default.kv")" in
  *"# Later, point tiers"*) ok "档位文件里带着「以后怎么拆开」的注释（给人和 AI 看的）";;
  *) bad "档位文件没有说明怎么改";;
esac

echo
echo "== 2b. 真装 + 真接管（选中的那几家）=="
# 判据是 readlink：shim 是一个指向 newgate 的**符号链接**，`cat` 一个链接会
# 去读它指向的二进制（于是那条命令替换里全是 ELF 字节）。
case "$(readlink "$NEWGATE_HOME/bin/claude" 2>/dev/null)" in
  *newgate*) ok "claude 的 PATH shim 装上了（指向 newgate）";;
  *) bad "claude 没有 shim";;
esac
if [ -f "$NEWGATE_INSTALL_BIN/claude" ]; then ok "假 npm 被唤去装 claude（这一条验的是「没装就装」）"; else bad "没跑安装命令"; fi
# codex 这次**没有**被勾选，所以它必须一个字节都没被碰——包括它的配置文件
# 根本不该被创建（codex 自己第一次运行时才写它）。
if [ -f "$HOME/.codex/config.toml" ]; then
  bad "没勾选 codex，但它的 config.toml 被写了/改写了"
else
  ok "没勾选的 codex 一个字节都没被碰（config.toml 都没创建）"
fi
if [ -f "$NEWGATE_HOME/.newgate.pid" ]; then bad "--no-start 却把代理起起来了"; else ok "--no-start：代理没被起起来"; fi

echo
echo "== 3. 幂等：再跑一次不会把已有配置问一遍、也不会改掉它 =="
before="$(md5sum "$NEWGATE_HOME/mappings/default.kv" | awk '{print $1}')"
# 这一次一个按键都不给（脚本立即 Ctrl-C）。能跑完说明它**没有**问上游那一屏。
python3 - "$BIN" <<'PY' >/dev/null 2>&1 || true
import os, pty, select, sys, time
pid, fd = pty.fork()
if pid == 0:
    os.execvpe(sys.argv[1], [sys.argv[1], "config", "--no-start"], dict(os.environ, NEWGATE_LANG="en"))
time.sleep(1.0)
try:
    os.write(fd, b"\x03")
except OSError:
    pass
try: os.waitpid(pid, 0)
except ChildProcessError: pass
PY
after="$(md5sum "$NEWGATE_HOME/mappings/default.kv" | awk '{print $1}')"
check "第二次运行没碰 default.kv" "$after" "$before"

echo
echo "== 4. 已经有一个能用的配置时，向导不再问上游 =="
# 判据：上面那次一个按键都没给就走了（如果它问了上游，Ctrl-C 会让它什么都不做，
# 但那时它会先画「Base URL」那一屏）。这里用一行更硬的证据：退出码与文件不变。
python3 - "$BIN" <<'PY'
import os, pty, select, sys, time
pid, fd = pty.fork()
if pid == 0:
    os.execvpe(sys.argv[1], [sys.argv[1], "config", "--no-start"], dict(os.environ, NEWGATE_LANG="en"))
out = b""
end = time.time() + 1.5
while time.time() < end:
    r, _, _ = select.select([fd], [], [], 0.1)
    if r:
        try: out += os.read(fd, 65536)
        except OSError: break
try: os.write(fd, b"\x03")
except OSError: pass
try: os.waitpid(pid, 0)
except ChildProcessError: pass
assert b"Base URL" not in out, out[-800:]
PY
check "没有出现 Base URL 那一屏" "$?" "0"

echo
echo "结果: $PASS 通过, $FAIL 失败"
[ "$FAIL" = "0" ]
