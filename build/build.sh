#!/usr/bin/env bash
# 从本发行版编出一个静态二进制。
#
# 这是**唯一的构建入口**：本地怎么编、CI 怎么编、fork 的人怎么编，都是这一条命令。
# 它做四件事：
#
#   1. 检查 core/ 这个 submodule 在不在（内核源码钉在那一发上）；
#   2. 写一张 Pin，指向**本仓库的当前提交**，用 NEWGATE_MODULES_PIN 交给内核的构建
#      ——核心仓库根那份 modules-ext.json 是**默认发行版**的配置，不是我们的，
#      所以这里不碰它，用环境变量指一份临时的；
#   3. 在内核里 make static；
#   4. 把二进制拷到 dist/。
#
# 用法
#   build/build.sh [输出目录]              默认 dist/
#   NEWGATE_DIST=dist-simple-cli.json      换一份规格书（同一个仓库的变体）
set -euo pipefail

here=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
out=${1:-$here/dist}
spec_name=${NEWGATE_DIST:-dist.json}
core=$here/core

[ -f "$here/$spec_name" ] || { echo "找不到规格书 $here/$spec_name" >&2; exit 1; }
if [ ! -f "$core/go/go.mod" ]; then
  echo "core/ 里没有内核源码。它是个 submodule，先：" >&2
  echo "  git submodule update --init --recursive" >&2
  exit 1
fi

dist=$(python3 -c 'import json,sys;print(json.load(open(sys.argv[1]))["distribution"])' "$here/$spec_name")

# 本次构建用的发行版提交。工作区脏就明说——编出来的东西说不清是哪一版，比编不出来更糟。
rev=$(git -C "$here" rev-parse HEAD)
if [ -n "$(git -C "$here" status --porcelain -- modules testing "$spec_name")" ]; then
  echo "⚠ 发行版有未提交的改动：二进制的版本按 $rev 记，但内容与它不一致" >&2
fi

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
cat > "$work/pin.json" <<EOF
{ "repo": "file://$here", "revision": "$rev", "spec": "$spec_name" }
EOF

echo "── 内核：$core @ $(git -C "$core" rev-parse --short HEAD)"
echo "── 发行版：$dist @ ${rev:0:7}（规格书 $spec_name）"
mkdir -p "$out"
( cd "$core/go" && NEWGATE_MODULES_PIN="$work/pin.json" make static >/dev/null )
cp "$core/go/bin/newgate" "$out/newgate-$dist-$(uname -s | tr 'A-Z' 'a-z')-$(uname -m)"
ls -l "$out"
