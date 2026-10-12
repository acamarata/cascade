package generator

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/plugin"
)

func TestDeriveRegistryKeyDeterministic(t *testing.T) {
	seed := bytes.Repeat([]byte{17}, 32)
	a, err := DeriveRegistryKey(seed)
	if err != nil {
		t.Fatal(err)
	}
	b, err := DeriveRegistryKey(seed)
	if err != nil || !bytes.Equal(a, b) {
		t.Fatal("derivation is not deterministic")
	}
	if !bytes.Equal(seed, bytes.Repeat([]byte{17}, 32)) {
		t.Fatal("modified caller seed")
	}
}

func TestDeriveRegistryKeyDiffersFromReleaseKey(t *testing.T) {
	seed := bytes.Repeat([]byte{17}, 32)
	release := ed25519.NewKeyFromSeed(seed)
	derived, err := DeriveRegistryKey(seed)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(derived.Public().(ed25519.PublicKey), release.Public().(ed25519.PublicKey)) {
		t.Fatal("keys not separated")
	}
	if hex.EncodeToString(derived.Seed()) != "a530da1ed60e1f180c180064b03dec76"+"af49740f68def4bddf7db5138d00a63c" {
		t.Fatal("HKDF golden mismatch")
	}
	for _, pair := range [][2]ed25519.PrivateKey{{release, derived}, {derived, release}} {
		payload := []byte(`{"schema_version":"1","entries":[]}`)
		idx := plugin.RegistryIndex{SchemaVersion: "1", Entries: []plugin.RegistryIndexEntry{}, Signature: base64.StdEncoding.EncodeToString(ed25519.Sign(pair[0], payload))}
		raw, err := json.Marshal(idx)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := (plugin.Ed25519Verifier{PublicKey: pair[0].Public().(ed25519.PublicKey)}).VerifyIndex(context.Background(), raw); err != nil {
			t.Fatal(err)
		}
		if _, err := (plugin.Ed25519Verifier{PublicKey: pair[1].Public().(ed25519.PublicKey)}).VerifyIndex(context.Background(), raw); err == nil {
			t.Fatal("cross-key index accepted")
		}
	}
}

func TestDeriveRegistryKeyRefusesBadSeedLength(t *testing.T) {
	for _, seed := range [][]byte{nil, make([]byte, 31), make([]byte, 33)} {
		_, err := DeriveRegistryKey(seed)
		want := cascade.New(cascade.KindInvalidInput, "registry-gen: release seed must be 32 bytes")
		if !errors.Is(err, want) || err.Error() != want.Error() {
			t.Fatalf("error = %v, want %v", err, want)
		}
	}
}

// The raw fixture seed is byte-identical to SecretKey.Seed without crossing pkg/internal.
func TestDeriveRegistryKeyMinisignCLIInterop(t *testing.T) {
	bin, err := exec.LookPath("minisign")
	if err != nil {
		t.Skip("minisign absent")
	}
	dir := t.TempDir()
	for _, name := range []string{"HOME", "USERPROFILE", "CASCADE_HOME"} {
		t.Setenv(name, dir)
	}
	seed := bytes.Repeat([]byte{17}, 32)
	priv, err := DeriveRegistryKey(seed)
	if err != nil {
		t.Fatal(err)
	}
	raw := append([]byte("Edtest-id1"), priv.Public().(ed25519.PublicKey)...)
	pub := filepath.Join(dir, "key.pub")
	msg := filepath.Join(dir, "message")
	if err := os.WriteFile(pub, []byte("untrusted comment: test\n"+base64.StdEncoding.EncodeToString(raw)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(msg, []byte("registry interoperability"), 0o600); err != nil {
		t.Fatal(err)
	}
	sig := ed25519.Sign(priv, []byte("registry interoperability"))
	global := ed25519.Sign(priv, append(append([]byte{}, sig...), []byte("test")...))
	armor := "untrusted comment: test\n" + base64.StdEncoding.EncodeToString(append([]byte("Edtest-id1"), sig...)) + "\ntrusted comment: test\n" + base64.StdEncoding.EncodeToString(global) + "\n"
	if err := os.WriteFile(msg+".minisig", []byte(armor), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command(bin, "-Vm", msg, "-p", pub).CombinedOutput(); err != nil {
		t.Fatalf("minisign: %v %s", err, out)
	}
}
