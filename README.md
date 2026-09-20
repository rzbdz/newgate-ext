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

## What it does

**Takes over your CLI, without your CLI knowing.** `newgate on` puts a PATH shim
in front of `claude` and `opencode` and rewrites their own config in place —
`settings.json`, env blocks, `opencode.json` — model names becoming tier names.
Byte-exact backups are taken first, and `newgate off` restores those bytes (the
end-to-end suite verifies the restore with a checksum). Your editor, your
client, your muscle memory: unchanged. One binary serves every client, because
`argv[0]` decides who is being invoked.

**One command to switch which API is behind it.** `newgate profile glm` — the
next request takes glm's chain. No client restart, no config edit, no session
interrupted. Tiers resolve through ordered candidate chains, with fallback,
first-byte timeouts, breaker health and latency ordering deciding what happens
when an upstream misbehaves; every reroute is reported, never silent.

**Upgrades never drop a request.** `newgate restart` hands the listening socket
to the new process and lets the old one drain what is still in flight, streaming
responses included. Swapping the binary is safe at any moment — including from
a session that is itself going through the gateway.

**Patches the quirks the upstreams actually have**, and only at the crossings
where they matter: DeepSeek's tail shape and reasoning pass-back, GLM's thinking
hand-back, Claude Code's background calls, OpenCode's intra-agent slots. Every
patch is a module you can switch off at runtime (`newgate st`), and each one
says why it exists.

**Compat first.** The client keeps its dialect, its env contract and its idea
of which model it is talking to. Requests are rewritten as bytes, never
re-serialized, so fields you have never heard of survive the trip.

## Compose your own

Everything here is a module — the gateway, the breaker, the UI, the entry
point, even the message catalog. This repository is one *selection* of them:

```jsonc
// dist.json
{ "distribution": "default",
  "modules": ["i18n", "tui", "deepseek", "glm", "claudecode", "opencode"],
  "disable": [] }
```

Fork it, edit that list, drop in a module of your own, and
`build/build.sh` gives you a release. `dist-hello.json` in this repo is the
skeleton — the framework plus one `hello` module — and it builds too, which is
the point: **the kernel has no product in it**, so a distribution is a module
list rather than a fork of the code. See
[the kernel's README](https://github.com/rzbdz/newgate#everything-is-a-module)
for what that buys.

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
| `i18n` | this distribution's own message catalog, appended to the language the kernel installed |
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

## Language

The interface speaks English (the source language) and follows your system
locale, with the kernel's precedence:

```text
NEWGATE_LANG  >  ~/.config/newgate/state.json  >  LC_ALL / LC_MESSAGES / LANG  >  English
```

This distribution carries its own message catalog (`modules/i18n/catalogs/`) so
that the parts the kernel cannot know about — the client onboarding, the
upstream-quirk patchers, the UI — are translated too. `newgate lang` reports
coverage for both halves.

```bash
newgate lang            # what is in effect, and how complete each language is
newgate lang zh-Hans    # persist it
```

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
