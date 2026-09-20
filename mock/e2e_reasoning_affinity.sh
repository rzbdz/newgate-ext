#!/usr/bin/env bash
# Real-upstream regression for DeepSeek taking over a foreign tool loop.
#
# 2026-09-20 从内核仓库搬过来：它断言的是**本发行版那个模块**的补丁契约
# （deepseek 插件的 RebaseToolLoop），住在内核那边等于让内核的测试依赖一个
# 发行版模块。内核侧的同类断言现在由 mock/e2e_claude.sh 用内核自己的
# claude-bg / schema-repair 当探针。
#
# This is intentionally NOT part of `go test ./...`: unit tests must never use
# the network. It spends a few real tokens through the running newgate daemon.
#
# Flow:
#   1. Ask SOURCE_PROFILE to emit a real tool_use.
#   2. Send its exact assistant content plus tool_result through the default
#      route (no /p/<profile> override).
#   3. The default chain starts with DeepSeek. Assert its special hook rebases
#      the foreign unfinished tool loop and keeps the original fallback order.
#
# Usage:
#   bash mock/e2e_reasoning_affinity.sh
#   SOURCE_PROFILE=ark NEWGATE_URL=http://127.0.0.1:8899 \
#     bash mock/e2e_reasoning_affinity.sh
set -euo pipefail

BASE_URL="${NEWGATE_URL:-http://127.0.0.1:8899}"
SOURCE_PROFILE="${SOURCE_PROFILE:-ark}"
MODEL="${MODEL:-normal}"
EXPECTED_TARGET="${EXPECTED_TARGET:-smt-deepseek/deepseek-flash}"
TMP="$(mktemp -d /tmp/newgate-reasoning-affinity.XXXXXX)"
trap 'rm -rf "$TMP"' EXIT

post() {
  local url=$1 request=$2 headers=$3 response=$4
  curl -sS -D "$headers" -o "$response" -w '%{http_code}' \
    -X POST "$url" \
    -H 'Content-Type: application/json' \
    --data-binary @"$request"
}

route() {
  command grep -i '^X-Newgate-Route:' "$1" |
    head -n 1 |
    sed -e 's/^[^:]*:[[:space:]]*//' -e 's/\r$//'
}

jq -n --arg model "$MODEL" '{
  model: $model,
  max_tokens: 128,
  stream: false,
  thinking: {type: "adaptive"},
  messages: [{
    role: "user",
    content: "Call the echo tool exactly once with value x."
  }],
  tools: [{
    name: "echo",
    description: "Echo a value",
    input_schema: {
      type: "object",
      properties: {value: {type: "string"}},
      required: ["value"]
    }
  }]
}' >"$TMP/turn1.request.json"

status1=$(post \
  "$BASE_URL/p/$SOURCE_PROFILE/v1/messages" \
  "$TMP/turn1.request.json" \
  "$TMP/turn1.headers" \
  "$TMP/turn1.response.json")
if [[ "$status1" != 200 ]]; then
  echo "turn 1 failed: HTTP $status1" >&2
  jq -c . "$TMP/turn1.response.json" >&2
  exit 1
fi

tool_id=$(jq -r '[.content[]? | select(.type == "tool_use") | .id][0] // empty' \
  "$TMP/turn1.response.json")
if [[ -z "$tool_id" ]]; then
  echo "turn 1 did not produce tool_use" >&2
  jq -c '{stop_reason,content,error}' "$TMP/turn1.response.json" >&2
  exit 1
fi

jq -n \
  --arg model "$MODEL" \
  --arg tool_id "$tool_id" \
  --slurpfile response "$TMP/turn1.response.json" '{
    model: $model,
    max_tokens: 16,
    stream: false,
    thinking: {type: "adaptive"},
    messages: [
      {
        role: "user",
        content: "Call the echo tool exactly once with value x."
      },
      {
        role: $response[0].role,
        content: $response[0].content
      },
      {
        role: "user",
        content: [{
          type: "tool_result",
          tool_use_id: $tool_id,
          content: "x"
        }]
      }
    ],
    tools: [{
      name: "echo",
      description: "Echo a value",
      input_schema: {
        type: "object",
        properties: {value: {type: "string"}},
        required: ["value"]
      }
    }]
  }' >"$TMP/turn2.request.json"

status2=$(post \
  "$BASE_URL/v1/messages" \
  "$TMP/turn2.request.json" \
  "$TMP/turn2.headers" \
  "$TMP/turn2.response.json")
if [[ "$status2" != 200 ]]; then
  echo "turn 2 failed: HTTP $status2" >&2
  jq -c . "$TMP/turn2.response.json" >&2
  exit 1
fi

route2=$(route "$TMP/turn2.headers")
if [[ "$route2" != *"$EXPECTED_TARGET"* ]]; then
  echo "DeepSeek rebase did not preserve the default route: turn2='$route2'" >&2
  exit 1
fi

echo "PASS: foreign tool loop was rebased onto $route2"
