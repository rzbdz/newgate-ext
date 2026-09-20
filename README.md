<div align="center">

# newgate-ext

### The best gateway for **vibecoding**.

<sub>Transparent takeover · one-command API switching · failover that explains itself · metrics and debug logs · graceful zero-downtime upgrades · modular to the bone</sub>

[![CI](https://github.com/rzbdz/newgate-ext/actions/workflows/ci.yml/badge.svg?branch=main)](https://github.com/rzbdz/newgate-ext/actions/workflows/ci.yml)
![release](https://github.com/rzbdz/newgate-ext/actions/workflows/release.yml/badge.svg)
![Go](https://img.shields.io/badge/go-1.27-00ADD8?logo=go&logoColor=white)
![dependencies](https://img.shields.io/badge/third--party_deps-0-brightgreen)

</div>

---

One local gateway between Claude Code / OpenCode and the models you choose. It
stays out of the way of the flow: your session keeps running while you switch
providers, and when a new model — or a fresh upstream quirk — shows up, adapting
to it is one small module, in your own fork, on your own schedule.

**It takes over your CLI, without your CLI knowing.**
A PATH shim and the client's own config rewritten in place — env injected
silently, byte-exact backups taken first. Install once; you never edit a config
by hand again, and you never reopen a session.

**Changing which API answers is one command, not a migration.**
`newgate --set-profile ark` — the next request takes the new chain. Your client
keeps running, its config untouched, the old provider one command away.

**Nothing dies with a single upstream.**
Every tier is an ordered fallback chain — your rules first, then predicted
time-to-first-byte. Every skip is explained and every reroute logged.

**You can see what actually happened.**
`newgate metrics` — latency by context size, failovers, first-byte timeouts,
rewrites, each counter with a one-line explanation. `newgate debug on` for the
full request log, `newgate doctor` when something is wrong.

**Upgrading doesn't interrupt you.**
`newgate restart` hands the listening socket to the new binary and drains what's
in flight, streams included — safe in the middle of a long agent session,
including one going through the gateway being replaced.

**The upstreams' quirks are modules, not `if` statements.**
DeepSeek's reasoning pass-back and tail shape, GLM's thinking hand-back, Claude
Code's background classifier calls — `newgate st` lists all six, each with the
failure it exists to prevent, switchable at runtime. The client × model bridges
are modules too (Claude Code × DeepSeek, Claude Code × GLM, and Oh My OpenAgent's
slots inside OpenCode). Read one, replace it, or write the patch for the upstream
we have never heard of.

## Fork it, and make it yours

A module is one directory with a `New()` in it. Swap our routing policy for
yours, drop the quirks you don't care about, keep the gateway and the breaker:

```jsonc
// dist-mine.json  →  build/build.sh  →  one static binary per platform
{ "distribution": "mine",
  "modules": ["tui", "deepseek", "glm", "claudecode", "opencode"],
  "disable": [] }
```

Gateway, tier resolution, breaker, UI and entry point are all modules, so
replacing a policy means replacing a module — not patching a core. `newgate
restart` hands the socket over, so shipping your version doesn't interrupt
anyone, including yourself mid-session. Start from `dist-hello.json` (the
framework plus one `hello`) if you would rather begin at almost nothing.

## Install

```bash
gh release download --repo rzbdz/newgate-ext -p '*linux-amd64'
mv newgate-default-linux-amd64 ~/.local/bin/newgate && chmod +x ~/.local/bin/newgate
newgate init && newgate on claude     # and: newgate on opencode
```

One static binary, no runtime dependencies. `newgate init` lays down a config
with placeholders; put your keys in `providers.json` (or in `NEWGATE_KEY_*`
environment variables), then reopen the shell and keep working.

## What this repository is

newgate is split in two, and the split is the point:

| | |
| --- | --- |
| [**rzbdz/newgate**](https://github.com/rzbdz/newgate) | the **kernel**: chains, breaker, gateway, upgrade handoff, the module framework. No product decisions — it knows no upstream and no client by name. |
| **rzbdz/newgate-ext** *(here)* | one **distribution**: which modules ship, which upstreams get patched, in what order. A product decision, and yours to change. |

This one is the flagship: Claude Code and OpenCode, on the tiers you chose, with
the quirks of the upstreams you actually use already handled. `dist.json` is the
whole of it — everything else is modules.

## Links

[the kernel](https://github.com/rzbdz/newgate) ·
[why it is built this way](https://github.com/rzbdz/newgate/blob/main/docs/03-architecture.md) ·
[the extension guide](https://github.com/rzbdz/newgate/blob/main/docs/09-extension-guide.md) ·
[releases](https://github.com/rzbdz/newgate-ext/releases) ·
[CLAUDE.md](CLAUDE.md) — the house rules a contributor follows
