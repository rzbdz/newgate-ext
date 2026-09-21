#!/usr/bin/env bash
# 建公开站：`site/src` 的 markdown → `site/dist`，外加那个可点的界面演示。
#
# # 为什么要有这个脚本（而不是让人敲两条命令）
#
# 两条命令**有先后**，而且反过来是坏的：sitegen 每次都把 `site/dist` 整个删掉重来
# （理由见 tools/sitegen/main.go 的 emit——覆盖写会让删掉的页面留一份只有搜索引擎
# 找得到的孤儿），而演示页的产物正落在 `site/dist/demo`。先建演示、再跑 sitegen，
# 结果就是演示页被删掉，而站点上那个链接 404——**站点本身是好好的**，所以本地看
# 不出问题，只有点进演示才发现。
#
# 顺序写在一个地方，就不会有人记错。
#
# # 它为什么要调 node
#
# 只有演示页那一半要（vite + svelte + codemirror，全在前端那个 package.json 里）。
# 纯文档那一半是零依赖的 Go——所以 `go run ./tools/sitegen` 单独跑永远是离线可用的，
# 这正是「构建必须离线」那条规矩要保住的东西。
#
# 用法：
#
#	bash site/build.sh            # 站点 + 演示页 → site/dist（按 Pages 那层前缀）
#	bash site/build.sh --local    # 同上，但按「从根服务」建，给本地预览用
#	bash site/build.sh --docs     # 只要站点（不碰前端工具链）
set -euo pipefail

root=$(cd "$(dirname "$0")/.." && pwd)

docs_only=0
local=0
for arg in "$@"; do
  case "$arg" in
    --docs) docs_only=1 ;;
    --local) local=1 ;;
    *) echo "不认识的参数：$arg" >&2; exit 2 ;;
  esac
done

# 站点生成器与演示页那两个 base 必须是同一个数：演示页住在站点根下的 /demo/，
# 两个指岔了的结果是演示页的 index.html 找得到、它引的 js 找不到（或反过来）——
# 而在浏览器里那两种情况长得几乎一样（白屏）。所以**只留一个口子**
# （SITEGEN_BASE，与 tools/sitegen 的 basePath 同一个变量），演示页那一节从它推。
if [ "$local" = "1" ]; then
  export SITEGEN_BASE=/
fi

echo "── sitegen：site/src → site/dist"
(cd "$root" && go run ./tools/sitegen)

if [ "$docs_only" = "1" ]; then
  exit 0
fi

echo "── 演示页：同一份前端的第二个入口 → site/dist/demo"
if ! command -v pnpm >/dev/null 2>&1; then
  {
    echo "找不到 pnpm：演示页建不了。"
    echo "站点本身已经建好了（site/dist），只是没有 demo/。"
    echo "装上前端工具链（corepack enable pnpm）再跑一次，或者用 --docs 明确表示只要文档。"
  } >&2
  exit 1
fi
web="$root/modules/web-dashboard/web"
[ -d "$web/node_modules" ] || (cd "$web" && pnpm install)
(cd "$web" && pnpm build:demo)

echo
echo "站点在 $root/site/dist"
echo "本地看：python3 -m http.server -d $root/site/dist 8801"
