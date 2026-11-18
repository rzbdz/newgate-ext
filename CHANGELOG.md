# Changelog

All notable changes to this distribution are recorded here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and this project uses
[semantic versioning](https://semver.org/).

This file is the **source of truth for release notes**. The release workflow
takes the section whose heading matches the pushed tag (`## [0.8.1]` for
`v0.8.1`) and uses it as the GitHub Release body. A tag with no matching section
fails the release job — deliberately: a release without a changelog is not a
finished release.

## [Unreleased]

### Changed

- **The kernel now ships no modules.** `rzbdz/newgate` is a framework: the
  component graph, typed capabilities, the process-entry contract, the i18n
  engine, and the tooling. Everything else — the gateway, the breaker, the CLI,
  config, client takeover, locale, plugin manager, the HTTP/styling/view leaf
  libraries — moved here and lives in `modules/` and `lib/` next to the upstream
  patches and client adapters. The split follows the line "the kernel is
  mechanism, the distribution is product".
  - A build now names every module it installs in its spec; `disable` is gone
    from the specs (there is nothing in the kernel to disable). `dist-hello.json`
    is the smallest example: `i18n`, `entry`, `hello`.
  - The composition root takes two hooks (`app.Options.Trace`,
    `OnAssemblyFailure`) instead of knowing where the product keeps its log.
  - Behaviour is unchanged: the whole end-to-end suite passes against the
    product binary, as before.

### Added

- **One-line install, then one-command setup.** `curl -fsSL …/install.sh | bash`
  puts the static binary in `~/.local/bin/newgate` (verifying the release's
  `SHA256SUMS`, keeping the previous binary as `newgate.prev`). `newgate config`
  is the first-run wizard: pick which clients should go through newgate, type
  one base URL and one key, pick a model from the endpoint's `/models` list (or
  type it), and it writes `providers.json` + `mappings/default.kv` +
  `state.json`, installs the clients that are missing (claude, codex, opencode,
  hermes — each through its own module's installer), takes them over, and starts
  the proxy. Every tier of the bootstrap profile points at that one model, so
  the whole chain works immediately; split the tiers later with `newgate tui`
  or by asking the agent to do it.
  - Re-running it asks only for clients: a machine that already has a usable
    profile is not asked for its base URL again (`--reconfigure` forces it).
  - It never touches clients you did not tick, and it says "not taken over yet"
    (with the `run <client>`-then-`newgate on` line) when a config-mode client
    has not written its config file yet — instead of pretending it was taken over.
  - Without a terminal it writes nothing and prints the manual steps.
- **Installers for Claude Code and OpenCode** (`npm i -g @anthropic-ai/claude-code`,
  `npm i -g opencode-ai`) alongside the existing codex / hermes ones, so
  `newgate <client> -y` and the wizard can install them.
- `install.sh` at the repository root: the one-liner above.

### Changed

- The **runtime** port grew a second face. `TakeOver` / `Release` / `TakeOverAll`
  / `TakeoverStatus` / `Start` are now on a `runtime-control` capability
  (`runtime.Control`), so a module can do what `newgate on|off|start` do without
  importing the kernel's internals. `newgate start` and the wizard run the same
  implementation; `StartOptions.SkipTakeover` lets the wizard start the proxy
  without taking over clients it was told to leave alone.

### Fixed

- **Claude Code is capped at a 512K context window.** Every injected model name
  used to carry the `[1m]` suffix, and profiles could declare
  `context_window: 1000000`. A long session then grew to 1.01M tokens, the next
  request failed with `400 Input tokens exceed the model context limit`, and
  `/compact` could not rescue it — the compaction request itself carries the
  whole conversation and is over the limit too. Model names are now injected
  bare; the window travels through `CLAUDE_CODE_MAX_CONTEXT_TOKENS` and is
  capped at 512K (auto-compact at 400K), leaving room for newgate's reasoning
  backfill, which can grow a request by about 14%.
- Two newgate daemons on one machine (different `NEWGATE_HOME` and port) no
  longer fight over Hermes' `config.yaml`. Only the daemon the file already
  points at syncs the `/model` list; moving it to another port is an explicit
  `newgate on hermes`.
- **Hermes' `/model` list no longer stops syncing on machines where the
  official installer put a shell wrapper at `~/.local/bin/hermes`.** newgate
  reads the interpreter out of the launcher's shebang to cross-check its YAML
  edits; the wrapper's shebang is `#!/bin/sh`, so a shell was asked to run the
  check script and sat waiting on the inherited terminal. Takeover and the
  background sync both blocked (the timeout path is fail-open, so the only
  visible symptom was a new profile never appearing in `/model`). Only a
  `python*` interpreter is accepted now, and the child never inherits stdin.

## [0.8.2] - 2026-11-04

### Added

- **Hermes Agent support** (`newgate on hermes` / `newgate off hermes`). The
  takeover edits `$HERMES_HOME/config.yaml` (default `~/.hermes`) so the main
  loop, `delegate_task` subagents and every auxiliary task (vision, compression,
  titles, approval, session search, web extract, curator …) go through newgate.
  Two ways of choosing a model live side by side:
  - **follow newgate**: the default model is a tier name (`normal`), and the
    active profile decides which real model serves it;
  - **pick in Hermes**: `/model` lists a `newgate` row (the tiers) and one
    `newgate-<profile>` row per profile, pinned to that profile and listing every
    model it binds as `provider/model`.
- Hermes slots (`model`, `delegation`, `vision`, `compression`, `auxiliary`) are
  set on the new **Hermes** card in the web UI or kept at their defaults
  (normal / mid / vision / mid / light).
- **The `/model` list follows your profiles.** While the daemon runs, a new,
  changed or deleted profile is synced into `config.yaml` within seconds. Only
  newgate's own `providers.newgate*` rows are touched — a model you picked in
  Hermes with `/model … --global` is never reverted. The Hermes gateway re-reads
  the file on every message, so no restart is needed for the list.
- `newgate hermes-gateway [sync|restart]`, a `Hermes` line in `newgate status`,
  and sync / restart buttons on the card. After the first takeover newgate tells
  you when the running Hermes gateway still uses the old upstream for vision, web
  extract and approval (it copies those into its environment at start-up); the
  restart is a graceful `hermes gateway restart`, and it is your call.
- Editing `config.yaml` is byte surgery: only the keys above change, everything
  else stays byte for byte. Before writing, Hermes' own YAML parser must agree
  that the new file equals the old one plus exactly the intended changes;
  otherwise nothing is written. A file Hermes changed in the meantime is not
  overwritten, and an unreadable newgate config leaves the file alone.
- `newgate off hermes` restores key by key: the values newgate wrote go back to
  what they were before the takeover, and anything you changed in Hermes in the
  meantime is kept.

### Changed

- The gateway answers a GET or HEAD without a body (local-server probes such as
  `/api/tags`, `/v1/props`, `/version`) with 404 instead of a 400, and no longer
  counts it as a failure.
- `GET …/v1/models` under a client's URL can be answered by that client's
  module. Hermes uses it: the `newgate` row lists the tiers, each
  `newgate-<profile>` row lists the tiers plus that profile's models, and a
  binding you change in a profile shows up in `/model` right away. Every other
  client gets the same list as before.

### Kernel

- Bumped to `7b84ff2`: the bodyless-probe 404 and the per-client `/models`
  answer (`special.ModelLister`) above.

## [0.8.1] - 2026-11-04

### Added

- **`newgate upgrade` can replace the binary.** It downloads the platform asset
  from a GitHub Release and verifies `SHA256SUMS`, or — on a development machine
  — fetches and runs `build/build.sh` from a local checkout. The mode is chosen
  on first run and can be inspected/changed with `newgate self-update`.
  `newgate upgrade@<tag>` pins a version; `newgate self-update rollback` restores
  the previous binary; the daemon hands its socket over so live sessions survive
  the swap, and a new binary that fails its trial run is rolled back
  automatically.
- **`newgate upgrade` asks before it acts**: separate y/N prompts for the binary
  and for module-owned data (`-y` answers both, `-f` also allows reinstall or a
  downgrade).
- **Updates in the web interface**, shipped as a frontend plugin owned by the
  self-update module. Modules can now register their own UI (slot + contract),
  and the UI itself knows no plugin by name.
- **Model fingerprints** (`newgate verify`): make an upstream produce a short
  numeric fingerprint and compare it with a reference bank; warn when the
  binding's name does not match the model actually being served. The web UI
  gains a fingerprints page, and the model picker shows the last verdict.
- **Dynamic data upgrades**: `newgate upgrade` runs upgrade actions registered by
  modules (today: dynamic data and the fingerprint bank).
- **The model picker asks the provider** what it serves (`modellist`).
- **`newgate nothink on|forever|off`**: a global no-reasoning switch.
- **`ssh-tunnel`**: reach a remote machine's interface over SSH, from the CLI and
  the web UI.
- **Web home mode**: a landing overview of every chain, per-client icons, a fixed
  card order, pin-vs-follow and a fallback switch, themes as a plugin port (three
  ship), and a sidebar grouped by purpose.
- **Per-client profiles in the terminal**: `newgate status`, `newgate tui` and
  `newgate agents` show which profile each client uses, whether it follows the
  default, and which tier each slot resolves to.
- Specs accept a `github` field naming the release repository, so a fork's
  upgrade channel points at the fork.

### Changed

- `newgate --profile=<name>` (and `--preset`) stops at the pinned profile's chain
  head: no silent switch to another profile, even when the pinned profile has no
  usable candidate. Global `newgate fallback off` behaves the same way.

### Fixed

- A model name that is configured but currently unavailable is no longer reported
  as "neither a known tier nor bound in any profile". The request resolves to its
  tier and follows that tier's usable chain; under a pinned profile the 404 says
  the tier has no usable candidate there. (Field report: a circuit-broken
  `deepseek-flash` binding made `--profile=ds` fail with a config-shaped error
  during an upstream outage.)
- A successful `newgate probe` now closes a breaker immediately instead of
  waiting out the 60-second isolation window.
- opencode: the agent identity is part of the base URL again.
- Claude Code: every classifier gate is approved, not only the `<block>` one.
- ssh-tunnel: routes no longer reach past the remote interface; the tunnelled
  page no longer reads the local machine's data; new targets have their fields
  and clickable addresses.
- Web/home: section actions target their own section, card paths are clickable
  again, and the fallback button re-reads its state.
- self-update: tags are kept in source clones; untagged builds are left alone;
  source builds require Go 1.21 or newer; an unreadable source directory means
  release mode.

### Kernel

- Bumped to `627b447`: the resolution/breaker fixes above, plus per-client
  profile support and visible agent environment-variable names.

## [0.8.0] - 2026-09-28

### Added

- **A browser dashboard** on the gateway port: sidebar, tabs and a split view;
  a config workbench whose raw editor updates the controls as you type;
  providers configurable from the page; a breaker table with a per-row test
  button; a log view that only refreshes what it needs; grouped feature
  switches; and a UI that follows the resolved language.
- **Codex support** (kernel client module plus distribution glue): install
  guidance, a dashboard card, and two takeover modes — write tier names, or
  write Codex's own model names and map them back to tiers, with the mapping
  table editable from the page.
- **`arch-diagram`** serves the architecture map at `/arch`; `newgate arch`
  prints the address. The map is generated from a real assembly plus the
  import graph.
- **A public docs site** generated by `tools/sitegen` (hand-written renderer, no
  SSG), with a landing page that embeds the real UI.
- **A `dashboard`-only distribution** (no terminal UI), so "the UI is just a
  module" is exercised by a product spec.
- **Codex × DeepSeek tool crossing** and the DeepSeek `/responses` tail repair.
- Every module describes what it does; every client reports its profile; the
  skeleton module shows how a module carries its own i18n catalog.
- Claude Code: the slot → tier map is configurable (with the raw pane as an
  editor again), the subagent slot keeps its own `inherit`, and the Bash
  classifier's state reaches the web interface.
- Every injected Claude Code model name carries the 1M context marker.

### Changed

- The assembly start order is generated (`distgen`) instead of resolved at
  runtime.

### Fixed

- Dashboard: the favicon (valid SVG at last), stable card order across silent
  refreshes, one writable face per file, the raw pane no longer echoing its own
  reset, a scrolling many-card column, and a check for who is asking.
- Codex × DeepSeek: custom tools are degraded wherever they arrive; the switch
  is actually read.
- Build: say out loud when the kernel commit has not been pushed.

## [0.7.0] - 2026-09-20

### Changed

- **The distribution speaks through its own catalog.** User-visible text moved
  into `modules/i18n/catalogs/zh-Hans.json`; the kernel ships the i18n mechanism
  and the same checker for both repositories. Breaking for anyone carrying
  local patches to strings.

## [0.6.1] - 2026-09-20

### Fixed

- Kernel bump: the daemon no longer guesses its identity from insufficient
  evidence.

## [0.6.0] - 2026-09-20

### Changed

- README rewritten in English.
- Modules moved in from the kernel: the terminal UI, the client modules and
  `configshare` (a product library that is not wired yet). The distribution now
  builds entirely from its own module set.

## [0.5.0] - 2026-09-20

### Fixed

- Product plugins declare their own ordering edges instead of relying on the
  kernel to guess them.

## [0.4.0] - 2026-09-20

### Added

- **Skeleton distributions in one line**: `"disable": ["*"]` turns off every
  kernel module, so a spec can start from the frame plus one module
  (`dist-hello.json` is the live example).

## [0.3.0] - 2026-09-20

### Changed

- **Breaking:** the `go/` layer is flattened. The distribution repository is its
  own Go module; the kernel sits in `core/` as a submodule and a `replace`
  target.

## [0.2.0] - 2026-09-20

### Added

- Multiple specs produce multiple binaries from one build; the `hello` skeleton
  distribution (frame + one module) ships as the minimal example.
- Entry ownership: an invocation must be claimed by exactly one entry, and the
  skeleton spec proves the bare binary still does something.

## [0.1.2] - 2026-09-20

### Changed

- Docs: remove stale wording about the old module flow; document how to download
  a prebuilt binary (including the required `chmod +x`).

## [0.1.1] - 2026-09-20

### Fixed

- Build: resolve relative output paths before changing directory, and follow the
  repository rename.

## [0.1.0] - 2026-09-20

### Added

- The distribution repository takes shape: specs, build script, its own tests,
  and the first external modules (`hello`, `simple-cli`).
