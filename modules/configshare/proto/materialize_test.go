package proto

import (
	paths "github.com/rzbdz/newgate-ext/modules/config/paths"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateSnapshotRejectsUnmanagedPath(t *testing.T) {
	sandbox(t)
	files := snapshotFiles()
	files["state.json"] = []byte("{}") // machine-local state, never written from the network
	_, err := ValidateSnapshot(paths.Config(), files)
	if err == nil || !strings.Contains(err.Error(), "managed set") {
		t.Fatalf("files outside the managed set must be refused, got: %v", err)
	}
}

// TestValidateSnapshotRejectsInlineKey is the basis of key discipline: no secrets on the wire.
//
// providers.json is git-managed on the host, so inline once = in history forever. A
// host in that state is either too old or hand-edited; both must be stopped, not
// written in cleartext to every replica's providers.json.
func TestValidateSnapshotRejectsInlineKey(t *testing.T) {
	sandbox(t)
	files := snapshotFiles()
	files[ProvidersName] = []byte(`{"providers":{
	  "leaky":{"base_url":"https://x","api_key":"sk-cleartext-key"},
	  "ok":{"base_url":"https://y","api_key_env":"OK_KEY"}
	}}`)
	_, err := ValidateSnapshot(paths.Config(), files)
	if err == nil {
		t.Fatal("an inline api_key must be rejected")
	}
	if !strings.Contains(err.Error(), "leaky") {
		t.Fatalf("the error message should name the provider: %v", err)
	}
	if !strings.Contains(err.Error(), "secrets channel") {
		t.Fatalf("the error message should say which path the key must take: %v", err)
	}
	// api_key_env is legitimate; must not be caught by mistake.
	files[ProvidersName] = []byte(`{"providers":{
	  "ok":{"base_url":"https://y","api_key_env":"OK_KEY"}}}`)
	if _, err := ValidateSnapshot(paths.Config(), files); err != nil {
		t.Fatalf("api_key_env must not be mistaken for an inline key: %v", err)
	}
	// No key source at all (waiting for the secrets channel) is legitimate too.
	files[ProvidersName] = validProviders()
	if _, err := ValidateSnapshot(paths.Config(), files); err != nil {
		t.Fatalf("having no key source must not be rejected: %v", err)
	}
}

func TestValidateSnapshotRejectsBrokenFiles(t *testing.T) {
	tests := []struct {
		name string
		rel  string
		data string
	}{
		{"providers is not JSON", ProvidersName, `{`},
		{"broken tier json", "mappings/prod.json", `{"roles":`},
		// This line is deliberately not key=value (and deliberately Chinese: the point is
		// text the kv parser cannot read at all).
		{"tier kv is not key=value", "mappings/prod.kv", `这就是一行中文`},
		{"tier kv with an unknown key", "mappings/prod.kv", "normal=smt-deepseek/x\nunknown-key=y\n"},
		{"broken meta", MetaFileName, `{`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			sandbox(t)
			files := map[string][]byte{tc.rel: []byte(tc.data)}
			if _, err := ValidateSnapshot(paths.Config(), files); err == nil {
				t.Fatalf("%s should be rejected", tc.name)
			}
		})
	}
}

func TestValidateSnapshotAcceptsGoodKV(t *testing.T) {
	sandbox(t)
	files := map[string][]byte{
		ProvidersName: validProviders(),
		"mappings/prod.kv": []byte(
			"# easy to hand-edit\nname=prod\nnormal=smt-deepseek/deepseek-chat\n"),
	}
	if _, err := ValidateSnapshot(paths.Config(), files); err != nil {
		t.Fatalf("a legitimate kv should pass: %v", err)
	}
}

// TestValidateSnapshotWarnsButAccepts pins the line: semantic doubts warn, never reject.
//
// A mistyped provider name must not starve the fleet of config updates (fail-open).
// Host publish **rejects** these (fixed on the spot, free); replicas only report.
func TestValidateSnapshotWarnsButAccepts(t *testing.T) {
	sandbox(t)
	files := map[string][]byte{
		// glm has api_key_env (legitimate); bare has neither (key should come via the channel).
		// Only the latter should be reported - flagging api_key_env too is a false warning,
		// and enough false warnings means nobody reads the real ones.
		ProvidersName: []byte(`{"providers":{
		  "glm":{"base_url":"https://g","api_key_env":"G"},
		  "bare":{"base_url":"https://b"}
		}}`),
		"mappings/prod.json": []byte(`{"name":"prod","roles":{
		  "normal":[{"provider":"typo-provider","model":"x"}]}}`),
	}
	warns, err := ValidateSnapshot(paths.Config(), files)
	if err != nil {
		t.Fatalf("a dangling reference must not reject the whole snapshot: %v", err)
	}
	joined := strings.Join(warns, "\n")
	if !strings.Contains(joined, "typo-provider") {
		t.Fatalf("the dangling provider reference should be reported, got: %v", warns)
	}
	if !strings.Contains(joined, "provider bare has no api_key_env") {
		t.Fatalf("it should warn that bare has no key source, got: %v", warns)
	}
	if strings.Contains(joined, "provider glm") {
		t.Fatalf("glm has api_key_env and must not be warned about (that is a false warning): %v", warns)
	}
}

