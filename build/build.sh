#!/usr/bin/env bash
# 从本发行版编出一个静态二进制。
#
# 这是**唯一的构建入口**：本地怎么编、CI 怎么编、fork 的人怎么编，都是这一条命令。
# 它做四件事：
#
#   1. 检查 core/ 这个 submodule 在不在——它是内核源码，也是 go.mod 里那条
#      `replace github.com/rzbdz/newgate/go => ../core/go` 的目标；
#   2. 生成装配清单（go/manifest/modules_gen.go，按仓库根的规格书）；
#   3. 编一个全静态二进制；
#   4. 拷到 dist/。
#
# 用法
#   build/build.sh [输出目录]              默认 dist/
#   NEWGATE_DIST=dist-simple-cli.json      换一份规格书（同一个仓库的变体）
#
# # 与 2026-09-20 之前那版的区别
#
# 旧版写一张临时 Pin、然后 `cd core/go && make static`——真正的组装机制住在内核
# 仓库里，代价是编一次发行版就把**内核 checkout 改脏**（生成的清单被重写成发行版
# 那份），而且编的是发行版的已提交状态（改完必须先 commit）。
#
# 现在发行版是**自己的 Go module**：编的是自己的 main，内核只是它的一份依赖
# （replace 到 submodule）。于是三件事一起消失：内核工作区不再被改写、构建读的是
# 工作区（不必先 commit）、发行版的测试与内核的测试各跑各的。
set -euo pipefail

here=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
out=${1:-$here/dist}
spec_name=${NEWGATE_DIST:-dist.json}
core=$here/core
gomod=$here/go

[ -f "$here/$spec_name" ] || { echo "找不到规格书 $here/$spec_name" >&2; exit 1; }
if [ ! -f "$core/go/go.mod" ]; then
  echo "core/ 里没有内核源码。它是个 submodule，先：" >&2
  echo "  git submodule update --init --recursive" >&2
  exit 1
fi

dist=$(python3 -c 'import json,sys;print(json.load(open(sys.argv[1]))["distribution"])' "$here/$spec_name")

# 版本号带上工作区状态。旧版在工作区脏时只**警告一句**，而二进制的版本仍然记成
# 那个提交号——两个说法对不上。现在直接写进二进制：`newgate version` 自己会说
# 「这是 dirty 的」，谎报不了。
rev=$(git -C "$here" describe --tags --always --dirty 2>/dev/null || echo unknown)
if [ -n "$(git -C "$here" status --porcelain -- go dist.json dist-simple-cli.json)" ]; then
  echo "⚠ 发行版有未提交的改动：版本号带 -dirty，内容与任何提交都不完全一致" >&2
fi

echo "── 内核：$core @ $(git -C "$core" rev-parse --short HEAD)"
echo "── 发行版：$dist @ $rev（规格书 $spec_name）"

cd "$gomod"
go run ./tools/distgen

mkdir -p "$out"
build_time=$(date '+%Y-%m-%d_%H:%M:%S%z')
commit_time=$(git -C "$here" log -1 --date=format:'%Y-%m-%d_%H:%M:%S%z' --format=%cd 2>/dev/null || echo unknown)
ldflags_common="-s -w -X main.version=$dist-$rev -X main.buildTime=$build_time"
ldflags_common="$ldflags_common -X main.commitTime=$commit_time -X main.spec=$spec_name"

# 编一个平台。linux 额外要求全静态（跨机器部署靠它：snode1 那台 glibc 旧，
# 动态链接的二进制直接起不来）。darwin 不要求——macOS 上不存在「全静态」这种
# 东西，CGO_ENABLED=0 才是那条保证。
build_one() {
  local goos=$1 goarch=$2 artifact=$3 ldflags=$4
  CGO_ENABLED=0 GOOS="$goos" GOARCH="$goarch" go build -trimpath -tags netgo,osusergo \
    -ldflags "$ldflags" -o "$artifact" ./cmd/newgate
}

# 平台矩阵：默认只编本机（日常开发要的就是它）。发布时由 CI 给一份清单，
# 例如 NEWGATE_PLATFORMS="linux/amd64 linux/arm64 darwin/arm64"。
host_os=$(uname -s | tr 'A-Z' 'a-z')
host_arch=$(uname -m)
platforms=${NEWGATE_PLATFORMS:-"$host_os/$host_arch"}

failed=""
for p in $platforms; do
  goos=${p%/*}; goarch=${p#*/}
  artifact="$out/newgate-$dist-$goos-$goarch"
  ldflags="$ldflags_common"
  if [ "$goos" = linux ]; then
    ldflags="$ldflags -extldflags '-static'"
  fi
  if build_one "$goos" "$goarch" "$artifact" "$ldflags"; then
    echo "  ok  $goos/$goarch  $artifact"
  else
    echo "  --  $goos/$goarch  编不出来（本机工具链不支持这个目标？）" >&2
    failed="$failed $goos/$goarch"
    rm -f "$artifact"
  fi
done

# 本机平台的静态断言：写坏一处引号就会退化成动态链接，而那个症状要到**另一台
# 机器上**才出现——所以在这里就红。（交叉编出来的没法用 ldd 看，跳过。）
#
# 先取输出再判断，不要写成 `ldd … | grep -q`：脚本开着 pipefail，而 ldd 对静态
# 二进制**返回非零**（"not a dynamic executable" 是它的失败输出），管道于是整体
# 算失败——正好把唯一成功的那一格判成失败。2026-09-20 实测踩过。
host_artifact="$out/newgate-$dist-$host_os-$host_arch"
if [ "$host_os" = linux ] && [ -x "$host_artifact" ] && command -v ldd >/dev/null; then
  ldd_out=$(ldd "$host_artifact" 2>&1 || true)
  case "$ldd_out" in
    *"not a dynamic executable"*|*"statically linked"*) : ;;
    *)
      echo "本机产物不是静态二进制（跨机器会起不来）：" >&2
      echo "$ldd_out" >&2
      exit 1
      ;;
  esac
fi

if [ -n "$failed" ]; then
  echo "这些平台没编出来:$failed" >&2
  exit 1
fi

( cd "$out" && sha256sum newgate-$dist-* > SHA256SUMS 2>/dev/null || shasum -a 256 newgate-$dist-* > SHA256SUMS )
ls -l "$out"
