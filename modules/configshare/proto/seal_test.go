package proto

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/rzbdz/newgate/lib/i18n"
)

func TestDeriveKeysDomainSeparatedAndStable(t *testing.T) {
	root := bytes.Repeat([]byte{0x2b}, RootKeyLen)
	enc1, tok1, err := DeriveKeys(root)
	if err != nil {
		t.Fatalf("derivation failed: %v", err)
	}
	enc2, tok2, err := DeriveKeys(root)
	if err != nil {
		t.Fatalf("derivation failed: %v", err)
	}
	// One root key must derive the same pair every time - or two machines derive apart.
	if !bytes.Equal(enc1, enc2) || !bytes.Equal(tok1, tok2) {
		t.Fatal("the same root key derived different values (HKDF should not do that)")
	}
	// Domain separation: the key and the token must differ. **That is all this derivation
	// is for** - the token goes into headers, logs, middleware; if it were the key, log
	// readers would hold every subscription's cleartext.
	if bytes.Equal(enc1, tok1) {
		t.Fatal("the derived encryption key equals the auth token: purpose separation is not in effect, so a leaked token is a leaked key")
	}
	if len(enc1) != 32 || len(tok1) != 32 {
		t.Fatalf("wrong derivation lengths: enc=%d auth=%d, both should be 32", len(enc1), len(tok1))
	}
	// Changing the root key must produce completely different values.
	other := bytes.Repeat([]byte{0x2c}, RootKeyLen)
	enc3, tok3, _ := DeriveKeys(other)
	if bytes.Equal(enc1, enc3) || bytes.Equal(tok1, tok3) {
		t.Fatal("a different root key derived the same value")
	}
}

func TestDeriveKeysRejectsShortRoot(t *testing.T) {
	if _, _, err := DeriveKeys(bytes.Repeat([]byte{1}, RootKeyLen-1)); err == nil {
		t.Fatal("a root key shorter than 32 bytes should be rejected")
	} else if !strings.Contains(err.Error(), "bytes") {
		t.Fatalf("the error message should state the length, got: %v", err)
	}
}

func TestSealOpenRoundTrip(t *testing.T) {
	encKey, _, _ := DeriveKeys(bytes.Repeat([]byte{7}, RootKeyLen))
	plain := []byte(`{"hello":"world","n":9007199254740993}`)
	env, err := Seal(encKey, KindConfig, "host-a", 42, plain)
	if err != nil {
		t.Fatalf("sealing failed: %v", err)
	}
	if env.Gen != 42 || env.Kind != KindConfig || env.Host != "host-a" {
		t.Fatalf("wrong envelope header: %+v", env)
	}
	got, err := Open(encKey, KindConfig, env)
	if err != nil {
		t.Fatalf("decryption failed: %v", err)
	}
	// Byte-for-byte equality: big integers must not be rewritten by a JSON round trip (§4).
	if !bytes.Equal(got, plain) {
		t.Fatalf("content changed after the round trip:\n was %s\n got %s", plain, got)
	}
}

func TestSealTamperDetected(t *testing.T) {
	encKey, _, _ := DeriveKeys(bytes.Repeat([]byte{7}, RootKeyLen))
	env, _ := Seal(encKey, KindConfig, "h", 1, []byte("payload"))

	flip := func(e *Envelope) *Envelope {
		cp := *e
		cp.CT = append([]byte(nil), e.CT...)
		cp.CT[0] ^= 0x01
		return &cp
	}
	if _, err := Open(encKey, KindConfig, flip(env)); !errors.Is(err, ErrTampered) {
		t.Fatalf("flipping one byte of the ciphertext must fail decryption, got: %v", err)
	}
	// The nonce is authenticated too.
	cp := *env
	cp.Nonce = append([]byte(nil), env.Nonce...)
	cp.Nonce[0] ^= 0x01
	if _, err := Open(encKey, KindConfig, &cp); !errors.Is(err, ErrTampered) {
		t.Fatalf("flipping one byte of the nonce must fail decryption, got: %v", err)
	}
	// A different root key (= two machines trusted different blobs): the symptom is
	// ErrTampered, and the text must mention the root key, else users suspect the network.
	wrong, _, _ := DeriveKeys(bytes.Repeat([]byte{8}, RootKeyLen))
	if _, err := Open(wrong, KindConfig, env); !errors.Is(err, ErrTampered) {
		t.Fatalf("a different root key must fail decryption, got: %v", err)
	}
	if !strings.Contains(i18n.ID(ErrTampered), "root key") {
		t.Fatalf("the ErrTampered message should mention the root key: %v", ErrTampered)
	}
}

