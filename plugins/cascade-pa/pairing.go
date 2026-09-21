// Purpose: the shared one-use pairing-code verifier every bridge adapter
//   binds through (W/S-48.T1 owns it; W/S-49.T3's WhatsApp adapter is the
//   second consumer named in the contract, which is why nothing here is
//   Telegram-specific).
//
// Inputs: crypto/rand entropy (production) or an injected io.Reader
//   (tests) for issuance; a claimed code plus a subject id for
//   verification; the BridgeState store both halves share.
//
// Outputs: IssueCode returns the plaintext code ONCE, at issuance time.
//   VerifyAndConsume returns a PairOutcome the caller switches on: Bound,
//   Refused, or LockedOut (the attempt that reached the ceiling, which
//   also burns the outstanding code).
//
// Constraints: R-16.37 verbatim — 8-character Crockford base32 (I/L/O/U
//   dropped so no glyph is ambiguous when spoken or typed), 10-minute TTL,
//   one use, and FIVE wrong candidates against one outstanding code burn
//   that code and report a lockout.
//
//   DURABLE, AND ONLY AS A KEYED DIGEST. Issuance (the daemon's pa.pair_code
//   RPC) and verification ("/pair <code>" arriving on the poll loop) are
//   different call paths over one durable row, so the code's proof has to be
//   persisted. What is stored is HMAC-SHA256 of the code's canonical
//   (upper-cased) form under a key derived from the bridge's own bot token
//   (DerivePairCodeKey), never the code and never a bare hash of it.
//
//   WHY KEYED, NOT sha256. The code is 8 Crockford characters = 40 bits, so a
//   bare unsalted sha256 of it is recoverable by brute force in minutes on
//   one commodity GPU — well inside the 10-minute TTL. Read access to
//   cascade.db (a backup, a synced directory, one permissions slip) plus the
//   bot's public username would then be enough to pair yourself onto the
//   allowlist. R-16.37 pins the code's shape, so the answer is a key the
//   database does not contain rather than a longer code. The key is derived
//   from the token at module assembly and never persisted: a stolen database
//   alone cannot verify or recover a code, and a store built without a key
//   REFUSES both verbs instead of falling back to an unkeyed digest.
//
//   The wrong-attempt counter is durable for the same reason it exists — a
//   lockout a restart resets is not a lockout.
//
//   Comparison is constant-time (crypto/subtle) over the digests, so a
//   caller learns "matched" or "did not match", never how many leading
//   bytes agreed.
//
//   This is a DIFFERENT credential from internal/nodes/pairing.go's
//   LAN-discovery PairingStore (128-bit, per-peer-per-hour lockout, feeds
//   Q/S-36.T1 node enrollment). The contract states plainly that the
//   bridge binding "is NOT a node enrollment" and "never satisfies a
//   node-dispatch gate"; sharing a store or a constant set with the
//   node-pairing path would blur a boundary the contract draws on purpose.
//
// SPORT: plugins/cascade-pa PairCodeStore/ADDED, IssueCode/ADDED,
//   VerifyAndConsume/ADDED (P1-E23-W5-S48-T1).

package cascadepa

import (
	"context"
	"crypto/subtle"
	"encoding/base32"
	"io"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
)

// R-16.37 pairing constants, verbatim.
const (
	// PairCodeLength is the code's character length: 5 bytes of entropy at
	// 5 bits/char = 40 bits = exactly 8 Crockford base32 characters, no
	// padding.
	PairCodeLength = 8
	// PairCodeEntropyBytes backs PairCodeLength (5*8 == 8*5 bits).
	PairCodeEntropyBytes = 5
	// PairCodeTTL is the fixed lifetime of an issued code.
	PairCodeTTL = 10 * time.Minute
	// PairCodeMaxAttempts is the wrong-candidate count that burns the
	// outstanding code and reports a lockout on the attempt reaching it.
	PairCodeMaxAttempts = 5
)

// pairCodeEncoding is Crockford's base32 alphabet (RFC 4648 base32 is NOT
// this alphabet — Crockford drops I, L, O and U).
var pairCodeEncoding = base32.NewEncoding("0123456789ABCDEFGHJKMNPQRSTVWXYZ").WithPadding(base32.NoPadding)

