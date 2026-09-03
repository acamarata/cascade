// Purpose: ElevationTrustStore's TOFU tests. TestTrustStore_SecondEnroll_
//
//	DifferentKey_Refused is the single most important test in this
//	ticket: an attacker who can call --enroll a second time must NEVER
//	be able to silently replace the trusted device key.
//
// SPORT: internal/elevation trust tests/ADDED (P1-E04-W1-S07-T6).
package elevation

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
)

// writeCorruptFile writes non-JSON bytes to path, creating its parent
// directory, so TestFileBackend_CorruptFile_FailsClosed can prove Load
// refuses rather than crashing or silently treating garbage as absent.
func writeCorruptFile(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, []byte("{not valid json"), 0o600)
}

// memBackend is an in-memory Backend fake (Art.1: _test.go only).
// corrupt, when true, makes Load report a parse error, exercising the
// "unreadable record must never read as either enrolled or unenrolled"
// fail-closed path.
type memBackend struct {
	mu      sync.Mutex
	rec     TrustRecord
	present bool
	corrupt bool
	saveErr error
}

func (b *memBackend) Load() (TrustRecord, bool, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.corrupt {
		return TrustRecord{}, false, cascade.New(cascade.KindIntegrity, "memBackend: simulated corrupt record")
	}
	return b.rec, b.present, nil
}

func (b *memBackend) Save(rec TrustRecord) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.saveErr != nil {
		return b.saveErr
	}
	b.rec, b.present = rec, true
	return nil
}

func fixedClock(t time.Time) Clock { return fixedClockImpl{t} }

type fixedClockImpl struct{ t time.Time }

func (f fixedClockImpl) Now() time.Time { return f.t }

const pubKeyA = "QUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUE=" // 32 'A' bytes, base64
const pubKeyB = "QkJCQkJCQkJCQkJCQkJCQkJCQkJCQkJCQkJCQkJCQkJCQkJCQkJCQkI=" // 32 'B' bytes, base64

func TestTrustStore_Enroll_FirstRun(t *testing.T) {
	backend := &memBackend{}
	store := NewElevationTrustStore(backend, fixedClock(time.Unix(1000, 0)))

	fp, err := store.Enroll(pubKeyA)
	if err != nil {
		t.Fatalf("first Enroll: %v", err)
	}
	if fp == "" {
		t.Fatal("Enroll returned an empty fingerprint")
	}
	wantFP, _ := Fingerprint(pubKeyA)
	if fp != wantFP {
		t.Errorf("fingerprint = %q, want %q", fp, wantFP)
	}
	if !store.IsEnrolled() {
		t.Error("IsEnrolled false after a successful first Enroll")
	}
	if !backend.rec.TOFUAcknowledged {
		t.Error("TOFUAcknowledged not set on first enrollment")
	}
}

// TestTrustStore_SecondEnroll_DifferentKey_Refused is the ticket's most
// important test: an attacker enrolls a second, different key after
// legitimate enrollment has already happened, and it MUST be refused —
// never silently accepted, never overwriting the original record.
func TestTrustStore_SecondEnroll_DifferentKey_Refused(t *testing.T) {
	backend := &memBackend{}
	store := NewElevationTrustStore(backend, fixedClock(time.Unix(1000, 0)))

	firstFP, err := store.Enroll(pubKeyA)
	if err != nil {
		t.Fatalf("legitimate first Enroll: %v", err)
	}

	// The attacker's second, DIFFERENT key.
	_, err = store.Enroll(pubKeyB)
	if err == nil {
		t.Fatal("SECURITY: a second Enroll with a different key was accepted — TOFU is broken")
	}
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindConflict {
		t.Errorf("kind = %v (ok=%v), want KindConflict (ErrAlreadyEnrolled)", kind, ok)
	}

	// The record must be UNCHANGED: still the first key's fingerprint.
	rec, ok, loadErr := backend.Load()
	if loadErr != nil || !ok {
		t.Fatalf("Load after refused re-enroll: ok=%v err=%v", ok, loadErr)
	}
	if rec.FingerprintSHA256 != firstFP {
		t.Errorf("SECURITY: trust record fingerprint changed to %q after a refused re-enroll (was %q) — the attacker's key was accepted", rec.FingerprintSHA256, firstFP)
	}
	if rec.PubKeyB64 != pubKeyA {
		t.Error("SECURITY: trust record pubkey_b64 changed after a refused re-enroll")
	}

	// And the daemon-side verifier must still trust only the original key.
	if store.Verify(firstFP) != true {
		t.Error("original key's fingerprint no longer verifies after the refused re-enroll attempt")
	}
	attackerFP, _ := Fingerprint(pubKeyB)
	if store.Verify(attackerFP) {
		t.Fatal("SECURITY: the attacker's second key's fingerprint verifies — TOFU is broken")
	}
}

