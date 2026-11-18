package proto

import (
	"crypto/rand"
	"encoding/hex"
	"os"
	"strings"

	"github.com/rzbdz/newgate/lib/i18n"
)

// ErrNoRootKey means this machine has no root key yet (never config trusted).
//
// i18n.E stores only the source text (message identity); rendering happens when Error()
// is called - so it is safe in a package-level variable (i18n.T is not; it would freeze).
var ErrNoRootKey = i18n.E("this machine has no root key yet", nil)

// GenerateRootKey creates a new root key.
//
// Only the host calls it (`newgate config secrets init`), once in a lifetime. Everything
// on this machine derives from it: encryption key, bearer token. **It never travels on
// any channel** - replicas get it by a person copying the blob (deliberate, see docs/13).
func GenerateRootKey() ([]byte, error) {
	root := make([]byte, RootKeyLen)
	if _, err := rand.Read(root); err != nil {
		return nil, i18n.Ef(err, "cannot read random bytes: {err}", nil)
	}
	return root, nil
}

// RootKeyBlob is the text a person copies around (hex).
//
// Why a separate "blob" concept instead of letting users cat the file: this manual path
// is the only "once per machine" step in the design, and it must not go wrong. A
// copyable string that **is exactly what lands on disk** means `cat root.key` prints
// something pasteable on another machine - no transcoding, no transcription errors.
func RootKeyBlob(root []byte) string { return hex.EncodeToString(root) }

// SaveRootKey writes the root key to disk (0600, hex text, see RootKeyBlob).
func SaveRootKey(root []byte) error {
	return writeFileAtomic(ConfigShareRootKeyFile(), []byte(RootKeyBlob(root)+"\n"), 0o600)
}

// LoadRootKey reads the root key back.
//
// Both inputs are accepted: **hex text** (the normal path; SaveRootKey writes it and
// users copy it), or **32 raw bytes** (someone inserted it another way). Why tolerate
// that: this path's errors are expensive and read badly - hex-only would leave someone
// who made a key with `head -c 32 /dev/urandom > root.key` with "root key too short",
// then suspecting the protocol, the network, tailscale - when the problem is format.
// Accepting both kills that class of failure at the source.
func LoadRootKey() ([]byte, error) {
	raw, err := readFileIfExists(ConfigShareRootKeyFile())
	if err != nil {
		return nil, err
	}
	if raw == nil {
		return nil, ErrNoRootKey
	}
	return ParseRootKey(string(raw))
}

// ParseRootKey parses the blob the user supplies (`config trust <blob>`).
func ParseRootKey(text string) ([]byte, error) {
	// Hex path: whitespace on both sides is tolerated - a trailing newline (SaveRootKey
	// writes one) and spaces picked up when pasting are both normal.
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return nil, ErrNoRootKey
	}
	if b, err := hex.DecodeString(trimmed); err == nil {
		if len(b) < RootKeyLen {
			return nil, i18n.E("this string is {n} bytes, at least {min} are needed — it looks truncated (it should be {chars} hex characters)",
				i18n.A{"n": len(b), "min": RootKeyLen, "chars": RootKeyLen * 2})
		}
		return b, nil
	}

	// Raw path: **not a single byte may be trimmed**.
	//
	// Hit for real on 2026-09-18, with a nasty symptom: this used to feed the TrimSpace
	// result into the "is it 32 raw bytes" test, but the key is **random bytes** - if
	// either end lands in the whitespace set, the length drops to 31/30 and the path
	// works only sometimes. It went red in CI and would not reproduce locally
	// (TestRootKeyParsing in `modules/configshare/proto`, reporting "30 characters") -
	// it rolls dice on every run.
	//
	// Worse, it does not just error: when the trimmed byte really is part of the key,
	// you get a **different key** - the two machines derive different keys, the symptom
	// is "decryption failed", and that sends people at the network and tailscale.
	//
	// Only one decoration is accepted: a trailing newline (echo / editors). No ambiguity
	// - 33 bytes can only be "32-byte key + newline"; a key ending in 0x0A is 32 bytes
	// and takes the correct branch below.
	raw := text
	if n := len(raw); n == RootKeyLen+1 && raw[n-1] == '\n' {
		raw = raw[:RootKeyLen]
	}
	if len(raw) == RootKeyLen {
		return []byte(raw), nil
	}
	return nil, i18n.E("this string is neither hex ({n} bytes) nor long enough as a raw key ({min} bytes) — was it copied in full?",
		i18n.A{"n": len(trimmed), "min": RootKeyLen})
}

// RootKeyFingerprint is a short fingerprint of the root key (**is it the same one**).
//
// Why: the two most common deploy accidents (wrong blob, half a blob) can only show as
// "decryption failed", which points users at the network. Print the fingerprint in
// `config status` and one glance on each machine settles it. First 8 chars of a derived
// value - leaking it reveals nothing about the root key.
func RootKeyFingerprint(root []byte) string {
	encKey, _, err := DeriveKeys(root)
	if err != nil {
		return "?"
	}
	return hex.EncodeToString(encKey)[:8]
}

// RootKeyFromEnv allows an env var to supply the root key (automation/testing only).
func RootKeyFromEnv() ([]byte, bool, error) {
	v := os.Getenv("NEWGATE_CONFIGSHARE_ROOT_KEY")
	if v == "" {
		return nil, false, nil
	}
	root, err := ParseRootKey(v)
	if err != nil {
		return nil, true, err
	}
	return root, true, nil
}
