#!/usr/bin/env bash
# 零 token 端到端：**客户端没装时那句问话，以及随后的 best-effort 安装**。
#
# 它锁的是这条路上唯一一处「我们会去改这台机器」的地方——跑一条外部命令把一个
# 工具装上。所以它有两条不能破的性质，各验一段：
#
#   1  **不装就是真的不装**：答 n 之后，说的那句话里必须带着**完整的命令**（用户
#      要能自己复制去跑），而且工具**确实还没在**——「问了一句、然后偷偷装了」比
#      不问还糟；
#   2  **装不成必须说清楚**：装不上（没有 npm / 网络不通 / 没权限）不是故障，
#      但**不能看着像装成了**。分开验两种装不成：命令失败，以及命令成功但这个
#      进程的 PATH 里还是找不到它（包管理器的 bin 目录不在这条 PATH 上，很常见）。
#
# 用**假 npm**：我们绝不在这台机器上真装东西。假 npm 只往沙箱里写。
#
# 全部在临时沙箱里跑，不碰真实 ~/.config，也不碰真实 PATH。

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
SANDBOX="${NEWGATE_E2E_SANDBOX:-$(mktemp -d /tmp/newgate-install-e2e.XXXXXX)}"
BIN="$SANDBOX/bin/newgate"
FAKEBIN="$SANDBOX/fakebin"     # 假 npm 住这儿
INSTALLED="$SANDBOX/bin"       # 「装出来」的 codex 落这儿（在 PATH 上）
OFFPATH="$SANDBOX/not-on-path" # 装到 PATH 外面的那一种

PASS=0; FAIL=0
ok()   { echo "  ✓ $1"; PASS=$((PASS+1)); }
bad()  { echo "  ✗ $1"; FAIL=$((FAIL+1)); }
check(){ if [ "$2" = "$3" ]; then ok "$1"; else bad "$1 (期望 '$3'，实际 '$2')"; fi; }

export NEWGATE_HOME="$SANDBOX/ng"
export NEWGATE_LANG=en
unset LC_ALL LC_MESSAGES LANG LANGUAGE 2>/dev/null || true
mkdir -p "$NEWGATE_HOME/mappings" "$(dirname "$BIN")" "$FAKEBIN" "$OFFPATH"
# **PATH 是密闭的**：只有沙箱那两个目录加系统最基本的两个。
#
# 为什么不能只把自己的目录**插到前面**：这台开发机上装着真的 codex（nvm 的 bin
# 里）和真的 npm，插前面只保证「我们的先被找到」，却挡住「真的还在那儿」——
# 而这条 e2e 的**全部前提**就是那个工具不在。第一版就是这么写的，于是
# `Installed()` 一开始就是真，安装那条路一次都没走到，而失败的样子是「真 codex
# 抱怨 -y 不认识」——一个和安装毫无关系的报错。实测（2026-09-21）：
# `command -v codex` → `~/.nvm/versions/node/v24.13.0/bin/codex`。
#
# 密闭之后本脚本自己用的 cat / rm / chmod / dirname 也还在（都在 /usr/bin 或
# /bin），而 bash 在 /bin——所以这一条对脚本本身是透明的。
export PATH="$FAKEBIN:$SANDBOX/bin:/usr/bin:/bin"

cleanup() {
  # **先停 daemon，再删目录**。第 5 条会启动客户端，而 `newgate <agent>` 在代理
  # 没起来时会**顺手把它拉起来**——那个进程活过了脚本，还在往 NEWGATE_HOME 里
  # 写文件，于是 `rm -rf` 撞上「目录非空」（2026-09-21 实测，第一版就这么红的）。
  # 停它走 `newgate stop`：pidfile 与优雅退出都由它自己管，比 kill 一个 pid 稳。
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
cp "$BIN_SRC" "$BIN"

cat > "$NEWGATE_HOME/providers.json" <<'JSON'
{"providers":{"demo":{"protocol":"anthropic","base_url":"http://127.0.0.1:9/demo","api_key":"sk-e2e"}}}
JSON
cat > "$NEWGATE_HOME/mappings/demo.json" <<'JSON'
{"description":"install e2e","roles":{"normal":[{"provider":"demo","model":"demo-model"}]}}
JSON
printf '%s' '{"default_profile":"demo"}' > "$NEWGATE_HOME/state.json"

# 假 npm：把 argv 记下来（下面要断言「跑的就是它自己说的那条命令」），再按
# FAKE_NPM_MODE 决定装到哪儿。真实 npm 的 install 会把可执行文件放进 prefix 的
# bin 里，这里照那个形状做。
cat > "$FAKEBIN/npm" <<'NPM'
#!/usr/bin/env bash
echo "$@" > "$FAKE_NPM_LOG"
case "${FAKE_NPM_MODE:-ok}" in
  ok)
    cat > "$FAKE_NPM_TARGET/codex" <<'CODEX'
#!/usr/bin/env bash
echo "fake codex ran with: $*"
CODEX
    chmod +x "$FAKE_NPM_TARGET/codex"
    ;;
  fail)
    echo "npm ERR! network timeout" >&2
    exit 1
    ;;
  offpath)
    cat > "$FAKE_NPM_TARGET/codex" <<'CODEX'