// TestValidateSnapshotNoFalseWarningForLocalExtends: a tier extending a **machine-private**
// tier is legitimate. Without local tiers in the known set, every round emits a false
// warning - and enough false warnings means nobody reads the real ones.
func TestValidateSnapshotNoFalseWarningForLocalExtends(t *testing.T) {
	sandbox(t)
	writeLive(t, "mappings/local-base.json", []byte(`{"name":"local-base","roles":{}}`))
	files := map[string][]byte{
		ProvidersName:        validProviders(),
		"mappings/prod.json": []byte(`{"name":"prod","extends":"local-base","roles":{}}`),
	}
	warns, err := ValidateSnapshot(paths.Config(), files)
	if err != nil {
		t.Fatalf("it must not be rejected: %v", err)
	}
	for _, w := range warns {
		if strings.Contains(w, "local-base") {
			t.Fatalf("extends a machine-private tier is legitimate and must not warn: %v", warns)
		}
	}
}

func TestMaterializeWritesSkipsAndRemoves(t *testing.T) {
	sandbox(t)
	first := snapshotFiles()
	res, err := Materialize(paths.Config(), first, nil)
	if err != nil {
		t.Fatalf("writing to disk failed: %v", err)
	}
	if len(res.Written) != 3 || len(res.Removed) != 0 {
		t.Fatalf("the first round should write 3 files, got written=%v removed=%v",
			res.Written, res.Removed)
	}
	for rel := range first {
		if !existsLive(rel) {
			t.Fatalf("%s was not written", rel)
		}
	}

	// Round 2: identical content -> nothing written (no mtime touch, no fake watcher change).
	res2, err := Materialize(paths.Config(), first, res.Manifest)
	if err != nil {
		t.Fatalf("the second write failed: %v", err)
	}
	if len(res2.Written) != 0 || len(res2.Unchanged) != 3 {
		t.Fatalf("identical content should be skipped entirely, got written=%v unchanged=%v",
			res2.Written, res2.Unchanged)
	}

	// Round 3: the host retires one tier and changes one provider table.
	third := snapshotFiles()
	delete(third, "mappings/prod.json")
	third[ProvidersName] = []byte(`{"providers":{"only this":{"base_url":"https://only"}}}`)
	res3, err := Materialize(paths.Config(), third, res2.Manifest)
	if err != nil {
		t.Fatalf("the third write failed: %v", err)
	}
	if len(res3.Removed) != 1 || res3.Removed[0] != "mappings/prod.json" {
		t.Fatalf("the retired tier should be removed, got %v", res3.Removed)
	}
	if existsLive("mappings/prod.json") {
		t.Fatal("the retired tier is still there - if we only write and never delete, " +
			"references like @prod would always resolve to something")
	}
	if !strings.Contains(string(readLive(t, ProvidersName)), "only this") {
		t.Fatal("the modified provider table was not written")
	}
}

// TestMaterializeKeepsLocalFiles: **delete only what we wrote**. Local-layer files must
// never be touched by this function - that is the user's layer of the "two-layer config".
func TestMaterializeKeepsLocalFiles(t *testing.T) {
	sandbox(t)
	writeLive(t, "mappings/local-only.json", []byte(`{"name":"local-only"}`))
	applied := applyFiles(t, snapshotFiles())

	// Retire the managed tier in the same round, to confirm only our own files go.
	next := snapshotFiles()
	delete(next, "mappings/prod.json")
	if _, err := Materialize(paths.Config(), next, applied); err != nil {
		t.Fatalf("writing to disk failed: %v", err)
	}
	if !existsLive("mappings/local-only.json") {
		t.Fatal("a local-layer file was deleted - materialize must only delete " +
			"what is in its own manifest")
	}
}

func TestMaterializeFileMode(t *testing.T) {
	sandbox(t)
	if _, err := Materialize(paths.Config(), snapshotFiles(), nil); err != nil {
		t.Fatalf("writing to disk failed: %v", err)
	}
	// 0660 rather than relying on umask: CLAUDE.md §3.1 records this trap (root-written
	// files end up 0640, and a daemon running as claude then cannot write them).
	fi, err := os.Stat(filepath.Join(paths.Config(), ProvidersName))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o660 {
		t.Fatalf("managed files should be mode 0660, got %v", fi.Mode().Perm())
	}
}

func TestSecretsMaterialize(t *testing.T) {
	sandbox(t)
	path := secretsPath()

	// With no file yet it reads as an empty table, not an error.
	sf, err := LoadSecretsFile(path)
	if err != nil || len(sf.Keys) != 0 {
		t.Fatalf("absence should give an empty table, got %+v / %v", sf, err)
	}
	if _, ok := LookupSecret(path, "smt-deepseek"); ok {
		t.Fatal("no secrets should be found when there are no files")
	}

	sum, err := MaterializeSecrets(path, map[string]string{"smt-deepseek": "sk-abc", "glm": "sk-def"})
	if err != nil {
		t.Fatalf("writing to disk failed: %v", err)
	}
	if v, ok := LookupSecret(path, "smt-deepseek"); !ok || v != "sk-abc" {
		t.Fatalf("cannot find the secret just written: %q %v", v, ok)
	}
	// 0600: whoever can read it can spend that subscription (providers.json's 0660 is
	// deliberate group visibility; a cleartext key is not).
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("secrets.json should be mode 0600, got %v", fi.Mode().Perm())
	}

	// Write the same content again: the hash is unchanged (idempotent), nothing is rewritten.
	again, err := MaterializeSecrets(path,
		map[string]string{"glm": "sk-def", "smt-deepseek": "sk-abc"})
	if err != nil {
		t.Fatalf("rewrite failed: %v", err)
	}
	if again != sum {
		t.Fatalf("the same content should give the same hash: %s vs %s (a different order must "+
			"not matter, or every round would look changed)", again, sum)
	}
}
