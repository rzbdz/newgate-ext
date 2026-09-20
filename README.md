<div align="center">

# newgate-ext

### Claude Code and OpenCode, on the tiers **you** chose — with the upstreams' quirks already patched.

[![CI](https://github.com/rzbdz/newgate-ext/actions/workflows/ci.yml/badge.svg?branch=main)](https://github.com/rzbdz/newgate-ext/actions/workflows/ci.yml)
![release](https://github.com/rzbdz/newgate-ext/actions/workflows/release.yml/badge.svg)
![Go](https://img.shields.io/badge/go-1.27-00ADD8?logo=go&logoColor=white)
![dependencies](https://img.shields.io/badge/third--party_deps-0-brightgreen)
![platforms](https://img.shields.io/badge/platforms-linux%20%7C%20darwin-informational)

</div>

---

A [newgate](https://github.com/rzbdz/newgate) distribution. The kernel does
mechanism; this repo makes the product decisions — which modules ship, and whose
upstream quirks get patched before anyone hits them.

## Install

```bash
gh release download --repo rzbdz/newgate-ext -p '*linux-amd64'   # single static binary
mv newgate-default-linux-amd64 ~/.local/bin/newgate && chmod +x ~/.local/bin/newgate
newgate init && newgate on       # take over claude + opencode
```

Your client is not asked, not reconfigured by hand, and not restarted in a way
you'd notice. Reopen the shell and keep working.

## See it

```console
$ newgate status
  Proxy    ● Running   pid 537422 · 127.0.0.1:8899 · 27req/0err
  Takeover    claude ✓   opencode ✓

$ newgate st                       # what got patched, and why
  State  Plugin               Why it exists
  ✓      claude-bg            Claude Code's background calls carry no thinking, yet Chinese
                              models think by default → 15-30s, waves of timeouts, wedged sessions
  ✓      claudecode-deepseek  Claude Code strips DeepSeek thinking blocks; turn thinking off so the
                              next round does not 400 for a block that must be passed back
  ✓      deepseek.tail-shape  DeepSeek rejects a request whose tail is a bare tool_result — append
                              the minimal continuation instead of losing the round
```

```console
$ newgate profile glm              # the next request takes glm's chain
$ newgate tier normal             # …and here is exactly where that request goes
$ newgate breaker                 # who is tripped, why, and for how long
```

## What you get

| | |
| --- | --- |
| **Takeover you can't feel** | A PATH shim plus `settings.json` / `opencode.json` rewritten in place, byte-exact backups taken first. `newgate off` restores those bytes — not "equivalent settings". |
| **The quirks, already handled** | DeepSeek's reasoning pass-back and tail shape, GLM's thinking hand-back, Claude Code's background calls, OpenCode's intra-agent slots. Each is a module with a written reason, switchable at runtime. |
| **One command to change the API** | `newgate profile ark` — no client restart, no config edit, no session interrupted. Per-tier fallback chains, breakers and latency ordering decide what happens when an upstream misbehaves. |
| **Upgrades that drop nothing** | `newgate restart` hands the listening socket to the new process and drains in flight, streams included. |
| **Tiers, breaker, metrics** | All of the kernel's mechanism, wired for these clients: `newgate tier`, `newgate breaker`, `newgate metrics`, `newgate doctor`. |
| **Everything is a module** | Including this repo's own message catalog — see below. |

## Upgrading is a non-event

```bash
gh release download --repo rzbdz/newgate-ext -p '*linux-amd64' -O /tmp/ng
mv -f /tmp/ng ~/.local/bin/newgate && newgate restart
```

`restart` is not stop-then-start: the socket is handed over, in-flight requests
finish on the old process, and clients see nothing. Safe to run in the middle of
a long agent session — including one going through the gateway you're replacing.

## Compose your own

This repository is one *selection* of modules:

```jsonc
// dist.json
{ "distribution": "default",
  "modules": ["i18n", "tui", "deepseek", "glm", "claudecode", "opencode"],
  "disable": [] }
```

Fork it, edit the list, drop in a module of your own, and `build/build.sh`
produces a release. `dist-hello.json` is the skeleton — the
framework plus one `hello` module — and it builds too. That is the point: the
kernel has no product in it, so a distribution is a **module list**, not a fork
of the code.

## Language

English is the source language; Simplified Chinese ships beside it (both 100%
translated). The interface follows your system locale, but a configured
language wins — plenty of people run an English box and read Chinese:

```bash
newgate lang zh-Hans     # persist it (beats LANG); NEWGATE_LANG overrides per command
newgate lang             # what is in effect, and coverage per language
```

## Links

[kernel](https://github.com/rzbdz/newgate) ·
[why it is built this way](https://github.com/rzbdz/newgate/blob/main/docs/03-architecture.md) ·
[CLAUDE.md](CLAUDE.md) — the house rules a contributor follows
