package proto

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	paths "github.com/rzbdz/newgate-ext/modules/config/paths"
	"github.com/rzbdz/newgate/lib/i18n"
)

// maxEnvelopeBytes is the size cap on a single pull.
//
// A config snapshot is tens of KB of JSON, secrets less. The cap exists because **the
// peer's behavior is not ours to control** (a jump chain or proxy host may sit in
// between, and the endpoint may be misconfigured): without one, a single pull could eat
// all the daemon's memory. 8 MiB is "far from reality, and far from danger".
const maxEnvelopeBytes = 8 << 20

// Two pull failures that must speak plainly. Both are **first-minute** errors (no role
// set, blob copied wrong), so the text gives the direction to look, not a status code.
//
// i18n.E is used, not i18n.T: E stores only the source text (message identity), and
// rendering happens when Error() is called, i.e. after languages are installed. i18n.T
// in a package-level variable would freeze the translation on the source language.
var (
	ErrNotHost = i18n.E("the peer is not a host (the endpoint returned 404) — is its role not set to host?", nil)
	ErrAuth    = i18n.E("authentication refused: this machine's root key differs from the host's (the config trust blob was copied wrong or not at all)", nil)
)

// Config is everything needed to build a replica client.
//
// Paths are not here: they all come from the paths package (the single source of config
// dir paths in this repo); tests isolate via NEWGATE_HOME (the testkit.Sandbox way).
type Config struct {
	Endpoint string // 形如 https://gateway-host.tailnet.ts.net:8899
	RootKey  []byte // 预共享根密钥原文
	Version  string // 本二进制构建串，喂给 Gate
	Log      *log.Logger
	HTTP     *http.Client // nil = 默认（20s 超时）
	Now      func() time.Time
}

// Client is the replica-side puller and applier.
type Client struct {
	cfg       Config
	encKey    []byte
	authToken []byte
	http      *http.Client
	now       func() time.Time
}

// New derives the keys and builds the client.
func New(cfg Config) (*Client, error) {
	if strings.TrimSpace(cfg.Endpoint) == "" {
		return nil, i18n.E("the endpoint is empty (run newgate config join <endpoint> first)", nil)
	}
	encKey, token, err := DeriveKeys(cfg.RootKey)
	if err != nil {
		return nil, err
	}
	c := &Client{cfg: cfg, encKey: encKey, authToken: token, http: cfg.HTTP}
	if c.http == nil {
		c.http = &http.Client{Timeout: 20 * time.Second}
	}
	c.now = cfg.Now
	if c.now == nil {
		c.now = time.Now
	}
	return c, nil
}

// Channel is one channel's result for this round.
type Channel struct {
	Kind string
	Gen  int // 这一轮之后生效的代数

	// Changed: content changed and is on disk. **Only then log "updated"**: a generation
	// bump with identical bytes (host re-published the same content) is not an update.
	Changed   bool
	Refused   bool // 拉到了、但拒绝应用（保留上一份继续服务）
	Reason    string
	Err       error // 这一通道自身的错误（网络、解密、校验）
	Drift     *DriftReport
	Written   []string
	Removed   []string
	Unchanged int
	Archive   string
	Warnings  []string
}

// Outcome is one pull round's result.
//
// The two channels are independent: a flaky config endpoint must not stall secrets
// rotation, and vice versa. So errors are per channel; the error return is global only.
type Outcome struct {
	Config  Channel
	Secrets Channel
	Notes   []string // 给调用方展示的旁注（记账损坏、权威变更…）

	// The fields below are what finalize sums up as "how this round went overall"; callers
	// only **render** it - recording and judging happen inside Pull's lock, see finalize.
	Failed    bool
	ErrText   string // 稳定的错误串（同一问题重复出现时字符串不变，日志抑制靠它）
	FailCount int    // 这一轮之后连续失败的次数
	Recovered bool   // 这一轮恢复（此前有失败）
	PrevGen   int    // 这一轮之前生效的配置代数（日志里写 "41 → 42" 用）
}

