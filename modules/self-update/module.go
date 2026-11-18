// Package selfupdate replaces newgate's own binary: download the artifact from a GitHub
// Release, or build one in a local repo, then swap it in gracefully (daemon socket, no gap).
//
// # Where it hangs
//
// This module **adds no new upgrade command**: it hands modules/upgrade a registered
// action with id `binary`, so `newgate upgrade` upgrades binary and data together, and
// `newgate upgrade@v0.9.0` pins the binary to a tag. upgrade knows nothing of downloading
// Releases, building, or handoff - all here; this knows nothing of refreshing a reference
// library's data. It only adds `newgate self-update`: see/change "which route", and rollback.
//
// # Two routes (mode), settled on first install
//
//   - **release**: fetch the artifact for this platform from the latest Release in the
//     spec's GitHub repo (dist.json's `github`), check SHA256SUMS, swap. No-build machines.
//   - **git**: in a local checkout, fetch -> fast-forward if possible -> `build/build.sh`
//     -> swap. For developers and early adopters: they have the repo, they want "my tree".
//
// The route is recorded in state.json's `self-update` section, **inferred on the binary's
// first run** from the build-time source dir (see settings.go): the checkout that built it
// is here and is this repo -> git; not -> release. Afterwards only that section counts,
// and `newgate self-update git|release` changes it. Inferred rather than recomputed,
// because "which route this machine takes" is a **decision**, not a directory's fate.
//
// # `@version`
//
// `newgate upgrade@<ref>` ignores mode: look for a Release of that tag; if present with an
// artifact for this platform -> download it; else (tag without a release, a branch/commit,
// or no artifact for this platform, e.g. riscv64) -> clone that ref into a temp dir, build,
// swap. The developer's local checkout is never switched - that is what he is editing.
//
// # Safety (each rule is "a bad swap is far worse than none")
//
//   - The new binary first **runs `version` under the name newgate**; if it fails (wrong
//     architecture, truncated, wrong distribution), no swap;
//   - Replacement is a same-dir temp file + rename, old one hard-linked to `<path>.prev`:
//     `newgate self-update rollback` restores it in one step;
//   - Without `--force` it never goes backwards: an older Release (dev norm) is just noted;
//   - Only one upgrade runs at a time (config-dir flock); with a daemon running, run
//     `restart` on the new binary (graceful handoff) and verify its /proc/<pid>/exe.
package selfupdate

import (
	"context"

	cliapi "github.com/rzbdz/newgate-ext/modules/cli/extension"
	modules "github.com/rzbdz/newgate/component"
	i18n "github.com/rzbdz/newgate/lib/i18n"

	upgradeapi "github.com/rzbdz/newgate-ext/modules/upgrade"
	webapi "github.com/rzbdz/newgate-ext/modules/web-dashboard"
)

// Name is this module's name in the assembly table and `newgate plugin` (= directory name).
const Name = "self-update"

// actionID is the ID of the action registered into upgrade.
const actionID = "binary"

// New declares the component.
//
// Both dependencies are Optional: without upgrade there is no `newgate upgrade` item,
// but `newgate self-update` can still look, change and roll back; with no UI both entries
// are gone and the module does nothing - no background behavior (no periodic checks).
func New() modules.Component {
	var releases []modules.Release
	return modules.Component{
		Name: Name,
		Desc: func() string {
			return i18n.T("replace the newgate binary from a GitHub release or a local build, with a graceful daemon handoff", nil)
		},
		Type: "infra",
		Requires: []modules.Requirement{
			modules.Optional(upgradeapi.Capability),
			modules.Optional(cliapi.Capability),
			// The web face (web.go): extension endpoints give data and copy, the plugin port ships JS.
			modules.Optional(webapi.ExtensionCapability),
			modules.Optional(webapi.PluginCapability),
		},
		Start: func(_ context.Context, ctx modules.Context) error {
			// Record "which route" on first run (see the package note). Failure does not block
			// startup: unreadable, infer each time, and retry on the first real upgrade.
			_, _ = ensureSettings()

			if up, ok := modules.Get(ctx, upgradeapi.Capability); ok {
				rel, err := up.Register(upgradeapi.Action{
					ID:        actionID,
					Title:     func() string { return i18n.T("newgate binary", nil) },
					Describe:  describeAction,
					Run:       runAction,
					Versioned: true,
					// Replacing newgate itself: `newgate upgrade` asks about it separately (see upgrade's Kind).
					Kind: upgradeapi.KindProgram,
				})
				if err != nil {
					return err
				}
				releases = append(releases, rel)
			}
			if web, ok := modules.Get(ctx, webapi.ExtensionCapability); ok {
				rel, err := web.Register(webName, newWebState().handle)
				if err != nil {
					return err
				}
				releases = append(releases, rel)
				// The plugin mounts only when the endpoint is there: without it, the top bar errors.
				if plugins, ok := modules.Get(ctx, webapi.PluginCapability); ok {
					rel, err := plugins.RegisterPlugin(webapi.Plugin{
						Name:   webName,
						Assets: webAssets(),
						Entry:  "plugin.js",
						Slots:  []webapi.PluginSlot{webapi.SlotTopbar},
					})
					if err != nil {
						return err
					}
					releases = append(releases, rel)
				}
			}
			if ui, ok := modules.Get(ctx, cliapi.Capability); ok {
				rel, err := ui.RegisterCommand(selfUpdateCommand{})
				if err != nil {
					return err
				}
				releases = append(releases, rel)
			}
			return nil
		},
		Stop: func(context.Context) error { return modules.ReleaseAll(releases) },
	}
}
