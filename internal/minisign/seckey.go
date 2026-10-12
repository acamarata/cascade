package minisign

// Purpose: decode minisign secret files without exposing signing material.
// Inputs: armored secret file and passphrase. Outputs: seed and key identifier.
// Constraints: reject malformed algorithms, costs and checksums; erase scratch.
// SPORT: release-signing-custody.
import (
	"bytes"
	"crypto/ed25519"
	"crypto/subtle"
	"encoding/base64"
	"encoding/binary"

	"github.com/acamarata/cascade/pkg/cascade"
	"golang.org/x/crypto/blake2b"
	"golang.org/x/crypto/scrypt"
)

// SecretKey owns a release seed. Seed returns a copy; Zero erases this value.
type SecretKey struct {
	keyID [8]byte
	seed  [32]byte
}

// KeyID returns the minisign identifier.
func (k SecretKey) KeyID() [8]byte { return k.keyID }

// Seed returns a caller-owned copy, which the caller must erase after use.
func (k SecretKey) Seed() []byte { return append([]byte(nil), k.seed[:]...) }

// Zero erases the seed and identifier.
func (k *SecretKey) Zero() { clear(k.seed[:]); clear(k.keyID[:]) }

func secretIntegrity(reason string) error {
	return cascade.New(cascade.KindIntegrity, "minisign: secret key "+reason)
}

// DecryptSecretKey validates the minisign envelope before returning any seed.
func DecryptSecretKey(file, passphrase []byte) (SecretKey, error) {
	lines := bytes.Split(bytes.TrimRight(file, "\n"), []byte("\n"))
	if len(lines) != 2 || !bytes.HasPrefix(lines[0], []byte(untrustedCommentPrefix)) {
		return SecretKey{}, secretIntegrity("malformed file")
	}
	raw := make([]byte, base64.StdEncoding.DecodedLen(len(lines[1])))
	defer clear(raw)
	n, err := base64.StdEncoding.Decode(raw, lines[1])
	if err != nil || n != 158 {
		return SecretKey{}, secretIntegrity("invalid length or encoding")
	}
	raw = raw[:n]
	if string(raw[:2]) != "Ed" || string(raw[4:6]) != "B2" {
		return SecretKey{}, secretIntegrity("unknown algorithm")
	}
	switch string(raw[2:4]) {
	case "Sc":
		if err := decryptSecretPayload(raw, passphrase); err != nil {
			return SecretKey{}, err
		}
	case "\x00\x00":
	default:
		return SecretKey{}, secretIntegrity("unknown KDF")
	}
	return checkedSecret(raw)
}

func checkedSecret(raw []byte) (SecretKey, error) {
	input := append([]byte("Ed"), raw[54:126]...)
	defer clear(input)
	sum := blake2b.Sum256(input)
	defer clear(sum[:])
	if subtle.ConstantTimeCompare(sum[:], raw[126:158]) != 1 {
		return SecretKey{}, secretIntegrity("checksum mismatch")
	}
	priv := ed25519.NewKeyFromSeed(raw[62:94])
	defer clear(priv)
	if subtle.ConstantTimeCompare(priv, raw[62:126]) != 1 {
		return SecretKey{}, secretIntegrity("inconsistent public key")
	}
	var k SecretKey
	copy(k.keyID[:], raw[54:62])
	copy(k.seed[:], raw[62:94])
	return k, nil
}

// decryptSecretPayload follows libsodium's scrypt parameter selection (r=8).
// Caps bound untrusted-file allocation to the minisign interactive limit, 1 GiB.
func decryptSecretPayload(raw, passphrase []byte) error {
	ops := binary.LittleEndian.Uint64(raw[38:46])
	mem := binary.LittleEndian.Uint64(raw[46:54])
	if ops < 512 || ops > 1<<30 || mem < 16384 || mem > 1<<30 {
		return secretIntegrity("invalid scrypt cost")
	}
	n, p := scryptParams(ops, mem)
	if p == 0 || p > 1<<20 {
		return secretIntegrity("invalid scrypt cost")
	}
	mask, err := scrypt.Key(passphrase, raw[6:38], int(n), 8, int(p), 104)
	if err != nil {
		return secretIntegrity("invalid scrypt cost")
	}
	defer clear(mask)
	for i := range mask {
		raw[54+i] ^= mask[i]
	}
	return nil
}

// scryptParams is libsodium's pickparams for crypto_pwhash_scryptsalsa208sha256
// (r=8), which minisign uses: ops is clamped to 32768; below mem/32 the cost
// comes from ops with p=1, else N comes from mem and p from the remaining ops.
func scryptParams(ops, mem uint64) (n, p uint64) {
	ops = max(ops, 32768)
	if ops < mem/32 {
		return smallestPow2Above(ops / 32 / 2), 1
	}
	n = smallestPow2Above(mem / 1024 / 2)
	return n, min(ops/4/n, 0x3fffffff) / 8
}

// smallestPow2Above returns the smallest power of two, at least 2, above half.
func smallestPow2Above(half uint64) uint64 {
	n := uint64(2)
	for n <= half {
		n *= 2
	}
	return n
}
