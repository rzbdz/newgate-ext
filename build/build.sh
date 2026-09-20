#!/usr/bin/env bash
# 从本发行版编出静态二进制。可以一次编**多份配置**（规格书）→ 多个二进制。
#
# 这是**唯一的构建入口**：本地怎么编、CI 怎么编、fork 的人怎么编，都是这一条命令。
# 它做四件事：
#
#   1. 检查 core/ 这个 submodule 在不在——它是内核源码，也是 go.mod 里那条
#      `replace github.com/rzbdz/newgate => ./core` 的目标；
#   2. 生成装配清单（manifest/modules_gen.go，按仓库根的**全部**规格书）；
#   3. 每份规格书 × 每个平台各编一个全静态二进制；
#   4. 拷到 dist/，附一份 SHA256SUMS。
#
# 用法
#   build/build.sh [输出目录]                 默认只编 dist.json（日常开发要的就是它）
#   NEWGATE_DISTS="dist.json dist-hello.json" 一次编多份规格书 → 多个二进制
#   NEWGATE_ALL=1                             仓库根所有 dist*.json
#   NEWGATE_DIST=dist-hello.json              单份（= NEWGATE_DISTS 里只有它）
#   NEWGATE_PLATFORMS="linux/amd64 linux/arm64"  平台矩阵（发布时由 CI 给）
#
# # 为什么要多份（2026-09-20）
#
# `dist-hello.json` 是「整个框架 + 一个 hello」的骨架配置。调试发行版机制本身时，
# 编一个这样的二进制比切到 `template` 分支去调有用得多：切分支会让主分支上的改动
# 和骨架配置没法同时验证（而「改完在两边各编一次」正是最容易漏的那一步）。多编
# 一份的代价是几秒钟，所以本地调试直接 `NEWGATE_DISTS="dist.json dist-hello.json"`。
#
# GitHub Release 只发**重点的那份**（发行版的主配置）——那是产品，骨架是给人 fork
# 的样板，不需要挂在 Release 上。这件事由 CI 的 release.yml 用 NEWGATE_DISTS 决定，
# 不在本脚本里写死：脚本只回答「怎么编」，回答「发什么」是流水线的事。
#
# # 产物名
#
#   newgate-<规格书里的 distribution>-<平台>-<架构>
#
# 例如 `newgate-default-linux-amd64`、`newgate-hello-darwin-arm64`。注意它是**多调用
# 型**的（argv0 决定入口：newgate / claude / opencode…），拿去跑之前先按正确的名字
# 落一份（见 README 与 CLAUDE.md §3）。
#
# # 与 2026-09-20 之前那版的区别
#
# 旧版写一张临时 Pin、然后 `cd core && make static`——真正的组装机制住在内核
# 仓库里，代价是编一次发行版就把**内核 checkout 改脏**（生成的清单被重写成发行版
# 那份），而且编的是发行版的已提交状态（改完必须先 commit）。
#
# 现在发行版是**自己的 Go module**：编的是自己的 main，内核只是它的一份依赖
# （replace 到 submodule）。于是三件事一起消失：内核工作区不再被改写、构建读的是
# 工作区（不必先 commit）、发行版的测试与内核的测试各跑各的。
set -euo pipefail

