#!/usr/bin/env bash
# The browser-layer check: start a **throwaway** sandbox daemon, have mock/ui_check.mjs open it,
# and verify "does the UI really render, and does clicking really land on disk".
#
# It is not in CI (it needs a browser, and CI uses no node at all - see the ui_check.mjs file header).
# Same place as the e2e that needs a real token: **run it locally by a developer**.
#
#   bash mock/ui_check.sh
#
# playwright must be importable: by default it is looked up in node_modules, and if it is installed elsewhere point
# PLAYWRIGHT_MODULE at it (e.g. PLAYWRIGHT_MODULE=/path/playwright/index.mjs).
#
# The sandbox is **one-shot**: it starts a daemon in /tmp on its own, cleans up after itself, and never touches
# ~/.config/newgate, nor a running live daemon (the port is spaced away from everything else).

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
# The language is **deliberately not English**: the most valuable assertion in this check is "one language per screen" (see
# the origin of that bug in ui_check.mjs), and under the source language it is skipped entirely - running with en means
# turning that assertion off yourself. zh-Hans is in both catalogs, no network needed.
export NEWGATE_LANG=zh-Hans
# codex needs a sandbox too (added 2026-09-21): that card now has a button that **switches the takeover mode**,
# and pressing it really takes over codex's config.toml. Without pointing CODEX_HOME, one browser acceptance run
# would change **the real one on this machine**, ~/.codex/config.toml - and that is the user's hand-written config,
# with comments and [projects.*]. The first rule of every e2e is "never touch real config".
export CODEX_HOME="$SANDBOX/codex"
unset LC_ALL LC_MESSAGES LANG LANGUAGE 2>/dev/null || true
mkdir -p "$NEWGATE_HOME/mappings" "$(dirname "$BIN")" "$CODEX_HOME"
# A codex config that has **not been taken over yet**: the mode-switch button will take it over on the spot, so it has to be there.
cat > "$CODEX_HOME/config.toml" <<'TOML'
# The codex config in the sandbox, not the real one
model = "gpt-5.6-terra"
TOML

cleanup() {
  [ -n "${SRV_PID:-}" ] && kill "$SRV_PID" 2>/dev/null
  if [ -n "${NEWGATE_E2E_KEEP:-}" ]; then
    echo "sandbox kept (NEWGATE_E2E_KEEP): $SANDBOX"
  else
    rm -rf "$SANDBOX"
  fi
}
trap cleanup EXIT

echo "sandbox: $SANDBOX"
[ -x "$BIN_SRC" ] || { echo "binary not found $BIN_SRC - run build/build.sh first"; exit 1; }
cp "$BIN_SRC" "$BIN"

# Minimal config: one provider + one profile + a port. For the UI to have something to look at, there must be something to see.
cat > "$NEWGATE_HOME/providers.json" <<'JSON'
{"providers":{"demo":{"protocol":"anthropic","base_url":"http://127.0.0.1:9/demo","api_key":"sk-ui-check"}}}
JSON
cat > "$NEWGATE_HOME/mappings/demo.json" <<'JSON'
{"description":"ui check","roles":{"normal":[{"provider":"demo","model":"demo-model"}]}}
JSON
# Then add 9 more profiles: push the config section past the threshold (8) where the horizontal tab strip turns into a vertical rail,
# so the browser layer can verify the rail really appears rather than only working in a single large config.
for i in $(seq 1 9); do printf 'desc=fill profile %s\n' "$i" > "$NEWGATE_HOME/mappings/fill$i.kv"; done
printf '%s' "{\"default_profile\":\"demo\",\"port\":$PORT}" > "$NEWGATE_HOME/state.json"

"$BIN" __serve --port "$PORT" >"$SANDBOX/serve.log" 2>&1 &
SRV_PID=$!
for _ in $(seq 1 50); do
  curl -sf -o /dev/null "http://127.0.0.1:$PORT/ui/api/snapshot" && break
  sleep 0.1
done
if ! curl -sf -o /dev/null "http://127.0.0.1:$PORT/ui/api/snapshot"; then
  echo "sandbox daemon did not start, log:"; tail -20 "$SANDBOX/serve.log"; exit 1
fi

# Generate real traffic: the health table must **have a row first** before item 21 can talk about pressing that "test" button.
# demo's base points at local port 9 (discard), so this request necessarily fails to connect - which is exactly what is wanted:
# a bad binding appears in the table (the probe button's very purpose is "does this one work right now", and nothing verifies it better
# than one that is already broken).
#
# It changes the **in-memory health table**, does not persist, and does not affect the other sections (those look at files and the UI).
curl -s -o /dev/null -X POST "http://127.0.0.1:$PORT/v1/messages" \
  -H 'Content-Type: application/json' \
  -d '{"model":"normal","max_tokens":8,"messages":[{"role":"user","content":"ui check"}]}' || true

# The second argument is the sandbox's state.json: the conflict section has to change it **from outside**, playing the command line
# that wrote the file first (the only way the browser layer can verify "did the dialog really pop up").
node "$ROOT/mock/ui_check.mjs" "http://127.0.0.1:$PORT/ui/" "$NEWGATE_HOME/state.json"
