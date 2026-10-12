package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/minisign"
	"github.com/acamarata/cascade/pkg/plugin"
	"github.com/acamarata/cascade/pkg/plugin/registry/generator"
	"golang.org/x/crypto/blake2b"
	"golang.org/x/crypto/scrypt"
)

func releaseTestEnv(t *testing.T) (minisign.SecretKey, []byte) {
	t.Helper()
	for _, name := range []string{"HOME", "USERPROFILE", "CASCADE_HOME"} {
		t.Setenv(name, t.TempDir())
	}
	priv := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{19}, 32))
	raw := make([]byte, 158)
	copy(raw, "EdScB2")
	binary.LittleEndian.PutUint64(raw[38:46], 512)
	binary.LittleEndian.PutUint64(raw[46:54], 16384)
	copy(raw[54:62], "test-id\x00")
	copy(raw[62:126], priv)
	sum := blake2b.Sum256(append([]byte("Ed"), raw[54:126]...))
	copy(raw[126:], sum[:])
	pubData := append([]byte("Edtest-id\x00"), priv.Public().(ed25519.PublicKey)...)
	pub := []byte(fmt.Sprintf("untrusted comment: minisign public key %016X\n%s\n", binary.LittleEndian.Uint64(raw[54:62]), base64.StdEncoding.EncodeToString(pubData)))
	pass := []byte("temporary registry test passphrase")
	mask, err := scrypt.Key(pass, raw[6:38], 16, 8, 64, 104) // libsodium params for ops 512, mem 16384
	if err != nil {
		t.Fatal(err)
	}
	for i := range mask {
		raw[54+i] ^= mask[i]
	}
	file := []byte("untrusted comment: test key\n" + base64.StdEncoding.EncodeToString(raw) + "\n")
	t.Setenv("MINISIGN_RELEASE_SECKEY_B64", base64.StdEncoding.EncodeToString(file))
	t.Setenv("MINISIGN_RELEASE_PASSPHRASE", string(pass))
	sk, err := minisign.DecryptSecretKey(file, pass)
	if err != nil {
		t.Fatal(err)
	}
	return sk, pub
}

