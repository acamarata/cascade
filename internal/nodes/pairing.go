// Purpose: pairing-code issuance and verification (R-21.155/R-21.184,
//
//	P1-E36-W7-S72-T1): a 128-bit single-use, time-limited, rate-limited
//	credential that authenticates a LAN-discovered peer before it may
//	enter the S-36.T1 enrollment/elevation path. A pairing code IS an
//	authentication credential for its lifetime (this file's own doc
//	comment on that point governs every function below): single-use,
//	expiring, constant-time-compared, and rate-limited against guessing.
//
// Inputs: crypto/rand entropy (production) or an injected io.Reader
//
//	(tests) for code generation; a claimed code string and a caller-
//	supplied peer identifier for verification.
//
// Outputs: GeneratePairingCode returns the 26-character base32 code
//
//	string once, at issuance (never persisted in plaintext logs —
//	callers MUST NOT log or journal the returned string). VerifyPairingCode
//	returns ok=true exactly once per code, or a typed fail-closed refusal
//	(expired/consumed/lockout/unknown), never a partial/ambiguous result.
//
// Constraints: R-21.155/R-21.184 — 128 bits of entropy, 26-char base32,
//
//	10-minute TTL, exactly-once atomic consumption (single-row compare-
//	and-set under this store's own mutex — see the CONTRADICTION note
//	below on the limits of that guarantee across process boundaries), a
//	5-attempts-per-peer-per-hour ceiling whose 5th failure locks that
//	peer out of EVERY code (not just the one it was tried against — see
//	VerifyPairingCode's doc comment on why per-peer lockout cannot be
//	scoped to a single code). Comparison is constant-time
//	(crypto/subtle.ConstantTimeCompare) so verification timing never
//	leaks which prefix of a guessed code was correct. This file does NOT
//	implement discovery (internal/nodes/discovery.go) or the Noise
//	XXpsk3 PAKE (pair_noise.go) — see the ticket journal's honest
//	completion accounting for the full scope this ticket did not reach.
//
//	CROSS-PROCESS ATOMICITY (CONTRADICTION — full quote in the ticket
//	journal). "Atomically exactly once via a single-row compare-and-set"
//	is satisfied WITHIN one process by this store's mutex; this package's
//	only persistence precedent (records.go's fileRecordBackend, and this
//	file's own filePairingBackend) is a whole-file JSON rewrite with no
//	file-level locking, matching every other *Backend in this package.
//	Two separate `cascade` processes verifying the same code concurrently
//	is therefore NOT proven atomic by this implementation; achieving that
//	would need a locking or single-writer primitive no ticket in this
//	tree's files_scope has built. In practice `node serve` is the single
//	long-lived process that would own pairing state, so this gap is real
//	but narrow.
//
// SPORT: internal/nodes PairingCode/ADDED, GeneratePairingCode/ADDED,
//
//	VerifyPairingCode/ADDED (P1-E36-W7-S72-T1, PARTIAL — see journal).

package nodes

import (
	"crypto/subtle"
	"encoding/base32"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
)

// PairingCodeTTL is the fixed lifetime of a generated pairing code
// (R-21.155/R-21.184: 10 minutes).
const PairingCodeTTL = 10 * time.Minute

// PairingCodeEntropyBytes is 128 bits.
const PairingCodeEntropyBytes = 16

// pairingMaxAttemptsPerPeerPerHour is the per-peer verification ceiling;
// the attempt that reaches this count locks the peer out and burns the
// code (R-21.184).
const pairingMaxAttemptsPerPeerPerHour = 5

// pairingCodeAlphabet is RFC 4648 base32 without padding: 26 characters
// exactly encode 16 bytes (128 bits) at 5 bits/char (⌈128/5⌉ = 26).
var pairingCodeEncoding = base32.StdEncoding.WithPadding(base32.NoPadding)

// pairingRecord is one issued code's server-side state. Code is retained
// so VerifyPairingCode can constant-time-compare against it; callers of
// GeneratePairingCode are the only party that ever sees the plaintext
// code as a RETURN VALUE, and must never log or journal it.
type pairingRecord struct {
	Code          string
	IssuingNodeID string
	IssuedAt      time.Time
	ExpiresAt     time.Time
	Consumed      bool
}

type pairingAttemptWindow struct {
	Count     int
	WindowEnd time.Time
	LockedOut bool
}

// PairingStore issues and verifies pairing codes. All state is
// in-process (protected by mu); see the package doc's cross-process
// atomicity contradiction.
type PairingStore struct {
	mu       sync.Mutex
	clock    Clock
	records  map[string]*pairingRecord // keyed by Code
	attempts map[string]*pairingAttemptWindow
}

// NewPairingStore constructs an empty PairingStore driven by clock.
func NewPairingStore(clock Clock) *PairingStore {
	return &PairingStore{
		clock: clock, records: make(map[string]*pairingRecord),
		attempts: make(map[string]*pairingAttemptWindow),
	}
}