// Pull pulls one round and applies it.
//
// The order is **config first, then secrets**: the config channel updates the ledger's
// authoritative identity (HostID), and the secrets channel then uses the same identity
// to decide "did the authority change"; reversed, a needless extra baseline reset occurs.
func (c *Client) Pull(ctx context.Context, force bool) (Outcome, error) {
	var out Outcome
	run := func() error {
		st, err := LoadState()
		if err != nil {
			// A broken ledger must not stall sync entirely: restart from the initial state, but say so.
			out.Notes = append(out.Notes, err.Error())
			c.logf("[configshare] %v", err)
		}
		out.PrevGen = st.Generation
		out.Config = c.pullConfig(ctx, &st, force)
		out.Secrets = c.pullSecrets(ctx, &st, force)
		out.finalize(&st, c.now())
		return SaveState(st)
	}
	// Take the lock for the whole round: CLI config pull and the background poller can
	// overlap on the ledger. Wait 3s (manual patience); the background one uses wait=0.
	ran, err := WithLock(ConfigShareLockFile(), 3*time.Second, run)
	if err != nil {
		return out, err
	}
	if !ran {
		out.Notes = append(out.Notes, i18n.T("another sync round is already running; this round was skipped", nil))
	}
	return out, nil
}

// finalize sums up the round and writes the failure count into the ledger.
//
// Why inside Pull rather than once per caller: only a **single writer** keeps the ledger
// from overwriting itself, and Pull is already in the lock. Callers (background poller /
// config pull) only render this result in their own tone.
//
// ErrText's stability is intentional: repeated instances of one problem must give the
// identical string, or the poller's log suppression ("same error only at 1/2/5/10/25/50...")
// breaks and it spews a line every 30 seconds. So rejection text **carries no file names**.
func (o *Outcome) finalize(st *State, now time.Time) {
	var msgs []string
	for _, ch := range []Channel{o.Config, o.Secrets} {
		switch {
		case ch.Err != nil:
			msgs = append(msgs, ch.Err.Error())
		case ch.Refused:
			msgs = append(msgs, ch.Reason)
		}
	}
	if len(msgs) == 0 {
		if st.FailCount > 0 {
			o.Recovered = true
		}
		st.FailCount = 0
		st.LastError = ""
		st.LastSuccessAt = now
		return
	}
	o.Failed = true
	// The separator goes through i18n (full-width semicolon in Chinese). ErrText stability
	// is unaffected: one language per process, so the joined string stays identical.
	o.ErrText = strings.Join(msgs, i18n.T("; ", nil))
	st.FailCount++
	st.LastError = o.ErrText
	st.LastErrorAt = now
	o.FailCount = st.FailCount
}

