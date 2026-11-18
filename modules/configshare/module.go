// Package configshare lets one person's dev machines share **one set** of LLM API subscriptions.
//
// The problem is concrete: one person has several machines for different projects, yet
// all subscriptions are the same set. Today each machine keeps its own providers.json +
// mappings/, so adding an upstream means touching N machines, and slips make them drift.
//
// # Authority model (the foundation of this feature)
//
// **Single writer**: one machine is the host (role=host), the sole authority; the rest
// are replicas (role=client), read-only. Several hosts are **not supported**: they fight.
//
// Why "design the host properly" comes first: once a replica can edit config, "authority"
// is gone, every replica becomes a potential drift source, and drift is **silent** (the
// remote sync succeeded, local behavior unchanged). So:
//
//   - local edits on a replica are not errors, but must be **seen** (drift guard, proto/drift.go);
//   - "machine-specific config" gets a formal **local layer** (files the host does not
//     own) rather than editing the managed ones;
//   - `publish` is host-only.
//
// # Topology (decides every other choice)
//
//	authority host (behind tailscale)      proxy host (later)      replica (no inbound?)
//	git repo <-- human edits --> snapshot  same protocol relay     outbound pull only
//	/__newgate/config  <------- tailnet ---------------------      last-known-good cache
//	/__newgate/secrets
//
// Three hard constraints:
//
//  1. **Replicas are outbound-only**. Restricted nodes forbid inbound, so pull only, no
//     push; that also dodges the reconnect handling a long-lived push would need.
//  2. **No ssh**. The host goes behind a jump chain where ssh's trust model does not
//     hold (hence `git clone ssh://` is out too). Transport is our own pull.
//  3. **git stays on the host**, for versioning/rollback/audit only (a tool for humans);
//     the wire is our own protocol, replicas install no git and clone nothing.
//
// # Why not a "remote gateway"
//
// The other rejected path: "every CLI points at one remote daemon, no local daemon". It
// puts a fragile WAN hop on **every request's hot path**, needs mosh-style resumable
// streams, and adds a **single point of failure**: a system whose point is switching
// upstreams when one dies should not build its own point of collapse (docs/12).
//
// The key insight: **"one change takes effect everywhere" is a property of config
// distribution, not of centralizing traffic**. Standard shape: control-plane/data-plane
// separation plus a last-known-good cache (Kong / Envoy / Istio / Consul); the data
// plane caches locally and fetches nothing through it, so a dead host is survivable.
//
// # Where the code is
//
//   - proto/ -- wire protocol, sealed envelope, replica-side pull and apply. It only
//     knows paths/store/domain and the standard library, and no port module (which is
//     why cli can import it without a cycle, and why it can be tested off the graph).
//   - module.go (this file), host.go, cli.go, diagnostics.go -- the component, host-side
//     distribution, the `newgate config` command and diagnostics.
//
// # Current state of this file
//
// Only this comment, **no `New()` yet**. Not an omission: `tools/genmodules` recognizes
// a component by the exact criterion "the directory has module.go **and** it exports a
// New function returning modules.Component" (see the tools/genmodules comment; the
// criterion is deliberately tight), so a directory where `New()` has not appeared **is
// not** wired into app/modules_gen.go, and not a byte of the assembled binary changes.
// app/modules_gen_test.go's moduleDirs skips such directories too - a supported state.
//
// This file exists first because app/layout_test.go locks a directory rule with no
// exceptions: every directory under modules/ must expose a root module.go. `New()` and
// the component appear in the commit that adds the `newgate config` command; until then,
// this module is invisible to the binary.
//
// Note: **do not** spell out the full signature of that New anywhere in this comment.
// moduleDirs matches substrings with bytes.Contains; that literal string makes this
// directory a component directory, and "15 assembled, 16 directories" fails (2026-09-17).
package configshare
