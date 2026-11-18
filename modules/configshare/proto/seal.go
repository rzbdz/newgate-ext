package proto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"fmt"

	"github.com/rzbdz/newgate/lib/i18n"
)

// The info strings for domain separation. They are **part of the protocol**: change one
// and old and new ends cannot open each other - which is wanted (no silent downgrade).
// The trailing v1 leaves room for a second rotation path later (root key rotation is the first).
const (
	infoEncKey    = "newgate-configshare-enc-v1"
	infoAuthToken = "newgate-configshare-auth-v1"

	// RootKeyLen is the root key length. 32 bytes = one HKDF-SHA256 output block, and
	// the AES-256 key size. `newgate config secrets init` generates exactly this.
	RootKeyLen = 32
)

// ErrTampered means the envelope failed AEAD authentication.
//
// Three causes are **indistinguishable** at this layer (AEAD's nature and its value):
// content edited, different root keys on the two ends, protocol version mismatch. The
// user-facing text must name all three, or the hardest first-deploy error (two machines
// trusting different blobs) reads as "decryption failed" and users suspect the network.
// i18n.E stores the source text; rendering happens at Error() - so it is safe in a
// package-level variable (i18n.T is not, it would freeze on the source language).
var ErrTampered = i18n.E("the envelope failed authentication (its content was changed, the two root keys differ, or the protocol versions do not match)", nil)

// DeriveKeys derives (AES-GCM key, bearer token) from the pre-shared root key.
//
// Why two derived values rather than the root key itself (HKDF's whole point here):
// the token rides the Authorization header on every request, so it lands in the host
// daemon's logs, any reverse proxy's logs, and packet captures. If the token were the
// encryption key, **anyone reading logs gets all subscriptions in cleartext**. After
// derivation, a leaked token does not reveal the key - that is domain separation.
//
// salt=nil is deliberate: the root key is pre-shared high-entropy random, not a
// password, so both ends must be able to derive the same value with no per-call salt.
func DeriveKeys(root []byte) (encKey, authToken []byte, err error) {
	if len(root) < RootKeyLen {
		// Too short is almost always "a chunk was missed in config trust" or "it was pasted
		// as base64 while this takes raw bytes". Stating the length beats "invalid key".
		return nil, nil, i18n.E("the root key is only {n} bytes, at least {min} are needed (the string config trust gives you)",
			i18n.A{"n": len(root), "min": RootKeyLen})
	}
	encKey, err = hkdf.Key(sha256.New, root, nil, infoEncKey, 32)
	if err != nil {
		return nil, nil, i18n.Ef(err, "cannot derive the encryption key: {err}", nil)
	}
	authToken, err = hkdf.Key(sha256.New, root, nil, infoAuthToken, 32)
	if err != nil {
		return nil, nil, i18n.Ef(err, "cannot derive the auth token: {err}", nil)
	}
	return encKey, authToken, nil
}

// aad is this envelope's associated data: not encrypted, but authenticated.
//
// Binding (protocol version, channel, authority, generation) blocks four attacks where
// the envelope is legitimate and merely moved; none of them needs the key:
//   - **downgrade**: feeding a v1 envelope to a future v2-only parser (or the reverse);
//   - **cross-channel**: putting a /config envelope at /secrets (both endpoints use
//     the same ciphertext format, so without channel binding each can open the other);
//   - **generation replay**: replaying generation 41 as 42 to roll a replica back;
//   - **misattribution**: labelling host A's envelope as host B (Host is cleartext, so
//     without AAD binding it is a freely editable field - and replicas judge "did the
//     authority change" by exactly that field).
//
// This is what "AEAD protects integrity" means concretely - encrypt without binding the
// context and the ciphertext can be moved around, and a moved ciphertext still opens.
func aad(kind, host string, gen int) []byte {
	return []byte(fmt.Sprintf("newgate/%s/v%d/host%s/gen%d", kind, ProtocolVersion, host, gen))
}

// Seal seals plaintext into an envelope. kind is KindConfig / KindSecrets, host is the
// publisher's authority identity (its own host_id, the same one in the server ledger).
func Seal(encKey []byte, kind, host string, gen int, plaintext []byte) (*Envelope, error) {
	gcm, err := newGCM(encKey)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	// A random nonce rather than a counter: GCM nonce reuse **catastrophically** leaks
	// plaintext XORs, and a counter needs persisted state on both ends, which a reinstall
	// replays. At this volume (tens a day) random nonce collisions are negligible.
	if _, err := rand.Read(nonce); err != nil {
		return nil, i18n.Ef(err, "cannot read random bytes: {err}", nil)
	}
	return &Envelope{
		V:     ProtocolVersion,
		Kind:  kind,
		Host:  host,
		Gen:   gen,
		Nonce: nonce,
		CT:    gcm.Seal(nil, nonce, plaintext, aad(kind, host, gen)),
	}, nil
}

// Open opens the envelope. kind is the channel the caller **expects** - not read from
// the envelope, because an envelope's claim about itself does not count; AAD decides.
//
// host is not validated here: the caller compares env.Host with its ledger (that is the
// "authority changed" business judgment, not a cryptographic one). AAD only guarantees
// env.Host was sealed with the ciphertext; a byte changed and it will not open.
func Open(encKey []byte, kind string, env *Envelope) ([]byte, error) {
	if env == nil {
		return nil, i18n.E("the envelope is empty", nil)
	}
	// Version/channel are checked first: those are "misconfigured", and the text should say
	// so; inside ErrTampered they would turn into a scary "content was modified".
	if env.V != ProtocolVersion {
		return nil, i18n.E("the peer speaks protocol version {peer}, this binary only understands {mine} (both ends have to be upgraded together)",
			i18n.A{"peer": env.V, "mine": ProtocolVersion})
	}
	if env.Kind != kind {
		return nil, i18n.E("the envelope claims to be the {got} channel, but {want} was expected",
			i18n.A{"got": env.Kind, "want": kind})
	}
	gcm, err := newGCM(encKey)
	if err != nil {
		return nil, err
	}
	if len(env.Nonce) != gcm.NonceSize() {
		return nil, i18n.Ef(ErrTampered, "{err} (nonce length {got}, expected {want})",
			i18n.A{"got": len(env.Nonce), "want": gcm.NonceSize()})
	}
	plain, err := gcm.Open(nil, env.Nonce, env.CT, aad(kind, env.Host, env.Gen))
	if err != nil {
		return nil, ErrTampered
	}
	return plain, nil
}

func newGCM(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, i18n.Ef(err, "the AES key is unusable: {err}", nil)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, i18n.Ef(err, "GCM is unusable: {err}", nil)
	}
	return gcm, nil
}