// TestSealBindsContext is what AAD is for: ciphertext can be moved, and binding
// (version, channel, authority, generation) makes moving it fail to open.
func TestSealBindsContext(t *testing.T) {
	encKey, _, _ := DeriveKeys(bytes.Repeat([]byte{7}, RootKeyLen))
	env, _ := Seal(encKey, KindConfig, "host-a", 42, []byte("payload"))

	t.Run("cross-channel", func(t *testing.T) {
		// Put the /config envelope at /secrets: both endpoints use the same ciphertext
		// format, so without channel binding each opens the other.
		if _, err := Open(encKey, KindSecrets, env); err == nil {
			t.Fatal("a config-channel envelope must not open on the secrets channel")
		}
	})
	t.Run("replay an old generation", func(t *testing.T) {
		cp := *env
		cp.Gen = 41 // 把第 42 代重放成第 41 代，让副本回滚
		if _, err := Open(encKey, KindConfig, &cp); !errors.Is(err, ErrTampered) {
			t.Fatalf("tampering with the generation must fail decryption, got: %v", err)
		}
	})
	t.Run("mix-up", func(t *testing.T) {
		cp := *env
		cp.Host = "host-b" // Host 是明文，不绑进 AAD 的话它就能被随便改
		if _, err := Open(encKey, KindConfig, &cp); !errors.Is(err, ErrTampered) {
			t.Fatalf("tampering with the authority identity must fail decryption, got: %v", err)
		}
	})
	t.Run("downgrade", func(t *testing.T) {
		cp := *env
		cp.V = ProtocolVersion + 1
		_, err := Open(encKey, KindConfig, &cp)
		if err == nil || errors.Is(err, ErrTampered) {
			t.Fatalf("a protocol version mismatch should give a readable message (not ErrTampered), got: %v", err)
		}
		if !strings.Contains(err.Error(), "protocol version") {
			t.Fatalf("the version-mismatch message should make clear it is a version problem: %v", err)
		}
	})
}

