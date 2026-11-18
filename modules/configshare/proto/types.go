// Package proto is config sharing's wire protocol and replica-side apply logic.
//
// Why a separate subpackage (instead of the modules/configshare root)
//
// There is exactly one right place to start the background poll: the daemon's
// cli.Serve, which requires modules/cli to import it - and if this code lived in the
// configshare root, cli -> configshare -> cliapi (`config` command) would be a cycle.
//
// In a subpackage the dependency is one-way: cli -> configshare/proto, and proto knows
// only paths/config, store, domain and the standard library, no port module (especially
// not cliapi). A bonus: this package **tests off the component graph** - tests run on
// httptest + temp dirs (see *_test.go).
//
// # Protocol shape
//
//	host (role=host)                       replica (role=client)
//	  GET /__newgate/config  --envelope-->   pull -> decrypt -> validate -> write
//	  GET /__newgate/secrets --envelope-->   same flow, another channel
//
// Why not reuse git/ssh as transport (the rejected path): the host sits behind
// tailscale and must support a proxy host (jump), and restricted nodes forbid inbound -
// ssh's trust model does not hold behind a jump chain. So transport is our own
// **outbound-only** pull. git stays entirely on the host for versioning/rollback.
//
// # Three properties that run through the package
//
//  1. **Replicas are outbound-only**. No code connects to a replica; restricted nodes
//     (no inbound) can be replicas too.
//  2. **fail-open**. Any failure (network, decrypt, schema, drift) keeps the current
//     config serving. A dead host means everyone forwards as usual - no single point.
//  3. **Never silent**. A refusal says which files and why, and never floods.
package proto

// Wire constants.
const (
	// ProtocolVersion is the envelope format version. Bump on structural change; it is
	// bound into the AEAD's AAD (seal.go), so mismatched versions show as **decrypt
	// failure**, not half a struct - no compatibility guessing; both ends move together.
	ProtocolVersion = 1

	// SupportedSchema is the highest config schema this binary can interpret correctly.
	//
	// Why it is the **only version field with teeth**: the other field,
	// min_newgate_version, holds a `git describe` string (`build-17-...-gf90dc81`),
	// which has **no numeric ordering** against a future `build-18-...`, so it can
	// only be compared for equality. schema is a hand-bumped integer and does order.
	SupportedSchema = 1

	// The two channel names: the envelope's Kind (bound into AAD) and a path segment.
	KindConfig  = "config"
	KindSecrets = "secrets"

	// PathConfig / PathSecrets are the two endpoints on the host daemon.
	//
	// Two rather than one: config changes often (adding a provider), secrets rarely
	// (monthly); caching and frequency differ, and audits should too. Merged into one,
	// every provider edit would resend every secret.
	PathConfig  = "/__newgate/config"
	PathSecrets = "/__newgate/secrets"
)

// Envelope is what travels over the endpoint: all plaintext in CT, only routing and Gen outside.
//
// Why Gen is plaintext and still safe: a replica must **not even decrypt** when the
// generation is unchanged (idle is fully silent, see poller.go), so it must read Gen
// first. And Gen is bound into the AAD, so lying fails authentication - an attacker
// can only claim an old generation so this replica skips a round: denial of service,
// not key leakage, and the link already has bearer auth plus AEAD.
//
// Host is plaintext for **correctness**, not convenience: "did the authority change"
// must be decidable before decrypting. On such a change the new host's generation
// restarts at 0, and a replica comparing generations alone would think "older than
// what I applied" and **refuse updates forever**, silent, fixed only by reinstalling
// the host. Plaintext Host (also in the AAD, not forgeable) shows that to the fast path.
type Envelope struct {
	V     int    `json:"v"`
	Kind  string `json:"kind"`
	Host  string `json:"host"`
	Gen   int    `json:"gen"`
	Nonce []byte `json:"nonce"`
	CT    []byte `json:"ct"`
}

// Snapshot is one config snapshot a host publishes (the plaintext JSON in the envelope).
//
// Files' values are []byte rather than string: content goes over JSON as base64 and is
// written **byte for byte**, with no JSON round trip. This repo was bitten by "JSON
// round trips change bytes" (big integers becoming `...92`, field order and unknown
// keys lost, CLAUDE.md §4), and a snapshot tolerates that least.
//
// Keys are **slash paths relative to the config dir** (`providers.json`,
// `mappings/prod.json`), constrained by the allowlist (managed.go), and also passing
// os.Root on write - the payload is from the network, so names must not escape.
type Snapshot struct {
	// HostID is the authority identity, generated on the host's first start and stable.
	//
	// Why: Generation is a monotonic counter, and if the host's ledger is lost (reinstall,
	// new machine, deleted host.json) the count restarts at 0. Without HostID a replica
	// would think every new snapshot is older and **refuse updates forever**, silent.
	// With it, an authority change is recognized and the replica resets its baseline.
	HostID     string            `json:"host_id"`
	Generation int               `json:"generation"`
	Version    string            `json:"version,omitempty"` // 内容哈希，审计与"变了没有"用
	Files      map[string][]byte `json:"files"`
}

// Secrets is the key set a host publishes (another channel's plaintext).
//
// Why a separate channel rather than inside the config: `providers.json` is git-managed
// on the host, so an api_key committed once stays in history forever. So the config
// channel **carries no secrets**; adding a key takes one host action, once.
type Secrets struct {
	HostID     string            `json:"host_id"`
	Generation int               `json:"generation"`
	Keys       map[string]string `json:"keys"` // provider 名 → 明文 key
}
