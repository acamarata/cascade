package elevation

import (
	"crypto/ed25519"
	"encoding/base64"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

// Purpose (this file): the file-backed keystore's contract, and the two
//   properties that make an admittedly weaker proof acceptable — it is
//   selected only when nothing better exists, and it always names itself.
// SPORT: internal/elevation file-keystore tests (ADD) — P1-W3-01.

// TestGenerateKeyIsIdempotent holds the rule that matters most: a second
// enrolment must not orphan the trust record by regenerating the key.
func TestGenerateKeyIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	ks := NewFileKeystore(dir)

	if err := ks.GenerateKey(); err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	first, err := ks.PubKeyB64()
	if err != nil {
		t.Fatal(err)
	}
	if err := ks.GenerateKey(); err != nil {
		t.Fatalf("second GenerateKey: %v", err)
	}
	second, err := ks.PubKeyB64()
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Error("a second GenerateKey replaced the key, orphaning any enrolled trust record")
	}
}

// TestTheKeyFileIsNotWorldReadable is the one protection this tier has.
//
// The mode assertion is POSIX-only, and deliberately so rather than by
// omission: Windows does not implement Unix permission bits at all, so
// os.Stat there reports 0666 for a file opened with 0600 and the check would
// be asserting the syscall's fallback, not the keystore's intent. On Windows
// the file's confidentiality rests on the ACL its parent (the per-user config
// directory) inherits, which the Go standard library exposes no portable way
// to read. What IS asserted on every platform is that GenerateKey wrote a
// regular, non-empty file at the expected path — so the test cannot pass
// vacuously on the platform where the mode half is skipped.
func TestTheKeyFileIsNotWorldReadable(t *testing.T) {
	dir := t.TempDir()
	if err := NewFileKeystore(dir).GenerateKey(); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(dir, elevationKeyFileName))
	if err != nil {
		t.Fatal(err)
	}
	if !info.Mode().IsRegular() || info.Size() == 0 {
		t.Fatalf("key file is %v of %d bytes, want a non-empty regular file", info.Mode(), info.Size())
	}
	if runtime.GOOS == "windows" {
		t.Skip("Windows has no Unix permission bits; the mode half of this check cannot run there")
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("key file mode = %v, want 0600", perm)
	}
}

// TestSignVerifiesAgainstTheEnrolledKey proves the signature is real.
func TestSignVerifiesAgainstTheEnrolledKey(t *testing.T) {
	ks := NewFileKeystore(t.TempDir())
	if err := ks.GenerateKey(); err != nil {
		t.Fatal(err)
	}
	pubB64, err := ks.PubKeyB64()
	if err != nil {
		t.Fatal(err)
	}
	pub, err := base64.StdEncoding.DecodeString(pubB64)
	if err != nil {
		t.Fatal(err)
	}
	payload := []byte("request-id action-hash nonce")
	sig, err := ks.Sign(payload)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	if !ed25519.Verify(pub, payload, sig) {
		t.Error("the signature does not verify against the enrolled public key")
	}
	if ed25519.Verify(pub, []byte("a different payload"), sig) {
		t.Error("the signature verified against a payload it was not made for")
	}
}

// TestAnUnenrolledKeystoreRefuses covers both read paths.
func TestAnUnenrolledKeystoreRefuses(t *testing.T) {
	ks := NewFileKeystore(t.TempDir())
	if _, err := ks.PubKeyB64(); !cascade.HasKind(err, cascade.KindNotFound) {
		t.Errorf("PubKeyB64: err = %v, want KindNotFound", err)
	}
	if _, err := ks.Sign([]byte("x")); !cascade.HasKind(err, cascade.KindNotFound) {
		t.Errorf("Sign: err = %v, want KindNotFound", err)
	}
}

// TestACorruptKeyFileIsNotSilentlyReplaced is the fail-closed rule. A
// keystore that regenerated on a damaged file would report a working
// enrolment while every previously-issued attestation stopped verifying.
func TestACorruptKeyFileIsNotSilentlyReplaced(t *testing.T) {
	dir := t.TempDir()
	ks := NewFileKeystore(dir)
	if err := ks.GenerateKey(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, elevationKeyFileName), []byte("not base64!!"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ks.GenerateKey(); !cascade.HasKind(err, cascade.KindIntegrity) {
		t.Errorf("GenerateKey over a corrupt file: err = %v, want KindIntegrity", err)
	}
	if _, err := ks.Sign([]byte("x")); !cascade.HasKind(err, cascade.KindIntegrity) {
		t.Errorf("Sign over a corrupt file: err = %v, want KindIntegrity", err)
	}
	// And a truncated-but-valid-base64 key is caught by length, not by
	// producing a panicking ed25519 key.
	if err := os.WriteFile(filepath.Join(dir, elevationKeyFileName),
		[]byte(base64.StdEncoding.EncodeToString([]byte("short"))), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ks.Sign([]byte("x")); !cascade.HasKind(err, cascade.KindIntegrity) {
		t.Errorf("Sign over a truncated key: err = %v, want KindIntegrity", err)
	}
}

// TestTheFileTierAlwaysNamesItself is the disclosure half of the trade:
// the weaker proof must be visible before and after enrolment.
func TestTheFileTierAlwaysNamesItself(t *testing.T) {
	ks := NewFileKeystore(t.TempDir())
	if got := ks.Tier(); got != TierFile {
		t.Errorf("Tier() before enrolment = %q, want %q", got, TierFile)
	}
	if err := ks.GenerateKey(); err != nil {
		t.Fatal(err)
	}
	if got := ks.Tier(); got != TierFile {
		t.Errorf("Tier() after enrolment = %q, want %q", got, TierFile)
	}
}

// TestSelectKeystoreNeverDowngradesSilently is the selection rule. It
// asserts the OUTCOME an operator can observe — whichever keystore is
// returned reports a tier, and on a host with no platform keystore that
// tier is the file one.
func TestSelectKeystoreNeverDowngradesSilently(t *testing.T) {
	dir := t.TempDir()
	got := SelectKeystore(dir)
	if NewKeystore().IsAvailable() {
		if got.Tier() == TierFile {
			t.Error("a host with a platform keystore was downgraded to the file tier")
		}
		return
	}
	if got.Tier() != TierFile {
		t.Errorf("Tier() = %q on a host with no platform keystore, want %q", got.Tier(), TierFile)
	}
	// With no directory there is nothing to fall back TO, so the platform
	// keystore's own honest refusal is returned rather than a file keystore
	// that cannot store anything.
	if SelectKeystore("").Tier() == TierFile {
		t.Error("a file keystore was selected with no directory to put a key in")
	}
}
