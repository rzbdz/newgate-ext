<div align="center">

# newgate-ext

### A [newgate](https://github.com/rzbdz/newgate) distribution — wired for Claude Code and OpenCode

The kernel does mechanism. **This repo does product**: which modules ship, whose
upstream quirks get patched, and in what order they run.

[![CI](https://github.com/rzbdz/newgate-ext/actions/workflows/ci.yml/badge.svg?branch=main)](https://github.com/rzbdz/newgate-ext/actions/workflows/ci.yml)
![release](https://github.com/rzbdz/newgate-ext/actions/workflows/release.yml/badge.svg)
![Go](https://img.shields.io/badge/go-1.27-00ADD8?logo=go&logoColor=white)
![dependencies](https://img.shields.io/badge/third--party_deps-0-brightgreen)
![platforms](https://img.shields.io/badge/platforms-linux%20%7C%20darwin-informational)

</div>

---

## Install

Static, single-file, no runtime dependencies. Grab the binary for your platform
from the [latest release](https://github.com/rzbdz/newgate-ext/releases/latest):

```bash
gh release download -p 'newgate-*-linux-amd64' -p SHA256SUMS
sha256sum -c SHA256SUMS --ignore-missing
chmod +x newgate-default-linux-amd64      # GitHub does not preserve the exec bit
mv newgate-default-linux-amd64 ~/.local/bin/newgate
newgate version
```

**The rename is not cosmetic.** The binary is multi-call: `argv[0]` decides which
entry claims the invocation (`newgate`, `claude`, `opencode`, …). Run it under
any other name and it cannot tell who it is.

Then point a client at it:

```bash
newgate init          # scaffold config
newgate start         # daemon on 127.0.0.1:8899
newgate status
```

## What's in the box

| module | what it owns |
| --- | --- |
| `tui` | menuconfig-style terminal UI — edit profiles and tier bindings |
| `deepseek` | DeepSeek family: reasoning content save/backfill, tail-shape repair, cross-provider tool-loop migration |
| `glm` | GLM family: reasoning hand-back and the default thinking switch |
| `claudecode` | Claude Code as a client: model slots, env contract, background calls |
| `claudecode_deepseek` / `claudecode_glm` | behaviour that only exists at the *cross point* of that client and that model |
| `opencode` | OpenCode as a client |
| `opencodeomo` | Oh My OpenAgent as an optional OpenCode extension |
| `configshare` | multi-machine config sharing — **a library, not yet a module**: no `New()`, not in any spec, in no binary |
| `simple-cli` | a minimal UI sample: renders `status`, ignores the rest |
| `hello` | the smallest module there is, and the entire skeleton distribution |

Everything else — the gateway, the breaker, config, runtime, the CLI — comes
from the kernel at `core/`.

## Three binaries, one repo

A **spec** (`dist*.json`) says which modules go in and which kernel modules stay
out. `build/build.sh` compiles one binary per spec.

| spec | distribution | what it is |
| --- | --- | --- |
| `dist.json` | `default` | **the flagship** — everything above, kernel `cli` + `tui` |
| `dist-simple-cli.json` | `simple-cli` | same gateway, kernel `cli` swapped for `simple-cli` |
| `dist-hello.json` | `hello` | the skeleton: the whole framework + one `hello` module |

```jsonc
// dist-hello.json — "none of the kernel's modules, just mine"
{ "distribution": "hello", "modules": ["hello"], "disable": ["*"] }
```

`"disable": ["*"]` reads as **all of the kernel's own modules**. Spelling the
directory names out instead would be a copy of the kernel's module table, and a
copy rots: add a module to the kernel and a list-everything distribution silently
grows it. `*` grows with the kernel — by not growing at all.

## Build

```bash
git clone --recursive git@github.com:rzbdz/newgate-ext.git && cd newgate-ext
build/build.sh                    # → dist/newgate-default-<os>-<arch>

NEWGATE_ALL=1 build/build.sh      # every spec in the repo → several binaries
NEWGATE_DISTS="dist.json dist-hello.json" build/build.sh out/
NEWGATE_DIST=dist-simple-cli.json build/build.sh out/
NEWGATE_PLATFORMS="linux/amd64 linux/arm64 darwin/arm64" build/build.sh dist/
```

Artifacts are named `newgate-<distribution>-<os>-<arch>`, and `build/build.sh` is
the *only* entry point — local, CI, and release all go through it. It checks the
`core/` submodule, regenerates the assembly manifest, cross-compiles fully static
binaries, asserts each host artifact really is static (`ldd`), and writes
`SHA256SUMS`.

> Build a skeleton binary to debug the framework: `NEWGATE_ALL=1` produces
> `newgate-hello-linux-amd64`, where `newgate` is a hello world. No branch
> switching, no stashing.

## Test

Two suites, and the distribution's pipeline runs the kernel's **first**:

```bash
cd core && GOPROXY=off go test ./...                    # core-test: the kernel, offline
gofmt -l . && go vet ./... && go run ./tools/distgen -check && go test ./...   # dist-test
build/build.sh dist && bash mock/e2e_claude.sh          # e2e: real binary, fake upstream
```

- `core-test` proves the kernel still holds on its own — the kernel needs no
  network and no second repository to test itself.
- `dist-test` proves *this* repo's modules and specs assemble.
- The three end-to-end scripts (`mock/e2e_claude.sh`, `mock/e2e_opencode.sh`,
  `mock/e2e_claude_dist.sh`, 112 assertions together) drive the real binary
  against a byte-exact fake upstream. **Zero tokens.** The kernel's own e2e keeps
  the mechanism-side cases; the client-behaviour cases live here, next to the
  modules that implement them.

## Fork it

This repo is the top level: `core/` is a submodule pinned to one kernel commit,
and `go.mod` replaces the kernel module with it. Your fork is a real Go module
that happens to live next to a copy of its dependency.

```bash
$EDITOR dist.json          # pick your modules, disable kernel ones
mkdir modules/my-thing && $EDITOR modules/my-thing/module.go
$EDITOR dist.json          # add "my-thing" to modules
build/build.sh
```

Then run the generator and commit what it wrote — `manifest/modules_gen.go` is
**generated but checked in**: it records which modules that commit assembled.
CI fails on drift (`go run ./tools/distgen -check`).

The [`template`](https://github.com/rzbdz/newgate-ext/tree/template) branch is
the same skeleton with nothing but `hello`: the whole framework, one module,
`newgate` prints hello world.

### Where does a change belong?

> **Would this still be here if someone shipped a completely different
> distribution?**

A quirk of one upstream, a client × model cross semantic, a UI taste — that is
this repo. The gateway, the breaker, the component framework, anything every
distribution needs — that is [the kernel](https://github.com/rzbdz/newgate), and
it takes a patch there.

Getting it wrong is not silent: the spec says what was assembled, the build log
prints it, and `newgate plugin` reports what is actually running.

## Layout

```text
core/                  submodule: the kernel
modules/<name>/        this distribution's modules (same shape as the kernel's)
manifest/modules_gen.go generated, checked in: spec → assembly selection
tools/distgen/         spec JSON → that manifest
cmd/newgate/           the distribution's main: which graph, which version
dist*.json             the specs
build/build.sh         the only build entry point
mock/                  end-to-end against the kernel's fake upstream (reused, not copied)
testing/               graph/spec tests: every spec assembles, starts, stops
```

## Branches

| branch | what |
| --- | --- |
| `main` | the official distribution |
| `template` | fork skeleton: the framework + `hello` |

## License

No license file yet — this is pre-1.0 and still moving. Ask before you build
something you intend to depend on.
