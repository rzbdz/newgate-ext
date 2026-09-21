#!/usr/bin/env bash
# 浏览器那一层检查：起一个**丢弃式**的沙箱 daemon，让 mock/ui_check.mjs 打开它，
# 验「界面真的渲染出来了吗、点了真的落盘吗」。
#
# 它不进 CI（要一个浏览器，而 CI 完全不用 node——见 ui_check.mjs 的文件头）。
# 与那条要真 token 的 e2e 同一个位置：**开发者本地跑**。
#
#   bash mock/ui_check.sh
#
# 需要 playwright 可被 import：默认从 node_modules 找，装在别处就用
# PLAYWRIGHT_MODULE 指过去（例：PLAYWRIGHT_MODULE=/path/playwright/index.mjs）。
#
# 沙箱是**一次性**的：它在 /tmp 里自己起一个 daemon、自己收尸，绝不碰
# ~/.config/newgate，也绝不碰跑着的线上 daemon（端口与别处错开）。

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
SANDBOX="${NEWGATE_UI_SANDBOX:-$(mktemp -d /tmp/newgate-ui.XXXXXX)}"
PORT="${NEWGATE_UI_PORT:-18909}"
BIN="$SANDBOX/bin/newgate"

export NEWGATE_HOME="$SANDBOX/ng"
# 语言**故意不是英文**：这个检查里最值钱的一条是「一屏只有一种语言」（见
# ui_check.mjs 里那个 bug 的由来），而它在源语言下整条被跳过——用 en 跑等于
# 自己把那条关掉。zh-Hans 两份目录里都有，不必联网。
export NEWGATE_LANG=zh-Hans
# codex 也要沙箱（2026-09-21 加）：那张卡上现在有一个**切换接管模式**的按钮，
# 按下去会真的去接管 codex 的 config.toml。不指 CODEX_HOME 的话，一次浏览器验收
# 就会去改**这台机器上真正那份** ~/.codex/config.toml——而那是用户手写的配置，
# 有注释、有 [projects.*]。所有 e2e 的第一条规矩就是「不碰真实配置」。
export CODEX_HOME="$SANDBOX/codex"
unset LC_ALL LC_MESSAGES LANG LANGUAGE 2>/dev/null || true
mkdir -p "$NEWGATE_HOME/mappings" "$(dirname "$BIN")" "$CODEX_HOME"
# 一份**还没被接管过**的 codex 配置：切模式那个按钮会当场接管它，所以它得在。
cat > "$CODEX_HOME/config.toml" <<'TOML'
# 沙箱里的 codex 配置，别当真
model = "gpt-5.6-terra"
TOML

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

# 最小配置：一个 provider + 一个 profile + 端口。界面要有的看，就要有东西可看。
cat > "$NEWGATE_HOME/providers.json" <<'JSON'
{"providers":{"demo":{"protocol":"anthropic","base_url":"http://127.0.0.1:9/demo","api_key":"sk-ui-check"}}}
JSON
cat > "$NEWGATE_HOME/mappings/demo.json" <<'JSON'
{"description":"ui check","roles":{"normal":[{"provider":"demo","model":"demo-model"}]}}
JSON
# 再塞 9 个变化 profile：把 config 一节顶过「横 tab 条转竖栏」的阈值（8），
# 这样浏览器那层才能验到竖栏真的出现而不是只在单机大配置里有效。
for i in $(seq 1 9); do printf 'desc=填充 profile %s\n' "$i" > "$NEWGATE_HOME/mappings/fill$i.kv"; done
printf '%s' "{\"default_profile\":\"demo\",\"port\":$PORT}" > "$NEWGATE_HOME/state.json"

"$BIN" __serve --port "$PORT" >"$SANDBOX/serve.log" 2>&1 &
SRV_PID=$!
for _ in $(seq 1 50); do
  curl -sf -o /dev/null "http://127.0.0.1:$PORT/ui/api/snapshot" && break
  sleep 0.1
done
if ! curl -sf -o /dev/null "http://127.0.0.1:$PORT/ui/api/snapshot"; then
  echo "沙箱 daemon 没起来，日志："; tail -20 "$SANDBOX/serve.log"; exit 1
fi

# 造一条真实流量：健康表里**得先有一行**，第 21 条才谈得上按那个「测试」按钮。
# demo 的 base 指向本地 9 端口（discard），所以这一发必然连不上——正是要的：
# 表上出现一条坏 binding（探活按钮的作用恰恰是「这条现在到底通不通」，没有比
# 一条已经坏掉的更能验它了）。
#
# 它改的是**内存里的健康表**，不落盘、不影响别的段（那些段看的是文件与界面）。
curl -s -o /dev/null -X POST "http://127.0.0.1:$PORT/v1/messages" \
  -H 'Content-Type: application/json' \
  -d '{"model":"normal","max_tokens":8,"messages":[{"role":"user","content":"ui check"}]}' || true

# 第二个参数是沙箱的 state.json：冲突那一段要**从外面**改它，扮演那个抢先写文件
# 的命令行（浏览器那一层唯一能验「对话框真的弹出来了吗」的办法）。
node "$ROOT/mock/ui_check.mjs" "http://127.0.0.1:$PORT/ui/" "$NEWGATE_HOME/state.json"
