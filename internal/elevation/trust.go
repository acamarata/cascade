// Purpose: ElevationTrustStore, the daemon-side (and, in this W1 slice,
//
//	standalone-CLI-side) trust-on-first-use record for the enrolled
//	elevation public key: one record, one key, ever — until an explicit
//	re-enrollment path with attestation exists (S-06.T3, W2), a second
//	enrollment attempt is always refused.
//
// Inputs: a pubkey (base64) to enroll, or a fingerprint to verify.
// Outputs: the enrolled record's fingerprint and public key, or a typed
//
//	refusal.
//
// Constraints: R-14.163 fail-closed — Verify returns true ONLY on an exact
//
//	fingerprint match against an existing record; every other condition
//	(no record, unparseable record, mismatch) is false. No generic
//	domain-keyed KV API exists yet anywhere in this tree for another
//	package to write a single record through (internal/storage/domains.go
//	only exposes Bootstrap's anchor-table creation as of this ticket; it
//	has no Get/Set surface a consumer package can call) — rather than
//	build a bespoke sqlite table for one JSON record, or import
//	internal/storage's driver internals from outside its own package
//	boundary, this store persists its one record as a single JSON file
//	under internal/runtime's DataDir(), through an injectable Backend
//	seam so tests never touch a real CASCADE_HOME (Art.7.1). This is a
//	concrete decision this ticket had to make because the generic
//	domain-KV API this ticket's contract assumed had not landed as of
//	this ticket — see the ticket journal.
//
// SPORT: internal/elevation ElevationTrustStore/ADDED (P1-E04-W1-S07-T6).

package elevation

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

// TrustRecord is the persisted TOFU enrollment record, in the wire shape
// this ticket's contract names verbatim: {pubkey_b64, fingerprint_sha256,
// enrolled_at, tofu_acknowledged}.
type TrustRecord struct {
	PubKeyB64         string    `json:"pubkey_b64"`
	FingerprintSHA256 string    `json:"fingerprint_sha256"`
	EnrolledAt        time.Time `json:"enrolled_at"`
	TOFUAcknowledged  bool      `json:"tofu_acknowledged"`
}

// Clock abstracts time.Now so ElevationTrustStore never reads the wall
// clock directly (forbidigo). Duck-typed against internal/runtime.Clock
// and internal/testkit.Clock, both of which already satisfy it.
type Clock interface {
	Now() time.Time
}

// Backend persists exactly one TrustRecord. fileBackend is the production
// implementation; tests use a tmp-dir-rooted fileBackend or an in-memory
// fake (trust_test.go), never the real CASCADE_HOME.
type Backend interface {
	// Load returns the current record, or ok=false if none is enrolled.
	Load() (rec TrustRecord, ok bool, err error)
	// Save persists rec, replacing any prior record.
	Save(rec TrustRecord) error
}

// ElevationTrustStore is the TOFU trust store: Enroll succeeds exactly
// once per Backend; every subsequent Enroll call is refused (this
// ticket's Enroll signature carries no attestation parameter, so there is
// no path in this ticket for a re-enrollment to ever succeed — that path
// is S-06.T3/W2's, layered on top once it exists).
//
// The type name intentionally stutters with the package name: it is this
// ticket's contract-mandated name (04-PEWS-PLAN-W1-W3.md Epic D S-07.T6
// task 6), matching ElevationKeystore's stutter for the same reason.
//
//nolint:revive // contract-mandated name, see doc comment above
type ElevationTrustStore struct {
	backend Backend
	clock   Clock
}

// NewElevationTrustStore constructs a store over backend, driven by clock.
func NewElevationTrustStore(backend Backend, clock Clock) *ElevationTrustStore {
	return &ElevationTrustStore{backend: backend, clock: clock}
}