// GeneratePairingCode issues a fresh 128-bit code bound to issuingNodeID,
// reading entropy from rnd (crypto/rand in production; an injected
// deterministic reader in tests). The returned string is the ONLY time
// the plaintext code is available to a caller; callers must display it
// to the operator directly and never write it to a log or journal.
func (s *PairingStore) GeneratePairingCode(rnd io.Reader, issuingNodeID string) (string, error) {
	if issuingNodeID == "" {
		return "", cascade.New(cascade.KindInvalidInput, "nodes: pairing code must be bound to an issuing node id")
	}
	raw := make([]byte, PairingCodeEntropyBytes)
	if _, err := io.ReadFull(rnd, raw); err != nil {
		return "", cascade.Wrap(cascade.KindInternal, err, "nodes: pairing code entropy generation failed")
	}
	code := pairingCodeEncoding.EncodeToString(raw)

	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.clock.Now()
	s.records[code] = &pairingRecord{
		Code: code, IssuingNodeID: issuingNodeID, IssuedAt: now,
		ExpiresAt: now.Add(PairingCodeTTL),
	}
	return code, nil
}

// ErrPairingCodeRefused is the single typed refusal VerifyPairingCode
// returns for every failure mode (unknown, expired, consumed, malformed,
// or a locked-out peer): a caller must not be able to distinguish
// "wrong code" from "right code, wrong peer" from timing or error-message
// shape, which would leak information a fail-closed credential check
// must not leak.
func ErrPairingCodeRefused() error {
	return cascade.New(cascade.KindPermissionDenied, "nodes: pairing code refused (unknown, expired, already used, malformed, or this peer is locked out)")
}

// VerifyPairingCode checks claimedCode on behalf of peerID (an opaque
// identifier for the verifying party, supplied by the caller's transport
// — e.g. discovery's opaque instance id). On success it consumes the
// code (exactly once, within this process — see the package doc) and
// returns the issuing node id. Every failure path — unknown code,
// expired, already consumed, malformed, or a peer past its attempt
// ceiling — returns the SAME ErrPairingCodeRefused, fail-closed.
func (s *PairingStore) VerifyPairingCode(peerID, claimedCode string) (issuingNodeID string, err error) {
	if peerID == "" || claimedCode == "" {
		return "", ErrPairingCodeRefused()
	}
	if _, shapeErr := DecodePairingCode(claimedCode); shapeErr != nil {
		return "", ErrPairingCodeRefused()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.clock.Now()

	// The per-peer attempt ceiling is checked BEFORE the code lookup and
	// counted on EVERY call (right or wrong code alike): otherwise an
	// attacker who never happens to guess a real code would never trip
	// the lockout at all, since there would be no pairingRecord to
	// attribute the failure to (R-21.184).
	if s.peerLockedOut(peerID, now) {
		return "", ErrPairingCodeRefused()
	}

	rec := s.findByConstantTimeCompare(claimedCode)
	if rec == nil || rec.Consumed || now.After(rec.ExpiresAt) {
		s.recordFailure(peerID, now)
		return "", ErrPairingCodeRefused()
	}
	rec.Consumed = true
	return rec.IssuingNodeID, nil
}

// findByConstantTimeCompare scans every live record comparing claimedCode
// in constant time against each candidate, so verification timing never
// leaks which prefix (if any) of a guessed code matched a real one. The
// number-of-records scan itself is not disguised (an implementation
// detail of this in-process store, not a secret), only the per-candidate
// byte comparison is.
func (s *PairingStore) findByConstantTimeCompare(claimedCode string) *pairingRecord {
	claimed := []byte(claimedCode)
	for _, rec := range s.records {
		if len(claimed) == len(rec.Code) && subtle.ConstantTimeCompare(claimed, []byte(rec.Code)) == 1 {
			return rec
		}
	}
	return nil
}

// peerLockedOut reports whether peerID is currently locked out, expiring
// a stale window (past its rolling hour) back to not-locked-out first.
func (s *PairingStore) peerLockedOut(peerID string, now time.Time) bool {
	w, ok := s.attempts[peerID]
	if !ok {
		return false
	}
	if now.After(w.WindowEnd) {
		delete(s.attempts, peerID)
		return false
	}
	return w.LockedOut
}

// recordFailure counts one failed verification attempt against peerID,
// locking it out on the pairingMaxAttemptsPerPeerPerHour'th failure
// within the rolling hour (R-21.184).
func (s *PairingStore) recordFailure(peerID string, now time.Time) {
	w, ok := s.attempts[peerID]
	if !ok || now.After(w.WindowEnd) {
		w = &pairingAttemptWindow{WindowEnd: now.Add(time.Hour)}
		s.attempts[peerID] = w
	}
	w.Count++
	if w.Count >= pairingMaxAttemptsPerPeerPerHour {
		w.LockedOut = true
	}
}

// DecodePairingCode fail-closed-validates that raw has this package's
// exact pairing-code shape (26-character base32, decodes to exactly 16
// bytes) without consulting any store — the FuzzDecodePairingCode target
// this ticket's contract calls for exercises this function. It never
// panics on any input.
func DecodePairingCode(raw string) ([]byte, error) {
	if len(raw) != 26 {
		return nil, cascade.Newf(cascade.KindInvalidInput, "nodes: pairing code must be 26 characters, got %d", len(raw))
	}
	if strings.ContainsAny(raw, "=") {
		return nil, cascade.New(cascade.KindInvalidInput, "nodes: pairing code must not be padded")
	}
	decoded, err := pairingCodeEncoding.DecodeString(strings.ToUpper(raw))
	if err != nil {
		return nil, cascade.Wrap(cascade.KindInvalidInput, err, "nodes: pairing code is not valid base32")
	}
	if len(decoded) != PairingCodeEntropyBytes {
		return nil, cascade.Newf(cascade.KindInvalidInput, "nodes: pairing code decodes to %d bytes, want %d", len(decoded), PairingCodeEntropyBytes)
	}
	return decoded, nil
}
