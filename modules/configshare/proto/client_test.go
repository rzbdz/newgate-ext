package proto

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	paths "github.com/rzbdz/newgate-ext/modules/config/paths"
	"github.com/rzbdz/newgate/lib/i18n"
)

func rootKey(seed byte) []byte { return bytes.Repeat([]byte{seed}, RootKeyLen) }

func mustPull(t *testing.T, c *Client, force bool) Outcome {
	t.Helper()
	out, err := c.Pull(context.Background(), force)
	if err != nil {
		t.Fatalf("Pull failed: %v", err)
	}
	return out
}

func anyContains(ss []string, sub string) bool {
	for _, s := range ss {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

// backupCount counts backups/configshare-* entries (archiving should only happen on --force).
func backupCount(t *testing.T) int {
	t.Helper()
	m, err := filepath.Glob(filepath.Join(paths.BackupDir(), "configshare-*"))
	if err != nil {
		t.Fatal(err)
	}
	return len(m)
}

func TestPullAppliesSnapshot(t *testing.T) {
	sandbox(t)
	root := rootKey(1)
	h := newFakeHost(t, root)
	h.setConfig(1, snapshotFiles())
	h.setSecrets(1, map[string]string{"smt-deepseek": "sk-1"})
	c := newTestClient(t, h, root)

	out := mustPull(t, c, false)
	if out.Failed {
		t.Fatalf("the first round must not fail: %s", out.ErrText)
	}
	if !out.Config.Changed || len(out.Config.Written) != 3 {
		t.Fatalf("the first round should write 3 files, got changed=%v written=%v",
			out.Config.Changed, out.Config.Written)
	}
	for rel := range snapshotFiles() {
		if !existsLive(rel) {
			t.Fatalf("%s was not written", rel)
		}
	}
	if !out.Secrets.Changed {
		t.Fatal("the secrets should be written on the first round")
	}
	if v, ok := LookupSecret(secretsPath(), "smt-deepseek"); !ok || v != "sk-1" {
		t.Fatalf("secrets were not written: %q %v", v, ok)
	}

	st, err := LoadState()
	if err != nil {
		t.Fatal(err)
	}
	if st.Generation != 1 || st.HostID != h.hostID() {
		t.Fatalf("wrong bookkeeping: gen=%d host=%q", st.Generation, st.HostID)
	}
	if st.SecretsGen != 1 {
		t.Fatalf("secrets generation not recorded: %d", st.SecretsGen)
	}
	if len(st.Applied) != 3 {
		t.Fatalf("the applied manifest should have 3 entries (the baseline for the "+
			"drift guard), got %v", st.Applied)
	}
	if st.FailCount != 0 || st.LastError != "" {
		t.Fatalf("a successful round must not leave failure bookkeeping: %+v", st)
	}
}

// TestPullFastPathDoesNotDecrypt makes "idle is fully silent" testable.
//
// Trick: round 2 serves an envelope that **cannot be decrypted at all**, with generation
// and authority unchanged, so the fast path (no decrypt when generation matches) is
// unaffected; a secret decrypt would raise ErrTampered - locking both properties.
func TestPullFastPathDoesNotDecrypt(t *testing.T) {
	sandbox(t)
	root := rootKey(1)
	h := newFakeHost(t, root)
	h.setConfig(2, snapshotFiles())
	c := newTestClient(t, h, root)
	mustPull(t, c, false)

	garbage, _ := json.Marshal(Envelope{
		V: ProtocolVersion, Kind: KindConfig, Host: h.hostID(), Gen: 2,
		Nonce: make([]byte, 12), CT: []byte("this is not ciphertext"),
	})
	h.setRawConfig(garbage)

	out := mustPull(t, c, false)
	if out.Config.Err != nil || out.Failed {
		t.Fatalf("an unchanged generation must not decrypt, and so must not error: "+
			"%v / %s", out.Config.Err, out.ErrText)
	}
	if out.Config.Changed {
		t.Fatal("an unchanged generation must not write to disk")
	}
}

// TestPullRejectsOlderGeneration is the monotonicity guard: a late, reordered or
// man-in-the-middle-replayed old snapshot must not roll the fleet back.
//
// The price (accepted on purpose) is that a **deliberate rollback** on the host does
// not propagate - rolling back means publishing a higher generation on the host. A
// channel that can silently downgrade the fleet is far more dangerous than that.
func TestPullRejectsOlderGeneration(t *testing.T) {
	sandbox(t)
	root := rootKey(1)
	h := newFakeHost(t, root)
	newer := snapshotFiles()
	newer[ProvidersName] = []byte(`{"providers":{"fifth generation":{"base_url":"https://five"}}}`)
	h.setConfig(5, newer)
	c := newTestClient(t, h, root)
	mustPull(t, c, false)

	older := snapshotFiles()
	older[ProvidersName] = []byte(`{"providers":{"third generation":{"base_url":"https://three"}}}`)
	h.setConfig(3, older)

	out := mustPull(t, c, false)
	if out.Config.Changed {
		t.Fatal("an older generation must not be applied")
	}
	if strings.Contains(string(readLive(t, ProvidersName)), "third generation") {
		t.Fatal("old content was written - the fleet was rolled back")
	}
	if !strings.Contains(string(readLive(t, ProvidersName)), "fifth generation") {
		t.Fatal("the original content should be kept as is")
	}
	if st, _ := LoadState(); st.Generation != 5 {
		t.Fatalf("the recorded generation went backwards: %d", st.Generation)
	}
}

// TestPullHostChangeResetsBaseline pins why HostID exists.
//
// When the host's ledger file is lost (reinstall, new machine, deleted host.json) the
// generation restarts at 0. Comparing generations alone, a replica would consider every
// new snapshot older and **refuse updates forever** - a silent, reinstall-only failure.
func TestPullHostChangeResetsBaseline(t *testing.T) {
	sandbox(t)
	root := rootKey(1)
	h := newFakeHost(t, root)
	first := snapshotFiles()
	first[ProvidersName] = []byte(`{"providers":{"old host":{"base_url":"https://old"}}}`)
	h.setConfig(5, first)
	c := newTestClient(t, h, root)
	mustPull(t, c, false)

	// The authority changed; the new host's count starts at 2 (below 5).
	h.setHostID("ffffffffffffffffffffffffffffffff")
	second := snapshotFiles()
	second[ProvidersName] = []byte(`{"providers":{"new host":{"base_url":"https://new"}}}`)
	h.setConfig(2, second)

	out := mustPull(t, c, false)
	if !out.Config.Changed {
		t.Fatalf("a new authority should be applied even with a smaller generation. got %+v", out.Config)
	}
	if !strings.Contains(string(readLive(t, ProvidersName)), "new host") {
		t.Fatal("the new authority content was not written")
	}
	if !anyContains(out.Config.Warnings, "authority changed") {
		t.Fatalf("an authority change must be announced (otherwise a sudden "+
			"generation reset makes no sense): %v", out.Config.Warnings)
	}
	st, _ := LoadState()
	if st.Generation != 2 || !strings.HasPrefix(st.HostID, "ffff") {
		t.Fatalf("bookkeeping did not follow the new authority: gen=%d host=%q", st.Generation, st.HostID)
	}
}

// TestPullSchemaTooNewRefusedAndKeepsPrevious is the toothy level of the two gates.
//
// A higher schema = field meanings may have changed, and **misreading is not loud**
// (wrong upstream, or "provider has no api_key" then 404, looking like an outage). So
// refuse to apply, keep the previous one - still fail-open: the machine keeps serving.
func TestPullSchemaTooNewRefusedAndKeepsPrevious(t *testing.T) {
	sandbox(t)
	root := rootKey(1)
	h := newFakeHost(t, root)
	h.setConfig(1, snapshotFiles())
	c := newTestClient(t, h, root)
	mustPull(t, c, false)

	next := snapshotFiles()
	next[ProvidersName] = []byte(`{"providers":{"future format":{"base_url":"https://future"}}}`)
	next[MetaFileName] = metaFile(SupportedSchema+1, "")
	h.setConfig(2, next)

	out := mustPull(t, c, false)
	if !out.Config.Refused {
		t.Fatalf("a higher schema must be rejected, got %+v", out.Config)
	}
	if !strings.Contains(out.Config.Reason, "schema_version") {
		t.Fatalf("the refusal reason should name schema_version: %q", out.Config.Reason)
	}
	if strings.Contains(string(readLive(t, ProvidersName)), "future format") {
		t.Fatal("it was refused, yet it wrote to disk")
	}
	if !strings.Contains(string(readLive(t, ProvidersName)), "smt-deepseek") {
		t.Fatal("the previous config should be kept as is (fail-open: the machine keeps serving)")
	}
	// The generation **does not advance**: after upgrade the snapshot is reapplied, not "handled".
	if st, _ := LoadState(); st.Generation != 1 {
		t.Fatalf("the generation must not advance on refusal, or after the upgrade this "+
			"config would never arrive again: %d", st.Generation)
	}
	// This is a failure (back off, show it), but not a "fault": Err should be empty.
	if out.Config.Err != nil {
		t.Fatalf("a schema refusal is a definite verdict, not a fault: %v", out.Config.Err)
	}
	if !out.Failed {
		t.Fatal("refusing to apply must count as not converged (it backs off, and status must show it)")
	}
}

// TestPullVersionMismatchWarnsButApplies is the other level: build string mismatch =>
// allow + warn. Hard refusal would starve a working machine of provider updates.
func TestPullVersionMismatchWarnsButApplies(t *testing.T) {
	sandbox(t)
	root := rootKey(1)
	h := newFakeHost(t, root)
	next := snapshotFiles()
	next[MetaFileName] = metaFile(SupportedSchema, "build-99-other-build")
	h.setConfig(1, next)
	c := newTestClient(t, h, root)

	out := mustPull(t, c, false)
	if out.Config.Refused {
		t.Fatalf("a build string mismatch must not be refused: %s", out.Config.Reason)
	}
	if !out.Config.Changed {
		t.Fatal("it should be applied as usual")
	}
	if !anyContains(out.Config.Warnings, "build-99-other-build") {
		t.Fatalf("the build string mismatch must be warned about: %v", out.Config.Warnings)
	}
}

// TestPullDriftRefusedThenForced is the most important one in commit 1: the drift guard.
//
// The failure mode to stop: someone hand-edits providers.json on a replica, believes it
// took effect, and the next poll **silently** overwrites it. This test pins three
// actions: refuse (no write, no archive); drift left in the ledger; --force archives first.
func TestPullDriftRefusedThenForced(t *testing.T) {
	sandbox(t)
	root := rootKey(1)
	h := newFakeHost(t, root)
	h.setConfig(1, snapshotFiles())
	c := newTestClient(t, h, root)
	mustPull(t, c, false)

	local := []byte(`{"providers":{"local-private":{"base_url":"http://127.0.0.1:1234"}}}`)
	writeLive(t, ProvidersName, local)
	remote := snapshotFiles()
	remote[ProvidersName] = []byte(`{"providers":{"remote":{"base_url":"https://remote"}}}`)
	h.setConfig(2, remote)

	// ---- round 1: refuse ----
	out := mustPull(t, c, false)
	if !out.Config.Refused {
		t.Fatalf("with local edits it must refuse to apply, got %+v", out.Config)
	}
	if out.Config.Drift == nil || !out.Config.Drift.Blocking() {
		t.Fatal("it should report blocking drift")
	}
	if !bytes.Equal(readLive(t, ProvidersName), local) {
		t.Fatal("it was refused, yet it touched the disk")
	}
	if n := backupCount(t); n != 0 {
		t.Fatalf("a refusal must not create an archive directory (the user's files "+
			"are untouched), got %d", n)
	}
	st, _ := LoadState()
	if st.Generation != 1 {
		t.Fatalf("the generation must not advance on refusal (otherwise this one is never applied "+
			"again once the user reverts): %d", st.Generation)
	}
	if st.Drift == nil || !st.Drift.Blocking() {
		t.Fatal("drift is persistent state and must stay in the ledger for status to show")
	}

	// ---- round 2: --force ----
	out = mustPull(t, c, true)
	if !out.Config.Changed {
		t.Fatalf("--force must apply, got %+v", out.Config)
	}
	if !bytes.Contains(readLive(t, ProvidersName), []byte("remote")) {
		t.Fatal("the remote content was not written")
	}
	if out.Config.Archive == "" {
		t.Fatal("--force must archive first: otherwise this command is unrecoverable")
	}
	got, err := os.ReadFile(filepath.Join(out.Config.Archive, ProvidersName))
	if err != nil || !bytes.Equal(got, local) {
		t.Fatalf("the archive should hold the user's original: %q / %v", got, err)
	}
	st, _ = LoadState()
	if st.Drift != nil {
		t.Fatal("drift must be cleared after a successful apply")
	}
	if st.Generation != 2 {
		t.Fatalf("a successful apply should advance the generation to 2, got %d", st.Generation)
	}
}

func TestPullSecretsRotation(t *testing.T) {
	sandbox(t)
	root := rootKey(1)
	h := newFakeHost(t, root)
	h.setConfig(1, snapshotFiles())
	h.setSecrets(1, map[string]string{"smt-deepseek": "sk-1"})
	c := newTestClient(t, h, root)
	mustPull(t, c, false)

	// Rotation: change one, add one.
	h.setSecrets(2, map[string]string{"smt-deepseek": "sk-2", "glm": "sk-3"})
	out := mustPull(t, c, false)
	if !out.Secrets.Changed {
		t.Fatalf("changed secrets must be written, got %+v", out.Secrets)
	}
	// Config unchanged -> the config channel must be fully silent (the core value of two
	// independent channels: changing a provider must not resend all secrets, and vice versa).
	if out.Config.Changed {
		t.Fatal("the config did not change yet was written again: the two channels must count separately")
	}
	for prov, want := range map[string]string{"smt-deepseek": "sk-2", "glm": "sk-3"} {
		if v, ok := LookupSecret(secretsPath(), prov); !ok || v != want {
			t.Fatalf("wrong secret for %s: %q %v", prov, v, ok)
		}
	}
	if st, _ := LoadState(); st.SecretsGen != 2 {
		t.Fatalf("the secrets generation did not advance: %d", st.SecretsGen)
	}
}

// TestPullSecretsDriftRefused: "secrets come only from the host" is its contract, but
// a contract is no reason to silently overwrite local edits. Same guard.
func TestPullSecretsDriftRefused(t *testing.T) {
	sandbox(t)
	root := rootKey(1)
	h := newFakeHost(t, root)
	h.setConfig(1, snapshotFiles())
	h.setSecrets(1, map[string]string{"smt-deepseek": "sk-1"})
	c := newTestClient(t, h, root)
	mustPull(t, c, false)

	edited := map[string]string{"smt-deepseek": "sk-locally-edited"}
	if _, err := MaterializeSecrets(secretsPath(), edited); err != nil {
		t.Fatal(err)
	}
	h.setSecrets(2, map[string]string{"smt-deepseek": "sk-new"})

	out := mustPull(t, c, false)
	if !out.Secrets.Refused {
		t.Fatalf("a hand-edited local secrets.json must be refused, got %+v", out.Secrets)
	}
	if v, _ := LookupSecret(secretsPath(), "smt-deepseek"); v != "sk-locally-edited" {
		t.Fatalf("it was refused, yet it overwrote the local edit: %q", v)
	}
	if out.Failed != true {
		t.Fatal("refusing to apply must count as not converged")
	}
}

func TestPullErrors(t *testing.T) {
	sandbox(t)
	root := rootKey(1)

	t.Run("the peer is not the host", func(t *testing.T) {
		h := newFakeHost(t, root)
		h.setConfig(1, snapshotFiles())
		h.setNotFound(true)
		out := mustPull(t, newTestClient(t, h, root), false)
		if !errors.Is(out.Config.Err, ErrNotHost) {
			t.Fatalf("expected ErrNotHost, got %v", out.Config.Err)
		}
	})

	t.Run("a different root key (wrong blob copied)", func(t *testing.T) {
		h := newFakeHost(t, root)
		h.setConfig(1, snapshotFiles())
		// The client trusted another blob: the bearer token does not match -> 401.
		// The text must point at the root key, else users will suspect the network.
		out := mustPull(t, newTestClient(t, h, rootKey(2)), false)
		if !errors.Is(out.Config.Err, ErrAuth) {
			t.Fatalf("expected ErrAuth, got %v", out.Config.Err)
		}
		if !strings.Contains(i18n.ID(ErrAuth), "root key") {
			t.Fatalf("the ErrAuth text must mention the root key: %v", ErrAuth)
		}
	})

	t.Run("the envelope was tampered with", func(t *testing.T) {
		h := newFakeHost(t, root)
		encKey, _, _ := DeriveKeys(root)
		plain, _ := json.Marshal(Snapshot{HostID: h.hostID(), Generation: 1, Files: snapshotFiles()})
		env, err := Seal(encKey, KindConfig, h.hostID(), 1, plain)
		if err != nil {
			t.Fatal(err)
		}
		env.CT[0] ^= 0x01
		raw, _ := json.Marshal(env)
		h.setRawConfig(raw)

		out := mustPull(t, newTestClient(t, h, root), false)
		if !errors.Is(out.Config.Err, ErrTampered) {
			t.Fatalf("tampering must be detected, got %v", out.Config.Err)
		}
		if existsLive(ProvidersName) {
			t.Fatal("content that fails verification must never be written")
		}
	})

	t.Run("the response is not an envelope", func(t *testing.T) {
		h := newFakeHost(t, root)
		h.setRawConfig([]byte("<html>this is not JSON</html>"))
		out := mustPull(t, newTestClient(t, h, root), false)
		if out.Config.Err == nil {
			t.Fatal("a response that is not an envelope must error")
		}
	})
}

// TestPullRefusesUnmanagedFileInSnapshot: the payload comes from the network, so a
// compromised (or broken) host must not be able to write state.json, dump/ or beyond.
func TestPullRefusesUnmanagedFileInSnapshot(t *testing.T) {
	sandbox(t)
	root := rootKey(1)
	h := newFakeHost(t, root)
	files := snapshotFiles()
	files["state.json"] = []byte(`{"default_profile":"hijacked"}`)
	files["../escape.json"] = []byte("{}")
	h.setConfig(1, files)
	c := newTestClient(t, h, root)

	out := mustPull(t, c, false)
	if !out.Config.Refused {
		t.Fatalf("a file outside the managed set must be refused, got %+v", out.Config)
	}
	if existsLive("state.json") {
		t.Fatal("state.json was written (that is machine-local state)")
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(paths.Config()), "escape.json")); err == nil {
		t.Fatal("it wrote outside the config directory")
	}
}

func TestPullInlineKeyRefused(t *testing.T) {
	sandbox(t)
	root := rootKey(1)
	h := newFakeHost(t, root)
	files := snapshotFiles()
	files[ProvidersName] = []byte(`{"providers":{"x":` +
		`{"base_url":"https://x","api_key":"sk-cleartext"}}}`)
	h.setConfig(1, files)

	out := mustPull(t, newTestClient(t, h, root), false)
	if !out.Config.Refused {
		t.Fatalf("an inline key must be rejected: %+v", out.Config)
	}
	if !strings.Contains(out.Config.Reason, "secrets channel") {
		t.Fatalf("the refusal reason should say which path the key must take: %q", out.Config.Reason)
	}
}

// TestPullRemovesRetiredFiles is the flip side of "write-only, never delete": after the
// host retires a tier, the replica must not keep it (or @that-tier refs would resolve).
func TestPullRemovesRetiredFiles(t *testing.T) {
	sandbox(t)
	root := rootKey(1)
	h := newFakeHost(t, root)
	h.setConfig(1, snapshotFiles())
	c := newTestClient(t, h, root)
	mustPull(t, c, false)
	if !existsLive("mappings/prod.json") {
		t.Fatal("wrong precondition")
	}

	next := snapshotFiles()
	delete(next, "mappings/prod.json")
	h.setConfig(2, next)
	out := mustPull(t, c, false)
	if len(out.Config.Removed) != 1 || out.Config.Removed[0] != "mappings/prod.json" {
		t.Fatalf("the retired tier should be removed, got %v", out.Config.Removed)
	}
	if existsLive("mappings/prod.json") {
		t.Fatal("the retired tier is still there")
	}
}