here=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
out=${1:-$here/dist}
# 输出目录**先转成绝对路径**：脚本中途要 `cd "$gomod"`，那之后的相对路径会指到别处。
# 2026-09-20 CI 实测踩过：`build/build.sh dist` 把产物写进了 `dist/`，而端到端脚本
# 在仓库根找它，报「找不到二进制」——构建那个 job 还是绿的，所以红在隔壁那步。
# 相对路径按**调用者当时的工作目录**解释（不是仓库根，也不是脚本自己所在的那层）。
case "$out" in
  /*) : ;;
  *)  out="$(pwd)/$out" ;;
esac
core=$here/core
gomod=$here

# 编哪几份。三个开关的优先顺序：全都要 > 点名几份 > 就一份（默认 dist.json）。
if [ -n "${NEWGATE_ALL:-}" ]; then
  specs=$(cd "$here" && ls dist*.json)
elif [ -n "${NEWGATE_DISTS:-}" ]; then
  specs=${NEWGATE_DISTS}
else
  specs=${NEWGATE_DIST:-dist.json}
fi

if [ ! -f "$core/go.mod" ]; then
  echo "core/ 里没有内核源码。它是个 submodule，先：" >&2
  echo "  git submodule update --init --recursive" >&2
  exit 1
fi
for s in $specs; do
  [ -f "$here/$s" ] || { echo "找不到规格书 $here/$s" >&2; exit 1; }
done

# 内核的提交**推上去了才算数**。submodule 平时是 detached 的：在 core/ 里改完、
# 提交、忘了 `git push`，编出来的二进制就基于一个只存在于本机的提交——别人拿到
# （CI 拿到）的是一个指向不存在提交的指针，发行版的流水线会在 checkout 那一步
# **整体**死掉：`fatal: remote error: upload-pack: not our ref`（2026-09-20 实测）。
#
# 只警告不报错：本地开发时从「还没推的内核提交」编一份出来是合理的（正是要试它），
# 但必须说出来——那份产物谁也复现不了，推上去的那一刻 CI 就会替你说。
if core_head=$(git -C "$core" rev-parse --verify -q HEAD); then
  if [ -z "$(git -C "$core" for-each-ref --contains "$core_head" --format='%(refname)' refs/remotes/)" ]; then
    {
      echo "⚠ core/ 的 HEAD ${core_head:0:7} 不在任何**已获取的**远端分支上。"
      echo "  要么忘了 push（git -C core push origin main），要么本地没 fetch"
      echo "  （git -C core fetch）。这份产物别人复现不了，CI 也过不了 checkout。"
    } >&2
  fi
fi

# 版本号带上工作区状态。旧版在工作区脏时只**警告一句**，而二进制的版本仍然记成
# 那个提交号——两个说法对不上。现在直接写进二进制：`newgate version` 自己会说
# 「这是 dirty 的」，谎报不了。
rev=$(git -C "$here" describe --tags --always --dirty 2>/dev/null || echo unknown)
if [ -n "$(git -C "$here" status --porcelain -- go 'dist*.json')" ]; then
  echo "⚠ 发行版有未提交的改动：版本号带 -dirty，内容与任何提交都不完全一致" >&2
fi

echo "── 内核：$core @ $(git -C "$core" rev-parse --short HEAD)"
echo "── 发行版规格书：$specs"

# 清单只生成一次：distgen 扫的是仓库根的全部规格书，一次就写全（选哪份是链接期的事）。
cd "$gomod"
go run ./tools/distgen

mkdir -p "$out"
build_time=$(date '+%Y-%m-%d_%H:%M:%S%z')
commit_time=$(git -C "$here" log -1 --date=format:'%Y-%m-%d_%H:%M:%S%z' --format=%cd 2>/dev/null || echo unknown)

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
# uname -m 给的是机器自己的叫法，Go 用的是另一套：x86_64 → amd64、aarch64 → arm64。
# 不映射的话**默认这条路**（不给 NEWGATE_PLATFORMS）会直接失败：
#   go: unsupported GOOS/GOARCH pair linux/x86_64
# 2026-09-20 实测踩过——上一版脚本只把 uname -m 用在文件名上，所以从没暴露。
case "$(uname -m)" in
  x86_64)        host_arch=amd64 ;;
  aarch64|arm64) host_arch=arm64 ;;
  i386|i686)     host_arch=386 ;;
  *)             host_arch=$(uname -m) ;;
esac
platforms=${NEWGATE_PLATFORMS:-"$host_os/$host_arch"}

failed=""
host_artifacts=""
for spec in $specs; do
  dist=$(python3 -c 'import json,sys;print(json.load(open(sys.argv[1]))["distribution"])' "$here/$spec")
  # 规格书里的 distribution 同时是**版本号的前缀**和产物名里的那一段。两者是同一个
  # 事实，所以从同一处读——手工拼一个产物名去跑 e2e，最容易在这里对不上。
  ldflags_common="-s -w -X main.version=$dist-$rev -X main.buildTime=$build_time"
  ldflags_common="$ldflags_common -X main.commitTime=$commit_time -X main.spec=$spec"
  for p in $platforms; do
    goos=${p%/*}; goarch=${p#*/}
    artifact="$out/newgate-$dist-$goos-$goarch"
    ldflags="$ldflags_common"
    if [ "$goos" = linux ]; then
      ldflags="$ldflags -extldflags '-static'"
    fi
    if build_one "$goos" "$goarch" "$artifact" "$ldflags"; then
      echo "  ok  $spec  $goos/$goarch  $artifact"
    else
      echo "  --  $spec  $goos/$goarch  编不出来（本机工具链不支持这个目标？）" >&2
      failed="$failed $spec:$goos/$goarch"
      rm -f "$artifact"
    fi
    if [ "$goos" = "$host_os" ] && [ "$goarch" = "$host_arch" ]; then
      host_artifacts="$host_artifacts $artifact"
    fi
  done
done

# 本机平台的静态断言：写坏一处引号就会退化成动态链接，而那个症状要到**另一台
# 机器上**才出现——所以在这里就红。（交叉编出来的没法用 ldd 看，跳过。）
#
# 先取输出再判断，不要写成 `ldd … | grep -q`：脚本开着 pipefail，而 ldd 对静态
# 二进制**返回非零**（"not a dynamic executable" 是它的失败输出），管道于是整体
# 算失败——正好把唯一成功的那一格判成失败。2026-09-20 实测踩过。
if [ "$host_os" = linux ] && command -v ldd >/dev/null; then
  for artifact in $host_artifacts; do
    [ -x "$artifact" ] || continue
    ldd_out=$(ldd "$artifact" 2>&1 || true)
    case "$ldd_out" in
      *"not a dynamic executable"*|*"statically linked"*) : ;;
      *)
        echo "本机产物 $artifact 不是静态二进制（跨机器会起不来）：" >&2
        echo "$ldd_out" >&2
        exit 1
        ;;
    esac
  done
fi

if [ -n "$failed" ]; then
  echo "这些组合没编出来:$failed" >&2
  exit 1
fi

( cd "$out" && sha256sum newgate-* > SHA256SUMS 2>/dev/null || shasum -a 256 newgate-* > SHA256SUMS )
ls -l "$out"
