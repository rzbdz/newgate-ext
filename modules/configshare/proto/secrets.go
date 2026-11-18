package proto

import (
	"encoding/json"
	"io/ioutil"
	"os"
	"strings"

	"github.com/rzbdz/newgate/lib/i18n"
)

// SecretsFileName is where the secrets channel lands.
//
// It sits in the config dir but is **not** part of the managed config set (not in
// managed.go's allowlist): it has its own ledger, generation and permissions (0600),
// because it is the only cleartext key copy. Mixing it in would entangle "config drift"
// with "secrets drift", and the two change an order of magnitude apart.
const SecretsFileName = "secrets.json"

// SecretsFormat is secrets.json's format version (unrelated to the wire protocol; it is
// this file's structure version; bump on semantic changes, refuse anything higher).
const SecretsFormat = 1

// SecretsFile is secrets.json's on-disk shape.
type SecretsFile struct {
	Version int               `json:"version"`
	Keys    map[string]string `json:"keys"` // provider 名 → 明文 key
}

// LoadSecretsFile reads the secrets file. Missing returns an empty table (**not an
// error**): most machines lack it before `config join`, and a host may use no keys.
func LoadSecretsFile(path string) (SecretsFile, error) {
	empty := SecretsFile{Version: SecretsFormat, Keys: map[string]string{}}
	raw, err := ioutil.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return empty, nil
		}
		return empty, err
	}
	var f SecretsFile
	if err := json.Unmarshal(raw, &f); err != nil {
		return empty, i18n.Ef(err, "cannot parse {path}: {err}", i18n.A{"path": path})
	}
	if f.Keys == nil {
		f.Keys = map[string]string{}
	}
	return f, nil
}

// LookupSecret looks up one provider's cleartext key.
//
// This is store.LoadProviders' key source (precedence in docs/13-config-share.md): on
// a hit use it, else fall back to api_key_env -> os.Getenv, else the provider's inline
// api_key (deprecated). It runs on **every** Load(), and the watcher Loads on every
// change - so **rotating keys needs no daemon restart**.
//
// It **deliberately** returns no error (the caller is a hot path, and this is fail-open
// territory): on a read or parse failure return false and let the caller fall back to
// env - better that one tier reports "no api_key" (a loud, checkable error) than that
// one bad file fails the whole config load and takes **every** tier down. To diagnose
// the file itself, use LoadSecretsFile (it returns an error); status/doctor use that.
func LookupSecret(path, provider string) (string, bool) {
	f, err := LoadSecretsFile(path)
	if err != nil {
		return "", false
	}
	v, ok := f.Keys[provider]
	if !ok || strings.TrimSpace(v) == "" {
		return "", false
	}
	return v, true
}

// secretsBytes is the secrets file's **canonical bytes**.
//
// Drift comparison (client.go) and the write (MaterializeSecrets) must hash the same
// bytes, or "should we write" and "did it change" would use two criteria, showing up as
// "changed every round" (same content, touched mtime, a fake hot reload). One impl.
func secretsBytes(keys map[string]string) ([]byte, error) {
	f := SecretsFile{Version: SecretsFormat, Keys: keys}
	if f.Keys == nil {
		f.Keys = map[string]string{}
	}
	raw, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(raw, '\n'), nil
}

// MaterializeSecrets writes keys to disk and returns the content's sha256 (for the
// ledger). Permission 0600: whoever reads it can spend that subscription - unlike
// providers.json (0660, visible to the group).
func MaterializeSecrets(path string, keys map[string]string) (string, error) {
	raw, err := secretsBytes(keys)
	if err != nil {
		return "", err
	}
	sum := hashBytes(raw)
	// Identical content is not written: no mtime touch, no fake change for the watcher
	// ("idle is fully silent" is a hard requirement).
	if cur, err := readFileIfExists(path); err == nil && cur != nil && hashBytes(cur) == sum {
		return sum, nil
	}
	if err := writeFileAtomic(path, raw, 0o600); err != nil {
		return "", err
	}
	return sum, nil
}
