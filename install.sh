#!/usr/bin/env bash
# newgate 的一站式安装：下载 Release 里本平台的静态产物，落到 ~/.local/bin/newgate，
# 然后告诉用户下一步敲什么。
#
#   curl -fsSL https://raw.githubusercontent.com/rzbdz/newgate-ext/main/install.sh | bash
#
# # 为什么要一个脚本（而不是让用户自己 gh release download）
#
# 「装好之后做什么」才是门槛：一个刚拿到新机器的人要的是**一条命令**把事情做完，
# 而不是先认平台名、再拼产物名、再 chmod。脚本做三件事：
#
#   1. 认平台/架构，挑出对应的产物；
#   2. 下载 + 用 Release 里的 SHA256SUMS 校验（**校验失败就当没下过**）；
#   3. 原子地换进安装目录（mv，不是 cp 覆盖——运行中的 old 进程不会被 ETXTBSY
#      卡住），然后打一句「下一步」。
#
# # 刻意不做的事
#
#   - 不 sudo、不写 /usr/local/bin（那是系统目录，装它需要人的决定）；
#   - 不改 shell rc：~/.local/bin 多数发行版已经在 PATH 上，不在的话我们**说清楚**
#     该加哪一行，而不是替人改一个启动脚本（改 rc 是那种「装完很久之后才生效、
#     而且没人记得是谁改的」的事）；
#   - 不删任何东西（覆盖同名文件前把它挪成 .prev）。
#
# 环境变量（给测试与无网络环境用）：
#   NEWGATE_VERSION   要装的 tag（默认 latest）
#   NEWGATE_INSTALL_DIR  安装目录（默认 ~/.local/bin）
#   NEWGATE_REPO      仓库（默认 rzbdz/newgate-ext）
#   NEWGATE_BIN       直接装本地这个文件（跳过下载；e2e 用它）

set -euo pipefail

REPO="${NEWGATE_REPO:-rzbdz/newgate-ext}"
VERSION="${NEWGATE_VERSION:-latest}"
INSTALL_DIR="${NEWGATE_INSTALL_DIR:-$HOME/.local/bin}"
DISTRIBUTION="default"

say()  { printf '%s\n' "$*"; }
die()  { printf 'newgate install: %s\n' "$*" >&2; exit 1; }

need() { command -v "$1" >/dev/null 2>&1 || die "this needs $1 on PATH"; }
need uname
need mkdir

# ---- 1. 平台与架构 ----
case "$(uname -s)" in
  Linux)  os=linux ;;
  Darwin) os=darwin ;;
  *) die "unsupported OS $(uname -s) (newgate ships linux and darwin builds; on Windows use WSL)" ;;
esac
case "$(uname -m)" in
  x86_64|amd64)   arch=amd64 ;;
  aarch64|arm64)  arch=arm64 ;;
  riscv64)        arch=riscv64 ;;
  *) die "unsupported architecture $(uname -m)" ;;
esac
asset="newgate-${DISTRIBUTION}-${os}-${arch}"
say "newgate installer · ${os}/${arch} · ${REPO} · ${VERSION}"

# ---- 2. 拿产物 ----
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

if [ -n "${NEWGATE_BIN:-}" ]; then
  [ -f "$NEWGATE_BIN" ] || die "NEWGATE_BIN=$NEWGATE_BIN 不存在"
  cp "$NEWGATE_BIN" "$tmp/$asset"
  say "using local binary $NEWGATE_BIN"
else
  need curl
  if [ "$VERSION" = latest ]; then
    base="https://github.com/${REPO}/releases/latest/download"
  else
    base="https://github.com/${REPO}/releases/download/${VERSION}"
  fi
  say "downloading $asset"
  curl -fsSL --retry 3 --retry-delay 1 -o "$tmp/$asset" "$base/$asset" \
    || die "cannot download $base/$asset (does that release have a $os/$arch build?)"

  # 校验和是**协议**（newgate upgrade 也读同一份）：没有它我们拒绝安装。
  # 一句话说清楚为什么：一个装到 PATH 上的二进制，来源不可验等同于给了一台机器的
  # 执行权；而这个校验的成本是几十 KB。
  if curl -fsSL --retry 3 -o "$tmp/SHA256SUMS" "$base/SHA256SUMS"; then
    want="$(awk -v f="$asset" '$2 == f { print $1 }' "$tmp/SHA256SUMS")"
    [ -n "$want" ] || die "SHA256SUMS 里没有 $asset"
    if command -v sha256sum >/dev/null 2>&1; then
      got="$(sha256sum "$tmp/$asset" | awk '{print $1}')"
    else
      got="$(shasum -a 256 "$tmp/$asset" | awk '{print $1}')"
    fi
    [ "$got" = "$want" ] || die "checksum mismatch for $asset (got $got, want $want) — refusing to install"
    say "checksum ok"
  else
    die "this release has no SHA256SUMS; refusing to install an unverifiable binary"
  fi
fi

# ---- 3. 装上 ----
mkdir -p "$INSTALL_DIR"
chmod +x "$tmp/$asset"
if [ -f "$INSTALL_DIR/newgate" ]; then
  mv -f "$INSTALL_DIR/newgate" "$INSTALL_DIR/newgate.prev" || true
  say "previous binary kept at $INSTALL_DIR/newgate.prev"
fi
# mv（rename）而不是 cp：运行中的进程占着 inode，覆盖写会 ETXTBSY。
mv -f "$tmp/$asset" "$INSTALL_DIR/newgate"
say "installed $INSTALL_DIR/newgate"

if ! printf '%s' ":$PATH:" | grep -q ":$INSTALL_DIR:"; then
  say ""
  say "$INSTALL_DIR 不在这个 shell 的 PATH 上。把它加进去（按你的 shell 选一行）："
  say "  echo 'export PATH=\"\$HOME/.local/bin:\$PATH\"' >> ~/.bashrc   # bash"
  say "  echo 'export PATH=\"\$HOME/.local/bin:\$PATH\"' >> ~/.zshrc    # zsh"
  say "  fish_add_path \$HOME/.local/bin                                # fish"
  say "然后重开一个 shell。"
fi

say ""
say "下一步："
say "  newgate config     # 选客户端、配第一个上游、一把装好"