// PairClock abstracts time.Now so this file never reads the wall clock
// directly (forbidigo), mirroring internal/nodes.Clock's identical shape.
type PairClock interface {
	Now() time.Time
}

// PairOutcome is VerifyAndConsume's closed three-way result.
type PairOutcome struct {
	// Bound reports an exact, unexpired, first-time match: the code is now
	// consumed and the caller may proceed to bind.
	Bound bool
	// LockedOut reports that THIS attempt reached PairCodeMaxAttempts: the
	// outstanding code is burned and the caller must emit the typed
	// lockout event.
	LockedOut bool
}

// Refused reports a failure that is neither a bind nor the lockout
// attempt: wrong code with attempts remaining, expired code, or no
// outstanding code at all. All of them render as one subject-facing
// "pairing failed"; this type does not distinguish them further on
// purpose, because a verifier that said WHY would leak whether a code had
// ever been issued.
func (o PairOutcome) Refused() bool { return !o.Bound && !o.LockedOut }

// PairCodeStore issues and verifies one outstanding code per subject. A
// second IssueCode for the same subject supersedes any prior pending code
// (R-21.227: a bridge subject has exactly one live code at a time).
type PairCodeStore struct {
	clock PairClock
	state BridgeState
	// key is the HMAC key every digest is computed under, derived from the
	// bridge's bot token by DerivePairCodeKey. An empty key is fail-closed:
	// both verbs refuse rather than computing an unkeyed digest.
	key []byte
	// locks is the per-subject mutex table; NewStores replaces it with the
	// one the whole bundle shares.
	locks *subjectLocks
}

// NewPairCodeStore constructs a store over clock, the durable state (nil ->
// fail-closed: every verb refuses) and the digest key derived from the
// bridge instance's own credential (DerivePairCodeKey). An empty key is a
// fail-closed store, never an unkeyed one.
func NewPairCodeStore(clock PairClock, state BridgeState, key []byte) *PairCodeStore {
	return &PairCodeStore{
		clock: clock, state: orUnconfigured(state),
		key: append([]byte(nil), key...), locks: newSubjectLocks(),
	}
}

// GenerateCode returns a fresh PairCodeLength-character Crockford code,
// reading entropy from rnd. It performs no store I/O, so a shape test can
// call it alone; IssueCode is the store-mutating wrapper production uses.
func GenerateCode(rnd io.Reader) (string, error) {
	raw := make([]byte, PairCodeEntropyBytes)
	if _, err := io.ReadFull(rnd, raw); err != nil {
		return "", cascade.Wrap(cascade.KindInternal, err, "cascade-pa: pair code entropy generation failed")
	}
	code := pairCodeEncoding.EncodeToString(raw)
	if len(code) != PairCodeLength {
		return "", cascade.Newf(cascade.KindInternal,
			"cascade-pa: generated pair code has length %d, want %d", len(code), PairCodeLength)
	}
	return code, nil
}

// IssueCode generates a fresh code for subject, records its digest as the
// sole outstanding code (resetting the attempt counter), and returns the
// plaintext once. subject must be non-empty — the bridge instance identity,
// e.g. a bot token digest, never the token itself.
func (s *PairCodeStore) IssueCode(ctx context.Context, rnd io.Reader, subject string) (string, error) {
	if subject == "" {
		return "", cascade.New(cascade.KindInvalidInput, "cascade-pa: pair code must be bound to a subject")
	}
	code, err := GenerateCode(rnd)
	if err != nil {
		return "", err
	}
	digest, err := CodeDigest(s.key, code)
	if err != nil {
		return "", err
	}
	return mutate(s.locks, subject, func() (string, error) { return s.recordCode(ctx, subject, code, digest) })
}

