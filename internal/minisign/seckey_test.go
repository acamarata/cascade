package minisign

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
	"golang.org/x/crypto/blake2b"
	"golang.org/x/crypto/scrypt"
)

// encryptedTestKey builds the minisign wire format with a cheap scrypt cost.
func encryptedTestKey(t *testing.T) ([]byte, []byte, ed25519.PrivateKey) {
	t.Helper()
	for _, name := range []string{"HOME", "USERPROFILE", "CASCADE_HOME"} {
		t.Setenv(name, t.TempDir())
	}
	pass := []byte("temporary test passphrase")
	priv := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{17}, 32))
	raw := make([]byte, 158)
	copy(raw, "EdScB2")
	binary.LittleEndian.PutUint64(raw[38:46], 512)
	binary.LittleEndian.PutUint64(raw[46:54], 16384)
	copy(raw[54:62], "test-id1")
	copy(raw[62:126], priv)
	sum := blake2b.Sum256(append([]byte("Ed"), raw[54:126]...))
	copy(raw[126:], sum[:])
	mask, err := scrypt.Key(pass, raw[6:38], 16, 8, 64, 104) // libsodium params for ops 512, mem 16384
	if err != nil {
		t.Fatal(err)
	}
	for i := range mask {
		raw[54+i] ^= mask[i]
	}
	return []byte("untrusted comment: test key\n" + base64.StdEncoding.EncodeToString(raw) + "\n"), pass, priv
}

func unencryptedTestKey(t *testing.T) ([]byte, ed25519.PrivateKey) {
	t.Helper()
	priv := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{23}, 32))
	raw := make([]byte, 158)
	copy(raw, []byte{'E', 'd', 0, 0, 'B', '2'})
	copy(raw[54:62], "plain-id")
	copy(raw[62:126], priv)
	sum := blake2b.Sum256(append([]byte("Ed"), raw[54:126]...))
	copy(raw[126:], sum[:])
	return secretKeyArmor(raw), priv
}

func secretKeyArmor(raw []byte) []byte {
	return []byte("untrusted comment: test key\n" + base64.StdEncoding.EncodeToString(raw) + "\n")
}

func editSecretKeyRaw(t *testing.T, file []byte, edit func([]byte)) []byte {
	t.Helper()
	lines := bytes.Split(bytes.TrimSpace(file), []byte("\n"))
	raw, err := base64.StdEncoding.DecodeString(string(lines[1]))
	if err != nil {
		t.Fatal(err)
	}
	edit(raw)
	return secretKeyArmor(raw)
}

func TestDecryptSecretKeyRoundTrip(t *testing.T) {
	file, pass, priv := encryptedTestKey(t)
	k, err := DecryptSecretKey(file, pass)
	if err != nil {
		t.Fatal(err)
	}
	seed := k.Seed()
	if !bytes.Equal(ed25519.NewKeyFromSeed(seed).Public().(ed25519.PublicKey), priv.Public().(ed25519.PublicKey)) {
		t.Fatal("paired public key differs")
	}
	if k.KeyID() != [8]byte{'t', 'e', 's', 't', '-', 'i', 'd', '1'} {
		t.Fatal("key id differs")
	}
	seed[0] ^= 1
	if bytes.Equal(seed, k.Seed()) {
		t.Fatal("Seed aliases secret")
	}
	k.Zero()
	if !bytes.Equal(k.Seed(), make([]byte, 32)) || k.KeyID() != [8]byte{} {
		t.Fatal("Zero retained secret")
	}
}

func assertSecretIntegrity(t *testing.T, file, pass []byte) {
	t.Helper()
	assertSecretIntegrityReason(t, file, pass, "checksum mismatch")
}

func assertSecretIntegrityReason(t *testing.T, file, pass []byte, reason string) {
	t.Helper()
	_, err := DecryptSecretKey(file, pass)
	want := cascade.New(cascade.KindIntegrity, "minisign: secret key "+reason)
	if !errors.Is(err, want) || err.Error() != want.Error() {
		t.Fatalf("error = %v, want %v", err, want)
	}
}

func TestDecryptSecretKeyWrongPassphraseIntegrity(t *testing.T) {
	file, _, _ := encryptedTestKey(t)
	assertSecretIntegrity(t, file, []byte("wrong"))
}

func TestDecryptSecretKeyTamperedChecksum(t *testing.T) {
	file, pass, _ := encryptedTestKey(t)
	lines := bytes.Split(file, []byte("\n"))
	raw, err := base64.StdEncoding.DecodeString(string(lines[1]))
	if err != nil {
		t.Fatal(err)
	}
	raw[len(raw)-1] ^= 1
	assertSecretIntegrity(t, []byte(string(lines[0])+"\n"+base64.StdEncoding.EncodeToString(raw)+"\n"), pass)
}

