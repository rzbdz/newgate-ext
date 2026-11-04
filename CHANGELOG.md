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