// recordCode is IssueCode's read-modify-write: it records digest as subject's
// sole outstanding code and returns the plaintext. Re-Loads on every attempt,
// so mutate's retry after a compare-and-swap refusal is a fresh write rather
// than a replay of a stale row.
func (s *PairCodeStore) recordCode(ctx context.Context, subject, code, digest string) (string, error) {
	st, _, err := s.state.Load(ctx, subject)
	if err != nil {
		return "", err
	}
	now := s.clock.Now()
	st.Subject = subject
	st.CodeDigest = digest
	st.CodeExpiresAt = now.Add(PairCodeTTL)
	st.WrongAttempts = 0
	if err := s.state.Save(ctx, st); err != nil {
		return "", err
	}
	return code, nil
}

// VerifyAndConsume checks candidate against subject's outstanding code and
// persists the resulting state change in the same call: a bind clears the
// code, a refusal increments the durable attempt counter, and the attempt
// that reaches the ceiling clears the code too.
func (s *PairCodeStore) VerifyAndConsume(ctx context.Context, subject, candidate string) (PairOutcome, error) {
	if subject == "" || candidate == "" {
		return PairOutcome{}, nil
	}
	if len(s.key) == 0 {
		return PairOutcome{}, ErrNoPairCodeKey
	}
	return mutate(s.locks, subject, func() (PairOutcome, error) { return s.verifyOnce(ctx, subject, candidate) })
}

// verifyOnce is VerifyAndConsume's read-modify-write. Held under the subject
// lock, so two goroutines racing one correct code cannot both observe it
// outstanding: the first consumes it, the second finds no code at all.
func (s *PairCodeStore) verifyOnce(ctx context.Context, subject, candidate string) (PairOutcome, error) {
	st, ok, err := s.state.Load(ctx, subject)
	if err != nil {
		return PairOutcome{}, err
	}
	if !ok || st.CodeDigest == "" {
		return PairOutcome{}, nil
	}
	st.Subject = subject
	matched, err := s.matches(st, candidate)
	if err != nil {
		return PairOutcome{}, err
	}
	if matched {
		st.CodeDigest, st.CodeExpiresAt, st.WrongAttempts = "", time.Time{}, 0
		if serr := s.state.Save(ctx, st); serr != nil {
			return PairOutcome{}, serr
		}
		return PairOutcome{Bound: true}, nil
	}
	return s.refuse(ctx, st)
}

// matches reports an exact, unexpired digest match, compared in constant
// time. A correct-but-expired code deliberately does NOT match: from the
// TTL boundary on it is a wrong credential, and it counts as an attempt.
func (s *PairCodeStore) matches(st SubjectState, candidate string) (bool, error) {
	if s.clock.Now().After(st.CodeExpiresAt) {
		return false, nil
	}
	digest, err := CodeDigest(s.key, candidate)
	if err != nil {
		return false, err
	}
	return subtle.ConstantTimeCompare([]byte(digest), []byte(st.CodeDigest)) == 1, nil
}

// refuse records one wrong attempt and returns Refused, or LockedOut on the
// attempt that reaches PairCodeMaxAttempts (burning the outstanding code).
func (s *PairCodeStore) refuse(ctx context.Context, st SubjectState) (PairOutcome, error) {
	st.WrongAttempts++
	locked := st.WrongAttempts >= PairCodeMaxAttempts
	if locked {
		st.CodeDigest, st.CodeExpiresAt = "", time.Time{}
	}
	if err := s.state.Save(ctx, st); err != nil {
		return PairOutcome{}, err
	}
	return PairOutcome{LockedOut: locked}, nil
}

// Pending reports whether subject currently has a live, unexpired code
// outstanding.
//
// HONEST SCOPE: no production caller reads this today. `cascade pa pair`
// deliberately does NOT ask before reissuing — a reissue supersedes whatever
// was outstanding, unconditionally, and telling the operator that a code was
// already live would be the same information a stranger must never learn from
// the bot. It exists because the supersession property has to be assertable
// from outside the package, and the adapters W/S-49.T3 adds are the callers
// positioned to surface it.
func (s *PairCodeStore) Pending(ctx context.Context, subject string) (bool, error) {
	st, ok, err := s.state.Load(ctx, subject)
	if err != nil {
		return false, err
	}
	if !ok || st.CodeDigest == "" {
		return false, nil
	}
	return !s.clock.Now().After(st.CodeExpiresAt), nil
}