#!/usr/bin/env bash
echo "fake codex ran with: $*"
CODEX
    chmod +x "$FAKE_NPM_TARGET/codex"
    ;;
esac
exit 0
NPM
chmod +x "$FAKEBIN/npm"
export FAKE_NPM_LOG="$SANDBOX/npm-argv.txt"

echo
echo "== 1. 答 n：说清楚、但**真的不装** =="
out=$(printf 'n\n' | "$BIN" codex 2>&1); rc=$?
check "没装时以 0 退出（用户自己的选择，不是错误）" "$rc" "0"
case "$out" in
  *"npm install --global @openai/codex"*) ok "拒绝时给出了完整命令（能直接复制去跑）";;
  *) bad "拒绝时没说清楚该跑什么：$out";;
esac
if [ -x "$INSTALLED/codex" ]; then bad "答了 n，codex 还是被装上了"; else ok "答了 n，codex 确实没被装上"; fi
if [ -f "$FAKE_NPM_LOG" ]; then bad "答了 n，假 npm 还是被调了"; else ok "答了 n，安装命令一次都没跑"; fi

echo
echo "== 2. -y：真装，而且装完接着往下走 =="
export FAKE_NPM_MODE=ok FAKE_NPM_TARGET="$INSTALLED"
out=$("$BIN" codex -y 2>&1); rc=$?
check "跑的就是它自己说的那条命令" "$(cat "$FAKE_NPM_LOG" 2>/dev/null)" "install --global @openai/codex"
if [ -x "$INSTALLED/codex" ]; then ok "装出来的 codex 在 PATH 上"; else bad "codex 没被装上"; fi
case "$out" in
  *"running: npm install --global @openai/codex"*) ok "装之前先说了要跑什么（不静默执行外部命令）";;
  *) bad "没说它要跑什么就执行了：$out";;
esac
# `-y` 是**我们**的参数，不许透传给客户端。
case "$out" in
  *"-y"*) bad "-y 被透传给了客户端：$out";;
  *) ok "-y 被摘掉了，没有透传给客户端";;
esac

echo
echo "== 3. 装不成（命令自己失败）：报出来，退出码是「不可用」 =="
rm -f "$INSTALLED/codex" "$FAKE_NPM_LOG"
export FAKE_NPM_MODE=fail FAKE_NPM_TARGET="$INSTALLED"
out=$("$BIN" codex -y 2>&1); rc=$?
check "装失败以 69 退出" "$rc" "69"
case "$out" in
  *"install failed"*"npm install --global @openai/codex"*) ok "失败那句点名了是哪条命令";;
  *) bad "装失败没说清是哪条命令：$out";;
esac
# 安装命令**自己的**输出也要看得见。它是实时打在 stderr 上的（不经过我们），
# 所以判据落在「合起来看得到」而不是「被塞进了错误字符串里」——第一版就是按后者
# 写的，于是明明看得见却判了红。
case "$out" in
  *"npm ERR! network timeout"*) ok "安装命令自己的报错原样可见（没被吞掉）";;
  *) bad "安装命令的报错被吞了：$out";;
esac
if [ -x "$INSTALLED/codex" ]; then bad "装失败了却有个 codex 在那儿"; else ok "装失败了，codex 确实不在"; fi

echo
echo "== 4. 装成了但不在 PATH 上：**不许假装成功** =="
rm -f "$FAKE_NPM_LOG"
export FAKE_NPM_MODE=offpath FAKE_NPM_TARGET="$OFFPATH"
out=$("$BIN" codex -y 2>&1); rc=$?
check "以 69 退出" "$rc" "69"
case "$out" in
  *"still not on PATH"*) ok "说清了「装好了但这个 shell 找不到它，开个新 shell」";;
  *) bad "装到 PATH 外面时没说清楚：$out";;
esac

echo
echo "== 5. 已经装了：**一个字都不碰它的命令行** =="
#
# 这是第 2 条的另一半，也是这个设计真正的边界（见 launch.go 里 runLaunch 的注释）：
# newgate 只在「这个工具不在这台机器上」时介入——问一句、装、然后照常启动。工具
# **在**的时候它是个透明启动器，argv 原样过去。
#
# 为什么这一条值得单列：`-y` 是**我们**的参数，但它只在「没装」那条路上被摘掉。
# 已经装了的时候它会被原样交给客户端（2026-09-21 实测：真 codex 报
# `unexpected argument '-y' found`）。看起来像个 bug，其实是上面那条规矩的**推论**
# ——「不碰它的命令行」和「把我们的参数摘掉」不可能同时成立。所以这里把行为钉住，
# 而不是把它改掉：要改的是那条规矩，得先想清楚「哪些参数是 newgate 的」。
cat > "$INSTALLED/codex" <<'CODEX'
#!/usr/bin/env bash
echo "codex argv: $*"
CODEX
chmod +x "$INSTALLED/codex"
out=$("$BIN" codex -y --model gpt-5 2>&1)
case "$out" in
  *"codex argv: -y --model gpt-5"*) ok "工具在的时候 argv 原样交给它（-y 也在）";;
  *) bad "argv 被我们动过了：$out";;
esac

echo
echo "结果: $PASS 通过, $FAIL 失败"
[ "$FAIL" = "0" ]