// pullConfig is one round of the config channel.
func (c *Client) pullConfig(ctx context.Context, st *State, force bool) Channel {
	ch := Channel{Kind: KindConfig}
	env, err := c.fetch(ctx, KindConfig, PathConfig)
	if err != nil {
		ch.Err = err
		return ch
	}
	ch.Gen = env.Gen

	// ---- fast path: authority unchanged, generation unchanged ----
	//
	// No decryption, no disk write, no log. This is where "idle is fully silent" happens:
	// the vast majority of the 2880 rounds a day leave right here.
	//
	// The `<=` rather than `==` is a **monotonicity guard**: a late, reordered, or
	// man-in-the-middle-replayed old snapshot must not roll the fleet back. So a
	// **deliberate rollback** on the host does not propagate - it needs a generation
	// bump (host-side `newgate config publish` does exactly that, see host.go). That
	// cost is accepted: a silent fleet-wide downgrade is far worse than a rollback.
	if st.HostID == env.Host && env.Gen <= st.Generation {
		ch.Gen = st.Generation
		return ch
	}

	plain, err := Open(c.encKey, KindConfig, env)
	if err != nil {
		ch.Err = err
		return ch
	}
	var snap Snapshot
	if err := json.Unmarshal(plain, &snap); err != nil {
		ch.Err = i18n.Ef(err, "cannot parse the snapshot: {err}", nil)
		return ch
	}
	if snap.HostID == "" {
		snap.HostID = env.Host // 宿主没填就用信封上那个（两者本应一致）
	}
	if snap.Generation != env.Gen {
		// Envelope says 42, ciphertext says 41: host-side assembly bug. One number; no guessing.
		ch.Err = i18n.E("the envelope generation {envelope} does not match the generation inside the snapshot {snapshot} (the host assembled it wrong)",
			i18n.A{"envelope": env.Gen, "snapshot": snap.Generation})
		return ch
	}
	if st.HostID != "" && st.HostID != snap.HostID {
		out := i18n.T("authority changed ({from} → {to}); the generation baseline was reset",
			i18n.A{"from": short(st.HostID), "to": short(snap.HostID)})
		ch.Warnings = append(ch.Warnings, out)
		c.logf("[configshare] %s", out)
		// Reset the baseline: a new host counts from 0; without it we never catch up (see types.go).
		st.Generation = 0
	}

	// ---- validation: schema gate -> content shape ----
	meta, err := ParseMeta(snap.Files)
	if err != nil {
		ch.Refused, ch.Reason, ch.Err = true, err.Error(), err
		return ch
	}
	ok, warn, refuse := Gate(meta, GateInput{CurrentVersion: c.cfg.Version})
	if warn != "" {
		ch.Warnings = append(ch.Warnings, warn)
	}
	if !ok {
		// Refuse to apply, **not** an error: this is no fault, just a clear "I can't
		// parse this one". Keep the previous one serving; status lists it as to handle.
		ch.Refused, ch.Reason = true, refuse
		return ch
	}
	warns, err := ValidateSnapshot(paths.Config(), snap.Files)
	ch.Warnings = append(ch.Warnings, warns...)
	if err != nil {
		ch.Refused, ch.Reason, ch.Err = true, err.Error(), err
		return ch
	}

	// ---- drift guard ----
	drift := DetectDrift(paths.Config(), st.Applied, snap.Files)
	if drift.Blocking() {
		ch.Drift = &drift
		if !force {
			ch.Refused = true
			ch.Reason = i18n.N(
				"{n} managed file on this machine was modified locally; applying the remote configuration stopped (your changes will not be overwritten)",
				"{n} managed files on this machine were modified locally; applying the remote configuration stopped (your changes will not be overwritten)",
				len(drift.Paths()), i18n.A{"n": len(drift.Paths())})
			// The ledger **doesn't move**: the generation holds, so the next round retries the
			// decision - after the user reverts or passes --force, the same generation is reapplied.
			st.Drift = &drift
			return ch
		}
		arch, err := ArchiveFiles(paths.Config(), stamp(c.now()), drift.Paths())
		if err != nil {
			ch.Refused, ch.Reason, ch.Err = true, err.Error(), err
			return ch
		}
		ch.Archive = arch
	}

	// ---- write to disk ----
	res, err := Materialize(paths.Config(), snap.Files, st.Applied)
	if err != nil {
		ch.Refused, ch.Reason, ch.Err = true, err.Error(), err
		return ch
	}
	ch.Written, ch.Removed, ch.Unchanged = res.Written, res.Removed, len(res.Unchanged)
	ch.Changed = len(res.Written)+len(res.Removed) > 0

	// ---- ledger ----
	st.HostID = snap.HostID
	st.Generation = env.Gen
	st.Version = snap.Version
	st.AppliedAt = c.now()
	st.Applied = res.Manifest
	st.Warnings = ch.Warnings
	// Drift in the ledger must describe **now**, not how it looked before we just fixed it:
	// recompute after writing to disk. After --force the blockers are archived and
	// overwritten, so the recompute is clean; the local layer (Extra) still shows up -
	// it should always be visible (hiding it is the silent-override trap).
	st.Drift = driftOrNil(DetectDrift(paths.Config(), res.Manifest, snap.Files))
	return ch
}