// TestTrustStore_SecondEnroll_SameKey_StillRefused proves Enroll refuses a
// second call even when the caller supplies the SAME key it already
// enrolled: this ticket's Enroll takes no attestation, so it has no way to
// distinguish "the legitimate device re-enrolling itself" from "an
// attacker who happens to already know the public key" — R-14.163 says
// refuse when the safe case cannot be proven, not "when it looks safe".
func TestTrustStore_SecondEnroll_SameKey_StillRefused(t *testing.T) {
	backend := &memBackend{}
	store := NewElevationTrustStore(backend, fixedClock(time.Unix(1000, 0)))
	if _, err := store.Enroll(pubKeyA); err != nil {
		t.Fatalf("first Enroll: %v", err)
	}
	_, err := store.Enroll(pubKeyA)
	if err == nil {
		t.Fatal("second Enroll (even with the identical key) must still be refused without an attestation")
	}
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindConflict {
		t.Errorf("kind = %v (ok=%v), want KindConflict", kind, ok)
	}
}

func TestTrustStore_Verify_Match(t *testing.T) {
	backend := &memBackend{}
	store := NewElevationTrustStore(backend, fixedClock(time.Unix(1000, 0)))
	fp, _ := store.Enroll(pubKeyA)
	if !store.Verify(fp) {
		t.Error("Verify(enrolled fingerprint) = false, want true")
	}
}

func TestTrustStore_Verify_Mismatch(t *testing.T) {
	backend := &memBackend{}
	store := NewElevationTrustStore(backend, fixedClock(time.Unix(1000, 0)))
	if _, err := store.Enroll(pubKeyA); err != nil {
		t.Fatalf("Enroll: %v", err)
	}
	if store.Verify("not-the-real-fingerprint") {
		t.Fatal("Verify accepted a mismatched fingerprint")
	}
}

func TestTrustStore_Verify_NoRecord(t *testing.T) {
	store := NewElevationTrustStore(&memBackend{}, fixedClock(time.Unix(1000, 0)))
	if store.Verify("anything") {
		t.Fatal("Verify against an empty store must fail closed (false)")
	}
}

func TestTrustStore_Verify_CorruptRecord_FailsClosed(t *testing.T) {
	backend := &memBackend{corrupt: true}
	store := NewElevationTrustStore(backend, fixedClock(time.Unix(1000, 0)))
	if store.Verify("anything") {
		t.Fatal("Verify must fail closed (false) when the backend cannot be read, never treat unreadable as trusted")
	}
	if store.IsEnrolled() {
		t.Fatal("IsEnrolled must fail closed (false) when the backend cannot be read")
	}
}

func TestTrustStore_Enroll_CorruptExistingRecord_Refuses(t *testing.T) {
	// A corrupted-but-present record must never be silently overwritten by
	// a fresh Enroll: that would let an attacker who can corrupt the file
	// on disk force a re-enrollment bypassing TOFU entirely.
	backend := &memBackend{corrupt: true}
	store := NewElevationTrustStore(backend, fixedClock(time.Unix(1000, 0)))
	_, err := store.Enroll(pubKeyA)
	if err == nil {
		t.Fatal("Enroll over an unreadable existing record must refuse, not overwrite it")
	}
}

func TestTrustStore_GetPubKey(t *testing.T) {
	backend := &memBackend{}
	store := NewElevationTrustStore(backend, fixedClock(time.Unix(1000, 0)))
	if _, err := store.GetPubKey(); err == nil {
		t.Fatal("GetPubKey before enrollment must fail")
	} else if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindNotFound {
		t.Errorf("kind = %v (ok=%v), want KindNotFound", kind, ok)
	}

	if _, err := store.Enroll(pubKeyA); err != nil {
		t.Fatalf("Enroll: %v", err)
	}
	got, err := store.GetPubKey()
	if err != nil {
		t.Fatalf("GetPubKey after enrollment: %v", err)
	}
	if got != pubKeyA {
		t.Errorf("GetPubKey = %q, want %q", got, pubKeyA)
	}
}

func TestTrustStore_Fingerprint_InvalidBase64(t *testing.T) {
	_, err := Fingerprint("not base64 at all!!!")
	if err == nil {
		t.Fatal("Fingerprint must reject invalid base64")
	}
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindInvalidInput {
		t.Errorf("kind = %v (ok=%v), want KindInvalidInput", kind, ok)
	}
}

func TestTrustStore_Enroll_InvalidPubKey(t *testing.T) {
	store := NewElevationTrustStore(&memBackend{}, fixedClock(time.Unix(1000, 0)))
	_, err := store.Enroll("not valid base64!!!")
	if err == nil {
		t.Fatal("Enroll with invalid base64 must fail")
	}
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindInvalidInput {
		t.Errorf("kind = %v (ok=%v), want KindInvalidInput", kind, ok)
	}
}

func TestTrustStore_Enroll_SaveFails(t *testing.T) {
	backend := &memBackend{saveErr: cascade.New(cascade.KindUnavailable, "disk full")}
	store := NewElevationTrustStore(backend, fixedClock(time.Unix(1000, 0)))
	_, err := store.Enroll(pubKeyA)
	if err == nil {
		t.Fatal("Enroll must surface a backend Save failure")
	}
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindUnavailable {
		t.Errorf("kind = %v (ok=%v), want KindUnavailable", kind, ok)
	}
}

func TestTrustStore_GetPubKey_BackendError(t *testing.T) {
	store := NewElevationTrustStore(&memBackend{corrupt: true}, fixedClock(time.Unix(1000, 0)))
	_, err := store.GetPubKey()
	if err == nil {
		t.Fatal("GetPubKey must surface a backend read failure")
	}
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindUnavailable {
		t.Errorf("kind = %v (ok=%v), want KindUnavailable", kind, ok)
	}
}
