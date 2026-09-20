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

## What you get

**Takes over Claude Code and OpenCode, without either of them knowing.**
A PATH shim plus `settings.json` / `opencode.json` rewritten in place — env and
config injected silently, byte-exact backups taken first. Install once; every
switch after that happens at the gateway, so you never reopen a session and
never edit a config by hand.

**The upstreams' quirks, already handled.**
DeepSeek's reasoning pass-back and tail shape, GLM's thinking hand-back, Claude
Code's background calls, OpenCode's intra-agent slots. Each one is a module with
a written reason for existing, switchable at runtime with `newgate st`.

**Changing which API answers is one command, not a migration.**
`newgate profile ark` — the next request takes the new chain. Your client keeps
running, its config is untouched, no session is interrupted. Tiers resolve
through ordered fallbacks, so one bad upstream is not your problem either.

**Upgrades that drop nothing.**
`newgate restart` hands the listening socket to the new process and the old one
drains what's in flight, streams included. Safe in the middle of a long agent
session — including one going through the gateway you're replacing.

**Tiers, breaker, metrics — wired for these clients.**
`newgate tier` explains a chain, `newgate breaker` shows who is tripped and why,
`newgate metrics` shows latency by context size and what got rewritten,
`newgate doctor` answers "why is this broken".

**English and 中文, both complete.**
The interface follows your system locale; a configured language wins, so an
English box can still read Chinese: `newgate lang zh-Hans`.

**Everything is a module — this repository included.**
It is one *selection* of modules. Fork it, edit the list, ship your own.

## Install

```bash
gh release download --repo rzbdz/newgate-ext -p '*linux-amd64'
mv newgate-default-linux-amd64 ~/.local/bin/newgate && chmod +x ~/.local/bin/newgate
newgate init && newgate on       # take over claude + opencode
```

One static binary, no runtime dependencies. Reopen the shell and keep working.

## Compose your own

```jsonc
// dist.json
{ "distribution": "default",
  "modules": ["i18n", "tui", "deepseek", "glm", "claudecode", "opencode"],
  "disable": [] }
```

`build/build.sh` turns that list into a release. `dist-hello.json` is the
skeleton — the framework plus one `hello` module — and it builds too. That is
the point: the kernel has no product in it, so a distribution is a **module
list**, not a fork of the code. See
[the kernel](https://github.com/rzbdz/newgate) for the mechanism underneath.

## Links

[kernel](https://github.com/rzbdz/newgate) ·
[why it is built this way](https://github.com/rzbdz/newgate/blob/main/docs/03-architecture.md) ·
[CLAUDE.md](CLAUDE.md) — the house rules a contributor follows
