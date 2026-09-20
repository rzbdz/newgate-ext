#!/usr/bin/env bash
# 发行版自己的构建脚本：从核心仓库的一份源码编出**本发行版**的静态二进制。
#
# 为什么这个脚本在发行版仓库里而不在核心仓库里：它回答的是「我的产品怎么造出来」，
# 而这个问题的主人只有发行版作者。核心仓库里那份默认 Pin 是**默认发行版**的配置，
# 不是所有发行版的——fork 一个 ext 仓库就该拥有一个自己的发行版，包括自己的造法。
#
# 用法
#   build/release.sh [输出目录]          默认 dist/
#   NEWGATE_CORE=/path/to/newgate        用本机已有的核心源码（不 clone）
#   NEWGATE_DIST=dist-simple-cli.json    换一份规格书（同一个仓库里的变体）
#
# 它做四件事：
#   1. 读自己的规格书（本仓库根的 $NEWGATE_DIST，默认 dist.json）；
#   2. 拿一份核心源码：$NEWGATE_CORE 指了就用它，否则按规格书里的 core 钉的版本 clone；
#   3. 写一张 Pin 指向**本仓库的当前提交**，用 NEWGATE_MODULES_PIN 交给构建
#      （不动核心仓库根那份默认 Pin——那是别人的配置）；
#   4. make static，把二进制拷出来。
set -euo pipefail

here=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
out=${1:-$here/dist}
spec_name=${NEWGATE_DIST:-dist.json}
spec="$here/$spec_name"
[ -f "$spec" ] || { echo "找不到规格书 $spec" >&2; exit 1; }

read -r dist core_repo core_rev < <(python3 - "$spec" <<'PY'
import json, sys
d = json.load(open(sys.argv[1]))
c = d.get("core") or {}
print(d.get("distribution") or "newgate", c.get("repo") or "https://github.com/rzbdz/newgate.git", c.get("revision") or "")
PY
)

# 本次构建用的发行版提交：工作区脏就明说，免得编出来的东西说不清是哪一版。
rev=$(git -C "$here" rev-parse HEAD)
if [ -n "$(git -C "$here" status --porcelain)" ]; then
  echo "⚠ $here 有未提交的改动：本次二进制的发行版版本按 $rev 记，但实际内容与它不一致" >&2
fi

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

if [ -n "${NEWGATE_CORE:-}" ]; then
  core=$NEWGATE_CORE
  echo "── 核心源码：$core（NEWGATE_CORE，不 clone）"
else
  core="$work/newgate"
  if [ -n "$core_rev" ]; then
    echo "── 核心源码：clone $core_repo @ ${core_rev:0:7}"
    git clone --quiet "$core_repo" "$core"
    git -C "$core" checkout --quiet "$core_rev"
  else
    echo "── 核心源码：clone $core_repo（规格书没钉 core 版本，用默认分支）"
    git clone --quiet "$core_repo" "$core"
  fi
fi

cat > "$work/pin.json" <<EOF
{
  "repo": "file://$here",
  "revision": "$rev",
  "spec": "$spec_name"
}
EOF

echo "── 发行版：$dist @ ${rev:0:7}（规格书 $spec_name）"
mkdir -p "$out"
( cd "$core/go" && NEWGATE_MODULES_PIN="$work/pin.json" make static >/dev/null )
cp "$core/go/bin/newgate" "$out/newgate-$dist-$(uname -s | tr 'A-Z' 'a-z')-$(uname -m)"
ls -l "$out"