func TestDecryptSecretKeyRefusesMalformedEnvelope(t *testing.T) {
	plain, _ := unencryptedTestKey(t)
	unknownSignature := editSecretKeyRaw(t, plain, func(raw []byte) { copy(raw[:2], "XX") })
	unknownChecksum := editSecretKeyRaw(t, plain, func(raw []byte) { copy(raw[4:6], "XX") })
	unknownKDF := editSecretKeyRaw(t, plain, func(raw []byte) { copy(raw[2:4], "XX") })
	for _, tc := range []struct {
		name, reason string
		file         []byte
	}{
		{"wrong line count", "malformed file", []byte("one line")},
		{"wrong comment", "malformed file", []byte("not a comment\n" + string(plain[len("untrusted comment: test key\n"):]))},
		{"invalid base64", "invalid length or encoding", []byte("untrusted comment: test key\n!!!\n")},
		{"wrong decoded length", "invalid length or encoding", secretKeyArmor([]byte("short"))},
		{"unknown signature algorithm", "unknown algorithm", unknownSignature},
		{"unknown checksum algorithm", "unknown algorithm", unknownChecksum},
		{"unknown KDF", "unknown KDF", unknownKDF},
	} {
		t.Run(tc.name, func(t *testing.T) { assertSecretIntegrityReason(t, tc.file, nil, tc.reason) })
	}
}

func TestDecryptSecretKeyRejectsUnsafeScryptCosts(t *testing.T) {
	file, _, _ := encryptedTestKey(t)
	for _, tc := range []struct {
		name       string
		operations uint64
		memory     uint64
	}{
		{"low operation cost", 511, 16384},
		{"low memory cost", 512, 16383},
		{"excessive operation cost", 1<<30 + 1, 16384},
		{"excessive derived parallelism", 1 << 30, 16384},
		{"excessive memory cost", 1 << 25, 1<<30 + 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bad := editSecretKeyRaw(t, file, func(raw []byte) {
				binary.LittleEndian.PutUint64(raw[38:46], tc.operations)
				binary.LittleEndian.PutUint64(raw[46:54], tc.memory)
			})
			assertSecretIntegrityReason(t, bad, []byte("temporary test passphrase"), "invalid scrypt cost")
		})
	}
}

// TestDecryptSecretKeyLibsodiumGolden decrypts payloads produced outside this
// code: libsodium 1.0.22 crypto_pwhash_scryptsalsa208sha256 (what minisign
// calls) over a synthetic seed of 32 bytes 0x2a, key id "golden01", salt
// 0x00..0x1f and passphrase "golden test passphrase". One vector takes the
// clamped-ops branch (N=16, p=64), the other the ops-bound branch (N=1024, p=1).
func TestDecryptSecretKeyLibsodiumGolden(t *testing.T) {
	const wantPublic = "197f6b23e16c8532c6abc838facd5ea789be0c76b2920334039bfa8b3d368d61"
	for _, tc := range []struct {
		name      string
		ops, mem  uint64
		encrypted string
	}{
		{"ops clamped, cost from memory", 512, 16384, "ff045d0cdf8dd105fcfdcc45322ec627d0d5ecb39855a2d49826c26c7869ed9e" +
			"6e75414384404312f4db85c6aca5b9d94f45dba80ed8c04bf777b093a0c0676acd7bd898097a5391bcdedc1eaa44b6a00318be03" +
			"3961c1815896da80347d9711b7698f4d69a6fd9f"},
		{"cost from operations", 32768, 1 << 21, "39d9fb58a3a517a7465ae3075329a4911f853df9a5441ddc517af5d21a4486df" +
			"6d79934defcb301e02de913e516b701d25e3444a988be46d36f134f2d6e33dfb137413041b9fad740fe445cbcaa55364fce33d1c" +
			"b643c202d1c37535dcc78ac06ed968bd5f10b081"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			payload, err := hex.DecodeString(tc.encrypted)
			if err != nil || len(payload) != 104 {
				t.Fatalf("bad golden payload: %v", err)
			}
			raw := append([]byte("EdScB2"), make([]byte, 48)...)
			for i := range 32 {
				raw[6+i] = byte(i)
			}
			binary.LittleEndian.PutUint64(raw[38:46], tc.ops)
			binary.LittleEndian.PutUint64(raw[46:54], tc.mem)
			k, err := DecryptSecretKey(secretKeyArmor(append(raw, payload...)), []byte("golden test passphrase"))
			if err != nil {
				t.Fatal(err)
			}
			defer k.Zero()
			seed := k.Seed()
			defer clear(seed)
			got := hex.EncodeToString(ed25519.NewKeyFromSeed(seed).Public().(ed25519.PublicKey))
			if got != wantPublic || k.KeyID() != [8]byte{'g', 'o', 'l', 'd', 'e', 'n', '0', '1'} {
				t.Fatalf("public key %s id %q, want %s golden01", got, k.KeyID(), wantPublic)
			}
		})
	}
}

func TestDecryptSecretKeyUnencryptedAndInconsistentPublicKey(t *testing.T) {
	file, priv := unencryptedTestKey(t)
	key, err := DecryptSecretKey(file, nil)
	if err != nil {
		t.Fatal(err)
	}
	seed := key.Seed()
	got := ed25519.NewKeyFromSeed(seed).Public().(ed25519.PublicKey)
	if !bytes.Equal(got, priv.Public().(ed25519.PublicKey)) {
		t.Fatal("unencrypted secret does not match its public key")
	}

	inconsistent := editSecretKeyRaw(t, file, func(raw []byte) {
		raw[94] ^= 1
		sum := blake2b.Sum256(append([]byte("Ed"), raw[54:126]...))
		copy(raw[126:], sum[:])
	})
	assertSecretIntegrityReason(t, inconsistent, nil, "inconsistent public key")
}
