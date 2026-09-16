// Purpose: tests for SelectKeystore, Enroll, and storageFailure — the
//
//	fallback decision that keeps an enrolled trust record from being
//	silently orphaned.
//
// NewKeystore() is a package-level function, not injectable, and on this
// host (darwin+cgo) it returns the real Keychain bridge: calling
// GenerateKey on it would write a real item to the operator's login
// Keychain, which this suite must never do (see keystore_darwin_test.go's
// own TestNewKeystore_ConstructsWithRealBridge comment for the same
// boundary). Every case below is therefore driven through the file-tier
// side of the decision, which fileKeyExists lets SelectKeystore resolve to
// WITHOUT ever constructing the platform keystore. IsAvailable() alone is
// safe (a read-only capability probe) and is exercised elsewhere.
//
// The dir=="" contract (Enroll never falls back and returns the platform's
// own error verbatim) and the platform-storage-failure fallback contract
// are NOT exercised here for that reason — both require calling GenerateKey
// on the real platform keystore. storageFailure, the predicate that
// actually encodes the fallback rule, IS fully unit-tested directly.
//
// SPORT: internal/elevation keystore-select tests/ADD — coverage (cov/elevation).
package elevation

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

// TestStorageFailure is the direct unit test of the rule Enroll's fallback
// depends on: a STORAGE failure (unavailable or permission-denied) falls
// back to the file keystore; an INTEGRITY failure never does, because it
// means a key file already exists and is damaged, and silently writing a
// new one there would orphan the enrolled trust record.
func TestStorageFailure(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"unavailable", cascade.New(cascade.KindUnavailable, "x"), true},
		{"permission-denied", cascade.New(cascade.KindPermissionDenied, "x"), true},
		{"integrity", cascade.New(cascade.KindIntegrity, "x"), false},
		{"not-found", cascade.New(cascade.KindNotFound, "x"), false},
		{"plain-error", errors.New("boom"), false},
		{"nil", nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := storageFailure(tc.err); got != tc.want {
				t.Errorf("storageFailure(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

// TestFileKeystoreIsAvailable covers keystore_file.go's IsAvailable: a
// usable directory reports true, no directory reports false.
func TestFileKeystoreIsAvailable(t *testing.T) {
	withDir := fileKeystore{dir: t.TempDir()}
	if !withDir.IsAvailable() {
		t.Error("IsAvailable() = false for a real directory, want true")
	}
	noDir := fileKeystore{dir: ""}
	if noDir.IsAvailable() {
		t.Error("IsAvailable() = true with no directory, want false")
	}
}

// TestSelectKeystorePrefersExistingFileKey proves an existing file key wins
// over the platform keystore: a previous enrolment already resolved this
// host's answer, and switching back would orphan it. fileKeyExists short-
// circuits SelectKeystore before it ever constructs the platform keystore,
// so this never touches the real Keychain.
func TestSelectKeystorePrefersExistingFileKey(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, elevationKeyFileName), []byte("placeholder"), 0o600); err != nil {
		t.Fatal(err)
	}
	got := SelectKeystore(dir)
	if got.Tier() != TierFile {
		t.Fatalf("Tier() = %q, want %q: an existing file key must win over the platform keystore", got.Tier(), TierFile)
	}
	if _, ok := got.(fileKeystore); !ok {
		t.Errorf("SelectKeystore returned %T, want fileKeystore", got)
	}
}

// TestEnrollWithExistingFileKeyIsIdempotentAndReportsTier drives Enroll
// through the same existing-file-key path: the key is left untouched (no
// orphaned trust record) and the returned tier is the one actually used, so
// a caller can disclose it.
func TestEnrollWithExistingFileKeyIsIdempotentAndReportsTier(t *testing.T) {
	dir := t.TempDir()
	seed := NewFileKeystore(dir)
	if err := seed.GenerateKey(); err != nil {
		t.Fatalf("seeding a file key: %v", err)
	}
	before, err := seed.PubKeyB64()
	if err != nil {
		t.Fatal(err)
	}

	ks, err := Enroll(dir)
	if err != nil {
		t.Fatalf("Enroll: %v", err)
	}
	if ks.Tier() != TierFile {
		t.Errorf("Enroll returned tier %q, want %q", ks.Tier(), TierFile)
	}
	after, err := ks.PubKeyB64()
	if err != nil {
		t.Fatal(err)
	}
	if before != after {
		t.Error("Enroll replaced an existing file key, orphaning the enrolled trust record")
	}
}

// TestEnrollReturnsIntegrityErrorVerbatimWithNoFallback covers the "never
// on integrity" half of the contract via the reachable file-tier path: a
// damaged key file already exists, so SelectKeystore resolves to the file
// keystore directly (never touching the platform), and GenerateKey's load
// surfaces a KindIntegrity error. Enroll must return it exactly rather than
// attempting to fall back from a keystore that is already the fallback.
func TestEnrollReturnsIntegrityErrorVerbatimWithNoFallback(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, elevationKeyFileName), []byte("not base64!!"), 0o600); err != nil {
		t.Fatal(err)
	}
	ks, err := Enroll(dir)
	if ks != nil {
		t.Errorf("Enroll returned a non-nil keystore alongside an error: %v", ks)
	}
	if !cascade.HasKind(err, cascade.KindIntegrity) {
		t.Errorf("Enroll err = %v, want KindIntegrity returned verbatim", err)
	}
}

// TestEnrollReturnsFileStorageFailureVerbatimWithNoFallback is the same
// no-double-fallback guard with a STORAGE-shaped failure instead of an
// integrity one: the key file exists but cannot be read (KindUnavailable),
// so selected is already file-tier and Enroll must still return the error
// verbatim rather than looping back into another file-keystore attempt.
func TestEnrollReturnsFileStorageFailureVerbatimWithNoFallback(t *testing.T) {
	dir := t.TempDir()
	keyPath := filepath.Join(dir, elevationKeyFileName)
	if err := os.WriteFile(keyPath, []byte("irrelevant"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(keyPath, 0o000); err != nil {
		t.Fatal(err)
	}
	// An unreadable file is how this case is staged, and there are two
	// hosts where chmod does not produce one: Windows, which has no Unix
	// mode bits at all, and any runner executing as root, which reads
	// regardless. Rather than guess which one we are on, ask the question
	// the test actually depends on and skip when the answer is no.
	if f, err := os.Open(keyPath); err == nil {
		_ = f.Close()
		t.Skip("this host can still read a 0000 file, so the storage failure cannot be staged")
	}

	ks, err := Enroll(dir)
	if ks != nil {
		t.Errorf("Enroll returned a non-nil keystore alongside an error: %v", ks)
	}
	if !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Errorf("Enroll err = %v, want KindUnavailable returned verbatim", err)
	}
}
