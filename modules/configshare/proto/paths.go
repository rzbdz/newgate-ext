package proto

import (
	"path/filepath"

	paths "github.com/rzbdz/newgate-ext/modules/config/paths"
)

// This file used to be part of the kernel's `modules/config/paths` (moved here with
// config sharing on 2026-09-20). The rule was simple: **file names are product knowledge,
// the directory root is kernel mechanism**. The kernel now exposes only `paths.Root()`
// (where runtime files live); what is named what underneath is the caller's business -
// the kernel should not maintain six functions in its paths package for one feature.
//
// The other half is ordering: these helpers used to sit next to "the kernel's own files"
// so a reader could not tell whose they were. Now they live in their only user's package.

// ConfigShareDir is this module's own bookkeeping dir.
//
// Separate from the config dir because nothing here **should be seen by the watcher**:
// the failure counter can change every 30s, which would be a constant reload source.
// For the same reason it is not in the kernel's EnsureDirs - created on first use.
func ConfigShareDir() string { return filepath.Join(paths.Root(), "configshare") }

// ConfigShareStateFile is the replica ledger: applied generation, the sha256 of each
// file we wrote (the drift baseline), and the failure count.
func ConfigShareStateFile() string { return filepath.Join(ConfigShareDir(), "state.json") }

// ConfigShareHostFile is the host ledger: authoritative identity + monotonic generation.
//
// host_id and generation share **one file** because "authority changed" and "generation
// reset" must take effect together: split in two, there would be a torn state between
// renames, and the replica judges "apply or not" on both values together.
func ConfigShareHostFile() string { return filepath.Join(ConfigShareDir(), "host.json") }

// ConfigShareLockFile is the background poll's advisory lock. A graceful handoff has two
// daemons alive for a few hundred ms, both writing the same files - flock lets the late
// one skip the round (failing to get the lock is not an error, see poller.go).
func ConfigShareLockFile() string { return filepath.Join(ConfigShareDir(), "lock") }

// ConfigShareRootKeyFile is the pre-shared root key (`newgate config trust` writes it).
// 0600: holding it means holding both derived values, the AES-GCM key and the bearer token.
func ConfigShareRootKeyFile() string { return filepath.Join(ConfigShareDir(), "root.key") }

// secretsPath is where keys land: `{"version":1,"keys":{"<provider>":"sk-..."}}`.
//
// Why the root dir rather than configshare/: it is **where host-pushed keys go**, read
// by the config layer when resolving a provider; its lifetime matches that config's.
//
// Why not state.json: the watcher watches its signature (watch.go's signature()), so
// every write triggers a full reload plus a log line. Keys change on the poll cycle -
// writing them there would install a metronome on the config system.
//
// 0600, not providers.json's 0660: providers holds only bindings (deliberate group
// visibility), while a cleartext key means whoever reads it spends the subscription.
// Exception: shared deploys with a daemon under another user (CLAUDE.md §3.1) - chmod
// 0660 as needed and make the group boundary the trust boundary.
func secretsPath() string { return filepath.Join(paths.Root(), "secrets.json") }
