package proto

import (
	"encoding/json"
	"strings"

	"github.com/rzbdz/newgate/lib/i18n"
)

// MetaFileName is the managed file carrying the version/schema declaration. It is part
// of the managed set (shipped to every replica), so replicas keep "which schema".
const MetaFileName = "newgate-config.json"

// Meta is the content of newgate-config.json.
//
// Why it exists: host and replica binaries **do not upgrade together** (a replica may go
// weeks without a restart), and the config format changes. This declaration lets a
// replica ask itself "can I interpret this" before applying.
type Meta struct {
	// SchemaVersion is the config format version, a small hand-bumped integer.
	SchemaVersion int `json:"schema_version"`

	// MinNewgateVersion is the minimum expected build (a `git describe` string).
	//
	// It **only drives a warning** and never blocks - see Gate for why.
	MinNewgateVersion string `json:"min_newgate_version,omitempty"`

	UpdatedAt string `json:"updated_at,omitempty"`
	UpdatedBy string `json:"updated_by,omitempty"` // which host / which person published it
}

// GateInput is the two external facts the decision needs.
type GateInput struct {
	// CurrentVersion is this binary's build string (cli.BuildInfo.Version).
	CurrentVersion string
}

// Gate is the version/schema two-level gate. Returns:
//
//	ok     -- whether this config can be applied
//	warn   -- empty means nothing to say; non-empty appears only in three places (daemon
//	          log, config status/doctor, Diagnostics), never on the request path
//	refuse -- why it refuses (ok=false then; the caller keeps the previous config serving)
//
// # Why the two levels behave differently rather than being one thing at two severities
//
// **min_newgate_version mismatch => allow + warn.** This binary **can parse** this
// config today; it was just written by a newer build: unknown fields are tolerated by
// store (it keeps unknown top-level keys) or absent and defaulted. A hard refusal would
// starve a working machine of provider updates, making a decorative mismatch into an
// incident. And the string is `git describe` output: `build-17-...` and `build-18-...`
// have **no numeric ordering**, so equality is all you can test - and that earns no teeth.
//
// **Higher schema_version => refuse to apply + keep the previous one.** What fails is
// not "a missing feature" but "**the field meanings changed**": this binary cannot
// interpret the content correctly, and misreading is not loud. This repo has had such
// changes (normal->mid rollback, three tiers to four, anthropic_url dialect split).
// Misreading looks like this: wrong upstream, or "provider has no api_key" then 404 -
// **it looks like an upstream outage**. Refusing is safer, and still fail-open.
//
// # Explicitly forbidden (recorded so nobody "just adds it" later)
//
// A warning **must not** be a RegisterRequestHook / special plugin (those run on the
// request hot path and report through notes - notes reach the user); **no** response
// headers (`X-Newgate-*` is read by agent CLIs); **no** printing from the
// wrapper/launch path (that is inside the agent's own startup). These three are SSE
// guarantees: a schema refusal must never put a word into the user's session stream.
func Gate(meta *Meta, in GateInput) (ok bool, warn, refuse string) {
	if meta == nil {
		// The host shipped no newgate-config.json (old host, or hand-written config dir).
		// Interpret by the current schema - unlike "higher schema", which is unreadable.
		return true, i18n.T("the snapshot has no {name} (the peer declared no schema); interpreting it as the current schema",
			i18n.A{"name": MetaFileName}), ""
	}
	if meta.SchemaVersion > SupportedSchema {
		return false, "", i18n.T(
			"this configuration declares schema_version={got}, but this binary only understands up to {max}: the meaning of fields may have changed, and misreading them raises no error (it routes to the wrong upstream or reports that a provider has no api_key). The previous configuration is kept in service. Upgrading newgate catches up automatically.",
			i18n.A{"got": meta.SchemaVersion, "max": SupportedSchema})
	}
	if meta.SchemaVersion <= 0 {
		return true, i18n.T("{name} has no schema_version; interpreting it as the current schema",
			i18n.A{"name": MetaFileName}), ""
	}
	// Match only on equal build strings. Dev builds (no injected version) are skipped -
	// otherwise every dev machine gets a line of noise.
	if want := strings.TrimSpace(meta.MinNewgateVersion); want != "" {
		got := strings.TrimSpace(in.CurrentVersion)
		if got != "" && got != "dev" && got != want {
			return true, i18n.T(
				"the host declares build {want}, this machine is {got}: this machine can still parse the configuration (unknown fields are ignored, missing ones fall back to defaults), so it was let through. Upgrade newgate to line up exactly.",
				i18n.A{"want": want, "got": got}), ""
		}
	}
	return true, "", ""
}

// ParseMeta pulls Meta out of the snapshot; missing returns (nil, nil), broken errors.
//
// Broken content (a file that is not JSON) is **not** "undeclared" - the host wrote
// something broken, and the caller must refuse rather than quietly use the current schema.
func ParseMeta(files map[string][]byte) (*Meta, error) {
	raw, ok := files[MetaFileName]
	if !ok {
		return nil, nil
	}
	var meta Meta
	if err := json.Unmarshal(raw, &meta); err != nil {
		return nil, i18n.Ef(err, "cannot parse {path}: {err}", i18n.A{"path": MetaFileName})
	}
	return &meta, nil
}
