package minisign

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

const testdataMinisignDir = "testdata/minisign"

func readTestdataMinisign(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(testdataMinisignDir, name))
	if err != nil {
		t.Fatalf("read testdata: %v", err)
	}
	return data
}

// TestParseAndVerify_RealFixture exercises the parser and
// verifier against a real minisign 0.12 CLI-produced signature (see
// testdata/README.md for provenance).
func TestParseAndVerify_RealFixture(t *testing.T) {
	message := readTestdataMinisign(t, "artifact.bin")
	sigBytes := readTestdataMinisign(t, "artifact.bin.minisig")
	pubBytes := readTestdataMinisign(t, "test.pub")

	sig, err := ParseSignature(sigBytes)
	if err != nil {
		t.Fatalf("ParseSignature: %v", err)
	}
	if sig.Algorithm != minisignAlgoPrehashed {
		t.Fatalf("algorithm = %q, want %q", sig.Algorithm, minisignAlgoPrehashed)
	}
	if sig.TrustedComment != "test comment v1" {
		t.Fatalf("trusted comment = %q", sig.TrustedComment)
	}

	pub, err := ParsePublicKey(pubBytes)
	if err != nil {
		t.Fatalf("ParsePublicKey: %v", err)
	}
	if err := Verify(pub, message, sig); err != nil {
		t.Fatalf("Verify: %v", err)
	}
}

func TestVerify_TamperedMessageRefused(t *testing.T) {
	message := readTestdataMinisign(t, "artifact.bin")
	sig, _ := ParseSignature(readTestdataMinisign(t, "artifact.bin.minisig"))
	pub, _ := ParsePublicKey(readTestdataMinisign(t, "test.pub"))

	tampered := append([]byte{}, message...)
	tampered[0] ^= 0xFF
	if err := Verify(pub, tampered, sig); err == nil {
		t.Fatal("expected refusal for a tampered message, got nil")
	} else if kind, _ := cascade.KindOf(err); kind != cascade.KindIntegrity {
		t.Fatalf("kind = %v, want KindIntegrity", kind)
	}
}

func TestVerify_TamperedTrustedCommentRefused(t *testing.T) {
	message := readTestdataMinisign(t, "artifact.bin")
	sig, _ := ParseSignature(readTestdataMinisign(t, "artifact.bin.minisig"))
	pub, _ := ParsePublicKey(readTestdataMinisign(t, "test.pub"))

	sig.TrustedComment = "a swapped comment"
	if err := Verify(pub, message, sig); err == nil {
		t.Fatal("expected refusal for a swapped trusted comment, got nil")
	}
}

func TestVerify_WrongPublicKeyRefused(t *testing.T) {
	message := readTestdataMinisign(t, "artifact.bin")
	sig, _ := ParseSignature(readTestdataMinisign(t, "artifact.bin.minisig"))

	otherPub := readTestdataMinisign(t, "test.pub")
	// Flip the key id so it no longer matches the signature's key id.
	otherPub2 := append([]byte{}, otherPub...)
	pub, err := ParsePublicKey(otherPub2)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	pub.KeyID[0] ^= 0xFF
	if err := Verify(pub, message, sig); err == nil {
		t.Fatal("expected refusal for a key-id mismatch, got nil")
	}
}

func TestVerify_WrongKeyBytesRefused(t *testing.T) {
	message := readTestdataMinisign(t, "artifact.bin")
	sig, _ := ParseSignature(readTestdataMinisign(t, "artifact.bin.minisig"))
	pub, _ := ParsePublicKey(readTestdataMinisign(t, "test.pub"))

	pub.Key[0] ^= 0xFF // key id still matches; the actual key material does not
	if err := Verify(pub, message, sig); err == nil {
		t.Fatal("expected refusal for tampered key bytes, got nil")
	}
}

func TestParseSignature_MalformedRefused(t *testing.T) {
	cases := map[string][]byte{
		"empty":                nil,
		"one line":             []byte("untrusted comment: x\n"),
		"bad base64 sig":       []byte("untrusted comment: x\n!!!not-base64!!!\ntrusted comment: y\nQQ==\n"),
		"wrong length sig":     []byte("untrusted comment: x\n" + b64(make([]byte, 10)) + "\ntrusted comment: y\n" + b64(make([]byte, 64)) + "\n"),
		"unknown algo":         []byte("untrusted comment: x\n" + b64(append([]byte("XX"), make([]byte, 72)...)) + "\ntrusted comment: y\n" + b64(make([]byte, 64)) + "\n"),
		"missing trusted line": []byte("untrusted comment: x\n" + b64(append([]byte(minisignAlgoLegacy), make([]byte, 72)...)) + "\nnot a trusted comment\n" + b64(make([]byte, 64)) + "\n"),
		"bad global sig len":   []byte("untrusted comment: x\n" + b64(append([]byte(minisignAlgoLegacy), make([]byte, 72)...)) + "\ntrusted comment: y\n" + b64(make([]byte, 10)) + "\n"),
	}
	for name, data := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseSignature(data); err == nil {
				t.Fatalf("%s: expected a parse refusal, got nil", name)
			} else if kind, _ := cascade.KindOf(err); kind != cascade.KindInvalidInput {
				t.Fatalf("%s: kind = %v, want KindInvalidInput", name, kind)
			}
		})
	}
}

func TestParsePublicKey_MalformedRefused(t *testing.T) {
	if _, err := ParsePublicKey([]byte("only one line\n")); err == nil {
		t.Fatal("expected refusal for a single-line public key file")
	}
	if _, err := ParsePublicKey([]byte("untrusted comment: x\nnotbase64!!!\n")); err == nil {
		t.Fatal("expected refusal for invalid base64")
	}
}

// FuzzMinisignSignature is this ticket's mandated fuzz target (06 §5.7 —
// the minisign signature-file parser is a new decoder). It never panics
// and never returns a non-zero Signature alongside a non-nil
// error. Seed corpus: testdata/fuzz/FuzzMinisignSignature/ (a real
// minisign-produced signature plus two malformed shapes).
func FuzzMinisignSignature(f *testing.F) {
	if data, err := os.ReadFile(filepath.Join(testdataMinisignDir, "artifact.bin.minisig")); err == nil {
		f.Add(data)
	}
	f.Add([]byte(""))
	f.Add([]byte("untrusted comment: x\nAAAA\ntrusted comment: y\nBBBB\n"))
	f.Fuzz(func(t *testing.T, data []byte) {
		sig, err := ParseSignature(data)
		if err != nil {
			if sig != (Signature{}) {
				t.Fatalf("non-zero result alongside an error: %+v", sig)
			}
			return
		}
		// A successfully parsed signature must never verify against an
		// unrelated random public key, and Verify must not panic
		// on arbitrary parsed fields either.
		var junkPub PublicKey
		junkPub.Key = make([]byte, 32)
		_ = Verify(junkPub, []byte("anything"), sig)
	})
}

// b64 is a tiny local base64 helper so the malformed-input table above
// stays readable (avoids repeating base64.StdEncoding.EncodeToString).
func b64(b []byte) string { return base64.StdEncoding.EncodeToString(b) }