func TestRootKeyParsing(t *testing.T) {
	root, err := GenerateRootKey()
	if err != nil {
		t.Fatalf("generation failed: %v", err)
	}
	if len(root) != RootKeyLen {
		t.Fatalf("generated root key length %d, want %d", len(root), RootKeyLen)
	}
	blob := RootKeyBlob(root)
	// The blob is the **directly pasteable** text: 64 hex chars, no decoration. This
	// manual path is the only per-machine manual step in the design and must not go
	// wrong - so this pins "what is printed is what can be pasted".
	if len(blob) != RootKeyLen*2 {
		t.Fatalf("the blob should be %d characters, got %d: %q", RootKeyLen*2, len(blob), blob)
	}
	if strings.ContainsAny(blob, " \n\t") {
		t.Fatalf("the blob must contain no whitespace (it gets pasted in): %q", blob)
	}
	back, err := ParseRootKey(blob)
	if err != nil {
		t.Fatalf("parsing the hex blob failed: %v", err)
	}
	if !bytes.Equal(back, root) {
		t.Fatal("hex round trip mismatch")
	}
	// 32 raw bytes are accepted too (someone used head -c 32 /dev/urandom > root.key).
	if raw, err := ParseRootKey(string(root)); err != nil || !bytes.Equal(raw, root) {
		t.Fatalf("should accept a 32-byte raw key, got err=%v", err)
	}

	// The test above **rolls dice on random bytes**: root differs each run and usually
	// does not start or end with whitespace, so it long masked a real bug - ParseRootKey
	// used to test the TrimSpace result for "32 raw bytes", so with whitespace at either
	// end the length fell to 30/31 and the path worked only sometimes. It went red in CI.
	//
	// The tests below pin that face with **deterministic** inputs, no luck involved.
	// Filler bytes are 0x88 / 0x99 / 0xaa: not hex digits (or the whole string would
	// decode as hex; the first version used 0x33, which is '3', and the test tripped
	// over its own trap), not whitespace, and not valid UTF-8 lead bytes.
	for _, raw := range [][]byte{
		append(append([]byte{' '}, bytes.Repeat([]byte{0x88}, RootKeyLen-2)...), '\n'),
		append(append([]byte{'\t'}, bytes.Repeat([]byte{0x99}, RootKeyLen-2)...), ' '),
		append(append([]byte{'\r'}, bytes.Repeat([]byte{0xaa}, RootKeyLen-2)...), 0x0b),
	} {
		got, err := ParseRootKey(string(raw))
		if err != nil {
			t.Fatalf("a 32-byte raw key with leading or trailing whitespace must be accepted (the root cause of the red CI), input %q: %v", raw, err)
		}
		// **Byte-for-byte**, not "the length is right": trimming a real key byte yields
		// a different key, two machines derive apart, and the symptom is "decryption
		// failed" which points at the network. Silent key swaps are the feared failure.
		if !bytes.Equal(got, raw) {
			t.Fatalf("the raw key was altered:\n got %x\n want %x", got, raw)
		}
	}

	// A trailing newline must be accepted (echo / editors), and only that one.
	withNL := append(bytes.Repeat([]byte{0x88}, RootKeyLen), '\n')
	if got, err := ParseRootKey(string(withNL)); err != nil || !bytes.Equal(got, withNL[:RootKeyLen]) {
		t.Fatalf("a 32-byte raw key plus a trailing newline should be accepted, got err=%v", err)
	}

	// Truncated hex must say it is a length problem, not send the user to suspect the protocol.
	if _, err := ParseRootKey(blob[:40]); err == nil || !strings.Contains(err.Error(), "truncated") {
		t.Fatalf("a truncated blob should yield a length hint, got: %v", err)
	}
	if _, err := ParseRootKey("   "); !errors.Is(err, ErrNoRootKey) {
		t.Fatalf("an empty string should be ErrNoRootKey, got: %v", err)
	}
}

func TestRootKeyFingerprint(t *testing.T) {
	a := bytes.Repeat([]byte{1}, RootKeyLen)
	b := bytes.Repeat([]byte{2}, RootKeyLen)
	fp := RootKeyFingerprint(a)
	if len(fp) != 8 {
		t.Fatalf("the fingerprint should be 8 characters, got %q", fp)
	}
	if fp == RootKeyFingerprint(b) {
		t.Fatal("fingerprints of different root keys must not match - they exist to check whether two machines copied the same key")
	}
	if fp != RootKeyFingerprint(a) {
		t.Fatal("the fingerprint of the same root key must be stable")
	}
}

func TestSaveLoadRootKey(t *testing.T) {
	sandbox(t)
	root := bytes.Repeat([]byte{9}, RootKeyLen)
	if _, err := LoadRootKey(); !errors.Is(err, ErrNoRootKey) {
		t.Fatalf("nothing written yet should be ErrNoRootKey, got: %v", err)
	}
	if err := SaveRootKey(root); err != nil {
		t.Fatalf("writing to disk failed: %v", err)
	}
	back, err := LoadRootKey()
	if err != nil {
		t.Fatalf("reading back failed: %v", err)
	}
	if !bytes.Equal(back, root) {
		t.Fatal("root key disk round trip mismatch")
	}
}