// pullSecrets is one round of the secrets channel.
func (c *Client) pullSecrets(ctx context.Context, st *State, force bool) Channel {
	ch := Channel{Kind: KindSecrets}
	env, err := c.fetch(ctx, KindSecrets, PathSecrets)
	if err != nil {
		ch.Err = err
		return ch
	}
	ch.Gen = env.Gen
	if st.HostID == env.Host && env.Gen <= st.SecretsGen {
		ch.Gen = st.SecretsGen
		return ch
	}
	plain, err := Open(c.encKey, KindSecrets, env)
	if err != nil {
		ch.Err = err
		return ch
	}
	var sec Secrets
	if err := json.Unmarshal(plain, &sec); err != nil {
		ch.Err = i18n.Ef(err, "cannot parse the secret set: {err}", nil)
		return ch
	}
	if sec.Generation != 0 && sec.Generation != env.Gen {
		ch.Err = i18n.E("the envelope generation {envelope} does not match the generation inside the secret set {secrets}",
			i18n.A{"envelope": env.Gen, "secrets": sec.Generation})
		return ch
	}
	want, err := secretsBytes(sec.Keys)
	if err != nil {
		ch.Err = err
		return ch
	}
	sum := hashBytes(want)

	// Drift guard, same rationale as the config channel: the local secrets.json was
	// hand-edited while the remote wants a different one - that erases someone's change,
	// so report and archive first. A contract is no license for a silent overwrite.
	path := secretsPath()
	if cur, err := readFileIfExists(path); err == nil && cur != nil {
		curSum := hashBytes(cur)
		if curSum != st.SecretsApplied && curSum != sum {
			dr := &DriftReport{Modified: []string{SecretsFileName}, At: c.now()}
			ch.Drift = dr
			if !force {
				ch.Refused = true
				ch.Reason = i18n.T("this machine's {name} was modified locally (your changes will not be overwritten)",
					i18n.A{"name": SecretsFileName})
				st.SecretsDrift = dr
				return ch
			}
			arch, err := ArchiveFiles(paths.Config(), stamp(c.now()), []string{SecretsFileName})
			if err != nil {
				ch.Refused, ch.Reason, ch.Err = true, err.Error(), err
				return ch
			}
			ch.Archive = arch
		}
	}

	written, err := MaterializeSecrets(path, sec.Keys)
	if err != nil {
		ch.Refused, ch.Reason, ch.Err = true, err.Error(), err
		return ch
	}
	ch.Changed = written != st.SecretsApplied

	st.SecretsGen = env.Gen
	st.SecretsApplied = written
	st.SecretsAt = c.now()
	// The write succeeded, so the secrets channel's drift is resolved (--force already archived).
	st.SecretsDrift = nil
	if sec.HostID != "" {
		st.HostID = sec.HostID
	}
	return ch
}

// fetch fetches the envelope at one endpoint.
func (c *Client) fetch(ctx context.Context, kind, path string) (*Envelope, error) {
	url := strings.TrimRight(c.cfg.Endpoint, "/") + path
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	// token is **another** HKDF-derived value, not the encryption key: it goes into headers,
	// logs, any middleware, and none of those can leak the encryption key (see
	// DeriveKeys in seal.go). Hex, not raw bytes: headers carry visible ASCII only.
	req.Header.Set("Authorization", "Bearer "+hex.EncodeToString(c.authToken))
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, i18n.Ef(err, "cannot reach {url}: {err}", i18n.A{"url": url})
	}
	defer resp.Body.Close()

	switch {
	case resp.StatusCode == http.StatusOK:
	case resp.StatusCode == http.StatusNotFound:
		return nil, ErrNotHost
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return nil, ErrAuth
	default:
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, i18n.E("{url} returned {status}: {body}",
			i18n.A{"url": url, "status": resp.Status, "body": strings.TrimSpace(string(body))})
	}

	var env Envelope
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxEnvelopeBytes)).Decode(&env); err != nil {
		return nil, i18n.Ef(err, "the response from {url} is not a valid envelope: {err}", i18n.A{"url": url})
	}
	return &env, nil
}

func (c *Client) logf(format string, args ...any) {
	if c.cfg.Log != nil {
		c.cfg.Log.Printf(format, args...)
	}
}

// short truncates host_id for logging (it is 32 hex chars; the whole string is useless).
func short(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	if id == "" {
		return i18n.T("(none)", nil)
	}
	return id
}