func TestRegistryGenDeriveFromRelease(t *testing.T) {
	sk, _ := releaseTestEnv(t)
	defer sk.Zero()
	seed := sk.Seed()
	defer clear(seed)
	key, err := generator.DeriveRegistryKey(seed)
	if err != nil {
		t.Fatal(err)
	}
	defer clear(key)
	outDir := t.TempDir()
	w, stdout, stderr := testWriter()
	args := []string{"--derive-from-release", "--sequence", "1", "--artifacts", fixturesDir, "--out", outDir}
	if rc := run(w, args); rc != 0 {
		t.Fatalf("run = %d: %s", rc, stderr)
	}
	data, err := os.ReadFile(filepath.Join(outDir, "index.json"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := (plugin.Ed25519Verifier{PublicKey: key.Public().(ed25519.PublicKey)}).VerifyIndex(context.Background(), data); err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{os.Getenv("MINISIGN_RELEASE_SECKEY_B64"), os.Getenv("MINISIGN_RELEASE_PASSPHRASE")} {
		if strings.Contains(stdout.String()+stderr.String()+string(data), value) {
			t.Fatal("secret leaked")
		}
	}
	if run(w, append(args, "--key-ref", "other")) == 0 {
		t.Fatal("conflicting keys accepted")
	}
	for _, name := range []string{"MINISIGN_RELEASE_SECKEY_B64", "MINISIGN_RELEASE_PASSPHRASE"} {
		t.Run(name, func(t *testing.T) {
			t.Setenv(name, "")
			w, _, stderr := testWriter()
			if run(w, args) == 0 || !strings.Contains(stderr.String(), name) {
				t.Fatalf("missing entry not named: %s", stderr)
			}
		})
	}
}

func TestRegistryGenPrintReleasePublic(t *testing.T) {
	sk, pub := releaseTestEnv(t)
	defer sk.Zero()
	w, stdout, stderr := testWriter()
	if rc := run(w, []string{"--derive-from-release", "--print-release-public"}); rc != 0 || !bytes.Equal(stdout.Bytes(), pub) {
		t.Fatalf("public output mismatch rc=%d: %s", rc, stderr)
	}
}

// TestRegistryGenReleaseFlagMatrix pins every release-mode flag combination:
// the public outputs, the refusals and the error codes.
func TestRegistryGenReleaseFlagMatrix(t *testing.T) {
	sk, _ := releaseTestEnv(t)
	defer sk.Zero()
	seed := sk.Seed()
	derived, err := generator.DeriveRegistryKey(seed)
	clear(seed)
	if err != nil {
		t.Fatal(err)
	}
	derivedHex := fmt.Sprintf("%x\n", derived.Public().(ed25519.PublicKey))
	clear(derived)
	defaultHex := fmt.Sprintf("%x\n", plugin.DefaultRegistryPublicKey())
	cases := []struct {
		name string
		args []string
		rc   int
		out  string
	}{
		{"default public", []string{"--print-default-public"}, 0, defaultHex},
		{"derived public", []string{"--derive-from-release", "--print-public"}, 0, derivedHex},
		{"default with derive", []string{"--print-default-public", "--derive-from-release"}, 2, ""},
		{"public without derive", []string{"--print-public"}, 2, ""},
		{"release public without derive", []string{"--print-release-public"}, 2, ""},
		{"both public outputs", []string{"--derive-from-release", "--print-public", "--print-release-public"}, 2, ""},
		{"derive without sequence", []string{"--derive-from-release"}, 2, ""},
		{"derive without artifacts", []string{"--derive-from-release", "--sequence", "1"}, 2, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w, stdout, stderr := testWriter()
			if rc := run(w, tc.args); rc != tc.rc || stdout.String() != tc.out {
				t.Fatalf("rc=%d stdout=%q stderr=%q, want rc=%d stdout=%q", rc, stdout, stderr, tc.rc, tc.out)
			}
			if tc.rc != 0 && stderr.Len() == 0 {
				t.Fatal("refusal without a message")
			}
		})
	}
}

// TestRegistryGenReleaseSecretRefusals covers bad custody values: each
// refuses with rc 1 and never echoes the value it was given.
func TestRegistryGenReleaseSecretRefusals(t *testing.T) {
	sk, _ := releaseTestEnv(t)
	sk.Zero()
	cases := []struct{ name, env, value, want string }{
		{"secret not base64 or hex", "MINISIGN_RELEASE_SECKEY_B64", "zz-not-a-key-zz", "MINISIGN_RELEASE_SECKEY_B64"},
		{"wrong passphrase", "MINISIGN_RELEASE_PASSPHRASE", "wrong temporary passphrase", "checksum"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(tc.env, tc.value)
			w, stdout, stderr := testWriter()
			if rc := run(w, []string{"--derive-from-release", "--print-public"}); rc != 1 {
				t.Fatalf("rc=%d, want 1: %s", rc, stderr)
			}
			if !strings.Contains(stderr.String(), tc.want) || strings.Contains(stdout.String()+stderr.String(), tc.value) {
				t.Fatalf("refusal must name %q and never echo the value: %s", tc.want, stderr)
			}
		})
	}
}

// TestRegistryGenReleaseUnwritableOut refuses when the output directory
// cannot be created, after a successful derivation.
func TestRegistryGenReleaseUnwritableOut(t *testing.T) {
	sk, _ := releaseTestEnv(t)
	sk.Zero()
	blocker := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	w, _, stderr := testWriter()
	args := []string{"--derive-from-release", "--sequence", "1", "--artifacts", fixturesDir, "--out", filepath.Join(blocker, "out")}
	if rc := run(w, args); rc != 1 {
		t.Fatalf("rc=%d, want 1: %s", rc, stderr)
	}
	if _, err := os.Stat(filepath.Join(blocker, "out", "index.json")); err == nil {
		t.Fatal("index written under an unwritable output path")
	}
}

// TestRegistryGenRefusesShortSigningKey refuses a key-ref whose value is not
// an Ed25519 private key and writes no index.
func TestRegistryGenRefusesShortSigningKey(t *testing.T) {
	t.Setenv("REG_GEN_SHORT_TEST_KEY", base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 16)))
	out := t.TempDir()
	w, _, stderr := testWriter()
	if rc := run(w, []string{"--artifacts", fixturesDir, "--sequence", "1", "--out", out, "--key-ref", "REG_GEN_SHORT_TEST_KEY"}); rc != 1 {
		t.Fatalf("rc=%d, want 1: %s", rc, stderr)
	}
	if _, err := os.Stat(filepath.Join(out, "index.json")); err == nil {
		t.Fatal("index written with an invalid signing key")
	}
}

func TestRegistryGenZeroesSeedCopy(t *testing.T) {
	sk, _ := releaseTestEnv(t)
	defer sk.Zero()
	seed := sk.Seed()
	if bytes.Equal(seed, make([]byte, 32)) {
		t.Fatal("empty input")
	}
	key, err := deriveAndZeroSeed(seed)
	if err != nil {
		t.Fatal(err)
	}
	defer clear(key)
	if !bytes.Equal(seed, make([]byte, 32)) {
		t.Fatal("seed retained after derivation")
	}
}