// Fingerprint returns the SHA-256 hex digest of the raw (base64-decoded)
// public key bytes. This is the concrete fingerprint format this ticket's
// contract left unspecified for a producer to pick — recorded here since
// internal/rpc's TrustStore.Lookup (elevation_attest.go) already ships
// keyed by an opaque fingerprint string and needs this package's callers
// to agree on one format.
func Fingerprint(pubKeyB64 string) (string, error) {
	raw, err := base64.StdEncoding.DecodeString(pubKeyB64)
	if err != nil {
		return "", cascade.Wrap(cascade.KindInvalidInput, err, "elevation: pubkey_b64 is not valid base64")
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

// Enroll records pubKeyB64 as the trusted key, IF AND ONLY IF no record
// exists yet (TOFU). It returns the new record's fingerprint on success.
// A pre-existing record — regardless of whether pubKeyB64 matches it —
// always returns ErrAlreadyEnrolled and never overwrites the record: this
// ticket's Enroll takes no attestation, so it has no way to prove the
// caller is the already-enrolled key's holder, and R-14.163 requires
// refusing rather than guessing.
func (s *ElevationTrustStore) Enroll(pubKeyB64 string) (fingerprint string, err error) {
	if _, ok, loadErr := s.backend.Load(); loadErr != nil {
		return "", cascade.Wrap(cascade.KindUnavailable, loadErr, "elevation: read trust record")
	} else if ok {
		return "", ErrAlreadyEnrolled()
	}

	fp, err := Fingerprint(pubKeyB64)
	if err != nil {
		return "", err
	}
	rec := TrustRecord{
		PubKeyB64:         pubKeyB64,
		FingerprintSHA256: fp,
		EnrolledAt:        s.clock.Now(),
		TOFUAcknowledged:  true,
	}
	if err := s.backend.Save(rec); err != nil {
		return "", cascade.Wrap(cascade.KindUnavailable, err, "elevation: write trust record")
	}
	return fp, nil
}

// Verify reports whether fingerprint matches the enrolled record's
// fingerprint. Fails closed: no record, an unreadable record, or any
// mismatch all return false — never true on anything this store could not
// positively confirm (R-14.163).
func (s *ElevationTrustStore) Verify(fingerprint string) bool {
	rec, ok, err := s.backend.Load()
	if err != nil || !ok {
		return false
	}
	return rec.FingerprintSHA256 == fingerprint
}

// GetPubKey returns the enrolled public key (base64), or a typed
// not-enrolled error.
func (s *ElevationTrustStore) GetPubKey() (string, error) {
	rec, ok, err := s.backend.Load()
	if err != nil {
		return "", cascade.Wrap(cascade.KindUnavailable, err, "elevation: read trust record")
	}
	if !ok {
		return "", cascade.New(cascade.KindNotFound, "elevation: no trust record enrolled")
	}
	return rec.PubKeyB64, nil
}

// IsEnrolled reports whether a trust record exists. A backend read
// failure is reported as NOT enrolled (fail closed: an unreadable record
// can never be treated as proof of trust).
func (s *ElevationTrustStore) IsEnrolled() bool {
	_, ok, err := s.backend.Load()
	return err == nil && ok
}

// fileBackend is the production Backend: one JSON file under dir.
type fileBackend struct {
	path string
}

// NewFileBackend returns a Backend that persists its record at
// <dataDir>/elevation/trust.json.
func NewFileBackend(dataDir string) Backend {
	return fileBackend{path: filepath.Join(dataDir, "elevation", "trust.json")}
}

func (b fileBackend) Load() (TrustRecord, bool, error) {
	data, err := os.ReadFile(b.path)
	if err != nil {
		if os.IsNotExist(err) {
			return TrustRecord{}, false, nil
		}
		return TrustRecord{}, false, err
	}
	var rec TrustRecord
	if err := json.Unmarshal(data, &rec); err != nil {
		// An unparseable record must never be treated as "no record" (that
		// would let a second Enroll silently overwrite a corrupted-but-
		// real enrollment) nor as a valid one (Verify must not match
		// against garbage). Surface it as an error so callers refuse.
		return TrustRecord{}, false, cascade.Wrap(cascade.KindIntegrity, err, "elevation: trust record is not valid JSON")
	}
	return rec, true, nil
}

func (b fileBackend) Save(rec TrustRecord) error {
	data, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return err
	}
	return runtime.WriteBytesAtomic(b.path, data)
}
